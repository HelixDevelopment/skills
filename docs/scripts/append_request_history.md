# append_request_history.sh

**Revision:** 2
**Last modified:** 2026-07-15T20:29:01Z

## Overview

`scripts/append_request_history.sh` is the `UserPromptSubmit` hook that
mechanically appends a newest-first row to this project's Sec 11.4.208
operator-request-history ledger (`requests/history.md`) every time the
operator submits a new prompt. It closes register item **G38**: the
ledger existed but was **reconstruction-only** — no mechanism appended a
row at accept-time (Sec 11.4.208(D)).

**Why this exists.** Constitution Sec 11.4.208 mandates a project-local,
always-in-sync, append-only, newest-first request-history document
carrying five fields per request: (1) full content, (2) an accepted
timestamp with an explicit timezone, (3) the Track, (4) the Alias, (5) the
model + effort used. Sub-clause (D) requires a "keep-applying mechanism"
— a helper script and/or a `UserPromptSubmit`-class hook that appends a
row per new prompt — and explicitly says a helper landed *without* that
hook is only an honestly-partial mechanism. This script is that hook.

## HONEST FEASIBILITY FINDING (Sec 11.4.6 — investigated, not guessed)

Before implementing, the `UserPromptSubmit` hook's actual capability was
investigated against the official Claude Code hooks documentation
(`https://code.claude.com/docs/en/hooks.md` +
`https://code.claude.com/docs/en/hooks-guide.md`), cross-checked against
this repo's own already-verified sibling hook
(`constitution/scripts/hooks/action_prefix_expand.sh`, which documents
having verified the same docs on 2026-06-09 and confirmed the prompt text
lives at `.prompt`), and against this project's own live shell
environment. Findings:

