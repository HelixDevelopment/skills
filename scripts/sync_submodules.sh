#!/usr/bin/env bash
# =============================================================================
# HelixKnowledge Skill Graph Service - Dependency Sync Script
# =============================================================================
# Reads helix-deps.yaml and resolves each dependency to ONE canonical
# location, per Constitution:
#   - §11.4.28C  ONE canonical location per dependency. If a dependency is
#                already vendored at a parent-ecosystem root (either
#                <root>/<name>/ or <root>/submodules/<name>/), that copy is
#                referenced and NO duplicate rival copy is created here.
#                Nested own-org submodule chains are FORBIDDEN.
#   - §11.4.29   Dependency names are lowercase snake_case.
#   - §11.4.31   This project ships helix-deps.yaml as its dependency
#                manifest; this script is the sync entry point for it.
#
# Otherwise the dependency is vendored locally as a "grouped" git submodule
# at <repo_root>/submodules/<name>/ (or "<repo_root>/<name>/" if the
# manifest declares layout: ungrouped), then fetched and fast-forwarded to
# the ref declared in the manifest.
#
# DRY-RUN BY DEFAULT: only prints the planned actions. Pass --apply to
# actually mutate the working tree (git submodule add/update, fetch,
# checkout + fast-forward merge).
#
# Usage:
#   sync_submodules.sh [--apply] [--manifest PATH] [--ecosystem-root DIR]...
#
# Ecosystem roots (parent locations that may already vendor a dependency)
# are supplied explicitly, either via one or more --ecosystem-root flags or
# via the colon-separated HELIX_ECOSYSTEM_ROOTS environment variable. None
# are assumed by default, so behavior is deterministic across machines.
# =============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
MANIFEST="${PROJECT_ROOT}/helix-deps.yaml"
APPLY=0
ECOSYSTEM_ROOTS=()

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
BOLD='\033[1m'
NC='\033[0m'

log_info()  { printf '%b[INFO]%b %s\n' "$BLUE" "$NC" "$1"; }
log_ok()    { printf '%b[OK]%b %s\n' "$GREEN" "$NC" "$1"; }
log_warn()  { printf '%b[WARN]%b %s\n' "$YELLOW" "$NC" "$1"; }
log_error() { printf '%b[ERROR]%b %s\n' "$RED" "$NC" "$1" >&2; }
log_plan()  { printf '%b[PLAN]%b %s\n' "$BOLD" "$NC" "$1"; }

