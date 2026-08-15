package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/rajpatil53/docktree/internal/compose"
	"github.com/rajpatil53/docktree/internal/config"
	"github.com/rajpatil53/docktree/internal/envfile"
	"github.com/rajpatil53/docktree/internal/gitignore"
	"github.com/rajpatil53/docktree/internal/identity"
	"github.com/rajpatil53/docktree/internal/paths"
	"github.com/rajpatil53/docktree/internal/ports"
	"github.com/rajpatil53/docktree/internal/registry"
	"github.com/rajpatil53/docktree/internal/runner"
	"github.com/rajpatil53/docktree/internal/stateful"
)

type commandDeps struct {
	runner     runner.Runner
	stdin      io.Reader
	stdout     io.Writer
	stderr     io.Writer
	cwd        func() (string, error)
	portFree   func(int) bool
	waitShared func(context.Context, *resolved) error
	event      func(string)
}

func defaultCommandDeps(stdin io.Reader, stdout, stderr io.Writer) commandDeps {
	deps := commandDeps{
		runner:   runner.Exec{},
		stdin:    stdin,
		stdout:   stdout,
		stderr:   stderr,
		cwd:      os.Getwd,
		portFree: ports.PortFree,
	}
	deps.waitShared = func(ctx context.Context, r *resolved) error {
		waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		return waitForSharedReadiness(waitCtx, r, deps, 250*time.Millisecond, nil)
	}
	return deps
}

func currentWorktreeRoot(deps commandDeps) (string, error) {
	wd, err := deps.cwd()
	if err != nil {
		return "", err
	}
	return normalizeWorktreeRoot(wd)
}

func normalizeWorktreeRoot(wd string) (string, error) {
	if wd == "" {
		return "", fmt.Errorf("empty working directory")
	}
	abs, err := filepath.Abs(wd)
	if err != nil {
		return "", err
	}
	if root, ok, err := findConfigRoot(abs); err != nil {
		return "", err
	} else if ok {
		return root, nil
	}
	if root, ok := gitTopLevel(abs); ok {
		return root, nil
	}
	return abs, nil
}

func findConfigRoot(wd string) (string, bool, error) {
	dir := wd
	for {
		if _, err := os.Stat(paths.ConfigFile(dir)); err == nil {
			return dir, true, nil
		} else if err != nil && !os.IsNotExist(err) {
			return "", false, err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false, nil
		}
		dir = parent
	}
}

func gitTopLevel(wd string) (string, bool) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = wd
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", false
	}
	return root, true
}

func (d commandDeps) emit(name string) {
	if d.event != nil {
		d.event(name)
	}
}

func prepareStack(ctx context.Context, wd string, deps commandDeps) (*resolved, *compose.Model, error) {
	deps.emit("resolve")
	m, err := config.LoadFile(paths.ConfigFile(wd))
	if err != nil {
		return nil, nil, err
	}
	base := runner.Command{
		Dir:    wd,
		Env:    os.Environ(),
		Stdin:  deps.stdin,
		Stdout: deps.stdout,
		Stderr: deps.stderr,
	}
	if err := runner.PreflightSecrets(ctx, deps.runner, base, m.Secrets.Wrapper, isInteractive(deps.stdin)); err != nil {
		return nil, nil, err
	}

	model, err := loadComposeModel(ctx, wd, m, deps)
	if err != nil {
		return nil, nil, err
	}
	r, err := resolveWithModel(wd, model, deps.portFree)
	if err != nil {
		return nil, nil, err
	}
	if err := validateComposeForRun(model, r, deps); err != nil {
		return nil, nil, err
	}
	if err := applyDerivedStatefulModes(ctx, r, deps); err != nil {
		return nil, nil, err
	}
	return r, model, nil
}

func validateComposeForRun(model *compose.Model, r *resolved, deps commandDeps) error {
	for _, issue := range compose.Validate(model, composeOptions(r)) {
		if issue.Severity == compose.Error {
			return fmt.Errorf("compose validation error: %s", issue.Message)
		}
		fmt.Fprintf(deps.stderr, "warning %s: %s\n", issue.Code, issue.Message)
	}
	return nil
}

func applyDerivedStatefulModes(ctx context.Context, r *resolved, deps commandDeps) error {
	if len(r.manifest.Stateful) == 0 {
		return nil
	}
	volumes, err := listForkVolumes(ctx, r.manifest.App, deps)
	if err != nil {
		return err
	}
	for _, volume := range volumes {
		if volume.App != r.manifest.App || volume.Slug != r.slug {
			continue
		}
		if _, ok := r.manifest.Stateful[volume.Service]; ok && volume.IsForkFor(volume.Service) {
			setStatefulMode(r, volume.Service, "isolated")
		}
	}
	return nil
}