| Field | Available to `UserPromptSubmit`? | Source | Honest fallback if absent |
|---|---|---|---|
| Prompt content | **YES** | stdin JSON `.prompt` (confirmed by `action_prefix_expand.sh`'s own header, re-verified 2026-07-16) | (none — empty prompt is a no-op) |
| cwd (for Track) | **YES** | stdin JSON `.cwd` | script's own `pwd` |
| Effort level | **YES** | stdin JSON `.effort.level`, also `$CLAUDE_EFFORT` env var (both empirically confirmed present in this project's real shell — `CLAUDE_EFFORT=xhigh` observed live) | literal `UNKNOWN` |
| **Model name** | **NO** | the docs explicitly state only `SessionStart` hooks MAY (not guaranteed) carry a `model` field, and NO event carries a `$CLAUDE_MODEL` env var | literal `UNKNOWN` — **always**, for every auto-captured row |
| Alias (`CLAUDE_CONFIG_DIR`) | best-effort | `$CLAUDE_CONFIG_DIR` env var (not in the documented-guaranteed set, but empirically present — `/home/milos/.claude-claude1` observed live) | honest `?` |
| Track number (`/mnt/track<N>/...`) | best-effort | pattern-matched against the resolved cwd | honest `?` |

**Conclusion:** a fully-honest auto-capture hook is feasible for four of
the five Sec 11.4.208 fields (Content, Accepted-timestamp+TZ, Track,
Alias) and the *effort* half of the fifth. The *model* half of field (5)
is **not observable** by this hook under any documented mechanism — this
script therefore **never fabricates it**: every auto-captured row's
Model+effort cell reads `UNKNOWN / <real-effort-or-UNKNOWN>`, literally
and always. This is the honest DONE-WITH-HONEST-PARTIAL boundary Sec
11.4.208(A)/(D) requires; it is not a bug to fix later, it is a
documented, permanent property of what a `UserPromptSubmit` hook can see.

## Prerequisites

- `bash` >= 4.
- Standard coreutils: `mktemp`, `mv`, `dirname`, `basename`, `cat`, `date`,
  `grep`, `sed`, `cmp`/`sha256sum` (evidence only, not required at
  runtime).
- `git` (optional at runtime — only used to resolve the branch component
  of Track; its absence degrades to the honest `?`, never a crash).
- `jq` (preferred JSON field extraction) — if absent, an awk fallback
  mirrors the local pattern already used by
  `constitution/scripts/hooks/guard-forbidden-commands.sh` and
  `constitution/scripts/hooks/action_prefix_expand.sh` (each hook keeps
  its own local copy on purpose — Sec 11.4.28 decoupling, no cross-hook
  sourcing).
- `flock` (optional — serializes concurrent invocations against the same
  target file; skipped, honestly, if absent).
- `sha256sum` (optional — names the out-of-tree lock file from a stable
  hash of the resolved target path; falls back to `shasum`, then
  `cksum`, then a sanitized-path name if all three are absent — locking
  still works either way).

## Usage examples

As the live hook (wired in `.claude/settings.json`, invoked automatically
by Claude Code on every `UserPromptSubmit` event with the event JSON fed
on stdin — see "Related scripts" below):

```bash
bash project/scripts/append_request_history.sh
```

Manual / test invocation — **always** override the target so the real
ledger is never touched:

```bash
echo '{"prompt":"hello","cwd":"/mnt/track2/proj","effort":{"level":"high"}}' \
  | REQUEST_HISTORY_FILE=/path/to/scratch/history.md \
    bash project/scripts/append_request_history.sh
```

or equivalently:

```bash
echo '{"prompt":"hello"}' \
  | bash project/scripts/append_request_history.sh --history-file /path/to/scratch/history.md
```

Self-test (paired Sec 1.1-style mutation check — proves the mechanism is
genuinely load-bearing; creates + destroys its own throwaway scratch
ledger under `mktemp -d`, **never** touches the real `requests/history.md`):

```bash
bash project/scripts/append_request_history.sh --selftest
```

Expected output (abridged):

```
== Sec 1.1-style selftest: append_request_history.sh (scratch ledger only) ==
...
assertion 1: PASS (1 AUTO-CAPTURED row + pre-existing REQ-001 row both present)
assertion 2: PASS (AUTO-CAPTURED row at line 7 precedes REQ-001 at line 15)
assertion 3: PASS (Model=UNKNOWN, effort=high captured verbatim, Track=T2/... derived from cwd)
assertion 4: PASS (2 AUTO-CAPTURED rows + original REQ-001 row all present after 2 invocations)
assertion 5: PASS (off-track cwd -> honest T?/..., absent effort -> honest UNKNOWN, never guessed)
assertion 6: PASS (missing-anchor file left byte-identical -- fail-closed, no wrong-place insert)
assertion 7: PASS (empty prompt produced no new row: still 2)

OVERALL SELFTEST: PASS (mechanism is genuinely load-bearing, scratch-only, never touched the real requests/history.md)
```

Show help:

```bash
bash project/scripts/append_request_history.sh -h
```

## Edge cases

- **Empty or absent `.prompt`.** Silent no-op, exit 0 — nothing is
  fabricated from nothing (verified by selftest assertion 7).
- **Anchor heading not found verbatim in the target file.** FAIL-CLOSED
  (Sec 11.4.6) — the file is left byte-identical, a diagnostic goes to
  stderr, and (in live-hook mode) the script still exits 0 so the
  operator's prompt is never blocked. It never guesses a fallback
  insertion point (verified by selftest assertion 6).
- **Target file does not exist at all.** Bootstrapped fresh with a
  minimal header + the anchor + the first entry. This path exists for
  robustness (e.g. a project adopting this hook before its ledger is
  created) — it is never exercised against this project's own
  `requests/history.md`, which already exists.
- **Model name.** Always literal `UNKNOWN` — see the feasibility table
  above. This is not an omission; it is the honest, permanent boundary of
  what a `UserPromptSubmit` hook can observe.
- **Off-track host / no `/mnt/track<N>/` cwd.** Track's number component
  is honestly `?`; if the cwd is a real git checkout the branch component
  is still resolved (e.g. `T?/main`), otherwise both halves are `?`.
- **`$CLAUDE_CONFIG_DIR` unset or not matching `.claude-<alias>`.** Alias
  is honestly `?`.
- **Neither `.effort.level` nor `$CLAUDE_EFFORT` set.** Effort is
  honestly `UNKNOWN`.
- **Prompt content containing embedded newlines/quotes.** Newlines are
  collapsed to single spaces so the entry stays one markdown list-item
  value (a documented non-goal is preserving original prompt line
  breaks); quote characters are captured verbatim, unescaped, exactly as
  the existing hand-curated rows in `requests/history.md` already do
  ("verbatim" capture) — this is not a new risk this hook introduces.
- **Concurrent invocations against the same target.** A short `flock`
  (10s timeout) on a lock file OUTSIDE the tracked tree — under
  `${TMPDIR:-/tmp}`, named from a stable hash of the resolved target
  path (so repeat invocations against the same target still serialize
  on the same lock file, without ever leaving a `.lock` artifact in the
  repo) — serializes them; if `flock` is unavailable the lock step is
  skipped honestly (documented gap — no stale-lock reaping is
  implemented here, unlike the full Sec 11.4.180 wrapper discipline,
  since a `UserPromptSubmit` hook's write is a single small insert, not
  a long-held writer).
- **Live-hook mode exit code.** ALWAYS `0`, regardless of internal
  success or failure — mirrors `action_prefix_expand.sh`'s fail-open
  discipline. A housekeeping bug in this script must never block an
  operator's prompt; a `UserPromptSubmit` hook exiting `2` BLOCKS the
  prompt per the Claude Code hooks contract, and this script never
  returns `2`. `--selftest` mode is the exception — it is a test utility,
  not the live hook path, and returns a real `0`/`1`.

## Internal behavior

1. `json_field PAYLOAD JQ_PATH AWK_KEY` — extracts a field via `jq`
   (preferred) or an awk fallback for the flat top-level-string shape
   (mirrors the local pattern already used by the sibling hooks in
   `constitution/scripts/hooks/`).
2. `derive_track CWD` — regex-matches `^/mnt/track([0-9]+)(/|$)` against
   the resolved cwd for the track number (honest `?` on no match), and
   (if the cwd is a real directory) runs `git -C CWD rev-parse
   --abbrev-ref HEAD` for the branch (honest `?` on failure/no git),
   printing `T<num>/<branch>` per Sec 11.4.182's labelling convention.
3. `derive_alias` — matches `$CLAUDE_CONFIG_DIR`'s basename against
   `^\.claude-(.+)$` (honest `?` on no match/unset).
4. `derive_effort PAYLOAD` — prefers `.effort.level`, falls back to
   `$CLAUDE_EFFORT`, else literal `UNKNOWN`.
5. `build_entry PROMPT TRACK ALIAS EFFORT` — renders the
   `### [AUTO-CAPTURED Sec 11.4.208(D)] <UTC-timestamp>` markdown block
   with the `Content` / `Accepted...Track...Alias...Model+effort` /
   `Disposition: UNPROCESSED` lines. Model is hard-coded `UNKNOWN` here —
   never passed in as a parameter, so there is no code path that could
   ever set it to anything else.
6. `insert_after_anchor FILE ANCHOR ENTRY_FILE_PATH` — the temp-write-
   then-rename core: acquires an optional `flock`, verifies the anchor
   line exists verbatim (else fails closed), builds the new content via
   `awk` (reading the pre-built entry from a private temp file — passed
   as a file path rather than embedded into an `awk -v` string, avoiding
   any quoting pitfall with arbitrary prompt content) into a fresh
   `mktemp` file in the SAME directory as the target, verifies the
   injection actually happened (an `awk`-side marker file), then
   `mv -f`'s the temp file onto the target — an atomic rename, so the
   target is never observed truncated or half-written.
7. `main` — parses `-h`/`--help`, `--selftest`, `--history-file`,
   `--anchor`; in live-hook mode (the default) reads stdin once, derives
   every field, builds the entry, calls `insert_after_anchor`, and
   ALWAYS exits 0 (fail-open).
8. `run_selftest` — the paired Sec 1.1-style mutation check (see Usage
   examples above for its 7 assertions); uses its own `mktemp -d`
   scratch directory with an EXIT trap that embeds the directory's path
   literally at trap-registration time (`trap "rm -rf '${scratch_dir}'"
   EXIT`) rather than deferring `$scratch_dir` expansion to trap-
   execution time — the latter is a real bash pitfall (a function-local
   variable referenced by a later-firing `EXIT` trap is unbound once the
   function that declared it has returned) that this script's own first
   selftest run caught and fixed during development.

## Related scripts

- `../../../.claude/settings.json` — wires this script as a
  `UserPromptSubmit` hook entry (`{"hooks":[{"type":"command","command":
  "bash \"$(git -C \"$CLAUDE_PROJECT_DIR\" rev-parse --show-toplevel)/
  scripts/
  append_request_history.sh\""}]}`), added ALONGSIDE the pre-existing
  `PreToolUse` → `constitution/scripts/hooks/guard-forbidden-commands.sh`
  entry (which this change does not remove or modify).
- `../../../requests/history.md` — the Sec 11.4.208 ledger this hook
  appends to (the real, tracked file — never touched by any test in this
  doc or by the `--selftest` mode, both of which operate on throwaway
  scratch copies only).
- `../../../GAPS_AND_RISKS_REGISTER.md` G38 — the tracked item this
  script closes (§11.4.208(D) auto-capture-hook facet).
- `../../../../../../../constitution/scripts/hooks/action_prefix_expand.sh`
  — sibling `UserPromptSubmit` hook; confirmed the `.prompt` stdin field
  and the fail-open-on-error discipline this script mirrors.
- `../../../../../../../constitution/scripts/hooks/guard-forbidden-commands.sh`
  — sibling `PreToolUse` hook; shares the local jq/awk field-extractor
  pattern (each hook keeps its own copy, Sec 11.4.28).
- `check_container_runtime_default.sh` (same directory) — sibling
  project script establishing this project's `--selftest` /
  temp-write-then-rename / trap-cleanup conventions, followed here.

## Last verified

2026-07-16 — captured evidence: `bash -n` clean; `--selftest` run →
`OVERALL SELFTEST: PASS` (all 7 assertions, including a real bug caught
and fixed — a bash `EXIT`-trap-references-a-since-returned-function-local-
variable pitfall in the selftest's own scratch-dir cleanup, and a test-
design fix so assertion 5 genuinely exercises the no-signal-available
path rather than accidentally inheriting this session's own live
`$CLAUDE_EFFORT`/`$CLAUDE_CONFIG_DIR`); two manual invocations against a
sha256-verified byte-identical scratch COPY of the real
`requests/history.md` (never the real file) each appended exactly one
well-formed newest-first row (verified via `diff` showing ONLY the two
new blocks added, all 9 pre-existing `### REQ-` headings intact, and the
real file's sha256 unchanged after both runs); `.claude/settings.json`
validated with `python3 -m json.tool` showing BOTH the pre-existing
`PreToolUse` entry (untouched) and the new `UserPromptSubmit` entry, and
the entry's `$(git -C "$CLAUDE_PROJECT_DIR" rev-parse --show-toplevel)`-
resolved command path confirmed to point at a real, executable file.
