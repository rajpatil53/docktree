// Package cmd holds docktree subcommands.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/rajpatil53/docktree/internal/compose"
	"github.com/rajpatil53/docktree/internal/config"
	"github.com/rajpatil53/docktree/internal/envfile"
	"github.com/rajpatil53/docktree/internal/gitignore"
	"github.com/rajpatil53/docktree/internal/paths"
)

func Init(args []string) error {
	return runInit(context.Background(), args, defaultCommandDeps(os.Stdin, os.Stdout, os.Stderr))
}

func runInit(ctx context.Context, args []string, deps commandDeps) error {
	seedEnv := false
	for _, arg := range args {
		switch arg {
		case "--seed-env":
			seedEnv = true
		default:
			return fmt.Errorf("usage: docktree init [--seed-env]")
		}
	}
	wd, err := currentWorktreeRoot(deps)
	if err != nil {
		return err
	}
	if err := scaffoldConfig(wd); err != nil {
		return err
	}
	if err := os.MkdirAll(paths.WorktreeDir(wd), 0o755); err != nil {
		return err
	}
	if seedEnv {
		if err := seedEnvFile(wd); err != nil {
			return err
		}
	}
	r, err := resolveWithFree(wd, deps.portFree)
	if err != nil {
		return err
	}
	cfg, err := config.LoadFile(paths.ConfigFile(wd))
	if err != nil {
		return err
	}
	var model *compose.Model
	if loaded, modelErr := loadComposeModel(ctx, wd, cfg, deps); modelErr == nil {
		model = loaded
		if err := appendDiscoveredServiceConfig(wd, cfg, model); err != nil {
			return err
		}
		r, err = resolveWithModel(wd, model, deps.portFree)
		if err != nil {
			return err
		}
	} else if !errors.Is(modelErr, compose.ErrNoComposeFile) {
		return modelErr
	}
	if _, err := gitignore.EnsureDocktree(wd); err != nil {
		return err
	}
	artifact, err := envArtifact(r, model)
	if err != nil {
		return err
	}
	if err := envfile.WriteArtifact(paths.EnvFile(wd), artifact); err != nil {
		return err
	}
	if err := ensureSharedNetwork(ctx, r, deps); err != nil {
		return err
	}
	fmt.Fprintf(deps.stdout, "docktree init: slug=%s project=%s env=%s\n", r.slug, r.names.Project, paths.EnvFile(wd))
	return nil
}

func appendDiscoveredServiceConfig(wd string, cfg *config.Config, model *compose.Model) error {
	inputs := discoveredPortInputs(cfg, model)
	var b strings.Builder
	for _, input := range inputs {
		if _, exists := cfg.Services[input.Service]; exists || input.Env == "" {
			continue
		}
		fmt.Fprintf(&b, "\n[services.%q]\nhost_port_env = %q\n", input.Service, input.Env)
	}
	if b.Len() == 0 {
		return nil
	}
	f, err := os.OpenFile(paths.ConfigFile(wd), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(b.String())
	return err
}

func envArtifact(r *resolved, model *compose.Model) (envfile.Artifact, error) {
	portEnv := map[string]int{}
	for _, name := range sortedServiceNames(r.manifest.Services) {
		envKey := r.manifest.Services[name].HostPortEnv
		if envKey == "" {
			continue
		}
		if port, ok := r.sharedPorts[name]; ok {
			portEnv[envKey] = port
		} else if port, ok := r.servicePorts[name]; ok {
			portEnv[envKey] = port
		}
	}

	rendered := r.manifest.RenderTemplates(config.TemplateContext{App: r.manifest.App, Slug: r.slug, MainSlug: r.mainSlug})
	env := map[string]string{}
	for key, value := range rendered.Env {
		env[key] = value
	}
	statefulKeys := map[string]string{}
	for _, name := range sortedStatefulNames(rendered.Stateful) {
		if statefulMode(r, name) != "isolated" {
			continue
		}
		for key, value := range rendered.Stateful[name].Env {
			if existing, ok := statefulKeys[key]; ok {
				return envfile.Artifact{}, fmt.Errorf("duplicate stateful env key %s for services %s and %s", key, existing, name)
			}
			statefulKeys[key] = name
			env[key] = value
		}
	}

	if model != nil && r.manifest.Proxy.Enabled {
		opts := composeOptions(r)
		for _, name := range sortedServiceNames(r.manifest.Services) {
			envKey := r.manifest.Services[name].PublicURLEnv
			if envKey == "" {
				continue
			}
			svc, ok := model.Services[name]
			if !ok || !compose.ServiceProxied(opts, name, svc) {
				continue
			}
			if existing, ok := env[envKey]; ok {
				return envfile.Artifact{}, fmt.Errorf("public_url_env %q for service %s collides with an existing env value %q", envKey, name, existing)
			}
			env[envKey] = compose.ProxyURL(opts, name)
		}
	}

	return envfile.Artifact{
		ProjectName:     r.names.Project,
		SharedNetwork:   r.names.SharedNetwork,
		WorktreeNetwork: worktreeNetworkName(r, model),
		Ports:           portEnv,
		Env:             env,
	}, nil
}

// worktreeNetworkName resolves the actual default-network name the worktree
// services join: compose's <project>_default unless the user compose names
// the default network explicitly.
func worktreeNetworkName(r *resolved, model *compose.Model) string {
	if model != nil {
		if network, ok := model.Networks["default"]; ok && network.Name != "" {
			return network.Name
		}
	}
	return r.names.Project + "_default"
}

// infraEnvArtifact renders the worktree-invariant env artifact consumed by
// infra services: stable infra project name, registry-backed shared ports,
// and [env] templated with the MAIN slug. No per-worktree ports, no worktree
// network, no stateful overrides — feeding worktree-varying values to infra
// services would change their compose config hash on every cross-worktree up
// and recreate the shared databases.
func infraEnvArtifact(r *resolved) envfile.Artifact {
	portEnv := map[string]int{}
	for _, name := range sortedServiceNames(r.manifest.Services) {
		envKey := r.manifest.Services[name].HostPortEnv
		if envKey == "" {
			continue
		}
		if port, ok := r.sharedPorts[name]; ok {
			portEnv[envKey] = port
		}
	}
	rendered := r.manifest.RenderTemplates(config.TemplateContext{App: r.manifest.App, Slug: r.mainSlug, MainSlug: r.mainSlug})
	env := map[string]string{}
	for key, value := range rendered.Env {
		env[key] = value
	}
	return envfile.Artifact{
		ProjectName:   r.names.InfraProject,
		SharedNetwork: r.names.SharedNetwork,
		Ports:         portEnv,
		Env:           env,
	}
}

func sortedStatefulNames(m map[string]config.Stateful) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// simple insertion sort; maps are tiny (a handful of env keys)
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}
