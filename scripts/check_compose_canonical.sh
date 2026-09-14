#!/usr/bin/env bash
# =============================================================================
# check_compose_canonical.sh - assert there is exactly ONE canonical compose
# file (project/deploy/docker-compose.yml) and that no ops script or systemd
# unit still references the retired rival root compose (Gap register G13, HIGH;
# Constitution Sec 11.4.108 / Sec 11.4.124 / Sec 11.4.186 / Sec 11.4.201).
# =============================================================================
# Purpose:
#   G13 (research/ops_hardening_design.md) canonicalizes the ops stack on the
#   single file project/deploy/docker-compose.yml, folding the app/worker/
#   monitoring services forward as compose profiles (Sec 11.4.122 - no silent
#   drop) and retiring the rival root project/docker-compose.yml. Two rival
#   compose copies drift (ports, image tags, env, service names, subnet) and
#   scripts pin the wrong one - the exact divergence Sec 11.4.186 forbids.
#
#   This gate mechanically asserts, on the REAL checked-out working tree (not
#   a guess - Sec 11.4.6), the G13 runtime signature (Sec 11.4.108):
#
#     A. SINGLE canonical compose - exactly one `docker-compose*.yml` file is
#        present under the project tree, and it is `deploy/docker-compose.yml`.
#     B. NO retired reference - no ops script or systemd unit references the
#        retired root compose, neither (B1) by an explicit root-level path
#        ($PROJECT_DIR/docker-compose.yml or $INSTALL_DIR/docker-compose.yml,
#        i.e. WITHOUT the /deploy/ segment) NOR (B2) implicitly, via a compose
#        invocation targeting a retired service name (`db`/`api`) that exists
#        only in the removed root file - the canonical file's datastore service
#        is `postgres` and its app service is `app`. B2 catches the hidden-
#        reference class (Sec 11.4.124): scripts that reach the root file via
#        cwd-discovery + a `db`/`api` service name never spell the filename.
#     C. CANONICAL PARSES - the canonical compose (all profiles) parses under a
#        rootless compose engine (Sec 11.4.161). SKIP-with-reason (Sec 11.4.3)
#        when no rootless engine is available - never a fake PASS.
#
#   Every verdict prints its resolved EVIDENCE first (Sec 11.4.201), so a
#   false-positive refusal is as diagnosable as a false-negative pass.
#
# Usage:
#   scripts/check_compose_canonical.sh [--project-root PATH]
#   scripts/check_compose_canonical.sh --selftest
#   scripts/check_compose_canonical.sh -h|--help
#
#   Plain mode: exits 0 iff checks A + B pass (C SKIPs cleanly when no engine).
#   Exits 1 on any real FAIL, printing the resolved evidence either way.
#
#   --selftest mode is the paired Sec 1.1 mutation test for THIS gate. It:
#     1. Asserts the reference-consistency check PASSes on the real scripts/
#        + deploy/systemd/ tree.
#     2. Copies the real scripts/ into a throwaway `mktemp -d` scratch dir,
#        injects ONE dangling reference to the retired root compose into a
#        scratch copy, and asserts the reference-consistency check FAILs on
#        that mutant dir.
#     3. Removes the scratch dir (trap-cleaned on every exit path, Sec 11.4.14).
#   It NEVER modifies any real tracked file. Exits 0 iff BOTH assertions hold
#   (the gate is genuinely load-bearing, not a bluff gate).
#
# Inputs:  the project working tree (read-only). No env vars required.
# Outputs: "EVIDENCE:" + "PASS:"/"FAIL:"/"SKIP:" lines on stdout in every mode.
#          Exit 0 on PASS, non-zero on FAIL.
# Side-effects: plain mode - read-only. --selftest - one throwaway mktemp -d
#   scratch dir, trap-cleaned; real tracked files are READ but NEVER WRITTEN.
# Dependencies: bash >= 4, coreutils (find, grep, sed, mktemp, cp, rm). A
#   rootless compose engine (podman compose / podman-compose / docker compose)
#   is used for check C only; its absence SKIPs C, never fails the gate.
# Cross-references:
#   ../deploy/docker-compose.yml (the canonical file this gate asserts on);
#   ../deploy/systemd/helix-skills.service; scripts/migrate.sh, backup.sh,
#   restore.sh, package.sh, _lib.sh (the ops scripts this gate scans);
#   research/ops_hardening_design.md (G13 design + runtime signature);
#   docs/scripts/check_compose_canonical.md (companion user guide);
#   Constitution Sec 11.4.108 (runtime-signature), Sec 11.4.124 (hidden
#   references), Sec 11.4.161 (rootless runtime), Sec 11.4.186 (anti-
#   divergence), Sec 11.4.201 (guard asserts real condition + prints evidence).
# Last verified: 2026-07-16
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEFAULT_PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
CANONICAL_REL="deploy/docker-compose.yml"

