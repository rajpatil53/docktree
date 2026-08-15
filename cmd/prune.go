package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rajpatil53/docktree/internal/dockerstate"
	"github.com/rajpatil53/docktree/internal/identity"
	"github.com/rajpatil53/docktree/internal/runner"
	"github.com/rajpatil53/docktree/internal/stateful"
)

func Prune(args []string) error {
	return runPrune(context.Background(), args, defaultCommandDeps(os.Stdin, os.Stdout, os.Stderr))
}

type pruneOptions struct {
	execute      bool
	includeForks bool
	confirmForks string
}

func runPrune(ctx context.Context, args []string, deps commandDeps) error {
	opts, err := parsePruneArgs(args)
	if err != nil {
		return err
	}
	r, err := resolveForCurrentDir(deps)
	if err != nil {
		return err
	}
	liveSlugs, err := liveWorktreeSlugs(r.wd)
	if err != nil {
		return err
	}
	stacks, err := loadRuntimeStacks(ctx, r, deps)
	if err != nil {
		return err
	}
	if err := pruneStacks(ctx, r, stacks, liveSlugs, opts, deps); err != nil {
		return err
	}
	if opts.includeForks {
		if err := pruneForkVolumes(ctx, r, stacks, liveSlugs, opts, deps); err != nil {
			return err
		}
	}
	return pruneSharedInfraIfUnused(ctx, r, stacks, liveSlugs, opts, deps)
}

func parsePruneArgs(args []string) (pruneOptions, error) {
	var opts pruneOptions
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--execute":
			opts.execute = true
		case "--include-forks":
			opts.includeForks = true
		case "--confirm-forks":
			if i+1 >= len(args) {
				return pruneOptions{}, fmt.Errorf("usage: docktree prune [--execute] [--include-forks --confirm-forks <app>]")
			}
			opts.confirmForks = args[i+1]
			i++
		default:
			return pruneOptions{}, fmt.Errorf("usage: docktree prune [--execute] [--include-forks --confirm-forks <app>]")
		}
	}
	return opts, nil
}

func pruneStacks(ctx context.Context, r *resolved, stacks []dockerstate.Stack, liveSlugs map[string]bool, opts pruneOptions, deps commandDeps) error {
	for _, stack := range stacks {
		if !isPruneCandidate(r, stack, liveSlugs) {
			continue
		}
		if stackRunning(stack) {
			fmt.Fprintf(deps.stdout, "running orphaned stack %s reported only\n", stack.Project)
			continue
		}
		if !opts.execute {
			fmt.Fprintf(deps.stdout, "dry-run: would remove stopped orphaned stack %s\n", stack.Project)
			continue
		}
		if err := deps.runner.Run(ctx, runner.Command{
			Argv:   []string{"docker", "compose", "-p", stack.Project, "down", "--remove-orphans"},
			Dir:    r.wd,
			Env:    os.Environ(),
			Stdin:  deps.stdin,
			Stdout: deps.stdout,
			Stderr: deps.stderr,
		}); err != nil {
			return err
		}
	}
	return nil
}

func isPruneCandidate(r *resolved, stack dockerstate.Stack, liveSlugs map[string]bool) bool {
	if stack.Project == "" || stack.Project == r.names.InfraProject || stack.Project == r.names.Project {
		return false
	}
	if !stack.Managed {
		return false
	}
	if stack.App != "" && stack.App != r.manifest.App {
		return false
	}
	if stack.App == "" && !strings.HasPrefix(stack.Project, r.manifest.App+"-") {
		return false
	}
	if stack.Slug == "" {
		return true
	}
	if stack.Slug == r.slug || stack.Slug == r.mainSlug {
		return false
	}
	return !liveSlugs[stack.Slug]
}

func stackRunning(stack dockerstate.Stack) bool {
	if strings.Contains(strings.ToLower(stack.Status), "running") {
		return true
	}
	for _, service := range stack.Services {
		if strings.EqualFold(service.State, "running") {
			return true
		}
	}
	return false
}

