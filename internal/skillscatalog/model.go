package skillscatalog

import (
	"encoding/json"
	"strings"

	"github.com/HelixDevelopment/skills/internal/models"
)

// GeneratorVersion is embedded in every generated file's footer/README as
// machine-readable provenance (DESIGN.md §2.4 item 8, §2.1). Bump it when the
// rendering CONTRACT (file layout, section shape, banner text) changes in a
// way a downstream reader/tool might care about -- not on every unrelated
// code change. ALSO bumped (v1 -> v2, F-A review finding, round 3,
// 2026-07-16) when the roster FINGERPRINT COMPUTATION itself changes in a
// way that changes every fingerprint's byte value for the same roster
// (fingerprint.go's writeTuple -- delimiter+newline joining replaced with
// netstring length-prefixing) -- computeSidecarIdentity folds
// GeneratorVersion together with the roster fingerprint precisely so a
// downstream reader can tell a pre-fix sidecar/footer apart from a
// post-fix one.
//
// Bumped AGAIN (v2 -> v3, Finding 2 review finding, round 5, 2026-07-16):
// render.go's escapeMDCell/escapeMDInline now ALSO escape '[' and ']' --
// previously-unescaped bracket characters that let a free-text Title/Name
// value inject a live "[text](url)" hyperlink into a generator-controlled
// page (see escapeMDCell's/escapeMDInline's own doc comments for the full
// finding). This is a rendering-CONTRACT change exactly like the v1->v2
// bump above: every page whose rendered bytes contain a previously-unescaped
// '[' or ']' now renders differently, so the fingerprint SHOULD (and, via
// computeSidecarIdentity folding in GeneratorVersion, DOES) change, forcing
// regeneration on the next Generate/Verify call for any already-generated
// tree that predates this fix.
const GeneratorVersion = "skills-catalog/v3"

// canonicalRelationOrder is the six-type rendering/fingerprint order fixed by
// DESIGN.md §2.4 item 3 and §4: hard-closure types first (requires, extends,
// composes), then advisory types (recommends, related_to, alternative_to).
//
// This is DELIBERATELY NOT models.HardClosureTypes' own element order
// ({requires, composes, extends}, internal/models/skill.go:39) -- that slice
// orders types for CYCLE-DETECTION iteration, not for rendering. DESIGN.md's
// stated canonical catalog order is the literal text "requires -> extends ->
// composes -> recommends -> related_to -> alternative_to", and this
// generator follows that text exactly (§11.4.6 -- no guessing a different
// order from a same-purpose-sounding but differently-ordered constant
// elsewhere in the codebase).
var canonicalRelationOrder = []models.DependencyType{
	models.DepTypeRequires,
	models.DepTypeExtends,
	models.DepTypeComposes,
	models.DepTypeRecommends,
	models.DepTypeRelatedTo,
	models.DepTypeAlternative,
}

// relationTypeLabel is the human-readable heading for each canonical
// relation type's Dependencies subsection (DESIGN.md §2.4 item 3).
var relationTypeLabel = map[models.DependencyType]string{
	models.DepTypeRequires:    "Requires",
	models.DepTypeExtends:     "Extends",
	models.DepTypeComposes:    "Composes",
	models.DepTypeRecommends:  "Recommends",
	models.DepTypeRelatedTo:   "Related To",
	models.DepTypeAlternative: "Alternative To",
}

// skillKindOrder fixes the enumeration order of the three ALWAYS-generated
// by-kind pages (DESIGN.md §2.3: "exactly the three fixed SkillKind values"
// -- a closed set, unlike the data-driven by-domain pages).
var skillKindOrder = []models.SkillKind{
	models.SkillKindAtomic,
	models.SkillKindComposite,
	models.SkillKindUmbrella,
}

// skillStatusOrder fixes the README "By Status" count enumeration order.
var skillStatusOrder = []models.SkillStatus{
	models.SkillStatusDraft,
	models.SkillStatusValidated,
	models.SkillStatusActive,
	models.SkillStatusDeprecated,
}

// skillRecord is one fully-loaded skill plus its pre-grouped/pre-sorted
// dependency, dependent, and resource views -- the single shape every
// renderer consumes, so every sorting/grouping decision lives in exactly one
// place (load.go), never re-decided ad hoc inside a renderer.
type skillRecord struct {
	Skill    models.Skill
	Metadata models.SkillMetadata

	// DomainSlug is "" iff Metadata.Domain == "" (the _unclassified bucket).
	DomainSlug string
	NameSlug   string

	// DepsByType is keyed by canonical relation type; each slice is already
	// sorted by DependsOnName ascending (load.go's groupAndSortDeps). A
	// relation type absent from this map (or mapped to a nil/empty slice)
	// means "omit this subsection" -- renderers must never emit an empty
	// heading for it (DESIGN.md §2.4 item 3).
	DepsByType map[models.DependencyType][]models.SkillDependency

	// Dependents is the reverse-edge ("what depends on me") view, already
	// sorted by name ascending (Store.GetDependents' own ORDER BY name,
	// internal/skill/graph.go:471).
	Dependents []models.Skill

	// Resources is already sorted by (URL, Title, ResourceType, ID) --
	// Store.GetByName's own deterministic ORDER BY (internal/skill/store.go
	// resSQL, store.go:231).
	Resources []models.Resource
}

// decodeMetadata unmarshals a Skill's raw JSONB metadata column into its
// typed shape. An empty/nil raw value decodes to the zero SkillMetadata
// (Store.Create's own json.Marshal(nil RawMessage) writes the JSON literal
// "null", which json.Unmarshal into a struct pointer accepts as a no-op --
// this is not an error case).
func decodeMetadata(raw json.RawMessage) (models.SkillMetadata, error) {
	var md models.SkillMetadata
	if len(raw) == 0 {
		return md, nil
	}
	if err := json.Unmarshal(raw, &md); err != nil {
		return md, err
	}
	return md, nil
}

// slugify derives a filesystem-safe, lowercase slug (§11.4.29) from a skill
// Name or Metadata.Domain value for use as a filename component. It is a
// strict superset of DESIGN.md §2's stated rule ("replacing '.' with '_'"):
// every dotted seed-corpus identifier (e.g. "android.aosp.build_system")
// slugifies identically under either rule; this superset additionally copes
// with any other punctuation a free-text Domain value might contain, so two
// distinct domains never silently collide into one filename via undefined
// behaviour. The skill's real Name/Domain string is ALWAYS preserved
// verbatim in the page's own heading/front-matter -- slugify is a
// filename-only transform, never applied to rendered prose.
func slugify(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		default:
			// Includes '.', the ONE substitution DESIGN.md's own text
			// specifies, plus any other punctuation/whitespace a free-text
			// Domain value might contain.
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" {
		out = "_"
	}
	return out
}
