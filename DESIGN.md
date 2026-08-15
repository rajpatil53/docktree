# Docktree — Design

**Status:** implemented rewrite baseline. The original Go code in this repo was
a throwaway POC; this document describes the **complete rewrite** now represented
by the Docktree command surface and generated artifacts. There is **no
backward-compatibility or migration constraint** (the POC was never deployed for
real), so we are free to choose the cleanest possible model.

> Naming is settled: the product and canonical binary are **Docktree** /
> `docktree`. The optional short alias is `dt`. The old working name `wtc`
> collided with an existing npm package in the same problem space, so the rewrite
> does not carry that name forward.

**Supported hosts:** Linux and macOS (incl. Docker Desktop). Windows only via
WSL2. This lets us use POSIX file locking and Linux container tooling without a
cross-platform abstraction.

**Compose floor:** Docker Compose **v2.24+**, after pinning the generated
projection behavior, `depends_on` handling, and `config --format json` usage that
shifted across earlier 2.x minors.

---

## 1. What Docktree is

`docktree` gives every **git worktree** of a project its own isolated Docker app
stack on one machine, while letting you **choose which infrastructure is shared**
across those worktrees. You point it at an (almost) unmodified
`docker-compose.yml`, follow a few small conventions, and:

```
docktree up        # this worktree's stack comes up on collision-free ports,
                   # joined to one long-lived shared-infra stack
docktree ls        # see every worktree: branch, status, ports, URLs, data mode
docktree fork db   # give THIS worktree its own copy of a stateful service
```

The headline mental model (borrowed from coasts, which the design is heavily
informed by): **each worktree gets its own app stack and stable ports; expensive
stateful infrastructure is shared until you choose to fork it.** Inside any
worktree the app dials the canonical service name/port
(`postgres:5432`) and generally need not know whether that resolves to the shared
infra or a per-worktree copy.

### Goals

- **Stack-agnostic.** No assumptions about Rails/Postgres/"backend+frontend". Any
  compose stack works; databases are optional.
- **Convention over configuration.** Zero config for the common case; a tiny
  optional `docktree.toml` only for sharing/isolation/secrets choices.
- **Light & derive-from-Docker.** A one-shot CLI. No daemon, no Docker-in-Docker,
  no background reconciler. **Docker is the source of truth for runtime state**
  (what's running, what's forked); the only files Docktree persists are a soft port
  registry and per-worktree *render artifacts* it can regenerate at will.
- **Non-destructive.** Inside a repo/worktree, Docktree owns only files under
  `.docktree/`; machine-wide state is limited to `~/.config/docktree/`. It never
  rewrites your compose or clobbers your `.env`. The compose still runs by hand
  (with defaults) if Docktree disappears.
- **Deterministic.** The same worktree resolves to the same identity and (where
  free) the same ports, so URLs/bookmarks/webhooks are stable.

### Non-goals

- Multi-host / remote environments (coasts' reverse-SSH tunnels). Single host only.
- A web dashboard / daemon. `docktree ls --json` is the programmatic surface; `docker`
  is the live truth.
- Owning secrets. We prepend the user's existing secrets tool as a command wrapper
  (so the "runs by hand" promise covers ports/networking, **not** secrets — see §7).
- Creating worktrees. `git worktree add` is the user's job; Docktree orchestrates
  stacks on top of existing worktrees (optionally via a hook that runs `up`).

---

## 2. What the POC taught us (keep / discard)

**Keep:** deterministic **hash + live-probe** ports with in-band bumping; the
**pure, side-effect-free safety guards** for destructive data ops (the best part of
the POC); **atomic writes** (temp+rename); deriving the main worktree via
`git rev-parse --git-common-dir`.

**Discard:** the role-based manifest and its dead fields; from-scratch `.env`
writes; the concatenation-dependent override that required the user to pre-strip
ports; the machine-global registry as a *hard* cross-app guarantee (kept only as a
soft reservation, §9); POSIX-`cksum` byte-compatibility; the
`RAILS_PORT`/`POSTGRES_DB`/hardcoded-DSN vocabulary.

**Rename:** the POC's package/command/config names (`wtc`, `.wtc/`, `wtc.toml`,
`~/.config/wtc`) are implementation leftovers. The rewrite uses `docktree`,
`.docktree/`, `docktree.toml`, and `~/.config/docktree/`.

---

## 3. Architecture

Two compose-project **tiers** over a single, (almost) unmodified user compose
file. No daemon; every command re-derives state from git + compose + Docker.

```
                 ┌─────────────────────────────────────────────┐
   git worktrees │  main/        feature-a/      feature-b/     │
                 └─────┬───────────────┬───────────────┬────────┘
                       │ docktree up   │ docktree up   │ docktree up
        per-worktree   ▼               ▼               ▼
        app stacks  app-main        app-feat_a      app-feat_b      (TIER B)
                           └──────┐        └──────┐        │  via per-worktree
                                  ▼               ▼        ▼  dt-uplink ambassadors
                          ┌───────────────────────────────────────┐
        shared infra      │   <app>-infra   (postgres, redis, …)   │ (TIER A)
        (long-lived)      │   network: <app>_shared                │
                          └───────────────────────────────────────┘
```

