# Docktree

AI coding agents make it practical to work on several tasks at once, across one or many codebases. Git worktrees isolate the code, but not the development environment: concurrent stacks compete for ports and shared databases can be polluted by another worktree's migrations, schemas, or test data.

Docktree makes Docker Compose worktrees independently runnable with almost no additional setup. It gives each worktree its own Compose project and stable, collision-resistant ports. Infrastructure such as Postgres or Redis can be shared to keep local development lightweight, then forked when a task needs isolated data.

Your existing Compose file and `.env` remain usable without Docktree.

## Install

Docktree requires Docker with Compose v2.24 or later, Git, and macOS, Linux, or WSL2. To install the latest release, with Go 1.26 or newer:

```bash
go install github.com/rajpatil53/docktree@latest
```

Ensure `$GOBIN` (usually `~/go/bin`) is on your `PATH`, then verify it:

```bash
docktree --help
```

To build from a source checkout instead:

```bash
CGO_ENABLED=0 go build -o ~/.local/bin/docktree .
export PATH="$HOME/.local/bin:$PATH"
```

## Basic usage

In the application repository, make published ports configurable. Replace a hardcoded host port such as `"3000:3000"` with a token and default:

```yaml
services:
  web:
    ports:
      - "${WEB_PORT:-3000}:3000"
```

Then initialise and start the current worktree:

```bash
cd /path/to/your-app
docktree init
docktree up
docktree open
```

`init` creates `docktree.toml`, adds Docktree's generated directory to `.gitignore`, and detects port tokens in the Compose file. `up` creates the worktree-specific Compose projection and starts the stack. `open` prints a published service URL.

Create another worktree and start it the same way:

```bash
git worktree add ../your-app-feature -b feature/example
cd ../your-app-feature
docktree up
docktree open
```

The two stacks run independently, with different host ports. Stop only the current worktree's app stack when you are done:

```bash
docktree down
```

Useful everyday commands:

```bash
docktree ls                 # list all Docktree stacks
docktree ps                 # inspect this worktree's services
docktree logs web           # follow a service's logs
docktree exec web -- sh     # run a command in a service
docktree doctor             # check Docker, Compose, config, and ports
```

## Advanced usage

### Share infrastructure

Add services that should run once per repository to `docktree.toml`:

```toml
app = "shop"
compose = "compose.yaml"
shared = ["postgres", "redis"]
```

After that, `docktree up` starts the shared infrastructure when needed and each worktree's app services connect to it by their normal Compose names (for example, `postgres:5432`). `docktree down` leaves shared infrastructure running.

Manage it explicitly when needed:

```bash
docktree shared up
docktree shared status
docktree shared down
docktree shared nuke --confirm shop-infra  # also removes its volumes
```

Shared services must expose the container ports that app services use, through `ports:` or `expose:` in Compose. Docktree cannot share a service that depends on a non-shared service.

### Configure services and environment

Use `[services]` to declare a port token explicitly, fix a service's port, or keep a service out of the default startup set:

```toml
[services.web]
host_port_env = "WEB_PORT"

[services.admin]
host_port_env = "ADMIN_PORT"
fixed = true
autostart = false
```

`autostart = false` makes the service opt-in: run it with `docktree up admin`. `fixed = true` prevents Docktree from moving the resolved port if it is already in use, which is useful for external callbacks.

Set worktree-aware environment values with `{app}`, `{slug}`, and `{main_slug}`:

```toml
[env]
OPENSEARCH_INDEX_PREFIX = "{slug}_"
```

If Compose needs a secrets command, configure a wrapper that injects environment variables and executes its trailing command:

```toml
[secrets]
wrapper = "doppler run --"
```

Generated values—including port tokens—take precedence over values provided by the shell or secrets wrapper. A literal service-level `environment:` value in Compose still takes precedence.

### Run commands against another worktree

Supply a worktree slug or path before the service where supported:

```bash
docktree open feature_example web
docktree ps ../your-app-feature
docktree logs feature_example web
docktree exec feature_example web -- sh
docktree explain feature_example
```

