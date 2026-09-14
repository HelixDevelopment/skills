# `scripts/test_guard_forbidden_commands.sh` — hermetic test suite for the §11.4.109 PreToolUse guard hook

**Revision:** 2
**Last modified:** 2026-07-15T20:29:01Z
**Authority:** constitution §11.4.109 (Mandatory Anti-Forgetting Enforcement: PreToolUse Guard Hook + Subagent Constitutional Preamble + Orchestrator Pre-Action Checklist)
**Scope:** register item G41 (HIGH, §11.4.109) — closes the gap where this
project wired ZERO PreToolUse hooks, so `constitution/scripts/hooks/guard-forbidden-commands.sh`
was never active at the tool-call boundary.

## Overview

This test proves that `.claude/settings.json` (at the MVP-project root,
`docs/research/mvp/Agent_AI_Skill_Tree_Development/.claude/settings.json`)
correctly wires the constitution's shared `guard-forbidden-commands.sh`
PreToolUse hook, and that the hook itself genuinely blocks every forbidden
command class (host-direct emulator, force-push/`--no-verify`/`--no-gpg-sign`,
sudo/su, host-power) while allowing ordinary commands through.

It is a **functional** test per §11.4.108/§11.4.201: it invokes the REAL hook
script with crafted PreToolUse JSON payloads on stdin — the exact contract
Claude Code uses — and asserts the REAL exit code the script returns. It
never greps the hook's source as a substitute for actually running it.

## Why this hook is referenced, never copied

Per §11.4.109/§11.4.177 the guard hook is inherited **by reference** from the
constitution submodule (`constitution/scripts/hooks/guard-forbidden-commands.sh`)
— it MUST NOT be copied into this project's tree, since a copy diverges
silently from future constitution updates. Both the wired `.claude/settings.json`
command and this test resolve the hook's absolute path via
`git -C <dir> rev-parse --show-toplevel`, which returns the actual git
repository root (`helix_skills/`) regardless of which subdirectory the
resolution is invoked from — this project's MVP checkout is 4 directory
levels below the repo root (`docs/research/mvp/Agent_AI_Skill_Tree_Development/`),
so a naive `$CLAUDE_PROJECT_DIR/constitution/...` path (the pattern documented
for projects where `constitution/` is an immediate child of the project root)
would resolve incorrectly here. The `git rev-parse --show-toplevel`
resolution is verified correct in both directions (see Prerequisites).

## Prerequisites

- `bash` (both this test and the hook under test require bash — `[[ ]]`,
  `BASH_REMATCH`, arrays; not POSIX-`sh` portable).
- `git` — used to resolve the repository root the same way the wired hook
  command in `.claude/settings.json` does.
- `constitution/scripts/hooks/guard-forbidden-commands.sh` present AND
  executable at the resolved repository root. The suite REFUSES to run
  (exit 1, zero cases) rather than silently report a false PASS if this
  path is missing or not executable — a guard test that cannot locate its
  target must fail closed (§11.4.201).

## Usage

```bash
bash project/scripts/test_guard_forbidden_commands.sh
```

