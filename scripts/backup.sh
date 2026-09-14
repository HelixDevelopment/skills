#!/bin/bash
# =============================================================================
# HelixKnowledge Skill Graph System - Backup Script
# =============================================================================
# Usage: ./backup.sh [--output <dir>] [--name <name>] [--full | --db-only]
# Creates a compressed archive containing:
#   - Database dump (pg_dump)
#   - Configuration files (.env, config.toml)
#   - Evidence data directory
#   - Migration status
# =============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_DIR="$(dirname "$SCRIPT_DIR")"
SERVICE_NAME="skill-system"
# G13: the single canonical compose file + its service names. Compose calls
# target this file explicitly (-f); canonical services are `postgres` (datastore)
# and `app` (opt-in `--profile app`), never the retired root file's `db`/`api`.
# See research/ops_hardening_design.md (G13) + scripts/check_compose_canonical.sh.
COMPOSE_FILE="$INSTALL_DIR/deploy/docker-compose.yml"
DB_SERVICE="postgres"
APP_SERVICE="app"

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
BACKUP_DIR="$INSTALL_DIR/data/backups"
BACKUP_NAME=""
BACKUP_TYPE="full"
RETENTION_DAYS=30

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

# Generate backup name
generate_name() {
    local timestamp
    timestamp=$(date +%Y%m%d_%H%M%S)
    local version
    version=$(cd "$INSTALL_DIR" && $COMPOSE_CMD -f "$COMPOSE_FILE" exec -T "$APP_SERVICE" /app/server --version 2>/dev/null || echo "unknown")
    version=$(echo "$version" | tr -d '[:space:]')
    
    if [ -n "$BACKUP_NAME" ]; then
        echo "$BACKUP_NAME"
    else
        echo "skill-system_${version}_${timestamp}"
    fi
}

# Create backup
create_backup() {
    local name="$1"
    local temp_dir
    temp_dir=$(mktemp -d)
    local backup_root="$temp_dir/$name"
    
    mkdir -p "$backup_root"/{database,config,evidence,metadata}
    
    log_info "Creating backup: $name"
    log_info "Type: $BACKUP_TYPE"
    
    # 1. Database backup
    log_info "Dumping database..."
    cd "$INSTALL_DIR"
    
    if ! $COMPOSE_CMD -f "$COMPOSE_FILE" exec -T "$DB_SERVICE" pg_dump \
        -U "$DB_USER" \
        -d "$DB_NAME" \
        --no-owner \
        --no-privileges \
        --clean \
        --if-exists \
        > "$backup_root/database/skilldb.sql" 2>/dev/null; then

        # Fallback: try with password in environment
        if ! $COMPOSE_CMD -f "$COMPOSE_FILE" exec -T -e PGPASSWORD="$DB_PASSWORD" "$DB_SERVICE" pg_dump \
            -U "$DB_USER" \
            -d "$DB_NAME" \
            --no-owner \
            --no-privileges \
            --clean \
            --if-exists \
            > "$backup_root/database/skilldb.sql" 2>/dev/null; then
            log_error "Database dump failed"
            rm -rf "$temp_dir"
            exit 1
        fi
    fi
    
    # Compress database dump
    gzip -f "$backup_root/database/skilldb.sql"
    log_success "Database dumped and compressed"
    
    # 2. Configuration backup (full mode only)
    if [ "$BACKUP_TYPE" = "full" ]; then
        log_info "Backing up configuration..."
        
        if [ -f "$INSTALL_DIR/.env" ]; then
            cp "$INSTALL_DIR/.env" "$backup_root/config/"
        fi
        
        if [ -f "$INSTALL_DIR/config/config.toml" ]; then
            cp "$INSTALL_DIR/config/config.toml" "$backup_root/config/"
        fi
        
        # Copy compose file (G13 canonical: deploy/docker-compose.yml)
        if [ -f "$COMPOSE_FILE" ]; then
            cp "$COMPOSE_FILE" "$backup_root/config/"
        fi
        
        log_success "Configuration backed up"
        
        # 3. Evidence data
        log_info "Backing up evidence data..."
        
        if [ -d "$INSTALL_DIR/data/evidence" ]; then
            tar -czf "$backup_root/evidence/evidence.tar.gz" \
                -C "$INSTALL_DIR/data" evidence/ 2>/dev/null || true
            log_success "Evidence data backed up"
        else
            log_warn "No evidence data directory found"
        fi
    fi
    
    # 4. Metadata
    log_info "Creating metadata..."
    
    cat > "$backup_root/metadata/backup.json" << EOF
{
    "name": "$name",
    "type": "$BACKUP_TYPE",
    "created_at": "$(date -Iseconds)",
    "version": "$(cd "$INSTALL_DIR" && $COMPOSE_CMD -f "$COMPOSE_FILE" exec -T "$APP_SERVICE" /app/server --version 2>/dev/null || echo "unknown")",
    "hostname": "$(hostname)",
    "database": {
        "name": "$DB_NAME",
        "host": "$DB_HOST",
        "port": $DB_PORT
    },
    "contents": {
        "database": true,
        "configuration": $( [ "$BACKUP_TYPE" = "full" ] && echo "true" || echo "false" ),
        "evidence": $( [ "$BACKUP_TYPE" = "full" ] && echo "true" || echo "false" )
    }
}
EOF
    
    # 5. Create final archive
    local archive_path="$BACKUP_DIR/${name}.tar.gz"
    mkdir -p "$BACKUP_DIR"
    
    tar -czf "$archive_path" -C "$temp_dir" "$name"
    rm -rf "$temp_dir"
    
    # Show result
    local size
    size=$(du -h "$archive_path" | cut -f1)
    
    log_success "Backup created: $archive_path"
    log_info "Size: $size"
    
    # Cleanup old backups
    cleanup_old_backups
    
    echo "$archive_path"
}

