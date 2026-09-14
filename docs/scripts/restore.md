# `scripts/restore.sh` — restore from a backup archive

**Revision:** 7
**Last modified:** 2026-07-16T20:15:00Z

## Overview

Restores the HelixKnowledge Skill Graph datastore from a backup archive
previously created by `backup.sh`: shows the archive's metadata, optionally
confirms with the operator, stops services, starts just the database,
restores the SQL dump (dropping and recreating the target database), and
(unless `--db-only`) restores configuration files and the evidence data
directory, then restarts services.

**Note on which compose stack this targets (G13):** like `backup.sh`, this
script now targets the single canonical compose file
`project/deploy/docker-compose.yml` (via an explicit
`compose -f "$INSTALL_DIR/deploy/docker-compose.yml"`), and its datastore
service is `postgres` — the retired root `project/docker-compose.yml` and its
`db` service name are gone (see `research/ops_hardening_design.md` G13 +
`scripts/check_compose_canonical.sh`). It still reads its environment from
`project/.env` (its own `INSTALL_DIR`), not `project/deploy/.env`, and uses
its own inline `load_env`/`detect_compose` helpers rather than sourcing
`_lib.sh`.

## Prerequisites

- One of: Docker with `docker compose`, `docker-compose`, or
  `podman-compose`.
- A valid backup archive produced by `backup.sh`.
- `jq` (optional — used to pretty-print backup metadata; falls back to
  `cat`-ing the raw JSON if absent).

## Usage

```
./restore.sh <backup-file> [--force] [--db-only] [--help|-h]
```

| Argument/Option | Effect |
|---|---|
| `<backup-file>` | Path to the backup archive (`.tar.gz`), required. |
| `--force` | Skip confirmation prompts (drop-and-recreate database, and the "this will overwrite existing data" warning). |
| `--db-only` | Restore only the database (skip configuration + evidence restore). |
| `--help`, `-h` | Print usage and exit 0. |

### Examples

```bash
./restore.sh data/backups/skill-system_v1.0_20240115.tar.gz
./restore.sh backup.tar.gz --force
./restore.sh backup.tar.gz --db-only
```

## Inputs

A backup archive (`.tar.gz`) as produced by `backup.sh`; `project/.env`
(optional, for DB connection defaults).

## Outputs

The target database dropped and recreated from the archive's SQL dump
(maintenance calls connect via `-d postgres`, the replay runs with
`-v ON_ERROR_STOP=1` — see Edge cases); (unless `--db-only`)
`project/.env` and `project/config/config.toml` overwritten from the
archive (the previous `.env` is preserved as
`.env.restore-backup.<epoch>` before being overwritten) and
`project/data/` repopulated from the archive's evidence tarball. Services
are stopped before the restore (aborting the whole restore with `exit 1`
if that stop genuinely fails — G65b fix, see Edge cases) and restarted
afterward. On success, prints a reminder to verify with
`curl http://localhost:8080/health`.

## Side-effects

- Drops and recreates the target Postgres database (**destructive** — this
  is the entire point of a restore, but it means any data in the target
  database that is not in the archive is lost).
- Overwrites `project/.env` and `project/config/config.toml` (in non
  `--db-only` mode), after saving a timestamped copy of the previous
  `.env`.
- Extracts the evidence tarball into `project/data/`, potentially
  overwriting existing evidence files with the same names.
- Stops then restarts the compose stack.

## Edge cases

- **Not executable in git — FIXED:** `scripts/restore.sh` (and
  `scripts/migrate.sh`) were tracked at mode `100644` (not executable),
  breaking any direct invocation (`./scripts/restore.sh ...`,
  `make db-restore`) with "Permission denied" before this script's own
  logic ever ran. Both are now staged as `100755`; verified by a
  round-trip mutation (revert to `100644` via
  `git update-index --chmod=-x`, confirm detected via `git ls-files -s`,
  then re-apply `--chmod=+x`).
