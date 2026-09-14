#!/usr/bin/env bash
# test_guard_forbidden_commands.sh - hermetic test suite for the §11.4.109
# PreToolUse guard hook (constitution/scripts/hooks/guard-forbidden-commands.sh)
# as wired for the HelixKnowledge Skill Graph System MVP project.
#
# Purpose:
#   Register item G41 (HIGH, §11.4.109) — the MVP project wired ZERO
#   PreToolUse hooks, so the constitution's guard-forbidden-commands.sh was
#   never active at the tool-call boundary. This suite is the mandated
#   hermetic test (≥ 20 cases per §11.4.109) that PROVES the guard, as wired
#   via docs/research/mvp/Agent_AI_Skill_Tree_Development/.claude/settings.json,
#   genuinely blocks every forbidden command class and genuinely allows every
#   ordinary command — a functional test that invokes the REAL guard script
#   with crafted PreToolUse JSON on stdin and asserts the real exit code
#   (§11.4.108/§11.4.201 — never a grep-only assertion, never a bluff).
#
# Usage:
#   bash scripts/test_guard_forbidden_commands.sh
#   (from anywhere inside the helix_skills repo; the hook path is resolved
#   via `git rev-parse --show-toplevel`, the SAME resolution mechanism wired
#   into .claude/settings.json, so this test exercises the identical
#   resolution path the real Claude Code hook invocation uses.)
#
# Inputs:
#   None (self-contained; the guard script it drives is read-only, referenced
#   by path, never copied — §11.4.109/§11.4.177).
#
# Outputs:
#   Per-case PASS/FAIL lines to stdout + a final summary line.
#
# Exit codes:
#   0 = every case passed (real exit code matched the expected exit code).
#   1 = at least one case FAILed (real exit code diverged from expected).
#
# Side-effects: none — every case is a read-only stdin/exit-code probe of the
#   guard script; no file is written, no git state is mutated, no network
#   call is made. Cases exercising the FORBIDDEN command classes (force-push,
#   sudo, host-power, emulator) are NEVER actually executed — the guard
#   script only receives the JSON payload describing the tool call and
#   returns its verdict; it does not itself run the underlying command.
#
# Dependencies:
#   bash, git (for `git rev-parse --show-toplevel` — the same resolution the
#   wired hook uses), the constitution submodule present at
#   <repo-root>/constitution/scripts/hooks/guard-forbidden-commands.sh
#   (verified present + executable before any case runs; the suite refuses
#   to run rather than silently no-op against a missing/stale hook path,
#   per §11.4.201 — a guard test that cannot resolve its target must fail
#   closed, not report a false PASS).
#
# Cross-references:
#   docs/scripts/test_guard_forbidden_commands.md (companion doc, §11.4.18)
#   constitution/docs/scripts/guard-forbidden-commands.md (hook's own doc)
#   constitution/scripts/hooks/guard-forbidden-commands.sh (the hook itself)
#   constitution/scripts/hooks/test_guard_branch_consistency.sh (sibling
#     hermetic suite for a different §11.4.109-class guard — same pattern)
#   .claude/settings.json (MVP-project-level wiring this suite validates)
#
# Last verified: 2026-07-16

set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"

# Resolve the repo root the SAME way .claude/settings.json's wired hook
# command does ($CLAUDE_PROJECT_DIR at runtime may be this MVP subdirectory,
# NOT the repo root — the constitution submodule lives 4 levels up from this
# project, so a naive "$CLAUDE_PROJECT_DIR/constitution/..." path would
# resolve incorrectly if invoked from here. `git rev-parse --show-toplevel`
# is robust regardless of which subdirectory Claude Code's project root
# happens to be, since both resolve to the same git working tree).
REPO_ROOT="$(git -C "$HERE" rev-parse --show-toplevel 2>/dev/null)"
if [ -z "$REPO_ROOT" ]; then
  echo "FAIL: could not resolve repo root via 'git rev-parse --show-toplevel' from $HERE" >&2
  echo "  RESULT: FAIL (0 cases run — cannot locate the hook under test)" >&2
  exit 1
fi

HOOK="$REPO_ROOT/constitution/scripts/hooks/guard-forbidden-commands.sh"

