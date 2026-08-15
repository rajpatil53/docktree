package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rajpatil53/docktree/internal/paths"
)

func TestPsLogsAndExecWrapWorktreeComposeProject(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeBasicProject(t, root)
	t.Setenv("HOME", t.TempDir())

	cases := []struct {
		name string
		run  func(context.Context, []string, commandDeps) error
		args []string
		want string
	}{
		{
			name: "ps",
			run:  runPS,
			args: []string{"--services"},
			want: "docker compose --env-file " + paths.EnvFile(root) + " -p shop-feature_one -f " + paths.WorktreeCompose(root) + " ps --services",
		},
		{
			name: "ps documented service flag",
			run:  runPS,
			args: []string{"--service"},
			want: "docker compose --env-file " + paths.EnvFile(root) + " -p shop-feature_one -f " + paths.WorktreeCompose(root) + " ps --services",
		},
		{
			name: "logs",
			run:  runLogs,
			args: []string{"api", "-f"},
			want: "docker compose --env-file " + paths.EnvFile(root) + " -p shop-feature_one -f " + paths.WorktreeCompose(root) + " logs api -f",
		},
		{
			name: "exec",
			run:  runExec,
			args: []string{"api", "--", "bin/rails", "console"},
			want: "docker compose --env-file " + paths.EnvFile(root) + " -p shop-feature_one -f " + paths.WorktreeCompose(root) + " exec api -- bin/rails console",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeRunner{}
			var events []string
			deps := newCommandTestDeps(root, fr, &events)
			if err := tc.run(context.Background(), tc.args, deps); err != nil {
				t.Fatal(err)
			}
			if !containsArgv(commandArgvStrings(fr.commands), tc.want) {
				t.Fatalf("commands = %#v, want %q", commandArgvStrings(fr.commands), tc.want)
			}
		})
	}
}

func TestLogsTreatsComposeDiscoveredServiceAsServiceArgument(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docktree.toml"), []byte("app = \"shop\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{configOutput: []byte(basicComposeConfigJSON)}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runLogs(context.Background(), []string{"api", "-f"}, deps); err != nil {
		t.Fatal(err)
	}
	want := "docker compose --env-file " + paths.EnvFile(root) + " -p shop-feature_one -f " + paths.WorktreeCompose(root) + " logs api -f"
	if !containsArgv(commandArgvStrings(fr.commands), want) {
		t.Fatalf("commands = %#v, want %q", commandArgvStrings(fr.commands), want)
	}
}

func TestLogsTreatsPortlessComposeDiscoveredServiceAsServiceArgument(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docktree.toml"), []byte("app = \"shop\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{configOutput: []byte(`{"services":{"worker":{}}}`)}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runLogs(context.Background(), []string{"worker", "-f"}, deps); err != nil {
		t.Fatal(err)
	}
	want := "docker compose --env-file " + paths.EnvFile(root) + " -p shop-feature_one -f " + paths.WorktreeCompose(root) + " logs worker -f"
	if !containsArgv(commandArgvStrings(fr.commands), want) {
		t.Fatalf("commands = %#v, want %q", commandArgvStrings(fr.commands), want)
	}
}

func TestCommandRootNormalizesSubdirectoriesToWorktreeRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeBasicProject(t, root)
	subdir := filepath.Join(root, "app", "models")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{configOutput: []byte(basicComposeConfigJSON)}
	var events []string
	deps := newCommandTestDeps(subdir, fr, &events)

	if err := runLogs(context.Background(), []string{"api", "-f"}, deps); err != nil {
		t.Fatal(err)
	}
	want := "docker compose --env-file " + paths.EnvFile(root) + " -p shop-feature_one -f " + paths.WorktreeCompose(root) + " logs api -f"
	if !containsArgv(commandArgvStrings(fr.commands), want) {
		t.Fatalf("commands = %#v, want %q", commandArgvStrings(fr.commands), want)
	}
}

func TestLogsReturnsComposeDiscoveryErrorsInsteadOfRetargetingService(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docktree.toml"), []byte("app = \"shop\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())

	configArgv := "docker compose -f " + filepath.Join(root, "compose.yaml") + " config --no-interpolate --no-normalize --format json"
	fr := &fakeRunner{outputErrByArgv: map[string]error{configArgv: errors.New("compose config failed")}}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	err := runLogs(context.Background(), []string{"api", "-f"}, deps)
	if err == nil || !strings.Contains(err.Error(), "compose config failed") {
		t.Fatalf("runLogs error = %v, want compose config failure", err)
	}
	if strings.Contains(strings.Join(commandArgvStrings(fr.commands), "\n"), "-p shop-api") {
		t.Fatalf("logs retargeted service name as worktree slug: %#v", commandArgvStrings(fr.commands))
	}
}

func TestPsTargetWorktreePathUsesTargetArtifactsAndProject(t *testing.T) {
	parent := t.TempDir()
	current := filepath.Join(parent, "Feature-One")
	target := filepath.Join(parent, "Feature-Two")
	writeBasicProject(t, current)
	writeBasicProject(t, target)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{configOutput: []byte(basicComposeConfigJSON)}
	var events []string
	deps := newCommandTestDeps(current, fr, &events)

	if err := runPS(context.Background(), []string{target, "--services"}, deps); err != nil {
		t.Fatal(err)
	}
	want := "docker compose --env-file " + paths.EnvFile(target) + " -p shop-feature_two -f " + paths.WorktreeCompose(target) + " ps --services"
	if !containsArgv(commandArgvStrings(fr.commands), want) {
		t.Fatalf("commands = %#v, want %q", commandArgvStrings(fr.commands), want)
	}
}
