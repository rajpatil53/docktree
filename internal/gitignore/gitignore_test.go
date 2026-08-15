package gitignore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureDocktreeCreatesGitignore(t *testing.T) {
	root := t.TempDir()

	changed, err := EnsureDocktree(root)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected new .gitignore to be reported as changed")
	}
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != ".docktree/\n" {
		t.Fatalf(".gitignore = %q", data)
	}
}

func TestEnsureDocktreeAppendsOnce(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".gitignore")
	if err := os.WriteFile(path, []byte("node_modules\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := EnsureDocktree(root)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected first ensure to append")
	}
	changed, err = EnsureDocktree(root)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("second ensure should be idempotent")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), ".docktree/") != 1 {
		t.Fatalf(".docktree entry count in %q", data)
	}
	if string(data) != "node_modules\n.docktree/\n" {
		t.Fatalf(".gitignore = %q", data)
	}
}
