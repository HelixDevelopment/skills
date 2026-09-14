#!/usr/bin/env bash
# =============================================================================
# _lib.sh - shared helpers for the HelixKnowledge Skill Graph ops scripts
#           (start.sh / stop.sh / restart.sh / status.sh / install.sh /
#           uninstall.sh / logs.sh).
# =============================================================================
# Purpose:
#   Single place that (a) resolves the project root + deploy/ paths relative
#   to this file, (b) detects an available container engine's compose
#   implementation (docker compose / podman compose / podman-compose),
#   (c) loads deploy/.env (if present) with dev-safe fallback defaults, and
#   (d) provides a `hx_compose` wrapper + a Postgres readiness waiter so
#   every ops script shares one implementation instead of six copies.
#
# Usage:
#   This file is meant to be SOURCED, never executed directly:
#     # shellcheck source=_lib.sh
#     source "$SCRIPT_DIR/_lib.sh"
#
# Inputs:
#   Environment variables already exported by the caller (rare); otherwise
#   values are loaded from deploy/.env or defaulted.
#
# Outputs:
#   Exported/settable variables: HX_PROJECT_ROOT, HX_DEPLOY_DIR,
#   HX_COMPOSE_FILE, HX_ENV_FILE, HX_SERVICE_NAME, HX_COMPOSE_BIN (array),
#   DB_NAME, DB_USER, DB_PASSWORD, DB_PORT, COMPOSE_PROJECT_NAME.
#   Functions: hx_log, hx_warn, hx_err, hx_die, hx_load_env,
#   hx_detect_engine, hx_compose, hx_pg_isready, hx_wait_for_postgres,
#   hx_systemd_unit_path, hx_has_systemd.
#
# Side-effects: none beyond reading deploy/.env from disk (never written).
#
# Dependencies: bash >= 4, coreutils, one of {docker, podman, podman-compose}.
#
# Cross-references: deploy/docker-compose.yml, deploy/.env.example,
#   docs/scripts/_lib.md (companion user guide).
# Last verified: 2026-07-15
# =============================================================================

# This file must be sourced, not executed. ${BASH_SOURCE[0]} == $0 only when
# run directly.
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    echo "_lib.sh is a library meant to be sourced, not executed directly." >&2
    echo "Usage: source \"\$(dirname \"\${BASH_SOURCE[0]}\")/_lib.sh\"" >&2
    exit 1
fi

# -----------------------------------------------------------------------
# Path resolution (relative to THIS file, not the caller's cwd).
# -----------------------------------------------------------------------
HX_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HX_PROJECT_ROOT="$(cd "${HX_LIB_DIR}/.." && pwd)"
HX_DEPLOY_DIR="${HX_PROJECT_ROOT}/deploy"
HX_COMPOSE_FILE="${HX_DEPLOY_DIR}/docker-compose.yml"
HX_ENV_FILE="${HX_DEPLOY_DIR}/.env"
HX_SERVICE_NAME="helix-skills"
HX_UNIT_FILENAME="${HX_SERVICE_NAME}.service"

# -----------------------------------------------------------------------
# Logging helpers. Respect HX_QUIET=1 (set by --quiet callers) for hx_log.
# Warnings/errors always go to stderr regardless of quiet mode.
# -----------------------------------------------------------------------
HX_QUIET="${HX_QUIET:-0}"

hx_log() {
    [[ "${HX_QUIET}" == "1" ]] && return 0
    printf '[helix-skills] %s\n' "$*"
}

hx_warn() {
    printf '[helix-skills] WARN: %s\n' "$*" >&2
}

hx_err() {
    printf '[helix-skills] ERROR: %s\n' "$*" >&2
}

hx_die() {
    hx_err "$*"
    exit 1
}

# -----------------------------------------------------------------------
# hx_load_env: source deploy/.env (if present) into the environment with
# dev-safe fallback defaults so every script sees the same DB_*/
# COMPOSE_PROJECT_NAME values compose.yml itself falls back to.
# -----------------------------------------------------------------------
hx_load_env() {
    if [[ -f "${HX_ENV_FILE}" ]]; then
        set -a
        # shellcheck disable=SC1090
        source "${HX_ENV_FILE}"
        set +a
    fi

    : "${COMPOSE_PROJECT_NAME:=helix-skills}"
    : "${DB_NAME:=skilldb}"
    : "${DB_USER:=skilluser}"
    : "${DB_PASSWORD:=skillpassword}"
    : "${DB_PORT:=5432}"

    export COMPOSE_PROJECT_NAME DB_NAME DB_USER DB_PASSWORD DB_PORT
}

