# `scripts/stop.sh` — bring the datastore stack down

**Revision:** 2
**Last modified:** 2026-07-16T12:48:58Z

## Overview

Runs `compose down` against `deploy/docker-compose.yml`. This is the
`ExecStop` command of the `systemctl --user` unit
(`deploy/systemd/helix-skills.service`) as well as the manual entry point.

## Prerequisites

- One of: Docker with `docker compose`, Podman with `podman compose`, or
  `podman-compose`.
- `deploy/docker-compose.yml` present (required).
- `deploy/.env` (optional).

## Usage

```
scripts/stop.sh [--compose] [--quiet] [-h|--help]
```

| Option | Effect |
|---|---|
| `--compose` | Explicit compose-teardown-mode selector (G65a fix). `stop.sh` has exactly one teardown mechanism today — `compose down` via `_lib.sh`'s `hx_compose` — so this flag is a documented, recognized no-op that selects that (only) path rather than falling through to "unknown argument". Present so callers such as `restore.sh` that pass it explicitly are honored instead of rejected. |
| `--quiet`, `-q` | Suppress informational (non-error) output. |
| `-h`, `--help` | Print usage and exit 0. |

### Examples

```bash
scripts/stop.sh            # stop the stack
scripts/stop.sh --compose  # explicit compose-teardown selector (accepted no-op)
scripts/stop.sh --quiet    # used by the systemd unit's ExecStop
```

## Inputs

`deploy/docker-compose.yml` (required), `deploy/.env` (optional).

## Outputs

Containers and the compose network are stopped and removed. **Named
volumes are preserved** — the Postgres data directory survives a
stop/start cycle.

## Side-effects

Stops/removes containers + the compose network via the container engine.
Never removes named volumes.

## Edge cases

- **No compose engine found / compose file missing:** dies via `_lib.sh`
  (`hx_detect_engine` / `hx_require_compose_file`) before attempting
  anything.
- **Stack already down:** `compose down` on an already-down stack is a
  no-op / idempotent.
- **`--compose` flag — G65a FIXED (now recognized, no longer "unknown
  argument"):** `restore.sh` invokes this script as
  `"$SCRIPT_DIR/stop.sh" --compose` before a restore. Previously
  `stop.sh`'s argument parser recognized only `--quiet`/`-q` and
  `-h`/`--help`, so `--compose` fell through to the `*` case, printed
  `stop.sh: unknown argument: --compose` to stderr, and exited 2 — every
  restore's "stop services first" step silently never actually stopped
  anything (masked further by a separate `restore.sh`-side bug, G65b — see
  `restore.md`). `--compose` is now parsed as a recognized, documented
  no-op selector (see Usage above) that falls through to the same
  `hx_compose down` call every other invocation of this script takes;
  `scripts/stop.sh --compose` now exits 0 and genuinely tears the stack
  down, proven by a black-box harness driving the real script against a
  stubbed `docker` binary and asserting both the exit code and that a real
  `compose ... down` invocation was issued. This closes **G65a** (filed in
  `GAPS_AND_RISKS_REGISTER.md` as part of G65, §11.4.201).

## Internal behavior

1. Parses `--compose` (accepted no-op) / `--quiet` / `--help`.
2. `hx_load_env` → `hx_require_compose_file` → `hx_detect_engine`.
3. `hx_compose down`.

## Dependencies

`_lib.sh`, one of `{docker, podman, podman-compose}`.

## Cross-references

`start.sh`, `restart.sh` (composes stop+start), `status.sh`,
`deploy/docker-compose.yml`, `deploy/systemd/helix-skills.service` (its
`ExecStop=`). Also invoked (with the now-recognized `--compose` flag, per
the G65a fix above and `restore.sh`'s own G65b fix) from the older
`restore.sh`.

## Last verified

2026-07-16, against `project/scripts/stop.sh` after the G65a fix
(`--compose` recognized as a documented no-op selector routing to the same
`hx_compose down` teardown path). Regression-tested with a black-box
harness invoking the real script (via a stubbed `docker` on `PATH`) with
`--compose` and asserting exit 0 plus a real `compose ... down` call in
the stub's invocation log.