# §11.4.6 anti-bluff: never wire/test a path we have not verified exists AND
# is executable. A missing or non-executable hook is a HARD FAILURE of this
# suite, never a silent SKIP-to-PASS.
if [ ! -e "$HOOK" ]; then
  echo "FAIL: guard script not found at: $HOOK" >&2
  echo "  RESULT: FAIL (0 cases run)" >&2
  exit 1
fi
if [ ! -x "$HOOK" ]; then
  echo "FAIL: guard script exists but is NOT executable: $HOOK" >&2
  echo "  RESULT: FAIL (0 cases run)" >&2
  exit 1
fi

PASS=0
FAIL=0

# run_case <name> <expected-exit> <json-payload>
#   Invokes the REAL guard script with the payload on stdin (exactly the
#   Claude Code PreToolUse contract) and compares its REAL exit code against
#   the expected one. Never a grep on the script source — a functional
#   black-box probe of the actual running hook (§11.4.108/§11.4.201).
run_case() {
  local name="$1" want="$2" payload="$3" got
  got=$(bash "$HOOK" <<<"$payload" >/dev/null 2>&1; echo $?)
  if [ "$got" -eq "$want" ]; then
    printf '  PASS  %-58s (exit %s)\n' "$name" "$got"
    PASS=$((PASS + 1))
  else
    printf '  FAIL  %-58s (got exit %s, want %s)\n' "$name" "$got" "$want"
    FAIL=$((FAIL + 1))
  fi
}

echo "§11.4.109 guard-forbidden-commands.sh hermetic test suite (MVP: HelixKnowledge Skill Graph System)"
echo "hook under test: $HOOK"
echo

# =============================================================================
# ALLOWED cases (expected exit 0) — ordinary commands MUST pass through.
# =============================================================================
run_case "non-Bash tool_name (Read) passes through untouched"       0 '{"tool_name":"Read","tool_input":{"file_path":"/tmp/x"}}'
run_case "non-Bash tool_name (Agent) passes through untouched"      0 '{"tool_name":"Agent","tool_input":{"description":"(T1/main) do work"}}'
run_case "missing tool_input.command allows"                        0 '{"tool_name":"Bash","tool_input":{}}'
run_case "empty tool_input.command allows"                          0 '{"tool_name":"Bash","tool_input":{"command":""}}'
run_case "git status (ordinary, read-only)"                         0 '{"tool_name":"Bash","tool_input":{"command":"git status"}}'
run_case "go test ./... (ordinary build/test command)"              0 '{"tool_name":"Bash","tool_input":{"command":"go test ./..."}}'
run_case "ls -la (ordinary listing)"                                0 '{"tool_name":"Bash","tool_input":{"command":"ls -la"}}'
run_case "git push origin main (no force flag)"                     0 '{"tool_name":"Bash","tool_input":{"command":"git push origin main"}}'
run_case "tail -f push-failure log (false-positive regression, §11.4.180)" 0 '{"tool_name":"Bash","tool_input":{"command":"git fetch --all && tail -f qa-results/push_failures/x.log"}}'
run_case "adb devices (adb present but not install/instrument)"     0 '{"tool_name":"Bash","tool_input":{"command":"adb devices"}}'

# =============================================================================
# BLOCKED cases (expected exit 2) — every forbidden class from §11.4.109.
# =============================================================================
# Class 1: host-direct emulator / adb-install / am-instrument (§6.X/§6.V/§6.AG)
run_case "raw emulator -avd launch blocked"                         2 '{"tool_name":"Bash","tool_input":{"command":"emulator -avd Pixel_5_API_31"}}'
run_case "ANDROID_HOME-relative emulator path blocked"              2 '{"tool_name":"Bash","tool_input":{"command":"$ANDROID_HOME/emulator/emulator -avd test"}}'
run_case "adb install (top-level) blocked"                          2 '{"tool_name":"Bash","tool_input":{"command":"adb install app.apk"}}'
run_case "adb -s <serial> install blocked"                          2 '{"tool_name":"Bash","tool_input":{"command":"adb -s emulator-5554 install app.apk"}}'
run_case "am instrument blocked"                                    2 '{"tool_name":"Bash","tool_input":{"command":"am instrument -w com.example.test/androidx.test.runner.AndroidJUnitRunner"}}'