`docktree explain` is the best way to inspect the derived slug, ports, networks, generated files, and Compose commands for a worktree.

### Isolate stateful data

Configure a stateful service before forking it. This Postgres example clones the shared database into a worktree-specific database:

```toml
[stateful.postgres]
engine = "postgres"
source_db = "shop_shared"
env = { POSTGRES_PRIMARY_DB = "shop_shared_{slug}" }
```

From a non-main worktree:

```bash
docktree fork postgres
```

Return to shared data by deleting the isolated copy. Run the command without a confirmation first if you need it to print the exact resource name:

```bash
docktree unfork postgres --confirm shop_shared_feature_example
```

Docktree refuses destructive state operations on the main worktree and shared stores. For non-Postgres stateful services, configure `snapshot_source`; Docktree creates an isolated Docker volume snapshot instead.

### Optional HTTPS proxy

Enable the machine-wide Caddy proxy to give HTTP services stable local hostnames instead of published ports:

```toml
[proxy]
enabled = true
dns_suffix = "localhost"

[services.web]
expose = "http"
proxy_port = 3000
public_url_env = "PUBLIC_URL"
```

`docktree up` starts the proxy automatically. The main worktree is available at `https://web.shop.localhost`; a feature worktree uses a hostname such as `https://web-feature-example.shop.localhost`. `public_url_env` writes that URL to the generated environment file.

For locally trusted HTTPS, export the proxy CA and run the one-time command it prints:

```bash
docktree trust
```

You can also use `docktree proxy up`, `docktree proxy status`, and `docktree proxy down` directly.

### Generated files, cleanup, and troubleshooting

Docktree owns only derived files under `.docktree/`:

| Path | Purpose |
| --- | --- |
| `.docktree/.env.worktree` | Resolved ports, environment, and network names. |
| `.docktree/compose.worktree.yml` | Per-worktree Compose projection. |
| `.docktree/compose.infra.yml` | Shared-infrastructure Compose projection. |

Port reservations live in `~/.config/docktree/registry.json`. All of these are regenerated as needed; do not edit them by hand.

Use `docktree prune` to preview stopped stacks whose worktrees no longer exist, then `docktree prune --execute` to remove them. Add `--include-forks` to preview or clean up orphaned forked volumes; actual fork deletion also requires `--confirm-forks <app>`.

Run `docktree doctor` first when something fails. Common causes are an old or stopped Docker Compose installation, a hardcoded host port, `container_name` in Compose, or explicitly named/external volumes that bypass per-worktree isolation.

For a disposable end-to-end Docker demo from this repository, run `./scripts/smoke.sh`; pass `--keep` to explore the running demo manually.

### Develop Docktree

```bash
go test -tags netgo ./...
go vet ./...
```

### Full command reference

| Command | Purpose |
| --- | --- |
| `docktree up [service...]` | Start the current worktree, optionally with selected services. |
| `docktree down` | Stop the current worktree's app stack. |
| `docktree ls [--json]` | List Docktree-managed stacks. |
| `docktree ps [worktree] [--service]` | Show a worktree's services. |
| `docktree open [worktree] [service]` | Print a service URL. |
| `docktree exec [worktree] <service> -- <cmd...>` | Run a command in a service. |
| `docktree logs [worktree] [service]` | Show service logs. |
| `docktree shared up/down/status/nuke` | Manage shared infrastructure. |
| `docktree fork <service>` | Create isolated state for a configured service. |
| `docktree unfork <service> --confirm <resource>` | Remove isolated state and return to shared data. |
| `docktree explain [worktree]` | Show derived identity, ports, files, and commands. |
| `docktree prune [--execute]` | Preview or remove orphaned Docktree resources. |
| `docktree init [--seed-env]` | Initialise Docktree in the current repository. |
| `docktree doctor` | Diagnose Docker, Compose, config, and port problems. |
| `docktree proxy up/down/status` | Manage the optional local HTTPS proxy. |
| `docktree trust` | Export the proxy CA and print the trust-store command. |
| `docktree version` | Print version and build metadata. |
