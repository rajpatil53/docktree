# Ambassador uplink: per-worktree scoped resolution of shared services

**Status:** approved design, implementation on `feat/ambassador-uplink`
**Date:** 2026-06-11

## Problem

The worktree projection attaches every worktree service to both its project
`default` network and the app-wide `<app>_shared` network so apps can dial
shared infra by canonical name (`postgres:5432`). Compose unconditionally
registers each service's name as a DNS alias on every network it joins (the
implicit alias cannot be suppressed; `aliases: []` is a no-op — verified
against `docker/compose` `getAliases`). With N worktrees running, the shared
network carries N identical `redis` aliases, and a container attached to
multiple networks resolves `redis` through an unspecified moby endpoint
ordering (`Endpoint.Less`: gw_priority, gwbridge/internal heuristics,
lexicographic network name). Cross-worktree interference results: one
worktree's test run can flush another's redis.

The attachment exists only for outbound worktree→infra dialing. Nothing
inbound depends on it: the Caddy proxy routes by container IP over
`docktree_proxy`, and no shared→worktree connection exists in code or docs.

## Invariant established by this design

> A worktree's `default` network is the only network its app services join.
> Every canonical name resolves there exactly once: worktree services via
> their native compose aliases, shared infra via the worktree's uplink
> ambassador. `<app>_shared` carries only infra services (plus inert uplink
> endpoints). No generic alias ever exists on a network visible to two
> worktrees.

## Design

### Topology

The worktree projection injects one generated service, `dt-uplink-<slug>`
(alpine/socat). It is the only worktree container on `<app>_shared`:

- on `default` it carries explicit aliases for every shared,
  non-volume-forked service (`networks: default: {aliases: [postgres, …]}`);
- on `docktree_shared` it has no docktree-relevant alias (its implicit
  service-name alias is slug-unique, so nothing collides);
- it runs one socat TCP forwarder per (service, container port):
  `socat TCP-LISTEN:<port>,fork,reuseaddr TCP:dt-upstream-<svc>:<port>`.

The infra projection gives each shared service a docktree-owned alias on the
shared network: `networks: docktree_shared: {aliases: [dt-upstream-<svc>]}`.
The uplink dials `dt-upstream-<svc>`, never the bare name — a dual-homed
ambassador resolving the bare name would get its own default-net alias back
and forward to itself (verified empirically).

Apps keep dialing `postgres:5432` unmodified. The name resolves on the
worktree's own network to the uplink; socat forwards over the shared network
to infra. `redis` (not shared) resolves only to the worktree's own redis.

### Why no reconcile machinery exists

socat `fork` mode opens the upstream per accepted connection, re-resolving
`dt-upstream-<svc>` against Docker's embedded DNS each time. Infra
recreation (image bump, config change, force-recreate) is healed by the next
connection with no docktree involvement — verified empirically: infra
postgres force-recreated onto a new IP; an untouched uplink forwarded the
next connection correctly. The dt-upstream aliases are declarative infra
projection config, so they re-register on every recreate, including
out-of-band `docker compose -p <app>-infra up --force-recreate`.

### Readiness semantics

An L4 forwarder accepts TCP even when infra is down (accept-then-close). To
keep readiness honest:

- the uplink gets a healthcheck that probes every `dt-upstream-<svc>:<port>`
  (infra reachable) and every local `127.0.0.1:<port>` listener (a crashed
  forwarder must not stay green); infra-down shows as an unhealthy container;
- `depends_on` edges that previously pointed at shared services are
  rewritten to `dt-uplink-<slug>: {condition: service_healthy}` instead of
  being deleted, so dependents wait for a live path to infra (waitShared
  still gates real infra readiness before the worktree stack starts);
- in-container wait scripts doing bare `nc -z postgres 5432` will
  false-positive while infra is down; documented limitation of any proxy.

### Fork granularity

`uplinked = shared − volume-forked(slug)`. A volume-forked service loses
exactly its own alias and forwarders — the real forked container carries the
canonical name on `default` — while every other shared service keeps its
uplink. This replaces the previous blunt rule where any fork detached the
entire worktree from the shared network, severing all other shared services.
Postgres fast-path (logical DB) forks are not in `opts.Forked` and correctly
keep their uplink: they genuinely dial shared postgres. When every shared
service is forked, no uplink is injected and the worktree projection does
not reference the shared network at all.