- **Tier A — `<app>-infra`** (long-lived, one per project): the *shared* services,
  brought up once, attached to an external bridge network `<app>_shared`,
  publishing canonical ports on **stable** host ports. Survives `docktree down`;
  removed only by `docktree shared down|nuke` or `docktree prune` (when no
  worktree stacks remain).
- **Tier B — `<app>-<slug>`** (one per worktree): the per-worktree services, run via
  the compose invocation in §5.1. App services stay on the worktree's own
  network; a generated `dt-uplink-<slug>` ambassador is the only Tier-B
  container that joins `<app>_shared`, answering to the shared services'
  canonical names locally and forwarding to the infra stack.

**State — two places only (Docker is the third, authoritative, runtime truth):**
1. `~/.config/docktree/registry.json` — *soft* per-app port-band + stable-shared-port
   reservations (flock-guarded, atomic). Missing registries are recreated; corrupt
   registries fail closed rather than being overwritten and losing reservations.
2. `<worktree>/.docktree/` — *render artifacts* Docktree regenerates on every
   `up`: `env` (the env slice) and generated compose files/projections. These are
   derived output, **not** authoritative state.
3. **Docker** is the authority for runtime state. "Is it up?" = `docker compose ls`
   + `docker ps --filter label=docktree.app=<app>`. **Fork status is derived, not
   stored:** a generic volume fork exists iff the per-worktree volume
   `<app>-<slug>-<svc>data` exists and carries the `docktree.fork=<svc>` label; a
   built-in engine fork (for example Postgres logical DB isolation) is preserved
   from the current `.docktree/.env.worktree` connection string because logical databases do
   not carry Docker labels. There is **no `state.json`** — this avoids drift
   between a stored flag and the generated artifacts.

**Isolation primitive & its limits.** `COMPOSE_PROJECT_NAME = <app>-<slug>`
isolates the resources Compose names by default: container names, the default
network (`<project>_default`), and **default-named** volumes
(`<project>_<volname>`). It does **not** isolate: top-level volumes with an
explicit `name:` or `external: true`; the shared external network (shared on
purpose); or services with an explicit `container_name:` (which becomes a fixed,
unprefixed name and **collides** across worktrees — the second `up` fails).
`docktree doctor` flags `container_name:` and explicitly-named/external top-level
volumes in the user compose, and the generated projection parameterizes/strips
`container_name` where it can.

**Why no daemon / no DinD.** coasts needs both because each instance is a nested
Docker engine, forcing virtual ports + socat forwarders + a SQLite reconciler to
bridge engine boundaries. We are single-host on one engine, so "reachable shared
service" is just "same Docker network + DNS / a stable host port." All of that
machinery collapses away.

---

## 4. Identity & naming

- **slug** = sanitized worktree directory basename (`[a-z0-9_]`, trim, empty →
  `main`, long names truncated + short hash suffix). Directory-based (not branch):
  branches get renamed and worktrees can be detached; the dir is stable. Branch is
  shown in `ls` for humans. Because the slug partitions ports/projects/volumes, a
  post-sanitization **collision** (`feature-a` and `feature.a` → `feature_a`) is
  detected against the registry and disambiguated with a short hash suffix.
- **app** = explicit `docktree.toml app=…` else the git repo / top-level dir name.
- **main slug** = slug of the primary worktree via `git rev-parse --git-common-dir`
  (CWD-independent). Identifies the canonical owner of shared data.
- **project name** = `<app>-<slug>` (Tier B) / `<app>-infra` (Tier A). **Locked
  formula** — changing it later orphans running stacks.
- **hash** = FNV-1a 32-bit over the slug (clean, deterministic; no compatibility
  constraint). Used for the per-worktree port offset.
- **slug validation** (`^[a-z0-9_]+$`) is enforced before a slug is interpolated
  into any shell command, volume name, or Docker arg.

---

## 5. Conventions (the keystones)

Your `docker-compose.yml` stays runnable by plain `docker compose` and structurally
unchanged. Three conventions make it Docktree-aware:

```yaml
services:
  api:
    build: .
    ports: ["${API_PORT:-3000}:3000"]      # 1. host ports as overridable tokens
    environment:
      DATABASE_URL: ${DATABASE_URL:-postgres://app:app@postgres:5432/app_dev}
    depends_on: [postgres]
  postgres:
    image: postgres:16
```

```toml
# docktree.toml
shared = ["postgres"]                       # 2. shared-service membership
```