func applyPostgresLogicalForkModes(ctx context.Context, r *resolved, deps commandDeps, strict bool) (bool, error) {
	changed := false
	for _, name := range sortedStatefulNames(r.manifest.Stateful) {
		st, err := renderedStatefulConfig(r, name)
		if err != nil {
			return false, err
		}
		if !stateful.UsePostgresFastPath(st.Engine) {
			continue
		}
		if r.slug == r.mainSlug {
			continue
		}
		if st.SourceDB == "" {
			if strict {
				return false, fmt.Errorf("stateful %s requires source_db for postgres forks", name)
			}
			continue
		}
		target := stateful.TargetDB(st.SourceDB, r.slug)
		exists, err := postgresDatabaseExists(ctx, r, deps, name, target)
		if err != nil {
			if strict {
				return false, err
			}
			continue
		}
		if exists {
			if statefulMode(r, name) != "isolated" {
				changed = true
			}
			setStatefulMode(r, name, "isolated")
			continue
		}
		if statefulMode(r, name) != "shared" {
			changed = true
			setStatefulMode(r, name, "shared")
		}
	}
	return changed, nil
}

func listForkVolumes(ctx context.Context, app string, deps commandDeps) ([]stateful.Volume, error) {
	data, err := deps.runner.Output(ctx, runner.Command{
		Argv:   []string{"docker", "volume", "ls", "--filter", "label=" + stateful.LabelApp + "=" + app, "--filter", "label=" + stateful.LabelFork, "--format", "json"},
		Env:    os.Environ(),
		Stdin:  deps.stdin,
		Stdout: deps.stdout,
		Stderr: deps.stderr,
	})
	if err != nil {
		return nil, err
	}
	return stateful.ParseVolumeListJSON(data)
}

func loadComposeModel(ctx context.Context, wd string, m *config.Config, deps commandDeps) (*compose.Model, error) {
	files, err := compose.DiscoverFiles(wd, environMap(os.Environ()), m.Compose)
	if err != nil {
		return nil, err
	}
	command := runner.Command{
		Argv:   compose.ConfigCommandArgv(files),
		Dir:    wd,
		Env:    os.Environ(),
		Stdin:  deps.stdin,
		Stdout: deps.stdout,
		Stderr: deps.stderr,
	}
	data, err := deps.runner.Output(ctx, runner.WithSecretsWrapper(m.Secrets.Wrapper, command))
	if err != nil {
		return nil, err
	}
	return compose.ParseConfigJSON(data)
}

func resolveWithModel(wd string, model *compose.Model, free func(int) bool) (*resolved, error) {
	r, err := resolveWithFree(wd, free)
	if err != nil {
		return nil, err
	}
	if r.manifest.Services == nil {
		r.manifest.Services = map[string]config.Service{}
	}
	for _, serviceName := range sortedComposeServiceNames(model.Services) {
		if _, exists := r.manifest.Services[serviceName]; !exists {
			r.manifest.Services[serviceName] = config.Service{Autostart: true}
		}
	}
	inputs := discoveredPortInputs(r.manifest, model)
	if len(inputs) == 0 {
		return r, nil
	}
	for _, input := range inputs {
		svc, exists := r.manifest.Services[input.Service]
		if !exists {
			svc.Autostart = true
		}
		if svc.HostPortEnv == "" {
			svc.HostPortEnv = input.Env
		}
		r.manifest.Services[input.Service] = svc
	}
	// Proxied HTTP services are reached over the proxy hostname, not a published
	// host port, so render drops their `ports`. Skip allocating (and persisting)
	// a host port for them: it would reserve a registry band for a port nothing
	// binds and make doctor report false port drift.
	r.proxied = map[string]bool{}
	if r.manifest.Proxy.Enabled {
		opts := composeOptions(r)
		kept := inputs[:0]
		for _, input := range inputs {
			if compose.ServiceProxied(opts, input.Service, model.Services[input.Service]) {
				r.proxied[input.Service] = true
				continue
			}
			kept = append(kept, input)
		}
		inputs = kept
	}
	existingPorts, err := persistedPortEnv(r.wd, r.names.Project)
	if err != nil {
		return nil, err
	}
	worktree, shared, primaryDefaults, err := resolvePortInputs(registryPath(), r.manifest.App, r.slug, r.mainSlug, inputs, free, existingPorts)
	if err != nil {
		return nil, err
	}
	if len(worktree) > 0 {
		r.servicePorts = worktree
	}
	if len(shared) > 0 {
		r.sharedPorts = shared
	}
	r.primaryDefaultPorts = primaryDefaults
	// resolveWithFree already seeded servicePorts (config-only); drop proxied
	// services so the env artifact and doctor drift never reference a host port
	// that render intentionally did not publish.
	for name := range r.proxied {
		delete(r.servicePorts, name)
	}
	return r, nil
}

