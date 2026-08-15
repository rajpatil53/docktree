#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
REPO_ROOT=$(CDPATH='' cd -- "$SCRIPT_DIR/.." && pwd -P)
FIXTURE_DIR="$REPO_ROOT/examples/smoke"

KEEP_DEMO=0
DEMO_ROOT=""
APP_NAME=""
MAIN_DIR=""
FEATURE_DIR=""
MAIN_URL=""
FEATURE_URL=""

usage() {
  cat <<'EOF'
Usage:
  ./scripts/smoke.sh
  ./scripts/smoke.sh --keep
  ./scripts/smoke.sh --cleanup DEMO_ROOT

With no arguments, build the current Docktree source, exercise two real Git
worktrees against Docker, assert isolation and shared Redis behavior, then
remove the demo. --keep leaves it running for manual exploration. A kept demo
contains its own destroy-demo helper; --cleanup is the equivalent explicit
cleanup command.
EOF
}

log() {
  printf '==> %s\n' "$*"
}

warn() {
  printf 'warning: %s\n' "$*" >&2
}

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

absolute_existing_dir() {
  [ -d "$1" ] || return 1
  (CDPATH='' cd -- "$1" && pwd -P)
}

validate_demo_root() {
  local candidate=$1
  local root
  local marker

  root=$(absolute_existing_dir "$candidate") || die "demo root does not exist: $candidate"
  [ "$root" != "/" ] || die "refusing to use / as a demo root"
  case "$(basename -- "$root")" in
    docktree-smoke.*) ;;
    *) die "refusing unrecognized demo directory: $root" ;;
  esac
  [ -f "$root/.docktree-smoke-root" ] || die "missing smoke ownership marker in $root"
  marker=$(sed -n '1p' "$root/.docktree-smoke-root")
  [ "$marker" = "docktree-smoke-v1" ] || die "invalid smoke ownership marker in $root"
  [ -x "$root/docktree" ] || die "missing Docktree demo wrapper in $root"
  [ -x "$root/docker" ] || die "missing Docker demo wrapper in $root"
  [ -s "$root/.app-name" ] || die "missing demo app identity in $root"
  printf '%s\n' "$root"
}

docktree_in() {
  local root=$1
  local directory=$2
  shift 2
  (cd "$directory" && "$root/docktree" "$@")
}

remove_exact_network_if_present() {
  local docker_wrapper=$1
  local network=$2

  if "$docker_wrapper" network inspect "$network" >/dev/null 2>&1; then
    "$docker_wrapper" network rm "$network" >/dev/null 2>&1 || true
  fi
}

