# Docktree

Docktree runs isolated Docker Compose development stacks for each git worktree
on one machine. It keeps app containers separate per worktree, assigns stable
host ports, and lets expensive infrastructure such as Postgres or Redis be
shared until a worktree needs its own copy.

The CLI command is `docktree`.

## What It Does

- Derives a stable identity from the current git worktree.
- Writes Docktree-owned artifacts under `.docktree/`.
- Reserves collision-resistant ports in `~/.config/docktree/registry.json`.
- Renders separate Compose projections for shared infrastructure and the current
  worktree.
- Starts shared infrastructure once per project and app stacks once per
  worktree.
- Supports opt-in stateful forks for worktrees that need isolated data.

Docktree does not rewrite your application Compose file or `.env`. Your normal
Compose setup stays usable without Docktree.

## Requirements

- Go 1.26 or newer to build from source.
- Docker with Docker Compose v2.24 or newer.
- Linux or macOS. Windows is supported through WSL2.
- A project managed by git.

## Install

With Go 1.26 or newer (installs the latest tagged release to `$GOBIN`, usually
`~/go/bin`):

```bash
go install github.com/rajpatil53/docktree@latest
```

Make sure `~/go/bin` (or your `GOBIN`) is on `PATH`.

### Build from source

From a clone of this repository:

```bash
CGO_ENABLED=0 go build -o ~/.local/bin/docktree .
```

Make sure the install directory is on `PATH`:

```bash
export PATH="$HOME/.local/bin:$PATH"
```

Verify the binary:

```bash
docktree --help
```

## Quick Start

Run these commands inside an application repository that already has a Docker
Compose file.

```bash
cd /path/to/your-app
docktree init
docktree doctor
docktree up
docktree ls
docktree open
```

When finished with the current worktree stack:

```bash
docktree down
```

`docktree down` stops only the current worktree's app stack. Shared
infrastructure is left running.

## Disposable Two-Worktree Demo

To exercise the current source against real Docker without modifying an
existing application, run the smoke demo from the repository root. It requires
Bash, curl, Git, Go, and a running Docker daemon with Compose:

```bash
./scripts/smoke.sh
```

It builds a temporary binary and application repository, starts primary and
feature worktrees with different web ports, verifies that both reach the same
shared Redis through their own uplinks, and then removes the containers,
networks, volume, registry, and temporary files.

Keep the fully initialized demo running to explore it manually:

```bash
./scripts/smoke.sh --keep
```

The command prints both worktree paths and URLs. The retained demo contains a
`docktree` wrapper that preserves its isolated registry and Docker context, a
`README.txt` with exploration commands, and a `destroy-demo` cleanup helper.
See [the smoke fixture](examples/smoke/README.md) for details. The first run may
pull the small demo and uplink images if they are not already cached.

## Compose Convention

Docktree works best when host ports are configurable through environment
variables. Instead of hardcoding host ports:

```yaml
services:
  api:
    ports:
      - "3000:3000"
```

Use a token with a default:

```yaml
services:
  api:
    ports:
      - "${API_PORT:-3000}:3000"
```

Docktree writes `API_PORT` into `.docktree/.env.worktree`, so each worktree can bind a
different host port while the container still listens on `3000`.

Shared services can keep their canonical service names. For example, app
containers can still connect to `postgres:5432`: each worktree projection
includes a generated `dt-uplink-<slug>` ambassador that answers to the shared
services' names on the worktree's own network and forwards each port to the
shared infra stack over the Docktree-managed `<app>_shared` network. App
services never join the shared network themselves, so a name like `redis`
resolves exactly once per worktree — to that worktree's redis — no matter how
many worktrees run at once.

One-off containers (test recipes, ad-hoc jobs) should attach the worktree's
network and dial canonical names there:

```sh
docker run --rm --network "$DOCKTREE_WORKTREE_NETWORK" my-test-image
```

`DOCKTREE_WORKTREE_NETWORK` is written to `.docktree/.env.worktree` alongside
`DOCKTREE_SHARED_NETWORK` (which remains valid for reaching shared infra
directly).

## Project Configuration

`docktree init` creates `docktree.toml` if it does not exist. The smallest valid
file is:

```toml
app = "shop"
```

Use `shared` to move services into the long-lived shared infra stack:

