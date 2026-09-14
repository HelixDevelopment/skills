# `scripts/migrate.sh` — database migration runner

**Revision:** 7
**Last modified:** 2026-07-16T13:34:09Z

## Overview

A small SQL-migration runner that applies/rolls back/creates/inspects
versioned SQL migration files under `project/migrations/*.up.sql` /
`*.down.sql`, tracked in a `schema_migrations` table it creates on demand.
It executes SQL directly inside the canonical `postgres` compose service —
targeting the single canonical compose file explicitly via
`compose -f deploy/docker-compose.yml exec -T postgres psql ...` (G13; no
longer a cwd-discovered rival root compose or a `db` service name).

**Note on which compose stack this targets:** like `backup.sh`/
`restore.sh`, this script is part of the older project-root-based family
(`INSTALL_DIR = project/`, `project/.env`) — see "Two coexisting script
families" in this project's top-level `README.md`. It is distinct from
whatever migration mechanism (if any) the Go backend under
`project/internal/db/` invokes at application startup; this document
covers only the standalone `scripts/migrate.sh` CLI.

## Prerequisites

- One of: Docker with `docker compose`, `docker-compose`, or
  `podman-compose`.
- A running `postgres` compose service (the canonical `deploy/docker-compose.yml`
  datastore service; the retired root file's `db` service is superseded).
- `project/.env` (optional; falls back to `DB_HOST=db`, `DB_PORT=5432`,
  `DB_NAME=skilldb`, `DB_USER=skilluser`, `DB_PASSWORD=skillpassword`).
- `project/migrations/` directory (created automatically if absent).

## Usage

```
./migrate.sh [up | down | status | create <name> | version]
```

| Command | Effect |
|---|---|
| `up` (default) | Apply all pending migrations in filename order. |
| `down` | Roll back exactly one migration (the current highest applied version). |
| `status` | Print a table of every discovered migration file and whether it is `applied`/`pending`. |
| `create <name>` | Create a new `<timestamp>_<name>.up.sql` / `.down.sql` pair with boilerplate `BEGIN;`/`COMMIT;` blocks. |
| `version` | Print the current schema version (max applied `version`, or `0`). |
| `--help`, `-h` | Print usage and exit 0. |

### Examples

```bash
./migrate.sh                     # apply all pending (default = up)
./migrate.sh status              # show applied/pending table
./migrate.sh create add_skills   # scaffold a new migration pair
./migrate.sh down                # roll back the last migration
./migrate.sh version             # print current schema version
```

## Inputs

`project/migrations/*.up.sql` / `*.down.sql` (migration file pairs, named
`<version>_<description>.up.sql` / `.down.sql`, where `<version>` is the
leading numeric prefix used both for ordering and as the primary key in
`schema_migrations`).

## Outputs

- `up`: applies every `*.up.sql` file whose numeric version is not yet
  recorded in `schema_migrations`, in ascending filename order; prints a
  count of applied migrations and the resulting current version.
- `down`: applies the `<version>_*.down.sql` file matching the current
  highest applied version, then deletes that version's row from
  `schema_migrations`. If no matching `.down.sql` file exists, the script
  now (G64 fix) prints two `log_error` lines and **exits 1 without deleting
  anything** — see Edge cases.
- `status`: a table of `Version | Status | Description` for every
  discovered `*.up.sql` file.
- `create <name>`: writes two new files under `project/migrations/`.
- `version`: a single integer.

## Side-effects

Creates/modifies the `schema_migrations` table and application schema
inside the target Postgres database; `create` writes two new files to
disk. `down` deletes a version's tracking row **only** after successfully
applying that version's `.down.sql` file; since the G64 fix (see Edge
cases) it no longer deletes the row when no matching `.down.sql` file
exists — it fails closed instead.

## Edge cases

- **Not executable in git — FIXED:** `scripts/migrate.sh` was tracked at
  mode `100644` (not executable), so `make db-migrate-down`
  (`Makefile:226`, `./scripts/migrate.sh down`) hit "Permission denied"
  before any of this script's own fixes (G51/G64) could ever run — a
  reachability defect independent of, and prior to, the script's internal
  logic. Both `scripts/migrate.sh` and `scripts/restore.sh` are now staged
  as `100755` (`git ls-files -s` shows the mode); verified by a round-trip
  mutation (revert to `100644` via `git update-index --chmod=-x`, confirm
  detected, then re-apply `--chmod=+x`).
- **No compose command found:** `log_error` + `exit 1`, same pattern as
  `backup.sh`/`restore.sh`.
- **`schema_migrations` table missing:** created automatically by
  `ensure_migrations_table()` on first use of any subcommand that needs
  it (`up`, `down`, `status`, `version`).
- **The `psql` invocation fails (`up`)** — because the `postgres` service is
  unreachable / `compose exec` cannot run at all, OR (since the G51 fix,
  see below) because any individual SQL statement inside the migration
  errors under `-v ON_ERROR_STOP=1`: the loop stops immediately
  (`log_error` + `exit 1`) — migrations after the failing one in the same
  `up` invocation are never attempted, and the failing migration's own
  partial effect is **not** automatically rolled back by this script
  (whatever the failed SQL script itself did or did not wrap in a
  transaction determines the actual database state; a `BEGIN;`/`COMMIT;`-wrapped
  migration that errors is rolled back in full by Postgres).
