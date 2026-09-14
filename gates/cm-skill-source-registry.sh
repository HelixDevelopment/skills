#!/usr/bin/env bash
# CM-SKILL-SOURCE-REGISTRY (HXC-159 T-P4.02, R-04)
#
# Asserts the typed source registry is fail-closed:
#   1. The collision test passes at RED_MODE=0 (typed ErrDuplicateSkill).
#   2. The same tests FAIL at RED_MODE=1 (they discriminate — a test that
#      cannot fail is a §11.4.1 bluff).
#   3. Union-count equality holds over the fixture corpora.
#
# Paired §1.1 mutation: patch Registry.Load to last-writer-wins
# (drop the DuplicateSkillError return) → this gate MUST FAIL;
# restore → PASS. Captured in qa-results/hxc159/registry/<run-id>/.
#
# Usage: gates/cm-skill-source-registry.sh [repo-root]
# Exit 0 = PASS, 1 = FAIL.
set -u
ROOT="${1:-$(git rev-parse --show-toplevel)}"

fail() { echo "CM-SKILL-SOURCE-REGISTRY: FAIL — $1"; exit 1; }

go -C "$ROOT" test ./pkg/skills/ -run 'TestCollisionFailsClosed|TestUnionCountEquality|TestDuplicateSourceNameRefused|TestManifestLoad|TestManifestRejectsUnknownDialect|TestLoadWritesNothingProvesInPlace' -count=1 >/dev/null 2>&1 \
  || fail "registry tests do not pass at RED_MODE=0"
if RED_MODE=1 go -C "$ROOT" test ./pkg/skills/ -run 'TestCollisionFailsClosed|TestDuplicateSourceNameRefused' -count=1 >/dev/null 2>&1; then
  fail "polarity check: RED_MODE=1 unexpectedly passes (tests cannot discriminate)"
fi
echo "CM-SKILL-SOURCE-REGISTRY: PASS — fail-closed collision typed, union-count equal, polarity discriminates"
