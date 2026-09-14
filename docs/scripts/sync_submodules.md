# `scripts/sync_submodules.sh` — dependency sync from `helix-deps.yaml`

**Revision:** 1
**Last modified:** 2026-07-16T00:00:00Z

## Overview

Reads `project/helix-deps.yaml` and resolves each declared dependency to
**one canonical location** per Constitution §11.4.28(C): if a dependency
is already vendored at a parent-ecosystem root (either `<root>/<name>/` or
`<root>/submodules/<name>/`), that existing copy is referenced and kept in
sync — no duplicate rival copy is created. Otherwise the dependency is
vendored locally as a git submodule at `<repo_root>/submodules/<name>/`
(or `<repo_root>/<name>/` if the manifest declares `layout: ungrouped`),
then fetched and fast-forwarded to the ref declared in the manifest.

**Dry-run by default** — the script only prints planned `git`
actions unless `--apply` is passed.

## Prerequisites

- `python3` with the `PyYAML` module (`python3 -c 'import yaml'` must
  succeed) — used to parse `helix-deps.yaml`.
- `git`.
- `realpath` (optional — falls back to a `python3 os.path.relpath`
  one-liner if absent).

## Usage

```
sync_submodules.sh [--apply] [--manifest PATH] [--ecosystem-root DIR]...
```

| Option | Effect |
|---|---|
| `--apply` | Mutate the working tree (default: dry-run/plan only). |
| `--manifest PATH` | Path to `helix-deps.yaml` (default: `<project_root>/helix-deps.yaml`). |
| `--ecosystem-root DIR` | A parent-ecosystem root to search for an already-vendored canonical copy of a dependency (checks both `DIR/<name>` and `DIR/submodules/<name>`). May be given multiple times; also read from the colon-separated `HELIX_ECOSYSTEM_ROOTS` environment variable. |
| `-h`, `--help` | Print usage and exit 0. |

### Examples

```bash
scripts/sync_submodules.sh                                   # dry-run, no ecosystem roots configured
scripts/sync_submodules.sh --ecosystem-root /home/user/Factory/projects
scripts/sync_submodules.sh --apply                            # actually add/sync submodules
HELIX_ECOSYSTEM_ROOTS=/a:/b scripts/sync_submodules.sh --apply
```

## Inputs

`project/helix-deps.yaml` (or the path given via `--manifest`) — a YAML
document with a top-level `deps:` list, each entry carrying `name`,
`ssh_url`, `ref` (default `main`), and `layout` (`grouped`, default, or
`ungrouped`).

## Outputs

In dry-run mode (default): a printed plan of every `git` command that
would be run, per dependency, with no filesystem changes. In `--apply`
mode: actual `git submodule add` / `git fetch` / `git checkout` / `git
merge --ff-only` invocations against the resolved canonical path for each
dependency.

## Side-effects

**Dry-run (default):** none — read-only, prints only.
**`--apply`:** may run `git submodule add` (new dependency) or `git fetch`
+ `git checkout` + `git merge --ff-only` (existing checkout, whether at an
ecosystem root or locally) against the target repository.

## Edge cases

- **Invalid dependency name/ref/layout/URL in the manifest:**
  `validate_dep_fields()` fails closed (`exit 1`) on any of: a `name` not
  matching lowercase snake_case `^[a-z0-9]+(_[a-z0-9]+)*$` (per
  Constitution §11.4.29 — this also structurally forbids `/`, `.`, `..`,
  and a leading `-`, closing a path-traversal vector); a `ref` not
  matching `^[A-Za-z0-9][A-Za-z0-9._/-]*$` or containing `..`; a `layout`
  outside the closed set `{grouped, ungrouped}`; a `ssh_url` starting with
  `-` or containing whitespace (git option-injection defense), or not
  matching a recognized git transport (`ssh://`, `git://`, `https://`,
  `http://`, `file://`, or the scp-like `user@host:path` form).
- **`helix-deps.yaml` not found:** `log_error` + `exit 1` before any
  parsing is attempted.
- **`python3` or its `yaml` module missing:** `log_error` + `exit 1`
  (checked explicitly before parsing, with a specific message for each
  missing piece).
- **`git` missing:** `log_error` + `exit 1`.
- **No dependencies declared:** logs a warning and exits 0 (not an error).
- **No `--ecosystem-root`/`HELIX_ECOSYSTEM_ROOTS` configured:** logs a
  warning that every not-already-locally-vendored dependency will resolve
  to `submodules/<name>` in this repo (i.e. no ecosystem-root reuse is
  possible without at least one root configured).
- **Dependency already vendored at an ecosystem root:** references that
  copy and syncs it in place — never creates a duplicate rival copy under
  this repo's own `submodules/<name>`, per §11.4.28(C).
- **Dependency already vendored locally (this repo):** syncs the existing
  local checkout rather than re-adding it as a submodule.

## Internal behavior

1. Parses `--apply` / `--manifest` / `--ecosystem-root` (repeatable) /
   `--help`; merges `HELIX_ECOSYSTEM_ROOTS` (colon-separated) into the
   ecosystem-roots list.
2. Verifies the manifest file, `python3`+PyYAML, and `git` are all
   present.
3. `parse_manifest()` — a `python3` heredoc reads the YAML and emits one
   tab-separated `name / ssh_url / ref / layout` line per declared
   dependency.
4. For each parsed line: `validate_dep_fields()` (fail-closed allow-list
   validation) then `process_dep()`:
   - Computes the local canonical path (`submodules/<name>` for
     `grouped`, `<name>` for `ungrouped`).
   - `find_ecosystem_copy()` — searches every configured ecosystem root
     for an existing `.git` at `<root>/submodules/<name>` or
     `<root>/<name>`; if found, `sync_existing_checkout()` fetches +
     checks out + fast-forward-merges that copy to the declared ref.
   - Else if the local canonical path already has a `.git`,
     `sync_existing_checkout()` is run against it instead.
   - Else `add_new_submodule()` runs `git submodule add -b <ref> --
     <ssh_url> <rel_path>` (relative to the repo's actual toplevel, via
     `git rev-parse --show-toplevel`) followed by `git submodule update
     --init --recursive`.
5. Prints a final summary line noting dry-run vs apply mode.

## Dependencies

`bash`, `python3` + `PyYAML`, `git`, `realpath` (optional).

## Cross-references

`project/helix-deps.yaml` (the manifest this script consumes); Constitution
§11.4.28(C) (one-canonical-location rule), §11.4.29 (lowercase snake_case
naming), §11.4.31 (submodule dependency manifest mandate — `helix-deps.yaml`
is this project's manifest under that mandate).

## Last verified

2026-07-16, against `project/scripts/sync_submodules.sh` (12000 bytes,
last modified 2026-07-15).
