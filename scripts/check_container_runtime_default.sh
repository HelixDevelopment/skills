#!/usr/bin/env bash
# =============================================================================
# check_container_runtime_default.sh - assert the Makefile's CONTAINER_RUNTIME
# default is rootless Podman, never rootful Docker (Gap register G39, HIGH;
# Constitution Sec 11.4.161 / Sec 11.4.76 / Sec 11.4.173 / Sec 11.4.201)
# =============================================================================
# Purpose:
#   Constitution Sec 11.4.161 mandates rootless Podman (or an equivalent
#   rootless runtime) as the default for ALL containerized workloads -
#   rootful Docker / sudo is forbidden unless the target platform has no
#   rootless option (Sec 11.4.112, a documented exception; not the case
#   here). This gate mechanically asserts the project Makefile's
#   `CONTAINER_RUNTIME ?= ...` default literal is "podman", resolved by
#   parsing the REAL Makefile text (never assumed, never guessed - Sec
#   11.4.6), and prints the resolved evidence on every run - PASS or FAIL -
#   so a refusal is diagnosable in one step and a false-positive refusal is
#   as visible as a false-negative pass (Sec 11.4.201 - guards MUST assert
#   the real condition and print their resolved evidence).
#
# Usage:
#   scripts/check_container_runtime_default.sh [--makefile PATH]
#   scripts/check_container_runtime_default.sh --selftest
#   scripts/check_container_runtime_default.sh -h|--help
#
#   Plain mode (no args, or --makefile PATH): parses the given Makefile
#   (default: ../Makefile relative to this script, i.e. project/Makefile)
#   and exits 0 iff its CONTAINER_RUNTIME default is exactly "podman".
#   Exits 1 on any other default, or on a missing file / unparseable line -
#   printing the resolved evidence (the matched line's extracted value, or
#   the concrete absence) either way.
#
#   --selftest mode is the paired Sec 1.1 mutation test for this gate. It:
#     1. Asserts the gate PASSes on the real project/Makefile.
#     2. Copies the real Makefile to a throwaway scratch file (mktemp -d),
#        mutates ONLY that scratch copy's `CONTAINER_RUNTIME ?= podman`
#        line to `CONTAINER_RUNTIME ?= docker`, and asserts the gate FAILs
#        against that docker-mutant copy.
#     3. Removes the scratch file/directory (trap-cleaned on every exit
#        path, per Sec 11.4.14).
#   It NEVER modifies the real Makefile. Exits 0 iff BOTH assertions hold
#   (i.e. the gate is genuinely load-bearing, not a bluff gate that would
#   pass regardless of the Makefile's actual content).
#
# Inputs:
#   The target Makefile's text (read-only). No environment variables are
#   read or required.
#
# Outputs:
#   Human-readable "EVIDENCE:" + "PASS:"/"FAIL:" lines on stdout in every
#   mode. Exit code 0 on PASS (plain mode) or on both --selftest assertions
#   holding; non-zero otherwise.
#
# Side-effects:
#   Plain mode: none (read-only parse). --selftest mode: creates and
#   removes exactly one throwaway scratch file under a private `mktemp -d`
#   directory, trap-cleaned on every exit path (Sec 11.4.14); the real
#   project/Makefile is READ but NEVER WRITTEN by this script in either
#   mode.
#
# Dependencies: bash >= 4, coreutils (grep, sed, mktemp, cp, rm). No
#   container engine (docker/podman) is required to RUN this gate - it
#   parses Makefile text only and does not invoke any container runtime.
#
# Cross-references:
#   ../Makefile (the CONTAINER_RUNTIME ?= default this gate asserts on);
#   docs/scripts/check_container_runtime_default.md (companion user guide);
#   Constitution Sec 11.4.161 (rootless container runtime mandate),
#   Sec 11.4.76 (Containers-submodule mandate), Sec 11.4.173 (containerized
#   + distributed build mandate), Sec 11.4.201 (every guard/gate MUST
#   assert the real condition + print resolved evidence on refusal).
# Last verified: 2026-07-16
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEFAULT_MAKEFILE="${SCRIPT_DIR}/../Makefile"
REQUIRED_DEFAULT="podman"

usage() {
    cat <<'EOF'
Usage: check_container_runtime_default.sh [--makefile PATH]
       check_container_runtime_default.sh --selftest
       check_container_runtime_default.sh -h|--help

Asserts the project Makefile's `CONTAINER_RUNTIME ?= ...` default is
exactly "podman" (Constitution Sec 11.4.161 - rootless container runtime
mandate). Prints the resolved evidence (matched line's extracted value, or
the concrete absence) on every run, PASS or FAIL (Sec 11.4.201).

  --makefile PATH   Check this Makefile instead of the project default
                     (../Makefile relative to this script).
  --selftest        Paired Sec 1.1 mutation test: asserts PASS on the real
                     Makefile AND FAIL on a throwaway docker-mutant scratch
                     copy. Never modifies the real Makefile.
  -h, --help        Show this help and exit.

Exit code: 0 on PASS (plain mode) or on both --selftest assertions holding;
non-zero otherwise.
EOF
}