usage() {
    cat <<'EOF'
Usage: check_compose_canonical.sh [--project-root PATH]
       check_compose_canonical.sh --selftest
       check_compose_canonical.sh -h|--help

Asserts the G13 canonical-compose runtime signature: exactly one
docker-compose*.yml under the project (deploy/docker-compose.yml), no ops
script / systemd unit referencing the retired root compose (explicit path OR
retired db/api service name), and that the canonical file parses. Prints the
resolved evidence on every run (Sec 11.4.201).

  --project-root PATH   Check this project root instead of ../ (relative to
                        this script).
  --selftest            Paired Sec 1.1 mutation test: asserts the reference
                        check PASSes on the real tree AND FAILs on a throwaway
                        mutant scratch copy with a re-introduced dangling
                        reference. Never modifies any real file.
  -h, --help            Show this help and exit.

Exit code: 0 on PASS (plain mode) or on both --selftest assertions holding;
non-zero otherwise.
EOF
}

# list_compose_files PROJECT_ROOT
# Prints (one per line) every docker-compose*.yml present in the working tree
# under PROJECT_ROOT, excluding build/dist/vcs scratch. Working-tree based
# (Sec 11.4.108 - the real checked-out state), so a file removed from the
# checkout is counted as gone regardless of git index staging state.
list_compose_files() {
    local root="$1"
    find "${root}" \
        \( -path '*/.git' -o -path '*/dist' -o -path '*/build' -o -path '*/node_modules' \) -prune \
        -o -type f -name 'docker-compose*.yml' -print \
        | sed "s#^${root}/##" | sort
}

# check_single_compose PROJECT_ROOT -> 0 if exactly the canonical file present
check_single_compose() {
    local root="$1"
    local files
    files="$(list_compose_files "${root}" || true)"
    local count
    count="$(printf '%s\n' "${files}" | grep -c . || true)"
    echo "EVIDENCE: tracked-tree compose files under ${root}:"
    if [[ -z "${files}" ]]; then
        echo "EVIDENCE:   (none)"
    else
        printf 'EVIDENCE:   %s\n' ${files}
    fi
    if [[ "${count}" -eq 1 && "${files}" == "${CANONICAL_REL}" ]]; then
        echo "PASS: exactly one canonical compose file (${CANONICAL_REL})."
        return 0
    fi
    echo "FAIL: expected exactly one compose file '${CANONICAL_REL}', found ${count} (G13, Sec 11.4.186)."
    return 1
}

# collect_scan_targets PROJECT_ROOT [SCRIPTS_DIR_OVERRIDE]
# Prints (one path per line) every file whose compose references G13 governs:
# the ops scripts (scripts/*.sh MINUS the check_/test_ gates, which carry the
# pattern strings by construction), the systemd unit(s), and the project
# Makefile (its compose build targets). SCRIPTS_DIR_OVERRIDE lets --selftest
# point the scripts scan at a mutant scratch copy.
collect_scan_targets() {
    local root="$1"
    local scripts_dir="${2:-${root}/scripts}"
    local f
    while IFS= read -r f; do
        case "$(basename "${f}")" in
            check_*|test_*) continue ;;
        esac
        printf '%s\n' "${f}"
    done < <(find "${scripts_dir}" -maxdepth 1 -type f -name '*.sh' 2>/dev/null | sort)
    find "${root}/deploy/systemd" -maxdepth 1 -type f -name '*.service' 2>/dev/null | sort
    [[ -f "${root}/Makefile" ]] && printf '%s\n' "${root}/Makefile"
}