Run from anywhere inside the `helix_skills` checkout (it resolves its own
location via `dirname "$0"` and the repo root via `git rev-parse --show-toplevel`,
so it does not depend on the caller's current working directory).

### Sample output (captured 2026-07-16)

```
§11.4.109 guard-forbidden-commands.sh hermetic test suite (MVP: HelixKnowledge Skill Graph System)
hook under test: /home/milos/Factory/projects/tools_and_research/helix_skills/constitution/scripts/hooks/guard-forbidden-commands.sh

  PASS  non-Bash tool_name (Read) passes through untouched         (exit 0)
  ...
  PASS  git push --force blocked                                   (exit 2)
  ...
  PASS  escape hatch does NOT override shutdown                    (exit 2)

  total: PASS=37 FAIL=0 (cases=37)
  RESULT: PASS (all 37 cases)
```

## Inputs

None — every case constructs its own JSON payload inline; the suite is
fully self-contained (no test-fixture DB, no network, no project state
read).

## Outputs

Per-case `PASS`/`FAIL` lines to stdout + a final `total: PASS=<n> FAIL=<n>`
summary line + `RESULT: PASS`/`RESULT: FAIL`.

## Side-effects

None. Every case is a read-only stdin → exit-code probe of the hook script.
Cases exercising a forbidden command class (`git push --force`, `sudo ...`,
`systemctl suspend`, `emulator -avd ...`, etc.) never actually execute that
command — the hook only receives the JSON description of the proposed tool
call and returns a verdict; neither the hook nor this test runs the
underlying command.

## Case inventory (37 cases — exceeds the §11.4.109 ≥ 20 floor)

**Allowed (exit 0) — 10 cases:** non-`Bash` `tool_name` passthrough (×2:
`Read`, `Agent`), missing/empty `tool_input.command`, `git status`,
`go test ./...`, `ls -la`, non-force `git push`, the 2026-07-11
false-positive-fix regression case (`tail -f qa-results/push_failures/x.log`
chained after `git fetch --all`), `adb devices` (adb present but not
`install`/instrument).

**Blocked (exit 2) — 22 cases**, one per forbidden class in the hook's own
4-class taxonomy:
- Class 1 (emulator/adb/instrument, §6.X/§6.V/§6.AG): 5 cases — raw
  `emulator -avd`, `$ANDROID_HOME`-relative emulator path, `adb install`,
  `adb -s <serial> install`, `am instrument`.
- Class 2 (force-push / verification-bypass, §6.T.3): 6 cases — `--force`,
  `-f`, `--force-with-lease`, `+refspec`, `--no-verify`, `--no-gpg-sign`.
- Class 3 (sudo/su, §6.U): 3 cases — `sudo <cmd>`, `su <user> -c <cmd>`
  (the F3-B1 bypass-closure case), bare `su`.
- Class 4 (host-power, Host Machine Stability Directive): 8 cases —
  `systemctl` {suspend, hibernate, poweroff, reboot}, `loginctl`
  {terminate-session, poweroff}, `pm-suspend`, bare `shutdown`.

**Escape hatch (`# guardrails:allow <reason>`) — 5 cases:** fires (WARN,
exit 0) for a non-power class with a reason (×2: sudo, force-push); does
NOT fire (still exit 2) when the marker carries no reason text; NEVER fires
for host-power even WITH a reason present (×2: `systemctl suspend`,
`shutdown`) — proving the "categorically non-overridable" behaviour the
hook documents for host-power.

## Edge cases

- **Hook path resolution failure:** if `git rev-parse --show-toplevel`
  cannot resolve (e.g. run outside any git working tree), the suite prints
  a diagnostic and exits 1 with zero cases run — never a silent 0-case
  PASS.
- **Hook present but not executable** (e.g. permission regression): the
  suite refuses to run and exits 1, distinguishing "hook missing" from
  "hook exists but broken" in its diagnostic.
- **Escape-hatch marker with empty reason** (`# guardrails:allow` with no
  trailing text): the hook's own regex requires ≥ 1 character after the
  marker, so this does NOT downgrade the block — case 35 asserts this
  explicitly (still exit 2).

## Internal behavior

1. Resolves `HERE` (this script's own directory) and `REPO_ROOT` (via
   `git -C "$HERE" rev-parse --show-toplevel`).
2. Verifies `$REPO_ROOT/constitution/scripts/hooks/guard-forbidden-commands.sh`
   exists AND is executable; refuses to proceed otherwise.
3. Runs each `run_case <name> <expected-exit> <json-payload>` — pipes the
   JSON payload to the hook via heredoc-string stdin, discards stdout/stderr
   (only the exit code is asserted; the hook's stderr text is documented,
   not machine-asserted, in the companion hook doc), compares against the
   expected exit code, tallies PASS/FAIL.
4. Prints the summary line and exits 0 (all passed) or 1 (≥ 1 FAIL).

## Dependencies

`bash`, `git`. Transitively depends on
`constitution/scripts/hooks/guard-forbidden-commands.sh`'s own JSON-field
extractor (`jq` if present on `PATH`, else its embedded awk fallback) — this
test does not need `jq` itself; the hook under test resolves that
independently.

## Cross-references

- `.claude/settings.json` (MVP-project-level PreToolUse wiring this suite
  validates) — `docs/research/mvp/Agent_AI_Skill_Tree_Development/.claude/settings.json`.
- `constitution/scripts/hooks/guard-forbidden-commands.sh` (the hook itself,
  inherited by reference, never copied).
- `constitution/docs/scripts/guard-forbidden-commands.md` (the hook's own
  companion doc — authoritative description of its 4 forbidden classes +
  escape-hatch semantics).
- `constitution/scripts/hooks/test_guard_branch_consistency.sh` (sibling
  hermetic test for a different §11.4.109-class guard hook — same
  `run_case <name> <expected-exit> <json-payload>` pattern this suite
  follows).
- `constitution/docs/AGENT_GUARDRAILS.md` (§11.4.109 SUBAGENT CONSTITUTIONAL
  PREAMBLE + ORCHESTRATOR PRE-ACTION CHECKLIST — the hook is the mechanical
  floor these checklists describe).

## Last verified

2026-07-16, against `project/scripts/test_guard_forbidden_commands.sh`
(37/37 cases PASS, suite exit 0) and
`constitution/scripts/hooks/guard-forbidden-commands.sh` (present,
executable, 15201 bytes, last modified 2026-07-15).
