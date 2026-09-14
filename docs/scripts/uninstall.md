# `scripts/uninstall.sh` — remove the systemd `--user` unit

**Revision:** 1
**Last modified:** 2026-07-16T00:00:00Z

## Overview

Reverses `install.sh`: disables + stops the unit (best-effort), removes
`~/.config/systemd/user/helix-skills.service`, and runs `systemctl --user
daemon-reload`. Does **NOT** touch the compose stack itself
(containers/volumes) — run `scripts/stop.sh` first if you want the stack
down too.

## Prerequisites

None required beyond the current installed state (if any) — the script is
safe to run whether or not the unit is installed.

## Usage

```
scripts/uninstall.sh [--dry-run] [-h|--help]
```

| Option | Effect |
|---|---|
| `--dry-run` | Show what would be done without changing anything. |
| `-h`, `--help` | Print usage and exit 0. |

### Examples

```bash
scripts/uninstall.sh --dry-run   # preview
scripts/uninstall.sh             # actually uninstall
scripts/stop.sh && scripts/uninstall.sh   # also stop the compose stack
```

## Inputs

None required beyond the current installed state (if any).

## Outputs

Unit disabled/stopped (best-effort) and its file removed; `daemon-reload`
run. **Idempotent** — safe to run when nothing is installed (exits 0
immediately with "Nothing to do").

## Side-effects

Removes one file under `~/.config/systemd/user/`, runs `systemctl --user
disable --now` and `systemctl --user daemon-reload`.

## Edge cases

- **Nothing installed:** if the unit file does not exist, logs "Nothing to
  do" and exits 0 immediately — no `systemctl` calls are made.
- **`disable --now` fails/times out:** logged as a warning ("unit may
  already be inactive/disabled, or the systemd --user manager is
  unresponsive") and the script continues to remove the file regardless.
- **`systemctl` not reachable:** logged as a warning; disable/stop is
  skipped and only the unit file is removed.
- **`daemon-reload` fails/times out:** logged as a warning noting the unit
  file is already removed from disk and the reload can be retried by hand.
- **Compose stack left running:** explicitly NOT stopped by this script;
  the final log line reminds the operator to run `scripts/stop.sh`
  separately if the stack itself should also come down.

## Internal behavior

1. Parses `--dry-run` / `--help`.
2. Computes the unit path via `hx_systemd_unit_path`; if it doesn't exist,
   exits 0 immediately.
3. `--dry-run`: prints the three planned actions (disable --now, remove,
   daemon-reload) without executing them, exits 0.
4. Live mode: `systemctl --user disable --now` (best-effort, warning on
   failure) → `rm -f` the unit file → `systemctl --user daemon-reload`
   (best-effort, warning on failure) → final reminder about the compose
   stack.

## Dependencies

`_lib.sh`, `systemctl` (systemd `--user`).

## Cross-references

`install.sh` (reverses this script), `deploy/systemd/helix-skills.service`,
`stop.sh` (suggested companion for a full teardown).

## Last verified

2026-07-16, against `project/scripts/uninstall.sh` (3956 bytes, last
modified 2026-07-15).
