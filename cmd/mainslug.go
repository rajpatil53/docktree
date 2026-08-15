package cmd

import (
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rajpatil53/docktree/internal/identity"
)

// primarySlugFromCommonDir derives the primary (main) worktree's slug from the
// output of `git rev-parse --git-common-dir`, which is always
// "<primary-worktree>/.git" regardless of which worktree git runs in. The
// primary worktree (the original clone) is the canonical owner of the shared
// dev DB, so its slug is what share-mode worktrees point at.
func primarySlugFromCommonDir(commonDir string) string {
	return identity.ComputeSlug(filepath.Base(filepath.Dir(commonDir)))
}

// resolveMainSlug returns the slug of the primary worktree — the single source
// of truth for which DB share-mode worktrees point at. It uses
// `git rev-parse --git-common-dir` (CWD-independent: every linked worktree
// resolves to the SAME primary, hence the SAME shared DB). fallbackSlug (the
// current slug) is used only if git can't be reached / returns nothing usable.
func resolveMainSlug(fallbackSlug string) string {
	return resolveMainSlugInDir("", fallbackSlug)
}

func resolveMainSlugInDir(wd, fallbackSlug string) string {
	mainSlug, _ := resolveMainSlugAndPrimaryInDir(wd, fallbackSlug)
	return mainSlug
}

func resolveMainSlugAndPrimaryInDir(wd, fallbackSlug string) (string, bool) {
	cmd := exec.Command("git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	if wd != "" {
		cmd.Dir = wd
	}
	out, err := cmd.Output()
	if err != nil {
		return fallbackSlug, false
	}
	commonDir := strings.TrimSpace(string(out))
	if commonDir == "" || !filepath.IsAbs(commonDir) {
		return fallbackSlug, false
	}
	primaryRoot := physicalPath(filepath.Dir(commonDir))
	currentRoot := ""
	if wd != "" {
		if abs, err := filepath.Abs(wd); err == nil {
			currentRoot = physicalPath(abs)
		}
	}
	return primarySlugFromCommonDir(commonDir), currentRoot != "" && currentRoot == primaryRoot
}

func physicalPath(path string) string {
	clean := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return filepath.Clean(resolved)
	}
	return clean
}
