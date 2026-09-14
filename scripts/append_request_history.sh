#!/usr/bin/env bash
# =============================================================================
# append_request_history.sh - UserPromptSubmit hook: mechanically append a
# newest-first Sec 11.4.208 operator-request-history row on every new operator
# prompt (Gap register G38; Constitution Sec 11.4.208(D) auto-capture-hook
# facet of the request-history mandate)
# =============================================================================
# Purpose:
#   Sec 11.4.208 requires a project-local, always-in-sync, append-only,
#   newest-first operator-request-history ledger (this project's is
#   requests/history.md) recording, per request: (1) full content, (2) an
#   accepted timestamp with an EXPLICIT timezone, (3) the Track, (4) the
#   Alias, (5) the model + effort used. The ledger existed (G38 filed
#   2026-07-15) as a RECONSTRUCTION-ONLY document - no mechanism appended a
#   row at accept-time. This script is that mechanism: wired as a
#   `UserPromptSubmit` hook in .claude/settings.json, it reads the hook's
#   stdin JSON on every new operator prompt and appends one newest-first row.
#
#   HONEST FEASIBILITY (Sec 11.4.6 - investigated, never guessed, verified
#   against https://code.claude.com/docs/en/hooks.md + hooks-guide.md,
#   2026-07-16): a `UserPromptSubmit` hook's stdin JSON carries
#   `session_id` / `hook_event_name` / `cwd` / `permission_mode` /
#   `effort.level` / `prompt_id` / `transcript_path` / `prompt` (the last
#   confirmed by this repo's own
#   constitution/scripts/hooks/action_prefix_expand.sh, itself verified
#   against the same doc on 2026-06-09). The MODEL NAME is NOT exposed to
#   this hook event - the docs state only `SessionStart` MAY (not
#   guaranteed) carry a `model` field, and there is no `$CLAUDE_MODEL`
#   env var at all. Consequently this script NEVER fabricates a model
#   value - it records the literal string `UNKNOWN` for Model on every
#   auto-captured row, and captures a REAL effort value from
#   `.effort.level` (falling back to `$CLAUDE_EFFORT`) when present. Track
#   and Alias are derived deterministically per Sec 11.4.182: Track from
#   the hook's `.cwd` field matched against `/mnt/track<N>/...` (else the
#   honest `?`) plus the git branch at that cwd (else `?`); Alias from
#   `$CLAUDE_CONFIG_DIR`'s basename matched against `.claude-<alias>`
#   (else `?`). A field that cannot be determined is written literally
#   `?` / `UNKNOWN` - NEVER guessed (Sec 11.4.6).
#
#   This is a DELIBERATELY MINIMAL, HONEST auto-capture: it appends ONLY
#   the five Sec 11.4.208 mandated fields plus an explicit
#   `[AUTO-CAPTURED]` marker and an UNPROCESSED disposition - it never
#   invents a Requirement id or a curated Disposition narrative (those
#   remain a human/agent curation step, same as every existing
#   hand-curated row in requests/history.md).
#
# Usage:
#   As a live hook (wired in .claude/settings.json, Claude Code invokes
#   this automatically on every UserPromptSubmit event, feeding the event
#   JSON on stdin):
#     bash project/scripts/append_request_history.sh
#
#   Manual / test invocation (never touches the real ledger unless you
#   pass its real path explicitly):
#     echo '{"prompt":"hello","cwd":"/mnt/track2/proj","effort":{"level":"high"}}' \
#       | REQUEST_HISTORY_FILE=/path/to/scratch/history.md \
#         bash project/scripts/append_request_history.sh
#     bash project/scripts/append_request_history.sh --history-file PATH
#
#   Self-test (paired Sec 1.1 mutation-style check - proves the mechanism
#   is genuinely load-bearing against a throwaway scratch copy, NEVER the
#   real ledger):
#     bash project/scripts/append_request_history.sh --selftest
#
#   -h|--help    Show usage and exit.
#
# Inputs:
#   stdin: the UserPromptSubmit hook event JSON (Claude Code hooks
#     contract). Read once; if `.prompt` is absent/empty the script is a
#     silent no-op (nothing to log) - it never fabricates a row from
#     nothing.
#   --history-file PATH / $REQUEST_HISTORY_FILE (optional): overrides the
#     ledger path. Default (repo-relative, resolved from this script's own
#     location, mirroring this project's sibling scripts):
#     "<this-script-dir>/../../requests/history.md" i.e. the project's
#     real requests/history.md - ALWAYS override this for any test.
#   --anchor TEXT / $REQUEST_HISTORY_ANCHOR (optional): the exact heading
#     line the new entry is inserted immediately AFTER (default:
#     "## Request ledger (newest first)"). If the anchor line is not found
#     verbatim in the target file, the script FAILS CLOSED (Sec 11.4.6) -
#     it never guesses an insertion point and never silently appends to
#     the wrong place.
#   $CLAUDE_CONFIG_DIR (optional env, read for Alias derivation).
#
# Outputs:
#   The target ledger file gains exactly one new "### [AUTO-CAPTURED ...]"
#   block, inserted immediately after the anchor heading (newest-first),
#   with everything else in the file byte-identical. Diagnostics go to
#   stderr. In live-hook mode (default, no --selftest) the script ALWAYS
#   exits 0 - a housekeeping failure in this script MUST NEVER block an
#   operator's prompt (mirrors the fail-open discipline of the sibling
#   constitution/scripts/hooks/action_prefix_expand.sh hook; a
#   `UserPromptSubmit` hook exit code 2 BLOCKS the prompt per the Claude
#   Code hooks contract - this script never returns 2, and in hook mode
#   never returns non-zero at all). `--selftest` mode returns a real
#   0 (all assertions held) / 1 (a assertion failed) exit code, since
#   that mode is a test utility, not the live hook path.
#
# Side-effects:
#   Writes to a private mktemp file NEXT TO the target ledger, then
#   atomically renames it onto the target (temp-write-then-rename
#   discipline, Sec 9.2/Sec 11.4.180 style) - the target is NEVER
#   truncated or partially written in place. A short-lived `flock` (10s
#   timeout, skipped honestly if `flock` is unavailable) on a lock file
#   OUTSIDE the tracked tree - under "${TMPDIR:-/tmp}", named from a
#   stable hash (sha256sum, with a shasum/cksum/sanitized-path fallback
#   chain) of the RESOLVED target ledger path - serializes concurrent
#   invocations against the SAME target so two near-simultaneous prompts
#   never race each other's insert, without ever leaving a lock artifact
#   inside the repo tree. --selftest additionally creates and removes its
#   own throwaway scratch directory (mktemp -d, trap-cleaned on every
#   exit path per Sec 11.4.14) - it NEVER touches the real
#   requests/history.md.
#
# Dependencies:
#   bash >= 4, coreutils (mktemp, mv, dirname, basename, cat, date), git
#   (for the Track branch component; absence degrades to the honest `?`),
#   jq (preferred JSON field extraction) with an awk fallback mirroring
#   constitution/scripts/hooks/guard-forbidden-commands.sh's and
#   action_prefix_expand.sh's own local extractor pattern. `flock` is
#   OPTIONAL (locking is skipped, honestly, if absent). `sha256sum` is
#   OPTIONAL (used to name the out-of-tree lock file; falls back to
#   `shasum`, then `cksum`, then a sanitized-path name - locking still
#   works, just with a less compact lock filename, if all three are
#   absent).
#
# Cross-references:
#   docs/scripts/append_request_history.md (companion user guide);
#   requests/history.md (the ledger this hook appends to);
#   .claude/settings.json (wires this script as a `UserPromptSubmit` hook,
#   alongside the pre-existing `PreToolUse` guard-forbidden-commands.sh
#   entry - added, never removed);
#   GAPS_AND_RISKS_REGISTER.md G38 (the tracked item this script closes);
#   constitution/scripts/hooks/action_prefix_expand.sh (sibling
#   UserPromptSubmit hook - confirms `.prompt` field + fail-open
#   discipline); constitution/scripts/hooks/guard-forbidden-commands.sh
#   (sibling PreToolUse hook - shares the local jq/awk field-extractor
#   pattern); Constitution Sec 11.4.208 (the mandate), Sec 11.4.182 (Track+
#   Alias derivation), Sec 11.4.6 (no-guessing - honest `?`/`UNKNOWN`),
#   Sec 11.4.180/Sec 9.2 (temp-write-then-rename + lock discipline).
# Last verified: 2026-07-16
# =============================================================================
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEFAULT_HISTORY_FILE="${SCRIPT_DIR}/../../requests/history.md"
DEFAULT_ANCHOR="## Request ledger (newest first)"

