package cmd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rajpatil53/docktree/internal/paths"
)

func TestExplainIncludesDerivationRuntimePortsDataSourcesAndArtifacts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeProject(t, root, `
app = "shop"
shared = ["postgres"]

[services.api]
host_port_env = "API_PORT"

[services.postgres]
host_port_env = "POSTGRES_PORT"

[stateful.postgres]
default_strategy = "shared"
engine = "postgres"
snapshot_source = "shop_pgdata"
source_db = "shop_shared"
`)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{
		configOutput: []byte(basicComposeConfigJSON),
		outputByArgv: map[string][]byte{
			forkVolumeListArgv("shop"): []byte(""),
			runtimeComposeLSArgv():     []byte(`[{"Name":"shop-feature_one","Status":"running(1)"}]`),
			runtimeDockerPSArgv("shop"): []byte(`[
					{"Labels":{"com.docktree.app":"shop","com.docktree.slug":"feature_one","com.docktree.project":"shop-feature_one","com.docktree.service":"api"},"State":"running","Ports":"0.0.0.0:18080->3000/tcp"},
					{"Labels":{"com.docktree.app":"shop","com.docktree.slug":"main","com.docktree.project":"shop-infra","com.docktree.service":"postgres"},"State":"running","Ports":"0.0.0.0:5432->5432/tcp"}
				]`),
		},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)
	var stdout strings.Builder
	deps.stdout = &stdout

	if err := runExplain(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}
	got := stdout.String()
	for _, want := range []string{
		"app: shop",
		"slug: feature_one",
		"main slug: feature_one",
		"worktree project: shop-feature_one",
		"infra project: shop-infra",
		"shared network: shop_shared",
		"postgres=localhost:5432 (shared)",
		"api=localhost:",
		"api=localhost:18080",
		"postgres default_strategy=shared source=shop_shared",
		"data sources: docktree.toml, docker compose config, docker ps labels",
		"compose worktree argv: docker compose --progress plain --env-file " + paths.EnvFile(root) + " -p shop-feature_one -f " + paths.WorktreeCompose(root) + " up -d --remove-orphans",
		"compose infra argv: docker compose --progress plain --env-file " + paths.InfraEnvFile(root) + " -p shop-infra -f " + paths.InfraCompose(root) + " up -d",
		"worktree projection: " + paths.WorktreeCompose(root),
		"infra projection: " + paths.InfraCompose(root),
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("explain output =\n%s\nwant %q", got, want)
		}
	}
}

func TestExplainPrintsInterposedComposeArgvUnderSecretsWrapper(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeProject(t, root, envOverrideProjectConfig+`
[secrets]
wrapper = "doppler run --"
`)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{
		configOutput:   []byte(basicComposeConfigJSON),
		networkMissing: true,
		outputByArgv: map[string][]byte{
			forkVolumeListArgv("shop"):  []byte(""),
			runtimeComposeLSArgv():      []byte(`[]`),
			runtimeDockerPSArgv("shop"): []byte(`[]`),
		},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)
	var stdout strings.Builder
	deps.stdout = &stdout

	// up writes the artifacts; explain must then print the same interposed
	// argv docktree would run, so the printed command is a faithful hand-run.
	if err := runUp(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}
	if err := runExplain(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}

	got := stdout.String()
	worktreePairs := envOverridePairs(t, paths.EnvFile(root))
	want := "compose worktree argv: doppler run -- env -- " + strings.Join(worktreePairs, " ") +
		" docker compose --progress plain --env-file " + paths.EnvFile(root) +
		" -p shop-feature_one -f " + paths.WorktreeCompose(root) + " up -d --remove-orphans"
	if !strings.Contains(got, want) {
		t.Fatalf("explain output =\n%s\nwant %q", got, want)
	}
}
