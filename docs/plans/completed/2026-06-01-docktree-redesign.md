# Docktree Redesign Implementation Plan

## Overview

Implement the Docktree redesign described in DESIGN.md as an in-place rewrite of the current wtc proof of concept. The new CLI will be docktree, use .docktree/ and ~/.config/docktree/, derive runtime state from Docker, render Docktree-owned compose projections, support shared infra, per-worktree stacks, diagnostics, and opt-in stateful service forks.

## Context

- Files involved: main.go, go.mod, README.md, DESIGN.md
- Current command files: cmd/init.go, cmd/up.go, cmd/doctor.go, cmd/db.go, cmd/remove.go, cmd/resolve.go, cmd/mainslug.go
- Current packages to replace or evolve: internal/manifest, internal/identity, internal/ports, internal/registry, internal/envfile, internal/dbmode, internal/infra, internal/override, internal/state, internal/dockerx
- New packages expected: internal/config, internal/compose, internal/paths, internal/runner, internal/dockerstate, internal/stateful
- Related patterns: pure decision helpers with unit tests, atomic temp+rename writes, flock-guarded registry access, injected command runners for shell/Docker behavior, Go table tests
- Dependencies: keep github.com/BurntSushi/toml; add gopkg.in/yaml.v3 for generated compose projection output; Docker Compose v2 remains an external runtime dependency

## Development Approach

- **Testing approach**: TDD
- Complete each task fully before moving to the next
- Preserve the current bias toward pure, side-effect-free helpers and fakeable shell/Docker boundaries
- No backward compatibility with wtc names, .wtc files, wtc.toml, or docker-compose.override.yml is required
- **CRITICAL: every task MUST include new or updated tests**
- **CRITICAL: all tests must pass before starting the next task**

## Implementation Steps

### Task 1: Rename product surface and add root dispatcher

**Files:**
- Modify: `go.mod`
- Modify: `main.go`
- Create: `cmd/root.go`
- Create: `cmd/root_test.go`
- Create: `internal/paths/paths.go`
- Create: `internal/paths/paths_test.go`
- Modify: `README.md`

- [x] Write tests in `cmd/root_test.go` that assert docktree dispatches the supported command names from DESIGN.md and rejects unknown commands with docktree usage text
- [x] Write tests in `internal/paths/paths_test.go` for `.docktree/.env.worktree`, `.docktree/compose.worktree.yml`, `.docktree/compose.infra.yml`, `docktree.toml`, and `~/.config/docktree/registry.json` path derivation
- [x] Change module/import naming from wtc to docktree and update imports
- [x] Make `main.go` delegate to `cmd.Run(args, stdin, stdout, stderr)` so command routing is testable without `os.Exit`
- [x] Add command stubs for `up`, `down`, `ls`, `ps`, `open`, `exec`, `logs`, `shared`, `fork`, `unfork`, `explain`, `prune`, `init`, and `doctor`
- [x] Replace user-facing `wtc`/`.wtc`/`wtc.toml` strings in the root surface with `docktree`/`.docktree`/`docktree.toml`
- [x] Run `go test -tags netgo ./...` and fix failures before Task 2

### Task 2: Implement Docktree config and identity model

**Files:**
- Replace: `internal/manifest/manifest.go`
- Modify: `internal/manifest/manifest_test.go`
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`
- Modify: `internal/identity/slug.go`
- Modify: `internal/identity/names.go`
- Modify: `internal/identity/slug_test.go`
- Modify: `internal/identity/names_test.go`
- Modify: `cmd/resolve.go`
- Modify: `cmd/mainslug.go`
- Modify: `cmd/mainslug_test.go`

- [x] Write config tests for minimal `docktree.toml`, `shared = ["postgres"]`, `[services.<name>]` `host_port_env`/`fixed`/`autostart`, `[stateful.<name>]` `default_strategy`/`engine`/`snapshot_source`/`source_db`/isolated env, `[env]` templating, `[secrets]` wrapper, and unknown-key warnings
- [x] Write identity tests for DESIGN.md slug rules: lowercase `a-z0-9_`, trim, empty to `main`, long-name hash suffix, slug validation, app fallback, main slug from git common dir, and project names `<app>-<slug>` and `<app>-infra`
- [x] Implement Docktree config structs and defaults; keep app explicit-or-repo-name, compose autodiscovery defaults, shared services, stateful services, env templates, and secrets wrapper
- [x] Replace legacy role-based service config with service-name-based config that is stack-agnostic
- [x] Replace cksum compatibility assumptions in identity with the redesign naming rules and FNV-related interfaces where identity needs a hash suffix
- [x] Update resolve logic to derive app, slug, main slug, project names, shared network `<app>_shared`, and config without reading or writing `.wtc/state`
- [x] Run `go test -tags netgo ./...` and fix failures before Task 3

### Task 3: Build compose discovery and projection rendering

**Files:**
- Create: `internal/compose/model.go`
- Create: `internal/compose/discover.go`
- Create: `internal/compose/render.go`
- Create: `internal/compose/validate.go`
- Create: `internal/compose/model_test.go`
- Create: `internal/compose/render_test.go`
- Create: `internal/compose/testdata/config-basic.json`
- Create: `internal/compose/testdata/config-shared-depends-on.json`
- Delete or retire: `internal/override/override.go`
- Delete or retire: `internal/override/override_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

