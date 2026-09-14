# `scripts/_lib.sh` — shared ops-script library

**Revision:** 2
**Last modified:** 2026-07-16T00:00:00Z

## Overview

`_lib.sh` is the shared helper library sourced by every script in the
"deploy/-based" ops family: `start.sh`, `stop.sh`, `restart.sh`, `status.sh`,
`install.sh`, `uninstall.sh`, and `logs.sh`. It is a **library, not a
command** — running it directly (`./scripts/_lib.sh`) prints a usage error
and exits 1 (detected via `"${BASH_SOURCE[0]}" == "${0}"`).

It centralizes four responsibilities so the seven callers do not each
re-implement them:

1. Resolves `HX_PROJECT_ROOT` / `HX_DEPLOY_DIR` relative to `_lib.sh`'s own
   location (never the caller's `cwd`), plus the compose-file / env-file /
   systemd-unit paths derived from it.
2. Detects which container-engine compose implementation is available on
   the host: `docker compose`, then `podman compose`, then the standalone
   `podman-compose` binary.
3. Loads `deploy/.env` (if present) with dev-safe fallback defaults for
   `COMPOSE_PROJECT_NAME` / `DB_NAME` / `DB_USER` / `DB_PASSWORD` /
   `DB_PORT`.
4. Provides an `hx_compose` wrapper (compose file + project name + optional
   env-file, forwarding all arguments) and a Postgres-readiness waiter.

## Prerequisites

- `bash` >= 4.
- Coreutils (`cd`, `dirname`, `pwd`, `mktemp`, etc.).
- One of: `docker` (with the `compose` subcommand), `podman` (with the
  `compose` subcommand), or the standalone `podman-compose` binary — only
  required by callers that actually run compose (`hx_detect_engine`
  callers); `status.sh` tolerates a missing engine via
  `hx_detect_engine_soft`.

## Usage

This file is **sourced**, never executed:

```bash
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=_lib.sh
source "${SCRIPT_DIR}/_lib.sh"
```

Running it directly prints:

```
_lib.sh is a library meant to be sourced, not executed directly.
Usage: source "$(dirname "${BASH_SOURCE[0]}")/_lib.sh"
```

and exits 1.

## Inputs

- Environment variables already exported by the caller (rare in practice).
- `deploy/.env`, read via `hx_load_env` if present (values fall back to
  dev-safe defaults when the file is absent or a given variable is unset).

## Outputs (exported symbols)

**Variables:** `HX_PROJECT_ROOT`, `HX_DEPLOY_DIR`, `HX_COMPOSE_FILE`,
`HX_ENV_FILE`, `HX_SERVICE_NAME`, `HX_UNIT_FILENAME`, `HX_COMPOSE_BIN`
(array, populated by `hx_detect_engine`/`hx_detect_engine_soft`),
`COMPOSE_PROJECT_NAME`, `DB_NAME`, `DB_USER`, `DB_PASSWORD`, `DB_PORT`
(all exported by `hx_load_env`).

**Functions:**

| Function | Purpose |
|---|---|
| `hx_log` | Info-level line to stdout; suppressed when `HX_QUIET=1`. |
| `hx_warn` | Warning to stderr; always printed regardless of quiet mode. |
| `hx_err` | Error to stderr; always printed. |
| `hx_die` | `hx_err` + `exit 1`. |
| `hx_load_env` | Sources `deploy/.env` (if present) and applies dev-safe defaults. |
| `hx_detect_engine_soft` | Populates `HX_COMPOSE_BIN`; returns 1 (never dies) if no engine found. |
| `hx_detect_engine` | Same as above but `hx_die`s with an actionable message if nothing is found. |
| `hx_require_compose_file` | `hx_die`s if `HX_COMPOSE_FILE` does not exist. |
| `hx_compose` | Runs the detected compose binary against the compose file + project name (+ env-file if present), forwarding all arguments. |
| `hx_pg_isready` | Runs `pg_isready` inside the `postgres` service via `hx_compose exec`. |
| `hx_wait_for_postgres <timeout_seconds>` | Polls `hx_pg_isready` every 2s until ready or timeout; returns 0/1. |
| `hx_systemd_unit_path` | Prints the absolute path of the user-scope systemd unit (may not exist yet). |
| `hx_systemctl_user` | Runs `systemctl --user "$@"` bounded by `HX_SYSTEMCTL_TIMEOUT` (default 10s) via `timeout`, if available. |
| `hx_has_systemd_user` | Best-effort liveness check: `systemctl` present AND `hx_systemctl_user status` succeeds within the timeout. |

## Side-effects

None beyond reading `deploy/.env` from disk (never written by this file).

## Edge cases

- **Sourced from an unusual `cwd`:** paths are resolved from
  `${BASH_SOURCE[0]}`, not the caller's `cwd`, so `HX_PROJECT_ROOT` is
  correct regardless of where the caller was invoked from.
- **`systemctl --user` hangs:** `hx_systemctl_user` wraps every systemctl
  call in `timeout "${HX_SYSTEMCTL_TIMEOUT}"` specifically because a
  systemd `--user` manager under heavy load (observed in practice: tens of
  thousands of loaded units on a shared host) can leave `systemctl --user`
  hanging indefinitely on an otherwise-live D-Bus session. Every caller in
  this ops layer goes through `hx_systemctl_user` rather than invoking
  `systemctl` directly for this reason.
- **No container engine present:** `hx_detect_engine_soft` returns 1
  without dying, so read-only callers (`status.sh`) can still report a
  "down" verdict instead of crashing; write callers (`start.sh`/`stop.sh`/
  `logs.sh`) use `hx_detect_engine`, which dies with an actionable
  "install Docker or Podman" message.
- **`deploy/.env` absent:** `hx_load_env` does not fail; it applies the
  same dev-safe defaults `deploy/docker-compose.yml` itself falls back to
  (`skilldb` / `skilluser` / `skillpassword` / port `5432` / project name
  `helix-skills`).

## Internal behavior

Path resolution happens unconditionally at source-time (lines
`HX_LIB_DIR=... HX_PROJECT_ROOT=...` etc.), before any function is called,
so every caller can rely on `HX_PROJECT_ROOT` immediately after the
`source` line. `hx_detect_engine_soft` tries the three supported compose
front-ends in a fixed preference order (`docker compose` →
`podman compose` → `podman-compose`) and never mixes engines within one
invocation.

## Dependencies

`bash >= 4`, coreutils, one of `{docker, podman, podman-compose}` (only for
callers that run compose).

## Cross-references

Sourced by: `start.sh`, `stop.sh`, `restart.sh`, `status.sh`, `install.sh`,
`uninstall.sh`, `logs.sh`. Not used by the older inline family
(`backup.sh`, `migrate.sh`, `package.sh`, `restore.sh`), which implement
their own inline `load_env`/`detect_compose` helpers rather than sourcing
`_lib.sh`. After G13 (`research/ops_hardening_design.md`), BOTH families
target the same single canonical compose file
`project/deploy/docker-compose.yml`; the inline family differs only in
reading its environment from `project/.env` (its own `INSTALL_DIR`) rather
than `project/deploy/.env`, and in not sharing this library.

## Last verified

2026-07-16, against `project/scripts/_lib.sh` (9800 bytes, last modified
2026-07-15).