### Port derivation and edge cases

Forwarded ports per shared service: declared compose `ports` targets
(protocol ≠ udp) plus `expose` entries (`5432`, `4222/tcp`, and ranges up to
32 ports; udp entries skipped), deduplicated, sorted. A shared service with
no derivable TCP ports is a render error (silently skipping it would break
its consumers at runtime with NXDOMAIN). If two uplinked services declare
the same container port, one listener cannot serve two names on one IP, so
colliding services are split into dedicated `dt-uplink-<slug>.<svc>`
ambassadors — the dot segment extends the slug-unique primary name, so a
dedicated name can never equal another worktree's uplink name. A user
compose service named `dt-uplink`, `dt-uplink-*`, or `dt-uplink.*` is a
render error (reserved namespace; names like `dt-uplinker` stay usable).

### Env artifact and recipes

`.docktree/.env.worktree` gains `DOCKTREE_WORKTREE_NETWORK=<project>_default`.
One-off recipe containers attach that single network and resolve `redis`,
`postgres`, `minio` each exactly once, to the right instance.
`DOCKTREE_SHARED_NETWORK` remains valid for reaching infra directly — and is
cured of the original ambiguity, since worktree services no longer alias
themselves there.

### Lifecycle and migration

No imperative networking anywhere: `cmd/` flow, the per-app flock, down,
prune, and doctor are untouched. The uplink is born and dies with the
worktree compose project; the default network stays compose-managed.

One ordering change: `ensureInfraForUp` always runs idempotent
`infra compose up -d` instead of skipping when infra looks ready. Compose
converges only on config change, which is exactly what delivers the new
`dt-upstream-*` aliases to a running infra stack on first post-upgrade up —
and reconciles future infra config drift declaratively. For that to be safe,
infra services consume a worktree-INVARIANT env artifact
(`.docktree/.env.infra`: stable infra project name, registry-backed shared
ports, `[env]` templated with the main slug) instead of the per-worktree
`.env.worktree` — otherwise every cross-worktree up would change the infra
config hash and bounce the shared databases.

Worktree `up` (and fork/unfork recreates) pass `--remove-orphans`: services
that vanish from a regenerated artifact — a fork's dropped uplink, an
unfork's dropped local copy — must not linger with canonical aliases, or
dual-DNS resolution returns through the back door.

Migration is one artifact regeneration: worktree services recreate once
(network set changed), infra recreates once (new aliases). No data impact,
no user action.

### Image dependency

`alpine/socat` (pinned tag, `DefaultUplinkImage`) becomes a pull dependency
of the shared-services path. Like the proxy image, it is a pinned constant
with a render-level override (`Options.UplinkImage`) and no TOML knob in v1;
a config knob for both images can land together later. Airgapped machines
must pre-seed it.

## Out of scope / follow-ups

- `docktree doctor` drift check comparing running infra aliases against the
  projection (cheap, read-only; follow-up).
- `docktree ps` filtering/annotation of the uplink row (cosmetic).
- UDP shared services (no forwarder in v1; render error if a shared service
  has only UDP ports).
- A break-glass `docktree net attach` for IP-announcing protocols (redis
  cluster announce-ip and similar regress behind any proxy topology).
- The machine-global shared-services tier reuses this mechanism: a global
  projection carries `dt-upstream-*` aliases on a global network and the
  uplink grows forwarders. Designed separately.

## Alternatives rejected

- **Inversion** (infra containers `docker network connect`-ed into each
  worktree network): equally correct topology, but endpoints are imperative
  runtime state lost on every infra recreate, requiring a sync primitive in
  four commands, doctor repair, a flock on down, prune GC, and an
  interrupted-state matrix. ~3× the code in the imperative layer.
- **Selective attachment** (only depends_on-inferred consumers stay
  dual-homed): does not fix the class — consumers still collide.
- **`extra_hosts: host-gateway`**: breaks exactly when the registry bumps a
  shared host port off its canonical value; silent wrong-database risk.
- **`gw_priority` pinning**: rests on an undocumented moby ordering side
  effect; engine-version floor; rejected as a durable fix.
- **Alias suppression / custom DNS / scoped lookups / links**: each verified
  impossible or contract-violating.
