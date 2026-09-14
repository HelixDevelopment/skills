# HXC-159 T-P3.03 submodule pin record

Canonical pin for the `skills` upstream checkout (Phase P3 incorporation).

- `sha=1506c914b56e13aa336b1ab4415b31ccc3607c19`
- `branch=main`

sha=1506c914b56e13aa336b1ab4415b31ccc3607c19
branch=main

Recorded 2026-09-14 (run `qa-results/hxc159/pin/20260914T002406Z/`).
The pin is a raw SHA on `main` — never a bare branch name. Per the T-P3.03
runtime signature it must remain an ANCESTOR of the live `main` tip
(asserted by `gates/cm-submodule-pin-on-main.sh` via `git merge-base
--is-ancestor`); it trails HEAD as further task commits land and is
re-recorded whenever the conductor cuts a new verified pin.
