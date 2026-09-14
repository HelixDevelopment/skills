#!/bin/bash
# =============================================================================
# HelixKnowledge Skill Graph System - Packaging Script
# =============================================================================
# Usage: ./package.sh [--output <dir>] [--version <version>]
# Creates distributable archives (tar.gz and zip) of the entire project
# for deployment to production or distribution.
# =============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

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
OUTPUT_DIR="$PROJECT_DIR/dist"
VERSION=""
INCLUDE_SOURCE=true

# Get version from git or argument
get_version() {
    if [ -n "$VERSION" ]; then
        echo "$VERSION"
    elif [ -d "$PROJECT_DIR/.git" ]; then
        git -C "$PROJECT_DIR" describe --tags --always 2>/dev/null || \
            git -C "$PROJECT_DIR" rev-parse --short HEAD 2>/dev/null || \
            echo "dev"
    else
        echo "dev"
    fi
}

# Create package
create_package() {
    local version="$1"
    local package_name="skill-system-${version}"
    local temp_dir
    temp_dir=$(mktemp -d)
    local package_dir="$temp_dir/$package_name"
    
    log_info "Creating package: $package_name"
    
    # Create directory structure. `deploy/` preserves the canonical compose
    # file's home so its ../config, ../migrations, ../Dockerfile relative paths
    # still resolve inside the package (G13 - single canonical compose).
    mkdir -p "$package_dir"/{bin,scripts,config,docs,migrations,deploy,data/{evidence,backups}}
    
    # Copy source code (if enabled)
    if [ "$INCLUDE_SOURCE" = true ]; then
        log_info "Including source code..."
        
        # Copy Go source
        cp -r "$PROJECT_DIR/cmd" "$package_dir/"
        cp -r "$PROJECT_DIR/internal" "$package_dir/"
        cp "$PROJECT_DIR/go.mod" "$package_dir/"
        cp "$PROJECT_DIR/go.sum" "$package_dir/" 2>/dev/null || true
        
        # Copy Makefile
        cp "$PROJECT_DIR/Makefile" "$package_dir/"
        
        # Copy Dockerfile
        cp "$PROJECT_DIR/Dockerfile" "$package_dir/"
        
        # Copy the canonical compose file (G13: project/deploy/docker-compose.yml),
        # preserving its deploy/ home so its relative paths resolve in-package.
        cp "$PROJECT_DIR/deploy/docker-compose.yml" "$package_dir/deploy/"
        
        # Copy .env.example
        cp "$PROJECT_DIR/.env.example" "$package_dir/"
    fi
    
    # Copy scripts
    log_info "Copying scripts..."
    cp "$PROJECT_DIR/scripts/"*.sh "$package_dir/scripts/"
    chmod +x "$package_dir/scripts/"*.sh
    
    # Copy config
    log_info "Copying configuration..."
    cp -r "$PROJECT_DIR/config/"* "$package_dir/config/" 2>/dev/null || true
    
    # Copy migrations
    log_info "Copying migrations..."
    cp -r "$PROJECT_DIR/migrations/"* "$package_dir/migrations/" 2>/dev/null || true
    
    # Copy documentation
    log_info "Copying documentation..."
    if [ -d "$PROJECT_DIR/docs" ]; then
        cp -r "$PROJECT_DIR/docs" "$package_dir/"
    fi
    
    # Copy README
    [ -f "$PROJECT_DIR/README.md" ] && cp "$PROJECT_DIR/README.md" "$package_dir/"
    [ -f "$PROJECT_DIR/LICENSE" ] && cp "$PROJECT_DIR/LICENSE" "$package_dir/"
    [ -f "$PROJECT_DIR/CHANGELOG.md" ] && cp "$PROJECT_DIR/CHANGELOG.md" "$package_dir/"
    
    # Create install guide
    cat > "$package_dir/INSTALL.txt" << EOF
HelixKnowledge Skill Graph System v${version}
═══════════════════════════════════════════════════════════════

Quick Start:
  1. Install Docker or Podman
  2. Run: ./scripts/install.sh
  3. Access: http://localhost:8080

For detailed instructions, see docs/INSTALL.md

Services:
  - API Server:    http://localhost:8080
  - HTTP/3 API:    https://localhost:8443
  - PostgreSQL:    localhost:5432

Management:
  ./scripts/start.sh    - Start services
  ./scripts/stop.sh     - Stop services
  ./scripts/status.sh   - Check status
  ./scripts/backup.sh   - Create backup
  ./scripts/restore.sh  - Restore from backup

Documentation:
  docs/README.md        - Project overview
  docs/INSTALL.md       - Installation guide
  docs/ARCHITECTURE.md  - Architecture details
  docs/API.md           - API documentation
  docs/MCP_INTEGRATION  - MCP integration guide
EOF
    
    # Create package manifest
    cat > "$package_dir/MANIFEST.json" << EOF
{
    "name": "skill-system",
    "version": "${version}",
    "built_at": "$(date -Iseconds)",
    "include_source": ${INCLUDE_SOURCE},
    "files": $(find "$package_dir" -type f | wc -l)
}
EOF
    
    # Create output directory
    mkdir -p "$OUTPUT_DIR"
    
    # Create tar.gz
    log_info "Creating tar.gz archive..."
    local tar_file="$OUTPUT_DIR/${package_name}.tar.gz"
    tar -czf "$tar_file" -C "$temp_dir" "$package_name"
    log_success "Created: $tar_file"
    
    # Create zip
    log_info "Creating zip archive..."
    local zip_file="$OUTPUT_DIR/${package_name}.zip"
    (cd "$temp_dir" && zip -rq "$zip_file" "$package_name")
    log_success "Created: $zip_file"
    
    # Show sizes
    echo ""
    echo -e "${BLUE}Package Sizes:${NC}"
    ls -lh "$tar_file" "$zip_file" | awk '{print "  " $5 "  " $9}'
    
    # Create checksums
    log_info "Creating checksums..."
    cd "$OUTPUT_DIR"
    sha256sum "${package_name}.tar.gz" "${package_name}.zip" > "${package_name}.sha256"
    log_success "Checksums: ${package_name}.sha256"
    
    # Cleanup
    rm -rf "$temp_dir"
    
    echo ""
    log_success "Packaging complete!"
    echo "  Output directory: $OUTPUT_DIR"
    echo ""
    echo "  Files:"
    ls -1 "$OUTPUT_DIR/${package_name}".*
}

# Main
main() {
    # Parse arguments
    while [ $# -gt 0 ]; do
        case "$1" in
            --output)
                OUTPUT_DIR="$2"
                shift 2
                ;;
            --version)
                VERSION="$2"
                shift 2
                ;;
            --no-source)
                INCLUDE_SOURCE=false
                shift
                ;;
            --help|-h)
                echo "Usage: $0 [options]"
                echo ""
                echo "Options:"
                echo "  --output <dir>    Output directory (default: dist/)"
                echo "  --version <ver>   Package version (default: git tag/hash)"
                echo "  --no-source       Exclude source code (binaries/scripts only)"
                echo "  --help            Show this help"
                echo ""
                echo "Examples:"
                echo "  $0                          Package with auto version"
                echo "  $0 --version 1.2.0          Package as v1.2.0"
                echo "  $0 --output /tmp/releases   Custom output dir"
                exit 0
                ;;
            *)
                log_error "Unknown option: $1"
                exit 1
                ;;
        esac
    done
    
    local version
    version=$(get_version)
    
    log_info "Packaging Skill Graph System v${version}..."
    create_package "$version"
}

main "$@"