- [x] Write projection tests from `docker compose config --format json` fixtures that verify infra projection contains only shared services and required top-level resources
- [x] Write worktree projection tests that verify shared services are removed, `depends_on` edges to shared services are subtracted, `env_file` appends `.docktree/.env.worktree` last, shared network is attached, labels are emitted, and `container_name` is stripped or parameterized
- [x] Write validation tests for shared services depending on non-shared services, hardcoded host port warnings, explicitly named/external volume warnings, and `autostart=false` manual profile assignment
- [x] Add `gopkg.in/yaml.v3` and implement JSON-to-typed compose model parsing with only the fields Docktree needs
- [x] Implement compose file-set discovery that honors `COMPOSE_FILE` and default `docker-compose.override.yml` behavior when invoking `docker compose config`
- [x] Implement renderers for `.docktree/compose.infra.yml` and `.docktree/compose.worktree.yml` from the resolved compose model
- [x] Run `go test -tags netgo ./...` and fix failures before Task 4

### Task 4: Implement registry, ports, and .docktree env artifacts

**Files:**
- Modify: `internal/registry/registry.go`
- Modify: `internal/registry/registry_test.go`
- Modify: `internal/ports/ports.go`
- Modify: `internal/ports/ports_test.go`
- Modify: `internal/envfile/envfile.go`
- Modify: `internal/envfile/envfile_test.go`
- Create: `internal/envfile/read_test.go`
- Create: `internal/gitignore/gitignore.go`
- Create: `internal/gitignore/gitignore_test.go`
- Modify: `cmd/resolve.go`

- [x] Write registry tests for `~/.config/docktree/registry.json` shape, per-app band reservations, stable shared port reservations, missing registry self-creation, corrupt registry fail-closed behavior, atomic writes, and lock-file use
- [x] Write port tests for FNV-1a offset, `0.0.0.0` probing, fixed-service collision errors, non-fixed linear bumping, exhausted bands, and bind-failure retry inputs
- [x] Write env artifact tests for ordered `.docktree/.env.worktree` output containing `COMPOSE_PROJECT_NAME`, `DOCKTREE_SHARED_NETWORK`, resolved `*_PORT` values, templated `[env]` values, and stateful URL values without touching the app `.env`
- [x] Implement the soft registry under `~/.config/docktree/registry.json` with per-app flocking and atomic save
- [x] Implement per-worktree and shared-service port resolution from compose-discovered port tokens and config fixed flags
- [x] Update env writing to write only `.docktree/.env.worktree` and add an idempotent helper that ensures `.docktree/` is gitignored
- [x] Run `go test -tags netgo ./...` and fix failures before Task 5

### Task 5: Implement core stack orchestration and shared infra lifecycle

**Files:**
- Create: `internal/runner/runner.go`
- Create: `internal/runner/runner_test.go`
- Modify: `cmd/up.go`
- Create: `cmd/down.go`
- Create: `cmd/shared.go`
- Modify: `cmd/init.go`
- Create: `cmd/up_test.go`
- Create: `cmd/down_test.go`
- Create: `cmd/shared_test.go`
- Modify: `internal/infra/infra.go`
- Modify: `internal/infra/infra_test.go`

- [x] Write runner tests for argv construction, working directory, process env, secrets wrapper prefixing, and non-interactive wrapper preflight failure
- [x] Write up tests with a fake runner that assert the order: resolve, write `.docktree/.env.worktree`, render projections, create shared network, start infra if needed, wait for shared readiness, run `docker compose` with `--env-file .docktree/.env.worktree` and the worktree projection
- [x] Write down tests that assert only the worktree project is stopped and shared infra is not touched
- [x] Write shared command tests for `up`, `down`, `status`, and `nuke` argv; `nuke` must require the guarded explicit confirmation path
- [x] Implement the per-app critical section around port resolution, infra bring-up, and compose up
- [x] Implement shared infra stable host-port mapping and readiness wait hooks with fakeable probes
- [x] Implement init to scaffold `docktree.toml`, create `.docktree/`, create or verify the shared network, and optionally seed `.env` from `.env.example` only when `--seed-env` is passed
- [x] Run `go test -tags netgo ./...` and fix failures before Task 6

