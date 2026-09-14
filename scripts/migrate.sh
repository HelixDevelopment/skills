#!/bin/bash
# =============================================================================
# HelixKnowledge Skill Graph System - Database Migration Script
# =============================================================================
# Usage: ./migrate.sh [up | down | status | create <name> | version]
#   up       Apply all pending migrations (default)
#   down     Rollback one migration
#   status   Show current migration status
#   create   Create a new migration file
#   version  Show current schema version
# =============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_DIR="$(dirname "$SCRIPT_DIR")"
MIGRATIONS_DIR="$INSTALL_DIR/migrations"
# G13: the single canonical compose file + its datastore service name. All
# compose calls below target this file explicitly (-f), never cwd-discovery of
# a rival root compose, and the canonical service is `postgres` (not `db`).
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
    DATABASE_URL="postgres://${DB_USER}:${DB_PASSWORD}@${DB_HOST}:${DB_PORT}/${DB_NAME}?sslmode=disable"
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

# Execute SQL via container
exec_sql() {
    local sql="$1"
    cd "$INSTALL_DIR"
    $COMPOSE_CMD -f "$COMPOSE_FILE" exec -T "$DB_SERVICE" psql -U "$DB_USER" -d "$DB_NAME" -t -c "$sql" 2>/dev/null
}

# Check if migrations table exists
ensure_migrations_table() {
    local exists
    exists=$(exec_sql "SELECT 1 FROM information_schema.tables WHERE table_name = 'schema_migrations';")
    
    if [ -z "$exists" ]; then
        log_info "Creating schema_migrations table..."
        exec_sql "CREATE TABLE IF NOT EXISTS schema_migrations (
            version BIGINT PRIMARY KEY,
            applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
            description TEXT
        );"
        log_success "Migrations table created"
    fi
}

# Get current version
get_current_version() {
    local version
    version=$(exec_sql "SELECT COALESCE(MAX(version), 0) FROM schema_migrations;" || echo "0")
    echo "$version" | tr -d ' '
}

