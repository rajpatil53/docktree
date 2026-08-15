package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rajpatil53/docktree/internal/ports"
)

func TestInitWritesDocktreeEnvAndGitignoreWithoutTouchingAppEnv(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("APP_OWNED=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docktree.toml"), []byte(`
app = "flexiple"
shared = ["postgres"]

[services.api]
host_port_env = "API_PORT"

[stateful.postgres]
engine = "postgres"
source_db = "flexiple_source"
env = { POSTGRES_PRIMARY_DB = "flexiple_source_{slug}" }

[env]
OPENSEARCH_INDEX_PREFIX = "{slug}_"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWD)
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	fr := &fakeRunner{networkMissing: true}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)
	if err := runInit(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(root, ".docktree", ".env.worktree"))
	if err != nil {
		t.Fatal(err)
	}
	wantPort := strconv.Itoa(20000 + ports.Offset("feature_one", 1000))
	for _, want := range []string{
		"COMPOSE_PROJECT_NAME=flexiple-feature_one",
		"DOCKTREE_SHARED_NETWORK=flexiple_shared",
		"DOCKTREE_WORKTREE_NETWORK=flexiple-feature_one_default",
		"API_PORT=" + wantPort,
		"OPENSEARCH_INDEX_PREFIX=feature_one_",
	} {
		if !strings.Contains(string(data), want+"\n") {
			t.Fatalf(".docktree/.env.worktree missing %q:\n%s", want, data)
		}
	}
	if strings.Contains(string(data), "DATABASE_URL=") {
		t.Fatalf(".docktree/.env.worktree should not contain stateful DATABASE_URL:\n%s", data)
	}
	if strings.Contains(string(data), "POSTGRES_PRIMARY_DB=") {
		t.Fatalf(".docktree/.env.worktree should not contain shared-mode stateful env:\n%s", data)
	}

	appEnv, err := os.ReadFile(filepath.Join(root, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(appEnv) != "APP_OWNED=1\n" {
		t.Fatalf("app .env was modified: %q", appEnv)
	}
	gitignore, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gitignore) != ".docktree/\n" {
		t.Fatalf(".gitignore = %q", gitignore)
	}
}

func TestInitScaffoldsConfigCreatesNetworkAndSeedsEnvOnlyWhenRequested(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Shop")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	if err := os.WriteFile(filepath.Join(root, ".env.example"), []byte("APP_ENV=dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRunner{networkMissing: true}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)
	if err := runInit(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "docktree.toml")); err != nil {
		t.Fatalf("docktree.toml was not scaffolded: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".docktree")); err != nil {
		t.Fatalf(".docktree was not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".env")); !os.IsNotExist(err) {
		t.Fatalf(".env should not be seeded without --seed-env, stat err=%v", err)
	}
	if !containsArgv(commandArgvStrings(fr.commands), "docker network create shop_shared") {
		t.Fatalf("network create command missing: %#v", commandArgvStrings(fr.commands))
	}

	fr = &fakeRunner{networkMissing: true}
	events = nil
	deps = newCommandTestDeps(root, fr, &events)
	if err := runInit(context.Background(), []string{"--seed-env"}, deps); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "APP_ENV=dev\n" {
		t.Fatalf(".env = %q", data)
	}

	if err := os.WriteFile(filepath.Join(root, ".env.example"), []byte("APP_ENV=changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runInit(context.Background(), []string{"--seed-env"}, deps); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(root, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "APP_ENV=dev\n" {
		t.Fatalf("existing .env was overwritten: %q", data)
	}
}

func TestInitDiscoversComposePortTokensWhenScaffoldingConfig(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{configOutput: []byte(basicComposeConfigJSON)}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runInit(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(root, ".docktree", ".env.worktree"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "API_PORT=") || !strings.Contains(string(data), "POSTGRES_PORT=") {
		t.Fatalf("init did not write compose-discovered ports:\n%s", data)
	}
	cfgData, err := os.ReadFile(filepath.Join(root, "docktree.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfgData), `[services."api"]`) || !strings.Contains(string(cfgData), `host_port_env = "API_PORT"`) {
		t.Fatalf("init did not scaffold discovered service config:\n%s", cfgData)
	}
}
