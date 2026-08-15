package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rajpatil53/docktree/internal/compose"
	"github.com/rajpatil53/docktree/internal/config"
	"github.com/rajpatil53/docktree/internal/identity"
	"github.com/rajpatil53/docktree/internal/paths"
)

func writeProxyProject(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docktree.toml"), []byte(`
app = "shop"

[proxy]
enabled = true

[services.api]
host_port_env = "API_PORT"
`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestUpBringsUpProxyWhenEnabled(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeProxyProject(t, root)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{
		configOutput:   []byte(`{"services":{"api":{"ports":[{"target":3000,"published":"${API_PORT:-3000}"}]}}}`),
		networkMissing: true,
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runUp(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}

	argv := commandArgvStrings(fr.commands)
	if !containsArgv(argv, "docker network create docktree_proxy") {
		t.Fatalf("proxy network create missing from %#v", argv)
	}
	home, _ := os.UserHomeDir()
	wantUp := "docker compose --progress plain -p docktree-proxy -f " + paths.ProxyCompose(home) + " up -d"
	if !containsArgv(argv, wantUp) {
		t.Fatalf("proxy up command missing from %#v", argv)
	}
	if _, err := os.Stat(paths.ProxyCompose(home)); err != nil {
		t.Fatalf("proxy compose projection not written: %v", err)
	}
}

func TestOpenPrintsProxyHostnameWhenEnabled(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeProxyProject(t, root)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{
		configOutput:          []byte(`{"services":{"api":{"ports":[{"target":3000,"published":"${API_PORT:-3000}"}]}}}`),
		allowUnexpectedOutput: true,
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)
	var stdout strings.Builder
	deps.stdout = &stdout

	if err := runOpen(context.Background(), []string{"api"}, deps); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "https://api-feature-one.shop.localhost" {
		t.Fatalf("open url = %q, want proxy hostname", got)
	}
}

func TestTrustExportsCaddyRootCA(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{}
	var events []string
	deps := newCommandTestDeps(t.TempDir(), fr, &events)
	var stdout strings.Builder
	deps.stdout = &stdout

	if err := runTrust(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}

	home, _ := os.UserHomeDir()
	dest := filepath.Join(paths.ProxyDir(home), "root.crt")
	want := "docker compose -p docktree-proxy cp docktree-proxy:/data/caddy/pki/authorities/local/root.crt " + dest
	if !containsArgv(commandArgvStrings(fr.commands), want) {
		t.Fatalf("cp command missing: %#v", commandArgvStrings(fr.commands))
	}
	if !strings.Contains(stdout.String(), "root CA exported") {
		t.Fatalf("missing export message: %q", stdout.String())
	}
}

func TestRunProxyDownStatusUsage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	t.Run("down", func(t *testing.T) {
		fr := &fakeRunner{}
		var ev []string
		deps := newCommandTestDeps(t.TempDir(), fr, &ev)
		if err := runProxy(context.Background(), []string{"down"}, deps); err != nil {
			t.Fatal(err)
		}
		if !containsArgv(commandArgvStrings(fr.commands), "docker compose -p docktree-proxy down") {
			t.Fatalf("down argv missing: %#v", commandArgvStrings(fr.commands))
		}
	})
	t.Run("status is the default action", func(t *testing.T) {
		fr := &fakeRunner{}
		var ev []string
		deps := newCommandTestDeps(t.TempDir(), fr, &ev)
		if err := runProxy(context.Background(), nil, deps); err != nil {
			t.Fatal(err)
		}
		if !containsArgv(commandArgvStrings(fr.commands), "docker compose -p docktree-proxy ps") {
			t.Fatalf("status argv missing: %#v", commandArgvStrings(fr.commands))
		}
	})
	t.Run("unknown action errors with usage", func(t *testing.T) {
		fr := &fakeRunner{}
		var ev []string
		deps := newCommandTestDeps(t.TempDir(), fr, &ev)
		err := runProxy(context.Background(), []string{"bogus"}, deps)
		if err == nil || !strings.Contains(err.Error(), "usage: docktree proxy") {
			t.Fatalf("bogus action error = %v, want usage message", err)
		}
	})
}

func TestUpConvergesPerProjectProxyNetwork(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeProxyProject(t, root)
	t.Setenv("HOME", t.TempDir())

	wantNet := compose.ProxyNetworkName(identity.Derive("shop", identity.ComputeSlug("Feature-One")).Project)

	fr := &fakeRunner{
		configOutput:   []byte(`{"services":{"api":{"ports":[{"target":3000,"published":"${API_PORT:-3000}"}]}}}`),
		networkMissing: true,
		outputByArgv: map[string][]byte{
			"docker network ls --filter label=" + compose.ProxyNetworkLabel + "=true --format {{.Name}}": []byte(wantNet + "\n"),
		},
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runUp(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}

	argv := commandArgvStrings(fr.commands)
	wantCreate := "docker network create --label " + compose.ProxyNetworkLabel + "=true --label " + compose.LabelManaged + "=true " + wantNet
	if !containsArgv(argv, wantCreate) {
		t.Fatalf("per-project proxy network create missing from %#v", argv)
	}

	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(paths.ProxyCompose(home))
	if err != nil {
		t.Fatal(err)
	}
	// Caddy must attach to (and treat as ingress) the per-project proxy network,
	// otherwise the app's HTTP services are unroutable.
	if !strings.Contains(string(data), "CADDY_INGRESS_NETWORKS") || !strings.Contains(string(data), wantNet) {
		t.Fatalf("proxy compose must wire Caddy to %s:\n%s", wantNet, data)
	}
}

func TestUpReusesExistingProxyNetwork(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeProxyProject(t, root)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{
		configOutput:   []byte(`{"services":{"api":{"ports":[{"target":3000,"published":"${API_PORT:-3000}"}]}}}`),
		networkMissing: false, // both shared + proxy networks already exist
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runUp(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}
	for _, got := range commandArgvStrings(fr.commands) {
		if got == "docker network create docktree_proxy" {
			t.Fatalf("must not recreate the proxy network when it already exists")
		}
	}
	home, _ := os.UserHomeDir()
	if !containsArgv(commandArgvStrings(fr.commands), "docker compose --progress plain -p docktree-proxy -f "+paths.ProxyCompose(home)+" up -d") {
		t.Fatalf("proxy compose up should still run")
	}
}

func TestUpSkipsHostPortForProxiedService(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeProxyProject(t, root)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{
		configOutput:   []byte(`{"services":{"api":{"ports":[{"target":3000,"published":"${API_PORT:-3000}"}]}}}`),
		networkMissing: true,
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runUp(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}
	env, err := os.ReadFile(paths.EnvFile(root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(env), "API_PORT=") {
		t.Fatalf("proxied api must not be allocated a host port in env:\n%s", env)
	}
}

func TestOpenAutoSelectsProxiedServiceAndHonorsHTTPSPort(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docktree.toml"), []byte(`
app = "shop"

[proxy]
enabled = true
https_port = 8443

[services.api]
host_port_env = "API_PORT"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{
		configOutput:          []byte(`{"services":{"api":{"ports":[{"target":3000,"published":"${API_PORT:-3000}"}]}}}`),
		allowUnexpectedOutput: true,
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)
	var stdout strings.Builder
	deps.stdout = &stdout

	if err := runOpen(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "https://api-feature-one.shop.localhost:8443" {
		t.Fatalf("auto-selected url = %q, want hostname with :8443", got)
	}
}

func TestOpenSurfacesProxyModelError(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeProxyProject(t, root)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{
		configOutput:          []byte("{not valid json"),
		allowUnexpectedOutput: true,
	}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	err := runOpen(context.Background(), []string{"api"}, deps)
	if err == nil || !strings.Contains(err.Error(), "proxy route") {
		t.Fatalf("expected surfaced proxy-route error, got %v", err)
	}
}

func TestUpSkipsProxyWhenDisabled(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Feature-One")
	writeBasicProject(t, root)
	t.Setenv("HOME", t.TempDir())

	fr := &fakeRunner{configOutput: []byte(basicComposeConfigJSON), networkMissing: true}
	var events []string
	deps := newCommandTestDeps(root, fr, &events)

	if err := runUp(context.Background(), nil, deps); err != nil {
		t.Fatal(err)
	}
	for _, got := range commandArgvStrings(fr.commands) {
		if got == "docker network create docktree_proxy" {
			t.Fatalf("proxy network should not be created when disabled")
		}
	}
}

func TestComposeOptionsCarriesProxyConfig(t *testing.T) {
	cfg := &config.Config{
		App:    "shop",
		Shared: []string{"postgres"},
		Services: map[string]config.Service{
			"api": {HostPortEnv: "API_PORT", Expose: "http", ProxyPort: 3000},
		},
		Stateful: map[string]config.Stateful{},
		Proxy: config.Proxy{
			Enabled:   true,
			Engine:    "caddy",
			DNSSuffix: "localhost",
			HTTPPort:  80,
			HTTPSPort: 443,
		},
	}
	r := &resolved{
		manifest: cfg,
		slug:     "feature_x",
		mainSlug: "main",
		names:    identity.Derive("shop", "feature_x"),
	}

	opts := composeOptions(r)

	if !opts.Proxy.Enabled || opts.Proxy.DNSSuffix != "localhost" {
		t.Fatalf("proxy options = %#v", opts.Proxy)
	}
	so := opts.Services["api"]
	if so.Expose != "http" || so.ProxyPort != 3000 {
		t.Fatalf("api service options = %#v", so)
	}
}
