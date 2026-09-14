#!/usr/bin/env bash
# =============================================================================
# status.sh - report status of the HelixKnowledge Skill Graph datastore stack
# =============================================================================
# Purpose:
#   Reports three independent signals: the systemctl --user unit state
#   (best-effort - never fatal if the unit isn't installed or systemd --user
#   isn't reachable), `compose ps` for the stack, and a live pg_isready
#   probe. Never crashes on a missing container engine or missing unit -
#   those are reported as part of the "down" verdict, not script failures.
#
# Usage:
#   scripts/status.sh [-h|--help]
#
# Inputs:
#   deploy/docker-compose.yml, deploy/.env (both optional for this script -
#   their absence is reported, not fatal).
#
# Outputs:
#   Human-readable report on stdout. Exit code: 0 if Postgres is reachable
#   and ready (the stack is genuinely "up"); non-zero (1) otherwise - this
#   includes "no container engine installed" and "compose file missing",
#   both of which are unambiguously "down".
#
# Side-effects: none (read-only probes only).
#
# Dependencies: _lib.sh; systemctl optional; one of
#   {docker, podman, podman-compose} optional (absence is reported, not
#   required).
#
# Cross-references: start.sh, stop.sh, restart.sh, docs/scripts/status.md
#   (companion user guide).
# Last verified: 2026-07-15
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=_lib.sh
source "${SCRIPT_DIR}/_lib.sh"

usage() {
    cat <<'EOF'
Usage: status.sh [-h|--help]

Report status of the HelixKnowledge Skill Graph datastore stack:
  1. systemctl --user unit state (best-effort).
  2. `compose ps` for the deploy/docker-compose.yml stack.
  3. A live pg_isready probe against the postgres service.

Exit code: 0 if postgres is up and ready, non-zero otherwise (this
includes "no container engine found" and "compose file missing" - both
are unambiguously "down").

Options:
  -h, --help  Show this help and exit.
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "status.sh: unknown argument: $1" >&2
            usage >&2
            exit 2
            ;;
    esac
done

hx_load_env

echo "== HelixKnowledge Skill Graph datastore status (project: ${COMPOSE_PROJECT_NAME}) =="

# -------------------------------------------------------------------
# 1) systemctl --user unit (best-effort; never fatal).
# -------------------------------------------------------------------
unit_path="$(hx_systemd_unit_path)"
echo
echo "-- systemd --user unit --"
if hx_has_systemd_user; then
    if [[ -f "${unit_path}" ]]; then
        hx_systemctl_user status "${HX_UNIT_FILENAME}" --no-pager 2>&1 || true
    else
        echo "not installed: ${unit_path} does not exist (run scripts/install.sh)."
    fi
else
    echo "systemctl --user is not reachable (absent, or not responding within ${HX_SYSTEMCTL_TIMEOUT}s)."
fi

# -------------------------------------------------------------------
# 2) compose ps (best-effort; absence of an engine is reported, not a
#    script crash).
# -------------------------------------------------------------------
echo
echo "-- compose ps --"
engine_found=0
if hx_detect_engine_soft; then
    engine_found=1
    echo "engine: ${HX_COMPOSE_BIN[*]}"
    if [[ -f "${HX_COMPOSE_FILE}" ]]; then
        hx_compose ps || true
    else
        echo "compose file not found: ${HX_COMPOSE_FILE}"
    fi
else
    echo "No container engine with compose support found (checked: docker compose, podman compose, podman-compose)."
fi

# -------------------------------------------------------------------
# 3) live pg_isready probe.
# -------------------------------------------------------------------
echo
echo "-- postgres readiness (pg_isready) --"
pg_ready=0
if [[ "${engine_found}" == "1" && -f "${HX_COMPOSE_FILE}" ]]; then
    if hx_pg_isready; then
        echo "READY: postgres is accepting connections (db=${DB_NAME}, user=${DB_USER})."
        pg_ready=1
    else
        echo "NOT READY: postgres is not accepting connections (stack down, still starting, or unreachable)."
    fi
else
    echo "SKIPPED: cannot probe postgres (no container engine and/or missing compose file - see above)."
fi

echo
if [[ "${pg_ready}" == "1" ]]; then
    echo "OVERALL: UP"
    exit 0
else
    echo "OVERALL: DOWN"
    exit 1
fi
