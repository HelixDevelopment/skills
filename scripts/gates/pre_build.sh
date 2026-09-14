#!/usr/bin/env bash
set -euo pipefail
echo "=== Pre-build gate ==="

# go vet
go vet ./... 2>&1

# gofmt check
if [ -n "$(gofmt -l .)" ]; then
    echo "FAIL: Unformatted files:"
    gofmt -l .
    exit 1
fi

# unit test layer
go test -short -race ./... 2>&1

# Security grep: no wildcard CORS with credentials in server code
if grep -rn 'Allow-Origin", "\*"' cmd/server/ 2>/dev/null; then
    echo "FAIL: Wildcard CORS found in server code"
    exit 1
fi

echo "=== Pre-build gate PASSED ==="
