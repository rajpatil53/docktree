// Package identity computes the worktree slug and all derived names. It is the
// single source of truth that replaces the duplicated bash in
// bin/worktree-init.sh and bin/wt-remove.sh.
package identity

import (
	"fmt"
	"hash/fnv"
	"path/filepath"
	"strings"
)

func ComputeSlug(basename string) string {
	return ComputeSlugWithHash(basename, FNV32a)
}

type Hash32 func(string) uint32

func ComputeSlugWithHash(basename string, hash Hash32) string {
	var b strings.Builder
	for _, r := range basename {
		switch {
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	slug := strings.Trim(b.String(), "_")
	if slug == "" {
		slug = "main"
	}
	if len(slug) > 40 {
		slug = slug[:30] + "_" + fmt.Sprintf("%08x", hash(slug))
	}
	return slug
}

func FNV32a(value string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(value))
	return h.Sum32()
}

func AppName(explicit, repoRoot string) string {
	if explicit != "" {
		return ComputeSlug(explicit)
	}
	return ComputeSlug(filepath.Base(repoRoot))
}