- **In-migration SQL errors are caught and fail fast (`up` and `down`) —
  G51 FIXED:** the migration-apply `psql` calls at `migrate.sh` (`up`, in
  `migrate_up`; `down`, in `migrate_down`) now run WITH `-v ON_ERROR_STOP=1`
  and NO LONGER discard `psql`'s stderr (the previous `2>/dev/null` was
  removed). Per `psql`'s documented exit-status contract, a bare `psql`
  exits `0` even when an individual SQL statement inside the script errors
  (bad syntax, a constraint violation, a reference to a table/column that
  doesn't exist); `-v ON_ERROR_STOP=1` changes this so `psql` stops at the
  first statement error and exits **non-zero**, and the real error text is
  now printed to stderr instead of being swallowed. Consequences of the
  fix: (1) `up` — a migration whose SQL genuinely errors partway through
  now makes `psql` exit non-zero, so the `if … then` branch is NOT taken,
  the version is NOT logged `"Applied migration ${version}"`, and NO row is
  inserted into `schema_migrations`; the loop stops with `log_error` +
  `exit 1` and the underlying error is visible on stderr. (2) `down` — a
  rollback whose SQL genuinely errors now makes `psql` exit non-zero, so
  `set -e` aborts the script **before** the `DELETE FROM schema_migrations`
  line runs, leaving the version's row intact rather than silently deleting
  it. In both cases the previous silent `schema_migrations`↔schema desync
  (recorded-as-applied / row-deleted while the SQL had actually failed) can
  no longer occur. This closes **G51** (filed in
  `GAPS_AND_RISKS_REGISTER.md`, §11.4.201). Note this catches errors WITHIN
  a migration's SQL; it does not change the two adjacent edge cases below
  (a missing `.down.sql` file, and a non-conforming migration filename),
  which are independent of `ON_ERROR_STOP`.
- **`down` with current version `0`:** logs a warning ("No migrations to
  rollback") and returns without error.
- **`down` with no matching `.down.sql` file for the current version —
  G64 FIXED (fail-closed, no longer deletes the tracking row):** the
  script used to log a warning and still **remove the version's row from
  `schema_migrations`** without running any rollback SQL, silently
  desyncing the tracking table from reality — the up-migration's schema
  changes were still physically applied to the database, but
  `schema_migrations` would then report that version as not-applied,
  setting up a subsequent `up` run to try to re-apply already-existing
  schema objects (duplicate-table/column errors, or a silent no-op masking
  the fact no rollback ever actually happened). This is now `log_error` +
  `exit 1` with **no** `DELETE` issued — a missing `.down.sql` file (or a
  filename mismatch) for the current version aborts the rollback rather
  than fabricating a "rolled back" state that never happened. Add the
  missing down file (or resolve the schema manually) and retry. This
  closes **G64** (filed in `GAPS_AND_RISKS_REGISTER.md`, §11.4.201) —
  composes with the adjacent G51 fix above (both are "don't touch
  `schema_migrations` unless the corresponding SQL genuinely ran"), but is
  a distinct code path: G51 guards the case where the `.down.sql` file
  exists but its SQL errors; G64 guards the case where no `.down.sql` file
  exists at all.
- **`create` with no `<name>` argument:** `log_error` ("Migration name
  required") + `exit 1`.
- **Migration filename parsing:** the numeric `<version>` is extracted via
  `grep -o '^[0-9]*'` on the basename, and the `<description>` via `sed`
  stripping the leading digits and the `.up.sql`/`.down.sql` suffix and
  converting underscores to spaces — a non-conforming filename (no leading
  digits) yields an empty `<version>`, which will not match any real
  schema version and will be treated as "not yet applied" every time
  `up`/`status` runs.

## Internal behavior

1. `load_env()` / `detect_compose()` — same pattern as `backup.sh`.
2. `exec_sql()` runs a single SQL statement via `compose -f
   deploy/docker-compose.yml exec -T postgres psql ... -t -c "$sql"`.
3. `ensure_migrations_table()` idempotently creates `schema_migrations
   (version BIGINT PRIMARY KEY, applied_at TIMESTAMP DEFAULT
   CURRENT_TIMESTAMP, description TEXT)`.
4. `get_current_version()` returns `MAX(version)` or `0`.
5. `migrate_up()` iterates `*.up.sql` files sorted by filename, skips
   already-applied versions, applies + records each pending one via
   `psql ... < "$migration"` followed by an `INSERT INTO
   schema_migrations`.
6. `migrate_down()` finds the `.down.sql` file for the current version; if
   found, applies it then deletes its `schema_migrations` row; if not
   found, logs two errors and exits 1 without touching
   `schema_migrations` (G64 fix — never delete-on-guess).
7. `migrate_status()` prints the applied/pending table.
8. `create_migration()` scaffolds a timestamp-versioned file pair.
9. `main()` dispatches on the first positional argument (default `up`),
   ensuring `project/migrations/` exists first.

## Dependencies

`bash`, one of `{docker compose, docker-compose, podman-compose}`,
`psql` (inside the `postgres` container), `grep`, `sed`, `date`.

## Cross-references

None directly invoked by other scripts in `project/scripts/`; operates
independently against `project/migrations/`. Compare/contrast with
whatever Go-native migration path exists under `project/internal/db/`
(out of scope for this document — see that package's own code for its
migration mechanism).

## Last verified

2026-07-16, against `project/scripts/migrate.sh` after the G64 fix
(`migrate_down()`'s missing-`.down.sql`-file branch now fails closed with
`log_error` + `exit 1` instead of deleting the `schema_migrations` row),
the earlier G51 fix (`-v ON_ERROR_STOP=1` + surfaced stderr on the
`up`/`down` migration-apply `psql` calls), and the G13 canonical-compose
change (`-f deploy/docker-compose.yml exec -T postgres`). G64 was
regression-tested with a RED (pre-fix)/GREEN (post-fix) shell harness that
stubs `exec_sql`/`get_current_version` and drives the real `main down`
dispatch against a fixture migrations directory containing an up-file with
no matching down-file for the current version; the harness asserts both
the exit code and that no `DELETE FROM schema_migrations` statement is
ever issued.