- **Invalid/corrupt archive:** `show_backup_info()`, `restore_database()`,
  `restore_config()`, and `restore_evidence()` each attempt their own
  `tar -xzf` extraction of the outer archive; on failure every one of them
  now logs a diagnostic (`"Invalid backup archive"` /
  `"Failed to extract backup archive '<path>' ..."`) and exits 1 before
  any destructive action is taken — see the tar-guard-and-cleanup-trap fix
  below.
- **Tar extraction failures previously unguarded + leaked their temp
  dir — FIXED, then a residual crash+leak on the trap itself found and
  fixed in round 3, then a further quote-injection crash+leak in round
  3's OWN fix found and fixed in round 4:** `restore_database()`, `restore_config()`, and
  `restore_evidence()` each `mktemp -d` a scratch `temp_dir` and used to
  extract the outer archive as a bare `tar -xzf ... 2>/dev/null` with no
  status check — a corrupt/truncated archive aborted the whole function
  via `set -e` with stderr discarded (no diagnostic at all) AND, because
  the abort happened before that function's own `rm -rf "$temp_dir"` line
  ever ran, the scratch directory was never cleaned up (leaked on every
  such failure; §11.4.14). Round 2 addressed this by (1) setting
  `trap 'rm -rf "$temp_dir"' EXIT` immediately after `mktemp -d` in every
  `temp_dir`-creating function (including `show_backup_info()`) and
  (2) explicitly checking the outer `tar -xzf` extraction's exit status.
  **That trap was itself broken — round 2's claim that the directory is
  "removed on ANY exit path" was FALSE, proven live on this host
  (bash 5.2.37) by a Fable-xhigh re-review (F8-residual):** the trap used
  SINGLE quotes (`trap 'rm -rf "$temp_dir"' EXIT`), which defers
  `$temp_dir`'s expansion to the moment the trap actually FIRES. On an
  explicit `exit` this still worked (the function's `local temp_dir` is
  still in scope), but on an **`errexit`-triggered** exit — any of the
  *other* unguarded commands in the same function body failing under
  `set -e` — bash unwinds the function's scope (destroying its `local
  temp_dir`) BEFORE running the EXIT trap, so the deferred-expansion trap
  itself died with `temp_dir: unbound variable` under `set -u`, the
  `rm -rf` never ran, and the temp dir leaked. Proven scenarios: a valid
  outer archive with a truncated inner `skilldb.sql.gz` (fails at the
  `gunzip` step), and non-interactive stdin hitting EOF at the
  drop-and-recreate confirm prompt (`read -p`) — both crashed the trap
  and left the scratch directory behind. **Round-3 fix:** every trap site
  now uses ARM-TIME expansion — `trap "rm -rf '$temp_dir'" EXIT` (double
  quotes) — so the current value of `$temp_dir` is substituted into the
  trap command string the moment the trap is armed (right after
  `mktemp -d`), and the resulting trap carries a literal path with no
  variable reference at all, immune to the function-scope unwind
  (`${temp_dir:-}` alone is insufficient — it stops the crash but
  `rm -rf ""` is then a silent no-op, so the directory still leaks;
  verified both ways on this host). The trap is
  cleared (`trap - EXIT`) at each function's own normal-completion point,
  as before, so it can never fire again after the function returns
  normally. Round 3 additionally guards every remaining unguarded command
  in these functions that could trigger the same errexit-unwind class —
  the `gunzip` decompression step, the drop-and-recreate confirmation
  `read`, the three configuration `cp` calls, the evidence `mkdir -p`,
  and the metadata-fallback `cat` — each now producing an explicit
  `log_error` and a clean `exit 1` instead of a bare, undiagnosed `set -e`
  death (see the corresponding Edge-cases entries below).
  **Round 3's OWN comment claimed "`mktemp -d`'s own output has no
  special characters, so wrapping the already-expanded path in single
  quotes inside the double-quoted trap string is safe" — that claim was
  FALSE, and a round-4 Fable-xhigh re-review (§11.4.194 fresh
  all-scenario pass) found it:** `mktemp -d` honors `$TMPDIR` VERBATIM as
  the scratch-directory prefix, `$TMPDIR` is unconstrained operator
  environment, and `load_env()` `export`s arbitrary `KEY=VALUE` pairs
  straight from a restored `.env` file — so a single quote (or any other
  shell metacharacter) can and does reach `$temp_dir`. Proven live on
  this host (bash 5.2.37): with `TMPDIR="/mnt/mike's disk/tmp"`, round
  3's naive single-quote wrap (`trap "rm -rf '$temp_dir'" EXIT`) produces
  unterminated shell syntax the moment the trap fires ("unexpected EOF
  while looking for matching `'`"), the `rm -rf` never runs, and the temp
  dir leaks — the exact defect class round 3 was supposed to have
  closed. **Round 4 fix:** the temp_dir path is shell-escaped with
  `printf '%q'` at arm time before being substituted into the trap
  command string — `trap "rm -rf $(printf '%q' "$temp_dir")" EXIT` — so
  the invariant this trap relies on is "always `%q`-safe," never
  "`mktemp`'s output happens to be clean." `local temp_dir` and the
  arm-at-mktemp / clear-at-normal-completion (`trap - EXIT`) structure
  are unchanged. Verified with a golden-FALSE-WITH-CARRIER case
  (§11.4.201/§11.4.107(10)): a quote-bearing private `TMPDIR` that leaks
  and crashes on round 3's form and cleans up safely (no leak, no
  wrong-path `rm`) on this `%q`-escaped form, run under both a plain and
  a quote-bearing `TMPDIR`.
