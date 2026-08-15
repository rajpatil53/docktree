package cmd

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/rajpatil53/docktree/internal/paths"
	"github.com/rajpatil53/docktree/internal/stateful"
)

func TestUnforkPostgresRegeneratesArtifactsWithSharedDataSource(t *testing.T) {
	root := writeGitBackedProject(t)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{
		configOutput: []byte(statefulComposeConfigJSON),
		outputByArgv: map[string][]byte{
			forkVolumeListArgv("shop"):                                  []byte(""),
			postgresDatabaseExistsArgv(root, "shop_shared_feature_one"): []byte("1\n"),
		},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runUnfork(context.Background(), []string{"postgres", "--confirm", "shop_shared_feature_one"}, deps); err != nil {
		t.Fatal(err)
	}

	envData, err := os.ReadFile(paths.EnvFile(root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(envData), "DATABASE_URL=") {
		t.Fatalf("unfork should not write stateful DATABASE_URL:\n%s", envData)
	}
	if strings.Contains(string(envData), "POSTGRES_PRIMARY_DB=") {
		t.Fatalf("unfork should remove isolated stateful env:\n%s", envData)
	}
	worktreeData, err := os.ReadFile(paths.WorktreeCompose(root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(worktreeData), "com.docktree.fork") {
		t.Fatalf("unfork projection still has fork label:\n%s", worktreeData)
	}

	argv := commandArgvStrings(fr.commands)
	down := "docker compose --env-file " + paths.EnvFile(root) + " -p shop-feature_one -f " + paths.WorktreeCompose(root) + " down"
	drop := "docker compose --env-file " + paths.InfraEnvFile(root) + " -p shop-infra -f " + paths.InfraCompose(root) + " exec -T postgres psql -U postgres -d postgres -v ON_ERROR_STOP=1 -c DROP DATABASE IF EXISTS \"shop_shared_feature_one\" WITH (FORCE);"
	up := "docker compose --progress plain --env-file " + paths.EnvFile(root) + " -p shop-feature_one -f " + paths.WorktreeCompose(root) + " up -d --remove-orphans --force-recreate"
	if !containsArgv(argv, drop) {
		t.Fatalf("postgres drop command missing from %#v", argv)
	}
	if !containsArgv(argv, down) {
		t.Fatalf("unfork should stop worktree stack before dropping logical DB: %#v", argv)
	}
	if !containsArgv(argv, up) {
		t.Fatalf("unfork did not restart worktree stack with shared artifacts: %#v", argv)
	}
	if indexArgv(argv, down) > indexArgv(argv, drop) {
		t.Fatalf("worktree stack must stop before logical DB drop: %#v", argv)
	}
}

func TestUnforkRefusesToDestroySharedStatefulResource(t *testing.T) {
	root := writeGitBackedProject(t)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{
		configOutput: []byte(statefulComposeConfigJSON),
		outputByArgv: map[string][]byte{
			forkVolumeListArgv("shop"):                                  []byte(""),
			postgresDatabaseExistsArgv(root, "shop_shared_feature_one"): []byte(""),
		},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	err := runUnfork(context.Background(), []string{"postgres", "--confirm", "shop_shared_feature_one"}, deps)
	if err == nil || !strings.Contains(err.Error(), "strategy \"shared\"") {
		t.Fatalf("runUnfork error = %v, want shared-mode safety guard", err)
	}
	if strings.Contains(strings.Join(commandArgvStrings(fr.commands), "\n"), "DROP DATABASE") {
		t.Fatalf("unfork attempted destructive SQL for shared resource: %#v", commandArgvStrings(fr.commands))
	}
}

func TestUnforkGenericVolumeRequiresConfirmation(t *testing.T) {
	root := writeGitBackedProject(t)
	t.Setenv("HOME", t.TempDir())
	appendConfig(t, root, `

[stateful.redis]
engine = "redis"
snapshot_source = "shop_redisdata"
`)

	fr := &fakeRunner{
		configOutput: []byte(statefulComposeConfigJSON),
		outputByArgv: map[string][]byte{forkVolumeListArgv("shop"): []byte(`[
			{"Name":"shop-feature_one-redisdata","Labels":"com.docktree.app=shop,com.docktree.slug=feature_one,com.docktree.project=shop-feature_one,com.docktree.service=redis,com.docktree.fork=redis"}
		]`)},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	err := runUnfork(context.Background(), []string{"redis"}, deps)
	if err == nil || !strings.Contains(err.Error(), "requires --confirm "+stateful.VolumeName("shop", "feature_one", "redis")) {
		t.Fatalf("unfork without confirmation error = %v", err)
	}

	if err := runUnfork(context.Background(), []string{"redis", "--confirm", stateful.VolumeName("shop", "feature_one", "redis")}, deps); err != nil {
		t.Fatal(err)
	}
	argv := commandArgvStrings(fr.commands)
	stop := "docker compose --env-file " + paths.EnvFile(root) + " -p shop-feature_one -f " + paths.WorktreeCompose(root) + " down"
	rm := "docker volume rm " + stateful.VolumeName("shop", "feature_one", "redis")
	up := "docker compose --progress plain --env-file " + paths.EnvFile(root) + " -p shop-feature_one -f " + paths.WorktreeCompose(root) + " up -d --remove-orphans --force-recreate"
	if !containsArgv(argv, stop) {
		t.Fatalf("worktree stack stop command missing from %#v", argv)
	}
	if !containsArgv(argv, rm) {
		t.Fatalf("volume rm command missing from %#v", argv)
	}
	if indexArgv(argv, stop) > indexArgv(argv, rm) {
		t.Fatalf("worktree stack must stop before its volume is removed: %#v", argv)
	}
	if !containsArgv(argv, up) {
		t.Fatalf("worktree stack restart command missing from %#v", argv)
	}
	if indexArgv(argv, rm) > indexArgv(argv, up) {
		t.Fatalf("forked volume must be removed before restart with shared artifacts: %#v", argv)
	}
}
