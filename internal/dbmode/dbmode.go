// Package dbmode holds reusable pure SQL helpers for Docktree's Postgres
// fork/unfork fast path. The actual docker/psql shell-outs live in command
// code; everything here is side-effect-free and unit-tested so the
// correctness-critical choices remain verifiable without a live database.
package dbmode

import (
	"fmt"
	"regexp"
)

var identifierRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// EffectiveDevDB returns the dev database a worktree should connect to.
//
//	share    -> main's DB (<app>_<mainSlug>) — zero-cost shared default
//	isolated -> the worktree's own DB (<app>_<slug>)
//
// devDBTemplate/testDB derivation is the caller's (it already applied the
// manifest name_template); here we only pick share-vs-own by NAME.
func EffectiveDevDB(mode, ownDevDB, mainDevDB string) string {
	if mode == "isolated" {
		return ownDevDB
	}
	return mainDevDB // share (and any unknown mode) defaults to the shared DB
}

// CloneFastSQL is the guarded TEMPLATE fast-path. Postgres refuses CREATE ...
// TEMPLATE while the source has active connections, so callers fall back to
// CloneFallbackCmd on failure. Callers must check that dst does not exist before
// running this; fork is intentionally not a destructive operation.
func CloneFastSQL(src, dst string) (string, error) {
	srcIdent, err := QuoteIdentifier(src)
	if err != nil {
		return "", err
	}
	dstIdent, err := QuoteIdentifier(dst)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("CREATE DATABASE %s WITH TEMPLATE %s;", dstIdent, srcIdent), nil
}

func DatabaseExistsSQL(db string) (string, error) {
	if err := ValidateIdentifier(db); err != nil {
		return "", err
	}
	return fmt.Sprintf("SELECT 1 FROM pg_database WHERE datname = %s;", shellQuote(db)), nil
}

// CloneFallbackCmd is the pg_dump|psql pipe (run inside the infra container so
// the binaries match the server version). createdb first, then stream. superuser
// is the Postgres role to connect as; it is validated as a safe identifier
// because it is interpolated into a shell command line.
func CloneFallbackCmd(src, dst, superuser string) (string, error) {
	if err := ValidateIdentifier(src); err != nil {
		return "", err
	}
	if err := ValidateIdentifier(dst); err != nil {
		return "", err
	}
	if err := ValidateIdentifier(superuser); err != nil {
		return "", err
	}
	return fmt.Sprintf("set -e; dump=$(mktemp); trap 'rm -f \"$dump\"' EXIT; createdb -U %[1]s -- %[2]s; pg_dump -U %[1]s --file=\"$dump\" -- %[3]s; psql -U %[1]s -d %[2]s --file=\"$dump\"", superuser, shellQuote(dst), shellQuote(src)), nil
}

// DropSQL force-drops a database (terminates active connections).
func DropSQL(db string) (string, error) {
	ident, err := QuoteIdentifier(db)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE);", ident), nil
}

func ValidateIdentifier(name string) error {
	if !identifierRe.MatchString(name) {
		return fmt.Errorf("unsafe postgres identifier %q", name)
	}
	return nil
}

func QuoteIdentifier(name string) (string, error) {
	if err := ValidateIdentifier(name); err != nil {
		return "", err
	}
	return `"` + name + `"`, nil
}

func shellQuote(value string) string {
	return "'" + value + "'"
}

// CheckIsolatable refuses isolating/sharing the main worktree — its DB is THE
// shared DB every share-mode worktree depends on (F2).
func CheckIsolatable(slug, mainSlug string) error {
	if slug == mainSlug {
		return fmt.Errorf("refusing to isolate/share the main worktree %q (its DB is shared by every share-mode worktree)", slug)
	}
	return nil
}

// ShouldDropDevDB decides whether teardown may drop the per-worktree dev DB.
// Only an isolated, non-main worktree owns its dev DB; share mode points at
// main's DB (never drop), and an unknown/blank mode is treated as unsafe (F2).
func ShouldDropDevDB(mode, slug, mainSlug string) bool {
	return mode == "isolated" && slug != mainSlug
}
