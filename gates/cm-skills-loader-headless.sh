#!/usr/bin/env bash
# CM-SKILLS-LOADER-HEADLESS (HXC-159 T-P4.06, R-09)
#
# The loader is a data/registry component: it must not import Fyne or any
# GL-linked package. This host lacks X11/GL headers — the constraint is a
# gate, not a hope:
#   1. No forbidden import string (fyne, opengl, X11/x11, vulkan, glfw,
#      egl) appears in pkg/skills/*.go — catches a direct import even when
#      the module is absent from go.mod (go list alone would report "no
#      such module", which step 2 also treats as FAIL).
#   2. `go list -deps ./pkg/skills` succeeds and its closure contains no
#      forbidden dependency — catches a TRANSITIVE GUI pull-in.
#   3. The full loader suite exits 0 on this header-less host (RS-16: the
#      run itself is the proof — a cgo GL dependency would fail to build).
#
# Paired §1.1 mutation: append a Fyne import to the package → this gate
# MUST FAIL; restore → PASS.
#
# Usage: gates/cm-skills-loader-headless.sh [repo-root]
# Exit 0 = PASS, 1 = FAIL.
set -u
ROOT="${1:-$(git rev-parse --show-toplevel)}"

fail() { echo "CM-SKILLS-LOADER-HEADLESS: FAIL — $1"; exit 1; }

if grep -rEin 'fyne|opengl|[^a-zA-Z]X11|[^a-zA-Z]x11|vulkan|glfw|[^a-zA-Z]egl\.h|GL/gl\.h|GLX' "$ROOT/pkg/skills/" --include='*.go' >/dev/null 2>&1; then
  grep -rEin 'fyne|opengl|[^a-zA-Z]X11|[^a-zA-Z]x11|vulkan|glfw|[^a-zA-Z]egl\.h|GL/gl\.h|GLX' "$ROOT/pkg/skills/" --include='*.go' | head -5
  fail "forbidden GUI import string in pkg/skills"
fi

DEPS="$(go -C "$ROOT" list -deps ./pkg/skills/ 2>&1)" \
  || fail "go list -deps failed (a cgo/GUI dep would break the build here): $(echo "$DEPS" | head -3)"
if echo "$DEPS" | grep -Ei 'fyne|opengl|x11|vulkan|glfw|ebiten|raylib' >/dev/null 2>&1; then
  echo "$DEPS" | grep -Ei 'fyne|opengl|x11|vulkan|glfw|ebiten|raylib' | head -5
  fail "GUI dependency in loader closure"
fi

go -C "$ROOT" test ./pkg/skills/ -count=1 >/dev/null 2>&1 \
  || fail "loader suite does not exit 0 on this header-less host"

echo "CM-SKILLS-LOADER-HEADLESS: PASS — no Fyne/GL/X11 in source or dep closure; suite green without headers"