usage() {
    cat <<'EOF'
Usage: append_request_history.sh [--history-file PATH] [--anchor TEXT]
       append_request_history.sh --selftest
       append_request_history.sh -h|--help

UserPromptSubmit hook: reads the hook event JSON from stdin and appends a
newest-first Sec 11.4.208 request-history row (Content/Accepted-timestamp+TZ/
Track/Alias/Model+effort) immediately after the ledger's anchor heading.
Model is always literal UNKNOWN (not exposed to this hook by Claude Code);
every other field not determinable is the honest `?` (Sec 11.4.6).

  --history-file PATH   Ledger file to append to (default: this project's
                         real requests/history.md, resolved relative to
                         this script). ALWAYS override for testing.
  --anchor TEXT         Heading line to insert after (default:
                         "## Request ledger (newest first)").
  --selftest            Paired Sec 1.1 style mutation check against a
                         throwaway scratch ledger. Never touches the real
                         requests/history.md.
  -h, --help            Show this help and exit.

Env overrides: $REQUEST_HISTORY_FILE, $REQUEST_HISTORY_ANCHOR.

Exit code: in default (live-hook) mode, ALWAYS 0 - a housekeeping failure
in this script must never block an operator prompt. In --selftest mode,
0 iff every assertion held, 1 otherwise.
EOF
}

