package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/rajpatil53/docktree/internal/compose"
	"github.com/rajpatil53/docktree/internal/config"
	"github.com/rajpatil53/docktree/internal/paths"
	"github.com/rajpatil53/docktree/internal/registry"
	"github.com/rajpatil53/docktree/internal/runner"
)

// proxyProject is the machine-global compose project name for the single Caddy
// reverse proxy that fronts every app/worktree HTTP service.
const proxyProject = "docktree-proxy"

func Proxy(args []string) error {
	return runProxy(context.Background(), args, defaultCommandDeps(os.Stdin, os.Stdout, os.Stderr))
}

func runProxy(ctx context.Context, args []string, deps commandDeps) error {
	action := "status"
	if len(args) > 0 {
		action = args[0]
	}
	switch action {
	case "up":
		wd, err := currentWorktreeRoot(deps)
		if err != nil {
			return err
		}
		m, err := config.LoadFile(paths.ConfigFile(wd))
		if err != nil {
			return err
		}
		return ensureProxy(ctx, deps, m.Proxy, nil)
	case "down":
		return deps.runner.Run(ctx, runner.Command{
			Argv:   []string{"docker", "compose", "-p", proxyProject, "down"},
			Env:    os.Environ(),
			Stdout: deps.stdout,
			Stderr: deps.stderr,
		})
	case "status":
		return deps.runner.Run(ctx, runner.Command{
			Argv:   []string{"docker", "compose", "-p", proxyProject, "ps"},
			Env:    os.Environ(),
			Stdout: deps.stdout,
			Stderr: deps.stderr,
		})
	default:
		return fmt.Errorf("usage: docktree proxy up|down|status")
	}
}

// ensureProxyForUp brings the machine-global reverse proxy (and its network) up
// during `docktree up`, so the docktree_proxy external network exists before the
// worktree stack — which joins it — starts.
func ensureProxyForUp(ctx context.Context, r *resolved, deps commandDeps) error {
	emitUpPhase(deps, "ensuring reverse proxy %s", proxyProject)
	return ensureProxy(ctx, deps, r.manifest.Proxy, appProxyNetworks(r))
}

// appProxyNetworks lists the per-project proxy networks the resolved stack's
// HTTP-exposed services attach to: always the worktree project's, plus the
// infra project's when a shared service opts into HTTP exposure (expose="http").
func appProxyNetworks(r *resolved) []string {
	nets := []string{compose.ProxyNetworkName(r.names.Project)}
	for _, name := range r.manifest.Shared {
		if r.manifest.Services[name].Expose == "http" {
			nets = append(nets, compose.ProxyNetworkName(r.names.InfraProject))
			break
		}
	}
	return nets
}

// ensureProxy converges the singleton reverse proxy: it ensures the base proxy
// network and this stack's per-project proxy networks exist, then regenerates
// and brings up the proxy compose attached to every discovered proxy network so
// Caddy can reach backends across all of them. The whole section is serialized
// by a machine-global flock so concurrent `up`s across apps do not race.
func ensureProxy(ctx context.Context, deps commandDeps, p config.Proxy, appNets []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	return withProxyCriticalSection(func() error {
		if err := ensureProxyNetwork(ctx, deps, compose.ProxyNetworkKey); err != nil {
			return err
		}
		for _, net := range appNets {
			if err := ensureProxyNetwork(ctx, deps, net); err != nil {
				return err
			}
		}
		nets := discoverProxyNetworks(ctx, deps, appNets)
		if err := writeProxyCompose(home, p, nets); err != nil {
			return err
		}
		deps.emit("start-proxy")
		return deps.runner.Run(ctx, runner.Command{
			Argv:   proxyComposeArgv(home, "up", "-d"),
			Env:    os.Environ(),
			Stdout: deps.stdout,
			Stderr: deps.stderr,
		})
	})
}

