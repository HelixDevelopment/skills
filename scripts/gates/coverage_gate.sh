#!/usr/bin/env bash
set -euo pipefail
echo "=== Coverage gate ==="

PROJECT_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
COVERAGE_FILE="${COVERAGE_FILE:-${PROJECT_ROOT}/coverage.out}"
FLOOR_FILE="$(dirname "$0")/coverage_floor.tsv"

# Run tests with coverage
cd "$PROJECT_ROOT"
go test -coverprofile="$COVERAGE_FILE" ./... 2>&1

if [ ! -f "$COVERAGE_FILE" ]; then
    echo "FAIL: coverage profile not generated"
    exit 1
fi

# Generate per-package coverage percentages
coverage_data=$(
    go tool cover -func="$COVERAGE_FILE" | \
        while IFS=$'\t' read -r pkg func stmt cov; do
            cov="${cov%\%}"
            case "$cov" in
                *\%) cov="${cov%\%}" ;;
            esac
            # Extract the package path (strip the function name)
            pkg_path="${pkg%\.*}"
            echo "$pkg_path|$cov"
        done
)

# Track overall result
exit_code=0

# Read the TSV floor file (skip comments and header)
while IFS=$'\t' read -r pkg_path floor notes; do
    # Skip empty lines and comments
    case "$pkg_path" in
        ''|'#'*) continue ;;
    esac

    pkg_path="$(echo "$pkg_path" | xargs)"
    floor="$(echo "$floor" | xargs)"

    # Find matching coverage for this package
    matched_cov=""
    while IFS='|' read -r cov_pkg cov_val; do
        if [ "$cov_pkg" = "$pkg_path" ]; then
            matched_cov="$cov_val"
            break
        fi
    done <<< "$coverage_data"

    if [ -z "$matched_cov" ]; then
        echo "WARN: package '$pkg_path' not found in coverage data (no test files?)"
        continue
    fi

    # Compare: matched_cov >= floor (as floats)
    cmp=$(echo "$matched_cov >= $floor" | bc 2>/dev/null || echo "0")
    if [ "$cmp" = "1" ]; then
        echo "OK:   $pkg_path  coverage=${matched_cov}%  floor=${floor}%"
    else
        echo "FAIL: $pkg_path  coverage=${matched_cov}%  floor=${floor}%  -- $notes"
        exit_code=1
    fi
done < "$FLOOR_FILE"

if [ "$exit_code" -eq 0 ]; then
    echo "=== Coverage gate PASSED ==="
else
    echo "=== Coverage gate FAILED ==="
fi
exit "$exit_code"