# ---------------------------------------------------------------------------
# json_field PAYLOAD JQ_PATH AWK_KEY
#   Extracts a top-level-or-nested string field from a JSON payload.
#   jq preferred; falls back to a plain top-level-string awk scanner
#   (mirrors guard-forbidden-commands.sh / action_prefix_expand.sh's own
#   local extractor - each hook keeps its own copy on purpose, Sec 11.4.28
#   decoupling: no cross-hook sourcing). The awk fallback only handles the
#   flat `"key":"value"` shape - it is used for `.prompt` (always
#   top-level); `.effort.level` uses jq only (nested), honestly falling
#   back to empty (never guessed) when jq is absent.
# ---------------------------------------------------------------------------
json_field() {
    local payload="$1" jqpath="$2" awkkey="${3:-}"
    if command -v jq >/dev/null 2>&1; then
        printf '%s' "$payload" | jq -r "${jqpath} // empty" 2>/dev/null || true
        return 0
    fi
    [ -n "$awkkey" ] || return 0
    printf '%s' "$payload" | awk -v key="$awkkey" '
        BEGIN { RS="\0" }
        {
            s = $0
            idx = index(s, "\"" key "\"")
            if (idx == 0) { exit }
            rest = substr(s, idx + length(key) + 2)
            sub(/^[ \t\r\n]*:[ \t\r\n]*/, "", rest)
            if (substr(rest, 1, 1) != "\"") { exit }
            rest = substr(rest, 2)
            out = ""; i = 1; n = length(rest)
            while (i <= n) {
                c = substr(rest, i, 1)
                if (c == "\\") {
                    nx = substr(rest, i + 1, 1)
                    if (nx == "n") out = out "\n"
                    else if (nx == "t") out = out "\t"
                    else if (nx == "r") out = out "\r"
                    else if (nx == "\"") out = out "\""
                    else if (nx == "\\") out = out "\\"
                    else if (nx == "/") out = out "/"
                    else out = out nx
                    i += 2; continue
                }
                if (c == "\"") break
                out = out c; i += 1
            }
            printf "%s", out
        }
    '
}

# derive_track CWD -> prints "T<N-or-?>/<branch-or-?>"
derive_track() {
    local cwd="$1" track_num="?" branch="?"
    if [[ "$cwd" =~ ^/mnt/track([0-9]+)(/|$) ]]; then
        track_num="${BASH_REMATCH[1]}"
    fi
    if [ -n "$cwd" ] && [ -d "$cwd" ] && command -v git >/dev/null 2>&1; then
        local b
        b="$(git -C "$cwd" rev-parse --abbrev-ref HEAD 2>/dev/null || true)"
        [ -n "$b" ] && branch="$b"
    fi
    printf 'T%s/%s' "$track_num" "$branch"
}

