# Hostname Proxy — Phase 1 (Generation Layer) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Generate correct Caddy reverse-proxy artifacts — per-service `caddy.*`
routing labels with host-port drop, and the machine-global proxy compose doc —
entirely at the projection layer, behind the opt-in `[proxy] enabled` flag, with
no runtime/Docker dependency.

**Architecture:** Pure generation. `identity.DNSLabel` produces DNS-safe name
components; `config` parses a new `[proxy]` block and per-service `expose`;
`compose.RenderWorktree` emits `caddy`/`caddy.reverse_proxy` labels (and drops the
published port) for services classified as HTTP; `compose.RenderProxy` emits the
global `docktree-proxy` compose doc; `cmd.composeOptions` threads the config
through. Everything is verified with the project's existing `render_test`/
`config_test` table style — no Docker, no `fakeRunner` in this phase.

**Tech Stack:** Go 1.21, `gopkg.in/yaml.v3`, `github.com/BurntSushi/toml`.

**Scope note:** This is Phase 1 of the design in
`docs/plans/2026-06-09-hostname-proxy-design.md`. It builds the generation layer
only. Phase 2 (global proxy lifecycle / `cmd/proxy.go`), Phase 3 (label-derived
URL surface), and Phase 4 (`docktree trust` + DNS polish) are separate plans.
Because nothing starts the proxy yet, enabling `[proxy]` is **not user-ready**
until Phase 2–3; this phase only makes the *generated* artifacts correct.

**Verification (run from repo root):**
`go test -tags netgo ./...`, `go vet ./...`.

---

## File Structure

| File | Responsibility | Create/Modify |
|---|---|---|
| `internal/identity/dns.go` | `DNSLabel` — single source of truth for DNS-safe name components (`_`→`-`, lowercase) | Create |
| `internal/identity/dns_test.go` | `DNSLabel` tests | Create |
| `internal/config/config.go` | parse `[proxy]` block + per-service `expose`/`proxy_port` | Modify |
| `internal/config/config_test.go` | config parse tests | Modify |
| `internal/compose/render.go` | `Options.Proxy`, `ServiceOptions.Expose/ProxyPort`, `IsHTTPPort`, proxy classification + `applyProxyLabels` + port drop, proxy-network rendering, `RenderProxy` | Modify |
| `internal/compose/render_test.go` | proxy label + `RenderProxy` tests | Modify |
| `cmd/orchestrate.go` | `composeOptions` threads proxy config into `compose.Options` | Modify |
| `cmd/proxy_test.go` | `composeOptions` wiring test | Create |

---

## Task 1: `identity.DNSLabel`

**Files:**
- Create: `internal/identity/dns.go`
- Create: `internal/identity/dns_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/identity/dns_test.go`:

```go
package identity

import "testing"

func TestDNSLabel(t *testing.T) {
	cases := map[string]string{
		"feature_x": "feature-x",
		"Feature_X": "feature-x",
		"shop":      "shop",
		"a_b_c":     "a-b-c",
		"main":      "main",
		"_edge_":    "edge",
		"my.app":    "my-app",
	}
	for in, want := range cases {
		if got := DNSLabel(in); got != want {
			t.Fatalf("DNSLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags netgo ./internal/identity/ -run TestDNSLabel -v`
Expected: FAIL — `undefined: DNSLabel`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/identity/dns.go`:

```go
package identity

import "strings"