- **No metadata found in the archive:** logged as a warning; the restore
  proceeds anyway (metadata is informational, not required for the
  restore itself). When `jq` is unavailable, the raw `backup.json` fallback
  `cat` is now guarded (F8-residual, round 3) — a failure logs
  `log_error` + `exit 1` instead of an undiagnosed `set -e` death.
- **Truncated/corrupt inner database dump — now guarded
  (F8-residual, round 3):** the `gunzip -c "$db_dump" > "$sql_file"`
  decompression step (only reached when the inner dump is `.gz`) was
  previously unguarded; a truncated/corrupt inner `skilldb.sql.gz` inside
  an otherwise-valid outer archive made `gunzip` fail with no diagnostic.
  Now checked explicitly: `log_error "Failed to decompress database dump
  ..."` + `exit 1` before any database is touched.
- **Database exists-check / DROP / CREATE were dead-on-arrival on every
  invocation — FIXED:** the three maintenance `psql` calls in
  `restore_database()` (the exists-check, `DROP DATABASE`, and
  `CREATE DATABASE`) previously omitted `-d` entirely, so `psql` defaulted
  the connection's target database to the **connection user**
  (`$DB_USER`, e.g. `skilluser`) — a database that does not exist by
  default — producing `FATAL: database "skilluser" does not exist` on
  every restore against the canonical `deploy/docker-compose.yml` config
  (confirmed live on PG 17.5). With stderr discarded (`2>/dev/null`) that
  fatal error was invisible, and because the exists-check's assignment is
  a plain command substitution under `set -e`/`pipefail`, a real failure
  killed the whole script right there — **after**
  `stop_services_before_restore()` had already torn the stack down — with
  no diagnostic message at all. All three calls now connect via
  `-d postgres` (Postgres's always-present administrative database, and
  the one you must connect to in order to drop/create another database),
  and the exists-check's result is captured explicitly
  (`|| db_exists_rc=$?`) and checked, aborting with a clear
  `log_error "Could not check whether database '$DB_NAME' exists ..."`
  on ANY exists-check failure — not only the missing-`-d` case — instead
  of a silent `set -e` death.
- **`DROP DATABASE` failure was silently swallowed — FIXED:** the DROP
  call used to be `... 2>/dev/null || true`, discarding a genuine failure
  (e.g. the database still has active connections, or the connect failure
  above) and handing an unknown, unverified database state to the
  following `CREATE DATABASE`. Both maintenance calls now check their exit
  status explicitly and abort (`log_error` + `exit 1`) rather than
  proceeding blind; `CREATE DATABASE` is never reached after a genuine
  `DROP` failure.
