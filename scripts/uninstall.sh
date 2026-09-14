#!/usr/bin/env bash
# =============================================================================
# uninstall.sh - remove the HelixKnowledge Skill Graph systemd --user unit
# =============================================================================
# Purpose:
#   Reverses install.sh: disables + stops the unit (best-effort), removes
#   ~/.config/systemd/user/helix-skills.service, and runs
#   `systemctl --user daemon-reload`. Does NOT touch the compose stack
#   itself (containers/volumes) - run scripts/stop.sh first if you want the
#   stack down too.
#
# Usage:
#   scripts/uninstall.sh [--dry-run] [-h|--help]
#
# Inputs: none required beyond the current installed state (if any).
#
# Outputs:
#   Unit disabled/stopped (best-effort) and its file removed; daemon-reload
#   run. Idempotent - safe to run when nothing is installed.
#
# Side-effects: removes one file under ~/.config/systemd/user/, runs
#   `systemctl --user disable --now` and `systemctl --user daemon-reload`.
#
# Dependencies: _lib.sh, systemctl (systemd --user).
#
# Cross-references: install.sh, deploy/systemd/helix-skills.service,
#   docs/scripts/uninstall.md (companion user guide).
# Last verified: 2026-07-15
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=_lib.sh
source "${SCRIPT_DIR}/_lib.sh"

usage() {
    cat <<'EOF'
Usage: uninstall.sh [--dry-run] [-h|--help]

Remove the HelixKnowledge Skill Graph systemctl --user unit: disables +
stops it (best-effort) and deletes
~/.config/systemd/user/helix-skills.service, then runs
`systemctl --user daemon-reload`. Idempotent - safe to run whether or not
the unit is currently installed. Does NOT stop/remove the compose stack
itself; run scripts/stop.sh separately if desired.

Options:
  --dry-run   Show what would be done without changing anything.
  -h, --help  Show this help and exit.
EOF
}

dry_run=0

while [[ $# -gt 0 ]]; do
    case "$1" in
        --dry-run)
            dry_run=1
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "uninstall.sh: unknown argument: $1" >&2
            usage >&2
            exit 2
            ;;
    esac
done

unit_path="$(hx_systemd_unit_path)"

if [[ ! -f "${unit_path}" ]]; then
    hx_log "Nothing to do: ${unit_path} does not exist."
    exit 0
fi

if [[ "${dry_run}" == "1" ]]; then
    echo "--- DRY RUN: no files removed, no systemctl commands run ---"
    echo "Would run: systemctl --user disable --now ${HX_UNIT_FILENAME} (best-effort)"
    echo "Would remove: ${unit_path}"
    echo "Would run: systemctl --user daemon-reload"
    exit 0
fi

if command -v systemctl >/dev/null 2>&1 && hx_has_systemd_user; then
    hx_systemctl_user disable --now "${HX_UNIT_FILENAME}" >/dev/null 2>&1 || \
        hx_warn "disable --now reported an error or timed out (unit may already be inactive/disabled, or the systemd --user manager is unresponsive) - continuing."
else
    hx_warn "systemctl --user not reachable (absent, or not responding within ${HX_SYSTEMCTL_TIMEOUT}s); skipping disable/stop, removing unit file only."
fi

rm -f "${unit_path}"
hx_log "Removed: ${unit_path}"

if command -v systemctl >/dev/null 2>&1; then
    if hx_systemctl_user daemon-reload; then
        hx_log "Ran: systemctl --user daemon-reload"
    else
        hx_warn "systemctl --user daemon-reload failed or did not respond within ${HX_SYSTEMCTL_TIMEOUT}s. \
The unit file is already removed from disk; re-run 'systemctl --user daemon-reload' by hand once the manager is responsive again."
    fi
fi

hx_log "Uninstall complete. The compose stack (containers/volumes) was left untouched - run scripts/stop.sh if you want it down too."
