#!/usr/bin/env bash
# =============================================================================
# install.sh - install the HelixKnowledge Skill Graph systemd --user unit
# =============================================================================
# Purpose:
#   Renders deploy/systemd/helix-skills.service (substituting the
#   @HELIX_SKILLS_PROJECT_ROOT@ placeholder with this checkout's absolute
#   path) into ~/.config/systemd/user/helix-skills.service, then runs
#   `systemctl --user daemon-reload`. USER SCOPE ONLY - never writes a
#   system-wide unit, never invokes sudo.
#
# Usage:
#   scripts/install.sh [--dry-run] [-h|--help]
#
# Inputs:
#   deploy/systemd/helix-skills.service (required template).
#
# Outputs:
#   ~/.config/systemd/user/helix-skills.service written (unless --dry-run);
#   `systemctl --user daemon-reload` run; instructions for enabling the unit
#   and (optionally) `loginctl enable-linger` printed.
#
# Side-effects: writes one file under ~/.config/systemd/user/, runs
#   `systemctl --user daemon-reload`. Idempotent: re-running with the same
#   checkout path is a no-op write (content compared before writing) and
#   daemon-reload is always safe to repeat.
#
# Dependencies: _lib.sh, systemctl (systemd --user).
#
# Cross-references: uninstall.sh, deploy/systemd/helix-skills.service,
#   docs/scripts/install.md (companion user guide).
# Last verified: 2026-07-15
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=_lib.sh
source "${SCRIPT_DIR}/_lib.sh"

usage() {
    cat <<'EOF'
Usage: install.sh [--dry-run] [-h|--help]

Install the HelixKnowledge Skill Graph datastore as a systemctl --user unit
(no sudo, no system-wide unit). Renders
deploy/systemd/helix-skills.service into
~/.config/systemd/user/helix-skills.service with this checkout's absolute
path substituted in, then runs `systemctl --user daemon-reload`.
Idempotent - safe to re-run.

Options:
  --dry-run   Show what would be written/run without changing anything.
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
            echo "install.sh: unknown argument: $1" >&2
            usage >&2
            exit 2
            ;;
    esac
done

template_path="${HX_DEPLOY_DIR}/systemd/${HX_UNIT_FILENAME}"
[[ -f "${template_path}" ]] || hx_die "systemd unit template not found: ${template_path}"

unit_dir="${HOME}/.config/systemd/user"
unit_path="$(hx_systemd_unit_path)"

# -----------------------------------------------------------------------
# Render the template: substitute @HELIX_SKILLS_PROJECT_ROOT@ with this
# checkout's absolute path. Uses bash parameter expansion (not sed) so an
# unusual path (containing characters special to sed) can never corrupt
# the substitution.
# -----------------------------------------------------------------------
render_unit() {
    local line
    while IFS= read -r line || [[ -n "${line}" ]]; do
        printf '%s\n' "${line//@HELIX_SKILLS_PROJECT_ROOT@/${HX_PROJECT_ROOT}}"
    done < "${template_path}"
}

rendered="$(render_unit)"
existing=""
[[ -f "${unit_path}" ]] && existing="$(cat "${unit_path}")"

hx_log "Project root:      ${HX_PROJECT_ROOT}"
hx_log "Unit template:     ${template_path}"
hx_log "Target unit path:  ${unit_path}"

if [[ "${dry_run}" == "1" ]]; then
    echo "--- DRY RUN: no files written, no systemctl commands run ---"
    if [[ "${existing}" == "${rendered}" && -n "${existing}" ]]; then
        echo "Would leave unchanged (already installed and identical): ${unit_path}"
    elif [[ -f "${unit_path}" ]]; then
        echo "Would OVERWRITE existing unit at: ${unit_path}"
    else
        echo "Would CREATE new unit at: ${unit_path}"
    fi
    echo
    echo "--- Rendered unit content ---"
    printf '%s\n' "${rendered}"
    echo "--- end ---"
    echo
    echo "Would then run: systemctl --user daemon-reload"
    exit 0
fi

mkdir -p "${unit_dir}"

if [[ "${existing}" == "${rendered}" && -n "${existing}" ]]; then
    hx_log "Unit already installed and unchanged: ${unit_path}"
else
    # Write atomically: temp file in the same directory, then rename.
    tmp_file="$(mktemp "${unit_dir}/.${HX_UNIT_FILENAME}.XXXXXX")"
    printf '%s\n' "${rendered}" > "${tmp_file}"
    mv -f "${tmp_file}" "${unit_path}"
    hx_log "Wrote unit: ${unit_path}"
fi

if command -v systemctl >/dev/null 2>&1; then
    if hx_systemctl_user daemon-reload; then
        hx_log "Ran: systemctl --user daemon-reload"
    else
        hx_die "systemctl --user daemon-reload failed or did not respond within \
${HX_SYSTEMCTL_TIMEOUT}s. The unit file was written to ${unit_path} - this \
step only registers it with the running systemd --user manager, so it is \
safe to retry: 'systemctl --user daemon-reload' by hand once that manager \
is responsive again, or re-run this script."
    fi
else
    hx_warn "systemctl not found; skipped daemon-reload. Install systemd (user session) to use this unit."
fi

cat <<EOF

Install complete. Next steps:

  1. Enable and start the service now:
       systemctl --user enable --now ${HX_UNIT_FILENAME}

  2. (Recommended for boot persistence) Allow this user's systemd --user
     instance to run without an active login session:
       loginctl enable-linger ${USER}

  3. Check status any time with:
       scripts/status.sh
     or:
       systemctl --user status ${HX_UNIT_FILENAME}
EOF