# derive_alias -> prints "<alias>" or "?"
derive_alias() {
    local cfgdir="${CLAUDE_CONFIG_DIR:-}"
    if [ -n "$cfgdir" ]; then
        local base="${cfgdir##*/}"
        if [[ "$base" =~ ^\.claude-(.+)$ ]]; then
            printf '%s' "${BASH_REMATCH[1]}"
            return 0
        fi
    fi
    printf '?'
}

# derive_effort PAYLOAD -> prints the effort level or "UNKNOWN"
derive_effort() {
    local payload="$1" effort
    effort="$(json_field "$payload" ".effort.level" "")"
    if [ -z "$effort" ]; then
        effort="${CLAUDE_EFFORT:-}"
    fi
    [ -n "$effort" ] || effort="UNKNOWN"
    printf '%s' "$effort"
}

# build_entry PROMPT TRACK ALIAS EFFORT -> prints the markdown block (with
# a trailing blank line) to stdout.
build_entry() {
    local prompt="$1" track="$2" alias="$3" effort="$4"
    local ts date_part time_part flat
    ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    date_part="${ts%%T*}"
    time_part="${ts#*T}"; time_part="${time_part%Z}"
    # Collapse embedded newlines/CRs so the entry stays a single markdown
    # list-item value (multi-line continuation is a documented non-goal -
    # see docs/scripts/append_request_history.md Edge cases).
    flat="$(printf '%s' "$prompt" | tr '\n\r' '  ' | sed -E 's/ +/ /g; s/^ //; s/ $//')"
    printf '### [AUTO-CAPTURED Sec 11.4.208(D)] %s\n' "$ts"
    printf -- '- **Content (verbatim, hook-captured):** "%s"\n' "$flat"
    printf -- '- **Accepted:** %s, %s, UTC \xc2\xb7 **Track:** %s \xc2\xb7 **Alias:** %s \xc2\xb7 **Model+effort:** UNKNOWN / %s\n' \
        "$date_part" "$time_part" "$track" "$alias" "$effort"
    printf -- '- **Disposition:** UNPROCESSED \xe2\x80\x94 mechanically captured by `append_request_history.sh`\n'
    printf '  (Sec 11.4.208(D) auto-capture hook; Model is UNKNOWN because Claude Code\n'
    printf '  does not expose the model name to UserPromptSubmit hooks); requirement\n'
    printf '  mapping + curation pending.\n'
    printf '\n'
}

# lock_path_for_target RESOLVED_TARGET_PATH -> prints an OUT-OF-TREE lock
#   file path under "${TMPDIR:-/tmp}", keyed on a stable hash of the
#   resolved target path (so concurrent invocations against the SAME
#   target still serialize on the SAME lock file, while the lock never
#   lands inside the tracked repo tree - Sec 11.4.30/Sec 11.4.6). Prefers
#   sha256sum, falls back to shasum/cksum, and as a last resort a
#   sanitized (non-hash) form of the path itself - never fails to produce
#   a usable lock path.
lock_path_for_target() {
    local resolved="$1" hash
    if command -v sha256sum >/dev/null 2>&1; then
        hash="$(printf '%s' "$resolved" | sha256sum | awk '{print $1}')"
    elif command -v shasum >/dev/null 2>&1; then
        hash="$(printf '%s' "$resolved" | shasum -a 256 | awk '{print $1}')"
    elif command -v cksum >/dev/null 2>&1; then
        hash="$(printf '%s' "$resolved" | cksum | awk '{print $1"-"$2}')"
    else
        hash="$(printf '%s' "$resolved" | tr -c 'A-Za-z0-9' '_')"
    fi
    printf '%s/append_request_history.%s.lock' "${TMPDIR:-/tmp}" "$hash"
}

