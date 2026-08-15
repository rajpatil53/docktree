package cmd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rajpatil53/docktree/internal/paths"
	"github.com/rajpatil53/docktree/internal/stateful"
)

func TestPruneDryRunReportsStoppedAndRunningOrphans(t *testing.T) {
	root := writeGitBackedProject(t)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{
		configOutput: []byte(statefulComposeConfigJSON),
		outputByArgv: map[string][]byte{
			runtimeComposeLSArgv(): []byte(`[
					{"Name":"shop-old","Status":"exited(0)"},
					{"Name":"shop-live","Status":"running(1)"}
				]`),
			runtimeDockerPSArgv("shop"): []byte(`[
					{"Labels":"com.docktree.managed=true,com.docktree.app=shop,com.docktree.slug=old,com.docktree.project=shop-old,com.docktree.service=api","State":"exited"},
					{"Labels":"com.docktree.managed=true,com.docktree.app=shop,com.docktree.slug=live,com.docktree.project=shop-live,com.docktree.service=api","State":"running"}
				]`),
		},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)
	var stdout strings.Builder
	deps.stdout = &stdout

	if err := runPrune(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}
	got := stdout.String()
	for _, want := range []string{
		"dry-run: would remove stopped orphaned stack shop-old",
		"running orphaned stack shop-live reported only",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("prune output = %q, missing %q", got, want)
		}
	}
	if containsArgv(commandArgvStrings(fr.commands), "docker compose -p shop-old down --remove-orphans") {
		t.Fatalf("dry-run removed a stack: %#v", commandArgvStrings(fr.commands))
	}
}

