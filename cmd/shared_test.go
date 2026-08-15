package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rajpatil53/docktree/internal/envfile"
	"github.com/rajpatil53/docktree/internal/paths"
)

func TestSharedCommandsUseInfraProjectArgv(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeBasicProject(t, root)
	t.Setenv("HOME", t.TempDir())

	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "up",
			args: []string{"up"},
			want: "docker compose --progress plain --env-file " + paths.InfraEnvFile(root) + " -p shop-infra -f " + paths.InfraCompose(root) + " up -d",
		},
		{
			name: "down",
			args: []string{"down"},
			want: "docker compose --env-file " + paths.InfraEnvFile(root) + " -p shop-infra -f " + paths.InfraCompose(root) + " down",
		},
		{
			name: "status",
			args: []string{"status"},
			want: "docker compose --env-file " + paths.InfraEnvFile(root) + " -p shop-infra -f " + paths.InfraCompose(root) + " ps",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeRunner{configOutput: []byte(basicComposeConfigJSON), networkMissing: true}
			var events []string
			deps := newCommandTestDeps(root, fr, &events)

			if err := runShared(context.Background(), tc.args, deps); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(paths.InfraCompose(root)); err != nil {
				t.Fatalf("shared %s did not render current infra projection: %v", tc.args[0], err)
			}
			if !containsArgv(commandArgvStrings(fr.commands), tc.want) {
				t.Fatalf("commands = %#v, want %q", commandArgvStrings(fr.commands), tc.want)
			}
		})
	}
}

func TestSharedNukeRequiresExplicitConfirmation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeBasicProject(t, root)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{configOutput: []byte(basicComposeConfigJSON)}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	err := runShared(context.Background(), []string{"nuke"}, deps)
	if err == nil {
		t.Fatal("shared nuke without confirmation returned nil")
	}
	if len(fr.commands) != 0 {
		t.Fatalf("unguarded nuke ran commands: %#v", commandArgvStrings(fr.commands))
	}

	if err := runShared(context.Background(), []string{"nuke", "--confirm", "shop-infra"}, deps); err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(commandArgvStrings(fr.commands), "\n")
	for _, want := range []string{
		"docker compose --env-file " + paths.InfraEnvFile(root) + " -p shop-infra -f " + paths.InfraCompose(root) + " down -v --remove-orphans",
		"docker network rm shop_shared",
	} {
		if !strings.Contains(argv, want) {
			t.Fatalf("nuke commands =\n%s\nwant %q", argv, want)
		}
	}
}

func TestSharedStatusAllowsPersistedMainWorktreePortAlreadyBound(t *testing.T) {
	root, _ := writeGitBackedBasicProject(t)
	t.Setenv("HOME", t.TempDir())
	if err := envfile.Write(paths.EnvFile(root), [][2]string{
		{"COMPOSE_PROJECT_NAME", "shop"},
		{"DOCKTREE_SHARED_NETWORK", "shop_shared"},
		{"API_PORT", "3000"},
	}); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRunner{configOutput: []byte(basicComposeConfigJSON)}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)
	deps.portFree = func(port int) bool {
		return port != 3000
	}

	if err := runShared(context.Background(), []string{"status"}, deps); err != nil {
		t.Fatal(err)
	}
	if !containsArgv(commandArgvStrings(fr.commands), "docker compose --env-file "+paths.InfraEnvFile(root)+" -p shop-infra -f "+paths.InfraCompose(root)+" ps") {
		t.Fatalf("commands = %#v, want shared status ps", commandArgvStrings(fr.commands))
	}
}

func TestSharedNonNukeCommandsRejectExtraArgs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeBasicProject(t, root)
	t.Setenv("HOME", t.TempDir())

	for _, args := range [][]string{
		{"up", "typo"},
		{"down", "typo"},
		{"status", "typo"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			fr := &fakeRunner{configOutput: []byte(basicComposeConfigJSON)}
			var events []string
			deps := newCommandTestDeps(root, fr, &events)

			err := runShared(context.Background(), args, deps)
			if err == nil || !strings.Contains(err.Error(), "usage: docktree shared") {
				t.Fatalf("runShared(%v) error = %v, want usage", args, err)
			}
			if len(fr.commands) != 0 {
				t.Fatalf("shared command ran despite extra args: %#v", commandArgvStrings(fr.commands))
			}
		})
	}
}