# scan_files_for_retired_refs FILE...
# For each file, flags references to the RETIRED root compose:
#   B1  a non-canonical compose FILE reference - either a shell/Make var root
#       path (PROJECT_DIR|INSTALL_DIR)/docker-compose.yml WITHOUT /deploy/, or a
#       `-f <path>docker-compose.yml` whose path is not deploy/ (e.g. the bare
#       Makefile `-f docker-compose.yml`). The canonical deploy path + the
#       `$COMPOSE_FILE`/`$(COMPOSE_FILE)` vars (which resolve to it) are allowed.
#   B2  a compose invocation (`$COMPOSE_CMD`/`$(COMPOSE_CMD)`) targeting a
#       RETIRED service name `db`/`api` (the canonical file uses postgres/app).
# Prints each offending "file: Bn LINE: text". Returns 0 if none, 1 if any.
# NOTE (honest boundary, Sec 11.4.6): B1+B2 catch the concrete retired-reference
# classes (explicit non-canonical path + retired service name); a brand-new
# bare `compose up` with NO -f relying on cwd discovery is out of this gate's
# scope - every current invocation now carries an explicit -f.
scan_files_for_retired_refs() {
    local hits=0 f
    for f in "$@"; do
        [[ -f "${f}" ]] || continue
        # B1a: shell/Make var root path, no /deploy/ segment.
        if grep -nE '\$[({]?(PROJECT_DIR|INSTALL_DIR)[)}]?/docker-compose\.yml' "${f}" \
            | grep -v '/deploy/docker-compose\.yml' | grep -q .; then
            grep -nE '\$[({]?(PROJECT_DIR|INSTALL_DIR)[)}]?/docker-compose\.yml' "${f}" \
                | grep -v '/deploy/docker-compose\.yml' | sed "s#^#${f}: B1 #"
            hits=1
        fi
        # B1b: `-f <path>docker-compose.yml` whose path is not deploy/.
        if grep -nE '[[:space:]]-f[[:space:]]+[^[:space:]]*docker-compose\.yml' "${f}" \
            | grep -v 'deploy/docker-compose\.yml' | grep -q .; then
            grep -nE '[[:space:]]-f[[:space:]]+[^[:space:]]*docker-compose\.yml' "${f}" \
                | grep -v 'deploy/docker-compose\.yml' | sed "s#^#${f}: B1 #"
            hits=1
        fi
        # B2: compose invocation targeting a retired service name db|api.
        if grep -nE 'COMPOSE_CMD' "${f}" \
            | grep -E '[[:space:]](db|api)([[:space:]]|$)' | grep -q .; then
            grep -nE 'COMPOSE_CMD' "${f}" \
                | grep -E '[[:space:]](db|api)([[:space:]]|$)' | sed "s#^#${f}: B2 #"
            hits=1
        fi
    done
    return "${hits}"
}

# check_no_retired_refs PROJECT_ROOT [SCRIPTS_DIR_OVERRIDE]
check_no_retired_refs() {
    local root="$1" scripts_override="${2:-}"
    local -a targets=()
    while IFS= read -r t; do targets+=("${t}"); done < <(collect_scan_targets "${root}" "${scripts_override}")
    local out rc
    out="$(scan_files_for_retired_refs "${targets[@]}")" && rc=0 || rc=$?
    if [[ "${rc}" -eq 0 ]]; then
        echo "EVIDENCE: scanned ${#targets[@]} files (scripts + systemd + Makefile); no retired-root-compose reference"
        echo "PASS: every compose reference resolves to the canonical file / postgres|app services (Sec 11.4.124)."
        return 0
    fi
    echo "EVIDENCE: retired-root-compose reference(s) found:"
    printf 'EVIDENCE:   %s\n' "${out}"
    echo "FAIL: a script/systemd unit/Makefile still references the retired root compose (G13, Sec 11.4.124)."
    return 1
}

