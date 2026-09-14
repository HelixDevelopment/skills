# `scripts/package.sh` — build a distributable archive

**Revision:** 2
**Last modified:** 2026-07-16T00:00:00Z

## Overview

Creates distributable `tar.gz` and `zip` archives of the entire project
(source, scripts, config, migrations, docs) for deployment or
distribution, plus a generated `INSTALL.txt` quick-start guide, a
`MANIFEST.json` describing the package, and a `.sha256` checksum file.

This script is unrelated to `backup.sh`/`restore.sh` (which snapshot
runtime *data*) — `package.sh` snapshots the *project itself* (source +
scripts + config + docs) for shipping to another environment.

## Prerequisites

- `zip` and `sha256sum` on `PATH`.
- Optionally a git repository at the project root (used to auto-derive a
  version string via `git describe --tags --always` or a short commit
  hash; falls back to `"dev"` if not a git repo or both git commands
  fail).

## Usage

```
./package.sh [--output <dir>] [--version <version>] [--no-source] [--help|-h]
```

| Option | Effect |
|---|---|
| `--output <dir>` | Output directory (default: `<project>/dist`). |
| `--version <ver>` | Package version (default: derived from `git describe`/short hash/`dev`). |
| `--no-source` | Exclude source code (`cmd/`, `internal/`, `go.mod`/`go.sum`, `Makefile`, `Dockerfile`, the canonical `deploy/docker-compose.yml`, `.env.example`) — package only binaries/scripts/config/docs. |
| `--help`, `-h` | Print usage and exit 0. |

### Examples

```bash
./package.sh                          # auto-versioned full package
./package.sh --version 1.2.0          # package as v1.2.0
./package.sh --output /tmp/releases   # custom output directory
./package.sh --no-source              # scripts/config/docs only, no Go source
```

## Inputs

`project/cmd/`, `project/internal/`, `project/go.mod`, `project/go.sum`,
`project/Makefile`, `project/Dockerfile`, the canonical
`project/deploy/docker-compose.yml` (copied into the package's `deploy/`
subdir so its relative paths resolve),
`project/.env.example` (all copied only when `--no-source` is **not**
given); `project/scripts/*.sh`; `project/config/`; `project/migrations/`;
`project/docs/`; `project/README.md`, `project/LICENSE`,
`project/CHANGELOG.md` (each copied only if present).

## Outputs

Under the output directory (default `project/dist/`):
`skill-system-<version>.tar.gz`, `skill-system-<version>.zip`, and
`skill-system-<version>.sha256` (checksums of the two archives). Each
archive's top-level directory contains the copied inputs above plus a
generated `INSTALL.txt` quick-start guide and a `MANIFEST.json` (name,
version, build timestamp, `include_source` flag, total file count).

## Side-effects

Creates a temp working directory (removed at the end of a successful
run); writes three files to the output directory; makes every copied
`*.sh` script executable in the package (`chmod +x`) regardless of the
executable bit on the source copy.

## Edge cases

- **Not a git repository:** version falls back to `"dev"` if
  `--version` is not given.
- **`git describe --tags` fails (no tags) but the repo exists:** falls
  back to `git rev-parse --short HEAD`; if that also fails, falls back to
  `"dev"`.
- **Optional source files absent** (`go.sum`, `config/`, `migrations/`,
  `docs/`, `README.md`, `LICENSE`, `CHANGELOG.md`): each copy is guarded
  with `|| true` or an existence check, so a missing optional file is
  silently skipped rather than failing the whole packaging run.
- **`zip` or `sha256sum` not installed:** the script does not check for
  these upfront; it will fail at the point of use (`zip: command not
  found` / `sha256sum: command not found`) under `set -euo pipefail`.

## Internal behavior

1. `get_version()` — returns `--version` if given, else derives from git,
   else `"dev"`.
2. `create_package()`:
   - Creates a temp directory structure:
     `{bin,scripts,config,docs,migrations,deploy,data/{evidence,backups}}`
     (the `deploy/` subdir preserves the canonical compose file's home so
     its `../config`, `../migrations`, `../Dockerfile` relative paths still
     resolve inside the package — G13).
   - Copies source (unless `--no-source`), scripts (always, made
     executable), config, migrations, docs, and top-level project files
     (each guarded for existence).
   - Writes `INSTALL.txt` (quick-start pointing at `scripts/install.sh`
     and the default ports) and `MANIFEST.json`.
   - Creates the output directory, then `tar -czf` and `zip -rq` archives
     from the temp tree, prints their sizes, computes a combined
     `sha256sum` checksum file, and removes the temp directory.
3. `main()` parses `--output`/`--version`/`--no-source`/`--help`, then
   calls `get_version()` followed by `create_package()`.

## Dependencies

`bash`, `git` (optional, for version auto-detection), `tar`, `zip`,
`sha256sum`, `mktemp`, `find`.

## Cross-references

References `scripts/install.sh`/`start.sh`/`stop.sh`/`status.sh`/
`backup.sh`/`restore.sh` only inside the generated `INSTALL.txt` text (as
end-user instructions for the *packaged* project) — does not invoke any
of them itself.

## Last verified

2026-07-16, against `project/scripts/package.sh` (7528 bytes, last
modified 2026-07-16) after the G13 canonical-compose change (packages
`deploy/docker-compose.yml` into the package's `deploy/` subdir).
