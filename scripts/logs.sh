#!/usr/bin/env bash
# =============================================================================
# logs.sh - tail HelixKnowledge Skill Graph datastore compose logs
# =============================================================================
# Purpose:
#   Thin wrapper around `compose logs` for the deploy/docker-compose.yml
#   stack, so operators don't need to remember which of
#   docker/podman/podman-compose is active on this host.
#
# Usage:
#   scripts/logs.sh [-f|--follow] [--tail N] [SERVICE...] [-h|--help]
#
# Inputs:
#   deploy/docker-compose.yml (required), deploy/.env (optional).
#
# Outputs:
#   Compose log output on stdout.
#
# Side-effects: none (read-only).
#
# Dependencies: _lib.sh, one of {docker, podman, podman-compose}.
#
# Cross-references: start.sh, stop.sh, status.sh, docs/scripts/logs.md
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
Usage: logs.sh [-f|--follow] [--tail N] [SERVICE...] [-h|--help]

Tail logs from the HelixKnowledge Skill Graph datastore compose stack.
With no SERVICE arguments, shows logs for every service in the stack.

Options:
  -f, --follow  Follow log output (like `tail -f`).
  --tail N      Number of lines to show from the end of the logs (default:
                compose's own default - all available lines).
  -h, --help    Show this help and exit.

Examples:
  scripts/logs.sh                  # all services, no follow
  scripts/logs.sh -f postgres      # follow the postgres service only
  scripts/logs.sh --tail 100
EOF
}

follow=0
tail_n=""
services=()

while [[ $# -gt 0 ]]; do
    case "$1" in
        -f|--follow)
            follow=1
            shift
            ;;
        --tail)
            [[ $# -ge 2 ]] || { echo "logs.sh: --tail requires an argument" >&2; exit 2; }
            tail_n="$2"
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        --)
            shift
            services+=("$@")
            break
            ;;
        -*)
            echo "logs.sh: unknown option: $1" >&2
            usage >&2
            exit 2
            ;;
        *)
            services+=("$1")
            shift
            ;;
    esac
done

hx_load_env
hx_require_compose_file
hx_detect_engine

args=(logs)
[[ "${follow}" == "1" ]] && args+=(--follow)
[[ -n "${tail_n}" ]] && args+=(--tail "${tail_n}")
args+=("${services[@]}")

hx_compose "${args[@]}"
