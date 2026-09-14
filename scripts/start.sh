#!/usr/bin/env bash
# =============================================================================
# start.sh - bring the HelixKnowledge Skill Graph datastore stack up
# =============================================================================
# Purpose:
#   Runs `compose up -d` against deploy/docker-compose.yml (via whichever of
#   docker compose / podman compose / podman-compose is available) and waits
#   for the postgres service to report healthy before returning. This is the
#   ExecStart command of the systemctl --user unit (deploy/systemd/
#   helix-skills.service) as well as the manual entry point.
#
# Usage:
#   scripts/start.sh [--timeout SECONDS] [--quiet] [-h|--help]
#
# Inputs:
#   deploy/docker-compose.yml (required), deploy/.env (optional).
#
# Outputs:
#   Container stack started; prints compose ps output; exits non-zero if
#   Postgres never becomes ready within the timeout (the stack is left
#   running so `scripts/logs.sh` / `scripts/status.sh` can diagnose it).
#
# Side-effects: starts containers/volumes/networks via the container engine.
#
# Dependencies: _lib.sh, one of {docker, podman, podman-compose}.
#
# Cross-references: stop.sh, restart.sh, status.sh, deploy/docker-compose.yml,
#   deploy/systemd/helix-skills.service, docs/scripts/start.md (companion
#   user guide).
# Last verified: 2026-07-15
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=_lib.sh
source "${SCRIPT_DIR}/_lib.sh"

usage() {
    cat <<'EOF'
Usage: start.sh [--timeout SECONDS] [--quiet] [-h|--help]

Bring the HelixKnowledge Skill Graph datastore (Postgres + pgvector) up via
docker compose / podman compose / podman-compose, then wait for Postgres to
report healthy.

Options:
  --timeout SECONDS  Max seconds to wait for Postgres readiness (default: 60).
  --quiet, -q         Suppress informational (non-error) output.
  -h, --help          Show this help and exit.
EOF
}

timeout_seconds=60

while [[ $# -gt 0 ]]; do
    case "$1" in
        --timeout)
            [[ $# -ge 2 ]] || { echo "start.sh: --timeout requires an argument" >&2; exit 2; }
            timeout_seconds="$2"
            shift 2
            ;;
        --quiet|-q)
            export HX_QUIET=1
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "start.sh: unknown argument: $1" >&2
            usage >&2
            exit 2
            ;;
    esac
done

hx_load_env
hx_require_compose_file
hx_detect_engine

hx_log "Using compose engine: ${HX_COMPOSE_BIN[*]}"
hx_log "Bringing up '${COMPOSE_PROJECT_NAME}' stack from ${HX_COMPOSE_FILE} ..."

hx_compose up -d

hx_log "Waiting up to ${timeout_seconds}s for postgres to report healthy ..."
if hx_wait_for_postgres "${timeout_seconds}"; then
    hx_log "postgres is ready."
    hx_compose ps
    hx_log "Stack is up."
    exit 0
else
    hx_err "postgres did not become ready within ${timeout_seconds}s."
    hx_compose ps || true
    hx_err "Check logs with: scripts/logs.sh postgres"
    exit 1
fi
