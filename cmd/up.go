package cmd

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/rajpatil53/docktree/internal/compose"
	"github.com/rajpatil53/docktree/internal/ports"
	"github.com/rajpatil53/docktree/internal/registry"
)

func Up(args []string) error {
	return runUp(context.Background(), args, defaultCommandDeps(os.Stdin, os.Stdout, os.Stderr))
}

func runUp(ctx context.Context, args []string, deps commandDeps) error {
	wd, err := currentWorktreeRoot(deps)
	if err != nil {
		return err
	}
	return withAppCriticalSection(wd, deps, func() error {
		emitUpPhase(deps, "preparing stack artifacts")
		r, model, err := prepareStack(ctx, wd, deps)
		if err != nil {
			return err
		}
		if err := writeStackArtifacts(r, model, deps); err != nil {
			return err
		}
		emitUpPhase(deps, "ensuring shared network %s", r.names.SharedNetwork)
		if err := ensureSharedNetwork(ctx, r, deps); err != nil {
			return err
		}
		if r.manifest.Proxy.Enabled {
			if err := ensureProxyForUp(ctx, r, deps); err != nil {
				return err
			}
		}
		if err := ensureInfraForUp(ctx, r, deps); err != nil {
			return err
		}
		if len(r.manifest.Shared) > 0 {
			emitUpPhase(deps, "waiting for shared services")
			if err := deps.waitShared(ctx, r); err != nil {
				return err
			}
		}
		changed, err := applyPostgresLogicalForkModes(ctx, r, deps, false)
		if err != nil {
			return err
		}
		if changed {
			if err := writeStackArtifacts(r, model, deps); err != nil {
				return err
			}
		}
		if err := ensureConfiguredIsolatedStateful(ctx, r, model, deps); err != nil {
			return err
		}
		emitUpPhase(deps, "starting worktree stack %s", r.names.Project)
		deps.emit("compose-up")
		// --remove-orphans: services dropped from the regenerated artifact
		// (e.g. a fork's uplink) must not linger with canonical aliases.
		if err := deps.runner.Run(ctx, worktreeComposeCommand(r, deps, "up", append([]string{"-d", "--remove-orphans"}, args...)...)); err != nil {
			if !isBindFailure(err) {
				return err
			}
			return retryWorktreeComposeAfterBindFailure(ctx, r, model, args, deps, err)
		}
		return nil
	})
}

func emitUpPhase(deps commandDeps, format string, args ...any) {
	fmt.Fprintf(deps.stderr, "docktree up: "+format+"\n", args...)
}

// ensureInfraForUp always converges the infra stack: compose up -d recreates
// containers only when their rendered config changed, which is what delivers
// projection updates (e.g. dt-upstream-* aliases) to a running stack without
// any imperative reconcile.
func ensureInfraForUp(ctx context.Context, r *resolved, deps commandDeps) error {
	if len(r.manifest.Shared) == 0 {
		return nil
	}
	emitUpPhase(deps, "starting shared infra %s", r.names.InfraProject)
	return ensureInfra(ctx, r, deps)
}

func isBindFailure(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "address already in use") ||
		strings.Contains(message, "port is already allocated") ||
		strings.Contains(message, "bind for")
}

func retryWorktreeComposeAfterBindFailure(ctx context.Context, r *resolved, model *compose.Model, args []string, deps commandDeps, bindErr error) error {
	if len(r.servicePorts) == 0 {
		return bindErr
	}
	appRegistry, err := registry.EnsureApp(registryPath(), r.manifest.App, defaultWorktreeServiceBands(r.manifest))
	if err != nil {
		return err
	}
	failedPorts := parseBindFailurePorts(bindErr)
	targetAllNonFixed := len(failedPorts) == 0
	matched := false
	nextPorts := make(map[string]int, len(r.servicePorts))
	for service, failedPort := range r.servicePorts {
		svc := r.manifest.Services[service]
		shouldBump := targetAllNonFixed || failedPorts[failedPort]
		if failedPorts[failedPort] {
			matched = true
		}
		if !shouldBump {
			nextPorts[service] = failedPort
			continue
		}
		if r.primaryDefaultPorts[service] {
			return fmt.Errorf("port %d for main worktree service %q failed to bind; free it or change its Compose port default", failedPort, service)
		}
		if svc.Fixed {
			return fmt.Errorf("port %d for fixed service %q failed to bind; free it or reassign", failedPort, service)
		}
		base, ok := appRegistry.Bands[service]
		if !ok {
			return fmt.Errorf("cannot retry bind failure for %s: missing registry band", service)
		}
		next, err := ports.ResolveAfterBindFailure(base, failedPort, registry.BandWidth, svc.Fixed, deps.portFree)
		if err != nil {
			return err
		}
		nextPorts[service] = next
	}
	if len(failedPorts) > 0 && !matched {
		nextPorts = make(map[string]int, len(r.servicePorts))
		for service, failedPort := range r.servicePorts {
			svc := r.manifest.Services[service]
			if r.primaryDefaultPorts[service] {
				return fmt.Errorf("port %d for main worktree service %q failed to bind; free it or change its Compose port default", failedPort, service)
			}
			if svc.Fixed {
				nextPorts[service] = failedPort
				continue
			}
			base, ok := appRegistry.Bands[service]
			if !ok {
				return fmt.Errorf("cannot retry bind failure for %s: missing registry band", service)
			}
			next, err := ports.ResolveAfterBindFailure(base, failedPort, registry.BandWidth, false, deps.portFree)
			if err != nil {
				return err
			}
			nextPorts[service] = next
		}
	}
	r.servicePorts = nextPorts
	if err := writeStackArtifacts(r, model, deps); err != nil {
		return err
	}
	emitUpPhase(deps, "retrying worktree stack %s after bind failure", r.names.Project)
	deps.emit("compose-up-retry")
	return deps.runner.Run(ctx, worktreeComposeCommand(r, deps, "up", append([]string{"-d", "--remove-orphans"}, args...)...))
}

var bindPortRe = regexp.MustCompile(`(?i)(?:0\.0\.0\.0|\[::\]|localhost|127\.0\.0\.1)?:(\d{1,5})`)

func parseBindFailurePorts(err error) map[int]bool {
	ports := map[int]bool{}
	if err == nil {
		return ports
	}
	for _, match := range bindPortRe.FindAllStringSubmatch(err.Error(), -1) {
		port, convErr := strconv.Atoi(match[1])
		if convErr == nil && port > 0 && port <= 65535 {
			ports[port] = true
		}
	}
	return ports
}
