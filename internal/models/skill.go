package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
)

// SkillStatus represents the lifecycle state of a skill
type SkillStatus string

const (
	SkillStatusDraft      SkillStatus = "draft"
	SkillStatusValidated  SkillStatus = "validated"
	SkillStatusActive     SkillStatus = "active"
	SkillStatusDeprecated SkillStatus = "deprecated"
)

// DependencyType defines how skills relate to each other
type DependencyType string

const (
	DepTypeRequires   DependencyType = "requires"   // existing — hard closure
	DepTypeExtends    DependencyType = "extends"    // existing — hard closure
	DepTypeRecommends DependencyType = "recommends" // existing — advisory

	// R16 granularity/composition additions (research/skill_granularity_and_composition.md §4.1).
	DepTypeComposes    DependencyType = "composes"       // NEW — hard closure, whole→part aggregation
	DepTypeRelatedTo   DependencyType = "related_to"     // NEW — advisory, symmetric "see also"
	DepTypeAlternative DependencyType = "alternative_to" // NEW — advisory, symmetric substitute
)

// HardClosureTypes is the set of relation types the "everything needed for X"
// resolver transitively walks (research/skill_granularity_and_composition.md
// §4.2). recommends/related_to/alternative_to are advisory and are never
// auto-pulled into the required closure.
var HardClosureTypes = []DependencyType{DepTypeRequires, DepTypeComposes, DepTypeExtends}

// IsHardClosure reports whether t participates in the acyclicity-enforced
// hard-closure set (requires/composes/extends). Advisory relations
// (recommends/related_to/alternative_to) are exempt and may cycle by nature
// (research/skill_granularity_and_composition.md §4.1).
func IsHardClosure(t DependencyType) bool {
	for _, h := range HardClosureTypes {
		if h == t {
			return true
		}
	}
	return false
}

// SkillKind classifies a skill on the aggregation axis (orthogonal to
// Metadata.Complexity, which is a difficulty axis). See
// research/skill_granularity_and_composition.md §3.1.
type SkillKind string

const (
	SkillKindAtomic    SkillKind = "atomic"    // indivisible building block (default)
	SkillKindComposite SkillKind = "composite" // mid-level aggregator
	SkillKindUmbrella  SkillKind = "umbrella"  // technology/stack root; wizard entry point
)

// Skill represents a single knowledge unit in the skill graph
type Skill struct {
	ID          uuid.UUID       `json:"id" db:"id" toml:"-"`
	Name        string          `json:"name" db:"name" toml:"name"`
	Version     string          `json:"version" db:"version" toml:"version"`
	Title       string          `json:"title" db:"title" toml:"title"`
	Description string          `json:"description" db:"description" toml:"description"`
	Content     string          `json:"content" db:"content" toml:"content"`
	Metadata    json.RawMessage `json:"metadata" db:"metadata" toml:"-"`
	Status      SkillStatus     `json:"status" db:"status" toml:"-"`
	Kind        SkillKind       `json:"kind" db:"kind" toml:"kind"` // NEW (R16) — default "atomic"
	CreatedAt   time.Time       `json:"created_at" db:"created_at" toml:"-"`
	UpdatedAt   time.Time       `json:"updated_at" db:"updated_at" toml:"-"`

	// Runtime fields (not persisted directly)
	Dependencies []SkillDependency `json:"dependencies,omitempty" db:"-" toml:"-"`
	Resources    []Resource        `json:"resources,omitempty" db:"-" toml:"-"`
	Embedding    pgvector.Vector   `json:"-" db:"embedding" toml:"-"`
	TreeDepth    int               `json:"tree_depth,omitempty" db:"-" toml:"-"`
}

// NormalizeOrAtomic returns k, or SkillKindAtomic if k is empty. Use this
// before persisting a Skill whose Kind was never set (e.g. parsed from a
// TOML file that omits the optional `kind` key) so the value written
// matches the column DEFAULT rather than an empty string that would
// violate the 002_granularity CHECK constraint.
func (k SkillKind) NormalizeOrAtomic() SkillKind {
	if k == "" {
		return SkillKindAtomic
	}
	return k
}

// SkillDependency represents a directed edge in the skill DAG
type SkillDependency struct {
	SkillID      uuid.UUID      `json:"skill_id" db:"skill_id"`
	DependsOn    uuid.UUID      `json:"depends_on" db:"depends_on"`
	RelationType DependencyType `json:"relation_type" db:"relation_type"`
	Optional     bool           `json:"optional" db:"optional"`               // NEW (R16) — default false
	SortOrder    *int           `json:"sort_order,omitempty" db:"sort_order"` // NEW (R16) — component ordering; nil = unordered

	// Join fields
	DependsOnName  string `json:"depends_on_name,omitempty" db:"depends_on_name"`
	DependsOnTitle string `json:"depends_on_title,omitempty" db:"depends_on_title"`
}

