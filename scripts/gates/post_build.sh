#!/usr/bin/env bash
set -euo pipefail
echo "=== Post-build gate ==="

# Build binaries
go build ./cmd/server/ 2>&1

# Run integration tests
if [ -n "${HELIX_TEST_DATABASE_URL:-}" ]; then
    go test -run Integration -race ./... 2>&1
else
    echo "SKIP: integration tests (HELIX_TEST_DATABASE_URL not set)"
fi

echo "=== Post-build gate PASSED ==="