// ensureProxyNetwork creates a docktree-managed proxy network if it does not yet
// exist. Per-project proxy networks carry the ProxyNetworkLabel so the proxy
// convergence can discover every one Caddy must attach to; the base network is
// kept label-free for backward compatibility and unioned in explicitly.
func ensureProxyNetwork(ctx context.Context, deps commandDeps, name string) error {
	deps.emit("create-proxy-network")
	inspect := runner.Command{
		Argv:   []string{"docker", "network", "inspect", name},
		Env:    os.Environ(),
		Stdout: io.Discard,
		Stderr: io.Discard,
	}
	if err := deps.runner.Run(ctx, inspect); err == nil {
		return nil
	}
	argv := []string{"docker", "network", "create"}
	if name != compose.ProxyNetworkKey {
		argv = append(argv, "--label", compose.ProxyNetworkLabel+"=true", "--label", compose.LabelManaged+"=true")
	}
	argv = append(argv, name)
	return deps.runner.Run(ctx, runner.Command{
		Argv:   argv,
		Env:    os.Environ(),
		Stdout: deps.stdout,
		Stderr: deps.stderr,
	})
}

// discoverProxyNetworks returns every proxy network Caddy must attach to: the
// base network, this stack's own per-project networks, and every other
// docktree-managed proxy network found on the host. Discovery failures degrade
// gracefully to the known set so a flaky `docker network ls` never blocks `up`.
func discoverProxyNetworks(ctx context.Context, deps commandDeps, appNets []string) []string {
	set := map[string]bool{compose.ProxyNetworkKey: true}
	for _, net := range appNets {
		set[net] = true
	}
	out, err := deps.runner.Output(ctx, runner.Command{
		Argv: []string{"docker", "network", "ls", "--filter", "label=" + compose.ProxyNetworkLabel + "=true", "--format", "{{.Name}}"},
		Env:  os.Environ(),
	})
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if name := strings.TrimSpace(line); name != "" {
				set[name] = true
			}
		}
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func writeProxyCompose(home string, p config.Proxy, networks []string) error {
	yamlBytes, err := compose.RenderProxy(compose.ProxyRenderOptions{
		Networks:  networks,
		HTTPPort:  p.HTTPPort,
		HTTPSPort: p.HTTPSPort,
	})
	if err != nil {
		return err
	}
	return writeAtomic(paths.ProxyCompose(home), yamlBytes)
}

func proxyComposeArgv(home, action string, args ...string) []string {
	argv := []string{"docker", "compose"}
	if action == "up" {
		argv = append(argv, "--progress", "plain")
	}
	argv = append(argv, "-p", proxyProject, "-f", paths.ProxyCompose(home), action)
	return append(argv, args...)
}

// proxyURLForService returns the stable hostname URL for a proxied service.
//
//   - proxy disabled, or the service is genuinely not routed → ("", false, nil):
//     the caller falls back to the published-port URL (correct, since
//     non-proxied services keep their host port).
//   - proxy enabled but the compose model fails to load → ("", false, error):
//     surfaced, NOT swallowed. Under proxy mode a routed service has no published
//     port, so the published-port fallback would print a dead localhost URL; the
//     user needs the real error instead.
//
// It loads the model so the routed/not-routed decision matches render exactly
// (compose.ServiceProxied).
func proxyURLForService(ctx context.Context, r *resolved, service string, deps commandDeps) (string, bool, error) {
	if !r.manifest.Proxy.Enabled {
		return "", false, nil
	}
	model, err := loadComposeModel(ctx, r.wd, r.manifest, deps)
	if err != nil {
		return "", false, fmt.Errorf("cannot resolve proxy route for %q: %w", service, err)
	}
	opts := composeOptions(r)
	target := service
	if target == "" {
		for _, name := range sortedModelServices(model) {
			if compose.ServiceProxied(opts, name, model.Services[name]) {
				target = name
				break
			}
		}
		if target == "" {
			return "", false, nil
		}
	}
	svc, ok := model.Services[target]
	if !ok || !compose.ServiceProxied(opts, target, svc) {
		return "", false, nil
	}
	return compose.ProxyURL(opts, target), true, nil
}

func sortedModelServices(model *compose.Model) []string {
	names := make([]string, 0, len(model.Services))
	for name := range model.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func withProxyCriticalSection(fn func() error) error {
	lockPath := registry.LockPath(registryPath(), "proxy")
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
