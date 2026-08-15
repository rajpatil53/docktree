package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rajpatil53/docktree/internal/compose"
	"github.com/rajpatil53/docktree/internal/config"
	"github.com/rajpatil53/docktree/internal/dbmode"
	"github.com/rajpatil53/docktree/internal/dockerstate"
	"github.com/rajpatil53/docktree/internal/identity"
	"github.com/rajpatil53/docktree/internal/paths"
	"github.com/rajpatil53/docktree/internal/runner"
)

func loadRuntimeStacks(ctx context.Context, r *resolved, deps commandDeps) ([]dockerstate.Stack, error) {
	lsOut, err := deps.runner.Output(ctx, runnerCommand(r, deps, []string{"docker", "compose", "ls", "--all", "--format", "json"}))
	if err != nil {
		return nil, err
	}
	stacks, err := dockerstate.ParseComposeLSJSON(lsOut)
	if err != nil {
		return nil, err
	}
	psOut, err := deps.runner.Output(ctx, runnerCommand(r, deps, []string{
		"docker", "ps", "-a",
		"--filter", "label=com.docktree.app=" + r.manifest.App,
		"--format", "json",
	}))
	if err != nil {
		return nil, err
	}
	services, err := dockerstate.ParseDockerPSJSON(psOut)
	if err != nil {
		return nil, err
	}
	return filterRuntimeStacks(r, dockerstate.Merge(stacks, services)), nil
}

func filterRuntimeStacks(r *resolved, stacks []dockerstate.Stack) []dockerstate.Stack {
	out := make([]dockerstate.Stack, 0, len(stacks))
	for _, stack := range stacks {
		if stack.App != "" && stack.App != r.manifest.App {
			continue
		}
		switch {
		case stack.Project == r.names.InfraProject:
			stack.App = r.manifest.App
			stack.Slug = r.mainSlug
		case stack.Project == r.names.Project:
			stack.App = r.manifest.App
			stack.Slug = r.slug
		case stack.App == r.manifest.App:
		case strings.HasPrefix(stack.Project, r.manifest.App+"-"):
			stack.App = r.manifest.App
			if stack.Slug == "" {
				stack.Slug = strings.TrimPrefix(stack.Project, r.manifest.App+"-")
			}
		default:
			continue
		}
		out = append(out, stack)
	}
	return out
}

func resolveTargetArgs(ctx context.Context, args []string, deps commandDeps) (*resolved, []string, error) {
	r, err := resolveForCurrentDir(deps)
	if err != nil {
		return nil, nil, err
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") || args[0] == "--" || serviceKnown(r, args[0]) {
		return r, args, nil
	}
	if targetRoot, ok, err := resolveTargetRoot(r.wd, args[0]); err != nil {
		return nil, nil, err
	} else if ok {
		retargeted, err := resolveTargetRootStack(ctx, targetRoot, deps)
		if err != nil {
			return nil, nil, err
		}
		return retargeted, args[1:], nil
	}
	model, err := loadComposeModel(ctx, r.wd, r.manifest, deps)
	if err == nil {
		enriched, enrichErr := resolveWithModel(r.wd, model, deps.portFree)
		if enrichErr != nil {
			return nil, nil, enrichErr
		}
		r = enriched
		if serviceKnown(r, args[0]) {
			return r, args, nil
		}
	} else if !errors.Is(err, compose.ErrNoComposeFile) {
		return nil, nil, err
	}
	retargeted, err := retargetResolved(r, identity.ComputeSlug(args[0]), deps.portFree)
	if err != nil {
		return nil, nil, err
	}
	return retargeted, args[1:], nil
}

func resolveTargetRoot(baseWD, arg string) (string, bool, error) {
	candidate := arg
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(baseWD, arg)
	}
	info, err := os.Stat(candidate)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	if !info.IsDir() {
		return "", false, fmt.Errorf("target worktree %q is not a directory", arg)
	}
	if _, err := os.Stat(paths.ConfigFile(candidate)); err != nil {
		if os.IsNotExist(err) && !filepath.IsAbs(arg) && !strings.ContainsAny(arg, `/\`) {
			return "", false, nil
		}
		return "", false, err
	}
	return candidate, true, nil
}

func resolveTargetRootStack(ctx context.Context, targetRoot string, deps commandDeps) (*resolved, error) {
	cfg, err := config.LoadFile(paths.ConfigFile(targetRoot))
	if err != nil {
		return nil, err
	}
	model, err := loadComposeModel(ctx, targetRoot, cfg, deps)
	if err != nil {
		return nil, err
	}
	return resolveWithModel(targetRoot, model, deps.portFree)
}

func retargetResolved(r *resolved, slug string, free func(int) bool) (*resolved, error) {
	next := *r
	next.slug = slug
	next.names = identity.Derive(r.manifest.App, slug)
	next.ownDevDB = dbNameWithMain(r.manifest, slug, r.mainSlug)
	next.mainDevDB = dbNameWithMain(r.manifest, r.mainSlug, r.mainSlug)
	next.testDB = next.ownDevDB + r.manifest.DB.TestSuffix
	next.devDB = dbmode.EffectiveDevDB(next.mode, next.ownDevDB, next.mainDevDB)
	next.names.DevDB = next.devDB
	next.names.TestDB = next.testDB
	next.primaryDefaultPorts = nil
	servicePorts, err := resolveServicePortsWithFree(r.manifest, slug, free)
	if err != nil {
		return nil, err
	}
	sharedPorts, err := resolveSharedPortsWithFree(r.manifest, free)
	if err != nil {
		return nil, err
	}
	next.servicePorts = servicePorts
	next.sharedPorts = sharedPorts
	return &next, nil
}

func serviceKnown(r *resolved, name string) bool {
	if _, ok := r.manifest.Services[name]; ok {
		return true
	}
	if _, ok := r.manifest.Stateful[name]; ok {
		return true
	}
	for _, shared := range r.manifest.Shared {
		if shared == name {
			return true
		}
	}
	return false
}

func resolvedURL(r *resolved, service string) (string, bool) {
	for _, name := range sortedServiceNames(r.manifest.Services) {
		if service != "" && name != service {
			continue
		}
		if port, ok := r.servicePorts[name]; ok {
			return fmt.Sprintf("http://localhost:%d", port), true
		}
		if port, ok := r.sharedPorts[name]; ok {
			return fmt.Sprintf("http://localhost:%d", port), true
		}
	}
	return "", false
}

func composePSState(ctx context.Context, r *resolved, deps commandDeps, infra bool) ([]dockerstate.Service, error) {
	command := worktreeComposeCommand(r, deps, "ps", "--format", "json")
	identity := dockerstate.Identity{App: r.manifest.App, Slug: r.slug, Project: r.names.Project}
	if infra {
		command = infraComposeCommand(r, deps, "ps", "--format", "json")
		identity = dockerstate.Identity{App: r.manifest.App, Slug: r.mainSlug, Project: r.names.InfraProject}
	}
	data, err := deps.runner.Output(ctx, command)
	if err != nil {
		return nil, err
	}
	return dockerstate.ParseComposePSJSON(data, identity)
}

func dockerInfoCommand(r *resolved, deps commandDeps) runner.Command {
	return runner.Command{
		Argv:   []string{"docker", "info", "--format", "{{json .ServerVersion}}"},
		Dir:    r.wd,
		Env:    os.Environ(),
		Stdin:  deps.stdin,
		Stdout: deps.stdout,
		Stderr: deps.stderr,
	}
}
