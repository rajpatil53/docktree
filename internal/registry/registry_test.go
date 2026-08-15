package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestEnsureAppWritesDocktreeRegistryShapeAndStableBands(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".config", "docktree", "registry.json")

	shop, err := EnsureApp(path, "shop", map[string]int{"api": 20000, "web": 21000})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(shop.Bands, map[string]int{"api": 20000, "web": 21000}) {
		t.Fatalf("shop bands = %#v", shop.Bands)
	}

	again, err := EnsureApp(path, "shop", map[string]int{"api": 23000, "worker": 22000})
	if err != nil {
		t.Fatal(err)
	}
	if again.Bands["api"] != 20000 {
		t.Fatalf("api band changed on re-request: %#v", again.Bands)
	}
	if again.Bands["worker"] != 22000 {
		t.Fatalf("missing service band not added: %#v", again.Bands)
	}

	var doc struct {
		Apps map[string]App `json:"apps"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("registry json invalid: %v\n%s", err, data)
	}
	if _, ok := doc.Apps["shop"]; !ok || doc.Apps["shop"].Bands["api"] != 20000 {
		t.Fatalf("registry shape = %#v", doc)
	}
}

func TestEnsureAppAllocatesUniquePerAppBands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")

	first, err := EnsureApp(path, "shop", map[string]int{"api": 20000})
	if err != nil {
		t.Fatal(err)
	}
	second, err := EnsureApp(path, "crm", map[string]int{"api": 20000})
	if err != nil {
		t.Fatal(err)
	}
	if first.Bands["api"] == second.Bands["api"] {
		t.Fatalf("bands collide: first=%#v second=%#v", first.Bands, second.Bands)
	}
}

func TestEnsureSharedPortsAreStableAndBandShifted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")

	free := func(port int) bool { return port != 6379 }
	shop, err := EnsureSharedPorts(path, "shop", map[string]int{"postgres": 5432, "redis": 6379}, free)
	if err != nil {
		t.Fatal(err)
	}
	if shop["postgres"] != 5432 {
		t.Fatalf("postgres shared port = %d, want canonical 5432", shop["postgres"])
	}
	if shop["redis"] != 7379 {
		t.Fatalf("redis shared port = %d, want shifted 7379", shop["redis"])
	}

	crm, err := EnsureSharedPorts(path, "crm", map[string]int{"postgres": 5432}, func(int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if crm["postgres"] != 6432 {
		t.Fatalf("second app postgres shared port = %d, want shifted 6432", crm["postgres"])
	}

	again, err := EnsureSharedPorts(path, "shop", map[string]int{"postgres": 5432}, func(int) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if again["postgres"] != 5432 {
		t.Fatalf("stable shared port changed: %#v", again)
	}
}

func TestMissingRegistrySelfCreatesAndCorruptRegistryFailsClosed(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing", "registry.json")
	if _, err := EnsureApp(missing, "shop", map[string]int{"api": 20000}); err != nil {
		t.Fatalf("missing registry should self-create: %v", err)
	}

	corrupt := filepath.Join(dir, "registry.json")
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureApp(corrupt, "shop", map[string]int{"api": 20000}); err == nil {
		t.Fatal("corrupt registry should fail closed instead of overwriting existing reservations")
	}
}

func TestRegistryAtomicWriteAndPerAppLockFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")

	if _, err := EnsureApp(path, "my/app", map[string]int{"api": 20000}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(LockPath(path, "my/app")); err != nil {
		t.Fatalf("expected per-app lock file: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp") {
			t.Fatalf("atomic temp file was not cleaned up: %s", entry.Name())
		}
	}
}