### Task 6: Implement diagnostics, status, and inspection commands

**Files:**
- Create: `internal/dockerstate/dockerstate.go`
- Create: `internal/dockerstate/dockerstate_test.go`
- Modify: `cmd/doctor.go`
- Create: `cmd/explain.go`
- Create: `cmd/ls.go`
- Create: `cmd/open.go`
- Create: `cmd/ps.go`
- Create: `cmd/logs.go`
- Create: `cmd/exec.go`
- Create: `cmd/doctor_test.go`
- Create: `cmd/explain_test.go`
- Create: `cmd/ls_test.go`
- Modify/Create: `cmd/ls_test.go` (includes open URL selection tests)
- Create: `cmd/ps_logs_exec_test.go`

- [x] Write dockerstate tests that parse fake `docker compose ls`, `docker ps` labels, compose ps JSON, and published port data into app, slug, project, status, service, port, URL, and fork-mode records
- [x] Write doctor tests for daemon unavailable, infra unhealthy, missing port tokens, shared-service config mismatches, `container_name` hazards, named/external volume hazards, port drift, and secrets preflight failures
- [x] Write explain tests that assert output includes app, slug, main slug, project names, shared network, resolved ports, actual published ports, data sources, compose argv, and generated projection paths
- [x] Write `ls --json` and open tests that assert stable machine-readable output and URL selection by worktree/service
- [x] Implement `ps`, `logs`, and `exec` as thin `docker compose -p <app>-<slug>` wrappers through the runner
- [x] Implement all diagnostics from DESIGN.md using Docker-derived runtime state rather than stored `state.json`
- [x] Run `go test -tags netgo ./...` and fix failures before Task 7

### Task 7: Implement stateful fork, unfork, and prune

**Files:**
- Modify: `internal/dbmode/dbmode.go`
- Modify: `internal/dbmode/dbmode_test.go`
- Create: `internal/stateful/stateful.go`
- Create: `internal/stateful/stateful_test.go`
- Create: `cmd/fork.go`
- Create: `cmd/unfork.go`
- Create: `cmd/prune.go`
- Create: `cmd/fork_test.go`
- Create: `cmd/unfork_test.go`
- Create: `cmd/prune_test.go`
- Delete or retire: `internal/state/state.go`
- Delete or retire: `internal/state/state_test.go`
- Modify: `cmd/db.go`

- [x] Write pure safety-guard tests that destructive operations are allowed only for an isolated, non-main worktree's own volume or logical DB, unknown strategy is unsafe, and shared/main stores are never dropped except shared nuke
- [x] Write generic volume snapshot tests for source/destination volume names, labels, quiesce requirement reporting, `docker run cp` argv, and fork-derived Docker label detection
- [x] Write Postgres fast-path tests for `CREATE DATABASE TEMPLATE`, `pg_dump` fallback, `source_db` naming, isolated env regeneration, and unsupported engine fallback to generic snapshot
- [x] Write fork/unfork command tests that assert `.docktree/.env.worktree` and projections are regenerated after changing the derived data source
- [x] Write prune tests for dry-run default, orphaned stopped stack removal, orphaned running stack reporting, forked volume confirmation requirement, and guard enforcement
- [x] Replace the old db isolate/share command surface with `docktree fork`/`unfork` while keeping reusable pure DB safety logic
- [x] Run `go test -tags netgo ./...` and fix failures before Task 8

### Task 8: Final verification and documentation

**Files:**
- Modify: `README.md`
- Modify: `DESIGN.md` if implementation decisions pin Compose version floor or clarify deviations
- Modify: `cmd/root_test.go`
- Modify: `internal/compose/render_test.go`
- Modify: `internal/dockerstate/dockerstate_test.go`

- [x] Update root command/help tests to snapshot the final command table and ensure README command names stay aligned with the CLI surface
- [x] Add final projection/status regression tests for a representative compose stack with api, frontend, postgres, redis, one `autostart=false` service, one hardcoded port warning, and one shared `depends_on` edge
- [x] Update README.md to describe Docktree install, `docktree.toml`, `.docktree/.env.worktree`, `up`/`down`/`shared`/`fork`/`unfork`/`doctor`/`explain`/`ls`/`open`, and development commands
- [x] Update DESIGN.md only for concrete implementation decisions made during the rewrite, such as the pinned Compose v2 floor
- [x] Run `go test -tags netgo ./...`
- [x] Run `go vet ./...`
- [x] Run `go test -tags netgo -coverprofile=/tmp/docktree.cover ./...`
- [x] Run `go tool cover -func=/tmp/docktree.cover | awk '/total:/ { sub(/%/,"",$3); if ($3+0 < 80) exit 1 }'`
- [x] Run `git status --short` and confirm only intentional redesign files changed
