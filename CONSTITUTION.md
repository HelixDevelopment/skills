# Helix Skills Constitution

## INHERITED FROM constitution/Constitution.md (HelixConstitution submodule)

All universal rules in `constitution/Constitution.md` apply
unconditionally to Helix Skills. This project Constitution **extends**
those universal rules with Helix Skills-specific addenda below — it
does NOT and MAY NOT weaken any universal clause. When this
Constitution disagrees with the constitution submodule, the
constitution submodule wins.

The constitution submodule is at `./constitution/` (pinned per `PIN.md`
and the `constitution` gitlink). To locate it from nested checkouts at
arbitrary depth, resolve from the repository top level
(`git rev-parse --show-toplevel`).

---

## Project scope

Helix Skills is a collection of skills for CLI AI agents, backed by the
Skill Graph service (Go module `github.com/HelixDevelopment/skills` at
the repo root, promoted from
`docs/research/mvp/Agent_AI_Skill_Tree_Development/project/` under
HXC-159 T-P3.01). It vendors the Helix Constitution as the
`constitution/` submodule and inherits every universal rule from it.

---

## Cascade anchors (minimal set binding this repo)

- **CONST-047 — recursive submodule application**: every deliverable
  produced here applies, fully and recursively, to every owned submodule
  under `vasic-digital` and `HelixDevelopment`.
- **CONST-051 — equal codebase, decoupling, dependency layout**:
  owned submodules are equal parts of this codebase; they stay
  project-not-aware (§11.4.28(B)); their checkouts resolve to
  `<repo_root>/submodules/<name>/` (**grouped** layout, per
  `helix-deps.yaml`). Nested own-org submodule chains are FORBIDDEN.
- **CONST-054 — submodule-dependency manifest**: `helix-deps.yaml` at
  this root is the authoritative own-org dependency declaration
  (schema_version 1, 7 grouped deps).
- **CONST-059 — canonical-root clarity**: the constitution submodule's
  three files are the canonical root; this file is the consumer
  extension. Universal rule → constitution file; project-specific
  rule → this file.
- **§11.4.35 — project instantiation**: this file plus `helix-deps.yaml`,
  `PIN.md`, and `gates/` supply the project-specific data the universal
  rules require (paths, prefix, roster).
- **§11.4.157 — five-carrier lockstep**: `CLAUDE.md`, `AGENTS.md`,
  `QWEN.md`, `GEMINI.md`, and this `CONSTITUTION.md` are maintained in
  lockstep; no governance change is complete until all five carry it.