# Remove old backups
cleanup_old_backups() {
    if [ "$RETENTION_DAYS" -gt 0 ]; then
        log_info "Cleaning up backups older than $RETENTION_DAYS days..."
        
        local count
        count=$(find "$BACKUP_DIR" -name "*.tar.gz" -mtime +$RETENTION_DAYS | wc -l)
        
        if [ "$count" -gt 0 ]; then
            find "$BACKUP_DIR" -name "*.tar.gz" -mtime +$RETENTION_DAYS -delete
            log_info "Removed $count old backup(s)"
        fi
    fi
}

# List backups
list_backups() {
    echo -e "\n${BLUE}Available Backups${NC}"
    echo "═══════════════════════════════════════════════════════════════"
    
    if [ ! -d "$BACKUP_DIR" ] || [ -z "$(ls -A "$BACKUP_DIR"/*.tar.gz 2>/dev/null)" ]; then
        echo "No backups found"
        return
    fi
    
    printf "  %-40s %12s %20s\n" "Name" "Size" "Date"
    echo "───────────────────────────────────────────────────────────────"
    
    for backup in $(ls -1t "$BACKUP_DIR"/*.tar.gz 2>/dev/null); do
        local name
        name=$(basename "$backup")
        local size
        size=$(du -h "$backup" | cut -f1)
        local date
        date=$(stat -c '%y' "$backup" 2>/dev/null | cut -d'.' -f1 || stat -f '%Sm' "$backup")
        
        printf "  %-40s %12s %20s\n" "$name" "$size" "$date"
    done
}

# Main
main() {
    load_env
    detect_compose
    
    # Parse arguments
    while [ $# -gt 0 ]; do
        case "$1" in
            --output)
                BACKUP_DIR="$2"
                shift 2
                ;;
            --name)
                BACKUP_NAME="$2"
                shift 2
                ;;
            --full)
                BACKUP_TYPE="full"
                shift
                ;;
            --db-only)
                BACKUP_TYPE="db-only"
                shift
                ;;
            --retention)
                RETENTION_DAYS="$2"
                shift 2
                ;;
            --list)
                list_backups
                exit 0
                ;;
            --help|-h)
                echo "Usage: $0 [options]"
                echo ""
                echo "Options:"
                echo "  --output <dir>     Backup directory (default: data/backups)"
                echo "  --name <name>      Custom backup name"
                echo "  --full             Full backup (db + config + evidence)"
                echo "  --db-only          Database only"
                echo "  --retention <days> Retention period (default: 30)"
                echo "  --list             List existing backups"
                echo "  --help             Show this help"
                echo ""
                echo "Examples:"
                echo "  $0                           Create full backup"
                echo "  $0 --db-only                 Database-only backup"
                echo "  $0 --name pre-upgrade        Named backup"
                echo "  $0 --retention 7             7-day retention"
                exit 0
                ;;
            *)
                log_error "Unknown option: $1"
                exit 1
                ;;
        esac
    done
    
    local name
    name=$(generate_name)
    
    create_backup "$name"
    
    log_success "Backup complete!"
}

main "$@"