func TestPruneExecuteRemovesStoppedOrphanedStacks(t *testing.T) {
	root := writeGitBackedProject(t)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{
		configOutput: []byte(statefulComposeConfigJSON),
		outputByArgv: map[string][]byte{
			runtimeComposeLSArgv(): []byte(`[{"Name":"shop-old","Status":"exited(0)"}]`),
			runtimeDockerPSArgv("shop"): []byte(`[
					{"Labels":"com.docktree.managed=true,com.docktree.app=shop,com.docktree.slug=old,com.docktree.project=shop-old,com.docktree.service=api","State":"exited"}
				]`),
		},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runPrune(context.Background(), []string{"--execute"}, deps); err != nil {
		t.Fatal(err)
	}
	if !containsArgv(commandArgvStrings(fr.commands), "docker compose -p shop-old down --remove-orphans") {
		t.Fatalf("stopped orphan removal command missing from %#v", commandArgvStrings(fr.commands))
	}
}

func TestPruneOffersAndExecutesInfraShutdownWhenNoWorktreeStacksRemain(t *testing.T) {
	root := writeGitBackedProject(t)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{
		configOutput: []byte(statefulComposeConfigJSON),
		outputByArgv: map[string][]byte{
			runtimeComposeLSArgv(): []byte(`[{"Name":"shop-infra","Status":"running(1)"}]`),
			runtimeDockerPSArgv("shop"): []byte(`[
				{"Labels":"com.docktree.managed=true,com.docktree.app=shop,com.docktree.slug=shop_main,com.docktree.project=shop-infra,com.docktree.service=postgres","State":"running"}
			]`),
		},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)
	var stdout strings.Builder
	deps.stdout = &stdout

	if err := runPrune(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "dry-run: would stop shared infra shop-infra") {
		t.Fatalf("prune output = %q, want shared infra dry-run notice", stdout.String())
	}

	fr = &fakeRunner{
		configOutput: []byte(statefulComposeConfigJSON),
		outputByArgv: map[string][]byte{
			runtimeComposeLSArgv(): []byte(`[{"Name":"shop-infra","Status":"running(1)"}]`),
			runtimeDockerPSArgv("shop"): []byte(`[
				{"Labels":"com.docktree.managed=true,com.docktree.app=shop,com.docktree.slug=shop_main,com.docktree.project=shop-infra,com.docktree.service=postgres","State":"running"}
			]`),
		},
	}
	events = nil
	deps = newCommandTestDeps(root, fr, &events)
	if err := runPrune(context.Background(), []string{"--execute"}, deps); err != nil {
		t.Fatal(err)
	}
	if !containsArgv(commandArgvStrings(fr.commands), "docker compose --env-file "+paths.InfraEnvFile(root)+" -p shop-infra -f "+paths.InfraCompose(root)+" down") {
		t.Fatalf("shared infra down command missing from %#v", commandArgvStrings(fr.commands))
	}
}

func TestPruneDoesNotRemoveUnlabeledPrefixMatchedComposeProjects(t *testing.T) {
	root := writeGitBackedProject(t)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{
		configOutput: []byte(statefulComposeConfigJSON),
		outputByArgv: map[string][]byte{
			runtimeComposeLSArgv():      []byte(`[{"Name":"shop-old","Status":"exited(0)"}]`),
			runtimeDockerPSArgv("shop"): []byte(`[]`),
		},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runPrune(context.Background(), []string{"--execute"}, deps); err != nil {
		t.Fatal(err)
	}
	if containsArgv(commandArgvStrings(fr.commands), "docker compose -p shop-old down --remove-orphans") {
		t.Fatalf("prune removed unlabeled prefix-matched project: %#v", commandArgvStrings(fr.commands))
	}
}

func TestPruneForkedVolumesRequireConfirmationAndSafetyGuards(t *testing.T) {
	root := writeGitBackedProject(t)
	t.Setenv("HOME", t.TempDir())
	oldVolume := stateful.VolumeName("shop", "old", "postgres")
	oldWithoutStackVolume := stateful.VolumeName("shop", "removed", "postgres")
	liveVolume := stateful.VolumeName("shop", "live", "postgres")
	currentVolume := stateful.VolumeName("shop", "feature_one", "postgres")

	fr := &fakeRunner{
		configOutput: []byte(statefulComposeConfigJSON),
		outputByArgv: map[string][]byte{
			runtimeComposeLSArgv(): []byte(`[
					{"Name":"shop-old","Status":"exited(0)"},
					{"Name":"shop-live","Status":"running(1)"},
					{"Name":"shop-feature_one","Status":"running(1)"}
				]`),
			runtimeDockerPSArgv("shop"): []byte(`[
					{"Labels":"com.docktree.managed=true,com.docktree.app=shop,com.docktree.slug=old,com.docktree.project=shop-old,com.docktree.service=api","State":"exited"},
					{"Labels":"com.docktree.managed=true,com.docktree.app=shop,com.docktree.slug=live,com.docktree.project=shop-live,com.docktree.service=api","State":"running"},
					{"Labels":"com.docktree.managed=true,com.docktree.app=shop,com.docktree.slug=feature_one,com.docktree.project=shop-feature_one,com.docktree.service=api","State":"running"}
			]`),
			"docker volume ls --filter label=com.docktree.app=shop --filter label=com.docktree.fork --format json": []byte(`[
				{"Name":"` + oldVolume + `","Labels":"com.docktree.app=shop,com.docktree.slug=old,com.docktree.project=shop-old,com.docktree.service=postgres,com.docktree.fork=postgres"},
				{"Name":"` + oldWithoutStackVolume + `","Labels":"com.docktree.app=shop,com.docktree.slug=removed,com.docktree.project=shop-removed,com.docktree.service=postgres,com.docktree.fork=postgres"},
				{"Name":"` + liveVolume + `","Labels":"com.docktree.app=shop,com.docktree.slug=live,com.docktree.project=shop-live,com.docktree.service=postgres,com.docktree.fork=postgres"},
				{"Name":"` + currentVolume + `","Labels":"com.docktree.app=shop,com.docktree.slug=feature_one,com.docktree.project=shop-feature_one,com.docktree.service=postgres,com.docktree.fork=postgres"},
				{"Name":"shop-shop_main-postgresdata","Labels":"com.docktree.app=shop,com.docktree.slug=shop_main,com.docktree.project=shop-shop_main,com.docktree.service=postgres,com.docktree.fork=postgres"}
			]`),
		},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	err := runPrune(context.Background(), []string{"--execute", "--include-forks"}, deps)
	if err == nil || !strings.Contains(err.Error(), "requires --confirm-forks shop") {
		t.Fatalf("expected fork confirmation error, got %v", err)
	}

	if err := runPrune(context.Background(), []string{"--execute", "--include-forks", "--confirm-forks", "shop"}, deps); err != nil {
		t.Fatal(err)
	}
	argv := commandArgvStrings(fr.commands)
	if !containsArgv(argv, "docker volume rm "+oldVolume) {
		t.Fatalf("forked volume rm command missing from %#v", argv)
	}
	if !containsArgv(argv, "docker volume rm "+oldWithoutStackVolume) {
		t.Fatalf("forked volume without stack should be pruned, commands: %#v", argv)
	}
	if containsArgv(argv, "docker volume rm "+liveVolume) {
		t.Fatalf("running worktree forked volume should not be pruned, commands: %#v", argv)
	}
	if containsArgv(argv, "docker volume rm "+currentVolume) {
		t.Fatalf("current worktree forked volume should not be pruned, commands: %#v", argv)
	}
	if containsArgv(argv, "docker volume rm shop-shop_main-postgresdata") {
		t.Fatalf("main forked volume should be guarded, commands: %#v", argv)
	}
}

func TestPruneDoesNotRemoveStoppedSiblingWorktreeThatStillExists(t *testing.T) {
	root := writeGitBackedProject(t)
	t.Setenv("HOME", t.TempDir())
	sibling := filepath.Join(filepath.Dir(root), "Sibling-One")
	runGit(t, root, "worktree", "add", "-b", "sibling-one", sibling)

	fr := &fakeRunner{
		configOutput: []byte(statefulComposeConfigJSON),
		outputByArgv: map[string][]byte{
			runtimeComposeLSArgv(): []byte(`[
				{"Name":"shop-sibling_one","Status":"exited(0)"},
				{"Name":"shop-old","Status":"exited(0)"}
			]`),
			runtimeDockerPSArgv("shop"): []byte(`[
				{"Labels":"com.docktree.managed=true,com.docktree.app=shop,com.docktree.slug=sibling_one,com.docktree.project=shop-sibling_one,com.docktree.service=api","State":"exited"},
				{"Labels":"com.docktree.managed=true,com.docktree.app=shop,com.docktree.slug=old,com.docktree.project=shop-old,com.docktree.service=api","State":"exited"}
			]`),
		},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runPrune(context.Background(), []string{"--execute"}, deps); err != nil {
		t.Fatal(err)
	}
	argv := commandArgvStrings(fr.commands)
	if containsArgv(argv, "docker compose -p shop-sibling_one down --remove-orphans") {
		t.Fatalf("prune removed live sibling worktree stack: %#v", argv)
	}
	if !containsArgv(argv, "docker compose -p shop-old down --remove-orphans") {
		t.Fatalf("orphaned stack removal command missing from %#v", argv)
	}
}