cleanup_demo() {
  local requested_root=$1
  local root
  local app
  local docker_wrapper
  local marker_app
  local containers
  local networks
  local volumes
  local resource
  local cleanup_failed=0

  if ! root=$(validate_demo_root "$requested_root"); then
    return 1
  fi
  if ! app=$(sed -n '1p' "$root/.app-name"); then
    warn "could not read the demo app identity from $root"
    return 1
  fi
  if [ -z "$app" ]; then
    warn "the demo app identity in $root is empty"
    return 1
  fi
  if [[ ! $app =~ ^dtsmoke_[0-9]+_[0-9]+$ ]]; then
    warn "refusing unsafe demo app identity '$app'; retaining $root"
    return 1
  fi
  if ! marker_app=$(sed -n '2p' "$root/.docktree-smoke-root"); then
    warn "could not read the demo identity marker from $root"
    return 1
  fi
  if [ "$marker_app" != "$app" ]; then
    warn "the demo app identity does not match its ownership marker; retaining $root"
    return 1
  fi
  docker_wrapper="$root/docker"

  log "Stopping demo stacks in $root"
  if ! "$docker_wrapper" info >/dev/null 2>&1; then
    warn "Docker daemon is unavailable; retaining $root for a later cleanup"
    return 1
  fi

  if [ -f "$root/feature/.docktree/compose.worktree.yml" ]; then
    docktree_in "$root" "$root/feature" down || warn "feature stack did not stop cleanly"
  fi
  if [ -f "$root/main/.docktree/compose.worktree.yml" ]; then
    docktree_in "$root" "$root/main" down || warn "main stack did not stop cleanly"
  fi
  if "$docker_wrapper" network inspect "${app}_shared" >/dev/null 2>&1; then
    docktree_in "$root" "$root/main" shared nuke --confirm "${app}-infra" || warn "shared stack did not nuke cleanly"
  fi

  # A signal or a half-completed Compose operation can leave exact demo
  # resources behind. The app name is unique to this sentinel-owned temp root,
  # so these fallbacks cannot match another Docktree project.
  containers=$("$docker_wrapper" ps -aq --filter "label=com.docktree.app=$app" 2>/dev/null || true)
  for resource in $containers; do
    "$docker_wrapper" rm -f "$resource" >/dev/null 2>&1 || true
  done

  volumes=$("$docker_wrapper" volume ls -q --filter "label=com.docker.compose.project=${app}-infra" 2>/dev/null || true)
  for resource in $volumes; do
    "$docker_wrapper" volume rm -f "$resource" >/dev/null 2>&1 || true
  done

  remove_exact_network_if_present "$docker_wrapper" "${app}-feature_default"
  remove_exact_network_if_present "$docker_wrapper" "${app}_default"
  remove_exact_network_if_present "$docker_wrapper" "${app}_shared"

  if ! "$docker_wrapper" info >/dev/null 2>&1; then
    warn "Docker became unavailable during cleanup; retaining $root"
    return 1
  fi
  if ! containers=$("$docker_wrapper" ps -aq --filter "label=com.docktree.app=$app" 2>/dev/null); then
    warn "could not verify demo containers; retaining $root"
    return 1
  fi
  if ! volumes=$("$docker_wrapper" volume ls -q --filter "label=com.docker.compose.project=${app}-infra" 2>/dev/null); then
    warn "could not verify demo volumes; retaining $root"
    return 1
  fi
  if ! networks=$("$docker_wrapper" network ls --format '{{.Name}}' 2>/dev/null); then
    warn "could not verify demo networks; retaining $root"
    return 1
  fi
  if [ -n "$containers" ] || [ -n "$volumes" ]; then
    cleanup_failed=1
  fi
  for resource in "${app}-feature_default" "${app}_default" "${app}_shared"; do
    if printf '%s\n' "$networks" | awk -v expected="$resource" '$0 == expected { found = 1 } END { exit !found }'; then
      cleanup_failed=1
    fi
  done

  if [ "$cleanup_failed" -ne 0 ]; then
    warn "some demo Docker resources remain; retaining $root for inspection"
    return 1
  fi

  case "$PWD/" in
    "$root"/*)
      if ! cd "$(dirname -- "$root")"; then
        warn "could not leave the demo root before removing it"
        return 1
      fi
      ;;
  esac
  if ! rm -rf "$root"; then
    warn "could not remove demo root $root"
    return 1
  fi
  if [ -e "$root" ]; then
    warn "demo root still exists after removal: $root"
    return 1
  fi
  log "Removed demo root $root"
}

assert_eq() {
  local got=$1
  local want=$2
  local label=$3
  [ "$got" = "$want" ] || die "$label: got '$got', want '$want'"
}

assert_ne() {
  local left=$1
  local right=$2
  local label=$3
  [ "$left" != "$right" ] || die "$label: both values were '$left'"
}

assert_nonempty() {
  local value=$1
  local label=$2
  [ -n "$value" ] || die "$label was empty"
}

assert_contains() {
  local value=$1
  local expected=$2
  local label=$3
  case "$value" in
    *"$expected"*) ;;
    *) die "$label did not contain '$expected'" ;;
  esac
}

assert_line() {
  local value=$1
  local expected=$2
  local label=$3
  if ! printf '%s\n' "$value" | awk -v expected="$expected" '$0 == expected { found = 1 } END { exit !found }'; then
    die "$label did not contain line '$expected'"
  fi
}

env_value() {
  local file=$1
  local key=$2
  awk -F= -v key="$key" '$1 == key { sub(/^[^=]*=/, ""); print; exit }' "$file"
}

wait_for_http() {
  local url=$1
  local label=$2
  local attempt=1

  while [ "$attempt" -le 30 ]; do
    if curl --noproxy '*' --fail --silent --show-error --max-time 2 "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
    attempt=$((attempt + 1))
  done
  die "$label did not become reachable at $url"
}

wait_for_redis_from() {
  local directory=$1
  local label=$2
  local attempt=1
  local output=""

  while [ "$attempt" -le 30 ]; do
    if output=$(docktree_in "$DEMO_ROOT" "$directory" exec -T client redis-cli -h redis PING 2>/dev/null); then
      output=${output//$'\r'/}
      if [ "$output" = "PONG" ]; then
        return 0
      fi
    fi
    sleep 1
    attempt=$((attempt + 1))
  done
  docktree_in "$DEMO_ROOT" "$directory" exec -T client redis-cli -h redis PING || true
  die "$label could not reach shared Redis through its uplink"
}

infra_container_id() {
  "$DEMO_ROOT/docker" ps -q \
    --filter "label=com.docktree.app=$APP_NAME" \
    --filter "label=com.docktree.tier=infra" \
    --filter "label=com.docktree.service=redis"
}

container_count_on_network() {
  local network=$1
  shift
  "$DEMO_ROOT/docker" ps -q --filter "network=$network" "$@" |
    awk 'NF { count++ } END { print count + 0 }'
}

git_demo() {
  "$DEMO_ROOT/bin/run-env" git -c core.hooksPath=/dev/null -c commit.gpgsign=false "$@"
}

write_demo_helpers() {
  cp "$SCRIPT_DIR/smoke.sh" "$DEMO_ROOT/bin/smoke-cleanup"

  cat >"$DEMO_ROOT/bin/run-env" <<'EOF'
#!/bin/sh
set -eu

BIN_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd -P)
DEMO_ROOT=$(CDPATH='' cd -- "$BIN_DIR/.." && pwd -P)
DEMO_HOME="$DEMO_ROOT/home"
DOCKER_CONFIG=$(sed -n '1p' "$DEMO_ROOT/.docker-config")

export HOME="$DEMO_HOME"
export XDG_CONFIG_HOME="$DEMO_HOME/.config"
export DOCKER_CONFIG

if [ -f "$DEMO_ROOT/.docker-context" ]; then
  IFS= read -r DOCKER_CONTEXT <"$DEMO_ROOT/.docker-context" || true
  export DOCKER_CONTEXT
else
  unset DOCKER_CONTEXT
fi
if [ -f "$DEMO_ROOT/.docker-host" ]; then
  IFS= read -r DOCKER_HOST <"$DEMO_ROOT/.docker-host" || true
  export DOCKER_HOST
else
  unset DOCKER_HOST
fi
if [ -f "$DEMO_ROOT/.docker-tls-verify" ]; then
  IFS= read -r DOCKER_TLS_VERIFY <"$DEMO_ROOT/.docker-tls-verify" || true
  export DOCKER_TLS_VERIFY
else
  unset DOCKER_TLS_VERIFY
fi
if [ -f "$DEMO_ROOT/.docker-cert-path" ]; then
  IFS= read -r DOCKER_CERT_PATH <"$DEMO_ROOT/.docker-cert-path" || true
  export DOCKER_CERT_PATH
else
  unset DOCKER_CERT_PATH
fi

exec "$@"
EOF

  cat >"$DEMO_ROOT/docktree" <<'EOF'
#!/bin/sh
set -eu
DEMO_ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd -P)
exec "$DEMO_ROOT/bin/run-env" "$DEMO_ROOT/bin/docktree-bin" "$@"
EOF

  cat >"$DEMO_ROOT/docker" <<'EOF'
#!/bin/sh
set -eu
DEMO_ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd -P)
exec "$DEMO_ROOT/bin/run-env" docker "$@"
EOF

  cat >"$DEMO_ROOT/destroy-demo" <<'EOF'
#!/bin/sh
set -eu
DEMO_ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd -P)
cd "$(dirname -- "$DEMO_ROOT")"
exec "$DEMO_ROOT/bin/smoke-cleanup" --cleanup "$DEMO_ROOT"
EOF

  chmod +x "$DEMO_ROOT/bin/run-env" "$DEMO_ROOT/bin/smoke-cleanup" "$DEMO_ROOT/docktree" "$DEMO_ROOT/docker" "$DEMO_ROOT/destroy-demo"
}

write_manual_readme() {
  cat >"$DEMO_ROOT/README.txt" <<EOF
Docktree two-worktree demo
==========================

Always use the generated wrapper at:
  $DEMO_ROOT/docktree

Main worktree:
  cd "$MAIN_DIR"
  ../docktree ls
  ../docktree open web
  curl --noproxy '*' "\$(../docktree open web)"
  ../docktree exec -T client redis-cli -h redis GET docktree:smoke

Feature worktree:
  cd "$FEATURE_DIR"
  ../docktree ps --service
  ../docktree open web
  curl --noproxy '*' "\$(../docktree open web)"
  ../docktree exec -T client redis-cli -h redis GET docktree:smoke

Generated artifacts worth inspecting:
  $MAIN_DIR/.docktree/
  $FEATURE_DIR/.docktree/

Destroy the demo and its Redis volume:
  $DEMO_ROOT/destroy-demo
EOF
}

print_kept_demo() {
  local status=$1

  if [ "$status" -eq 0 ]; then
    printf '\nDemo left running for manual exploration.\n'
  else
    printf '\nPartial demo retained for debugging after a failed smoke run.\n'
  fi
  printf '  root:       %s\n' "$DEMO_ROOT"
  printf '  main:       %s\n' "$MAIN_DIR"
  printf '  feature:    %s\n' "$FEATURE_DIR"
  [ -n "$MAIN_URL" ] && printf '  main URL:   %s\n' "$MAIN_URL"
  [ -n "$FEATURE_URL" ] && printf '  feature URL:%s%s\n' " " "$FEATURE_URL"
  printf '  Docktree:   %s/docktree\n' "$DEMO_ROOT"
  printf '  instructions: %s/README.txt\n' "$DEMO_ROOT"
  printf '  cleanup:    %s/destroy-demo\n' "$DEMO_ROOT"
}

handle_exit() {
  local status=$?

  trap - EXIT INT TERM
  if [ -z "$DEMO_ROOT" ] || [ ! -d "$DEMO_ROOT" ]; then
    exit "$status"
  fi
  if [ "$KEEP_DEMO" -eq 1 ]; then
    print_kept_demo "$status"
    exit "$status"
  fi
  if ! cleanup_demo "$DEMO_ROOT"; then
    status=1
  fi
  if [ "$status" -eq 0 ]; then
    log "Two-worktree smoke test passed and cleaned up"
  fi
  exit "$status"
}

if [ "$#" -eq 1 ] && { [ "$1" = "-h" ] || [ "$1" = "--help" ]; }; then
  usage
  exit 0
fi
if [ "$#" -eq 1 ] && [ "$1" = "--keep" ]; then
  KEEP_DEMO=1
elif [ "$#" -eq 2 ] && [ "$1" = "--cleanup" ]; then
  require_command docker
  cleanup_demo "$2"
  exit $?
elif [ "$#" -ne 0 ]; then
  usage >&2
  exit 2
fi

for command_name in awk curl docker git go mktemp sed; do
  require_command "$command_name"
done
[ -f "$FIXTURE_DIR/compose.yaml" ] || die "missing smoke Compose fixture"
[ -f "$FIXTURE_DIR/docktree.toml.tmpl" ] || die "missing smoke Docktree template"
docker compose version >/dev/null 2>&1 || die "Docker Compose is unavailable"
docker info >/dev/null 2>&1 || die "Docker daemon is unavailable"

ORIGINAL_HOME=${HOME:-}
[ -n "$ORIGINAL_HOME" ] || die "HOME is not set"
ORIGINAL_DOCKER_CONFIG=${DOCKER_CONFIG:-"$ORIGINAL_HOME/.docker"}
case "$ORIGINAL_DOCKER_CONFIG" in
  /*) ;;
  *) ORIGINAL_DOCKER_CONFIG="$(pwd -P)/$ORIGINAL_DOCKER_CONFIG" ;;
esac

TEMP_BASE=$(absolute_existing_dir "${TMPDIR:-/tmp}") || die "TMPDIR is not an accessible directory"
DEMO_ROOT=$(mktemp -d "$TEMP_BASE/docktree-smoke.XXXXXX")
APP_NAME="dtsmoke_$(date +%s)_$$"
MAIN_DIR="$DEMO_ROOT/main"
FEATURE_DIR="$DEMO_ROOT/feature"

mkdir -p "$DEMO_ROOT/bin" "$DEMO_ROOT/home/.config" "$MAIN_DIR"
printf 'docktree-smoke-v1\n%s\n' "$APP_NAME" >"$DEMO_ROOT/.docktree-smoke-root"
printf '%s\n' "$APP_NAME" >"$DEMO_ROOT/.app-name"
printf '%s\n' "$ORIGINAL_DOCKER_CONFIG" >"$DEMO_ROOT/.docker-config"
if [ "${DOCKER_CONTEXT+x}" = x ]; then printf '%s\n' "$DOCKER_CONTEXT" >"$DEMO_ROOT/.docker-context"; fi
if [ "${DOCKER_HOST+x}" = x ]; then printf '%s\n' "$DOCKER_HOST" >"$DEMO_ROOT/.docker-host"; fi
if [ "${DOCKER_TLS_VERIFY+x}" = x ]; then printf '%s\n' "$DOCKER_TLS_VERIFY" >"$DEMO_ROOT/.docker-tls-verify"; fi
if [ "${DOCKER_CERT_PATH+x}" = x ]; then printf '%s\n' "$DOCKER_CERT_PATH" >"$DEMO_ROOT/.docker-cert-path"; fi

write_demo_helpers
write_manual_readme
trap handle_exit EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

log "Building the current Docktree source"
CGO_ENABLED=0 go build -o "$DEMO_ROOT/bin/docktree-bin" "$REPO_ROOT"

log "Creating disposable Git repository"
cp "$FIXTURE_DIR/compose.yaml" "$FIXTURE_DIR/.gitignore" "$MAIN_DIR/"
sed "s/__DOCKTREE_SMOKE_APP__/$APP_NAME/g" "$FIXTURE_DIR/docktree.toml.tmpl" >"$MAIN_DIR/docktree.toml"
git_demo -C "$MAIN_DIR" init -q
git_demo -C "$MAIN_DIR" symbolic-ref HEAD refs/heads/main
git_demo -C "$MAIN_DIR" add .
git_demo -C "$MAIN_DIR" \
  -c user.name='Docktree Smoke' \
  -c user.email='docktree-smoke@example.invalid' \
  commit -qm 'Create Docktree smoke fixture'

log "Initializing primary worktree"
docktree_in "$DEMO_ROOT" "$MAIN_DIR" init
[ -z "$(git_demo -C "$MAIN_DIR" status --porcelain)" ] || die "docktree init changed tracked fixture files"

log "Creating and initializing linked feature worktree"
git_demo -C "$MAIN_DIR" worktree add -q -b feature "$FEATURE_DIR" main
docktree_in "$DEMO_ROOT" "$FEATURE_DIR" init
[ -z "$(git_demo -C "$FEATURE_DIR" status --porcelain)" ] || die "docktree init changed tracked files in the feature worktree"

log "Starting primary worktree and shared Redis"
docktree_in "$DEMO_ROOT" "$MAIN_DIR" up
wait_for_redis_from "$MAIN_DIR" "main worktree"
MAIN_INFRA_ID=$(infra_container_id)
assert_nonempty "$MAIN_INFRA_ID" "shared Redis container ID after main up"
case "$MAIN_INFRA_ID" in
  *$'\n'*) die "more than one shared Redis container was running after main up" ;;
esac

log "Starting feature worktree"
docktree_in "$DEMO_ROOT" "$FEATURE_DIR" up
wait_for_redis_from "$FEATURE_DIR" "feature worktree"
FEATURE_INFRA_ID=$(infra_container_id)
assert_eq "$FEATURE_INFRA_ID" "$MAIN_INFRA_ID" "shared Redis container identity"

MAIN_ENV="$MAIN_DIR/.docktree/.env.worktree"
FEATURE_ENV="$FEATURE_DIR/.docktree/.env.worktree"
MAIN_PROJECT=$(env_value "$MAIN_ENV" COMPOSE_PROJECT_NAME)
FEATURE_PROJECT=$(env_value "$FEATURE_ENV" COMPOSE_PROJECT_NAME)
MAIN_WEB_PORT=$(env_value "$MAIN_ENV" WEB_PORT)
FEATURE_WEB_PORT=$(env_value "$FEATURE_ENV" WEB_PORT)
MAIN_REDIS_PORT=$(env_value "$MAIN_ENV" REDIS_PORT)
FEATURE_REDIS_PORT=$(env_value "$FEATURE_ENV" REDIS_PORT)
MAIN_SHARED_NETWORK=$(env_value "$MAIN_ENV" DOCKTREE_SHARED_NETWORK)
FEATURE_SHARED_NETWORK=$(env_value "$FEATURE_ENV" DOCKTREE_SHARED_NETWORK)
MAIN_WORKTREE_NETWORK=$(env_value "$MAIN_ENV" DOCKTREE_WORKTREE_NETWORK)
FEATURE_WORKTREE_NETWORK=$(env_value "$FEATURE_ENV" DOCKTREE_WORKTREE_NETWORK)

assert_eq "$MAIN_PROJECT" "$APP_NAME" "main Compose project"
assert_eq "$FEATURE_PROJECT" "$APP_NAME-feature" "feature Compose project"
assert_nonempty "$MAIN_WEB_PORT" "main web port"
assert_nonempty "$FEATURE_WEB_PORT" "feature web port"
assert_ne "$MAIN_WEB_PORT" "$FEATURE_WEB_PORT" "worktree web ports"
assert_nonempty "$MAIN_REDIS_PORT" "main Redis port"
assert_nonempty "$FEATURE_REDIS_PORT" "feature Redis port"
assert_eq "$MAIN_REDIS_PORT" "$FEATURE_REDIS_PORT" "shared Redis port"
assert_eq "$MAIN_SHARED_NETWORK" "$FEATURE_SHARED_NETWORK" "shared network"
assert_eq "$MAIN_SHARED_NETWORK" "${APP_NAME}_shared" "shared network name"
assert_ne "$MAIN_WORKTREE_NETWORK" "$FEATURE_WORKTREE_NETWORK" "worktree networks"
"$DEMO_ROOT/docker" network inspect "$MAIN_WORKTREE_NETWORK" >/dev/null
"$DEMO_ROOT/docker" network inspect "$FEATURE_WORKTREE_NETWORK" >/dev/null
assert_eq "$(container_count_on_network "$MAIN_WORKTREE_NETWORK")" "3" "main worktree network container count"
assert_eq "$(container_count_on_network "$MAIN_WORKTREE_NETWORK" --filter 'label=com.docktree.tier=worktree' --filter 'label=com.docktree.slug=main')" "2" "main worktree service count"
assert_eq "$(container_count_on_network "$MAIN_WORKTREE_NETWORK" --filter 'label=com.docktree.tier=uplink' --filter 'label=com.docktree.slug=main')" "1" "main worktree uplink count"
assert_eq "$(container_count_on_network "$MAIN_WORKTREE_NETWORK" --filter 'label=com.docktree.slug=feature')" "0" "feature containers on the main network"
assert_eq "$(container_count_on_network "$FEATURE_WORKTREE_NETWORK")" "3" "feature worktree network container count"
assert_eq "$(container_count_on_network "$FEATURE_WORKTREE_NETWORK" --filter 'label=com.docktree.tier=worktree' --filter 'label=com.docktree.slug=feature')" "2" "feature worktree service count"
assert_eq "$(container_count_on_network "$FEATURE_WORKTREE_NETWORK" --filter 'label=com.docktree.tier=uplink' --filter 'label=com.docktree.slug=feature')" "1" "feature worktree uplink count"
assert_eq "$(container_count_on_network "$FEATURE_WORKTREE_NETWORK" --filter 'label=com.docktree.slug=main')" "0" "main containers on the feature network"
assert_eq "$(container_count_on_network "$MAIN_SHARED_NETWORK" --filter 'label=com.docktree.tier=infra' --filter 'label=com.docktree.service=redis')" "1" "shared-network Redis container count"
assert_eq "$(container_count_on_network "$MAIN_SHARED_NETWORK" --filter 'label=com.docktree.tier=uplink')" "2" "shared-network uplink count"
assert_eq "$(container_count_on_network "$MAIN_SHARED_NETWORK" --filter 'label=com.docktree.tier=uplink' --filter 'label=com.docktree.slug=main')" "1" "main uplink count"
assert_eq "$(container_count_on_network "$MAIN_SHARED_NETWORK" --filter 'label=com.docktree.tier=uplink' --filter 'label=com.docktree.slug=feature')" "1" "feature uplink count"
assert_eq "$(container_count_on_network "$MAIN_SHARED_NETWORK" --filter 'label=com.docktree.tier=worktree')" "0" "worktree containers attached to the shared network"

MAIN_URL=$(docktree_in "$DEMO_ROOT" "$MAIN_DIR" open web)
FEATURE_URL=$(docktree_in "$DEMO_ROOT" "$FEATURE_DIR" open web)
assert_eq "$MAIN_URL" "http://localhost:$MAIN_WEB_PORT" "main web URL"
assert_eq "$FEATURE_URL" "http://localhost:$FEATURE_WEB_PORT" "feature web URL"
assert_ne "$MAIN_URL" "$FEATURE_URL" "worktree web URLs"
wait_for_http "$MAIN_URL" "main web service"
wait_for_http "$FEATURE_URL" "feature web service"

log "Verifying shared state through both worktree uplinks"
REDIS_KEY='docktree:smoke'
REDIS_VALUE="written-through-$APP_NAME"
SET_RESULT=$(docktree_in "$DEMO_ROOT" "$MAIN_DIR" exec -T client redis-cli -h redis SET "$REDIS_KEY" "$REDIS_VALUE")
SET_RESULT=${SET_RESULT//$'\r'/}
assert_eq "$SET_RESULT" "OK" "Redis SET through main uplink"
GET_RESULT=$(docktree_in "$DEMO_ROOT" "$FEATURE_DIR" exec -T client redis-cli -h redis GET "$REDIS_KEY")
GET_RESULT=${GET_RESULT//$'\r'/}
assert_eq "$GET_RESULT" "$REDIS_VALUE" "Redis GET through feature uplink"

log "Running diagnostics and inspection commands"
docktree_in "$DEMO_ROOT" "$MAIN_DIR" doctor
docktree_in "$DEMO_ROOT" "$FEATURE_DIR" doctor
SHARED_STATUS=$(docktree_in "$DEMO_ROOT" "$MAIN_DIR" shared status)
assert_contains "$SHARED_STATUS" "redis" "shared status"
printf '%s\n' "$SHARED_STATUS"
LS_OUTPUT=$(docktree_in "$DEMO_ROOT" "$MAIN_DIR" ls)
assert_contains "$LS_OUTPUT" "$APP_NAME"$'\t' "docktree ls main project"
assert_contains "$LS_OUTPUT" "$APP_NAME-feature"$'\t' "docktree ls feature project"
printf '%s\n' "$LS_OUTPUT"
PS_OUTPUT=$(docktree_in "$DEMO_ROOT" "$FEATURE_DIR" ps --service)
assert_line "$PS_OUTPUT" "client" "feature service list"
assert_line "$PS_OUTPUT" "dt-uplink-feature" "feature service list"
assert_line "$PS_OUTPUT" "web" "feature service list"
printf '%s\n' "$PS_OUTPUT"

write_manual_readme
log "Validated main=$MAIN_URL feature=$FEATURE_URL shared-redis=$MAIN_INFRA_ID"