# Apply migration up
migrate_up() {
    log_info "Applying pending migrations..."
    ensure_migrations_table
    
    local current_version
    current_version=$(get_current_version)
    log_info "Current version: $current_version"
    
    local applied=0
    
    # Find and apply pending migrations
    for migration in $(ls -1 "$MIGRATIONS_DIR"/*.up.sql 2>/dev/null | sort); do
        local filename
        filename=$(basename "$migration")
        local version
        version=$(echo "$filename" | grep -o '^[0-9]*')
        local description
        description=$(echo "$filename" | sed 's/^[0-9]*_//;s/\.up\.sql$//' | tr '_' ' ')
        
        # Check if already applied
        local already_applied
        already_applied=$(exec_sql "SELECT 1 FROM schema_migrations WHERE version = ${version};")
        
        if [ -z "$already_applied" ]; then
            log_info "Applying migration ${version}: ${description}..."
            
            # Execute migration
            # -v ON_ERROR_STOP=1: psql exits non-zero on ANY in-migration SQL
            # error (psql otherwise exits 0 on a statement error, silently
            # desyncing schema_migrations). stderr is surfaced (not discarded)
            # so a real failure is visible and caught by the else/exit 1 path.
            # See G51 in GAPS_AND_RISKS_REGISTER.md (§11.4.201).
            cd "$INSTALL_DIR"
            if $COMPOSE_CMD -f "$COMPOSE_FILE" exec -T "$DB_SERVICE" psql -v ON_ERROR_STOP=1 -U "$DB_USER" -d "$DB_NAME" < "$migration"; then
                exec_sql "INSERT INTO schema_migrations (version, description) VALUES (${version}, '${description}');"
                log_success "Applied migration ${version}"
                applied=$((applied + 1))
            else
                log_error "Failed to apply migration ${version}"
                exit 1
            fi
        fi
    done
    
    if [ "$applied" -eq 0 ]; then
        log_info "No pending migrations"
    else
        log_success "Applied $applied migration(s)"
    fi
    
    local new_version
    new_version=$(get_current_version)
    log_info "Current version: $new_version"
}

# Rollback one migration
migrate_down() {
    log_info "Rolling back last migration..."
    ensure_migrations_table
    
    local current_version
    current_version=$(get_current_version)
    
    if [ "$current_version" = "0" ]; then
        log_warn "No migrations to rollback"
        return
    fi
    
    # Find the down migration
    local down_file
    down_file=$(find "$MIGRATIONS_DIR" -name "${current_version}_*.down.sql" 2>/dev/null | head -1)
    
    if [ -n "$down_file" ] && [ -f "$down_file" ]; then
        log_info "Applying rollback: $(basename "$down_file")..."
        cd "$INSTALL_DIR"
        # -v ON_ERROR_STOP=1 + surfaced stderr: psql exits non-zero on ANY
        # in-rollback SQL error, so `set -e` aborts BEFORE the DELETE below and
        # the schema_migrations row is NOT removed on a failed rollback (psql
        # otherwise exits 0 on a statement error, silently desyncing state).
        # See G51 in GAPS_AND_RISKS_REGISTER.md (§11.4.201).
        $COMPOSE_CMD -f "$COMPOSE_FILE" exec -T "$DB_SERVICE" psql -v ON_ERROR_STOP=1 -U "$DB_USER" -d "$DB_NAME" < "$down_file"
        exec_sql "DELETE FROM schema_migrations WHERE version = ${current_version};"
        log_success "Rolled back migration ${current_version}"
    else
        # G64 (§11.4.201/§11.4.6 fail-closed, never guess-delete): a missing
        # down file used to fall through to deleting the schema_migrations
        # row anyway - that DESYNCS tracked state from reality, because the
        # up-migration's schema changes are still physically applied to the
        # database while schema_migrations now reports the version as
        # "not applied". A subsequent `migrate.sh up` would then try to
        # re-apply that up-migration against objects that already exist
        # (duplicate-table/column errors, or worse, a silent no-op that hides
        # the fact the rollback never actually happened). There is no safe
        # action to take without the down script, so refuse and fail closed
        # instead of mutating the tracking table.
        log_error "No down migration found for version ${current_version} (expected: ${MIGRATIONS_DIR}/${current_version}_*.down.sql)."
        log_error "Refusing to delete the schema_migrations row without a down script - the up-migration's schema changes are still applied and doing so would desync tracked state from reality. Add the missing down file, or resolve manually, then retry."
        exit 1
    fi
}

# Show migration status
migrate_status() {
    ensure_migrations_table
    
    local current_version
    current_version=$(get_current_version)
    
    echo ""
    echo -e "${BLUE}Migration Status${NC}"
    echo "═══════════════════════════════════════════════════════════════"
    printf "  %-12s %-10s %s\n" "Version" "Status" "Description"
    echo "───────────────────────────────────────────────────────────────"
    
    for migration in $(ls -1 "$MIGRATIONS_DIR"/*.up.sql 2>/dev/null | sort); do
        local filename
        filename=$(basename "$migration")
        local version
        version=$(echo "$filename" | grep -o '^[0-9]*')
        local description
        description=$(echo "$filename" | sed 's/^[0-9]*_//;s/\.up\.sql$//' | tr '_' ' ')
        
        local status="${YELLOW}pending${NC}"
        local already_applied
        already_applied=$(exec_sql "SELECT 1 FROM schema_migrations WHERE version = ${version};")
        
        if [ -n "$already_applied" ]; then
            status="${GREEN}applied${NC}"
        fi
        
        printf "  %-12s %-10b %s\n" "$version" "$status" "$description"
    done
    
    echo ""
    log_info "Current version: $current_version"
}

# Create new migration
create_migration() {
    local name="$1"
    
    if [ -z "$name" ]; then
        log_error "Migration name required"
        exit 1
    fi
    
    # Generate version number (timestamp)
    local version
    version=$(date +%Y%m%d%H%M%S)
    local filename_base="${version}_${name}"
    
    # Create migration files
    local up_file="$MIGRATIONS_DIR/${filename_base}.up.sql"
    local down_file="$MIGRATIONS_DIR/${filename_base}.down.sql"
    
    cat > "$up_file" << EOF
-- Migration: ${name}
-- Version: ${version}
-- Created: $(date -Iseconds)

BEGIN;

-- Add your migration here

COMMIT;
EOF

    cat > "$down_file" << EOF
-- Rollback: ${name}
-- Version: ${version}

BEGIN;

-- Add rollback here

COMMIT;
EOF

    log_success "Created migration files:"
    echo "  $up_file"
    echo "  $down_file"
}

# Show version
show_version() {
    ensure_migrations_table
    local version
    version=$(get_current_version)
    echo "$version"
}

# Main
main() {
    load_env
    detect_compose
    
    # Ensure migrations directory exists
    mkdir -p "$MIGRATIONS_DIR"
    
    local command="${1:-up}"
    
    case "$command" in
        up)
            migrate_up
            ;;
        down)
            migrate_down
            ;;
        status)
            migrate_status
            ;;
        create)
            create_migration "${2:-}"
            ;;
        version)
            show_version
            ;;
        --help|-h)
            echo "Usage: $0 [up | down | status | create <name> | version]"
            echo ""
            echo "Commands:"
            echo "  up              Apply all pending migrations (default)"
            echo "  down            Rollback last migration"
            echo "  status          Show migration status"
            echo "  create <name>   Create a new migration"
            echo "  version         Show current schema version"
            echo "  --help          Show this help"
            echo ""
            echo "Examples:"
            echo "  $0                    Apply all pending"
            echo "  $0 status             Show status"
            echo "  $0 create add_skills  Create new migration"
            exit 0
            ;;
        *)
            log_error "Unknown command: $command"
            echo "Run '$0 --help' for usage"
            exit 1
            ;;
    esac
}

main "$@"
