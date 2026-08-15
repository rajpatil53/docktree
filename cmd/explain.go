package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/rajpatil53/docktree/internal/compose"
	"github.com/rajpatil53/docktree/internal/config"
	"github.com/rajpatil53/docktree/internal/identity"
	"github.com/rajpatil53/docktree/internal/paths"
	"github.com/rajpatil53/docktree/internal/stateful"
)

func Explain(args []string) error {
	return runExplain(context.Background(), args, defaultCommandDeps(os.Stdin, os.Stdout, os.Stderr))
}

func runExplain(ctx context.Context, args []string, deps commandDeps) error {
	if len(args) > 1 {
		return fmt.Errorf("usage: docktree explain [worktree]")
	}
	wd, err := currentWorktreeRoot(deps)
	if err != nil {
		return err
	}
	r, model, err := prepareStack(ctx, wd, deps)
	if err != nil {
		return err
	}
	if len(args) == 1 {
		if targetRoot, ok, targetErr := resolveTargetRoot(wd, args[0]); targetErr != nil {
			return targetErr
		} else if ok {
			r, model, err = prepareStack(ctx, targetRoot, deps)
			if err != nil {
				return err
			}
		} else {
			r, err = retargetResolved(r, identity.ComputeSlug(args[0]), deps.portFree)
			if err != nil {
				return err
			}
		}
	}
	stacks, err := loadRuntimeStacks(ctx, r, deps)
	if err != nil {
		return err
	}
	actual := actualPorts(stacks)

	fmt.Fprintf(deps.stdout, "app: %s\n", r.manifest.App)
	fmt.Fprintf(deps.stdout, "slug: %s\n", r.slug)
	fmt.Fprintf(deps.stdout, "main slug: %s\n", r.mainSlug)
	fmt.Fprintf(deps.stdout, "worktree project: %s\n", r.names.Project)
	fmt.Fprintf(deps.stdout, "infra project: %s\n", r.names.InfraProject)
	fmt.Fprintf(deps.stdout, "shared network: %s\n", r.names.SharedNetwork)
	fmt.Fprintf(deps.stdout, "env file: %s\n", paths.EnvFile(r.wd))
	fmt.Fprintf(deps.stdout, "worktree projection: %s\n", paths.WorktreeCompose(r.wd))
	fmt.Fprintf(deps.stdout, "infra projection: %s\n", paths.InfraCompose(r.wd))
	fmt.Fprintf(deps.stdout, "data sources: docktree.toml, docker compose config, docker ps labels\n")
	fmt.Fprintf(deps.stdout, "compose worktree argv: %s\n", strings.Join(worktreeComposeCommand(r, deps, "up", "-d", "--remove-orphans").Argv, " "))
	fmt.Fprintf(deps.stdout, "compose infra argv: %s\n", strings.Join(infraComposeCommand(r, deps, "up", "-d").Argv, " "))
	fmt.Fprintln(deps.stdout, "resolved ports:")
	for _, line := range portLines(r.servicePorts, r.sharedPorts) {
		fmt.Fprintln(deps.stdout, "  "+line)
	}
	fmt.Fprintln(deps.stdout, "actual published ports:")
	for _, line := range actualPortLines(actual) {
		fmt.Fprintln(deps.stdout, "  "+line)
	}
	fmt.Fprintln(deps.stdout, "stateful data:")
	for _, line := range statefulLines(r) {
		fmt.Fprintln(deps.stdout, "  "+line)
	}
	if model != nil {
		fmt.Fprintf(deps.stdout, "compose services: %s\n", strings.Join(composeServiceNames(model.Services), ", "))
	}
	return nil
}

func portLines(worktree, shared map[string]int) []string {
	var lines []string
	for _, name := range sortedIntMapKeys(shared) {
		lines = append(lines, fmt.Sprintf("%s=localhost:%d (shared)", name, shared[name]))
	}
	for _, name := range sortedIntMapKeys(worktree) {
		lines = append(lines, fmt.Sprintf("%s=localhost:%d (worktree)", name, worktree[name]))
	}
	return lines
}

func actualPortLines(ports map[servicePortKey]int) []string {
	var lines []string
	keys := make([]servicePortKey, 0, len(ports))
	for key := range ports {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].project != keys[j].project {
			return keys[i].project < keys[j].project
		}
		return keys[i].service < keys[j].service
	})
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("%s/%s=localhost:%d", key.project, key.service, ports[key]))
	}
	if len(lines) == 0 {
		return []string{"(none)"}
	}
	return lines
}

func statefulLines(r *resolved) []string {
	rendered := r.manifest.RenderTemplates(config.TemplateContext{App: r.manifest.App, Slug: r.slug, MainSlug: r.mainSlug})
	var lines []string
	for _, name := range sortedStatefulNames(rendered.Stateful) {
		st := rendered.Stateful[name]
		strategy := st.DefaultStrategy
		if strategy == "" {
			strategy = "shared"
		}
		source := st.SnapshotSource
		if stateful.UsePostgresFastPath(st.Engine) {
			source = st.SourceDB
		}
		if source == "" {
			source = "shared"
		}
		lines = append(lines, fmt.Sprintf("%s default_strategy=%s source=%s", name, strategy, source))
	}
	if len(lines) == 0 {
		return []string{"(none)"}
	}
	return lines
}

func sortedIntMapKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func composeServiceNames(services map[string]compose.Service) []string {
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