- **Target database already exists, no `--force`:** prompts "Drop and
  recreate? [y/N]"; any answer other than `y`/`Y` cancels the restore
  (exit 0) without touching the database. **Non-interactive stdin (EOF) at
  this prompt — now guarded (F8-residual, round 3):** the `read -p` call
  was previously unguarded; a closed/`/dev/null` stdin (a non-interactive
  invocation without `--force`) hits EOF, `read` returns non-zero, and
  this used to abort with no diagnostic. Now checked explicitly —
  `log_error "Failed to read confirmation input (EOF or closed stdin) ..."`
  + `exit 1` — a conservative-safe abort (never treated as an implicit
  "yes" for a destructive drop-and-recreate); use `--force` to skip the
  prompt for non-interactive invocations.
- **No `--force`, general confirmation:** prompts "Continue with restore?
  [y/N]" before stopping services at all; declining exits 0 immediately.
  **Non-interactive stdin (EOF) at this prompt — now guarded (F9, round
  4, §11.4.194/§11.4.201):** this was an unguarded `read -p` with no
  status check, the identical defect class round 3 closed in
  `restore_database()`'s drop-and-recreate prompt (see that fix's
  Edge-cases entry above) but that a round-4 Fable-xhigh re-review found
  had NOT also been applied to `main()`'s own confirmation prompt. A
  closed/`/dev/null` stdin (a non-interactive invocation without
  `--force`) hits EOF, `read` returns non-zero, and this used to abort
  with only the preceding WARN line printed and no diagnostic at all. Now
  checked explicitly — `log_error "Failed to read confirmation input
  (EOF or closed stdin) ..."` + `exit 1` — a conservative-safe abort
  (never treated as an implicit "yes" for a destructive restore); use
  `--force` to skip this prompt entirely for non-interactive invocations.
