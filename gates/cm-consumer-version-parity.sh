#!/usr/bin/env bash
# CM-CONSUMER-VERSION-PARITY (HXC-159 T-P4.01.4, R-25)
#
# The shared `pkg/skills` library MUST stay dependency-free (stdlib only).
# That structural absence — not a version pin — is what makes the
# replace-outside-main-module problem impossible for both consumers.
#
# Asserts: `go list -deps ./pkg/skills` yields no package outside stdlib
# (plus the package itself). Any third-party import → FAIL.
#
# Paired §1.1 mutation: add a third-party import to pkg/skills → FAIL;
# restore → PASS. Captured in qa-results/hxc159/loader/<run-id>/.
#
# Usage: gates/cm-consumer-version-parity.sh [repo-root]
# Exit 0 = PASS, 1 = FAIL.
set -u
ROOT="${1:-$(git rev-parse --show-toplevel)}"

fail() { echo "CM-CONSUMER-VERSION-PARITY: FAIL — $1"; exit 1; }

DEPS="$(go -C "$ROOT" list -deps ./pkg/skills 2>/dev/null)" \
  || fail "'go list -deps ./pkg/skills' failed (package missing?)"
[ -n "$DEPS" ] || fail "empty dependency list"

# NOTE: a naive `grep '\.'` heuristic is WRONG here — stdlib holds dotted
# paths such as crypto/internal/entropy/v1.0.0. The .Standard field is the
# precise discriminator (this comment records a false-positive caught and
# fixed during T-P4.01, not assumed away).
NONSTDLIB="$(go -C "$ROOT" list -deps -f '{{if not .Standard}}{{.ImportPath}} {{end}}' ./pkg/skills 2>/dev/null | tr ' ' '\n' | grep -v '^github.com/HelixDevelopment/skills/pkg/skills$' | grep -v '^$' || true)"
if [ -n "$NONSTDLIB" ]; then
  fail "third-party imports in pkg/skills dependency graph: $(echo "$NONSTDLIB" | tr '\n' ' ')"
fi
N="$(echo "$DEPS" | wc -l)"
echo "CM-CONSUMER-VERSION-PARITY: PASS — pkg/skills graph is stdlib-only ($N packages, zero third-party)"
