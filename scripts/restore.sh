#!/bin/bash
# =============================================================================
# HelixKnowledge Skill Graph System - Restore Script
# =============================================================================
# Usage: ./restore.sh <backup-file> [--force] [--db-only]
# Restores from a backup archive created by backup.sh
# =============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_DIR="$(dirname "$SCRIPT_DIR")"
SERVICE_NAME="skill-system"
# G13: the single canonical compose file + its datastore service name. Compose
# calls target this file explicitly (-f); the canonical datastore service is
# `postgres`, never the retired root file's `db`.
# See research/ops_hardening_design.md (G13) + scripts/check_compose_canonical.sh.
COMPOSE_FILE="$INSTALL_DIR/deploy/docker-compose.yml"
DB_SERVICE="postgres"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info() { echo -e "${BLUE}[INFO]${NC} $1"; }
log_success() { echo -e "${GREEN}[OK]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

# Defaults
FORCE=false
DB_ONLY=false

# Load environment
load_env() {
    if [ -f "$INSTALL_DIR/.env" ]; then
        export $(grep -v '^#' "$INSTALL_DIR/.env" | xargs 2>/dev/null || true)
    fi
    
    DB_HOST="${DB_HOST:-db}"
    DB_PORT="${DB_PORT:-5432}"
    DB_NAME="${DB_NAME:-skilldb}"
    DB_USER="${DB_USER:-skilluser}"
    DB_PASSWORD="${DB_PASSWORD:-skillpassword}"
}

# Detect compose command
detect_compose() {
    if docker compose version &> /dev/null; then
        COMPOSE_CMD="docker compose"
    elif command -v docker-compose &> /dev/null; then
        COMPOSE_CMD="docker-compose"
    elif command -v podman-compose &> /dev/null; then
        COMPOSE_CMD="podman-compose"
    else
        log_error "No compose command found"
        exit 1
    fi
}

# Show backup info
show_backup_info() {
    local archive="$1"
    local temp_dir
    temp_dir=$(mktemp -d)
    # F8 (§11.4.14/§11.4.201) + F8-residual (round 3, Fable-xhigh review) +
    # F8-residual-2 (round 4, Fable-xhigh re-review #3, §11.4.194): every
    # function in this script that creates a temp_dir traps EXIT to remove
    # it - closing the general "any unguarded command failure under
    # `set -e` leaks the temp dir" class, not just the specific tar calls
    # originally flagged. On an `errexit`-triggered exit, bash unwinds the
    # function's scope (destroying its `local temp_dir`) BEFORE running the
    # EXIT trap, so a trap that defers `$temp_dir` expansion to fire-time
    # (`trap 'rm -rf "$temp_dir"' EXIT`) dies under `set -u` with
    # "temp_dir: unbound variable" the moment ANY unguarded command in the
    # function body fails - the `rm -rf` never runs and the temp dir leaks
    # (proven live in round 3: a truncated inner archive at the gunzip
    # step, or an EOF at the drop-and-recreate confirm prompt, both crashed
    # that form and left the temp dir behind).
    #
    # Round 3's fix armed the trap with the temp_dir path already EXPANDED
    # into the command string at arm-time, single-quoted
    # (`trap "rm -rf '$temp_dir'" EXIT`). Round 3's OWN comment claimed
    # "`mktemp -d`'s own output has no special characters" - that claim was
    # FALSE in general, and a round-4 Fable-xhigh re-review (§11.4.194
    # fresh all-scenario pass) found it: `mktemp -d` honors `$TMPDIR`
    # VERBATIM as the scratch-directory prefix, `$TMPDIR` is unconstrained
    # operator environment, and `load_env()` (above) `export`s arbitrary
    # `KEY=VALUE` pairs straight from a restored `.env` file - so a
    # single-quote (or worse, an even shell-metacharacter) can and does
    # reach `$temp_dir`. Proven live on this host (bash 5.2.37): with
    # `TMPDIR="/mnt/mike's disk/tmp"`, round 3's naive single-quote wrap
    # produces unterminated shell syntax the moment the trap fires
    # ("unexpected EOF while looking for matching `'"), the `rm -rf` never
    # runs, and the temp dir leaks - the exact defect class round 3 was
    # supposed to have closed. A TWO-single-quote `TMPDIR` is worse: it
    # risks the trap running `rm -rf` against a mangled/wrong path instead
    # of merely failing loudly.
    #
    # Round-4 fix: the temp_dir path is shell-escaped with `printf '%q'` at
    # arm time before being substituted into the trap command string -
    # `%q` produces a token that bash's trap re-parse always treats as one
    # literal argument regardless of what characters (quotes, spaces, `$`,
    # backticks, ...) `$temp_dir` contains, so the invariant this trap
    # relies on is "always `%q`-safe," never "mktemp's output happens to be
    # clean." `local temp_dir` and the arm-at-mktemp /
    # clear-at-normal-completion (`trap - EXIT`) structure are unchanged.
    # Verified with a golden-FALSE-WITH-CARRIER case (§11.4.201/
    # §11.4.107(10)): a quote-bearing private `TMPDIR` that leaks + crashes
    # on round 3's form and cleans up safely (no leak, no wrong-path `rm`)
    # on this `%q`-escaped form, run under both a plain and a
    # quote-bearing `TMPDIR`.
    trap "rm -rf -- $(printf '%q' "$temp_dir")" EXIT

    tar -xzf "$archive" -C "$temp_dir" 2>/dev/null || {
        log_error "Invalid backup archive"
        exit 1
    }

    local metadata_file
    metadata_file=$(find "$temp_dir" -name "backup.json" 2>/dev/null | head -1)
    
    if [ -n "$metadata_file" ] && [ -f "$metadata_file" ]; then
        echo -e "\n${BLUE}Backup Information${NC}"
        echo "═══════════════════════════════════════════════════════════════"
        
        if command -v jq &> /dev/null; then
            echo "  Name:        $(jq -r '.name' "$metadata_file")"
            echo "  Type:        $(jq -r '.type' "$metadata_file")"
            echo "  Created:     $(jq -r '.created_at' "$metadata_file")"
            echo "  Version:     $(jq -r '.version' "$metadata_file")"
            echo "  Hostname:    $(jq -r '.hostname' "$metadata_file")"
            echo ""
            echo "  Contents:"
            echo "    Database:      $(jq -r '.contents.database' "$metadata_file")"
            echo "    Config:        $(jq -r '.contents.configuration' "$metadata_file")"
            echo "    Evidence:      $(jq -r '.contents.evidence' "$metadata_file")"
        else
            # F8-residual (round 3): guarded - was an unguarded `cat` whose
            # failure (e.g. the file vanishing between the `-f` check and
            # this read, or a permission error) would previously have
            # crashed the single-quoted EXIT trap above with an
            # "unbound variable" error instead of cleaning up temp_dir; the
            # now-arm-time-expanded trap survives this class either way,
            # but the diagnostic is added for honesty.
            if ! cat "$metadata_file"; then
                log_error "Failed to read backup metadata file '$metadata_file' (cat exited non-zero)."
                exit 1
            fi
        fi
    else
        log_warn "No metadata found in backup"
    fi

    rm -rf -- "$temp_dir"
    trap - EXIT
}

# Restore database
restore_database() {
    local archive="$1"
    local temp_dir
    temp_dir=$(mktemp -d)
    # F8-residual-2 (round 4): %q-escaped arm-time expansion - see
    # show_backup_info()'s comment above for why round 3's naive
    # single-quote wrap breaks (and leaks) on a quote-bearing TMPDIR, and
    # why a single-quoted deferred-expansion trap would ALSO crash+leak on
    # an errexit-triggered unwind (e.g. the gunzip guard or the
    # drop-and-recreate confirm-read guard below failing).
    trap "rm -rf -- $(printf '%q' "$temp_dir")" EXIT

    log_info "Extracting database backup..."
    # F8 (§11.4.201): this extraction used to be unguarded
    # (`tar -xzf ... 2>/dev/null` with no status check) - a mid-restore
    # extraction failure (corrupt/truncated archive) aborted via `set -e`
    # with stderr discarded and no diagnostic at all. Now checked explicitly
    # with stderr surfaced; the EXIT trap above still cleans up temp_dir on
    # this early exit.
    if ! tar -xzf "$archive" -C "$temp_dir"; then
        log_error "Failed to extract backup archive '$archive' (tar exited non-zero - see its output above). Aborting before touching the target database."
        exit 1
    fi

    local db_dump
    db_dump=$(find "$temp_dir" -name "skilldb.sql.gz" -o -name "skilldb.sql" 2>/dev/null | head -1)

    if [ -z "$db_dump" ]; then
        log_error "Database dump not found in backup"
        exit 1
    fi

    log_info "Database dump: $db_dump"

    # Decompress if needed
    #
    # F8-residual (round 3, §11.4.201): guarded - was an unguarded
    # `gunzip -c ... > ...` with no status check. A truncated/corrupt inner
    # `skilldb.sql.gz` (a real, PROVEN-live scenario: a valid outer archive
    # whose inner database dump is truncated) made gunzip fail, which under
    # `set -e` aborted the function immediately with NO diagnostic - and,
    # before the F8-residual trap fix above, crashed the cleanup trap itself
    # with "temp_dir: unbound variable" instead of removing temp_dir.
    local sql_file="$db_dump"
    if [[ "$db_dump" == *.gz ]]; then
        sql_file="${db_dump%.gz}"
        if ! gunzip -c "$db_dump" > "$sql_file"; then
            log_error "Failed to decompress database dump '$db_dump' (gunzip exited non-zero - the archive's inner database dump may be corrupt or truncated). Aborting before touching the target database."
            exit 1
        fi
    fi

    # Check if database exists and prompt
    #
    # F2 (§11.4.201/§9.2, restore dead-on-arrival): every maintenance call
    # below (this exists-check, the DROP, and the CREATE) now connects with
    # `-d postgres` - Postgres's always-present administrative database, and
    # the ONE you must connect to in order to drop/create another database
    # (you cannot drop or create the database you are currently connected
    # to). These three calls previously omitted -d entirely, so psql
    # defaulted the connection's target database to the CONNECTION USER
    # ("$DB_USER", e.g. "skilluser") - a database that does not exist by
    # default - producing `FATAL: database "skilluser" does not exist` on
    # every invocation against the canonical deploy/docker-compose.yml
    # config (confirmed live on PG 17.5). With stderr previously discarded
    # (2>/dev/null) that fatal error was invisible, and because the
    # assignment below is a plain command substitution under `set -e`/
    # `pipefail` (not the `local x=$(...)` combined form, which would mask
    # the exit status entirely), a real failure used to kill the whole
    # script right here - AFTER stop_services_before_restore had already
    # torn the stack down - with no diagnostic message at all: restore was
    # dead-on-arrival for every invocation. The explicit rc capture below
    # additionally makes ANY exists-check failure (not only the
    # missing-`-d` case) abort with a clear, diagnosed error instead of a
    # silent `set -e` death.
    local db_exists_rc=0
    local db_exists
    db_exists=$(cd "$INSTALL_DIR" && $COMPOSE_CMD -f "$COMPOSE_FILE" exec -T "$DB_SERVICE" psql -U "$DB_USER" -d postgres -tc "SELECT 1 FROM pg_database WHERE datname = '$DB_NAME';" | tr -d '[:space:]') || db_exists_rc=$?

    if [ "$db_exists_rc" -ne 0 ]; then
        log_error "Could not check whether database '$DB_NAME' exists (psql -d postgres exited $db_exists_rc - see its output above). Aborting rather than proceeding without knowing the target database's state."
        exit 1
    fi

    if [ "$db_exists" = "1" ] && [ "$FORCE" = false ]; then
        echo ""
        log_warn "Database '$DB_NAME' already exists"
        # F8-residual (round 3, §11.4.201): guarded - was an unguarded
        # `read -p` with no status check. Non-interactive stdin (closed or
        # `/dev/null`, e.g. a script/cron invocation without --force) hits
        # EOF, `read` returns non-zero, and under `set -e` that aborted the
        # function immediately with NO diagnostic - and, before the
        # F8-residual trap fix above, crashed the cleanup trap itself with
        # "temp_dir: unbound variable" instead of removing temp_dir. On EOF
        # we abort rather than guess an answer (§11.4.6/§11.4.101 -
        # conservative-safe default: never treat "could not ask" as "yes"
        # for a destructive drop-and-recreate); use --force to skip this
        # prompt entirely for non-interactive invocations.
        if ! read -p "Drop and recreate? [y/N]: " confirm; then
            log_error "Failed to read confirmation input (EOF or closed stdin) for the drop-and-recreate prompt on database '$DB_NAME'. Aborting rather than proceeding without an explicit answer. Use --force to skip this prompt for non-interactive invocations."
            exit 1
        fi
        if [[ ! "$confirm" =~ ^[Yy]$ ]]; then
            log_info "Restore cancelled"
            exit 0
        fi
    fi

    # Drop and recreate database
    #
    # F3 (§11.4.201/§9.2, DROP failure swallowed): the DROP used to be
    # `... 2>/dev/null || true`, discarding a genuine failure (e.g. the
    # database still has active connections, or the F2 connect failure
    # above) and handing an unknown, unverified database state to the
    # following CREATE. Both maintenance calls now check status explicitly
    # and abort with a diagnosed error rather than proceeding blind.
    log_info "Recreating database..."
    cd "$INSTALL_DIR"
    if ! $COMPOSE_CMD -f "$COMPOSE_FILE" exec -T "$DB_SERVICE" psql -U "$DB_USER" -d postgres -c "DROP DATABASE IF EXISTS $DB_NAME;"; then
        log_error "Failed to drop existing database '$DB_NAME' (psql exited non-zero - see its output above, e.g. active connections still attached). Aborting rather than proceeding to CREATE DATABASE against an unknown pre-existing state."
        exit 1
    fi
    if ! $COMPOSE_CMD -f "$COMPOSE_FILE" exec -T "$DB_SERVICE" psql -U "$DB_USER" -d postgres -c "CREATE DATABASE $DB_NAME;"; then
        log_error "Failed to create database '$DB_NAME' (psql exited non-zero - see its output above)."
        exit 1
    fi

    # Restore from dump
    #
    # F1 (§11.4.201/§9.2, dump replay reports success on partial restore):
    # `-v ON_ERROR_STOP=1` + surfaced stderr (no more `2>/dev/null`) - without
    # ON_ERROR_STOP, psql exits 0 even when individual statements in the
    # replayed dump fail (each failing statement only prints its own error to
    # stderr and psql moves on to the next one), so this check previously
    # only ever caught a connection-level failure and "Database restored"
    # was logged even on a partially-failed replay. Same fix class already
    # applied to migrate.sh's up/down psql invocations.
    log_info "Restoring database (this may take a while)..."
    if ! $COMPOSE_CMD -f "$COMPOSE_FILE" exec -T "$DB_SERVICE" psql -v ON_ERROR_STOP=1 -U "$DB_USER" -d "$DB_NAME" < "$sql_file"; then
        log_error "Database restore failed (psql exited non-zero - see its output above for the failing statement)."
        exit 1
    fi

    log_success "Database restored"
    rm -rf -- "$temp_dir"
    trap - EXIT
}

# Restore configuration
restore_config() {
    local archive="$1"
    local temp_dir
    temp_dir=$(mktemp -d)
    # F8-residual-2 (round 4): %q-escaped arm-time expansion - see
    # show_backup_info()'s comment above for why round 3's naive
    # single-quote wrap breaks (and leaks) on a quote-bearing TMPDIR, and
    # why a single-quoted deferred-expansion trap would ALSO crash+leak on
    # an errexit-triggered unwind (e.g. one of the cp guards below failing).
    trap "rm -rf -- $(printf '%q' "$temp_dir")" EXIT

    log_info "Restoring configuration..."
    # F8 (§11.4.201): guarded (was unguarded `2>/dev/null` with no status
    # check - a mid-restore extraction failure aborted via `set -e` with no
    # message; the EXIT trap above cleans up temp_dir on this early exit).
    if ! tar -xzf "$archive" -C "$temp_dir"; then
        log_error "Failed to extract backup archive '$archive' while restoring configuration (tar exited non-zero - see its output above)."
        exit 1
    fi

    local config_dir
    config_dir=$(find "$temp_dir" -type d -name "config" 2>/dev/null | head -1)
    
    if [ -n "$config_dir" ] && [ -d "$config_dir" ]; then
        # F8-residual (round 3, §11.4.201): all three `cp` calls below are
        # now guarded - were unguarded (the last two via the
        # `[ -f ... ] && cp ...` shorthand, whose overall exit status is
        # `cp`'s own on failure). An unguarded `cp` failure (permission
        # denied, disk full, etc.) previously aborted via `set -e` with no
        # diagnostic - and, before the F8-residual trap fix above, crashed
        # the cleanup trap itself with "temp_dir: unbound variable" instead
        # of removing temp_dir.
        # Backup current config
        if [ -f "$INSTALL_DIR/.env" ]; then
            if ! cp "$INSTALL_DIR/.env" "$INSTALL_DIR/.env.restore-backup.$(date +%s)"; then
                log_error "Failed to back up the current .env before restoring configuration (cp exited non-zero). Aborting before overwriting .env from the archive."
                exit 1
            fi
        fi

        # Restore files
        if [ -f "$config_dir/.env" ]; then
            if ! cp "$config_dir/.env" "$INSTALL_DIR/"; then
                log_error "Failed to restore .env from the backup archive (cp exited non-zero)."
                exit 1
            fi
        fi
        if [ -f "$config_dir/config.toml" ]; then
            if ! cp "$config_dir/config.toml" "$INSTALL_DIR/config/"; then
                log_error "Failed to restore config.toml from the backup archive (cp exited non-zero)."
                exit 1
            fi
        fi

        log_success "Configuration restored"
    else
        log_warn "No configuration found in backup"
    fi

    rm -rf -- "$temp_dir"
    trap - EXIT
}

# Restore evidence
restore_evidence() {
    local archive="$1"
    local temp_dir
    temp_dir=$(mktemp -d)
    # F8-residual-2 (round 4): %q-escaped arm-time expansion - see
    # show_backup_info()'s comment above for why round 3's naive
    # single-quote wrap breaks (and leaks) on a quote-bearing TMPDIR, and
    # why a single-quoted deferred-expansion trap would ALSO crash+leak on
    # an errexit-triggered unwind (e.g. the mkdir guard below failing).
    trap "rm -rf -- $(printf '%q' "$temp_dir")" EXIT

    log_info "Restoring evidence data..."
    # F8 (§11.4.201): guarded (was unguarded `2>/dev/null` with no status
    # check - a mid-restore extraction failure aborted via `set -e` with no
    # message; the EXIT trap above cleans up temp_dir on this early exit).
    if ! tar -xzf "$archive" -C "$temp_dir"; then
        log_error "Failed to extract backup archive '$archive' while restoring evidence data (tar exited non-zero - see its output above)."
        exit 1
    fi

    local evidence_archive
    evidence_archive=$(find "$temp_dir" -name "evidence.tar.gz" 2>/dev/null | head -1)

    if [ -n "$evidence_archive" ] && [ -f "$evidence_archive" ]; then
        # F8-residual (round 3, §11.4.201): guarded - was an unguarded
        # `mkdir -p` with no status check. A failure (e.g. permission
        # denied) previously aborted via `set -e` with no diagnostic - and,
        # before the F8-residual trap fix above, crashed the cleanup trap
        # itself with "temp_dir: unbound variable" instead of removing
        # temp_dir.
        if ! mkdir -p "$INSTALL_DIR/data"; then
            log_error "Failed to create '$INSTALL_DIR/data' before restoring evidence data (mkdir exited non-zero)."
            exit 1
        fi
        # F5 (§11.4.201): a corrupt/truncated evidence.tar.gz used to be
        # swallowed (`2>/dev/null || true`) and "Evidence data restored"
        # logged unconditionally regardless of whether the extraction
        # actually succeeded. Evidence is non-critical to the restore as a
        # whole (same policy as "no evidence found" below - a warning, not
        # an abort) but the outcome MUST be reported honestly, never as a
        # false success.
        if tar -xzf "$evidence_archive" -C "$INSTALL_DIR/data"; then
            log_success "Evidence data restored"
        else
            log_warn "Evidence archive found but extraction failed (tar exited non-zero - the archive may be corrupt or truncated); evidence data was NOT restored. Continuing with the rest of the restore."
        fi
    else
        log_warn "No evidence data found in backup"
    fi

    rm -rf -- "$temp_dir"
    trap - EXIT
}

# Stop any running services before restore.
#
# G65 (GAPS_AND_RISKS_REGISTER.md, §11.4.201): this used to be
# `"$SCRIPT_DIR/stop.sh" --compose 2>/dev/null || true` inline in main() -
# the `|| true` swallowed stop.sh's real exit code (and `2>/dev/null` hid
# its stderr), so ANY stop.sh failure - not just "nothing was running yet"
# (compose down is already idempotent and exits 0 for that case) - was
# silently discarded and restore.sh reported success while a service that
# failed to stop may still be running when the next step tries to bring the
# database container back up. Fail closed: a genuine stop failure MUST
# abort the restore instead of being masked as if it never happened.
stop_services_before_restore() {
    log_info "Stopping services..."
    if ! "$SCRIPT_DIR/stop.sh" --compose; then
        log_error "Failed to stop running services before restore (scripts/stop.sh --compose exited non-zero - see its output above). Aborting rather than proceeding while the stack may still be running."
        exit 1
    fi
}

# Wait for Postgres to accept connections after starting it, bounded by a
# retry budget. Returns 0 once ready, 1 if the retry budget is exhausted
# without ever observing readiness.
#
# F4 (§11.4.201): this loop used to be inlined in main() and fell through
# unconditionally once `retries` reached 0, regardless of whether readiness
# was ever actually reached - proceeding straight into restore_database()
# against a database that might not yet be accepting connections. Extracted
# into its own function so the caller can (and now does, in main()) treat a
# retries-exhausted timeout as fatal instead of silently continuing. Retry
# count / interval / the initial settle sleep are env-overridable (defaults
# unchanged: 5s initial sleep + 30 * 2s = ~65s total, matching the prior
# behavior) purely so tests can exercise the exhausted-retries path quickly
# without waiting out the real production budget.
wait_for_database_ready() {
    local retries="${RESTORE_PG_READY_RETRIES:-30}"
    local interval="${RESTORE_PG_READY_INTERVAL_SECONDS:-2}"

    while [ "$retries" -gt 0 ]; do
        if $COMPOSE_CMD -f "$COMPOSE_FILE" exec -T "$DB_SERVICE" pg_isready -U "$DB_USER" -d "$DB_NAME" &> /dev/null; then
            return 0
        fi
        retries=$((retries - 1))
        sleep "$interval"
    done

    return 1
}

# Main
main() {
    load_env
    detect_compose
    
    local backup_file=""
    
    # Parse arguments
    while [ $# -gt 0 ]; do
        case "$1" in
            --force)
                FORCE=true
                shift
                ;;
            --db-only)
                DB_ONLY=true
                shift
                ;;
            --help|-h)
                echo "Usage: $0 <backup-file> [options]"
                echo ""
                echo "Arguments:"
                echo "  <backup-file>  Path to backup archive (.tar.gz)"
                echo ""
                echo "Options:"
                echo "  --force     Skip confirmation prompts"
                echo "  --db-only   Restore only database"
                echo "  --help      Show this help"
                echo ""
                echo "Examples:"
                echo "  $0 data/backups/skill-system_v1.0_20240115.tar.gz"
                echo "  $0 backup.tar.gz --force"
                echo "  $0 backup.tar.gz --db-only"
                exit 0
                ;;
            -*)
                log_error "Unknown option: $1"
                exit 1
                ;;
            *)
                if [ -z "$backup_file" ]; then
                    backup_file="$1"
                fi
                shift
                ;;
        esac
    done
    
    if [ -z "$backup_file" ]; then
        log_error "Backup file required"
        echo "Run '$0 --help' for usage"
        exit 1
    fi
    
    if [ ! -f "$backup_file" ]; then
        log_error "Backup file not found: $backup_file"
        exit 1
    fi
    
    # Show backup info
    show_backup_info "$backup_file"
    
    # Confirm restore
    #
    # F9 (round 4, §11.4.194/§11.4.201): guarded - this was an unguarded
    # `read -p` with no status check, the identical defect class round 3
    # closed in restore_database()'s drop-and-recreate prompt (see that
    # fix's comment there + the corresponding Edge-cases entry) but that a
    # round-4 Fable-xhigh re-review found had NOT also been applied to
    # main()'s own confirmation prompt. Non-interactive stdin (closed or
    # `/dev/null`, e.g. a script/cron invocation without --force) hits EOF,
    # `read` returns non-zero, and under `set -e` that used to abort
    # main() immediately with only the preceding WARN line printed and no
    # diagnostic at all. On EOF we abort rather than guess an answer
    # (§11.4.6/§11.4.101 - conservative-safe default: never treat "could
    # not ask" as "yes" for a destructive restore); use --force to skip
    # this prompt entirely for non-interactive invocations.
    if [ "$FORCE" = false ]; then
        echo ""
        log_warn "This will overwrite existing data!"
        if ! read -p "Continue with restore? [y/N]: " confirm; then
            log_error "Failed to read confirmation input (EOF or closed stdin) for the pre-restore confirmation. Use --force for non-interactive invocations."
            exit 1
        fi
        if [[ ! "$confirm" =~ ^[Yy]$ ]]; then
            log_info "Restore cancelled"
            exit 0
        fi
    fi
    
    # Stop services before restore
    stop_services_before_restore
    
    # Start just the database
    log_info "Starting database..."
    cd "$INSTALL_DIR"
    $COMPOSE_CMD -f "$COMPOSE_FILE" up -d "$DB_SERVICE"
    
    # Wait for database
    log_info "Waiting for database..."
    sleep "${RESTORE_PG_READY_INITIAL_DELAY_SECONDS:-5}"

    if ! wait_for_database_ready; then
        log_error "Postgres did not become ready within the retry budget after starting the '$DB_SERVICE' service. Aborting rather than attempting the restore against a database that may not be accepting connections yet."
        exit 1
    fi

    # Restore database
    restore_database "$backup_file"
    
    # Restore configuration and evidence (unless db-only)
    if [ "$DB_ONLY" = false ]; then
        restore_config "$backup_file"
        restore_evidence "$backup_file"
    fi
    
    # Restart services
    log_info "Restarting services..."
    "$SCRIPT_DIR/start.sh"
    
    echo ""
    log_success "Restore complete!"
    log_info "Verify with: curl http://localhost:8080/health"
}

main "$@"