# check_compose_parses COMPOSE_FILE  (SKIP when no rootless engine)
check_compose_parses() {
    local file="$1"
    local -a engine=()
    if command -v podman >/dev/null 2>&1 && podman compose version >/dev/null 2>&1; then
        engine=(podman compose)
    elif command -v podman-compose >/dev/null 2>&1; then
        engine=(podman-compose)
    elif command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
        engine=(docker compose)
    fi
    if [[ ${#engine[@]} -eq 0 ]]; then
        echo "EVIDENCE: no rootless compose engine (podman compose / podman-compose / docker compose) on PATH"
        echo "SKIP: cannot parse-validate the canonical compose without a rootless engine (Sec 11.4.3)."
        return 0
    fi
    echo "EVIDENCE: parsing ${file} with: ${engine[*]} (all profiles)"
    if "${engine[@]}" -f "${file}" --profile app --profile monitoring config >/dev/null 2>&1; then
        echo "PASS: canonical compose parses cleanly (all profiles)."
        return 0
    fi
    echo "FAIL: canonical compose did NOT parse:"
    "${engine[@]}" -f "${file}" --profile app --profile monitoring config 2>&1 | sed 's/^/FAIL:   /' || true
    return 1
}

run_checks() {
    local root="$1"
    local rc=0
    echo "== G13 canonical-compose gate: ${root} =="
    echo
    echo "-- Check A: single canonical compose file --"
    check_single_compose "${root}" || rc=1
    echo
    echo "-- Check B: no reference to the retired root compose --"
    check_no_retired_refs "${root}" || rc=1
    echo
    echo "-- Check C: canonical compose parses (all profiles) --"
    check_compose_parses "${root}/${CANONICAL_REL}" || rc=1
    echo
    if [[ "${rc}" -eq 0 ]]; then
        echo "OVERALL: PASS (G13 canonical-compose runtime signature satisfied)."
    else
        echo "OVERALL: FAIL (G13 canonical-compose runtime signature NOT satisfied)."
    fi
    return "${rc}"
}

run_selftest() {
    local scratch_dir
    scratch_dir="$(mktemp -d "${TMPDIR:-/tmp}/check_compose_canonical.XXXXXX")"
    # shellcheck disable=SC2064
    trap "rm -rf '${scratch_dir}'" EXIT

    local overall=0
    echo "== Sec 1.1 paired mutation test: check_compose_canonical.sh =="
    echo
    echo "-- assertion 1: reference check PASSes on the real scripts/ + deploy/systemd/ + Makefile --"
    if check_no_retired_refs "${DEFAULT_PROJECT_ROOT}"; then
        echo "assertion 1: PASS (gate correctly passes the fixed, canonical tree)"
    else
        echo "assertion 1: FAIL (gate did NOT pass the real, fixed tree - regression)"
        overall=1
    fi

    echo
    echo "-- assertion 2: reference check FAILs on a throwaway mutant scratch copy --"
    local mutant_scripts="${scratch_dir}/scripts"
    cp -r -- "${DEFAULT_PROJECT_ROOT}/scripts" "${mutant_scripts}"
    # Re-introduce ONE dangling reference to the retired root compose into a
    # scratch copy - the real scripts/ is never touched.
    printf '\n# MUTATION (Sec 1.1): dangling retired-root-compose reference\ncp "$PROJECT_DIR/docker-compose.yml" "$package_dir/"\n' \
        >> "${mutant_scripts}/package.sh"
    if grep -q 'cp "\$PROJECT_DIR/docker-compose.yml"' "${mutant_scripts}/package.sh"; then
        if check_no_retired_refs "${DEFAULT_PROJECT_ROOT}" "${mutant_scripts}"; then
            echo "assertion 2: FAIL (gate incorrectly PASSed the mutant - gate is not load-bearing)"
            overall=1
        else
            echo "assertion 2: PASS (gate correctly FAILs the re-introduced dangling reference)"
        fi
    else
        echo "assertion 2: FAIL (could not apply the mutation - inconclusive, treated as failure per Sec 11.4.6)"
        overall=1
    fi

    echo
    if [[ "${overall}" -eq 0 ]]; then
        echo "OVERALL SELFTEST: PASS (gate is genuinely load-bearing - PASS on real tree, FAIL on mutant)"
    else
        echo "OVERALL SELFTEST: FAIL"
    fi
    return "${overall}"
}

project_root="${DEFAULT_PROJECT_ROOT}"
mode="check"
while [[ $# -gt 0 ]]; do
    case "$1" in
        -h|--help) usage; exit 0 ;;
        --selftest) mode="selftest"; shift ;;
        --project-root)
            [[ $# -ge 2 ]] || { echo "check_compose_canonical.sh: --project-root requires a path" >&2; exit 2; }
            project_root="$(cd "$2" && pwd)"; shift 2 ;;
        *) echo "check_compose_canonical.sh: unknown argument: $1" >&2; usage >&2; exit 2 ;;
    esac
done

if [[ "${mode}" == "selftest" ]]; then
    run_selftest
    exit $?
fi

run_checks "${project_root}"
exit $?
