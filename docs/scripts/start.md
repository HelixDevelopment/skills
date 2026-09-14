# `scripts/start.sh` — bring the datastore stack up

**Revision:** 1
**Last modified:** 2026-07-16T00:00:00Z

## Overview

Brings the HelixKnowledge Skill Graph datastore stack (Postgres + pgvector,
defined in `deploy/docker-compose.yml`) up via whichever of
`docker compose` / `podman compose` / `podman-compose` is available, then
waits for the `postgres` service to report healthy before returning. This
is the `ExecStart` command of the `systemctl --user` unit
(`deploy/systemd/helix-skills.service`) as well as the manual entry point.

## Prerequisites

- One of: Docker with `docker compose`, Podman with `podman compose`, or
  `podman-compose`.
- `deploy/docker-compose.yml` present (required; the script dies with a
  clear message if it is missing).
- `deploy/.env` (optional; dev-safe defaults apply if absent).

## Usage

```
scripts/start.sh [--timeout SECONDS] [--quiet] [-h|--help]
```

| Option | Effect |
|---|---|
| `--timeout SECONDS` | Max seconds to wait for Postgres readiness (default: 60). |
| `--quiet`, `-q` | Suppress informational (non-error) output (`HX_QUIET=1`). |
| `-h`, `--help` | Print usage and exit 0. |

### Examples

```bash
scripts/start.sh                    # bring the stack up, default 60s wait
scripts/start.sh --timeout 120      # allow more time on a slow host
scripts/start.sh --quiet            # used by the systemd unit's ExecStart
```

## Inputs

`deploy/docker-compose.yml` (required), `deploy/.env` (optional).

## Outputs

Container stack started; `compose ps` printed on success. Exits non-zero
if Postgres never becomes ready within the timeout — the stack is
deliberately **left running** in that case so `scripts/logs.sh` /
`scripts/status.sh` can be used to diagnose it (the script does not tear
down what it just started on a readiness-timeout).

## Side-effects

Starts containers/volumes/networks via the detected container engine.

## Edge cases

- **No compose engine found:** dies via `_lib.sh`'s `hx_detect_engine`
  with an actionable install-one-of-these message.
- **Compose file missing:** dies via `hx_require_compose_file` before
  attempting to start anything.
- **Postgres never becomes ready:** exits 1 after printing `compose ps`
  and pointing at `scripts/logs.sh postgres`; the stack is left up, not
  torn down, so the operator can inspect it.
- **Already running:** `compose up -d` is idempotent — re-running
  `start.sh` against an already-up stack is a no-op for already-healthy
  services.

## Internal behavior

1. Parses `--timeout` / `--quiet` / `--help`.
2. `hx_load_env` → `hx_require_compose_file` → `hx_detect_engine` (dies on
   any failure).
3. `hx_compose up -d`.
4. `hx_wait_for_postgres "${timeout_seconds}"` (polls `pg_isready` inside
   the `postgres` service every 2s).
5. On ready: prints `compose ps`, exits 0. On timeout: prints `compose ps`
   (best-effort, `|| true`), prints a pointer to `scripts/logs.sh`, exits 1.

## Dependencies

`_lib.sh`, one of `{docker, podman, podman-compose}`.

## Cross-references

`stop.sh`, `restart.sh` (composes stop+start), `status.sh`,
`deploy/docker-compose.yml`, `deploy/systemd/helix-skills.service` (its
`ExecStart=`).

## Last verified

2026-07-16, against `project/scripts/start.sh` (3291 bytes, last modified
2026-07-15).