# insert_after_anchor FILE ANCHOR ENTRY_FILE_PATH
#   Temp-write-then-rename insertion. ENTRY_FILE_PATH is a private temp
#   file (built by the caller) holding the exact block of lines to
#   insert - passing it as a plain file path (rather than embedding
#   arbitrary prompt content into an awk -v string) avoids any awk
#   quoting/escaping pitfalls. Returns 0 on success, 1 on any failure
#   (anchor not found, dir unwritable, etc.) - NEVER guesses an insertion
#   point (Sec 11.4.6). Creates FILE fresh (with a minimal header + the
#   anchor) if it does not exist at all - a documented bootstrap edge
#   case, never used against the real tracked ledger (which always
#   exists).
insert_after_anchor() {
    local file="$1" anchor="$2" entry_file="$3"
    local dir tmp lockfile lockfd_ok=0 resolved_dir resolved_target

    dir="$(dirname "$file")"
    if [ ! -d "$dir" ]; then
        echo "append_request_history: target directory does not exist: $dir" >&2
        return 1
    fi

    resolved_dir="$(cd "$dir" && pwd)"
    resolved_target="${resolved_dir}/$(basename "$file")"
    lockfile="$(lock_path_for_target "$resolved_target")"
    if command -v flock >/dev/null 2>&1; then
        exec 9>"$lockfile" 2>/dev/null && lockfd_ok=1
        if [ "$lockfd_ok" -eq 1 ]; then
            if ! flock -w 10 9; then
                echo "append_request_history: could not acquire lock within 10s: $lockfile" >&2
                return 1
            fi
        fi
    fi

    if [ ! -f "$file" ]; then
        tmp="$(mktemp "${dir}/.append_request_history.bootstrap.XXXXXX")" || return 1
        {
            printf '# Operator Request History (bootstrap - Sec 11.4.208)\n\n'
            printf '%s\n\n' "$anchor"
            cat "$entry_file"
        } > "$tmp"
        mv -f "$tmp" "$file"
        return 0
    fi

    if ! grep -qxF "$anchor" "$file"; then
        echo "append_request_history: anchor not found verbatim in $file: $anchor" >&2
        return 1
    fi

    tmp="$(mktemp "${dir}/.append_request_history.XXXXXX")" || return 1
    local injected_marker="${tmp}.injected"
    rm -f "$injected_marker"
    awk -v anchor="$anchor" -v entryfile="$entry_file" -v markerfile="$injected_marker" '
        BEGIN {
            n = 0
            while ((getline eline < entryfile) > 0) {
                entry[n++] = eline
            }
            close(entryfile)
        }
        {
            print
            if (!injected && $0 == anchor) {
                print ""
                for (i = 0; i < n; i++) print entry[i]
                injected = 1
                print "1" > markerfile
            }
        }
    ' "$file" > "$tmp"

    if [ ! -f "$injected_marker" ]; then
        rm -f "$tmp"
        echo "append_request_history: insertion did not occur (unexpected - anchor matched by grep but not by awk)" >&2
        return 1
    fi
    rm -f "$injected_marker"

    mv -f "$tmp" "$file"
    return 0
}

main() {
    local mode="hook" history_file="" anchor=""

    while [ $# -gt 0 ]; do
        case "$1" in
            -h|--help) usage; exit 0 ;;
            --selftest) mode="selftest"; shift ;;
            --history-file)
                [ $# -ge 2 ] || { echo "append_request_history.sh: --history-file requires a path" >&2; exit 2; }
                history_file="$2"; shift 2 ;;
            --anchor)
                [ $# -ge 2 ] || { echo "append_request_history.sh: --anchor requires text" >&2; exit 2; }
                anchor="$2"; shift 2 ;;
            *)
                echo "append_request_history.sh: unknown argument: $1" >&2
                usage >&2
                exit 2 ;;
        esac
    done

    [ -n "$history_file" ] || history_file="${REQUEST_HISTORY_FILE:-$DEFAULT_HISTORY_FILE}"
    [ -n "$anchor" ] || anchor="${REQUEST_HISTORY_ANCHOR:-$DEFAULT_ANCHOR}"

    if [ "$mode" = "selftest" ]; then
        run_selftest
        exit $?
    fi

    # ---- live hook mode: fail-open, ALWAYS exit 0 ----
    local payload prompt track alias_val effort cwd entry entry_tmp overall=0
    payload="$(cat 2>/dev/null || true)"
    if [ -z "$payload" ]; then
        exit 0
    fi
    prompt="$(json_field "$payload" ".prompt" "prompt")"
    if [ -z "$prompt" ]; then
        exit 0
    fi
    cwd="$(json_field "$payload" ".cwd" "cwd")"
    [ -n "$cwd" ] || cwd="$(pwd)"
    track="$(derive_track "$cwd")"
    alias_val="$(derive_alias)"
    effort="$(derive_effort "$payload")"
    entry="$(build_entry "$prompt" "$track" "$alias_val" "$effort")"

    entry_tmp="$(mktemp "${TMPDIR:-/tmp}/append_request_history_entry.XXXXXX")" || exit 0
    printf '%s' "$entry" > "$entry_tmp"
    insert_after_anchor "$history_file" "$anchor" "$entry_tmp" || overall=1
    rm -f "$entry_tmp"

    if [ "$overall" -ne 0 ]; then
        echo "append_request_history: WARNING - request-history row NOT appended this turn (see stderr above); the prompt is NOT blocked (Sec 11.4.201 fail-open)." >&2
    fi
    exit 0
}

