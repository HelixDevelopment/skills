# =============================================================================
# HelixKnowledge Skill Graph System - Multi-Stage Dockerfile
# =============================================================================
# Build stage: Compile Go binaries with CGO support for tree-sitter
# Runtime stage: Minimal Alpine image with non-root user
# =============================================================================

# ---------------------------------------------------------------------------
# Stage 1: Builder
# ---------------------------------------------------------------------------
FROM golang:1.22-alpine AS builder

# Build metadata (override via --build-arg)
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown

# Install build dependencies
# - git: for Go module fetching
# - gcc, musl-dev: for CGO support (tree-sitter)
# - libc6-compat: compatibility layer
RUN apk add --no-cache \
    git \
    gcc \
    musl-dev \
    libc6-compat \
    ca-certificates \
    tzdata

# Set workspace
WORKDIR /build

# Copy go module files first for layer caching
COPY go.mod go.sum ./
RUN go mod download && go mod verify

# Copy source code
COPY . .

# Build all binaries with version embedding and static linking
# -ldflags:
#   -s -w: strip debug info for smaller binaries
#   -extldflags "-static": static linking
#   -X: inject version variables into main packages
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build \
    -ldflags "-s -w -extldflags '-static' \
    -X main.Version=${VERSION} \
    -X main.Commit=${COMMIT} \
    -X main.BuildTime=${BUILD_TIME}" \
    -o bin/server ./cmd/server

RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build \
    -ldflags "-s -w -extldflags '-static' \
    -X main.Version=${VERSION} \
    -X main.Commit=${COMMIT} \
    -X main.BuildTime=${BUILD_TIME}" \
    -o bin/worker ./cmd/worker

RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build \
    -ldflags "-s -w -extldflags '-static' \
    -X main.Version=${VERSION} \
    -X main.Commit=${COMMIT} \
    -X main.BuildTime=${BUILD_TIME}" \
    -o bin/skillctl ./cmd/cli

RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build \
    -ldflags "-s -w -extldflags '-static' \
    -X main.Version=${VERSION} \
    -X main.Commit=${COMMIT} \
    -X main.BuildTime=${BUILD_TIME}" \
    -o bin/skill-tui ./cmd/tui

# ---------------------------------------------------------------------------
# Stage 2: Runtime
# ---------------------------------------------------------------------------
FROM alpine:3.19

# Install runtime dependencies
# - ca-certificates: for HTTPS outbound connections
# - tzdata: timezone support
# - curl: for health checks
RUN apk add --no-cache \
    ca-certificates \
    tzdata \
    curl \
    && rm -rf /var/cache/apk/*

# Create non-root user for security
RUN addgroup -g 1000 skillgroup && \
    adduser -u 1000 -G skillgroup -s /bin/sh -D skilluser

# Create application directories
RUN mkdir -p /app /data/evidence /data/backups /config && \
    chown -R skilluser:skillgroup /app /data /config

# Copy binaries from builder
COPY --from=builder /build/bin/server /app/server
COPY --from=builder /build/bin/worker /app/worker
COPY --from=builder /build/bin/skillctl /app/skillctl
COPY --from=builder /build/bin/skill-tui /app/skill-tui

# Copy default config and migrations
COPY --from=builder /build/config/config.toml /config/config.toml
COPY --from=builder /build/migrations /app/migrations

# Set permissions
RUN chmod +x /app/server /app/worker /app/skillctl /app/skill-tui && \
    chown -R skilluser:skillgroup /app /config

# Switch to non-root user
USER skilluser

# Expose ports
# 8080: HTTP/2 API (TCP)
# 8443: HTTP/3 API (UDP)
EXPOSE 8080/tcp 8443/udp

# Health check
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -f http://localhost:8080/health || exit 1

# Set working directory
WORKDIR /app

# Default entrypoint: server binary
ENTRYPOINT ["/app/server"]

# Default command arguments (override per service in docker-compose)
CMD ["--config", "/config/config.toml"]
