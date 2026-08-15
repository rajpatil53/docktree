# Docktree — Hostname Routing via a Global Reverse Proxy (Design)

**Status:** design proposal, pending review. Additive and opt-in; no behavior
changes unless `[proxy] enabled = true`.

**Revision 2** — incorporates a plan review and verification of Caddy/Traefik/DNS
behavior. Notable changes from r1: default proxy engine is now **Caddy**
(`caddy-docker-proxy`), which dissolves two correctness bugs found in r1 (a
Traefik global service-name collision and a TLS wildcard-depth bug) and removes
the `mkcert` dependency; the offline `/etc/hosts` mutator is **deferred** out of
v1; the URL surface is rebuilt from labels (not ports); HTTP classification is
tightened to the existing `isHTTPPort` allowlist.

**Goal:** replace the per-worktree *dynamic host port* surface for **HTTP
services** with stable, derivable hostnames behind one machine-global reverse
proxy, so developers (and AI agents) reach a worktree's web/API at a predictable
URL that never churns — while keeping docktree's existing port + stateful-fork
model unchanged for databases and other non-HTTP services.

This is the [ddev](https://ddev.com) / Lando model adapted onto docktree's
existing tiers, labels, generated projections, and shared network.

---

## 1. Motivation

docktree reaches a worktree's services at `http://localhost:<port>`, where
`<port>` is `base + fnv1a(slug) % bandWidth` (`internal/ports/ports.go`),
registry-reserved under flock and bumped on bind collision. For the **HTTP**
tier this hurts:

- The port is **non-derivable** and can **change** (bump on collision/restart
  unless `fixed`). You must run `docktree open` to learn it; it breaks bookmarks,
  OAuth redirect allowlists, webhooks, frontend `.env`, and forces agents to
  re-discover it.
- It is a **flat 16-bit namespace** the whole machine contends for — the reason
  the registry/flock/probe/bump machinery exists.

A hostname is **derivable** (`<service>.<slug>.<app>.<suffix>`), **fixed for a
worktree over time**, and lives in a hierarchical namespace where collisions are
impossible by construction. HTTP services route onto one `:80`/`:443` by `Host`
header and drop their host ports entirely.

### Why this does NOT replace ports wholesale

Host-header routing is an HTTP-family concept (HTTP/1.1, HTTP/2, gRPC,
WebSocket). Raw TCP (Postgres, Redis, MySQL) has no hostname on the wire below
TLS; SNI routing needs TLS-capable clients and never isolates *data*. docktree's
shared-infra + stateful-fork model (`internal/stateful`, `internal/dbmode`)
already solves the data tier. **So this is a hybrid: proxy for HTTP, ports +
forks unchanged for the rest.** Databases keep showing `localhost:5432`.

---

## 2. Goals / Non-goals

**Goals**
- Stable, derivable, restart-stable URLs for HTTP services with no port.
- Single tool: the developer runs only `docktree` (never Caddy/DNS directly).
- Zero new *per-worktree* state — routing rides labels docktree already emits.
- Fully opt-in; when disabled, behavior is identical to today.
- Graceful fallback to `localhost:<port>` when the proxy/DNS isn't available.

**Non-goals**
- Routing non-HTTP/TCP services by hostname (kept on ports + forks).
- Managing a public DNS zone or per-host ACME certs.
- Mutating `/etc/hosts` (deferred — see §6).
- Multi-host. Single machine only, as today.

---

## 3. Architecture — a new global tier

```
Tier 0   docktree-proxy         ONE per machine. Binds :80/:443. Routes every
            │                    app + worktree HTTP service by Host header.
            │  reaches services over the global `docktree_proxy` network
Tier A   <app>-infra            per app (postgres, redis, …)        [unchanged]
Tier B   <app>-<slug>           per worktree (the app stack)        [unchanged]
```

**Why Tier 0 is global, not per-app.** Only one process can bind host `:80`, so a
proxy per `<app>-infra` would collide. One proxy is also *sufficient*: Host-header
routing fans every app/worktree out from a single `:80`. This mirrors ddev's
single `ddev-router`. The proxy therefore lives in machine-global state beside
`registry.json`, **not** inside any worktree or `<app>-infra`.

| Artifact | Location | Project |
|---|---|---|
| Proxy compose | `~/.config/docktree/proxy/compose.yml` | `docktree-proxy` |
| Caddy CA + data | named volume `docktree_caddy_data` → `/data` | (precious; see §7) |
| Proxy network | external bridge `docktree_proxy` | — |

### Network wiring

A global `docktree_proxy` bridge connects the proxy to every routed service,
while each app keeps its isolated `<app>_shared` for intra-app DNS:

- Proxy joins **only** `docktree_proxy`; runs with
  `CADDY_INGRESS_NETWORKS=docktree_proxy` so `{{upstreams}}` resolves backends on
  the right network (auto-detection is best-effort).
- Each HTTP worktree service joins `[default, docktree_shared, docktree_proxy]`.
- `<app>_shared` remains the per-app isolation boundary; `docktree_proxy` is
  purely proxy→service.

*Alternative considered:* dynamic `docker network connect <app>_shared
docktree-proxy` per app (ddev's approach). Rejected as default — the static global
network avoids attach/detach choreography. Kept as a fallback.

### Request path

```
browser → https://api.feature-x.shop.localhost
        → DNS: *.localhost → 127.0.0.1 (no config; browsers, any depth)
        → docktree-proxy :443  (Host: api.feature-x.shop.localhost)
        → site block keyed by that host → api container : 3000 over docktree_proxy
        → TLS: Caddy internal-CA leaf for that exact host (auto-minted, trusted)
```

---

## 4. Routing mechanism — Caddy label per service, keyed by hostname

`caddy-docker-proxy` watches the Docker socket (read-only) and builds an
in-memory Caddyfile from `caddy.*` labels, hot-reloading on container events. The
**site block is keyed by the hostname**, so each worktree's unique host yields its
own non-colliding route — no global service-name namespace to collide in (the bug
Traefik has). docktree emits the labels at render time from values it already
knows (`app`, `slug`, `service`, and the container port from the parsed model), so
routing state rides Docker labels — no per-worktree routing file
(consistent with CLAUDE.md "let Docker labels carry the state").

```yaml
# ~/.config/docktree/proxy/compose.yml  (docktree-owned, machine-global)
services:
  docktree-proxy:
    image: lucaslorentz/caddy-docker-proxy:2.11-alpine
    environment:
      CADDY_INGRESS_NETWORKS: docktree_proxy
    ports: ["80:80", "443:443"]
    volumes:
      - "/var/run/docker.sock:/var/run/docker.sock:ro"
      - "docktree_caddy_data:/data"        # CA lives here — MUST persist (§7)
      - "docktree_caddy_config:/config"
    networks: [docktree_proxy]
    restart: unless-stopped
    healthcheck:                            # admin API is always up, even with 0 sites
      test: ["CMD", "wget", "-qO-", "http://localhost:2019/config/"]
      interval: 5s
      timeout: 3s
      retries: 12
volumes:
  docktree_caddy_data: { name: docktree_caddy_data }
  docktree_caddy_config: { name: docktree_caddy_config }
networks:
  docktree_proxy: { name: docktree_proxy, external: true }
```

A worktree HTTP service after render — the only deltas vs. today are the two
`caddy.*` labels, the `docktree_proxy` network, and dropping the published port:

```yaml
  api:
    networks: [default, docktree_shared, docktree_proxy]    # proxy net added
    labels:
      com.docktree.app: shop                 # already emitted today
      com.docktree.slug: feature_x           # already emitted today
      com.docktree.service: api              # already emitted today
      caddy: api.feature-x.shop.localhost                   # + NEW (slug _→-)
      caddy.reverse_proxy: "{{upstreams 3000}}"             # + NEW (port = model.Port.Target)
    # ports: ["${API_PORT:-3000}:3000"]   ← REMOVED for proxied HTTP services
```

`3000` is the container-internal port from the parsed model
(`internal/compose/model.go` `Port.Target`). The host label value is built by a
shared `identity.DNSLabel` transform (slug `_`→`-`; lowercase), the single source
of truth used by both the label and `docktree open`/`explain`.

---

## 5. Proxy engine — Caddy (default)

| | **Caddy (`caddy-docker-proxy`) — chosen** | Traefik (alternative) |
|---|---|---|
| Route naming | keyed by **hostname** → no collision | global service-name namespace → must emit unique `<app>-<slug>-<service>` |
| Trusted local TLS | **built-in CA, per-host leaf, any `.localhost` depth, offline** | needs `mkcert`; `*.localhost` wildcard matches only one label → must flatten host or do on-demand certs |
| Hostname shape | pretty dotted `api.feature-x.shop.localhost` | must flatten to one label (`api-feature-x-shop.localhost`) for a wildcard cert |
| Labels docktree emits | `caddy` + `caddy.reverse_proxy` per service | `traefik.enable` + unique service + port |
| Extra host tool | none | `mkcert` |

**Decision: Caddy.** Verification confirmed Caddy special-cases the entire
`.localhost` suffix at any depth and mints trusted per-host leaves from its
built-in CA with no wildcard and no pre-listing, and that caddy-docker-proxy keys
sites by hostname (so no cross-container collision). This dissolves r1's two
critical bugs and removes `mkcert`. **Traefik** stays documented as the
alternative if label-native zero-per-service routing is later preferred over
Caddy's TLS ergonomics.

---

## 6. DNS — zero local config for the browser path

Default suffix **`.localhost`**: verified to resolve to `127.0.0.1` in Chrome,
Firefox, and Safari (macOS 26+) at **any label depth** with no `/etc/hosts`, no
dnsmasq, fully offline. This is the browser happy path and the common case.

`[proxy] dns_suffix` switches the suffix for two escape hatches:
- **Non-browser HTTP clients** (Node/Go server-to-server, Postman, older Safari) —
  `.localhost` is not resolved by the macOS system resolver. Switch to a public
  wildcard that always resolves: `dns_suffix = "127.0.0.1.sslip.io"` →
  `api.feature-x.shop.127.0.0.1.sslip.io`. Zero local config, all clients, needs
  internet. (Caddy still mints a trusted leaf because the trust is CA-based, not
  suffix-based — but sslip.io is not `.localhost`, so set `tls internal` for that
  suffix.)
- **Vanity** `*.dt.example → 127.0.0.1` for prettier all-client URLs at the cost
  of owning a domain.

**Offline floor:** `127.0.0.1:<port>` always works with zero config. `docktree
open`/`ls` fall back to it when the suffix preflight lookup fails. **`/etc/hosts`
mutation is deferred out of v1** — it conflicts with docktree's "machine state
only in `~/.config/docktree/`" invariant and needs a real elevation/cleanup
story; the `localhost:<port>` floor + the sslip suffix cover the offline and
non-browser cases without it.

No DNS server is embedded or run.

---

## 7. TLS — Caddy internal CA, one-time `docktree trust`

Caddy's built-in CA auto-mints a trusted (once the root is installed) leaf for
every `.localhost` host on config load — no `mkcert`, no wildcard, no ACME, fully
offline. Two operational facts drive the design:

1. **The CA is precious.** It lives at
   `/data/caddy/pki/authorities/local/root.crt`; if the `/data` volume is wiped,
   Caddy regenerates a new CA and all prior trust breaks. docktree treats
   `docktree_caddy_data` as durable infra state and `doctor` warns if it's absent.
2. **A containerized `caddy trust` can't reach the host store.** So `docktree
   trust` runs (all via `internal/runner`, so `fakeRunner`-testable):
   - `docker compose -p docktree-proxy cp docktree-proxy:/data/caddy/pki/authorities/local/root.crt <stable path>`
   - install host-side: macOS `security add-trusted-cert`; Linux
     `update-ca-trust`/`update-ca-certificates` + browser NSS via `certutil`.

   One-time per machine (re-run only if the CA volume is wiped). Without it, HTTPS
   shows a warning and `doctor` points at `docktree trust`; HTTP still works.

---

## 8. HTTP-vs-non-HTTP signal

A service is proxied iff it is HTTP-exposed. Resolution order:

1. Explicit `[services.<name>] expose = "http"` (or `"none"`) in `docktree.toml`.
2. Default heuristic: a service publishing a host-port token whose **container
   target port is in the existing `isHTTPPort` allowlist**
   (`internal/dockerstate/dockerstate.go`, e.g. `{80,443,3000,8080,…}`) **and** not
   in `shared`/`stateful`. This reuses docktree's current conservative HTTP test
   rather than "any published port" (which would misroute workers, metrics
   endpoints, mailcatcher, non-shared redis).

`doctor` prints the inferred classification so it is never silently wrong.
Multi-port services must name the routed port explicitly; docktree never guesses.

Config additions (`internal/config/config.go`, mirroring the existing
`Autostart *bool` tri-state idiom — no string|bool unions):

```toml
[proxy]
enabled    = true
engine     = "caddy"              # caddy | traefik
dns_suffix = "localhost"          # localhost | 127.0.0.1.sslip.io | <vanity>
http_port  = 80                   # fallback entry ports if 80/443 are taken
https_port = 443

[services.api]
expose     = "http"               # http | none ; default inferred (§8.2)
proxy_port = 3000                 # required only for multi-port containers
```

**Port-drop is binary in v1:** proxied → no published host port; not proxied →
host-port token exactly as today. The "proxied *and* still publish a port" hybrid
is deferred to avoid a 4-way test matrix.

---

## 9. Code touchpoints (by function, not line — lines drift)

| Area | File / function | Change |
|---|---|---|
| Proxy projection | `internal/compose/render.go` — new `RenderProxy` | Generate the machine-global `docktree-proxy` compose doc + `docktree_proxy` external network + CA volumes |
| Caddy labels + port drop | `render.go` `applyServiceLabels` → new `applyProxyLabels` | For proxied worktree services: add `caddy`/`caddy.reverse_proxy` labels, attach `docktree_proxy`, `delete(rawService,"ports")` (reuse the fork port-drop already at the `delete(rawService,"ports")` call) |
| Options | `render.go` `Options`; `cmd/orchestrate.go` `composeOptions` | New `Proxy` struct (enabled, engine, suffix, ports) + per-service `Expose`/`ProxyPort` |
| Config | `internal/config/config.go` `rawService`/`Service`, new `[proxy]` | `Expose`, `ProxyPort`, `Proxy` block; plumb to `compose.ServiceOptions` |
| HTTP classification | reuse `dockerstate.isHTTPPort`; `cmd/resolve.go` | Classify; **skip host-port allocation for proxied services** (registry/flock/probe untouched for the rest) |
| URL surface (labels, not ports) | `internal/dockerstate/dockerstate.go` `FindURL`; `cmd/inspect.go` `resolvedURL`; `cmd/ls.go` | **New derivation:** for a container carrying `com.docktree.*` labels with proxy on, build `https://<DNSLabel(slug)>…<suffix>` from labels + the configured suffix (threaded into `dockerstate`). Port-based path stays as the fallback for non-proxied services. Note: once `ports` are dropped, the old port loop yields nothing for proxied services — this is a second path, not a tweak |
| Global proxy lifecycle | new `cmd/proxy.go`; new global compose builder | `composeCommand` is hardwired to a worktree `r`/`--env-file`; add a **worktree-less** builder + `paths.ProxyDir()`/`paths.ProxyCompose()` (sibling of `RegistryFile`). Proxy compose has **no `--env-file`**. `docktree proxy up/down/status`; bring up on first proxy-enabled `up` |
| Readiness | `cmd/readiness.go` | Gate on proxy container running (`ps --status running`, like `infraRunning`) + its compose healthcheck on admin `:2019`; **not** a bare `:80` TCP dial (may be unbound with zero sites) |
| TLS | new `cmd/trust.go` | `docker compose cp` the root out + host-store install, all via `internal/runner` (§7) |
| Slug→host | `internal/identity` — new `DNSLabel` | `_`→`-`, lowercase; shared by the `caddy` label and `open`/`explain`; a test asserts the label and CLI use the same transform |
| Diagnostics | `cmd/doctor.go` | suffix resolves to `127.0.0.1`; `:80/:443` bindable; `docker.sock` mountable; CA volume present + root installed; HTTP classification; multi-port without `proxy_port` |

The render/label/URL/port-bypass changes are local. The genuinely new surface is
the **global proxy lifecycle** (§10) and the **global compose builder**.

---

## 10. Lifecycle (the new machine-global concerns)

| Concern | Mechanism |
|---|---|
| Bring-up | Proxy started on the **first** proxy-enabled `docktree up` on the machine; left running. `docktree proxy up` idempotent ("already running" = success). |
| Concurrency | A **machine-global flock** in `~/.config/docktree/` (sibling of the registry lock; reuse `registry.LockPath` with a `"proxy"` key) guards proxy + `docktree_proxy` network + CA-volume create. Distinct from the per-app `withAppCriticalSection`. |
| Teardown | **Cross-app ref-count**: proxy stays up while any `com.docktree.managed` worktree stack runs anywhere (`docker ps --filter label=com.docktree.managed=true`); `docktree proxy down` (and `prune` when the machine is empty) stops it. CA volume is **not** deleted on teardown. |
| `:80` conflict | If `:80/:443` is taken (another proxy, ddev), `doctor` reports it; fall back to `[proxy] http_port/https_port` (URLs then carry that port — degraded but functional). |
| Per-worktree up/down | **No proxy interaction** — Caddy hot-reloads on Docker events. `docktree down` removes containers → routes vanish. |
| CA durability | `docktree_caddy_data` survives teardown/prune; `doctor` warns if missing (would force re-trust). |
| Failure fallback | If proxy/DNS isn't ready, `open`/`ls`/`explain` fall back to `http://localhost:<port>`; `doctor` explains the gap. |

---

## 11. Backward compatibility & migration

- Entirely **opt-in** via `[proxy] enabled`. Default off → byte-identical to today.
- **Data/TCP services unchanged** (ports + forks).
- `docktree explain` shows the hostname for proxied services and `localhost:<port>`
  for the rest.
- Removing `[proxy]` reverts cleanly: services get their port tokens back on the
  next `up`.

---

## 12. Failure modes

| Failure | Behavior |
|---|---|
| Proxy not up | `open`/`ls` print `localhost:<port>` fallback; `doctor` flags it |
| Suffix won't resolve (non-browser client / offline) | `127.0.0.1:<port>` floor; switch `dns_suffix` to sslip.io for all-client resolution |
| `:80` taken | fallback entry port; `doctor` reports the conflict |
| CA volume wiped | Caddy regenerates CA → re-run `docktree trust`; `doctor` warns |
| No trust installed | HTTPS warning (HTTP still works); `doctor` shows `docktree trust` |
| Service exposes multiple ports | require `proxy_port`; never guess |
| Slug has `_` | `DNSLabel` maps `_`→`-` consistently in label and CLI output |

---

## 13. Testing strategy (per CLAUDE.md)

- `internal/compose/render_test.go`: `RenderProxy` output; a proxied service gets
  `caddy`/`caddy.reverse_proxy` labels + `docktree_proxy` net + **no** `ports`;
  non-HTTP services unchanged; multi-port requires `proxy_port`.
- `cmd/*_test.go` with `fakeRunner`: **every** new command registered/asserted —
  `docker network create docktree_proxy`, `docker volume`/`compose -p
  docktree-proxy up -d`, the proxy readiness `ps`, the cross-app ref-count `docker
  ps`, the `docker compose cp` + host-install in `trust`. `fakeRunner` rejects
  unregistered output commands, so this is a hard gate.
- `cmd/resolve_test.go`: proxied services skip host-port allocation; others still
  allocate/bump.
- URL tests: `FindURL`/`resolvedURL`/`ls` emit label-derived hostnames for
  proxied, `localhost:port` for data; `DNSLabel` `_`→`-` asserted equal to the
  `caddy` label value.
- `cmd/doctor_test.go`: suffix-resolves, `:80`-bindable, CA-present,
  classification, multi-port-missing-`proxy_port`.
- Standard suite: `go test -tags netgo ./...`, `go vet ./...`, coverage.

When §15 phases become tasks, split each into one-logical-unit, test-gated tasks
(write test → green → next), per the project's TDD + `fakeRunner` discipline.

---

## 14. Decisions (resolved) and remaining knobs

Resolved by verification (§5–§7):
1. **Engine: Caddy** (`caddy-docker-proxy`) — dissolves naming + TLS-depth bugs,
   drops `mkcert`. Traefik = documented alternative.
2. **Hostname: dotted multi-level** `<service>.<slug>.<app>.localhost` — verified
   to resolve in browsers at any depth.
3. **Network: static global `docktree_proxy`** — vs per-app `network connect`.
4. **Non-browser clients / offline:** `dns_suffix` → sslip.io; never `/etc/hosts`
   in v1.

Remaining knob (config, not a blocker): the `isHTTPPort` allowlist contents and
whether `expose` defaults on or off per project.

---

## 15. Phased rollout

- **Phase 0 — spike:** one global `caddy-docker-proxy` on `:80/:443` +
  `docktree_proxy`; one app/worktree routed via `caddy` labels; verify dotted
  `*.localhost` in a browser + Docker Desktop `:80` publish on macOS; export +
  install the CA and confirm green-lock HTTPS; confirm `DNSLabel` `_`→`-`.
- **Phase 1 — render + config:** `[proxy]` config, `Expose`/`ProxyPort`,
  `applyProxyLabels`, binary port-drop, `Options.Proxy`, `isHTTPPort`
  classification. Behind `enabled`.
- **Phase 2 — global lifecycle:** `cmd/proxy.go`, global compose builder +
  `paths.ProxyCompose`, global flock, cross-app ref-count, readiness, `:80`
  fallback, `doctor` checks.
- **Phase 3 — URL surface:** label-derived hostnames in `open`/`ls`/`explain`/
  `FindURL` + `localhost:port` fallback.
- **Phase 4 — TLS + DNS polish:** `docktree trust` (export + host install via
  runner), sslip.io suffix option, CA-volume `doctor` warnings.

**Deferred (not v1):** `/etc/hosts` offline sync; proxied-and-published hybrid
mode; Traefik engine; routing non-HTTP services by SNI.

### Implemented-with-known-deferrals (post-review)

The opt-in feature is shipped through Phase 4. A multi-reviewer pass confirmed the
core mechanics and surfaced these deliberate deferrals (safe behind `[proxy]
enabled`, tracked for a follow-up):

- **Proxy lifecycle teardown.** The proxy is long-lived, exactly like the shared
  `<app>-infra` tier — `docktree down` never stops it; `docktree proxy down`
  stops it explicitly. Automatic cross-app ref-counted teardown in `prune` (stop
  when no managed stacks remain anywhere) is **not yet implemented**.
- **Proxy readiness wait.** `up` brings the proxy up but does not block on its
  admin-API healthcheck; caddy-docker-proxy reconciles routes on Docker events,
  so a route is live shortly after the container is. A first-`up` cold-start
  window can briefly 404.
- **Doctor proxy checks.** The §9 diagnostics (suffix resolves to 127.0.0.1,
  :80/:443 bindable, CA volume present, printed HTTP classification) are not yet
  wired into `docktree doctor`.
- **Custom proxy ports.** `https_port`/`http_port` remap the published ports and
  the `open` URL honors a non-443 `https_port`, but there is no automatic :80
  conflict detection, and the per-machine singleton's ports are taken from
  whichever app last ran `up` (last-writer-wins; harmless at the 80/443 default).

Fixed during review (now correct): proxied services skip host-port allocation
(no registry leak / no false port-drift); `ServiceProxied` excludes shared and
stateful tiers even under `expose="http"`; `open` surfaces compose-model errors
under proxy mode instead of printing a dead `localhost:port`; the HTTP-port
allowlist is shared between `compose` and `dockerstate`; `{{upstreams 0}}` is
avoided via an :80 default.
