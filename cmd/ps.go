package cmd

import (
	"context"
	"os"
)

func PS(args []string) error {
	return runPS(context.Background(), args, defaultCommandDeps(os.Stdin, os.Stdout, os.Stderr))
}

func runPS(ctx context.Context, args []string, deps commandDeps) error {
	r, remaining, err := resolveTargetArgs(ctx, args, deps)
	if err != nil {
		return err
	}
	for i, arg := range remaining {
		if arg == "--service" {
			remaining[i] = "--services"
		}
	}
	return deps.runner.Run(ctx, worktreeComposeCommand(r, deps, "ps", remaining...))
}
