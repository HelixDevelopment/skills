# `scripts/restart.sh` — restart the datastore stack

**Revision:** 1
**Last modified:** 2026-07-16T00:00:00Z

## Overview

Thin composition of `stop.sh` + `start.sh` so there is exactly one
implementation of each step. This is the `ExecReload` command of the
`systemctl --user` unit (`deploy/systemd/helix-skills.service`) as well as
the manual entry point.

## Prerequisites

Same as `start.sh`/`stop.sh`: one of `{docker, podman, podman-compose}`,
`deploy/docker-compose.yml` present, `deploy/.env` optional.

## Usage

```
scripts/restart.sh [--timeout SECONDS] [--quiet] [-h|--help]
```

| Option | Effect |
|---|---|
| `--timeout SECONDS` | Forwarded to `start.sh` (max seconds to wait for Postgres readiness after the restart; default: 60). |
| `--quiet`, `-q` | Suppress informational (non-error) output; forwarded to both `stop.sh` and `start.sh`. |
| `-h`, `--help` | Print usage and exit 0. |

### Examples

```bash
scripts/restart.sh                 # stop then start, default timeout
scripts/restart.sh --timeout 120   # allow more time for the post-restart start
```

## Inputs

`deploy/docker-compose.yml` (required), `deploy/.env` (optional) — via the
`stop.sh`/`start.sh` calls it makes.

## Outputs

Stack stopped then started again; exits non-zero if either step fails
(mirrors `stop.sh`/`start.sh` exit codes — bash's default `set -e`
propagates the first failing command's exit status since neither call is
suppressed with `|| true`).

## Side-effects

Same as `stop.sh` followed by `start.sh`.

## Edge cases

- **`stop.sh` fails:** because the script runs under `set -euo pipefail`
  and does not guard the `stop.sh` call, a failing stop aborts before
  `start.sh` runs.
- **`--quiet` propagation:** the script re-derives a `quiet_flag` array
  from `HX_QUIET` (set by its own `--quiet` parsing) and passes `--quiet`
  through to both sub-invocations explicitly, rather than relying on
  `HX_QUIET` being inherited by the child processes' own argument parsers.

## Internal behavior

1. Parses `--timeout` / `--quiet` / `--help` (identical shape to
   `start.sh`'s parser for `--timeout`).
2. Builds `quiet_flag=(--quiet)` if `HX_QUIET=1`.
3. Runs `"${SCRIPT_DIR}/stop.sh" "${quiet_flag[@]}"`.
4. Runs `"${SCRIPT_DIR}/start.sh" --timeout "${timeout_seconds}"
   "${quiet_flag[@]}"`.

## Dependencies

`_lib.sh`, `stop.sh`, `start.sh`.

## Cross-references

`start.sh`, `stop.sh`, `status.sh`,
`deploy/systemd/helix-skills.service` (its `ExecReload=`).

## Last verified

2026-07-16, against `project/scripts/restart.sh` (2562 bytes, last
modified 2026-07-15).
