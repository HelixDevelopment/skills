# T-P3.01.3 — module-path reconciliation decision

Declared: `github.com/helixdevelopment/skill-system` (`go.mod:1` pre-promotion).
Repository URL: `github.com/HelixDevelopment/skills` (canonical; `gh`
canonicalises the old `HelixDevelopment/helix_skills` remote here via
rename-redirect — same repository).

## Decision: `github.com/HelixDevelopment/skills`

- **Name `skills`, not `skill-system`**: the import identity must be the
  repository. `skill-system` names neither the repo nor any org convention;
  keeping it mints a second identity for one codebase.
- **Case `HelixDevelopment`, not `helixdevelopment`**: exact GitHub org case.
  The lowercase form resolves today only via redirect; the RED probe
  (T-P3.01.1) proved the proxy serves `github.com/HelixDevelopment/skills`
  directly (`v0.0.0-20260718024712-315b56ce1e4c`, cache dir
  `!helix!development`). One codebase, one import path, zero redirects.
- Uppercase in a module path is legal Go (style discourages, toolchain
  accepts); the `!`-escaped cache dir above is the toolchain handling it.

## Applied

- `go.mod` module line + all `github.com/helixdevelopment/skill-system`
  import prefixes across the promoted tree (143 files) →
  `github.com/HelixDevelopment/skills` (mechanical `sed`, verified by
  `go build ./...` + zero-residual grep).
- User-facing URLs (service_readme.md badges/clone/install/issues links,
  docs/INSTALL.md install/download/issues links) →
  `github.com/HelixDevelopment/skills` (raw + releases URLs follow).
- Historical research docs under
  `docs/research/mvp/Agent_AI_Skill_Tree_Development/research/` deliberately
  UNTOUCHED — they describe the past accurately.
