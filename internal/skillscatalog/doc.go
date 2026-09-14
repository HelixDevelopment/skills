// Package skillscatalog generates a deterministic, byte-stable Markdown
// reference catalog for the live skill graph (a top-level README + INDEX,
// by-domain and by-kind grouping pages, and one detail page per skill),
// together with a sha256 roster fingerprint sidecar (§11.4.86) that a later
// layer (CLI/REST/MCP/worker/git-hook -- G126+, explicitly OUT of this
// package's scope) can use to detect drift and re-arm regeneration.
//
// This package is purely additive and read-only against the existing skill
// graph: it consumes internal/skill.Store ONLY through its existing exported
// read methods (ListSkills, GetByName, GetDependents, Pool) and never
// modifies any existing file in internal/skill or internal/models.
//
// See the sibling research scratchpad at
// docs/research/mvp/Agent_AI_Skill_Tree_Development/research/
// (DESIGN.md/CODEBASE_MAP.md/TRACKED_ITEMS.md, item G125) for the full design this package implements the "Markdown-generation
// CORE" of.
package skillscatalog