```toml
app = "shop"
compose = "compose.yaml"
shared = ["postgres", "redis"]
```

Configure services with port tokens or manual start behavior:

```toml
[services.api]
host_port_env = "API_PORT"
fixed = false

[services.frontend]
host_port_env = "WEB_PORT"

[services.e2e]
autostart = false
```

`autostart = false` places the service behind Docktree's manual profile in the
generated projection.

Configure templated environment values:

```toml
[env]
OPENSEARCH_INDEX_PREFIX = "{slug}_"
```

Supported template variables are:

- `{app}`: the Docktree app name.
- `{slug}`: the current worktree slug.
- `{main_slug}`: the main worktree slug.

Generated env values (`[env]`, `public_url_env`, ports, stateful overrides) are
re-applied over the process environment of every Compose command Docktree runs
against the generated projections, so they take precedence over variables
exported in your shell or injected by the secrets wrapper. Keys a service pins
as literals in its own `environment:` block still win.

If Compose commands need a secrets wrapper, configure it once:

```toml
[secrets]
wrapper = "doppler run --"
```

Docktree prepends the wrapper to Docker Compose commands and checks it before
non-interactive runs. The wrapper must inject env and exec its trailing
command, like `doppler run --` or `op run --`; shell-form wrappers such as
`sh -c` are not supported. Because generated env values are re-applied after
the wrapper's injection, a wrapper secret with the same name as a generated
value does not override it.

## Generated Files

Docktree owns these files inside each worktree:

| Path | Purpose |
|---|---|
| `.docktree/.env.worktree` | Generated env file with `COMPOSE_PROJECT_NAME`, shared and worktree network names, resolved ports, templated env, and isolated stateful env overrides. |
| `.docktree/compose.worktree.yml` | Generated Compose projection for per-worktree services. |
| `.docktree/compose.infra.yml` | Generated Compose projection for services listed in `shared`. |

Docktree also stores machine-wide port reservations in:

```text
~/.config/docktree/registry.json
```

These files are derived artifacts. `docktree up`, `docktree shared up`, and
related commands regenerate them as needed.

## Daily Workflow

Start the current worktree:

```bash
docktree up
```

`up` prints Docktree phase messages, skips shared infra reconciliation when
shared services are already ready, and runs Compose in plain progress mode so
image build output remains visible in non-interactive terminals.

Start selected services only:

```bash
docktree up api worker
```

Show known Docktree stacks:

```bash
docktree ls
docktree ls --json
```

Print the current worktree's service URL:

```bash
docktree open
docktree open api
```

Target another worktree by slug or worktree path:

```bash
docktree open feature_checkout api
docktree ps ../feature-checkout
```

Inspect running services:

```bash
docktree ps
docktree ps --service
```

Tail logs:

```bash
docktree logs
docktree logs api
```

Run a command inside a service:

```bash
docktree exec api -- sh
docktree exec api -- bundle exec rails db:migrate
```

Explain what Docktree derived:

```bash
docktree explain
docktree explain feature_checkout
```

Run diagnostics:

```bash
docktree doctor
```

Stop only the current worktree stack:

```bash
docktree down
```

## Shared Infrastructure

Shared services are listed in `docktree.toml`:

```toml
shared = ["postgres", "redis"]
```

Start or reconcile shared infra:

```bash
docktree shared up
```

Show shared infra status:

```bash
docktree shared status
```

Stop shared infra without deleting volumes:

```bash
docktree shared down
```

Remove shared infra and volumes through the guarded path:

```bash
docktree shared nuke --confirm <app>-infra
```

Replace `<app>` with the configured app name.

## Stateful Forks

Use a fork when one worktree needs isolated data for a configured stateful
service.

Example Postgres configuration:

```toml
[stateful.postgres]
default_strategy = "shared"
engine = "postgres"
snapshot_source = "shop_pgdata"
source_db = "shop_shared"
superuser = "postgres"   # role to connect as; default "postgres" (the official image's POSTGRES_USER)
env = { POSTGRES_PRIMARY_DB = "shop_shared_{slug}" }
```

With the Postgres fast path, Docktree clones `source_db` into
`<source_db>_<slug>` for the current worktree. It connects as `superuser`
(default `postgres`); set this if your container's `POSTGRES_USER` differs. Stateful `env` entries are written
only when the service is isolated, so `docktree fork postgres` writes
`POSTGRES_PRIMARY_DB=shop_shared_<slug>` and `docktree unfork postgres` removes
that override.

