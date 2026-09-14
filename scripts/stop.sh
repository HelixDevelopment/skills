#!/usr/bin/env bash
# =============================================================================
# stop.sh - bring the HelixKnowledge Skill Graph datastore stack down
# =============================================================================
# Purpose:
#   Runs `compose down` against deploy/docker-compose.yml. This is the
#   ExecStop command of the systemctl --user unit (deploy/systemd/
#   helix-skills.service) as well as the manual entry point.
#
# Usage:
#   scripts/stop.sh [--compose] [--quiet] [-h|--help]
#
# Inputs:
#   deploy/docker-compose.yml (required), deploy/.env (optional).
#
# --compose:
#   Explicit compose-teardown-mode selector, accepted for caller-intent
#   clarity (restore.sh invokes `stop.sh --compose` before a restore, per
#   G65 in GAPS_AND_RISKS_REGISTER.md). stop.sh has exactly one teardown
#   mechanism today (compose down via _lib.sh's hx_compose) - the flag is a
#   documented no-op that selects that (only) path rather than being
#   rejected as an unknown argument. If a second teardown mechanism is ever
#   added, --compose becomes the selector that pins this one.
#
# Outputs:
#   Containers stopped and removed (named volumes are preserved - data
#   survives a stop/start cycle).
#
# Side-effects: stops/removes containers + the compose network via the
#   container engine. Never removes named volumes.
#
# Dependencies: _lib.sh, one of {docker, podman, podman-compose}.
#
# Cross-references: start.sh, restart.sh, status.sh, deploy/docker-compose.yml,
#   deploy/systemd/helix-skills.service, docs/scripts/stop.md (companion
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
Usage: stop.sh [--compose] [--quiet] [-h|--help]

Bring the HelixKnowledge Skill Graph datastore (Postgres + pgvector) down via
docker compose / podman compose / podman-compose. Named volumes (the
Postgres data directory) are preserved.

Options:
  --compose    Explicit compose-teardown-mode selector (no-op today - this
               is the script's only teardown path; accepted so callers like
               restore.sh that pass it explicitly do not hit "unknown
               argument").
  --quiet, -q  Suppress informational (non-error) output.
  -h, --help   Show this help and exit.
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --compose)
            # G65 (GAPS_AND_RISKS_REGISTER.md): recognized, documented no-op -
            # stop.sh's only teardown mechanism already is `compose down`
            # (below), so honoring this flag means accepting it rather than
            # falling through to the "unknown argument" branch.
            shift
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
            echo "stop.sh: unknown argument: $1" >&2
            usage >&2
            exit 2
            ;;
    esac
done

hx_load_env
hx_require_compose_file
hx_detect_engine

hx_log "Using compose engine: ${HX_COMPOSE_BIN[*]}"
hx_log "Bringing down '${COMPOSE_PROJECT_NAME}' stack from ${HX_COMPOSE_FILE} ..."

hx_compose down

hx_log "Stack is down."