func pruneForkVolumes(ctx context.Context, r *resolved, stacks []dockerstate.Stack, liveSlugs map[string]bool, opts pruneOptions, deps commandDeps) error {
	if opts.execute && opts.confirmForks != r.manifest.App {
		return fmt.Errorf("docktree prune --include-forks requires --confirm-forks %s", r.manifest.App)
	}
	runningSlugs := runningStackSlugs(r, stacks)
	volumes, err := listForkVolumes(ctx, r.manifest.App, deps)
	if err != nil {
		return err
	}
	for _, volume := range volumes {
		if volume.App != r.manifest.App || !volume.IsForkFor(volume.Service) {
			continue
		}
		if !isPruneEligibleVolumeSlug(r, volume.Slug, liveSlugs, runningSlugs) {
			continue
		}
		req := stateful.DestroyRequest{
			App:          r.manifest.App,
			Slug:         volume.Slug,
			MainSlug:     r.mainSlug,
			Service:      volume.Service,
			Strategy:     "isolated",
			ResourceKind: stateful.ResourceVolume,
			ResourceName: volume.Name,
		}
		if err := stateful.CheckDestroy(req); err != nil {
			fmt.Fprintf(deps.stdout, "skipping guarded forked volume %s: %v\n", volume.Name, err)
			continue
		}
		if !opts.execute {
			fmt.Fprintf(deps.stdout, "dry-run: would remove forked volume %s\n", volume.Name)
			continue
		}
		if err := deps.runner.Run(ctx, runnerCommand(r, deps, []string{"docker", "volume", "rm", volume.Name})); err != nil {
			return err
		}
	}
	return nil
}

func pruneSharedInfraIfUnused(ctx context.Context, r *resolved, stacks []dockerstate.Stack, liveSlugs map[string]bool, opts pruneOptions, deps commandDeps) error {
	if len(r.manifest.Shared) == 0 || !infraStackPresent(r, stacks) || remainingWorktreeStackPresent(r, stacks, liveSlugs) {
		return nil
	}
	if !opts.execute {
		fmt.Fprintf(deps.stdout, "dry-run: would stop shared infra %s\n", r.names.InfraProject)
		return nil
	}
	rendered, err := prepareSharedArtifacts(ctx, deps)
	if err != nil {
		return err
	}
	return deps.runner.Run(ctx, infraComposeCommand(rendered, deps, "down"))
}

func infraStackPresent(r *resolved, stacks []dockerstate.Stack) bool {
	for _, stack := range stacks {
		if stack.Project == r.names.InfraProject {
			return true
		}
	}
	return false
}

func remainingWorktreeStackPresent(r *resolved, stacks []dockerstate.Stack, liveSlugs map[string]bool) bool {
	for _, stack := range stacks {
		if stack.Project == "" || stack.Project == r.names.InfraProject {
			continue
		}
		if stack.App != "" && stack.App != r.manifest.App {
			continue
		}
		if stack.App == "" && !strings.HasPrefix(stack.Project, r.manifest.App+"-") {
			continue
		}
		if isPruneCandidate(r, stack, liveSlugs) && !stackRunning(stack) {
			continue
		}
		return true
	}
	return false
}

func runningStackSlugs(r *resolved, stacks []dockerstate.Stack) map[string]bool {
	running := map[string]bool{}
	for _, stack := range stacks {
		if stack.App != r.manifest.App || stack.Slug == "" || !stackRunning(stack) {
			continue
		}
		running[stack.Slug] = true
	}
	return running
}

func isPruneEligibleVolumeSlug(r *resolved, slug string, liveSlugs, runningSlugs map[string]bool) bool {
	if slug == "" || slug == r.slug || slug == r.mainSlug {
		return false
	}
	if liveSlugs[slug] || runningSlugs[slug] {
		return false
	}
	return true
}

func liveWorktreeSlugs(wd string) (map[string]bool, error) {
	infos, err := liveWorktreeInfo(wd)
	if err != nil {
		return nil, err
	}
	live := map[string]bool{}
	for slug := range infos {
		live[slug] = true
	}
	return live, nil
}

type worktreeInfo struct {
	Slug   string
	Branch string
}

func liveWorktreeInfo(wd string) (map[string]worktreeInfo, error) {
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = wd
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("listing git worktrees: %w", err)
	}
	return parseLiveWorktreeInfo(out), nil
}

func parseLiveWorktreeSlugs(data []byte) map[string]bool {
	infos := parseLiveWorktreeInfo(data)
	live := map[string]bool{}
	for slug := range infos {
		live[slug] = true
	}
	return live
}

func parseLiveWorktreeInfo(data []byte) map[string]worktreeInfo {
	live := map[string]worktreeInfo{}
	var path string
	var branch string
	prunable := false
	flush := func() {
		if path == "" {
			return
		}
		if !prunable {
			if info, err := os.Stat(path); err == nil && info.IsDir() {
				slug := identity.ComputeSlug(filepath.Base(path))
				live[slug] = worktreeInfo{Slug: slug, Branch: cleanBranch(branch)}
			}
		}
		path = ""
		branch = ""
		prunable = false
	}
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			path = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		case strings.HasPrefix(line, "branch "):
			branch = strings.TrimSpace(strings.TrimPrefix(line, "branch "))
		case strings.HasPrefix(line, "prunable"):
			prunable = true
		}
	}
	flush()
	return live
}

func cleanBranch(branch string) string {
	return strings.TrimPrefix(branch, "refs/heads/")
}
