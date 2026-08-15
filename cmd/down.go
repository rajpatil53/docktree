package cmd

import (
	"context"
	"fmt"
	"os"
)

func Down(args []string) error {
	return runDown(context.Background(), args, defaultCommandDeps(os.Stdin, os.Stdout, os.Stderr))
}

func runDown(ctx context.Context, args []string, deps commandDeps) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: docktree down")
	}
	wd, err := currentWorktreeRoot(deps)
	if err != nil {
		return err
	}
	r, err := resolveWithFree(wd, deps.portFree)
	if err != nil {
		return err
	}
	return deps.runner.Run(ctx, worktreeComposeCommand(r, deps, "down"))
}
