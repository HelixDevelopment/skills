#!/usr/bin/env bash
# =============================================================================
# restart.sh - restart the HelixKnowledge Skill Graph datastore stack
# =============================================================================
# Purpose:
#   Thin composition of stop.sh + start.sh so there is exactly one
#   implementation of each. This is the ExecReload command of the
#   systemctl --user unit (deploy/systemd/helix-skills.service) as well as
#   the manual entry point.
#
# Usage:
#   scripts/restart.sh [--timeout SECONDS] [--quiet] [-h|--help]
#
# Inputs:
#   deploy/docker-compose.yml (required), deploy/.env (optional).
#
# Outputs:
#   Stack stopped then started again; exits non-zero if either step fails
#   (mirrors stop.sh/start.sh exit codes).
#
# Side-effects: same as stop.sh followed by start.sh.
#
# Dependencies: _lib.sh, stop.sh, start.sh.
#
# Cross-references: start.sh, stop.sh, status.sh, docs/scripts/restart.md
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
Usage: restart.sh [--timeout SECONDS] [--quiet] [-h|--help]

Restart the HelixKnowledge Skill Graph datastore stack (stop.sh then
start.sh).

Options:
  --timeout SECONDS  Forwarded to start.sh (max seconds to wait for
                      Postgres readiness after the restart; default: 60).
  --quiet, -q         Suppress informational (non-error) output.
  -h, --help          Show this help and exit.
EOF
}

timeout_seconds=60

while [[ $# -gt 0 ]]; do
    case "$1" in
        --timeout)
            [[ $# -ge 2 ]] || { echo "restart.sh: --timeout requires an argument" >&2; exit 2; }
            timeout_seconds="$2"
            shift 2
            ;;
        --quiet|-q)
            HX_QUIET=1
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "restart.sh: unknown argument: $1" >&2
            usage >&2
            exit 2
            ;;
    esac
done

quiet_flag=()
[[ "${HX_QUIET}" == "1" ]] && quiet_flag=(--quiet)

hx_log "Restarting: stop then start ..."
"${SCRIPT_DIR}/stop.sh" "${quiet_flag[@]}"
"${SCRIPT_DIR}/start.sh" --timeout "${timeout_seconds}" "${quiet_flag[@]}"
