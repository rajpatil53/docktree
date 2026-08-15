package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rajpatil53/docktree/internal/compose"
	"github.com/rajpatil53/docktree/internal/config"
	"github.com/rajpatil53/docktree/internal/envfile"
	"github.com/rajpatil53/docktree/internal/paths"
)

func TestResolveDerivesDocktreeIdentityAndIgnoresLegacyState(t *testing.T) {
	wd := filepath.Join(t.TempDir(), "Feat-One")
	if err := os.MkdirAll(filepath.Join(wd, ".docktree"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	if err := os.WriteFile(filepath.Join(wd, ".docktree", "state"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wd, "docktree.toml"), []byte(`
app = "flexiple"
shared = ["postgres"]

[services.api]
host_port_env = "API_PORT"

[stateful.postgres]
engine = "postgres"
source_db = "flexiple_source"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := resolve(wd)
	if err != nil {
		t.Fatal(err)
	}
	if r.slug != "feat_one" || r.mainSlug != "feat_one" {
		t.Fatalf("slug/mainSlug = %q/%q, want feat_one/feat_one", r.slug, r.mainSlug)
	}
	if r.names.Project != "flexiple-feat_one" || r.names.InfraProject != "flexiple-infra" {
		t.Fatalf("projects = %q/%q", r.names.Project, r.names.InfraProject)
	}
	if r.names.SharedNetwork != "flexiple_shared" {
		t.Fatalf("shared network = %q", r.names.SharedNetwork)
	}
	if r.manifest.Services["api"].HostPortEnv != "API_PORT" {
		t.Fatalf("api service config = %+v", r.manifest.Services["api"])
	}
	if r.mode != "shared" {
		t.Fatalf("mode = %q, want shared", r.mode)
	}
}

func TestResolveUsesAppProjectForMainGitWorktree(t *testing.T) {
	main, feature := writeGitBackedBasicProject(t)
	t.Setenv("HOME", t.TempDir())

	mainResolved, err := resolveWithFree(main, func(int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if mainResolved.names.Project != "shop" {
		t.Fatalf("main project = %q, want app project shop", mainResolved.names.Project)
	}

	featureResolved, err := resolveWithFree(feature, func(int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if featureResolved.names.Project != "shop-feature_one" {
		t.Fatalf("feature project = %q, want slugged project shop-feature_one", featureResolved.names.Project)
	}
}

func TestResolvedStatefulAndPortHelperDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := &config.Config{
		App:    "shop",
		Shared: []string{"postgres", "redis"},
		Services: map[string]config.Service{
			"api":      {HostPortEnv: "API_PORT"},
			"worker":   {},
			"postgres": {HostPortEnv: "POSTGRES_PORT"},
			"redis":    {HostPortEnv: "REDIS_PORT"},
		},
		Stateful: map[string]config.Stateful{
			"postgres": {DefaultStrategy: "isolated"},
			"redis":    {},
		},
		DB: config.DB{NameTemplate: "{app}_{slug}", TestSuffix: "_test"},
	}

	if got := defaultStatefulModes(m); got["postgres"] != "shared" || got["redis"] != "shared" {
		t.Fatalf("default stateful modes = %#v", got)
	}
	r := &resolved{
		manifest: m,
		slug:     "feature_one",
		mainSlug: "main",
	}
	if mode := statefulMode(r, "postgres"); mode != "shared" {
		t.Fatalf("postgres mode = %q, want shared before a fork is derived or provisioned", mode)
	}
	setStatefulMode(r, "postgres", "isolated")
	if mode := statefulMode(r, "postgres"); mode != "isolated" {
		t.Fatalf("overridden postgres mode = %q, want isolated", mode)
	}

	bands := defaultWorktreeServiceBands(m)
	if len(bands) != 1 || bands["api"] == 0 {
		t.Fatalf("worktree service bands = %#v, want api only", bands)
	}
	servicePorts, err := resolveServicePortsWithFree(m, "feature_one", func(int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if servicePorts["api"] == 0 {
		t.Fatalf("service ports = %#v, want api resolved", servicePorts)
	}
	sharedPorts, err := resolveSharedPortsWithFree(m, func(int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if sharedPorts["postgres"] != 5432 || sharedPorts["redis"] != 6379 {
		t.Fatalf("shared ports = %#v, want canonical postgres and redis", sharedPorts)
	}
	if canonicalSharedPort("opensearch") != 9200 || canonicalSharedPort("unknown") != 0 {
		t.Fatalf("canonical ports were not resolved as expected")
	}
}

func TestResolveWithModelSeedsPortlessComposeServices(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeBasicProject(t, root)
	t.Setenv("HOME", t.TempDir())
	model, err := compose.ParseConfigJSON([]byte(`{
		"services": {
			"api": {"ports": [{"target": 3000, "published": "${API_PORT:-3000}"}]},
			"worker": {}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}

	r, err := resolveWithModel(root, model, func(int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.manifest.Services["worker"]; !ok {
		t.Fatalf("portless compose service was not seeded into resolved services: %#v", r.manifest.Services)
	}
}

func TestDiscoveredPortInputsFromComposeTokens(t *testing.T) {
	model, err := compose.ParseConfigJSON([]byte(`{
		"services": {
			"api": {"ports": [{"target": 3000, "published": "${API_PORT:-3000}"}]},
			"postgres": {"ports": [{"target": 5432, "published": "${POSTGRES_PORT:-5432}"}]},
			"worker": {"ports": [{"target": 5555, "published": "5555"}]}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		App:    "shop",
		Shared: []string{"postgres"},
		Services: map[string]config.Service{
			"api": {Fixed: true},
		},
	}

	inputs := discoveredPortInputs(cfg, model)
	if len(inputs) != 2 {
		t.Fatalf("inputs = %#v, want two tokenized services", inputs)
	}
	if inputs[0].Service != "api" || inputs[0].Env != "API_PORT" || inputs[0].Target != 3000 || inputs[0].Default != 3000 || inputs[0].Shared || !inputs[0].Fixed {
		t.Fatalf("api input = %#v", inputs[0])
	}
	if inputs[1].Service != "postgres" || inputs[1].Env != "POSTGRES_PORT" || inputs[1].Target != 5432 || inputs[1].Default != 5432 || !inputs[1].Shared {
		t.Fatalf("postgres input = %#v", inputs[1])
	}
}

func TestResolveReusesExistingEnvPortsEvenWhenAlreadyBound(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeBasicProject(t, root)
	t.Setenv("HOME", t.TempDir())
	if err := envfile.Write(paths.EnvFile(root), [][2]string{
		{"COMPOSE_PROJECT_NAME", "shop-feature_one"},
		{"DOCKTREE_SHARED_NETWORK", "shop_shared"},
		{"API_PORT", "21234"},
	}); err != nil {
		t.Fatal(err)
	}

	r, err := resolveWithFree(root, func(port int) bool {
		return port != 21234
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.servicePorts["api"]; got != 21234 {
		t.Fatalf("api port = %d, want persisted .docktree/.env.worktree port 21234", got)
	}
}

func TestResolvePortInputsReservesWorktreeAndSharedPorts(t *testing.T) {
	registryFile := filepath.Join(t.TempDir(), "registry.json")
	worktree, shared, primaryDefaults, err := resolvePortInputs(registryFile, "shop", "feature_one", "shop_main", []portInput{
		{Service: "api", Env: "API_PORT", Target: 3000},
		{Service: "postgres", Env: "POSTGRES_PORT", Target: 5432, Shared: true},
	}, func(int) bool { return true }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if worktree["api"] == 0 {
		t.Fatalf("api worktree port was not resolved: %#v", worktree)
	}
	if shared["postgres"] != 5432 {
		t.Fatalf("postgres shared port = %d, want 5432", shared["postgres"])
	}
	if len(primaryDefaults) != 0 {
		t.Fatalf("primary defaults = %#v, want none for non-main worktree", primaryDefaults)
	}

	_, _, _, err = resolvePortInputs(registryFile, "shop", "feature_one", "shop_main", []portInput{
		{Service: "oauth", Env: "OAUTH_PORT", Target: 3000, Fixed: true},
	}, func(int) bool { return false }, nil)
	if err == nil {
		t.Fatal("fixed service should fail when its resolved port is unavailable")
	}
}

func TestResolvePortInputsUsesMainWorktreeTokenDefault(t *testing.T) {
	registryFile := filepath.Join(t.TempDir(), "registry.json")
	worktree, _, primaryDefaults, err := resolvePortInputs(registryFile, "shop", "shop_main", "shop_main", []portInput{
		{Service: "api", Env: "API_PORT", Target: 3000, Default: 3000},
	}, func(int) bool { return true }, map[string]int{"API_PORT": 21234})
	if err != nil {
		t.Fatal(err)
	}
	if got := worktree["api"]; got != 3000 {
		t.Fatalf("main api port = %d, want compose token default 3000", got)
	}
	if !primaryDefaults["api"] {
		t.Fatalf("primary defaults = %#v, want api marked as default-selected", primaryDefaults)
	}
}

func TestResolvePortInputsRejectsTakenMainWorktreeTokenDefault(t *testing.T) {
	registryFile := filepath.Join(t.TempDir(), "registry.json")
	_, _, _, err := resolvePortInputs(registryFile, "shop", "shop_main", "shop_main", []portInput{
		{Service: "api", Env: "API_PORT", Target: 3000, Default: 3000},
	}, func(port int) bool { return port != 3000 }, nil)
	if err == nil || !strings.Contains(err.Error(), "main worktree") || !strings.Contains(err.Error(), "3000") {
		t.Fatalf("resolvePortInputs error = %v, want main worktree default-port collision", err)
	}
}

func TestResolvePortInputsPreservesNonMainExistingEnvOverTokenDefault(t *testing.T) {
	registryFile := filepath.Join(t.TempDir(), "registry.json")
	worktree, _, primaryDefaults, err := resolvePortInputs(registryFile, "shop", "feature_one", "shop_main", []portInput{
		{Service: "api", Env: "API_PORT", Target: 3000, Default: 3000},
	}, func(port int) bool { return port != 21234 }, map[string]int{"API_PORT": 21234})
	if err != nil {
		t.Fatal(err)
	}
	if got := worktree["api"]; got != 21234 {
		t.Fatalf("feature api port = %d, want persisted .docktree/.env.worktree port 21234", got)
	}
	if len(primaryDefaults) != 0 {
		t.Fatalf("primary defaults = %#v, want none for non-main worktree", primaryDefaults)
	}
}