// Resource is an external reference attached to a skill
type Resource struct {
	ID            uuid.UUID  `json:"id" db:"id"`
	SkillID       uuid.UUID  `json:"skill_id" db:"skill_id"`
	URL           string     `json:"url" db:"url" toml:"url"`
	Title         string     `json:"title" db:"title" toml:"title"`
	ResourceType  string     `json:"resource_type" db:"resource_type" toml:"resource_type"`
	FetchedHash   string     `json:"fetched_hash" db:"fetched_hash"`
	ContentCached string     `json:"content_cached" db:"content_cached"`
	LastValidated *time.Time `json:"last_validated" db:"last_validated"`
	CreatedAt     time.Time  `json:"created_at" db:"created_at"`
}

// Evidence is a learned pattern from a real codebase
type Evidence struct {
	ID            uuid.UUID       `json:"id" db:"id"`
	SkillID       uuid.UUID       `json:"skill_id" db:"skill_id"`
	SourceProject string          `json:"source_project" db:"source_project"`
	SourceFile    string          `json:"source_file" db:"source_file"`
	CodeSnippet   string          `json:"code_snippet" db:"code_snippet"`
	Pattern       string          `json:"pattern" db:"pattern"`
	Language      string          `json:"language" db:"language"`
	Validated     bool            `json:"validated" db:"validated"`
	Embedding     pgvector.Vector `json:"-" db:"embedding"`
	CreatedAt     time.Time       `json:"created_at" db:"created_at"`
}

// SkillRegistryEntry tracks health and completeness
type SkillRegistryEntry struct {
	SkillID     uuid.UUID  `json:"skill_id" db:"skill_id"`
	SkillName   string     `json:"skill_name" db:"skill_name"`
	MissingDeps []string   `json:"missing_deps" db:"missing_deps"`
	Stale       bool       `json:"stale" db:"stale"`
	LastReview  *time.Time `json:"last_review" db:"last_review"`
	AutoExpand  bool       `json:"auto_expand" db:"auto_expand"`
	Coverage    float64    `json:"coverage" db:"coverage"`
}

// AuditLogEntry tracks all system events
type AuditLogEntry struct {
	Timestamp time.Time       `json:"ts" db:"ts"`
	Event     string          `json:"event" db:"event"`
	SkillID   *uuid.UUID      `json:"skill_id,omitempty" db:"skill_id"`
	Details   json.RawMessage `json:"details" db:"details"`
}

// SkillTreeNode wraps a skill with tree traversal info
type SkillTreeNode struct {
	Skill    Skill           `json:"skill"`
	Depth    int             `json:"depth"`
	Children []SkillTreeNode `json:"children,omitempty"`
}

// SkillMetadata is the structured metadata for a skill
type SkillMetadata struct {
	Tags       []string `json:"tags" toml:"tags"`
	Domain     string   `json:"domain" toml:"domain"`
	Complexity string   `json:"complexity" toml:"complexity"`
}

// TOMLSkillWrapper is used for TOML import/export. It wraps a single
// TOMLSkillDef under the `[skill]` table.
//
// B1 fix (Fable code-review remediation, P1.T1): Dependencies/Resources/
// Components used to live here as WRAPPER-level fields tagged
// `toml:"skill.dependencies"` / `toml:"skill.resources"` /
// `toml:"skill.components"`, on the theory that a dotted struct tag would
// match the nested `[skill.dependencies]` / `[[skill.resources]]` /
// `[[skill.components]]` TOML tables the seed corpus actually authors (see
// any seed/skills/*.toml). It does NOT: BurntSushi/toml (the decoder
// ImportFromTOML/CreateFromTOML/skill_create actually use) only matches a
// dotted tag against a literal quoted key of that exact name — it never
// walks into a nested table for it. Decoding any real seed TOML into the
// old wrapper shape left Dependencies/Resources permanently zero-valued
// (proof: `toml.Decode` against seed/skills/android.toml, which declares
// `requires = ["java.language", "kotlin.language"]` under
// `[skill.dependencies]`, decoded Dependencies.Requires as an EMPTY slice
// and reported `skill.dependencies`/`skill.dependencies.requires`/etc. as
// Undecoded keys) — silently starving every imported skill of its
// requires/extends/recommends/composes/related_to/alternative_to edges and
// resources, with ImportFromTOML never erroring (an empty dependency list
// just resolves to zero edges). This is ALSO why the pre-existing seed
// TOML import path never actually wired any dependency/resource data
// end-to-end despite appearing to succeed — that dead-import defect is
// RESOLVED by this restructure (proven by
// TestP1T1Migration_M10_SeedTOMLsStillLoadAsAtomicAndValidatorGreen's
// extended edge-count assertion in internal/skill/migration_granularity_test.go).
//
// The fix: Dependencies/Resources/Components now live INSIDE TOMLSkillDef
// with plain (undotted) tags, so they decode/encode as genuinely nested
// TOML tables under `[skill]` — exactly matching both the seed authoring
// form and BurntSushi's actual (undotted, structural) nesting semantics.
type TOMLSkillWrapper struct {
	Skill TOMLSkillDef `toml:"skill"`
}

