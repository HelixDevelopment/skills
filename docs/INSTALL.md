# Installation Guide

## Table of Contents

- [Prerequisites](#prerequisites)
- [Quick Install](#quick-install)
- [Manual Installation](#manual-installation)
- [Configuration](#configuration)
- [Verification](#verification)
- [Troubleshooting](#troubleshooting)
- [Upgrade Procedure](#upgrade-procedure)

---

## Prerequisites

### Minimum Requirements

| Resource | Minimum | Recommended |
|----------|---------|-------------|
| RAM | 4 GB | 8 GB |
| Disk | 10 GB | 50 GB SSD |
| CPU | 2 cores | 4 cores |
| Network | Local only | Internet (for LLM features) |

### Required Software

- **Docker** 24.0+ or **Podman** 4.0+
- **Docker Compose** 2.20+ or **Podman Compose**
- **curl** (for health checks)
- **systemd** (for user services, optional)

### Operating System Support

| OS | Status | Notes |
|----|--------|-------|
| Linux | Fully supported | Native Docker/Podman |
| macOS | Fully supported | Docker Desktop |
| Windows (WSL2) | Supported | WSL2 + Docker Desktop |
| Windows (native) | Not supported | Use WSL2 |

### Checking Prerequisites

```bash
# Check Docker
docker --version
docker compose version

# Check Podman (alternative)
podman --version
podman-compose --version

# Check memory
free -h

# Check disk space
df -h
```

---

## Quick Install

### One-Command Installation

```bash
# Download and run installer
curl -fsSL https://raw.githubusercontent.com/HelixDevelopment/skills/main/scripts/install.sh | bash

# Or with custom directory
curl -fsSL ... | bash -s -- /custom/path
```

The installer will:
1. Check all dependencies
2. Create `/opt/skill-system` (or your custom directory)
3. Copy all deployment files
4. Pull container images
5. Run database migrations
6. Create systemd user service
7. Start the stack
8. Print status and next steps

---

## Manual Installation

### Step 1: Download Release

```bash
# Download latest release
curl -L -o skill-system.tar.gz \
  https://github.com/HelixDevelopment/skills/releases/latest/download/skill-system.tar.gz

# Extract
tar -xzf skill-system.tar.gz
cd skill-system
```

### Step 2: Configure Environment

```bash
# Copy environment template
cp .env.example .env

# Edit configuration
nano .env
```

Minimum required changes:

```env
# Change default password!
DB_PASSWORD=your-secure-password

# Set secrets (generate with: openssl rand -base64 32)
JWT_SECRET=your-jwt-secret
API_KEY=your-api-key
```

### Step 3: Start Database

```bash
# Start PostgreSQL first (default profile brings up only the postgres datastore)
docker compose -f deploy/docker-compose.yml up -d postgres

# Wait for database to be ready
docker compose -f deploy/docker-compose.yml exec postgres pg_isready -U skilluser
```

### Step 4: Run Migrations

```bash
./scripts/migrate.sh up
```

### Step 5: Start Services

```bash
# Start all services (postgres + app + worker; the app/worker are opt-in
# under the `app` compose profile)
docker compose -f deploy/docker-compose.yml --profile app up -d

# Or use the start script
./scripts/start.sh
```

### Step 6: Verify

```bash
# Check health
curl http://localhost:8080/health

# List skills
curl http://localhost:8080/api/v1/skills

# Check container status (enable the app profile to see app/worker too)
docker compose -f deploy/docker-compose.yml --profile app ps
```

---

## Configuration

### Environment Variables

All configuration is via environment variables in the `.env` file.

#### Database

| Variable | Default | Description |
|----------|---------|-------------|
| `DB_NAME` | `skilldb` | Database name |
| `DB_USER` | `skilluser` | Database user |
| `DB_PASSWORD` | `skillpassword` | **Change this!** |
| `DB_PORT` | `5432` | Database port |
| `DB_SSL_MODE` | `disable` | Use `require` in production |

#### Server

| Variable | Default | Description |
|----------|---------|-------------|
| `HTTP_PORT` | `8080` | HTTP/2 API port |
| `HTTP3_PORT` | `8443` | HTTP/3 QUIC port |
| `LOG_LEVEL` | `info` | debug, info, warn, error |
| `ENABLE_HTTP3` | `true` | Enable HTTP/3 support |
| `ENABLE_METRICS` | `true` | Enable Prometheus metrics |

#### Auto-Expansion

| Variable | Default | Description |
|----------|---------|-------------|
| `AUTO_EXPAND_ENABLED` | `true` | Enable auto-growth |
| `AUTO_EXPAND_MAX_DEPTH` | `3` | Max expansion depth |
| `AUTO_EXPAND_CONFIDENCE_THRESHOLD` | `0.7` | Min confidence for auto-create |

#### LLM (Optional)

| Variable | Default | Description |
|----------|---------|-------------|
| `LLM_PROVIDER` | - | `openai`, `anthropic`, `ollama` |
| `LLM_API_KEY` | - | API key for provider |
| `LLM_MODEL` | - | Model name (e.g., `gpt-4`) |

#### Security

| Variable | Default | Description |
|----------|---------|-------------|
| `JWT_SECRET` | - | JWT signing secret |
| `API_KEY` | - | Static API key |
| `CORS_ORIGINS` | `*` | Allowed origins |

### config.toml

For advanced configuration, edit `config/config.toml`:

```toml
[server]
http_port = 8080
http3_port = 8443
read_timeout = "30s"
write_timeout = "30s"
idle_timeout = "120s"

[database]
host = "db"
port = 5432
name = "skilldb"
max_connections = 25
max_idle = 5

[embedding]
provider = "local"
dimension = 768
batch_size = 32

[auto_expand]
enabled = true
max_depth = 3
confidence_threshold = 0.7
cooldown = "24h"

[validation]
enabled = true
interval = "1h"
batch_size = 50
auto_fix = true

[memory]
working_size = 100
working_ttl = "1h"
episodic_retention = "30d"
semantic_cache_size = 10000
```

---

## Verification

### Health Checks

```bash
# Basic health
curl http://localhost:8080/health

# Expected response:
# {"status":"healthy","version":"1.0.0","database":"connected"}
```

### Test Queries

```bash
# Create a skill
curl -X POST http://localhost:8080/api/v1/skills \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Go Concurrency",
    "description": "Goroutines, channels, and sync primitives",
    "category": "backend"
  }'

# List skills
curl http://localhost:8080/api/v1/skills

# Search skills
curl "http://localhost:8080/api/v1/skills/search?q=concurrency"

# Get graph
curl http://localhost:8080/api/v1/graph
```

### Status Script

```bash
# Full status
./scripts/status.sh

# Watch mode (refreshes every 5s)
./scripts/status.sh --watch

# JSON output
./scripts/status.sh --json
```

### Container Status

```bash
# View containers (enable the app profile to include app/worker)
docker compose -f deploy/docker-compose.yml --profile app ps

# View logs
docker compose -f deploy/docker-compose.yml --profile app logs -f

# View specific service logs
docker compose -f deploy/docker-compose.yml --profile app logs -f app
docker compose -f deploy/docker-compose.yml --profile app logs -f worker
docker compose -f deploy/docker-compose.yml logs -f postgres
```

---

## Troubleshooting

### Common Issues

#### Port Already in Use

```bash
# Check what's using port 8080
sudo lsof -i :8080

# Change port in .env
HTTP_PORT=8081
HTTP3_PORT=8444
```

#### Database Connection Failed

```bash
# Check if database container is running
docker compose -f deploy/docker-compose.yml ps postgres

# Check database logs
docker compose -f deploy/docker-compose.yml logs postgres

# Reset database (WARNING: deletes data!)
docker compose -f deploy/docker-compose.yml --profile app --profile monitoring down -v --remove-orphans
docker compose -f deploy/docker-compose.yml up -d postgres
./scripts/migrate.sh up
```

#### Permission Denied

```bash
# Ensure scripts are executable
chmod +x scripts/*.sh

# Fix volume permissions
sudo chown -R $USER:$USER data/
```

#### systemd Service Not Starting

```bash
# Check service status
systemctl --user status skill-system

# View logs
journalctl --user -u skill-system -f

# Reload systemd
systemctl --user daemon-reload

# Check for syntax errors in service file
systemd-analyze --user verify ~/.config/systemd/user/skill-system.service
```

#### Memory Issues

```bash
# Check memory usage
docker stats

# Reduce memory limits in .env
DB_MEMORY_LIMIT=512M
API_MEMORY_LIMIT=256M
WORKER_MEMORY_LIMIT=512M
```

#### Slow Performance

1. **Check database indexes**: Run `VACUUM ANALYZE` in PostgreSQL
2. **Increase worker concurrency**: `WORKER_CONCURRENCY=8`
3. **Enable connection pooling**: Check `DB_MAX_OPEN_CONNS`
4. **Monitor metrics**: Enable the `monitoring` compose profile (`docker compose -f deploy/docker-compose.yml --profile monitoring up -d`)

### Getting Help

1. Check logs: `docker compose -f deploy/docker-compose.yml --profile app logs -f`
2. Run status: `./scripts/status.sh`
3. Review configuration: `cat .env`
4. Open an issue: [GitHub Issues](https://github.com/HelixDevelopment/skills/issues)

---

## Upgrade Procedure

### In-Place Upgrade

```bash
# 1. Stop services
./scripts/stop.sh

# 2. Backup (important!)
./scripts/backup.sh

# 3. Download new version
curl -L -o skill-system-new.tar.gz \
  https://github.com/HelixDevelopment/skills/releases/latest/download/skill-system.tar.gz

# 4. Extract and copy
tar -xzf skill-system-new.tar.gz
cp -r skill-system-new/* /opt/skill-system/

# 5. Run migrations
./scripts/migrate.sh up

# 6. Start services
./scripts/start.sh

# 7. Verify
./scripts/status.sh
```

### Migration-Safe Upgrade

For major version upgrades:

```bash
# 1. Full backup with evidence
./scripts/backup.sh --full

# 2. Stop and remove containers
./scripts/stop.sh
docker compose -f deploy/docker-compose.yml --profile app --profile monitoring down --remove-orphans

# 3. Pull new images
docker compose -f deploy/docker-compose.yml pull

# 4. Start with new version
./scripts/start.sh

# 5. Run any new migrations
./scripts/migrate.sh up

# 6. Verify
curl http://localhost:8080/health
```

### Rollback

If upgrade fails:

```bash
# Restore from backup
./scripts/restore.sh data/backups/skill-system_vOLD_TIMESTAMP.tar.gz --force

# Restart
./scripts/restart.sh --full
```
