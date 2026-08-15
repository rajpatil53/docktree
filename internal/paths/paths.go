package paths

import "path/filepath"

const (
	WorktreeDirName     = ".docktree"
	ConfigFileName      = "docktree.toml"
	EnvFileName         = ".env.worktree"
	InfraEnvFileName    = ".env.infra"
	WorktreeComposeName = "compose.worktree.yml"
	InfraComposeName    = "compose.infra.yml"
	RegistryFileName    = "registry.json"
	ProxyComposeName    = "compose.proxy.yml"
)

func WorktreeDir(root string) string {
	return filepath.Join(root, WorktreeDirName)
}

func EnvFile(root string) string {
	return filepath.Join(WorktreeDir(root), EnvFileName)
}

// InfraEnvFile is the worktree-INVARIANT env artifact the infra projection
// consumes: identical content regardless of which worktree rendered it, so
// cross-worktree `up`s never change the infra services' config hash.
func InfraEnvFile(root string) string {
	return filepath.Join(WorktreeDir(root), InfraEnvFileName)
}

func WorktreeCompose(root string) string {
	return filepath.Join(WorktreeDir(root), WorktreeComposeName)
}

func InfraCompose(root string) string {
	return filepath.Join(WorktreeDir(root), InfraComposeName)
}

func ConfigFile(root string) string {
	return filepath.Join(root, ConfigFileName)
}

func RegistryFile(home string) string {
	return filepath.Join(home, ".config", "docktree", RegistryFileName)
}

// ProxyDir is the machine-global directory that holds the docktree-proxy compose
// projection, alongside the registry. The reverse proxy is a per-machine
// singleton, so it lives outside any worktree.
func ProxyDir(home string) string {
	return filepath.Join(home, ".config", "docktree", "proxy")
}

func ProxyCompose(home string) string {
	return filepath.Join(ProxyDir(home), ProxyComposeName)
}
