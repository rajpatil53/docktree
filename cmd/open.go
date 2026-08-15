package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/rajpatil53/docktree/internal/dockerstate"
)

func Open(args []string) error {
	return runOpen(context.Background(), args, defaultCommandDeps(os.Stdin, os.Stdout, os.Stderr))
}

func runOpen(ctx context.Context, args []string, deps commandDeps) error {
	r, remaining, err := resolveTargetArgs(ctx, args, deps)
	if err != nil {
		return err
	}
	if len(remaining) > 1 {
		return fmt.Errorf("usage: docktree open [worktree] [service]")
	}
	service := ""
	if len(remaining) == 1 {
		service = remaining[0]
	}
	if url, ok, err := proxyURLForService(ctx, r, service, deps); err != nil {
		return err
	} else if ok {
		fmt.Fprintln(deps.stdout, url)
		return nil
	}
	stacks, err := loadRuntimeStacks(ctx, r, deps)
	if err != nil {
		return err
	}
	if url, ok := dockerstate.FindURL(stacks, r.slug, service); ok {
		fmt.Fprintln(deps.stdout, url)
		return nil
	}
	if url, ok := resolvedURL(r, service); ok {
		fmt.Fprintln(deps.stdout, url)
		return nil
	}
	if service == "" {
		return fmt.Errorf("no published service URL found for %s", r.slug)
	}
	return fmt.Errorf("no published URL found for %s/%s", r.slug, service)
}
