package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/rajpatil53/docktree/internal/config"
	"github.com/rajpatil53/docktree/internal/paths"
	"github.com/rajpatil53/docktree/internal/runner"
)

func Shared(args []string) error {
	return runShared(context.Background(), args, defaultCommandDeps(os.Stdin, os.Stdout, os.Stderr))
}

func runShared(ctx context.Context, args []string, deps commandDeps) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: docktree shared up|down|status|nuke")
	}
	switch args[0] {
	case "up":
		if len(args) != 1 {
			return fmt.Errorf("usage: docktree shared up")
		}
		return runSharedUp(ctx, deps)
	case "down":
		if len(args) != 1 {
			return fmt.Errorf("usage: docktree shared down")
		}
		r, err := prepareSharedArtifacts(ctx, deps)
		if err != nil {
			return err
		}
		return deps.runner.Run(ctx, infraComposeCommand(r, deps, "down"))
	case "status":
		if len(args) != 1 {
			return fmt.Errorf("usage: docktree shared status")
		}
		r, err := prepareSharedArtifacts(ctx, deps)
		if err != nil {
			return err
		}
		return deps.runner.Run(ctx, infraComposeCommand(r, deps, "ps"))
	case "nuke":
		return runSharedNuke(ctx, args[1:], deps)
	default:
		return fmt.Errorf("usage: docktree shared up|down|status|nuke")
	}
}

func runSharedUp(ctx context.Context, deps commandDeps) error {
	wd, err := currentWorktreeRoot(deps)
	if err != nil {
		return err
	}
	return withAppCriticalSection(wd, deps, func() error {
		r, model, err := prepareStack(ctx, wd, deps)
		if err != nil {
			return err
		}
		if err := writeStackArtifacts(r, model, deps); err != nil {
			return err
		}
		if err := ensureSharedNetwork(ctx, r, deps); err != nil {
			return err
		}
		return ensureInfra(ctx, r, deps)
	})
}

func runSharedNuke(ctx context.Context, args []string, deps commandDeps) error {
	r, err := resolveForCurrentDir(deps)
	if err != nil {
		return err
	}
	if len(args) != 2 || args[0] != "--confirm" || args[1] != r.names.InfraProject {
		return fmt.Errorf("docktree shared nuke requires --confirm %s", r.names.InfraProject)
	}
	r, err = prepareSharedArtifacts(ctx, deps)
	if err != nil {
		return err
	}
	if err := deps.runner.Run(ctx, infraComposeCommand(r, deps, "down", "-v", "--remove-orphans")); err != nil {
		return err
	}
	return deps.runner.Run(ctx, runnerCommand(r, deps, []string{"docker", "network", "rm", r.names.SharedNetwork}))
}

func prepareSharedArtifacts(ctx context.Context, deps commandDeps) (*resolved, error) {
	wd, err := currentWorktreeRoot(deps)
	if err != nil {
		return nil, err
	}
	cfg, err := config.LoadFile(paths.ConfigFile(wd))
	if err != nil {
		return nil, err
	}
	model, err := loadComposeModel(ctx, wd, cfg, deps)
	if err != nil {
		return nil, err
	}
	r, err := resolveWithModel(wd, model, deps.portFree)
	if err != nil {
		return nil, err
	}
	if err := validateComposeForRun(model, r, deps); err != nil {
		return nil, err
	}
	if err := writeStackArtifacts(r, model, deps); err != nil {
		return nil, err
	}
	return r, nil
}

func resolveForCurrentDir(deps commandDeps) (*resolved, error) {
	wd, err := currentWorktreeRoot(deps)
	if err != nil {
		return nil, err
	}
	return resolveWithFree(wd, deps.portFree)
}

func runnerCommand(r *resolved, deps commandDeps, argv []string) runner.Command {
	return runner.Command{
		Argv:   argv,
		Dir:    r.wd,
		Env:    os.Environ(),
		Stdin:  deps.stdin,
		Stdout: deps.stdout,
		Stderr: deps.stderr,
	}
}