// DNSLabel converts a docktree identity component (slug, app, or service name)
// into a DNS-safe label: lowercase, with every character outside [a-z0-9-]
// collapsed to '-', and leading/trailing '-' trimmed. Docktree slugs are
// validated ^[a-z0-9_]+$, so in practice this maps '_' to '-'. It is the single
// source of truth shared by the proxy host label (compose render) and the CLI
// URL output (open/explain).
func DNSLabel(component string) string {
	var b strings.Builder
	for _, r := range component {
		switch {
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -tags netgo ./internal/identity/ -run TestDNSLabel -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/identity/dns.go internal/identity/dns_test.go
git commit -m "feat(identity): add DNSLabel for DNS-safe name components"
```

---

## Task 2: `[proxy]` config + per-service `expose`/`proxy_port`

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/config/config_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags netgo ./internal/config/ -run TestLoadConfigParsesProxyBlock -v`
Expected: FAIL — `cfg.Proxy undefined` (compile error).

- [ ] **Step 3: Write minimal implementation**

In `internal/config/config.go`:

(a) Add `Expose`/`ProxyPort` to `Service` (after `Autostart bool`):

```go
type Service struct {
	HostPortEnv string
	Fixed       bool
	Autostart   bool
	Expose      string
	ProxyPort   int
}
```

(b) Add the `Proxy` type (next to `Secrets`):

```go
type Proxy struct {
	Enabled   bool
	Engine    string
	DNSSuffix string
	HTTPPort  int
	HTTPSPort int
}
```

(c) Add `Proxy Proxy` to `Config` (after `Secrets Secrets`):

```go
	Secrets          Secrets
	Proxy            Proxy
```

(d) Add the raw decode targets. Extend `rawService`:

```go
type rawService struct {
	HostPortEnv string `toml:"host_port_env"`
	Fixed       bool   `toml:"fixed"`
	Autostart   *bool  `toml:"autostart"`
	Expose      string `toml:"expose"`
	ProxyPort   int    `toml:"proxy_port"`
}
```

Add a `rawProxy` type and a `Proxy` field on `rawConfig`:

```go
type rawProxy struct {
	Enabled   bool   `toml:"enabled"`
	Engine    string `toml:"engine"`
	DNSSuffix string `toml:"dns_suffix"`
	HTTPPort  int    `toml:"http_port"`
	HTTPSPort int    `toml:"https_port"`
}
```

In `rawConfig`, after `Secrets Secrets \`toml:"secrets"\``:

```go
	Proxy            rawProxy               `toml:"proxy"`
```

(e) In `LoadFile`, after `cfg.Secrets = raw.Secrets`, add:

```go
	cfg.Proxy = normalizeProxy(raw.Proxy)
```

(f) In `normalizeServices`, set the new fields:

```go
		services[name] = Service{
			HostPortEnv: svc.HostPortEnv,
			Fixed:       svc.Fixed,
			Autostart:   autostart,
			Expose:      svc.Expose,
			ProxyPort:   svc.ProxyPort,
		}
```

(g) Add `normalizeProxy` (next to `normalizeServices`):

```go
func normalizeProxy(raw rawProxy) Proxy {
	p := Proxy{
		Enabled:   raw.Enabled,
		Engine:    raw.Engine,
		DNSSuffix: raw.DNSSuffix,
		HTTPPort:  raw.HTTPPort,
		HTTPSPort: raw.HTTPSPort,
	}
	if p.Engine == "" {
		p.Engine = "caddy"
	}
	if p.DNSSuffix == "" {
		p.DNSSuffix = "localhost"
	}
	if p.HTTPPort == 0 {
		p.HTTPPort = 80
	}
	if p.HTTPSPort == 0 {
		p.HTTPSPort = 443
	}
	return p
}
```

(h) In `defaultConfig`, set the proxy defaults so a missing file matches:

```go
		MainBranch:     "main",
		Ports:          "hashed",
		SharedServices: []string{},
		Proxy:          normalizeProxy(rawProxy{}),
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -tags netgo ./internal/config/ -run TestLoadConfigProxy -v`
Expected: PASS for both new tests.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): parse [proxy] block and per-service expose/proxy_port"
```

---

## Task 3: render Caddy labels + port drop for HTTP services

**Files:**
- Modify: `internal/compose/render.go`
- Modify: `internal/compose/render_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/compose/render_test.go`:

```go
func TestRenderWorktreeEmitsProxyLabelsAndDropsPorts(t *testing.T) {
	model, err := ParseConfigJSON([]byte(`{
		"services": {
			"api": {
				"image": "example/api",
				"ports": [{"target": 3000, "published": "${API_PORT:-3000}", "protocol": "tcp"}]
			},
			"worker": {
				"image": "example/worker",
				"ports": [{"target": 5555, "published": "${WORKER_PORT:-5555}", "protocol": "tcp"}]
			},
			"postgres": {"image": "postgres:16"}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{
		App:              "shop",
		Slug:             "feature_x",
		Project:          "shop-feature_x",
		SharedNetwork:    "shop_shared",
		GeneratedEnvFile: ".env.worktree",
		Shared:           []string{"postgres"},
		Proxy:            ProxyOptions{Enabled: true, DNSSuffix: "localhost"},
	}

	out, err := RenderWorktree(model, opts)
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeYAML(t, out)
	services := asMap(t, doc["services"])

	api := asMap(t, services["api"])
	apiLabels := asStringMap(t, api["labels"])
	if apiLabels["caddy"] != "api.feature-x.shop.localhost" {
		t.Fatalf("api caddy label = %q", apiLabels["caddy"])
	}
	if apiLabels["caddy.reverse_proxy"] != "{{upstreams 3000}}" {
		t.Fatalf("api caddy.reverse_proxy = %q", apiLabels["caddy.reverse_proxy"])
	}
	if _, ok := api["ports"]; ok {
		t.Fatalf("proxied api should not publish host ports: %#v", api["ports"])
	}
	if !contains(keys(asMap(t, api["networks"])), ProxyNetworkKey) {
		t.Fatalf("api networks = %#v, want %s", api["networks"], ProxyNetworkKey)
	}

	worker := asMap(t, services["worker"])
	if _, ok := asStringMap(t, worker["labels"])["caddy"]; ok {
		t.Fatalf("non-HTTP worker should not be proxied")
	}
	if _, ok := worker["ports"]; !ok {
		t.Fatalf("non-proxied worker should keep its ports")
	}

	proxyNet := asMap(t, asMap(t, doc["networks"])[ProxyNetworkKey])
	if proxyNet["name"] != "docktree_proxy" || proxyNet["external"] != true {
		t.Fatalf("proxy network = %#v", proxyNet)
	}
}

func TestRenderWorktreeProxyDisabledKeepsPorts(t *testing.T) {
	model, err := ParseConfigJSON([]byte(`{
		"services": {"api": {"ports": [{"target": 3000, "published": "${API_PORT:-3000}"}]}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderWorktree(model, Options{
		App: "shop", Slug: "feature_x", Project: "shop-feature_x",
		SharedNetwork: "shop_shared", GeneratedEnvFile: ".env.worktree",
		Proxy: ProxyOptions{Enabled: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	api := asMap(t, asMap(t, decodeYAML(t, out)["services"])["api"])
	if _, ok := asStringMap(t, api["labels"])["caddy"]; ok {
		t.Fatalf("proxy disabled should emit no caddy label")
	}
	if _, ok := api["ports"]; !ok {
		t.Fatalf("proxy disabled should keep ports")
	}
}

func TestRenderWorktreeExposeOverrides(t *testing.T) {
	model, err := ParseConfigJSON([]byte(`{
		"services": {
			"api":    {"ports": [{"target": 3000, "published": "${API_PORT:-3000}"}]},
			"mailer": {"ports": [{"target": 2525, "published": "${MAIL_PORT:-2525}"}]}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderWorktree(model, Options{
		App: "shop", Slug: "feature_x", Project: "shop-feature_x",
		SharedNetwork: "shop_shared", GeneratedEnvFile: ".env.worktree",
		Proxy: ProxyOptions{Enabled: true, DNSSuffix: "localhost"},
		Services: map[string]ServiceOptions{
			"api":    {Expose: "none"},
			"mailer": {Expose: "http", ProxyPort: 2525},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	services := asMap(t, decodeYAML(t, out)["services"])
	if _, ok := asStringMap(t, asMap(t, services["api"])["labels"])["caddy"]; ok {
		t.Fatalf("api expose=none should not be proxied")
	}
	mailer := asMap(t, services["mailer"])
	mailerLabels := asStringMap(t, mailer["labels"])
	if mailerLabels["caddy"] != "mailer.feature-x.shop.localhost" {
		t.Fatalf("mailer caddy = %q", mailerLabels["caddy"])
	}
	if mailerLabels["caddy.reverse_proxy"] != "{{upstreams 2525}}" {
		t.Fatalf("mailer upstreams = %q", mailerLabels["caddy.reverse_proxy"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags netgo ./internal/compose/ -run TestRenderWorktreeEmitsProxyLabels -v`
Expected: FAIL — `opts.Proxy undefined` / `ProxyNetworkKey undefined` (compile error).

- [ ] **Step 3: Write minimal implementation**

In `internal/compose/render.go`:

(a) Add the import (the block currently has `fmt`, `strings`, `yaml.v3`):

```go
import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"docktree/internal/identity"
)
```

(b) Add the proxy network key to the `const` block (next to `SharedNetworkKey`):

```go
	SharedNetworkKey = "docktree_shared"
	ProxyNetworkKey  = "docktree_proxy"
	ManualProfile    = "docktree-manual"
```

(c) Add `Proxy` to `Options` (after `StatefulModes`):

```go
	StatefulModes    map[string]string
	Proxy            ProxyOptions
}

type ProxyOptions struct {
	Enabled   bool
	DNSSuffix string
}
```

(d) Add `Expose`/`ProxyPort` to `ServiceOptions`:

```go
type ServiceOptions struct {
	HostPortEnv       string
	Fixed             bool
	Autostart         bool
	AutostartExplicit bool
	Expose            string
	ProxyPort         int
}
```

(e) In `renderProjection`, inside the worktree branch (the `else`), after the
existing `if forked { setLabels(rawService, map[string]string{LabelFork: name}) }`
block and before the branch closes, add:

```go
			if opts.Proxy.Enabled && serviceProxied(opts, name, service) {
				applyProxyLabels(rawService, opts, name, service)
			}
```

(f) In `renderNetworks`, add a proxy case alongside the `SharedNetworkKey` case:

```go
		if name == ProxyNetworkKey {
			out[name] = map[string]any{
				"name":     ProxyNetworkKey,
				"external": true,
			}
			continue
		}
```

(g) Add the proxy helpers (near `applyServiceLabels`):

```go
// IsHTTPPort reports whether a container target port is one docktree treats as
// HTTP-exposable. Mirrors internal/dockerstate's conservative allowlist.
func IsHTTPPort(target int) bool {
	switch target {
	case 80, 443, 3000, 3001, 5000, 5173, 8000, 8080, 8081, 9000:
		return true
	default:
		return false
	}
}

func serviceProxied(opts Options, name string, svc Service) bool {
	switch opts.Services[name].Expose {
	case "http":
		return true
	case "none":
		return false
	}
	if sharedSet(opts.Shared)[name] {
		return false
	}
	if _, ok := opts.StatefulModes[name]; ok {
		return false
	}
	for _, p := range svc.Ports {
		if p.Published != "" && IsHTTPPort(p.Target) {
			return true
		}
	}
	return false
}

func proxyHost(opts Options, service string) string {
	suffix := opts.Proxy.DNSSuffix
	if suffix == "" {
		suffix = "localhost"
	}
	return strings.Join([]string{
		identity.DNSLabel(service),
		identity.DNSLabel(opts.Slug),
		identity.DNSLabel(opts.App),
	}, ".") + "." + suffix
}

func proxyUpstreamPort(so ServiceOptions, svc Service) int {
	if so.ProxyPort > 0 {
		return so.ProxyPort
	}
	for _, p := range svc.Ports {
		if IsHTTPPort(p.Target) {
			return p.Target
		}
	}
	if len(svc.Ports) > 0 {
		return svc.Ports[0].Target
	}
	return 0
}

func applyProxyLabels(raw map[string]any, opts Options, name string, svc Service) {
	setLabels(raw, map[string]string{
		"caddy":               proxyHost(opts, name),
		"caddy.reverse_proxy": fmt.Sprintf("{{upstreams %d}}", proxyUpstreamPort(opts.Services[name], svc)),
	})
	networks := serviceNetworkMap(raw["networks"])
	networks[ProxyNetworkKey] = nil
	raw["networks"] = networks
	delete(raw, "ports")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -tags netgo ./internal/compose/ -run TestRenderWorktree -v`
Expected: PASS for the three new tests and the existing worktree tests
(no regression — proxy code is gated on `opts.Proxy.Enabled`).

- [ ] **Step 5: Commit**

```bash
git add internal/compose/render.go internal/compose/render_test.go
git commit -m "feat(compose): emit Caddy labels and drop host ports for proxied HTTP services"
```

---

## Task 4: `compose.RenderProxy` — global proxy compose doc

**Files:**
- Modify: `internal/compose/render.go`
- Modify: `internal/compose/render_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/compose/render_test.go`:

```go
func TestRenderProxyDoc(t *testing.T) {
	out, err := RenderProxy(ProxyRenderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeYAML(t, out)

	svc := asMap(t, asMap(t, doc["services"])["docktree-proxy"])
	if svc["image"] != DefaultProxyImage {
		t.Fatalf("image = %v, want %s", svc["image"], DefaultProxyImage)
	}
	ports := asSlice(t, svc["ports"])
	if len(ports) != 2 || ports[0] != "80:80" || ports[1] != "443:443" {
		t.Fatalf("ports = %#v", ports)
	}
	vols := asSlice(t, svc["volumes"])
	if vols[0] != "/var/run/docker.sock:/var/run/docker.sock:ro" {
		t.Fatalf("first volume = %v, want read-only docker socket", vols[0])
	}
	if !containsAny(vols, ProxyDataVolume+":/data") {
		t.Fatalf("volumes missing CA data mount: %#v", vols)
	}
	labels := asStringMap(t, svc["labels"])
	if labels[LabelTier] != "proxy" || labels[LabelManaged] != "true" {
		t.Fatalf("proxy labels = %#v", labels)
	}
	net := asMap(t, asMap(t, doc["networks"])[ProxyNetworkKey])
	if net["name"] != ProxyNetworkKey || net["external"] != true {
		t.Fatalf("proxy network = %#v", net)
	}
	if _, ok := svc["healthcheck"]; !ok {
		t.Fatalf("proxy service missing healthcheck")
	}
}

func TestRenderProxyDocCustomPorts(t *testing.T) {
	out, err := RenderProxy(ProxyRenderOptions{HTTPPort: 8080, HTTPSPort: 8443})
	if err != nil {
		t.Fatal(err)
	}
	svc := asMap(t, asMap(t, decodeYAML(t, out)["services"])["docktree-proxy"])
	ports := asSlice(t, svc["ports"])
	if ports[0] != "8080:80" || ports[1] != "8443:443" {
		t.Fatalf("custom ports = %#v", ports)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags netgo ./internal/compose/ -run TestRenderProxyDoc -v`
Expected: FAIL — `undefined: RenderProxy` / `ProxyRenderOptions` / `DefaultProxyImage`.

- [ ] **Step 3: Write minimal implementation**

In `internal/compose/render.go`, add the constants to the `const` block:

```go
	DefaultProxyImage = "lucaslorentz/caddy-docker-proxy:2.11-alpine"
	ProxyDataVolume   = "docktree_caddy_data"
	ProxyConfigVolume = "docktree_caddy_config"
```

Add the renderer (near `RenderInfra`/`RenderWorktree`):

```go
type ProxyRenderOptions struct {
	Image     string
	Network   string
	HTTPPort  int
	HTTPSPort int
}

// RenderProxy generates the machine-global docktree-proxy compose document. It
// is independent of any worktree: it owns the single Caddy reverse proxy that
// binds host 80/443 and routes every app/worktree HTTP service by hostname.
func RenderProxy(opts ProxyRenderOptions) ([]byte, error) {
	if opts.Image == "" {
		opts.Image = DefaultProxyImage
	}
	if opts.Network == "" {
		opts.Network = ProxyNetworkKey
	}
	if opts.HTTPPort == 0 {
		opts.HTTPPort = 80
	}
	if opts.HTTPSPort == 0 {
		opts.HTTPSPort = 443
	}
	doc := map[string]any{
		"services": map[string]any{
			"docktree-proxy": map[string]any{
				"image":       opts.Image,
				"restart":     "unless-stopped",
				"environment": map[string]any{"CADDY_INGRESS_NETWORKS": opts.Network},
				"ports": []any{
					fmt.Sprintf("%d:80", opts.HTTPPort),
					fmt.Sprintf("%d:443", opts.HTTPSPort),
				},
				"volumes": []any{
					"/var/run/docker.sock:/var/run/docker.sock:ro",
					ProxyDataVolume + ":/data",
					ProxyConfigVolume + ":/config",
				},
				"networks": []any{opts.Network},
				"labels": map[string]any{
					LabelManaged: "true",
					LabelService: "docktree-proxy",
					LabelTier:    "proxy",
				},
				"healthcheck": map[string]any{
					"test":     []any{"CMD", "wget", "-qO-", "http://localhost:2019/config/"},
					"interval": "5s",
					"timeout":  "3s",
					"retries":  12,
				},
			},
		},
		"networks": map[string]any{
			opts.Network: map[string]any{"name": opts.Network, "external": true},
		},
		"volumes": map[string]any{
			ProxyDataVolume:   map[string]any{"name": ProxyDataVolume},
			ProxyConfigVolume: map[string]any{"name": ProxyConfigVolume},
		},
	}
	return yaml.Marshal(doc)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -tags netgo ./internal/compose/ -run TestRenderProxyDoc -v`
Expected: PASS for both tests.

- [ ] **Step 5: Commit**

```bash
git add internal/compose/render.go internal/compose/render_test.go
git commit -m "feat(compose): add RenderProxy for the machine-global proxy compose doc"
```

---

## Task 5: thread proxy config through `composeOptions`

**Files:**
- Modify: `cmd/orchestrate.go:368` (`composeOptions`)
- Create: `cmd/proxy_test.go`

- [ ] **Step 1: Write the failing test**

Create `cmd/proxy_test.go`:

```go
package cmd

import (
	"testing"

	"docktree/internal/config"
	"docktree/internal/identity"
)

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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags netgo ./cmd/ -run TestComposeOptionsCarriesProxyConfig -v`
Expected: FAIL — `opts.Proxy undefined` field on the returned `compose.Options`
(the wiring isn't there yet).

- [ ] **Step 3: Write minimal implementation**

In `cmd/orchestrate.go`, in `composeOptions`, set the new per-service fields where
`services[name]` is built:

```go
		services[name] = compose.ServiceOptions{
			HostPortEnv:       svc.HostPortEnv,
			Fixed:             svc.Fixed,
			Autostart:         svc.Autostart,
			AutostartExplicit: !svc.Autostart,
			Expose:            svc.Expose,
			ProxyPort:         svc.ProxyPort,
		}
```

And add the `Proxy` field to the returned `compose.Options` (after `StatefulModes`):

```go
		StatefulModes:    statefulModes,
		Proxy: compose.ProxyOptions{
			Enabled:   r.manifest.Proxy.Enabled,
			DNSSuffix: r.manifest.Proxy.DNSSuffix,
		},
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -tags netgo ./cmd/ -run TestComposeOptionsCarriesProxyConfig -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/orchestrate.go cmd/proxy_test.go
git commit -m "feat(cmd): thread proxy config into compose options"
```

---

## Task 6: full-suite verification

**Files:** none (verification only).

- [ ] **Step 1: Run the full test suite**

Run: `go test -tags netgo ./...`
Expected: PASS across all packages (no regressions).

- [ ] **Step 2: Vet**

Run: `go vet ./...`
Expected: no output (clean).

- [ ] **Step 3: Coverage snapshot (optional, matches CLAUDE.md)**

Run: `go test -tags netgo -coverprofile=/tmp/docktree.cover ./... && go tool cover -func=/tmp/docktree.cover | tail -1`
Expected: a total coverage line; the new code paths in `identity`, `config`, and
`compose` are exercised by Tasks 1–5.

- [ ] **Step 4: Commit (if anything was adjusted during verification)**

```bash
git add -A
git commit -m "test: verify hostname-proxy phase 1 generation layer"
```

---

## Self-Review

**Spec coverage (against `2026-06-09-hostname-proxy-design.md`):**
- §4 Caddy `caddy`/`caddy.reverse_proxy` labels keyed by hostname → Task 3. ✓
- §4 port drop for proxied HTTP services → Task 3 (`delete(raw,"ports")`). ✓
- §4 `docktree_proxy` network attach + external render → Task 3. ✓
- §4 `RenderProxy` global compose doc + CA volumes + healthcheck → Task 4. ✓
- §5 engine=caddy default → Task 2 (`normalizeProxy`). ✓
- §6 `dns_suffix` default `localhost`, configurable → Task 2 + Task 3 (`proxyHost`). ✓
- §8 `expose`/`proxy_port` config + `isHTTPPort` allowlist classification → Tasks 2, 3. ✓
- §8 binary port-drop (no publish hybrid) → Task 3 (no hybrid path). ✓
- §9 `DNSLabel` single source of truth (`_`→`-`) → Task 1, used in Task 3. ✓
- §9 `composeOptions` threads `Options.Proxy` → Task 5. ✓
- **Deferred to later phases (correctly out of scope here):** lifecycle/`cmd/proxy.go`
  (Phase 2), label-derived URL surface / `FindURL` (Phase 3), `docktree trust`
  (Phase 4), host-port-allocation skip in `resolve.go` (Phase 2 — Phase 1 leaves the
  unused `*_PORT` token in the env file, which is harmless).

**Placeholder scan:** none — every code step contains complete code; every run
step has an exact command and expected result.

**Type consistency:** `ProxyOptions{Enabled, DNSSuffix}`, `ServiceOptions.Expose
(string)`, `ServiceOptions.ProxyPort (int)`, `ProxyRenderOptions{Image, Network,
HTTPPort, HTTPSPort}`, `config.Proxy{Enabled, Engine, DNSSuffix, HTTPPort,
HTTPSPort}` are used identically across Tasks 2/3/4/5. `IsHTTPPort` allowlist
matches `internal/dockerstate`'s `isHTTPPort`. `ProxyNetworkKey`/`ProxyDataVolume`/
`DefaultProxyImage` names are consistent between Tasks 3 and 4.

**Note for the executor:** `RenderProxy` (Task 4) is added but not yet *called* by
any command in this phase — Phase 2 wires it into a global proxy lifecycle. It is
fully unit-tested here so Phase 2 builds on a verified generator.
```
