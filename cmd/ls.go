package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/rajpatil53/docktree/internal/dockerstate"
)

func LS(args []string) error {
	return runLS(context.Background(), args, defaultCommandDeps(os.Stdin, os.Stdout, os.Stderr))
}

func runLS(ctx context.Context, args []string, deps commandDeps) error {
	jsonOut := false
	for _, arg := range args {
		switch arg {
		case "--json":
			jsonOut = true
		default:
			return fmt.Errorf("usage: docktree ls [--json]")
		}
	}
	r, err := resolveForCurrentDir(deps)
	if err != nil {
		return err
	}
	stacks, err := loadRuntimeStacks(ctx, r, deps)
	if err != nil {
		return err
	}
	enrichStackBranches(r.wd, stacks)
	if jsonOut {
		encoder := json.NewEncoder(deps.stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(stacks)
	}
	for _, stack := range stacks {
		fmt.Fprintf(deps.stdout, "%s\t%s\t%s\t%s\n", stack.Slug, stack.Branch, stack.Project, stack.Status)
	}
	return nil
}

func enrichStackBranches(wd string, stacks []dockerstate.Stack) {
	infos, err := liveWorktreeInfo(wd)
	if err != nil {
		return
	}
	for i := range stacks {
		if info, ok := infos[stacks[i].Slug]; ok {
			stacks[i].Branch = info.Branch
		}
	}
}