# ------------------------------------------------------------------------
# Strict allow-list validation at the trust boundary (defense-in-depth).
# The manifest is semi-trusted, but validating here makes two classes of
# defect structurally impossible regardless of manifest contents:
#   * path traversal — 'name'/'layout' build local_path and the ecosystem
#     search paths; a '../' or '/' in 'name' would escape the repo tree.
#   * git argument injection — 'ref'/'ssh_url' are passed as positional
#     arguments to git; a value beginning with '-' would be parsed as an
#     option (e.g. an --upload-pack/--output style flag), not data.
# On any violation we fail closed (exit 1) rather than proceed.
# ------------------------------------------------------------------------
validate_dep_fields() {
  local name="$1" ssh_url="$2" ref="$3" layout="$4"

  # Constitution §11.4.29: dependency names are lowercase snake_case. This
  # regex also forbids '/', '.', '..', and a leading '-', closing the path
  # traversal vector.
  if [[ ! "$name" =~ ^[a-z0-9]+(_[a-z0-9]+)*$ ]]; then
    log_error "Invalid dependency name '${name}': must be lowercase snake_case [a-z0-9_] (Constitution §11.4.29)."
    exit 1
  fi

  # Git ref: must start alphanumeric (no leading '-' => no option injection),
  # contain only [A-Za-z0-9._/-], and never contain '..' (ref-escape / RCE via
  # crafted refspec). Rejects whitespace implicitly.
  if [[ ! "$ref" =~ ^[A-Za-z0-9][A-Za-z0-9._/-]*$ ]] || [[ "$ref" == *".."* ]]; then
    log_error "Invalid ref '${ref}' for '${name}': must start alphanumeric, only [A-Za-z0-9._/-], no '..'."
    exit 1
  fi

  # Layout is a closed enum.
  case "$layout" in
    grouped|ungrouped) ;;
    *) log_error "Invalid layout '${layout}' for '${name}': must be 'grouped' or 'ungrouped'."; exit 1 ;;
  esac

  # URL: never begins with '-' (git option injection) or contains whitespace,
  # and must match a recognized git transport (scp-like host:path, ssh://,
  # git://, https://, http://, file://).
  if [[ "$ssh_url" == -* ]] || [[ "$ssh_url" =~ [[:space:]] ]]; then
    log_error "Invalid url '${ssh_url}' for '${name}': must not start with '-' or contain whitespace."
    exit 1
  fi
  if [[ ! "$ssh_url" =~ ^(ssh://|git://|https://|http://|file://|[A-Za-z0-9._-]+@[A-Za-z0-9._-]+:) ]]; then
    log_error "Invalid url '${ssh_url}' for '${name}': not a recognized git URL form."
    exit 1
  fi
}

usage() {
  cat <<'EOF'
Usage: sync_submodules.sh [--apply] [--manifest PATH] [--ecosystem-root DIR]...

  --apply               Mutate the working tree (default: dry-run/plan only).
  --manifest PATH       Path to helix-deps.yaml
                        (default: <project_root>/helix-deps.yaml).
  --ecosystem-root DIR  A parent-ecosystem root to search for an already
                        vendored canonical copy of a dependency, checking
                        both DIR/<name> and DIR/submodules/<name>. May be
                        given multiple times. Also read from the
                        colon-separated HELIX_ECOSYSTEM_ROOTS env var.
  -h, --help            Show this help.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --apply)
      APPLY=1
      shift
      ;;
    --manifest)
      [[ $# -ge 2 ]] || { log_error "--manifest requires a PATH argument"; exit 1; }
      MANIFEST="$2"
      shift 2
      ;;
    --ecosystem-root)
      [[ $# -ge 2 ]] || { log_error "--ecosystem-root requires a DIR argument"; exit 1; }
      ECOSYSTEM_ROOTS+=("$2")
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      log_error "Unknown argument: $1"
      usage
      exit 1
      ;;
  esac
done

if [[ -n "${HELIX_ECOSYSTEM_ROOTS:-}" ]]; then
  IFS=':' read -r -a env_roots <<<"${HELIX_ECOSYSTEM_ROOTS}"
  ECOSYSTEM_ROOTS+=("${env_roots[@]}")
fi

if [[ ! -f "${MANIFEST}" ]]; then
  log_error "Manifest not found: ${MANIFEST}"
  exit 1
fi

if ! command -v python3 >/dev/null 2>&1; then
  log_error "python3 is required to parse ${MANIFEST}"
  exit 1
fi

if ! python3 -c 'import yaml' >/dev/null 2>&1; then
  log_error "python3 module 'yaml' (PyYAML) is required to parse ${MANIFEST}"
  exit 1
fi

if ! command -v git >/dev/null 2>&1; then
  log_error "git is required"
  exit 1
fi

# ------------------------------------------------------------------------
# Parse helix-deps.yaml into tab-separated lines: name / ssh_url / ref / layout
# ------------------------------------------------------------------------
parse_manifest() {
  local manifest="$1"
  python3 - "${manifest}" <<'PYEOF'
import sys
import yaml

with open(sys.argv[1], "r", encoding="utf-8") as fh:
    data = yaml.safe_load(fh) or {}

for dep in (data.get("deps") or []):
    name = str(dep.get("name", "")).strip()
    ssh_url = str(dep.get("ssh_url", "")).strip()
    ref = str(dep.get("ref", "main")).strip()
    layout = str(dep.get("layout", "grouped")).strip()
    if not name or not ssh_url:
        continue
    print("\t".join([name, ssh_url, ref, layout]))
PYEOF
}

# Resolve PATH relative to BASE, even if PATH does not exist yet.
relative_path() {
  local target="$1" base="$2"
  if command -v realpath >/dev/null 2>&1; then
    realpath -m --relative-to="${base}" "${target}"
  else
    python3 -c 'import os, sys; print(os.path.relpath(sys.argv[1], sys.argv[2]))' "${target}" "${base}"
  fi
}

# Return 0 and print the path if a canonical vendored copy of NAME is found
# under any configured ecosystem root; return 1 otherwise.
find_ecosystem_copy() {
  local name="$1" root candidate
  for root in "${ECOSYSTEM_ROOTS[@]}"; do
    for candidate in "${root}/submodules/${name}" "${root}/${name}"; do
      if [[ -e "${candidate}/.git" ]]; then
        printf '%s\n' "${candidate}"
        return 0
      fi
    done
  done
  return 1
}

sync_existing_checkout() {
  local path="$1" ref="$2"
  if [[ ${APPLY} -eq 1 ]]; then
    log_plan "Fetching '${ref}' and fast-forwarding at ${path}..."
    git -C "${path}" fetch origin -- "${ref}"
    git -C "${path}" checkout "${ref}" --
    git -C "${path}" merge --ff-only "origin/${ref}"
    log_ok "Synced to origin/${ref} at ${path}"
  else
    log_plan "Would run: git -C ${path} fetch origin ${ref}"
    log_plan "Would run: git -C ${path} checkout ${ref}"
    log_plan "Would run: git -C ${path} merge --ff-only origin/${ref}"
  fi
}

add_new_submodule() {
  local name="$1" ssh_url="$2" ref="$3" local_path="$4"
  local repo_root rel_path
  repo_root="$(git -C "${PROJECT_ROOT}" rev-parse --show-toplevel 2>/dev/null || printf '%s' "${PROJECT_ROOT}")"
  rel_path="$(relative_path "${local_path}" "${repo_root}")"
  if [[ ${APPLY} -eq 1 ]]; then
    log_plan "Adding submodule '${name}' -> ${rel_path} (${ssh_url}@${ref})..."
    git -C "${repo_root}" submodule add -b "${ref}" -- "${ssh_url}" "${rel_path}"
    git -C "${repo_root}" submodule update --init --recursive -- "${rel_path}"
    log_ok "'${name}' added as submodule at ${local_path}"
  else
    log_plan "Would run: git -C ${repo_root} submodule add -b ${ref} -- ${ssh_url} ${rel_path}"
    log_plan "Would run: git -C ${repo_root} submodule update --init --recursive -- ${rel_path}"
  fi
}

process_dep() {
  local name="$1" ssh_url="$2" ref="$3" layout="$4"
  local local_path found_path

  if [[ "${layout}" == "grouped" ]]; then
    local_path="${PROJECT_ROOT}/submodules/${name}"
  else
    local_path="${PROJECT_ROOT}/${name}"
  fi

  echo "--- ${name} ---"

  if found_path="$(find_ecosystem_copy "${name}")"; then
    log_info "Already vendored at canonical parent-ecosystem location:"
    log_info "  ${found_path}"
    log_info "No duplicate rival copy will be created under ${local_path}"
    log_info "(Constitution §11.4.28C: ONE canonical location per dependency)."
    sync_existing_checkout "${found_path}" "${ref}"
  elif [[ -e "${local_path}/.git" ]]; then
    log_info "Canonical location (this repo, ${layout}): ${local_path}"
    log_info "Already vendored locally; will be kept in sync."
    sync_existing_checkout "${local_path}" "${ref}"
  else
    log_info "Not found at any configured ecosystem root or locally."
    log_info "Canonical location (this repo, ${layout}): ${local_path}"
    add_new_submodule "${name}" "${ssh_url}" "${ref}" "${local_path}"
  fi

  echo
}

# ------------------------------------------------------------------------
# Main
# ------------------------------------------------------------------------
mapfile -t dep_lines < <(parse_manifest "${MANIFEST}")

if [[ ${#dep_lines[@]} -eq 0 ]]; then
  log_warn "No dependencies declared in ${MANIFEST}"
  exit 0
fi

log_info "Loaded ${#dep_lines[@]} dependency declaration(s) from ${MANIFEST}"
if [[ ${#ECOSYSTEM_ROOTS[@]} -gt 0 ]]; then
  log_info "Ecosystem roots to check: ${ECOSYSTEM_ROOTS[*]}"
else
  log_warn "No --ecosystem-root/HELIX_ECOSYSTEM_ROOTS configured; every dependency not already vendored locally will resolve to submodules/<name> in this repo."
fi
if [[ ${APPLY} -eq 0 ]]; then
  log_warn "DRY-RUN mode (default). No changes will be made. Pass --apply to mutate."
else
  log_warn "APPLY mode. Changes WILL be made to the working tree."
fi
echo

for line in "${dep_lines[@]}"; do
  IFS=$'\t' read -r dep_name dep_url dep_ref dep_layout <<<"${line}"
  validate_dep_fields "${dep_name}" "${dep_url}" "${dep_ref}" "${dep_layout}"
  process_dep "${dep_name}" "${dep_url}" "${dep_ref}" "${dep_layout}"
done

log_ok "Sync plan complete for ${#dep_lines[@]} dependency declaration(s)."
if [[ ${APPLY} -eq 0 ]]; then
  log_info "This was a DRY-RUN. Re-run with --apply to execute the planned actions."
fi
