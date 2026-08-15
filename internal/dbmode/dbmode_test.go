package dbmode

import (
	"strings"
	"testing"
)

func TestEffectiveDevDB(t *testing.T) {
	own, main := "flexiple_feat_x", "flexiple_flexiple_platform"
	if got := EffectiveDevDB("isolated", own, main); got != own {
		t.Errorf("isolated -> %q, want %q", got, own)
	}
	if got := EffectiveDevDB("share", own, main); got != main {
		t.Errorf("share -> %q, want %q", got, main)
	}
	// Unknown/blank mode must default to the shared DB (never the own DB by
	// accident — share is the safe default).
	if got := EffectiveDevDB("", own, main); got != main {
		t.Errorf("blank mode -> %q, want shared %q", got, main)
	}
}

func TestCloneCommands(t *testing.T) {
	fast, err := CloneFastSQL("flexiple_flexiple_platform", "flexiple_feat_x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fast, "TEMPLATE") || strings.Contains(fast, "DROP DATABASE") {
		t.Fatalf("fast path wrong: %q", fast)
	}
	if !strings.Contains(fast, `CREATE DATABASE "flexiple_feat_x" WITH TEMPLATE "flexiple_flexiple_platform"`) {
		t.Fatalf("fast path should back docktree fork: %q", fast)
	}
	fb, err := CloneFallbackCmd("flexiple_flexiple_platform", "flexiple_feat_x", "primary")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fb, "pg_dump") || !strings.Contains(fb, "psql") || !strings.Contains(fb, "createdb") {
		t.Fatalf("fallback wrong: %q", fb)
	}
	if strings.Contains(fb, " | ") {
		t.Fatalf("fallback must not hide pg_dump failures behind a pipe: %q", fb)
	}
	if !strings.Contains(fb, "set -e") || !strings.Contains(fb, "--file=") {
		t.Fatalf("fallback should fail fast and restore from a checked dump file: %q", fb)
	}
	if !strings.Contains(fb, "createdb -U primary -- 'flexiple_feat_x'") {
		t.Fatalf("fallback should shell-quote db names: %q", fb)
	}
	exists, err := DatabaseExistsSQL("flexiple_feat_x")
	if err != nil {
		t.Fatal(err)
	}
	if exists != "SELECT 1 FROM pg_database WHERE datname = 'flexiple_feat_x';" {
		t.Fatalf("exists SQL = %q", exists)
	}
}

func TestCloneFallbackHonorsSuperuser(t *testing.T) {
	fb, err := CloneFallbackCmd("shop_shared", "shop_feat_x", "appuser")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"createdb -U appuser", "pg_dump -U appuser", "psql -U appuser"} {
		if !strings.Contains(fb, want) {
			t.Fatalf("fallback missing %q: %q", want, fb)
		}
	}
	if strings.Contains(fb, "-U primary") {
		t.Fatalf("fallback must not hardcode a superuser: %q", fb)
	}
}

func TestCloneFallbackRejectsUnsafeSuperuser(t *testing.T) {
	for _, bad := range []string{"app user", "x; rm -rf /", "", "-flag", "u'`"} {
		if _, err := CloneFallbackCmd("safe_src", "safe_dst", bad); err == nil {
			t.Fatalf("CloneFallbackCmd accepted unsafe superuser %q", bad)
		}
	}
}

func TestDropSQLForce(t *testing.T) {
	got, err := DropSQL("flexiple_feat_x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "WITH (FORCE)") || !strings.Contains(got, `"flexiple_feat_x"`) {
		t.Fatalf("DropSQL must force: %q", got)
	}
}

func TestCloneCommandsRejectUnsafeIdentifiers(t *testing.T) {
	for _, name := range []string{"bad-name", "x; DROP DATABASE postgres;--", "", "9starts_with_digit"} {
		if _, err := CloneFastSQL("safe_name", name); err == nil {
			t.Fatalf("CloneFastSQL accepted unsafe identifier %q", name)
		}
		if _, err := CloneFallbackCmd("safe_name", name, "primary"); err == nil {
			t.Fatalf("CloneFallbackCmd accepted unsafe identifier %q", name)
		}
		if _, err := DropSQL(name); err == nil {
			t.Fatalf("DropSQL accepted unsafe identifier %q", name)
		}
		if _, err := DatabaseExistsSQL(name); err == nil {
			t.Fatalf("DatabaseExistsSQL accepted unsafe identifier %q", name)
		}
	}
}

func TestCheckIsolatableRefusesMain(t *testing.T) {
	if err := CheckIsolatable("flexiple_platform", "flexiple_platform"); err == nil {
		t.Fatal("isolate/share on the main worktree must be refused (F2)")
	}
	if err := CheckIsolatable("feat_x", "flexiple_platform"); err != nil {
		t.Fatalf("normal worktree should be isolatable: %v", err)
	}
}

func TestShouldDropDevDB(t *testing.T) {
	if !ShouldDropDevDB("isolated", "feat_x", "flexiple_platform") {
		t.Fatal("isolated worktree dev DB should be dropped")
	}
	if ShouldDropDevDB("share", "feat_x", "flexiple_platform") {
		t.Fatal("share-mode worktree must NOT drop the shared dev DB (F2)")
	}
	if ShouldDropDevDB("", "feat_x", "flexiple_platform") {
		t.Fatal("unknown mode must not drop")
	}
	if ShouldDropDevDB("isolated", "flexiple_platform", "flexiple_platform") {
		t.Fatal("must never drop the main slug's DB even if marked isolated")
	}
}
