package cmd

import (
	"context"
	"os"
)

func Logs(args []string) error {
	return runLogs(context.Background(), args, defaultCommandDeps(os.Stdin, os.Stdout, os.Stderr))
}

func runLogs(ctx context.Context, args []string, deps commandDeps) error {
	r, remaining, err := resolveTargetArgs(ctx, args, deps)
	if err != nil {
		return err
	}
	return deps.runner.Run(ctx, worktreeComposeCommand(r, deps, "logs", remaining...))
}
