#!/usr/bin/env bash
# CM-SKILL-CAPABILITY-DECLARED (HXC-159 T-P4.05, R-14)
#
# Asserts capability declaration + fail-closed refusal at the call boundary:
#   1. The capability tests pass at RED_MODE=0 (refusal typed, audited;
#      enforcement at Use, never at parse).
#   2. The same tests FAIL at RED_MODE=1 (they discriminate — §11.4.1).
#   3. Paired §1.1 mutation: patch Skill.Allows to always-true
#      (undeclared use silently succeeds — the T-P4.05.1 defect) →
#      this gate MUST FAIL; restore → PASS.
#
# Usage: gates/cm-skill-capability-declared.sh [repo-root]
# Exit 0 = PASS, 1 = FAIL.
set -u
ROOT="${1:-$(git rev-parse --show-toplevel)}"

fail() { echo "CM-SKILL-CAPABILITY-DECLARED: FAIL — $1"; exit 1; }

go -C "$ROOT" test ./pkg/skills/ -run 'TestUndeclaredCapabilityRefusedAtCallBoundary|TestDeclaredCapabilityAllowedAndAudited|TestRefusalIsAudited|TestParseTimeDoesNotRefuse|TestAllowedToolsMapping' -count=1 >/dev/null 2>&1 \
  || fail "capability tests do not pass at RED_MODE=0"
if RED_MODE=1 go -C "$ROOT" test ./pkg/skills/ -run 'TestUndeclaredCapabilityRefusedAtCallBoundary' -count=1 >/dev/null 2>&1; then
  fail "polarity check: RED_MODE=1 unexpectedly passes (tests cannot discriminate)"
fi

# Paired mutation: always-allow at the boundary must trip the gate.
MUT="$ROOT/pkg/skills/capability.go"
cp "$MUT" "$MUT.bak"
python3 - "$MUT" <<'EOF'
import sys
p = sys.argv[1]
src = open(p).read()
old = """func (s Skill) Allows(c string) bool {
	want := strings.ToLower(strings.TrimSpace(c))"""
new = """func (s Skill) Allows(c string) bool {
	return true // MUTATED for paired §1.1 check: always-allow
	want := strings.ToLower(strings.TrimSpace(c))"""
assert old in src, "mutation anchor not found"
open(p, "w").write(src.replace(old, new, 1))
EOF
if go -C "$ROOT" test ./pkg/skills/ -run 'TestUndeclaredCapabilityRefusedAtCallBoundary' -count=1 >/dev/null 2>&1; then
  mv "$MUT.bak" "$MUT"
  fail "paired mutation (always-allow) unexpectedly passes — enforcement is decorative"
fi
mv "$MUT.bak" "$MUT"
go -C "$ROOT" test ./pkg/skills/ -run 'TestUndeclaredCapabilityRefusedAtCallBoundary' -count=1 >/dev/null 2>&1 \
  || fail "restore after mutation did not return to green"

echo "CM-SKILL-CAPABILITY-DECLARED: PASS — refusal typed+audited at the call boundary, polarity discriminates, always-allow mutation caught"
