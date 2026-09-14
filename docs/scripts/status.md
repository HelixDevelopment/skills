# `scripts/status.sh` — report datastore stack status

**Revision:** 1
**Last modified:** 2026-07-16T00:00:00Z

## Overview

Reports three independent signals about the HelixKnowledge Skill Graph
datastore stack: the `systemctl --user` unit state (best-effort — never
fatal if the unit isn't installed or systemd `--user` isn't reachable),
`compose ps` for the stack, and a live `pg_isready` probe. The script never
crashes on a missing container engine or missing unit — those conditions
are reported as part of the "down" verdict, not as script failures.

## Prerequisites

None strictly required — every dependency is optional and its absence is
reported rather than treated as fatal. For a meaningful "UP" verdict you
need one of `{docker, podman, podman-compose}` and `deploy/docker-compose.yml`
present with the stack actually started.

## Usage

```
scripts/status.sh [-h|--help]
```

| Option | Effect |
|---|---|
| `-h`, `--help` | Print usage and exit 0. |

### Examples

```bash
scripts/status.sh
echo $?     # 0 if postgres is up and ready, 1 otherwise
```

## Inputs

`deploy/docker-compose.yml`, `deploy/.env` (both optional for this script —
their absence is reported, not fatal).

## Outputs

Human-readable report on stdout, in three sections:

1. `-- systemd --user unit --` — unit status via `hx_systemctl_user status`
   if installed and systemd `--user` is reachable; otherwise an explanatory
   line (not installed / systemctl unreachable).
2. `-- compose ps --` — the detected engine name, then `compose ps` output
   if the compose file exists; otherwise an explanatory line.
3. `-- postgres readiness (pg_isready) --` — `READY`/`NOT READY`/`SKIPPED`
   with the reason.

Exit code: **0** if Postgres is reachable and ready (the stack is
genuinely "up"); **non-zero (1)** otherwise — this includes "no container
engine installed" and "compose file missing", both of which are
unambiguously "down".

## Side-effects

None (read-only probes only).

## Edge cases

- **No container engine installed:** `hx_detect_engine_soft` returns 1;
  the compose-ps section prints "No container engine with compose support
  found" and the readiness section is `SKIPPED`; overall verdict is
  `DOWN`, exit 1.
- **Compose file missing:** compose-ps section reports the missing path;
  readiness section is `SKIPPED`.
- **systemd `--user` unreachable/hung:** the unit-state section reports
  "systemctl --user is not reachable (absent, or not responding within
  ${HX_SYSTEMCTL_TIMEOUT}s)" rather than hanging (bounded by
  `hx_systemctl_user`'s `timeout` wrapper) — this section's outcome never
  affects the overall exit code, only the readiness probe does.

## Internal behavior

1. `hx_load_env` (never dies — dev-safe defaults apply).
2. Section 1: `hx_has_systemd_user` gate → `hx_systemctl_user status` if
   the unit file exists, else "not installed" message.
3. Section 2: `hx_detect_engine_soft` gate → `hx_compose ps` if the
   compose file exists.
4. Section 3: only probed if an engine was found AND the compose file
   exists; calls `hx_pg_isready`.
5. Overall verdict + exit code derived solely from the readiness probe
   (`pg_ready` flag), not from the unit or compose-ps sections.

## Dependencies

`_lib.sh`; `systemctl` optional; one of `{docker, podman, podman-compose}`
optional (absence is reported, not required).

## Cross-references

`start.sh`, `stop.sh`, `restart.sh`.

## Last verified

2026-07-16, against `project/scripts/status.sh` (4689 bytes, last modified
2026-07-15).
