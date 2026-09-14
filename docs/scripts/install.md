# `scripts/install.sh` — install the systemd `--user` unit

**Revision:** 1
**Last modified:** 2026-07-16T00:00:00Z

## Overview

Renders `deploy/systemd/helix-skills.service` (substituting the
`@HELIX_SKILLS_PROJECT_ROOT@` placeholder with this checkout's absolute
path) into `~/.config/systemd/user/helix-skills.service`, then runs
`systemctl --user daemon-reload`. **USER SCOPE ONLY** — never writes a
system-wide unit, never invokes `sudo`.

## Prerequisites

- `deploy/systemd/helix-skills.service` present (the template; required).
- `systemctl` (systemd `--user`) for the `daemon-reload` step — its
  absence is a warning, not a fatal error (the unit file is still
  written).

## Usage

```
scripts/install.sh [--dry-run] [-h|--help]
```

| Option | Effect |
|---|---|
| `--dry-run` | Show what would be written/run without changing anything. |
| `-h`, `--help` | Print usage and exit 0. |

### Examples

```bash
scripts/install.sh --dry-run    # preview the rendered unit + planned actions
scripts/install.sh              # actually install
```

## Inputs

`deploy/systemd/helix-skills.service` (required template).

## Outputs

`~/.config/systemd/user/helix-skills.service` written (unless `--dry-run`);
`systemctl --user daemon-reload` run; instructions for enabling the unit
and (optionally) `loginctl enable-linger` printed on success.

## Side-effects

Writes one file under `~/.config/systemd/user/`, runs `systemctl --user
daemon-reload`. **Idempotent**: re-running with the same checkout path is
a no-op write (rendered content is compared against the existing file
before writing) and `daemon-reload` is always safe to repeat.

## Edge cases

- **Already installed and identical:** the script compares the rendered
  unit against the existing file's content and skips the write entirely,
  logging "Unit already installed and unchanged".
- **Placeholder substitution:** uses bash parameter expansion
  (`${line//@HELIX_SKILLS_PROJECT_ROOT@/${HX_PROJECT_ROOT}}`), not `sed`,
  specifically so a checkout path containing characters special to `sed`
  can never corrupt the substitution.
- **Atomic write:** the rendered unit is written to a `mktemp` temp file
  in the same directory as the target, then `mv -f`'d into place — never a
  direct in-place write.
- **`systemctl` absent:** logged as a warning ("skipped daemon-reload");
  the unit file is still written.
- **`daemon-reload` times out/fails:** the script dies with a message
  explaining the unit file was already written successfully and that this
  step (registering it with the running manager) is safe to retry by hand
  or by re-running the script.
- **Template missing:** dies immediately via `hx_die` before any write is
  attempted.

## Internal behavior

1. Parses `--dry-run` / `--help`.
2. Verifies `deploy/systemd/helix-skills.service` exists.
3. Renders the template line-by-line via `render_unit()` (bash string
   substitution, not `sed`).
4. Compares rendered content against any existing installed unit.
5. `--dry-run`: prints the plan (create/overwrite/unchanged) and the full
   rendered content, then exits 0 without writing or running `systemctl`.
6. Live mode: writes atomically (temp file + `mv -f`) if changed, then
   runs `systemctl --user daemon-reload` via `hx_systemctl_user` (bounded
   by `HX_SYSTEMCTL_TIMEOUT`), then prints next-step instructions
   (`systemctl --user enable --now`, `loginctl enable-linger`,
   `scripts/status.sh`).

## Dependencies

`_lib.sh`, `systemctl` (systemd `--user`).

## Cross-references

`uninstall.sh` (reverses this script), `deploy/systemd/helix-skills.service`
(the template rendered), `status.sh` (suggested next step).

## Last verified

2026-07-16, against `project/scripts/install.sh` (5770 bytes, last
modified 2026-07-15).
