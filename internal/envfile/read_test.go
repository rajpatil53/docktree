package envfile

import (
	"path/filepath"
	"testing"
)

func TestReadParsesKeyValueAndMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	// Missing file → empty map, no error.
	m, err := Read(path)
	if err != nil || len(m) != 0 {
		t.Fatalf("missing file: m=%v err=%v", m, err)
	}

	if err := Write(path, [][2]string{
		{"SLUG", "flexiple_platform"},
		{"DATABASE_URL", "postgres://primary:password@localhost:5432/flexiple_flexiple_platform"},
	}); err != nil {
		t.Fatal(err)
	}
	m, err = Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if m["SLUG"] != "flexiple_platform" {
		t.Errorf("SLUG = %q", m["SLUG"])
	}
	// Value containing '=' or ':' is preserved verbatim after the first '='.
	if m["DATABASE_URL"] != "postgres://primary:password@localhost:5432/flexiple_flexiple_platform" {
		t.Errorf("DATABASE_URL = %q", m["DATABASE_URL"])
	}
}