# -----------------------------------------------------------------------
# hx_detect_engine_soft: populate HX_COMPOSE_BIN (array) with the first
# working compose invocation, preferring `docker compose`, then
# `podman compose`, then the standalone `podman-compose` binary. Returns 1
# (HX_COMPOSE_BIN left empty) instead of dying when none is found/working -
# for callers (status.sh) that must keep reporting even when the engine is
# absent, per the "non-zero if down, never crash" contract.
# -----------------------------------------------------------------------
hx_detect_engine_soft() {
    HX_COMPOSE_BIN=()

    if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
        HX_COMPOSE_BIN=(docker compose)
        return 0
    fi

    if command -v podman >/dev/null 2>&1 && podman compose version >/dev/null 2>&1; then
        HX_COMPOSE_BIN=(podman compose)
        return 0
    fi

    if command -v podman-compose >/dev/null 2>&1; then
        HX_COMPOSE_BIN=(podman-compose)
        return 0
    fi

    return 1
}

# -----------------------------------------------------------------------
# hx_detect_engine: same as hx_detect_engine_soft, but dies with a clear,
# actionable message if no engine is found. Use this in start/stop/
# restart/logs, which have nothing useful to do without an engine.
# -----------------------------------------------------------------------
hx_detect_engine() {
    if ! hx_detect_engine_soft; then
        hx_die "No container engine with compose support found. Checked: \
'docker compose', 'podman compose', 'podman-compose'. Install one of: \
Docker (https://docs.docker.com/get-docker/) or \
Podman + podman-compose (https://podman.io/getting-started/installation)."
    fi
}

# -----------------------------------------------------------------------
# hx_require_compose_file: fail closed with a clear message if the compose
# file this whole layer depends on is missing.
# -----------------------------------------------------------------------
hx_require_compose_file() {
    [[ -f "${HX_COMPOSE_FILE}" ]] || hx_die "Compose file not found: ${HX_COMPOSE_FILE}"
}

# -----------------------------------------------------------------------
# hx_compose: run the detected compose binary against our compose file +
# project name, optionally with the env file, forwarding all arguments.
# Requires hx_detect_engine to have run first.
# -----------------------------------------------------------------------
hx_compose() {
    local -a cmd=("${HX_COMPOSE_BIN[@]}" -f "${HX_COMPOSE_FILE}" -p "${COMPOSE_PROJECT_NAME}")
    if [[ -f "${HX_ENV_FILE}" ]]; then
        cmd+=(--env-file "${HX_ENV_FILE}")
    fi
    cmd+=("$@")
    "${cmd[@]}"
}

# -----------------------------------------------------------------------
# hx_pg_isready: run pg_isready inside the running postgres container via
# compose exec. Returns pg_isready's own exit code (0 = accepting
# connections). Non-zero (including compose-exec failure when the
# container isn't running at all) means "down"/"not ready".
# -----------------------------------------------------------------------
hx_pg_isready() {
    hx_compose exec -T postgres pg_isready -U "${DB_USER}" -d "${DB_NAME}" >/dev/null 2>&1
}

# -----------------------------------------------------------------------
# hx_wait_for_postgres <timeout_seconds>: poll hx_pg_isready every 2s until
# it succeeds or the timeout elapses. Returns 0 on ready, 1 on timeout.
# -----------------------------------------------------------------------
hx_wait_for_postgres() {
    local timeout="${1:-60}"
    local waited=0
    local interval=2

    while (( waited < timeout )); do
        if hx_pg_isready; then
            return 0
        fi
        sleep "${interval}"
        waited=$(( waited + interval ))
    done

    return 1
}

# -----------------------------------------------------------------------
# hx_systemd_unit_path: absolute path to the installed user-scope unit
# file (may or may not exist yet).
# -----------------------------------------------------------------------
hx_systemd_unit_path() {
    printf '%s/.config/systemd/user/%s\n' "${HOME}" "${HX_UNIT_FILENAME}"
}

# -----------------------------------------------------------------------
# hx_systemctl_user: run `systemctl --user "$@"`, bounded by
# HX_SYSTEMCTL_TIMEOUT seconds (default 10). A systemd --user manager
# under heavy load (observed in practice: tens of thousands of loaded
# units on a shared host) can leave `systemctl --user` hanging
# indefinitely on an otherwise-live D-Bus session; every caller in this
# ops layer MUST go through this wrapper rather than invoking systemctl
# directly, so a stuck manager degrades a single command to a bounded,
# reported timeout instead of hanging the whole script forever.
# -----------------------------------------------------------------------
HX_SYSTEMCTL_TIMEOUT="${HX_SYSTEMCTL_TIMEOUT:-10}"

hx_systemctl_user() {
    if ! command -v timeout >/dev/null 2>&1; then
        systemctl --user "$@"
        return $?
    fi
    timeout "${HX_SYSTEMCTL_TIMEOUT}" systemctl --user "$@"
}

# -----------------------------------------------------------------------
# hx_has_systemd_user: true if a user systemd instance is reachable AND
# responsive within HX_SYSTEMCTL_TIMEOUT in this session (best-effort
# check, never fatal on its own; a hung/overloaded manager is reported
# as "not reachable" rather than blocking the caller).
# -----------------------------------------------------------------------
hx_has_systemd_user() {
    command -v systemctl >/dev/null 2>&1 && hx_systemctl_user status >/dev/null 2>&1
}
