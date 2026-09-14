#!/usr/bin/env bash
# CM-HXC159-SOURCE-CONSUMABLE (HXC-159 T-P3.01.6)
#
# Asserts the upstream `skills` source is consumable as a Go module at a
# given rev (default: HEAD):
#   1. <rev>:go.mod exists at the repo ROOT with the canonical module path
#      github.com/HelixDevelopment/skills (exact name AND case).
#   2. When rev == HEAD, the live tree additionally satisfies:
#      `go build ./...` green AND `go list ./...` yields >=1 package.
#      (For any other rev the build/list checks honestly SKIP — they can only
#      run on the checked-out tree — while the go.mod check still FAILs/PASSes
#      mechanically off git objects.)
#
# Usage: gates/cm-hxc159-source-consumable.sh [repo-root] [rev]
# Exit 0 = PASS, 1 = FAIL.
set -u
ROOT="${1:-$(git rev-parse --show-toplevel)}"
REV="${2:-HEAD}"
CANON="github.com/HelixDevelopment/skills"

fail() { echo "CM-HXC159-SOURCE-CONSUMABLE: FAIL — $1"; exit 1; }

HEAD_SHA="$(git -C "$ROOT" rev-parse HEAD)"
REV_SHA="$(git -C "$ROOT" rev-parse "$REV")"
if [ "$REV_SHA" = "$HEAD_SHA" ]; then
  # Live tree (includes staged-but-uncommitted promotion work): read go.mod
  # off disk — `git show HEAD:` would miss it before the promotion commits.
  [ -f "$ROOT/go.mod" ] || fail "rev $REV (live tree) has no root go.mod (module not promoted)"
  GOMOD="$(cat "$ROOT/go.mod")"
else
  GOMOD="$(git -C "$ROOT" show "$REV:go.mod" 2>/dev/null)" \
    || fail "rev $REV has no root go.mod (module not promoted)"
fi
echo "$GOMOD" | grep -qx "module $CANON" \
  || fail "root go.mod module line != 'module $CANON' (got: $(echo "$GOMOD" | head -1))"

if [ "$REV_SHA" != "$HEAD_SHA" ]; then
  echo "CM-HXC159-SOURCE-CONSUMABLE: PASS (go.mod-only, rev $REV_SHA != HEAD; live build/list honestly skipped)"
  exit 0
fi

go -C "$ROOT" build ./... >/dev/null 2>&1 \
  || fail "'go build ./...' is not green on the live tree"
N="$(go -C "$ROOT" list ./... 2>/dev/null | wc -l)"
[ "$N" -ge 1 ] || fail "'go list ./...' yields zero packages"
echo "CM-HXC159-SOURCE-CONSUMABLE: PASS — root go.mod module=$CANON, build green, packages=$N"