Create an isolated copy for the current worktree:

```bash
docktree fork postgres
```

Drop the isolated copy and return to shared data:

```bash
docktree unfork postgres --confirm <resource>
```

The confirmation resource is intentionally explicit. For Postgres logical forks
it is the generated database name. For generic volume forks it is the generated
volume name. Run `docktree unfork postgres` without `--confirm` first if you need
Docktree to print the expected confirmation value.

Docktree refuses destructive stateful operations on the main worktree and on
shared stores.

## Cleanup

By default, pruning is a dry run:

```bash
docktree prune
```

Actually remove stopped orphaned Docktree stacks:

```bash
docktree prune --execute
```

Also consider forked volumes:

```bash
docktree prune --include-forks
docktree prune --execute --include-forks --confirm-forks <app>
```

Running orphaned stacks are reported but not removed automatically.

## Troubleshooting

Run:

```bash
docktree doctor
```

Common issues:

- Docker is not running.
- Docker Compose is older than v2.24.
- A Compose service uses a hardcoded host port instead of `${VAR:-default}`.
- A shared service depends on a non-shared service.
- A service declares `container_name`, which can collide across worktrees.
- A top-level volume uses an explicit `name:` or `external: true`, which can
  bypass per-worktree isolation.
- Published ports have drifted from the values in `.docktree/.env.worktree`.
- The configured secrets wrapper is unavailable.

For more detail on a specific worktree, run:

```bash
docktree explain
```

## Command Reference

| Command | Purpose |
|---|---|
| `docktree up [service...]` | Regenerate artifacts, ensure shared infra only when needed, and start the current worktree stack with readable phase and build progress output. |
| `docktree down` | Stop the current worktree stack. |
| `docktree ls [--json]` | List Docktree-managed stacks. |
| `docktree ps [worktree] [--service]` | Run Compose `ps` for a worktree stack. |
| `docktree open [worktree] [service]` | Print a service URL. |
| `docktree exec [worktree] <service> -- <cmd...>` | Run a command inside a service. |
| `docktree logs [worktree] [service]` | Run Compose `logs` for a worktree stack. |
| `docktree shared up/down/status/nuke` | Manage shared infra; `nuke` requires `--confirm <app>-infra`. |
| `docktree fork <service>` | Give the current worktree isolated data for a stateful service. |
| `docktree unfork <service> --confirm <resource>` | Delete the isolated data copy and return to shared data. |
| `docktree explain [worktree]` | Print identity, ports, generated paths, data sources, and Compose argv. |
| `docktree prune [--execute] [--include-forks --confirm-forks <app>]` | Remove stopped orphaned Docktree resources. |
| `docktree init [--seed-env]` | Create `docktree.toml`, `.docktree/`, `.docktree/.env.worktree`, gitignore entries, and the shared network. With `--seed-env`, copy `.env.example` to `.env` if `.env` does not exist. |
| `docktree doctor` | Diagnose configuration, Docker, Compose, ports, infra, and secrets. |
| `docktree proxy up/down/status` | Manage the machine-global Caddy reverse proxy that routes worktree HTTP services by hostname (opt-in via `[proxy]`). |
| `docktree trust` | Export the proxy's Caddy local-CA root and print the one-time command to install it into the host trust store. |
| `docktree version` / `docktree --version` | Print the Docktree version and build metadata. |

## Notes for Former `wtc` Users

- `wtc` is now `docktree`.
- `wtc.toml` is now `docktree.toml`.
- `.wtc/` is now `.docktree/`.
- `~/.config/wtc/apps.json` is now `~/.config/docktree/registry.json`.
- `wtc db isolate/share` is now `docktree fork/unfork`.
- `wtc infra` is now `docktree shared`.

## Development

Run the standard verification suite:

```bash
go test -tags netgo ./...
go vet ./...
go test -tags netgo -coverprofile=/tmp/docktree.cover ./...
go tool cover -func=/tmp/docktree.cover
```

Build a local binary:

```bash
CGO_ENABLED=0 go build -o /Users/raj-flexiple/.local/bin/docktree .
```