1. **Host ports as `${VAR:-default}` tokens.** Docktree is the sole writer of those
   tokens, so it injects collision-free ports without editing the compose. **A
   service whose host port is hardcoded (`"3000:3000"`) is NOT isolated** —
   `docktree up` warns (with a suggested token name) and proceeds, but that port
   is shared and will collide across worktrees. Docktree never double-publishes
   (it does not add a second `ports` entry). So the token convention is the
   load-bearing precondition for per-worktree isolation, not an optional nicety.
2. **`docktree.toml shared = [...]` is the gate** that keeps a service out of the
   per-worktree stack and into Tier A. Compose `profiles:` / labels are not the
   source of truth. This keeps the user's compose ordinary and puts Docktree's
   topology choices in one explicit config file.
3. **Canonical service names stay canonical.** Apps keep dialing `postgres:5432`,
   `redis:6379`, etc. Docktree creates one external network (`<app>_shared`) for
   the infra projection, and each worktree projection injects a generated
   `dt-uplink-<slug>` ambassador: it carries the shared services' canonical
   aliases on the worktree's own default network and TCP-forwards each port to
   `dt-upstream-<svc>` over `<app>_shared`. Every canonical name therefore
   resolves exactly once on the worktree's network — worktree services natively,
   shared services via the uplink — and no worktree service ever joins the
   shared network, so no generic alias is ambiguous anywhere even with N
   worktrees running. The base compose does not have to declare any of this.

`docktree init` scaffolds `docktree.toml` from an existing compose; `docktree
doctor` diagnoses when conventions are missing or inconsistent.

---

## 6. Configuration

Three layers of decreasing magic; the common case needs none.

- **Discovered from compose:** services, container ports, which host ports use
  `${VAR:-default}` tokens, declared networks. Read via `docker compose config
  --format json` (resolves anchors, overrides, interpolation, and profiles).
  Discovery runs with a **discovery-only env** that supplies harmless defaults so
  unset interpolation vars don't error (compose warns but still emits the model);
  this resolves the chicken/egg of "need ports to render config, parse config to
  find ports."
- **Inferred from git:** `app`, `slug`, `main_branch`
  (`git symbolic-ref refs/remotes/origin/HEAD`, fallback `main`), main slug.
- **Declared in optional `docktree.toml`** — only what can't be inferred.

**Minimal — often nothing.** With port tokens and no sharing/isolation needs,
`docktree up` works with **no** `docktree.toml`.

Mark a shared service:

```toml
app = "myapp"
shared = ["postgres"]          # everything else is per-worktree
```

Richer (sharing + data isolation + namespacing + secrets):

```toml
app = "flexiple"
compose = "docker-compose.yml"        # optional; auto-discovered otherwise

shared = ["postgres", "redis", "opensearch"]

[services.api]
host_port_env = "API_PORT"            # the ${API_PORT:-3000} token in compose
fixed = true                          # never bump (e.g. OAuth redirect URIs); error instead

[services.frontend]
host_port_env = "WEB_PORT"

[services.e2e]
autostart = false                     # part of the stack, but `docktree up` does NOT start it;
                                      # run on demand (`docktree up e2e` / `docktree exec`)

# Stateful-service data isolation (§8). Default `default_strategy` is `shared`
# because most worktrees should not pay for private data unless they need it.
[stateful.postgres]
default_strategy = "shared"                   # shared | isolated
engine = "postgres"                   # enables the built-in no-downtime fast-path
snapshot_source = "myapp_pgdata"      # volume to seed an isolated copy from (generic path)
source_db = "app_shared"              # source DB for the postgres fast-path
superuser = "postgres"                # role psql/createdb/pg_dump connect as; default "postgres"
env = { DATABASE_NAME = "app_shared_{slug}" } # written only when isolated

# Per-worktree namespacing of shared resources, no app code changes.
[env]
OPENSEARCH_INDEX_PREFIX = "{slug}_"

# Secrets (§7): v1 only supports a blanket wrapper. Nothing is stored at rest.
[secrets]
wrapper = "doppler run --"            # default: wrap the whole compose invocation
```

**Substitution variables** available in templated values (`source_db`,
`[stateful.<service>].env`, `[env]`, etc.): `{slug}`, `{app}`, `{main_slug}`.
Unknown `docktree.toml` keys produce a **warning**. Anything omitted falls back
to convention. Stateful `env` values are generated only for isolated services
and override same-key top-level `[env]` values in `.docktree/.env.worktree`.

---

## 7. Environment handling

Docktree **never** reads or rewrites the app's `.env`. It writes only
`.docktree/.env.worktree`.

Two distinct value classes, two delivery channels:

- **Compose-interpolation values** (`COMPOSE_PROJECT_NAME`, the `${*_PORT}` tokens,
  `DOCKTREE_SHARED_NETWORK`): these must be visible to the compose CLI for `${VAR}`
  substitution. A service-level `env_file:` is **not** an interpolation source —
  only the project `.env`, an `--env-file`, or the process env feed interpolation.
  Docktree delivers them via **`--env-file .docktree/.env.worktree`** (the default;
  debuggable and it persists for hand-runs) and/or the compose **process env**
  (`cmd.Env`). Both are valid interpolation channels.