type TOMLSkillDef struct {
	Name        string        `toml:"name"`
	Version     string        `toml:"version"`
	Title       string        `toml:"title"`
	Description string        `toml:"description"`
	Content     string        `toml:"content"`
	Kind        string        `toml:"kind"` // NEW (R16) — atomic (default, may be omitted) | composite | umbrella
	Metadata    SkillMetadata `toml:"metadata"`

	// Dependencies/Resources/Components decode from the nested
	// `[skill.dependencies]` / `[[skill.resources]]` / `[[skill.components]]`
	// TOML tables (see B1 fix note on TOMLSkillWrapper above) because they
	// are plain-tagged fields of THIS struct, which is itself embedded under
	// the wrapper's `toml:"skill"` tag — BurntSushi resolves the nesting
	// structurally (Go struct nesting), not from any dotted tag string.
	Dependencies TOMLDependencies `toml:"dependencies"`
	Resources    []TOMLResource   `toml:"resources"`
	// Components is the OPTIONAL ergonomic array-of-tables authoring form for
	// umbrella/composite skills that need per-component ordering/optionality
	// (research/skill_granularity_and_composition.md §5.1). Each entry
	// materializes as one `composes` edge. Resolving Components into
	// composes edges is G07 scope (p1t1_granularity_schema_migration.md §2
	// L5/L11) — P1.T1 only needs the field to exist and decode correctly
	// (proven directly against BurntSushi/toml post-B1-fix; see the note
	// above for what "correctly" excludes pre-fix).
	Components []TOMLComponent `toml:"components"`
}

type TOMLDependencies struct {
	Requires   []string `toml:"requires"`
	Extends    []string `toml:"extends"`
	Recommends []string `toml:"recommends"`

	// NEW (R16 §4.1) — hard-closure "whole aggregates part" edges + advisory
	// symmetric edges. Resolving these into stored skill_dependencies rows is
	// G07 scope (p1t1_granularity_schema_migration.md §2 L5/L11); P1.T1 only
	// needs the fields to exist and decode correctly from TOML (proven
	// directly against BurntSushi/toml post-B1-fix, independent of the
	// not-yet-wired DB-persistence path — see
	// TestP1T1Migration_M10_SeedTOMLsStillLoadAsAtomicAndValidatorGreen's
	// composes/related_to decode fixture).
	Composes    []string `toml:"composes"`
	RelatedTo   []string `toml:"related_to"`
	Alternative []string `toml:"alternative_to"`

	// Alias forms normalized at import time (§4.1 alias table):
	// depends_on/prerequisite -> requires; part_of (authored on the child,
	// pointing at the parent) -> composes (inverted, parent->child). Alias
	// resolution is G07 scope; P1.T1 only needs the fields to exist.
	DependsOn    []string `toml:"depends_on"`
	Prerequisite []string `toml:"prerequisite"`
	PartOf       []string `toml:"part_of"`
}

// TOMLComponent is one entry of the `[[skill.components]]` array-of-tables —
// see TOMLSkillDef.Components.
type TOMLComponent struct {
	Name     string `toml:"name"`
	Order    int    `toml:"order"`
	Optional bool   `toml:"optional"`
	Note     string `toml:"note"`
}

type TOMLResource struct {
	URL          string `toml:"url"`
	Title        string `toml:"title"`
	ResourceType string `toml:"resource_type"`
}

// SearchResult wraps a skill with search relevance score
type SearchResult struct {
	Skill Skill   `json:"skill"`
	Score float64 `json:"score"`
}

// ExpansionJob tracks auto-expansion progress
type ExpansionJob struct {
	ID        uuid.UUID `json:"id" db:"id"`
	SkillName string    `json:"skill_name" db:"skill_name"`
	Status    string    `json:"status" db:"status"` // pending | running | completed | failed
	Depth     int       `json:"depth" db:"depth"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

// LearningJob tracks codebase analysis progress
type LearningJob struct {
	ID             uuid.UUID `json:"id" db:"id"`
	ProjectPath    string    `json:"project_path" db:"project_path"`
	Status         string    `json:"status" db:"status"`
	Languages      []string  `json:"languages" db:"languages"`
	FilesProcessed int       `json:"files_processed" db:"files_processed"`
	PatternsFound  int       `json:"patterns_found" db:"patterns_found"`
	CreatedAt      time.Time `json:"created_at" db:"created_at"`
	UpdatedAt      time.Time `json:"updated_at" db:"updated_at"`
}
