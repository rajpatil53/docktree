package cmd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rajpatil53/docktree/internal/paths"
)

func TestDownStopsOnlyWorktreeProject(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeBasicProject(t, root)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{configOutput: []byte(basicComposeConfigJSON)}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runDown(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}

	argv := commandArgvStrings(fr.commands)
	if len(argv) != 1 {
		t.Fatalf("commands = %#v, want only one worktree down command", argv)
	}
	want := "docker compose --env-file " + paths.EnvFile(root) + " -p shop-feature_one -f " + paths.WorktreeCompose(root) + " down"
	if argv[0] != want {
		t.Fatalf("down argv = %q, want %q", argv[0], want)
	}
	if strings.Contains(argv[0], "shop-infra") || strings.Contains(argv[0], "compose.infra") {
		t.Fatalf("down touched shared infra: %q", argv[0])
	}
}

func TestDownRejectsUnexpectedArgs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeBasicProject(t, root)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{configOutput: []byte(basicComposeConfigJSON)}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	err := runDown(context.Background(), []string{"other-worktree"}, deps)
	if err == nil || !strings.Contains(err.Error(), "usage: docktree down") {
		t.Fatalf("runDown error = %v, want usage", err)
	}
	if len(fr.commands) != 0 {
		t.Fatalf("down ran commands despite unexpected args: %#v", commandArgvStrings(fr.commands))
	}
}