# Class 2: force-push / verification-bypass (§6.T.3)
run_case "git push --force blocked"                                 2 '{"tool_name":"Bash","tool_input":{"command":"git push --force origin main"}}'
run_case "git push -f blocked"                                       2 '{"tool_name":"Bash","tool_input":{"command":"git push -f origin main"}}'
run_case "git push --force-with-lease blocked"                       2 '{"tool_name":"Bash","tool_input":{"command":"git push --force-with-lease origin main"}}'
run_case "git push +refspec (force via refspec) blocked"            2 '{"tool_name":"Bash","tool_input":{"command":"git push origin +main:main"}}'
run_case "git commit --no-verify blocked"                            2 '{"tool_name":"Bash","tool_input":{"command":"git commit --no-verify -m x"}}'
run_case "git commit --no-gpg-sign blocked"                          2 '{"tool_name":"Bash","tool_input":{"command":"git commit --no-gpg-sign -m x"}}'

# Class 3: sudo / su (§6.U)
run_case "sudo <cmd> blocked"                                        2 '{"tool_name":"Bash","tool_input":{"command":"sudo systemctl status foo"}}'
run_case "su <user> -c <cmd> blocked (F3-B1 bypass closed)"          2 '{"tool_name":"Bash","tool_input":{"command":"su root -c \"cat /etc/shadow\""}}'
run_case "bare su blocked"                                           2 '{"tool_name":"Bash","tool_input":{"command":"su"}}'

# Class 4: host-power (Host Machine Stability Directive) — NOT overridable
run_case "systemctl suspend blocked"                                 2 '{"tool_name":"Bash","tool_input":{"command":"systemctl suspend"}}'
run_case "systemctl hibernate blocked"                               2 '{"tool_name":"Bash","tool_input":{"command":"systemctl hibernate"}}'
run_case "systemctl poweroff blocked"                                2 '{"tool_name":"Bash","tool_input":{"command":"systemctl poweroff"}}'
run_case "systemctl reboot blocked"                                  2 '{"tool_name":"Bash","tool_input":{"command":"systemctl reboot"}}'
run_case "loginctl terminate-session blocked"                        2 '{"tool_name":"Bash","tool_input":{"command":"loginctl terminate-session 1"}}'
run_case "loginctl poweroff blocked"                                 2 '{"tool_name":"Bash","tool_input":{"command":"loginctl poweroff"}}'
run_case "pm-suspend blocked"                                        2 '{"tool_name":"Bash","tool_input":{"command":"pm-suspend"}}'
run_case "bare shutdown blocked"                                     2 '{"tool_name":"Bash","tool_input":{"command":"shutdown -h now"}}'

# =============================================================================
# ESCAPE HATCH cases — audited "# guardrails:allow <reason>" marker.
# =============================================================================
# Fires (downgrades BLOCK to WARN, exit 0) for non-power classes:
run_case "escape hatch downgrades sudo to WARN (exit 0)"             0 '{"tool_name":"Bash","tool_input":{"command":"sudo whoami # guardrails:allow operator-approved test probe"}}'
run_case "escape hatch downgrades force-push to WARN (exit 0)"       0 '{"tool_name":"Bash","tool_input":{"command":"git push --force origin main # guardrails:allow operator-approved mirror reconciliation"}}'
run_case "escape hatch marker with NO reason text does NOT fire"     2 '{"tool_name":"Bash","tool_input":{"command":"sudo whoami # guardrails:allow"}}'

# NEVER fires for host-power, even WITH the marker present + a reason:
run_case "escape hatch does NOT override systemctl suspend"          2 '{"tool_name":"Bash","tool_input":{"command":"systemctl suspend # guardrails:allow operator-approved test probe"}}'
run_case "escape hatch does NOT override shutdown"                   2 '{"tool_name":"Bash","tool_input":{"command":"shutdown -h now # guardrails:allow operator-approved test probe"}}'

echo
echo "  total: PASS=$PASS FAIL=$FAIL (cases=$((PASS + FAIL)))"
if [ "$FAIL" -gt 0 ]; then
  echo "  RESULT: FAIL"
  exit 1
fi
echo "  RESULT: PASS (all $PASS cases)"
exit 0