func ensureConfiguredIsolatedStateful(ctx context.Context, r *resolved, model *compose.Model, deps commandDeps) error {
	changed := false
	for _, service := range sortedStatefulNames(r.manifest.Stateful) {
		st, err := renderedStatefulConfig(r, service)
		if err != nil {
			return err
		}
		if st.DefaultStrategy != "isolated" || statefulMode(r, service) == "isolated" {
			continue
		}
		if r.slug == r.mainSlug {
			return fmt.Errorf("refusing to fork main worktree %q", r.slug)
		}
		if stateful.UsePostgresFastPath(st.Engine) {
			if err := forkPostgres(ctx, r, service, st, deps); err != nil {
				return err
			}
		} else {
			if err := forkGenericVolume(ctx, r, service, st, deps); err != nil {
				return err
			}
		}
		setStatefulMode(r, service, "isolated")
		changed = true
	}
	if !changed {
		return nil
	}
	return writeStackArtifacts(r, model, deps)
}

func writeStackArtifacts(r *resolved, model *compose.Model, deps commandDeps) error {
	return writeStackArtifactsWithModes(r, model, deps, nil)
}

func writeStackArtifactsWithModes(r *resolved, model *compose.Model, deps commandDeps, modes map[string]string) error {
	for service, mode := range modes {
		setStatefulMode(r, service, mode)
	}
	if _, err := gitignore.EnsureDocktree(r.wd); err != nil {
		return err
	}
	deps.emit("write-env")
	artifact, err := envArtifact(r, model)
	if err != nil {
		return err
	}
	if err := envfile.WriteArtifact(paths.EnvFile(r.wd), artifact); err != nil {
		return err
	}
	if err := envfile.WriteArtifact(paths.InfraEnvFile(r.wd), infraEnvArtifact(r)); err != nil {
		return err
	}

	deps.emit("render-projections")
	opts := composeOptions(r)
	infraYAML, err := compose.RenderInfra(model, opts)
	if err != nil {
		return err
	}
	worktreeYAML, err := compose.RenderWorktree(model, opts)
	if err != nil {
		return err
	}
	if err := writeAtomic(paths.InfraCompose(r.wd), infraYAML); err != nil {
		return err
	}
	return writeAtomic(paths.WorktreeCompose(r.wd), worktreeYAML)
}

func composeOptions(r *resolved) compose.Options {
	services := map[string]compose.ServiceOptions{}
	for name, svc := range r.manifest.Services {
		services[name] = compose.ServiceOptions{
			HostPortEnv:       svc.HostPortEnv,
			Fixed:             svc.Fixed,
			Autostart:         svc.Autostart,
			AutostartExplicit: !svc.Autostart,
			Expose:            svc.Expose,
			ProxyPort:         svc.ProxyPort,
		}
	}
	forked := map[string]compose.ForkOptions{}
	statefulModes := map[string]string{}
	for name := range r.manifest.Stateful {
		mode := statefulMode(r, name)
		statefulModes[name] = mode
		if mode == "isolated" {
			st, err := renderedStatefulConfig(r, name)
			if err == nil && stateful.UsePostgresFastPath(st.Engine) {
				continue
			}
			forked[name] = compose.ForkOptions{
				VolumeName:   stateful.VolumeName(r.manifest.App, r.slug, name),
				SourceVolume: stateful.SnapshotSource(r.manifest.App, name, st.SnapshotSource),
			}
		}
	}
	return compose.Options{
		App:              r.manifest.App,
		Slug:             r.slug,
		MainSlug:         r.mainSlug,
		Project:          r.names.Project,
		InfraProject:     r.names.InfraProject,
		SharedNetwork:    r.names.SharedNetwork,
		GeneratedEnvFile: paths.EnvFileName,
		Shared:           r.manifest.Shared,
		Services:         services,
		Forked:           forked,
		StatefulModes:    statefulModes,
		Proxy: compose.ProxyOptions{
			Enabled:   r.manifest.Proxy.Enabled,
			DNSSuffix: r.manifest.Proxy.DNSSuffix,
			HTTPSPort: r.manifest.Proxy.HTTPSPort,
		},
	}
}