run_selftest() {
    local scratch_dir
    scratch_dir="$(mktemp -d "${TMPDIR:-/tmp}/append_request_history_selftest.XXXXXX")"
    # shellcheck disable=SC2064
    trap "rm -rf '${scratch_dir}'" EXIT

    local overall=0
    local ledger="${scratch_dir}/history.md"
    local anchor="## Request ledger (newest first)"

    echo "== Sec 1.1-style selftest: append_request_history.sh (scratch ledger only) =="
    echo

    {
        printf '# Scratch Ledger\n\n'
        printf 'Revision: 1\n\n'
        printf '%s\n\n' "$anchor"
        printf '### REQ-001 -- pre-existing entry\n'
        printf -- '- **Content:** pre-existing row, must survive untouched.\n\n'
    } > "$ledger"

    echo "-- assertion 1: a hook invocation appends exactly one new AUTO-CAPTURED row, pre-existing row untouched --"
    local payload1='{"prompt":"first test prompt","cwd":"/mnt/track2/some_project","effort":{"level":"high"}}'
    printf '%s' "$payload1" | REQUEST_HISTORY_FILE="$ledger" bash "${BASH_SOURCE[0]}" >/dev/null 2>&1

    local auto_count req001_count
    auto_count="$(grep -c '^### \[AUTO-CAPTURED' "$ledger" || true)"
    req001_count="$(grep -c '^### REQ-001' "$ledger" || true)"
    if [ "$auto_count" = "1" ] && [ "$req001_count" = "1" ] && grep -q "first test prompt" "$ledger"; then
        echo "assertion 1: PASS (1 AUTO-CAPTURED row + pre-existing REQ-001 row both present)"
    else
        echo "assertion 1: FAIL (auto_count=$auto_count req001_count=$req001_count)"
        overall=1
    fi

    echo
    echo "-- assertion 2: newest-first -- the new row is inserted ABOVE the pre-existing row --"
    local auto_line req_line
    auto_line="$(grep -n '^### \[AUTO-CAPTURED' "$ledger" | head -n1 | cut -d: -f1)"
    req_line="$(grep -n '^### REQ-001' "$ledger" | head -n1 | cut -d: -f1)"
    if [ -n "$auto_line" ] && [ -n "$req_line" ] && [ "$auto_line" -lt "$req_line" ]; then
        echo "assertion 2: PASS (AUTO-CAPTURED row at line $auto_line precedes REQ-001 at line $req_line)"
    else
        echo "assertion 2: FAIL (auto_line=$auto_line req_line=$req_line)"
        overall=1
    fi

    echo
    echo "-- assertion 3: honest field derivation -- Model literal UNKNOWN, effort real 'high', Track carries T2 --"
    if grep -q 'Model+effort:\*\* UNKNOWN / high' "$ledger" && grep -q '\*\*Track:\*\* T2/' "$ledger"; then
        echo "assertion 3: PASS (Model=UNKNOWN, effort=high captured verbatim, Track=T2/... derived from cwd)"
    else
        echo "assertion 3: FAIL"
        overall=1
    fi

    echo
    echo "-- assertion 4: a second invocation appends a SECOND row without corrupting the file (idempotent-safe under repeat calls) --"
    # Deliberately unset CLAUDE_EFFORT + CLAUDE_CONFIG_DIR for THIS invocation
    # only, so assertion 5 (below) exercises the genuine nothing-resolvable
    # path honestly, rather than accidentally inheriting this real session's
    # own live CLAUDE_EFFORT/CLAUDE_CONFIG_DIR values (Sec 11.4.199 - test the
    # actual absence condition, never assume it).
    local payload2='{"prompt":"second test prompt, with \"quotes\" and\nnewline","cwd":"/home/other/nontrack","effort":{}}'
    printf '%s' "$payload2" | REQUEST_HISTORY_FILE="$ledger" env -u CLAUDE_EFFORT -u CLAUDE_CONFIG_DIR bash "${BASH_SOURCE[0]}" >/dev/null 2>&1
    auto_count="$(grep -c '^### \[AUTO-CAPTURED' "$ledger" || true)"
    req001_count="$(grep -c '^### REQ-001' "$ledger" || true)"
    if [ "$auto_count" = "2" ] && [ "$req001_count" = "1" ] && grep -q "second test prompt" "$ledger" && grep -q "first test prompt" "$ledger"; then
        echo "assertion 4: PASS (2 AUTO-CAPTURED rows + original REQ-001 row all present after 2 invocations)"
    else
        echo "assertion 4: FAIL (auto_count=$auto_count req001_count=$req001_count)"
        overall=1
    fi

    echo
    echo "-- assertion 5: honest '?' fallback -- unresolvable Track/Alias/effort never fabricated --"
    if grep -q '\*\*Track:\*\* T?/' "$ledger" && grep -q 'Model+effort:\*\* UNKNOWN / UNKNOWN' "$ledger"; then
        echo "assertion 5: PASS (off-track cwd -> honest T?/..., absent effort -> honest UNKNOWN, never guessed)"
    else
        echo "assertion 5: FAIL"
        overall=1
    fi

    echo
    echo "-- assertion 6: missing-anchor is FAIL-CLOSED (never a silent wrong-place insert), and never touches an unrelated file --"
    local bad_ledger="${scratch_dir}/no_anchor.md"
    printf '# No anchor here\n\nJust prose.\n' > "$bad_ledger"
    local before_hash after_hash
    before_hash="$(sha256sum "$bad_ledger" | cut -d' ' -f1)"
    printf '%s' '{"prompt":"should not land anywhere"}' | REQUEST_HISTORY_FILE="$bad_ledger" bash "${BASH_SOURCE[0]}" >/dev/null 2>&1
    after_hash="$(sha256sum "$bad_ledger" | cut -d' ' -f1)"
    if [ "$before_hash" = "$after_hash" ]; then
        echo "assertion 6: PASS (missing-anchor file left byte-identical -- fail-closed, no wrong-place insert)"
    else
        echo "assertion 6: FAIL (file was modified despite missing anchor)"
        overall=1
    fi

    echo
    echo "-- assertion 7: empty prompt is a silent no-op (never fabricates a row from nothing) --"
    local pre_count post_count
    pre_count="$(grep -c '^### \[AUTO-CAPTURED' "$ledger" || true)"
    printf '%s' '{"prompt":"","cwd":"/mnt/track9/x"}' | REQUEST_HISTORY_FILE="$ledger" bash "${BASH_SOURCE[0]}" >/dev/null 2>&1
    post_count="$(grep -c '^### \[AUTO-CAPTURED' "$ledger" || true)"
    if [ "$pre_count" = "$post_count" ]; then
        echo "assertion 7: PASS (empty prompt produced no new row: still $post_count)"
    else
        echo "assertion 7: FAIL (pre=$pre_count post=$post_count)"
        overall=1
    fi

    echo
    if [ "$overall" -eq 0 ]; then
        echo "OVERALL SELFTEST: PASS (mechanism is genuinely load-bearing, scratch-only, never touched the real requests/history.md)"
    else
        echo "OVERALL SELFTEST: FAIL"
    fi
    return "$overall"
}

main "$@"
