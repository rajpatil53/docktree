package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMinimalConfigDefaultsAppFromRepoName(t *testing.T) {
	root := filepath.Join(t.TempDir(), "My-App")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "docktree.toml")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.App != "my_app" {
		t.Fatalf("App = %q, want fallback my_app", cfg.App)
	}
	if cfg.Compose != "" {
		t.Fatalf("Compose = %q, want empty autodiscovery default", cfg.Compose)
	}
	if len(cfg.Shared) != 0 {
		t.Fatalf("Shared = %v, want empty", cfg.Shared)
	}
	if cfg.Services == nil || cfg.Stateful == nil || cfg.Env == nil {
		t.Fatalf("maps should be initialized: services=%v stateful=%v env=%v", cfg.Services, cfg.Stateful, cfg.Env)
	}
	if cfg.Secrets.Wrapper != "" {
		t.Fatalf("Secrets.Wrapper = %q, want empty", cfg.Secrets.Wrapper)
	}
}

func TestLoadMissingConfigUsesDefaults(t *testing.T) {
	root := filepath.Join(t.TempDir(), "acme-api")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFile(filepath.Join(root, "docktree.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.App != "acme_api" {
		t.Fatalf("missing config App = %q, want acme_api", cfg.App)
	}
}

func TestLoadConfigParsesProxyBlock(t *testing.T) {
	path := writeConfig(t, `
app = "shop"

[proxy]
enabled = true
dns_suffix = "127.0.0.1.sslip.io"

[services.api]
host_port_env = "API_PORT"
expose = "http"
proxy_port = 3000

[services.db]
expose = "none"
`)
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Proxy.Enabled {
		t.Fatalf("Proxy.Enabled = false, want true")
	}
	if cfg.Proxy.Engine != "caddy" {
		t.Fatalf("Proxy.Engine = %q, want default caddy", cfg.Proxy.Engine)
	}
	if cfg.Proxy.DNSSuffix != "127.0.0.1.sslip.io" {
		t.Fatalf("Proxy.DNSSuffix = %q", cfg.Proxy.DNSSuffix)
	}
	if cfg.Proxy.HTTPPort != 80 || cfg.Proxy.HTTPSPort != 443 {
		t.Fatalf("Proxy ports = %d/%d, want 80/443", cfg.Proxy.HTTPPort, cfg.Proxy.HTTPSPort)
	}
	if cfg.Services["api"].Expose != "http" || cfg.Services["api"].ProxyPort != 3000 {
		t.Fatalf("api service = %#v", cfg.Services["api"])
	}
	if cfg.Services["db"].Expose != "none" {
		t.Fatalf("db expose = %q, want none", cfg.Services["db"].Expose)
	}
}

func TestLoadConfigParsesPublicURLEnv(t *testing.T) {
	path := writeConfig(t, `
app = "shop"

[services.minio]
expose = "http"
proxy_port = 9000
public_url_env = "MINIO_PUBLIC_URL"
`)
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Services["minio"].PublicURLEnv != "MINIO_PUBLIC_URL" {
		t.Fatalf("minio public_url_env = %q, want MINIO_PUBLIC_URL", cfg.Services["minio"].PublicURLEnv)
	}
}

func TestLoadConfigProxyDefaultsWhenOmitted(t *testing.T) {
	cfg, err := LoadFile(filepath.Join(t.TempDir(), "docktree.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Proxy.Enabled {
		t.Fatalf("Proxy.Enabled = true, want default false")
	}
	if cfg.Proxy.Engine != "caddy" || cfg.Proxy.DNSSuffix != "localhost" {
		t.Fatalf("proxy defaults = %#v", cfg.Proxy)
	}
}

func TestLoadConfigParsesDocktreeModel(t *testing.T) {
	path := writeConfig(t, `
app = "flexiple"
compose = "compose.yml"
shared = ["postgres"]

[services.api]
host_port_env = "API_PORT"
fixed = true

[services.e2e]
autostart = false

[stateful.postgres]
default_strategy = "isolated"
engine = "postgres"
snapshot_source = "flexiple_pgdata"
source_db = "flexiple_{app}_source"
env = { POSTGRES_PRIMARY_DB = "flexiple_{slug}" }

[env]
OPENSEARCH_INDEX_PREFIX = "{slug}_"
APP_NAMESPACE = "{app}_{main_slug}"

[secrets]
wrapper = "doppler run --"
`)

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.App != "flexiple" || cfg.Compose != "compose.yml" {
		t.Fatalf("app/compose = %q/%q", cfg.App, cfg.Compose)
	}
	if len(cfg.Shared) != 1 || cfg.Shared[0] != "postgres" {
		t.Fatalf("Shared = %v", cfg.Shared)
	}
	api := cfg.Services["api"]
	if api.HostPortEnv != "API_PORT" || !api.Fixed || !api.Autostart {
		t.Fatalf("api service = %+v, want host port env, fixed, autostart default true", api)
	}
	if cfg.Services["e2e"].Autostart {
		t.Fatalf("e2e Autostart = true, want false")
	}
	pg := cfg.Stateful["postgres"]
	if pg.DefaultStrategy != "isolated" || pg.Engine != "postgres" || pg.SnapshotSource != "flexiple_pgdata" || pg.SourceDB != "flexiple_{app}_source" {
		t.Fatalf("postgres stateful = %+v", pg)
	}
	if pg.Env["POSTGRES_PRIMARY_DB"] != "flexiple_{slug}" {
		t.Fatalf("postgres stateful env = %+v", pg.Env)
	}
	if cfg.Secrets.Wrapper != "doppler run --" {
		t.Fatalf("Secrets.Wrapper = %q", cfg.Secrets.Wrapper)
	}

	rendered := cfg.RenderTemplates(TemplateContext{Slug: "feat_one", MainSlug: "main", App: cfg.App})
	if rendered.Env["OPENSEARCH_INDEX_PREFIX"] != "feat_one_" {
		t.Fatalf("templated env = %q", rendered.Env["OPENSEARCH_INDEX_PREFIX"])
	}
	if rendered.Env["APP_NAMESPACE"] != "flexiple_main" {
		t.Fatalf("templated APP_NAMESPACE = %q", rendered.Env["APP_NAMESPACE"])
	}
	if rendered.Stateful["postgres"].SourceDB != "flexiple_flexiple_source" {
		t.Fatalf("templated source db = %q", rendered.Stateful["postgres"].SourceDB)
	}
	if rendered.Stateful["postgres"].Env["POSTGRES_PRIMARY_DB"] != "flexiple_feat_one" {
		t.Fatalf("templated stateful env = %+v", rendered.Stateful["postgres"].Env)
	}
}

func TestStatefulSuperuserDefaultsToPostgres(t *testing.T) {
	path := writeConfig(t, `
app = "shop"

[stateful.postgres]
engine = "postgres"
source_db = "shop_shared"

[stateful.legacy]
engine = "postgres"
source_db = "legacy_shared"
superuser = "primary"
`)

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Stateful["postgres"].Superuser; got != "postgres" {
		t.Fatalf("postgres Superuser = %q, want default postgres", got)
	}
	if got := cfg.Stateful["legacy"].Superuser; got != "primary" {
		t.Fatalf("legacy Superuser = %q, want configured primary", got)
	}
}

func TestUnknownKeysBecomeWarnings(t *testing.T) {
	path := writeConfig(t, `
app = "flexiple"
mystery = true

[services.api]
unexpected = "value"
`)

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := warningKeys(cfg.Warnings)
	if !strings.Contains(got, "mystery") || !strings.Contains(got, "services.api.unexpected") {
		t.Fatalf("warning keys = %q, want top-level and nested unknown keys", got)
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "docktree.toml")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(body)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func warningKeys(warnings []Warning) string {
	var b strings.Builder
	for _, warning := range warnings {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(warning.Key)
	}
	return b.String()
}

func TestLoadConfigRejectsEnvValuesThatCorruptTheArtifact(t *testing.T) {
	cases := map[string]string{
		"multiline env value": `
app = "shop"

[env]
PEM = """line1
line2"""
`,
		"env key with equals": `
app = "shop"

[env]
"A=B" = "x"
`,
		"env key with whitespace": `
app = "shop"

[env]
"MY KEY" = "x"
`,
		"multiline stateful env value": `
app = "shop"

[stateful.postgres]
env = { DATABASE_NAME = "a\nb" }
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadFile(writeConfig(t, body)); err == nil {
				t.Fatal("LoadFile accepted an env entry that corrupts the env artifact")
			}
		})
	}
}