- **Container-facing values** (DB connection strings, namespacing vars): reach
  containers via the generated projection's `env_file: [<app .env>, .docktree/.env.worktree]`
  (last wins).
  Note Compose precedence: a service's own `environment:` entry beats an `env_file`
  value for the same key, so Docktree injects via `env_file` and avoids
  re-declaring keys the app already sets in `environment:`.

Docktree does not seed `.env` by default. If `.env` is absent and `.env.example`
exists, `docktree doctor` reports that clearly and `docktree init --seed-env` can
copy it as an explicit user action.

**Artifact env precedence.** Every projection-targeting compose command Docktree
spawns re-applies the referenced artifact's values (`[env]`, public-URL envs,
stateful overrides, ports, project and network names — worktree commands read
`.docktree/.env.worktree`, infra commands the worktree-invariant
`.docktree/.env.infra`) over the compose process env: the pairs are appended to
the command env (Go's exec gives later duplicates precedence), and when a secrets
wrapper is configured they are also interposed via `env(1)` between the wrapper
and compose (`doppler run -- env -- KEY=VAL… docker compose …`) so they win over
the wrapper's own injection too. Discovery (`docker compose config
--no-interpolate`), the secrets preflight, and the machine-global proxy stack
deliberately stay override-free. Effective precedence for a contested key:
app-pinned `environment:` literal > Docktree artifact value > wrapper-injected
secret > ambient shell > `.env` files (the wrapper-beats-shell link is the
doppler default; a `--preserve-env`-style wrapper inverts it, but the artifact
value wins either way). Process-env values are never re-interpolated by compose,
so no `$`-escaping is involved; under a wrapper the interposed pairs do ride the
argv and are visible in process listings — the same values already sit in the
artifact file on disk. The override exists only inside Docktree-spawned
processes: a hand-run of the projections gets the `--env-file` channel but not
the re-application, so the wrapper wins interpolation there. With a wrapper
configured, `docktree explain` prints the interposed argv, so the printed
command is a faithful hand-run; without one the override rides only the process
env, and a bare hand-run falls back to `--env-file` precedence.

**Secrets wrapper interaction.** The `secrets.wrapper` (e.g. `doppler run --`) is
prepended to the compose process and its injected env therefore reaches **both**
interpolation and containers — except keys the artifacts also define, which the
re-application above makes Docktree-owned. The wrapper must inject env and exec
its trailing argv (`doppler run --`, `op run --`); shell-form wrappers (`sh -c`)
are unsupported. Docktree does **not** manage the secrets tool's auth:
in non-interactive contexts (CI, an editor, `docktree hook install`) it runs a
configurable preflight and **fails fast with a clear message** if the wrapper isn't
ready, rather than letting compose start with silently-missing env. The git hook
does **not** auto-run the wrapper unless explicitly opted in.

Per-secret sources (`command:`, `keychain:`, etc.) are deliberately out of v1. They
can be added later if wrapper-only secrets prove too coarse, but starting with a
single wrapper keeps Docktree out of the secrets business and avoids a local
keystore.

---

## 8. Stateful-service isolation (the data model)

Per **stateful service**, a `default_strategy` decides how its data behaves across
worktrees. **Default is `shared`**; isolate on demand. This is intentional:
Docktree optimizes for many short-lived worktrees where most changes do not touch
schema, seed data, or stateful workflows. Private data is available when needed,
but it should not be the default cost for every branch.

| default_strategy | what happens | how it's realized |
|---|---|---|
| `shared` (default) | all worktrees use the one Tier-A container + its data | reached over `<app>_shared` (DNS) / stable host port |
| `isolated` | this worktree gets its **own copy** that diverges independently, never written back to source | **(a) generic volume snapshot** (default, any engine) **or (b) engine fast-path** (opt-in, see below) |

This mirrors coasts' `Shared Services` and `Isolated Volumes (+ snapshot_source)`.
We **omit** coasts' "Shared Volumes" tier (N containers, one volume) — multiple
writers on one DB data dir corrupts it.

### Realizing `isolated`

`docktree fork <service>` (or declarative `default_strategy="isolated"` applied at `up`):

- **(a) Generic volume snapshot — the default, engine-agnostic mechanism.** Copy the
  `snapshot_source` volume (default = the Tier-A volume) into a per-worktree volume
  `<app>-<slug>-<svc>data` via `docker run --rm -v src:/from:ro -v dst:/to … cp -a
  /from/. /to/`, then re-include the service in the worktree's project mounting the
  clone. **Honest constraints:** (1) a *live* data dir cannot be byte-copied safely
  — the source must be quiesced for the copy, or be a stable baseline/seed volume
  rather than the live shared DB (coasts has the same limitation). (2) "Instant CoW
  reflink" only exists on a **native-Linux** engine whose volume backing is
  XFS/Btrfs/ZFS; on Docker Desktop the volume lives in the Linux VM (ext4) and there
  is no reflink — so the fast snapshot is a `cp`, scoped accordingly.
- **(b) Engine fast-path — opt-in, no-downtime, cheap.** When `engine` names a
  built-in (v1 ships **Postgres** only), Docktree clones at the logical-DB level
  (`CREATE DATABASE … TEMPLATE` / `pg_dump | psql`) inside the shared server and
  creates `<source_db>_<slug>` from `source_db`. This streams from a *live*
  server (no quiesce) and costs one DB's disk, not the whole cluster. It is a
  **named built-in capability** selected by `engine=`, **not** a TOML scripting
  DSL; an unsupported engine simply falls back to (a). A real command-template/plugin seam for MySQL/Mongo is a documented future extension
  (§15), deliberately out of v1 scope to avoid config surface for zero generality.

`docktree unfork <service>` drops the per-worktree copy + its cloned volume
(guarded — see below) and re-points back at shared.

### Service discovery & keeping fork clean on one engine

Discovery has two channels:

- Generic volume forks are discovered from Docktree labels on the forked volume.
- Postgres logical forks are discovered by checking whether `<source_db>_<slug>`
  exists in the shared Postgres service. If isolated, Docktree writes the
  configured `[stateful.<service>].env` overrides into `.docktree/.env.worktree`;
  if shared, those keys are omitted so apps use their normal shared defaults.
- Container-DNS-by-canonical-name works for the common `shared` case (verified:
  services on a shared external network resolve each other by **service name**,
  which Compose registers as a network-scoped alias).

The single-engine hazard: a forked worktree running a local `postgres` must not
also resolve the shared one. Resolution: worktree services live only on the
default network, and the uplink ambassador simply drops the forked service from
its alias/forwarder set (`uplinked = shared − volume-forked`), so the local copy
is the only thing answering to `postgres` on the only network the app is on.
Granularity is per service: forking `postgres` leaves the `minio`/`redis`
uplinks intact instead of severing the whole worktree from shared infra.
Postgres logical-DB fast-path forks keep their uplink — they genuinely dial the
shared engine. Env injection additionally covers apps that read the URL.

### Ordering / readiness

A `depends_on` edge to a shared service is rewritten onto the uplink ambassador
(§ "Compose mechanics") whose healthcheck only proves a live TCP path to infra,
and you cannot `depends_on` a service in another compose project. So `docktree
up` still performs an **external readiness wait** on shared services (poll the
published port / a configurable healthcheck) before starting the worktree
stack — "infra is reachable" is not the same as "infra is healthy."

### Safety guards (pure, unit-tested, enforced unconditionally)

Generalized from the POC's `dbmode`:
- Never drop/overwrite the **shared/main** store — neither the shared **volume**
  (`snapshot_source` / the Tier-A volume) nor the main worktree's logical DB.
- A destructive op (drop volume in `unfork`/`prune`, drop logical DB in the
  fast-path) is permitted **only** for an isolated, non-main worktree's **own**
  resource (`<app>-<slug>-<svc>data` / `<source_db>_<slug>`).
- Unknown/blank current mode ⇒ treated as **unsafe** (no destructive action).
- `docktree shared nuke` is the **single sanctioned exception** to "never drop the
  shared store," behind an explicit guarded prompt.

---

## 9. Ports

**Per-worktree services:** deterministic hash + probe.
`candidate = band_base(app) + (fnv1a(slug) % band_width)`. If free → use it (stable
across runs). If taken → linear-bump within the band, **except** `fixed` services
(OAuth redirect URIs), which **error** rather than move.

- **Probe the address Compose publishes on** (`0.0.0.0`, and IPv6 if relevant), not
  just loopback, so "free" means actually free.
- **Concurrency:** sibling `docktree up`s race on probe-then-bind. Docktree takes a
  **per-app exclusive flock** around the port-resolution + `up` critical section
  (cheap, single-host, still no daemon). A residual TOCTOU between probe and
  Compose's bind remains; the backstop is detecting Compose's bind failure and
  bumping+retrying.

**Shared services:** publish canonical ports on **stable** host ports once, so host
tools reach them at a fixed `localhost` port — coasts' `checkout` *outcome* with no
checkout step, because we never churn the port. When two apps both want `5432`,
Docktree **band-shifts the host-published port per app** (container port stays
canonical, so in-container DNS is unchanged); the actual published port is
recorded and shown by `docktree shared status` / `docktree explain` (so "hit
localhost:5432" is the common case, not an absolute promise).

**Registry** (`~/.config/docktree/registry.json`): a *soft* reservation of bands +
stable shared ports, flock-guarded and atomic. Advisory; cross-app uniqueness
degrades from a hard guarantee to hash+probe best-effort, acceptable on one host.

**No `checkout`.** Every worktree owns a stable distinct port; `docktree open
[worktree] [service]` opens its URL.

---

## 10. Shared-service lifecycle

- `docktree shared up` (auto-invoked by `docktree up` if infra isn't running)
  reads the effective compose model and renders a Docktree-owned infra projection
  containing only `docktree.toml shared = [...]` services. It creates
  `<app>_shared` first if absent, then runs:
  `docker compose -p <app>-infra -f .docktree/compose.infra.yml up -d`.
- **Precondition:** shared services must not `depends_on` non-shared services. Such
  an edge would make the infra projection incomplete and would drag app code into
  Tier A if left intact. `docktree doctor` flags this before `up`.
- **Race-safe bring-up:** concurrent siblings may both try to create the network /
  start infra. Docktree serializes infra bring-up under the per-app flock (§9) and
  treats Docker's "already exists" as success.
- Long-lived: `docktree down` stops only the worktree's stack, never the infra.
- Soft ref-count computed on demand from Docker (`docker ps --filter
  label=docktree.app=<app>` grouped by project); `docktree prune` offers to bring
  infra down only when zero worktree stacks remain.
- `docktree shared down` stops infra; `docktree shared nuke` removes it + volumes
  behind a guarded prompt (the §8 sanctioned exception).
- No daemon ⇒ drift (someone `docker rm`s infra) is noticed on the next command;
  `ls`/`shared status` detect-and-report.

---

## 11. Command surface

| Command | Purpose |
|---|---|
| `docktree up [service…]` | Derive identity, write `.docktree/.env.worktree` + generated compose projections, ensure network + infra are up (+ readiness wait), bring the current worktree's stack up (secrets-wrapper prefixed). Services marked `autostart = false` are part of the stack but **not** started by a bare `up`; name them explicitly (`docktree up <service>`) or use `docktree exec` to start them on demand. |
| `docktree down` | Stop+remove the current worktree's stack only. Leaves any forked volume in place (so a later `up` stays forked). Never touches infra/siblings. |
| `docktree ls [--json]` | All worktree stacks: slug, branch, status, ports, URLs, per-service data mode (derived from Docker) + infra status. Worktree-level (one row per stack). |
| `docktree ps [worktree] [--service]` | Per-**stack** service status (name, state, image, published ports) — a thin `docker compose -p <app>-<slug> ps` wrapper. Answers "which services in *this* stack are up/crashed and on what ports," which `ls` does not. |
| `docktree open [worktree] [service]` | Open/print a worktree service's stable URL. |
| `docktree exec [worktree] <service> -- <cmd…>` | `docker compose -p <app>-<slug> exec …`. |
| `docktree logs [worktree] [service] [-f] [--tail N]` | Tail a worktree stack's logs. |
| `docktree shared up\|down\|status\|nuke` | Manage the long-lived `<app>-infra` stack. |
| `docktree fork <service>` | Give the current worktree its own copy of a stateful service (volume snapshot, or Postgres fast-path). |
| `docktree unfork <service> --confirm <resource>` | Drop the current worktree's copy (guarded), re-point at shared. |
| `docktree explain [worktree]` | Full derivation: app, slug, main slug, project names, network, resolved ports + the actual published port, effective data sources, the exact `docker compose` argv, and the rendered projections. |
| `docktree prune` | GC stopped/orphaned worktree stacks (worktree dir gone) and, if none remain, offer to stop infra. Dry-run by default; forked **volumes** are GC'd only with explicit confirmation and under the §8 guards. |
| `docktree init [--seed-env]` | Scaffold a minimal `docktree.toml` from compose, reserve bands, create the shared network, ensure `.docktree/` is gitignored. With `--seed-env`, copy `.env.example` to `.env` only if `.env` is absent. Idempotent. |
| `docktree doctor` | Validate: daemon reachable; infra up + healthy; conventions present (port tokens, shared services declared in `docktree.toml`, generated projections valid); shared services attached + name-matched + no depends_on to non-shared; `container_name:`/named/external-volume hazards; port drift; secrets wrapper preflight. |

---

## 12. Compose mechanics, projections & invocation

The generated `.docktree/compose.worktree.yml` and `.docktree/compose.infra.yml`
are the riskiest components, because Compose merge semantics are easy to get
wrong and shared-service splitting cannot be expressed as a tiny append-only
override. Docktree therefore treats the user's effective compose model as input,
then renders Docktree-owned projections:

- **Infra projection:** only services listed in `docktree.toml shared = [...]`,
  plus top-level resources they need, attached to `<app>_shared`.
- **Worktree projection:** every non-shared service, attached to its default
  project network (and user-declared networks) only — never `<app>_shared`.
  Shared-service `depends_on` edges are rewritten onto the generated
  `dt-uplink-<slug>` ambassador (`condition: service_healthy`; its healthcheck
  probes the real upstreams, since an L4 forwarder accepts TCP even when infra
  is down), with `docktree up`'s external readiness wait still gating real
  infra readiness. The uplink is the only worktree container on `<app>_shared`
  and dials infra via the docktree-owned `dt-upstream-<svc>` aliases the infra
  projection declares (dialing the canonical name from the dual-homed uplink
  would resolve its own alias — a forwarding loop).
- **Labels:** every generated service, network, and Docktree-owned volume gets
  `docktree.app=<app>` and project/slug/fork labels so `ls`, `doctor`, and `prune`
  can derive state from Docker.

Where Docktree can use ordinary Compose merging, it emits each key with merge
behavior in mind:

| key | Compose merge | Docktree approach |
|---|---|---|
| `ports` | **append** (concatenate) | the user owns the single `${*_PORT}` line; Docktree supplies the value via env, and does **not** emit a competing `ports` entry |
| `environment` | merge by key (override wins) | inject only keys the app does not already set |
| `env_file` | append (last wins) | append `.docktree/.env.worktree` after the app's |
| `networks` | merge by key | app services keep their own nets; only the uplink (worktree) and infra services join `shared` |
| `depends_on` | **union of keys — cannot subtract** | see below |
| `container_name` | replace | parameterize/strip to avoid cross-worktree collisions |

**`depends_on` to a shared service.** Because a per-worktree service can declare
`depends_on` on a service moved into the infra projection, Docktree must rewrite
that edge in the worktree projection. Since `depends_on` merges by **union** (you
cannot subtract a key via an override), the rewrite is done by **re-rendering from
the resolved `docker compose config --format json` graph**: each edge onto an
uplinked shared service is replaced with an edge onto that service's uplink
ambassador (`condition: service_healthy`, preserving an author-declared
`required: false`), handling both the list short-form and the map long-form.
It is a small compose-graph rewriter, not a token injector.

**Cross-project DNS (verified).** Services on a shared `external` network reach each
other by **service name** (Compose registers the service name as a network-scoped
alias on every network the container joins — and that implicit alias cannot be
suppressed, which is exactly why worktree services must not join `<app>_shared`:
N worktrees would register N identical aliases there, and multi-network
resolution order is an unspecified moby implementation detail). Discovery relies
on the service name/alias, not `container_name`.

**Respect existing compose-file resolution.** Many projects already use a
`docker-compose.override.yml` or a `COMPOSE_FILE` list. Docktree must **not**
silently drop them: it discovers the user's effective file set (honoring
`COMPOSE_FILE` / the default override), resolves the effective model, and writes
its generated projections from that model rather than assuming a single
`docker-compose.yml`.

**`autostart = false`.** Such a service is part of the rendered stack but excluded
from a bare `docktree up` — realized via a Docktree-reserved `manual` profile in
the generated projection, so default `up` skips it and it starts only when named explicitly
(`docktree up <service>`) or via `docktree exec`. Compose-native; no daemon
required.

---

## 13. Discoverability & DX

- `docktree ls --json` and `docktree explain` are the agent/automation contract (no MCP
  server). `explain` shows the **deterministic** port derivation (`base +
  fnv1a(slug)%width`, currently-free?) and the **actual published port** from Docker
  labels/`ps`; it does **not** persist or reconstruct historical bump reasons.
- Detect-and-warn on hardcoded host ports (suggest a token name).
- `docktree doctor` turns cryptic compose failures into clear diagnoses (see §11) and
  catches the isolation-defeating hazards (`container_name:`, named/external
  volumes, missing port tokens, invalid shared service names, shared→non-shared
  `depends_on`).
- `docktree prune` closes the leak when a worktree is `rm -rf`'d instead of torn down —
  dry-run by default, with the §8 guards; also detects an **orphaned-running** stack
  (label says slug X, no worktree dir for X).
- `docktree init` ensures `.docktree/` is gitignored idempotently (`env` may hold
  connection strings; generated files are machine-local render output).
- Optional `docktree hook install` (run `docktree up` on `git worktree add`); shell
  completion; a real `--help`/command table.

---

## 14. Risks & mitigations

1. **`depends_on` × shared-service projection (highest risk).** A per-worktree
   service may depend on a service moved to Tier A. **Mitigation:** the worktree
   projection re-renders the resolved graph rewriting shared edges onto the
   healthy-uplink ambassador (§12), then `docktree up` waits for shared-service
   readiness externally. The test asserts the generated worktree model is valid
   and the app service does not pull shared services into Tier B.
2. **Fork networking on one engine.** Dual DNS resolution of a forked service name.
   **Mitigation:** worktree services never join `<app>_shared`; the uplink
   ambassador drops the forked service from its alias set, leaving the local
   fork as the only resolver of the name; injected env (§8) covers URL readers.
3. **Naming shared services on the CLI pulls in their `depends_on`.** A shared
   service depending on an app service drags it into Tier A. **Mitigation:**
   precondition + `docktree doctor` check (§10).
4. **`COMPOSE_PROJECT_NAME` doesn't isolate everything.** `container_name:` collides;
   named/`external` volumes are shared. **Mitigation:** `docktree doctor` checks +
   generated projection parameterization (§3).
5. **Live-volume snapshot consistency.** Can't byte-copy a running DB. **Mitigation:**
   quiesce / seed from baseline / use the engine fast-path; reflink is Linux-only
   (§8).
6. **Compose v2 semantics vary across minors.** Docktree pins Docker Compose
   v2.24+ and keeps projection/status regression coverage around the semantics it
   relies on.
7. **Concurrency:** sibling `up` port races and simultaneous infra bring-up.
   **Mitigation:** per-app flock around resolve+up and infra bring-up; bump-on-bind-
   failure backstop (§9/§10).
8. **No port tokens ⇒ no isolation.** A hardcoded host port can't be remapped; that
   service collides. **Mitigation:** warn, proceed without isolating it, document the
   token convention as the load-bearing precondition (§5).
9. **Stable-shared-port collisions across apps.** Band-shift makes `localhost:5432`
   "wrong" for the shifted app. **Mitigation:** `shared status`/`explain` report
   the real port (§9).
10. **Existing user override / `COMPOSE_FILE`.** Silently dropping them breaks the
    user's stack. **Mitigation:** honor existing file resolution, resolve the
    effective model from the same file set, and generate projections from that
    model (§12).
11. **"Degrades to plain compose" excludes secrets.** Without the wrapper the stack
    starts unsecreted. **Mitigation:** scope the promise to ports/networking; fail
    fast on wrapper preflight (§7).
12. **No daemon ⇒ no live reconciliation.** Drift noticed on next command; `ls`/
    `doctor` detect-and-report.

---

## 15. Open questions

- **Short alias.** `dt` is a good daily-use alias, but it is generic. Lean: install
  `docktree` always; offer `dt` as opt-in shell alias/symlink.
- **Engine fast-path beyond Postgres.** v1 ships Postgres built-in; a
  command-template/plugin seam for MySQL/Mongo is deferred — design it as a real
  interface when needed, not speculative TOML.
- **Detached / bare-clone worktrees.** Deterministic slug rule for these edge cases.

**Considered against coasts and deliberately declined** (kept here so they read as
choices, not oversights): the canonical-port `checkout` switch — Docktree gives
every worktree its own stable port instead, with `docktree open` (no socat, no
contention); **file-mounted secrets and a local secrets keystore** — v1 only wraps
the user's existing secrets command; **per-secret sources** — deferred until wrapper
mode proves insufficient; and **typed manifest variants / `extends` composition** —
use `COMPOSE_FILE` layering for a test/e2e variant of the same project rather than
a manifest inheritance model.

---

## 16. Phased implementation roadmap

- **Phase 0 — Spikes (de-risk the hard mechanisms; produce written decisions).**
  (a) per-worktree `up` over an unmodified compose with `${VAR}` ports +
  `COMPOSE_PROJECT_NAME`; (b) the **compose projection + `depends_on` re-render**
  from `config --json` (§12) and the external-network interplay; (c) **fork
  networking** (non-attach + env injection, §8); (d) the **per-app concurrency
  flock**; (e) pin the **Compose version floor**.
- **Phase 1 — Core up/down + explainability.** compose discovery (tolerant
  `config --json`), git identity, deterministic hash+probe ports (0.0.0.0, flock),
  soft registry, `.docktree/.env.worktree` (+ `--env-file`/process-env), generated
  projections honoring existing compose-file resolution, `autostart = false` (the
  `manual` profile), `.docktree/` gitignore, no-token warn-and-proceed,
  `docktree doctor`, `docktree explain`, `docktree ls --json`, and `docktree open`.
- **Phase 2 — Shared infra.** `<app>-infra`, external network, stable shared ports
  (+ band-shift), readiness wait, `docktree shared up|down|status|nuke`,
  auto-start.
- **Phase 3 — Data isolation.** `default_strategy`, `docktree fork`/`unfork` (generic
  volume snapshot), the pure safety guards incl. the volume-deletion guard, the
  opt-in Postgres fast-path, fork lifecycle across `down`/`prune`.
- **Phase 4 — DX polish.** `docktree ps`, `docktree logs`, `docktree prune`,
  `docktree init`, hardcoded-port warnings beyond the core path, completion/help,
  optional `hook install`, and optional `dt` alias setup.

---

## Appendix: glossary

- **app** — a project (one git repo / clone family).
- **worktree** — a git worktree of that app; the unit of isolation.
- **slug** — sanitized worktree identity; partitions ports/projects/volumes.
- **Tier A / shared infra** — the long-lived `<app>-infra` compose project.
- **Tier B / worktree stack** — a per-worktree `<app>-<slug>` compose project.
- **shared service** — a service listed in `docktree.toml shared = [...]`, run in
  Tier A, and reached by every worktree.
- **default_strategy** — per stateful service: `shared` (default) or `isolated`; `isolated`
  is realized by a generic volume snapshot (default) or the opt-in engine fast-path.
- **fork** — give a worktree its own copy of a stateful service.
- **canonical port** — the in-container port the app dials (`5432`); never changes.