// recreateWorktreeStack converges the worktree stack after an artifact
// rewrite. --remove-orphans matters: services that vanished from the artifact
// (a fork's dropped uplink, an unfork's dropped local copy) would otherwise
// keep running with a canonical alias and reintroduce dual-DNS resolution.
func recreateWorktreeStack(ctx context.Context, r *resolved, deps commandDeps) error {
	return deps.runner.Run(ctx, worktreeComposeCommand(r, deps, "up", "-d", "--remove-orphans", "--force-recreate"))
}

func ensureSharedNetwork(ctx context.Context, r *resolved, deps commandDeps) error {
	deps.emit("create-shared-network")
	inspect := runner.Command{
		Argv:   []string{"docker", "network", "inspect", r.names.SharedNetwork},
		Dir:    r.wd,
		Env:    os.Environ(),
		Stdout: io.Discard,
		Stderr: io.Discard,
	}
	if err := deps.runner.Run(ctx, inspect); err == nil {
		return nil
	}
	create := runner.Command{
		Argv:   []string{"docker", "network", "create", r.names.SharedNetwork},
		Dir:    r.wd,
		Env:    os.Environ(),
		Stdout: deps.stdout,
		Stderr: deps.stderr,
	}
	return deps.runner.Run(ctx, create)
}

func ensureInfra(ctx context.Context, r *resolved, deps commandDeps) error {
	if len(r.manifest.Shared) == 0 {
		return nil
	}
	deps.emit("start-infra")
	return deps.runner.Run(ctx, infraComposeCommand(r, deps, "up", "-d"))
}

func infraRunning(ctx context.Context, r *resolved, deps commandDeps) (bool, error) {
	out, err := deps.runner.Output(ctx, infraComposeCommand(r, deps, "ps", "--status", "running", "--services"))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}

func infraComposeCommand(r *resolved, deps commandDeps, action string, args ...string) runner.Command {
	return composeCommand(r, deps, r.names.InfraProject, paths.InfraEnvFile(r.wd), paths.InfraCompose(r.wd), action, args...)
}

func worktreeComposeCommand(r *resolved, deps commandDeps, action string, args ...string) runner.Command {
	return composeCommand(r, deps, r.names.Project, paths.EnvFile(r.wd), paths.WorktreeCompose(r.wd), action, args...)
}

func composeCommand(r *resolved, deps commandDeps, project, envFile, composeFile, action string, args ...string) runner.Command {
	command := runner.Command{
		Argv:   runner.ComposeArgv(project, envFile, composeFile, action, args...),
		Dir:    r.wd,
		Env:    os.Environ(),
		Stdin:  deps.stdin,
		Stdout: deps.stdout,
		Stderr: deps.stderr,
	}
	wrapper := r.manifest.Secrets.Wrapper
	command = runner.WithEnvOverrides(wrapper, artifactEnvOverrides(envFile), command)
	return runner.WithSecretsWrapper(wrapper, command)
}

// artifactEnvOverrides loads a generated env artifact as override pairs so
// docktree-owned values win over the ambient shell and wrapper-injected env.
// Reading at command-build time matters: up rewrites artifacts mid-run (bind
// retry, stateful mode changes) before re-running compose. Read errors
// degrade to no overrides rather than failing command construction — compose
// hard-fails on the same --env-file path, so a missing or unreadable
// artifact cannot silently run without its values.
func artifactEnvOverrides(envFile string) [][2]string {
	values, err := envfile.Read(envFile)
	if err != nil || len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	overrides := make([][2]string, 0, len(keys))
	for _, key := range keys {
		overrides = append(overrides, [2]string{key, values[key]})
	}
	return overrides
}

func withAppCriticalSection(wd string, deps commandDeps, fn func() error) error {
	m, err := config.LoadFile(paths.ConfigFile(wd))
	if err != nil {
		return err
	}
	lockPath := registry.LockPath(registryPath(), m.App+".up")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return fn()
}

func seedEnvFile(wd string) error {
	dst := filepath.Join(wd, ".env")
	if _, err := os.Stat(dst); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	src := filepath.Join(wd, ".env.example")
	data, err := os.ReadFile(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return writeAtomic(dst, data)
}

func scaffoldConfig(wd string) error {
	path := paths.ConfigFile(wd)
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	app := identity.AppName("", wd)
	return writeAtomic(path, []byte(fmt.Sprintf("app = %q\n", app)))
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func environMap(env []string) map[string]string {
	out := map[string]string{}
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			out[key] = value
		}
	}
	return out
}

func isInteractive(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	stat, err := f.Stat()
	return err == nil && stat.Mode()&os.ModeCharDevice != 0
}