- **Dump replay reported success even when statements inside it
  failed — FIXED:** the final `psql ... < "$sql_file"` replay call used to
  run without `-v ON_ERROR_STOP=1` and with stderr discarded
  (`2>/dev/null`). Per `psql`'s documented exit-status contract, a bare
  `psql` exits `0` even when an individual statement inside the replayed
  script errors (the failing statement is reported and `psql` moves on to
  the next one) — so this check previously only ever caught a
  connection-level failure, and `"Database restored"` was logged even on a
  partially-failed replay (the same silent-partial-success class already
  closed on `migrate.sh`'s `up`/`down` calls, see `migrate.md`). The
  replay now runs with `-v ON_ERROR_STOP=1` and surfaced stderr, so `psql`
  stops and exits non-zero at the first statement error, and
  `restore_database()` aborts with `log_error` instead of reporting
  success.
- **No config / evidence found in the archive:** each restore step
  (`restore_config`, `restore_evidence`) logs a warning and continues
  rather than failing the whole restore. This is distinct from a missing
  database dump — `restore_database()` logs "Database dump not found in
  backup" and exits 1 instead, since the database is the one artifact
  this script cannot proceed without.
- **Configuration `cp` / evidence `mkdir` failures — now guarded
  (F8-residual, round 3):** in `restore_config()`, the `.env` backup copy,
  the `.env` restore copy, and the `config.toml` restore copy (the latter
  two previously via the `[ -f ... ] && cp ...` shorthand) were each
  unguarded; a `cp` failure (permission denied, disk full, etc.) aborted
  with no diagnostic. In `restore_evidence()`, `mkdir -p
  "$INSTALL_DIR/data"` was likewise unguarded. All four now check their
  exit status explicitly and abort with a diagnosed `log_error` +
  `exit 1` instead of a bare `set -e` death.
- **Corrupt/truncated evidence archive reported as a false
  success — FIXED:** when an `evidence.tar.gz` is found inside the outer
  archive, its extraction into `project/data/` used to be
  `tar -xzf ... 2>/dev/null || true` followed by an unconditional
  `log_success "Evidence data restored"` — a corrupt or truncated evidence
  tarball was reported as a success regardless of whether the extraction
  actually happened. Evidence remains non-critical to the restore as a
  whole (a failed extraction is a `log_warn`, not an abort — same policy
  as "no evidence found" above), but the outcome is now reported honestly:
  the extraction's exit status is checked, and only a genuine success logs
  `"Evidence data restored"`; a failure logs a warning explaining the
  archive may be corrupt/truncated and that evidence data was NOT
  restored, then the restore continues.
- **`stop.sh --compose` — G65b FIXED (real failures no longer swallowed):**
  this step is now its own function, `stop_services_before_restore()`,
  called as `if ! "$SCRIPT_DIR/stop.sh" --compose; then log_error ...;
  exit 1; fi` — no more `2>/dev/null || true`. Previously the call was
  `"$SCRIPT_DIR/stop.sh" --compose 2>/dev/null || true`: the `|| true`
  discarded stop.sh's real exit code (`2>/dev/null` additionally hid its
  stderr), so ANY stop.sh failure — not only the case where nothing was
  running yet (`compose down` is already idempotent and exits 0 for that)
  — was silently discarded and the restore proceeded as if the stop had
  succeeded, `set -euo pipefail` never triggering because the `|| true`
  neutralized the non-zero status. Composes with the sibling **G65a** fix
  (`stop.md`'s "Edge cases") — before G65a, `--compose` was ALSO rejected
  by `stop.sh` as an unknown argument (exit 2), a second independent way
  this same step never actually stopped anything; both halves of G65 are
  now fixed. With both fixes: `stop.sh --compose` is recognized and
  genuinely tears the stack down (G65a), and if it ever exits non-zero for
  a real reason (e.g. a genuine `compose down` failure), `restore.sh` now
  aborts (`log_error` + `exit 1`) instead of silently continuing (G65b) —
  proven by a harness that stubs a failing `stop.sh` and asserts
  `stop_services_before_restore` propagates the failure rather than
  reaching past it. This closes **G65** (filed in
  `GAPS_AND_RISKS_REGISTER.md`, §11.4.201).
- **Postgres readiness wait falls through regardless of outcome —
  FIXED:** after starting just the `postgres` service, the script sleeps
  5s then polls `pg_isready` up to 30 times (2s apart, ~65s total) via the
  extracted `wait_for_database_ready()` function. This document previously
  claimed that if the retry loop exhausted without success "the subsequent
  `psql` restore command will simply fail and surface its own error" —
  **that claim was false**: `restore_database()`'s own maintenance calls
  connect independently and, against a database that only just finished
  starting, can succeed on a connection that happens to work even while
  the service is not yet fully healthy, or fail in ways that are hard to
  distinguish from the unrelated failures above; either way, proceeding
  past an exhausted readiness budget was never guaranteed to "just fail
  cleanly" — it silently attempted the restore regardless. The loop's
  outcome is now checked explicitly: `main()` calls
  `if ! wait_for_database_ready; then log_error "Postgres did not become
  ready ..."; exit 1; fi` **before** calling `restore_database()` at all,
  so an exhausted retry budget aborts the restore with a clear diagnostic
  instead of proceeding blind. The retry count / poll interval / initial
  settle sleep are env-overridable
  (`RESTORE_PG_READY_RETRIES` / `RESTORE_PG_READY_INTERVAL_SECONDS` /
  `RESTORE_PG_READY_INITIAL_DELAY_SECONDS`, all defaulting to the
  unchanged production values) purely so tests can exercise the
  exhausted-retries path without waiting out the real ~65s budget.

## Internal behavior

1. `load_env()` / `detect_compose()` — same as `backup.sh`.
2. `main()` parses `<backup-file>` (first non-flag argument) plus
   `--force`/`--db-only`/`--help`.
3. `show_backup_info()` — extracts (guarded, EXIT-trapped `temp_dir`) and
   prints `backup.json` metadata (via `jq` if available).
4. Confirmation prompt (skipped with `--force`).
5. `stop_services_before_restore()` stops services (`stop.sh --compose`)
   and **aborts the restore (`exit 1`) if that genuinely fails** (G65b
   fix — no longer best-effort/swallowed); starts just `postgres`, then
   `wait_for_database_ready()` polls for Postgres readiness (bounded retry
   loop, env-overridable for tests) — `main()` now aborts with
   `log_error` + `exit 1` if that budget is exhausted, rather than
   falling through regardless.
6. `restore_database()` — extracts + decompresses the SQL dump (guarded,
   EXIT-trapped `temp_dir`); checks whether the target database exists via
   a `-d postgres` maintenance connection (explicit rc-checked); drops and
   recreates it (`-d postgres`, each step's exit status checked); replays
   the dump with `-v ON_ERROR_STOP=1` and surfaced stderr, aborting on any
   replay failure instead of reporting success.
7. Unless `--db-only`: `restore_config()` (guarded/trapped extraction;
   backs up then overwrites `.env` / `config.toml`) and
   `restore_evidence()` (guarded/trapped outer extraction; extracts the
   evidence tarball into `project/data/`, now reporting a corrupt/
   truncated evidence archive honestly instead of a false success).
8. Restarts services via `start.sh`; prints a health-check reminder.

## Dependencies

`bash`, one of `{docker compose, docker-compose, podman-compose}`, `tar`,
`gunzip`, `jq` (optional), `stop.sh`, `start.sh`.

## Cross-references

`backup.sh` (produces the archives this script consumes), `stop.sh`,
`start.sh` (both invoked mid-script), `migrate.sh` (shares the
`-v ON_ERROR_STOP=1` + surfaced-stderr idiom this round applied to the
dump-replay `psql` call — see `migrate.md`'s G51 fix).

## Last verified

2026-07-16, against `project/scripts/restore.sh` after this round's fixes:
(F1) dump replay now runs with `-v ON_ERROR_STOP=1` and surfaced stderr
instead of silently reporting success on a partially-failed replay; (F2)
the exists-check/DROP/CREATE maintenance calls now connect via
`-d postgres` (fixing a `FATAL: database "<user>" does not exist`
dead-on-arrival confirmed live on PG 17.5) and the exists-check's own
failure is captured and diagnosed explicitly instead of dying silently
under `set -e`; (F3) a genuine `DROP DATABASE` failure now aborts instead
of being swallowed and letting `CREATE DATABASE` proceed regardless; (F4)
`wait_for_database_ready()` was extracted from `main()` and its outcome is
now checked, aborting on an exhausted retry budget instead of falling
through (this document's prior Edge-cases claim that "the subsequent
`psql` restore command will simply fail and surface its own error" was
itself corrected — see Edge cases); (F5) a corrupt/truncated evidence
archive is now reported honestly (`log_warn`) instead of an unconditional
`log_success`; (F7) `scripts/migrate.sh` + `scripts/restore.sh` are now
staged executable (`100755`) in git, fixing a reachability defect where
`make db-migrate-down` hit "Permission denied" before any of this
script's logic could run; (F8) every `temp_dir`-creating function now
guards its outer `tar -xzf` extraction with an explicit status check and
traps `EXIT` to remove `temp_dir` on any exit path, closing both a
silent-death and a leaked-scratch-directory class. Each testable fix was
regression-tested with a RED (pre-fix mutant reproduces the bug)/GREEN
(current script is safe) harness driving fake `docker compose`/`psql`
binaries (`docker exec ... psql`/`pg_isready` intercepted, behavior
switched via `HARNESS_*` env vars) plus real `tar` against both a valid
and a deliberately-corrupt fixture archive; F7 was verified by a
git-mode round-trip (`git ls-files -s` / `update-index --chmod`). All 19
harness assertions pass; the harness lives outside the tracked tree.
Also confirmed unregressed against this round: the earlier G65b fix (the
pre-restore stop step is `stop_services_before_restore()`, which
propagates a genuine `stop.sh --compose` failure as `exit 1` instead of
swallowing it via `2>/dev/null || true`), the sibling G65a fix in
`stop.sh` itself (`stop.md`), and the earlier G13 canonical-compose change
(compose calls target `-f deploy/docker-compose.yml`; datastore service
`postgres`). G65b was regression-tested with a harness that stubs a
genuinely-failing `stop.sh` and asserts `stop_services_before_restore`
exits non-zero without reaching past the stop step (RED against the
pre-fix inline `|| true` line, GREEN against the fixed function).

**Round 3 (2026-07-16, F8-residual):** a Fable-xhigh independent re-review
(§11.4.209) found ONE BLOCKING finding against round 2's F8 fix, proven
live on this host (bash 5.2.37): the EXIT trap's single-quoted form
(`trap 'rm -rf "$temp_dir"' EXIT`) crashes with `temp_dir: unbound
variable` — and therefore never removes `temp_dir` — the moment ANY
unguarded command in the same function body fails under `set -e` (an
`errexit`-triggered exit unwinds the function's `local temp_dir` out of
scope BEFORE the EXIT trap runs; an *explicit* `exit 1` does not have this
problem, only bash's own automatic `errexit` abort does). Round 2's own
claim that the directory is removed "on ANY exit path" was therefore
false for this specific case. Fixed by re-arming all four trap sites with
ARM-TIME expansion (`trap "rm -rf '$temp_dir'" EXIT`, double-quoted, so
the path is a literal string in the trap command with no variable lookup
at fire time — `${temp_dir:-}` alone was tested and found insufficient,
it stops the crash but `rm -rf ""` then silently no-ops, still leaking).
Round 3 additionally guards every other unguarded command in these four
functions that could still trigger the same class: the `gunzip`
decompression step, the drop-and-recreate confirmation `read`, the three
configuration `cp` calls, the evidence `mkdir -p`, and the
metadata-fallback `cat` — each now aborting with an explicit `log_error`
+ `exit 1` instead of an undiagnosed `set -e` death. Verified with a new,
dedicated harness (`run_tests_f8_residual.sh`, 8/8 assertions, run 3×
for determinism, byte-identical tracked-file sha256 before/after every
mutation round): (F8R-1) a genuinely truncated inner `skilldb.sql.gz`
inside a valid outer archive — GREEN on the current script (no `unbound
variable` on stderr, diagnosed abort, private `TMPDIR` left empty),
GREEN even when *only* the trap is reverted (proving defense-in-depth:
the gunzip guard's own explicit exit does not trigger the crash class
either), RED when the trap AND the gunzip guard are both reverted to
their exact pre-round-3 form (`unbound variable` on stderr, `TMPDIR`
left non-empty); (F8R-2) the identical RED/GREEN/defense-in-depth triad
for non-interactive stdin (EOF) at the drop-and-recreate confirmation
prompt. The full round-2 harness (`run_tests.sh`, 19/19 — F1/F2/
F2-guard/F3/F4/F5/F8×2/F7) and the standalone G65b harness
(`test_restore_stop_exit_propagation.sh`) were both re-run from a clean
state (no stale mutant files) against the round-3 script and remain
fully green — including two of round 2's own F8 mutations
(`F8_restore_database_revert` / `F8_restore_evidence_revert`) whose
search text had to be updated in this round to match the new trap
comment, since — before that update — they silently failed to match and
would have reused stale mutant artefacts left on disk from round 2's own
verification run rather than genuinely regenerating against the current
source (a stale-mutation-residue risk in the harness itself, caught and
fixed as part of this round's re-verification). The cp/mkdir guards'
happy paths (successful configuration restore including the pre-existing
`.env` timestamped backup; successful evidence extraction into
`project/data/`) were additionally confirmed via standalone functional
runs. `bash -n` / `sh -n` clean; zero mutation-residue markers
(`MUTATED for paired` / `// always pass` / `# MUTATION` / `_mutated_`)
anywhere in the tracked script. All harness scaffolding
(`restore_harness/`, `ops_fix_harness/`) lives outside the tracked tree.
