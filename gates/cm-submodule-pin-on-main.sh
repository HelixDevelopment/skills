#!/usr/bin/env bash
# CM-SUBMODULE-PIN-ON-MAIN (HXC-159 T-P3.03.5)
#
# Asserts the PIN.md-recorded pin is a raw SHA on `main` — never a bare
# branch name — that is an ancestor-or-equal of the current `main` tip
# (tasks.md runtime signature: asserted by a live
# `git merge-base --is-ancestor` check).
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
[ "$BRANCH" = "main" ] || fail "branch='$BRANCH', want 'main' (never a bare branch name)"
git -C "$ROOT" cat-file -t "$SHA" 2>/dev/null | grep -qx commit \
  || fail "SHA $SHA does not resolve to a commit"
git -C "$ROOT" merge-base --is-ancestor "$SHA" main \
  || fail "PIN.md sha=$SHA is NOT an ancestor of the main tip"
echo "CM-SUBMODULE-PIN-ON-MAIN: PASS — PIN.md sha=$SHA on branch=main, ancestor of main tip"
