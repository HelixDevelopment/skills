# `scripts/backup.sh` — create a backup archive

**Revision:** 2
**Last modified:** 2026-07-16T00:00:00Z

## Overview

Creates a compressed `tar.gz` archive containing a database dump
(`pg_dump`), configuration files (`.env`, `config/config.toml`, the
canonical `deploy/docker-compose.yml`), the evidence data directory, and a
JSON metadata file describing the backup. Companion of `restore.sh`, which
consumes the archives this script produces.

**Note on which compose stack this targets (G13):** `backup.sh` now targets
the single canonical compose file `project/deploy/docker-compose.yml` (the
same file the `start.sh`/`stop.sh`/`_lib.sh` family uses), via an explicit
`compose -f "$INSTALL_DIR/deploy/docker-compose.yml"`; its datastore service
is `postgres` and its app service is `app` — the retired root
`project/docker-compose.yml` and its `db`/`api` service names are gone (see
`research/ops_hardening_design.md` G13 + `scripts/check_compose_canonical.sh`).
It still differs from the `_lib.sh` family in one respect: it reads its
environment from `project/.env` (its own `INSTALL_DIR`), not
`project/deploy/.env`, and uses its own inline `load_env`/`detect_compose`
helpers rather than sourcing `_lib.sh`.

## Prerequisites

- One of: Docker with `docker compose`, `docker-compose` (legacy
  standalone binary), or `podman-compose`.
- A running `postgres` compose service (the script execs `pg_dump` inside it
  via `compose -f deploy/docker-compose.yml exec -T postgres`).
- `project/.env` (optional; falls back to `DB_HOST=db`,
  `DB_PORT=5432`, `DB_NAME=skilldb`, `DB_USER=skilluser`,
  `DB_PASSWORD=skillpassword` if absent or a given variable is unset).

## Usage

```
./backup.sh [--output <dir>] [--name <name>] [--full | --db-only] [--retention <days>] [--list] [--help|-h]
```

| Option | Effect |
|---|---|
| `--output <dir>` | Backup directory (default: `<project>/data/backups`). |
| `--name <name>` | Custom backup name (default: `skill-system_<version>_<timestamp>`). |
| `--full` | Full backup: database + configuration + evidence (default). |
| `--db-only` | Database only. |
| `--retention <days>` | Retention period in days for automatic cleanup of old backups (default: 30; 0 disables cleanup). |
| `--list` | List existing backups and exit (does not create a new backup). |
| `--help`, `-h` | Print usage and exit 0. |

### Examples

```bash
./backup.sh                           # full backup, default retention
./backup.sh --db-only                 # database-only backup
./backup.sh --name pre-upgrade        # named backup before a risky change
./backup.sh --retention 7             # keep only 7 days of backups
./backup.sh --list                    # show existing backups, then exit
```

## Inputs

`project/.env` (optional, for DB connection defaults), a running `postgres`
compose service, `project/config/config.toml` and
`project/deploy/docker-compose.yml`
(copied when present, `--full` mode only), `project/data/evidence/`
(archived when present, `--full` mode only).

## Outputs

A single `<name>.tar.gz` archive under the backup directory, containing:
`database/skilldb.sql.gz`, and (in `--full` mode) `config/.env`,
`config/config.toml`, `config/docker-compose.yml`, and
`evidence/evidence.tar.gz`, plus a `metadata/backup.json` describing the
backup's name/type/timestamp/version/hostname/DB connection info/contents
flags. The final archive path is printed (and echoed to stdout).

## Side-effects

Creates a temp directory (cleaned up on both success and the `pg_dump`
failure path), writes the final archive to `$BACKUP_DIR`, and — if
`--retention` is non-zero — deletes archives in that directory older than
the retention window on every run.

## Edge cases

- **No compose command found:** the script's own `detect_compose()`
  checks `docker compose`, then `docker-compose`, then `podman-compose`;
  none found → `log_error` + `exit 1`.
- **`pg_dump` fails on the first attempt:** the script retries once with
  `PGPASSWORD` explicitly injected via `compose exec -T -e
  PGPASSWORD=... postgres pg_dump ...` before giving up (`exit 1`, temp dir
  removed).
- **No evidence directory:** logged as a warning; the backup proceeds
  without an `evidence.tar.gz` member.
- **`--retention 0`:** disables the automatic cleanup of old backups
  entirely (the `if [ "$RETENTION_DAYS" -gt 0 ]` guard in
  `cleanup_old_backups`).
- **`--list` with no existing backups:** prints "No backups found" and
  returns (does not error).

## Internal behavior

1. `load_env()` sources `project/.env` if present, with fallback defaults.
2. `detect_compose()` picks the first available compose front-end.
3. `main()` parses arguments; `--list` short-circuits to `list_backups()`
   and exits.
4. `generate_name()` builds the backup name from `--name`, or
   `skill-system_<version>_<timestamp>` where `<version>` is read by
   execing the `app` container's `/app/server --version` (falls back to
   `"unknown"` if that fails; the `app` service is opt-in under the
   canonical file's `app` profile).
5. `create_backup()`: creates a temp working tree
   (`database/`, `config/`, `evidence/`, `metadata/`), dumps + gzips the
   database, copies configuration + archives evidence in `--full` mode,
   writes `metadata/backup.json`, tars the whole tree into the final
   archive under `$BACKUP_DIR`, prints the resulting size, then calls
   `cleanup_old_backups()`.

## Dependencies

`bash`, one of `{docker compose, docker-compose, podman-compose}`,
`gzip`, `tar`, `mktemp`, `date`, `du`.

## Cross-references

`restore.sh` (consumes the archives this script produces),
`package.sh` (a separate, unrelated "package the whole project for
distribution" script — not a backup).

## Last verified

2026-07-16, against `project/scripts/backup.sh` (9710 bytes, last modified
2026-07-16) after the G13 canonical-compose change (compose calls target
`-f deploy/docker-compose.yml`; services `postgres`/`app`).
