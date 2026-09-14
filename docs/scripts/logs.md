# `scripts/logs.sh` — tail datastore compose logs

**Revision:** 1
**Last modified:** 2026-07-16T00:00:00Z

## Overview

Thin wrapper around `compose logs` for the `deploy/docker-compose.yml`
stack, so operators don't need to remember which of
docker/podman/podman-compose is active on this host.

## Prerequisites

One of `{docker, podman, podman-compose}`; `deploy/docker-compose.yml`
present (required).

## Usage

```
scripts/logs.sh [-f|--follow] [--tail N] [SERVICE...] [-h|--help]
```

| Option | Effect |
|---|---|
| `-f`, `--follow` | Follow log output (like `tail -f`). |
| `--tail N` | Number of lines to show from the end of the logs (default: compose's own default — all available lines). |
| `SERVICE...` | Zero or more service names to restrict the log output to (default: all services). |
| `-h`, `--help` | Print usage and exit 0. |

### Examples

```bash
scripts/logs.sh                  # all services, no follow
scripts/logs.sh -f postgres      # follow the postgres service only
scripts/logs.sh --tail 100       # last 100 lines, all services
```

## Inputs

`deploy/docker-compose.yml` (required), `deploy/.env` (optional).

## Outputs

Compose log output on stdout.

## Side-effects

None (read-only).

## Edge cases

- **`--tail` without an argument:** the parser explicitly checks `[[ $# -ge
  2 ]]` and prints `logs.sh: --tail requires an argument` + exits 2 if the
  value is missing, rather than consuming the next flag as the tail count.
- **`--` separator:** everything after a bare `--` is treated as service
  names even if it looks like a flag (`services+=("$@"); break`).
- **No compose engine / compose file missing:** dies via `_lib.sh`
  (`hx_require_compose_file` / `hx_detect_engine`) before attempting to
  tail anything — this script has no fallback "report and continue" mode
  like `status.sh` does.

## Internal behavior

1. Parses `-f`/`--follow`, `--tail N`, `-h`/`--help`, `--` (positional
   separator), and bare positional arguments (treated as service names) or
   unknown `-*` flags (rejected with exit 2).
2. `hx_load_env` → `hx_require_compose_file` → `hx_detect_engine`.
3. Builds a `logs` compose subcommand invocation with `--follow`/`--tail`
   appended as applicable, plus any requested service names.
4. `hx_compose "${args[@]}"`.

## Dependencies

`_lib.sh`, one of `{docker, podman, podman-compose}`.

## Cross-references

`start.sh`, `stop.sh`, `status.sh` (both point operators here for
diagnostics after a readiness timeout).

## Last verified

2026-07-16, against `project/scripts/logs.sh` (2784 bytes, last modified
2026-07-15).
