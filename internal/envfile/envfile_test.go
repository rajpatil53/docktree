package envfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteOrderedAndAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".docktree", ".env.worktree")
	kv := [][2]string{
		{"SLUG", "flexiple_platform"},
		{"RAILS_PORT", "20959"},
		{"FRONTEND_V2_PORT", "21959"},
	}
	if err := Write(path, kv); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	want := "SLUG=flexiple_platform\nRAILS_PORT=20959\nFRONTEND_V2_PORT=21959\n"
	if string(data) != want {
		t.Fatalf("got:\n%q\nwant:\n%q", string(data), want)
	}
	if strings.Count(string(data), "SLUG=") != 1 {
		t.Fatal("duplicate key written")
	}
}

func TestWriteArtifactOrdersDocktreeEnvAndLeavesAppEnvAlone(t *testing.T) {
	dir := t.TempDir()
	appEnv := filepath.Join(dir, ".env")
	if err := os.WriteFile(appEnv, []byte("APP_OWNED=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, ".docktree", ".env.worktree")
	if err := WriteArtifact(path, Artifact{
		ProjectName:     "shop-feature",
		SharedNetwork:   "shop_shared",
		WorktreeNetwork: "shop-feature_default",
		Ports: map[string]int{
			"WEB_PORT": 4173,
			"API_PORT": 3042,
		},
		Env: map[string]string{
			"OPENSEARCH_INDEX_PREFIX": "feature_",
		},
	}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"COMPOSE_PROJECT_NAME=shop-feature",
		"DOCKTREE_SHARED_NETWORK=shop_shared",
		"DOCKTREE_WORKTREE_NETWORK=shop-feature_default",
		"API_PORT=3042",
		"WEB_PORT=4173",
		"OPENSEARCH_INDEX_PREFIX=feature_",
		"",
	}, "\n")
	if string(data) != want {
		t.Fatalf("artifact:\n%s\nwant:\n%s", data, want)
	}
	appData, err := os.ReadFile(appEnv)
	if err != nil {
		t.Fatal(err)
	}
	if string(appData) != "APP_OWNED=1\n" {
		t.Fatalf("app .env was modified: %q", appData)
	}
}

func TestPairsOmitsWorktreeNetworkWhenUnset(t *testing.T) {
	// The infra artifact is worktree-invariant: it has no worktree network.
	pairs := Pairs(Artifact{ProjectName: "shop-infra", SharedNetwork: "shop_shared"})
	for _, pair := range pairs {
		if pair[0] == "DOCKTREE_WORKTREE_NETWORK" {
			t.Fatalf("empty worktree network must be omitted: %#v", pairs)
		}
	}
}