# extract_default MAKEFILE_PATH
# Prints the CONTAINER_RUNTIME default token to stdout and returns 0 if a
# `CONTAINER_RUNTIME ?= ...` line was found; returns 1 (nothing printed) if
# the file is missing or carries no such line. Never guesses (Sec 11.4.6) -
# an absent/unparseable line is reported as absence, never silently
# defaulted to a guessed value.
extract_default() {
    local makefile="$1"
    if [[ ! -f "${makefile}" ]]; then
        return 1
    fi
    local raw_line
    raw_line="$(grep -E '^CONTAINER_RUNTIME[[:space:]]*\?=' "${makefile}" | head -n1 || true)"
    if [[ -z "${raw_line}" ]]; then
        return 1
    fi
    local value
    value="$(printf '%s\n' "${raw_line}" | sed -E 's/^CONTAINER_RUNTIME[[:space:]]*\?=[[:space:]]*//; s/[[:space:]]*$//')"
    printf '%s' "${value}"
    return 0
}

# check_makefile MAKEFILE_PATH
# Prints resolved evidence to stdout and returns 0 if the default is exactly
# "podman", 1 otherwise. Always prints its evidence line first, regardless
# of verdict (Sec 11.4.201).
check_makefile() {
    local makefile="$1"
    local value
    if ! value="$(extract_default "${makefile}")"; then
        echo "EVIDENCE: no 'CONTAINER_RUNTIME ?=' line found in: ${makefile}"
        echo "FAIL: cannot resolve CONTAINER_RUNTIME default (file missing or line absent)."
        return 1
    fi
    echo "EVIDENCE: ${makefile} -> CONTAINER_RUNTIME default = '${value}'"
    if [[ "${value}" == "${REQUIRED_DEFAULT}" ]]; then
        echo "PASS: CONTAINER_RUNTIME defaults to '${REQUIRED_DEFAULT}' (rootless-first, Constitution Sec 11.4.161)."
        return 0
    fi
    echo "FAIL: CONTAINER_RUNTIME defaults to '${value}', required '${REQUIRED_DEFAULT}' (Constitution Sec 11.4.161)."
    return 1
}

# run_selftest
# The paired Sec 1.1 mutation test. See the "--selftest mode" section of the
# in-source Purpose block above for the two assertions it makes.
run_selftest() {
    local scratch_dir
    scratch_dir="$(mktemp -d "${TMPDIR:-/tmp}/check_container_runtime_default.XXXXXX")"
    # shellcheck disable=SC2064
    trap "rm -rf '${scratch_dir}'" EXIT

    local overall=0

    echo "== Sec 1.1 paired mutation test: check_container_runtime_default.sh =="
    echo
    echo "-- assertion 1: gate PASSes on the real project Makefile --"
    if check_makefile "${DEFAULT_MAKEFILE}"; then
        echo "assertion 1: PASS (gate correctly passes the fixed Makefile)"
    else
        echo "assertion 1: FAIL (gate did NOT pass the real, fixed Makefile - regression)"
        overall=1
    fi

    echo
    echo "-- assertion 2: gate FAILs on a throwaway docker-mutant scratch copy --"
    local mutant="${scratch_dir}/Makefile.docker-mutant"
    cp -- "${DEFAULT_MAKEFILE}" "${mutant}"
    # Mutate ONLY the scratch copy - the real Makefile is never touched.
    sed -i -E 's/^(CONTAINER_RUNTIME[[:space:]]*\?=[[:space:]]*)podman([[:space:]]*)$/\1docker\2/' "${mutant}"
    if ! grep -qE '^CONTAINER_RUNTIME[[:space:]]*\?=[[:space:]]*docker[[:space:]]*$' "${mutant}"; then
        echo "assertion 2: FAIL (could not apply the docker-mutant - the scratch copy's pattern did not match; test is inconclusive, treated as failure per Sec 11.4.6)"
        overall=1
    elif check_makefile "${mutant}"; then
        echo "assertion 2: FAIL (gate incorrectly PASSed the docker-mutant - gate is not load-bearing)"
        overall=1
    else
        echo "assertion 2: PASS (gate correctly FAILs the docker-mutant)"
    fi

    echo
    if [[ "${overall}" -eq 0 ]]; then
        echo "OVERALL SELFTEST: PASS (gate is genuinely load-bearing - PASS on real Makefile, FAIL on docker-mutant)"
    else
        echo "OVERALL SELFTEST: FAIL"
    fi
    return "${overall}"
}

makefile_path="${DEFAULT_MAKEFILE}"
mode="check"

while [[ $# -gt 0 ]]; do
    case "$1" in
        -h|--help)
            usage
            exit 0
            ;;
        --selftest)
            mode="selftest"
            shift
            ;;
        --makefile)
            [[ $# -ge 2 ]] || { echo "check_container_runtime_default.sh: --makefile requires a path argument" >&2; exit 2; }
            makefile_path="$2"
            shift 2
            ;;
        *)
            echo "check_container_runtime_default.sh: unknown argument: $1" >&2
            usage >&2
            exit 2
            ;;
    esac
done

if [[ "${mode}" == "selftest" ]]; then
    run_selftest
    exit $?
fi

check_makefile "${makefile_path}"
exit $?
