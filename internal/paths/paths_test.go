package paths

import (
	"path/filepath"
	"testing"
)

func TestWorktreePaths(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "repos", "app")

	cases := []struct {
		name string
		got  string
		want string
	}{
		{"env", EnvFile(root), filepath.Join(root, ".docktree", ".env.worktree")},
		{"worktree compose", WorktreeCompose(root), filepath.Join(root, ".docktree", "compose.worktree.yml")},
		{"infra compose", InfraCompose(root), filepath.Join(root, ".docktree", "compose.infra.yml")},
		{"config", ConfigFile(root), filepath.Join(root, "docktree.toml")},
	}

	for _, tc := range cases {
		if tc.got != tc.want {
			t.Fatalf("%s path = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestRegistryPath(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "Users", "raj")
	want := filepath.Join(home, ".config", "docktree", "registry.json")
	if got := RegistryFile(home); got != want {
		t.Fatalf("registry path = %q, want %q", got, want)
	}
}

func TestProxyComposePath(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "Users", "raj")
	want := filepath.Join(home, ".config", "docktree", "proxy", "compose.proxy.yml")
	if got := ProxyCompose(home); got != want {
		t.Fatalf("proxy compose path = %q, want %q", got, want)
	}
}
