#!/usr/bin/env bash
# CM-SUBMODULE-PIN-ON-MAIN (HXC-159 T-P3.03.5)
#
# Asserts the PIN.md-recorded SHA is the current tip of local `main`
# (raw SHA pin with branch=main recorded — never a bare branch name).
#
# Usage: gates/cm-submodule-pin-on-main.sh [repo-root]
# Exit 0 = PASS, 1 = FAIL.
set -u
ROOT="${1:-$(git rev-parse --show-toplevel)}"
PIN="$ROOT/PIN.md"

fail() { echo "CM-SUBMODULE-PIN-ON-MAIN: FAIL — $1"; exit 1; }

[ -f "$PIN" ] || fail "PIN.md missing at $PIN"
SHA="$(grep -E '^sha=[0-9a-f]{40}$' "$PIN" | head -1 | cut -d= -f2)"
BRANCH="$(grep -E '^branch=\S+$' "$PIN" | head -1 | cut -d= -f2)"
[ -n "${SHA:-}" ] || fail "no valid sha=<40hex> line in PIN.md"
[ "$BRANCH" = "main" ] || fail "branch='$BRANCH', want 'main'"
git -C "$ROOT" cat-file -t "$SHA" 2>/dev/null | grep -qx commit \
  || fail "SHA $SHA does not resolve to a commit"
HEAD_SHA="$(git -C "$ROOT" rev-parse main)"
[ "$SHA" = "$HEAD_SHA" ] || fail "PIN.md sha=$SHA != main tip $HEAD_SHA (pin stale)"
echo "CM-SUBMODULE-PIN-ON-MAIN: PASS — PIN.md sha=$SHA == main tip, branch=main"
