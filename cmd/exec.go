package cmd

import (
	"context"
	"fmt"
	"os"
)

func Exec(args []string) error {
	return runExec(context.Background(), args, defaultCommandDeps(os.Stdin, os.Stdout, os.Stderr))
}

func runExec(ctx context.Context, args []string, deps commandDeps) error {
	r, remaining, err := resolveTargetArgs(ctx, args, deps)
	if err != nil {
		return err
	}
	if len(remaining) == 0 {
		return fmt.Errorf("usage: docktree exec [worktree] <service> -- <cmd...>")
	}
	return deps.runner.Run(ctx, worktreeComposeCommand(r, deps, "exec", remaining...))
}
