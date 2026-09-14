package skill

// G07 re-review remediation (F1–F5) — live-DB round-trip integrity.
//
// Design: research/g06_g07_skilltree_dag_design.md §2.2/§2.3/§4.
// Register: GAPS_AND_RISKS_REGISTER.md G07.
//
// §11.4.115 RED-first: every case here FAILS on the pre-remediation code and
// PASSES post-fix; each load-bearing fix has a paired §1.1 mutation
// (reverting the fix reproduces the RED). Gated on the same
// SKILL_SYSTEM_TEST_DB_* live-DB contract as the rest of the package
// (g07NewLiveStore → skillSkipIfNoTestDB); absent a Postgres it honestly
// t.Skip()s (§11.4.3/§11.4.27).

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/HelixDevelopment/skills/internal/models"
)

// g07StripHeader returns the TOML body with the leading comment header
// (research/g06_g07_skilltree_dag_design.md §2.3(3) permits the comment header
// as a documented normalization) removed — every line up to and including the
// first `[skill]` table header is dropped, leaving the deterministic body.
func g07StripHeader(b []byte) string {
	s := string(b)
	if i := strings.Index(s, "[skill]"); i >= 0 {
		return s[i:]
	}
	return s
}

// TestG07AllSixEdgeTypesRoundTrip (F4a): every one of the six typed edges —
// the three hard-closure (requires/extends/composes) AND the three advisory
// (recommends/related_to/alternative_to) — survives an export→import round-trip
// with its relation_type intact. Pre-fix ExportToTOML/ImportFromTOML only
// handled requires/extends/recommends, so extends here plus composes/
// related_to/alternative_to were the RED set.
func TestG07AllSixEdgeTypesRoundTrip(t *testing.T) {
	ctx, store, cleanup := g07NewLiveStore(t)
	if store == nil {
		return
	}
	defer cleanup()

	targets := map[string]models.DependencyType{
		"rt6.req":  models.DepTypeRequires,
		"rt6.ext":  models.DepTypeExtends,
		"rt6.rec":  models.DepTypeRecommends,
		"rt6.comp": models.DepTypeComposes,
		"rt6.rel":  models.DepTypeRelatedTo,
		"rt6.alt":  models.DepTypeAlternative,
	}
	for name := range targets {
		g07ImportLeaf(t, ctx, store, name)
	}

	srcTOML := `
[skill]
name = "rt6.src"
version = "0.3.0"
title = "six-type source"
content = "content"
kind = "composite"

[skill.dependencies]
requires = ["rt6.req"]
extends = ["rt6.ext"]
recommends = ["rt6.rec"]
composes = ["rt6.comp"]
related_to = ["rt6.rel"]
alternative_to = ["rt6.alt"]
`
	if _, err := store.ImportFromTOML(ctx, []byte(srcTOML)); err != nil {
		t.Fatalf("import six-type source: %v", err)
	}

	exported, err := store.ExportToTOML(ctx, "rt6.src")
	if err != nil {
		t.Fatalf("ExportToTOML: %v", err)
	}
	// Re-import under a fresh name via the codec (never a brittle text edit).
	var w models.TOMLSkillWrapper
	if err := toml.Unmarshal(exported, &w); err != nil {
		t.Fatalf("decode exported: %v", err)
	}
	w.Skill.Name = "rt6.src.rt"
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(w); err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	if _, err := store.ImportFromTOML(ctx, buf.Bytes()); err != nil {
		t.Fatalf("re-import: %v", err)
	}

	rt, err := store.GetByName(ctx, "rt6.src.rt")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	got := make(map[string]models.DependencyType, len(rt.Dependencies))
	for _, d := range rt.Dependencies {
		got[d.DependsOnName] = d.RelationType
	}
	for name, wantRel := range targets {
		if gotRel, ok := got[name]; !ok {
			t.Errorf("round-trip lost edge to %q (want %q); survived: %v", name, wantRel, got)
		} else if gotRel != wantRel {
			t.Errorf("edge to %q has relation_type=%q, want %q", name, gotRel, wantRel)
		}
	}
}

// TestG07Components_OptionalSortOrderRoundTrip (F3): a composes edge authored
// through [[skill.components]] (carrying order + optional) must export back
// through the [[skill.components]] carrier so those attrs survive the
// round-trip. Pre-F3 ExportToTOML emitted such an edge as a bare
// `composes = [...]` entry, dropping optional/sort_order — so the re-imported
// edge came back with SortOrder=nil, Optional=false (the RED).
func TestG07Components_OptionalSortOrderRoundTrip(t *testing.T) {
	ctx, store, cleanup := g07NewLiveStore(t)
	if store == nil {
		return
	}
	defer cleanup()

	g07ImportLeaf(t, ctx, store, "cmp.a")
	g07ImportLeaf(t, ctx, store, "cmp.b")

	srcTOML := `
[skill]
name = "cmp.umbrella"
version = "0.1.0"
title = "umbrella"
content = "content"
kind = "umbrella"

[[skill.components]]
name = "cmp.a"
order = 2
optional = true

[[skill.components]]
name = "cmp.b"
order = 1
optional = false
`
	if _, err := store.ImportFromTOML(ctx, []byte(srcTOML)); err != nil {
		t.Fatalf("import umbrella: %v", err)
	}

	exported, err := store.ExportToTOML(ctx, "cmp.umbrella")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	// The exported TOML must carry the component form, not a bare composes list.
	if !strings.Contains(string(exported), "[[skill.components]]") {
		t.Errorf("export dropped the [[skill.components]] carrier; got:\n%s", exported)
	}

	var w models.TOMLSkillWrapper
	if err := toml.Unmarshal(exported, &w); err != nil {
		t.Fatalf("decode exported: %v", err)
	}
	w.Skill.Name = "cmp.umbrella.rt"
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(w); err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	if _, err := store.ImportFromTOML(ctx, buf.Bytes()); err != nil {
		t.Fatalf("re-import: %v", err)
	}

	rt, err := store.GetByName(ctx, "cmp.umbrella.rt")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	byName := make(map[string]models.SkillDependency, len(rt.Dependencies))
	for _, d := range rt.Dependencies {
		byName[d.DependsOnName] = d
	}
	a, ok := byName["cmp.a"]
	if !ok {
		t.Fatalf("round-trip lost the cmp.a component edge; edges: %v", byName)
	}
	if a.RelationType != models.DepTypeComposes {
		t.Errorf("cmp.a relation_type=%q, want composes", a.RelationType)
	}
	if !a.Optional {
		t.Errorf("cmp.a Optional=false, want true (dropped on export→import)")
	}
	if a.SortOrder == nil || *a.SortOrder != 2 {
		t.Errorf("cmp.a SortOrder=%v, want 2 (dropped on export→import)", a.SortOrder)
	}
	b := byName["cmp.b"]
	if b.SortOrder == nil || *b.SortOrder != 1 {
		t.Errorf("cmp.b SortOrder=%v, want 1", b.SortOrder)
	}
}

// TestG07AliasFold_Idempotent_NoDuplicateKey (F2): a target named by both
// `requires` and its same-direction alias `depends_on` (both fold to requires),
// AND a target named by both `composes` and a [[skill.components]] entry, must
// each fold to ONE edge — the import stays idempotent instead of aborting with
// a raw Postgres duplicate-key (23505) on the (skill_id, depends_on,
// relation_type) PK. The component's optional/sort_order must win the fold.
func TestG07AliasFold_Idempotent_NoDuplicateKey(t *testing.T) {
	ctx, store, cleanup := g07NewLiveStore(t)
	if store == nil {
		return
	}
	defer cleanup()

	g07ImportLeaf(t, ctx, store, "fold.req")
	g07ImportLeaf(t, ctx, store, "fold.comp")

	srcTOML := `
[skill]
name = "fold.src"
version = "0.1.0"
title = "alias fold"
content = "content"
kind = "composite"

[skill.dependencies]
requires = ["fold.req"]
depends_on = ["fold.req"]
composes = ["fold.comp"]

[[skill.components]]
name = "fold.comp"
order = 7
optional = true
`
	if _, err := store.ImportFromTOML(ctx, []byte(srcTOML)); err != nil {
		t.Fatalf("import with folded duplicates should succeed idempotently, got: %v", err)
	}

	rt, err := store.GetByName(ctx, "fold.src")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	var reqCount, compCount int
	var compEdge models.SkillDependency
	for _, d := range rt.Dependencies {
		switch {
		case d.DependsOnName == "fold.req" && d.RelationType == models.DepTypeRequires:
			reqCount++
		case d.DependsOnName == "fold.comp" && d.RelationType == models.DepTypeComposes:
			compCount++
			compEdge = d
		}
	}
	if reqCount != 1 {
		t.Errorf("requires(fold.req) edge count = %d, want 1 (requires+depends_on must fold to one edge)", reqCount)
	}
	if compCount != 1 {
		t.Errorf("composes(fold.comp) edge count = %d, want 1 (composes list + component must fold to one edge)", compCount)
	}
	// Fold must keep the richer carrier: the component's attrs.
	if compEdge.SortOrder == nil || *compEdge.SortOrder != 7 || !compEdge.Optional {
		t.Errorf("folded composes edge lost component attrs: SortOrder=%v Optional=%v, want 7/true", compEdge.SortOrder, compEdge.Optional)
	}
}

// TestG07PartOf_Inverts_NoPartialLoss (F1, reconciled HXC-159 T-P3.01.5 per
// §11.4.120): `part_of` is now WIRED — a non-empty alias materializes the
// parent→child `composes` edge instead of hard-erroring. The F1 core (no
// silent loss, no partial persist) is asserted here against the NEW
// mechanism; the full case battery (absent-parent, fold, cycle) lives in
// part_of_inversion_test.go. The pre-fix version of this test asserted
// ErrPartOfUnsupported; that contract is void by design.
func TestG07PartOf_Inverts_NoPartialLoss(t *testing.T) {
	ctx, store, cleanup := g07NewLiveStore(t)
	if store == nil {
		return
	}
	defer cleanup()

	g07ImportLeaf(t, ctx, store, "po.parent")

	body := `
[skill]
name = "po.child"
version = "0.1.0"
title = "part_of child"
content = "content"

[skill.dependencies]
part_of = ["po.parent"]
`
	child, err := store.ImportFromTOML(ctx, []byte(body))
	if err != nil {
		t.Fatalf("import with part_of failed post-wiring: %v", err)
	}
	parent, err := store.GetByName(ctx, "po.parent")
	if err != nil {
		t.Fatalf("GetByName(po.parent): %v", err)
	}
	found := false
	for _, d := range parent.Dependencies {
		if d.RelationType == models.DepTypeComposes && d.DependsOn == child.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("no parent→child composes edge on po.parent (silent loss of part_of)")
	}
}

// TestG07StrictDecode_TypoDepKey_HardErrors (F5ii): a typo'd dependency key
// (`requiress` under [skill.dependencies]) decodes into nothing and would be
// SILENTLY DROPPED under the pre-fix toml.Unmarshal — the strict-decode guard
// must turn it into a hard error (ErrInvalidSkill) with no partial persist.
func TestG07StrictDecode_TypoDepKey_HardErrors(t *testing.T) {
	ctx, store, cleanup := g07NewLiveStore(t)
	if store == nil {
		return
	}
	defer cleanup()

	g07ImportLeaf(t, ctx, store, "sd.target")

	body := `
[skill]
name = "sd.typo"
version = "0.1.0"
title = "typo dep key"
content = "content"

[skill.dependencies]
requiress = ["sd.target"]
`
	_, err := store.ImportFromTOML(ctx, []byte(body))
	if err == nil {
		t.Fatalf("import with a typo'd dependency key succeeded; want a strict-decode hard error (silent edge drop is forbidden)")
	}
	if !errors.Is(err, ErrInvalidSkill) {
		t.Errorf("import error = %v, want it to wrap ErrInvalidSkill", err)
	}
	if _, gerr := store.GetByName(ctx, "sd.typo"); gerr == nil {
		t.Errorf("sd.typo was created despite the strict-decode error (no-partial-persist violated)")
	}
}

// TestG07StrictDecode_StatusKeyStillIgnored (F5ii scoping proof): the
// strict-decode guard is scoped to edge/resource containers ONLY, so a
// deliberately-ignored top-level `status` key must NOT be rejected — the live
// MCP skill_create fail-closed-to-draft contract
// (internal/mcp/skill_create_draft_test.go) depends on it. Import must succeed
// and the skill must land as draft.
func TestG07StrictDecode_StatusKeyStillIgnored(t *testing.T) {
	ctx, store, cleanup := g07NewLiveStore(t)
	if store == nil {
		return
	}
	defer cleanup()

	body := `
[skill]
name = "sd.status"
version = "0.1.0"
title = "status ignored"
content = "content"
status = "active"
`
	sk, err := store.ImportFromTOML(ctx, []byte(body))
	if err != nil {
		t.Fatalf("import with an ignored top-level status key must succeed (scoped strict-decode), got: %v", err)
	}
	if sk.Status != models.SkillStatusDraft {
		t.Errorf("Status = %q, want draft (a submitted status must never promote at creation)", sk.Status)
	}
}

// TestG07AdvisorySoftSkip_MissingTarget (F4a / F5iii): an advisory edge
// (recommends) to a target that does not exist is soft-skipped — the import
// succeeds, the hard-closure edge persists, and no advisory edge is created —
// documenting the deliberate divergence from §2.3(4) for advisory relations.
func TestG07AdvisorySoftSkip_MissingTarget(t *testing.T) {
	ctx, store, cleanup := g07NewLiveStore(t)
	if store == nil {
		return
	}
	defer cleanup()

	g07ImportLeaf(t, ctx, store, "adv.req")

	body := `
[skill]
name = "adv.src"
version = "0.1.0"
title = "advisory soft-skip"
content = "content"

[skill.dependencies]
requires = ["adv.req"]
recommends = ["adv.nonexistent"]
`
	if _, err := store.ImportFromTOML(ctx, []byte(body)); err != nil {
		t.Fatalf("import with an advisory edge to a missing target must succeed (soft-skip), got: %v", err)
	}
	rt, err := store.GetByName(ctx, "adv.src")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	var haveReq, haveRec bool
	for _, d := range rt.Dependencies {
		if d.DependsOnName == "adv.req" && d.RelationType == models.DepTypeRequires {
			haveReq = true
		}
		if d.RelationType == models.DepTypeRecommends {
			haveRec = true
		}
	}
	if !haveReq {
		t.Errorf("hard-closure requires edge missing after advisory soft-skip")
	}
	if haveRec {
		t.Errorf("an advisory edge to a missing target was persisted; want soft-skip (none)")
	}
}

// TestG07ByteStable_ExportImportExport (F4a byte-stability oracle): the TOML
// BODY (modulo the documented comment-header normalization, §2.3(3)) is stable
// across export→import→export — proving deterministic export ordering (F4c
// ORDER BY) + the component carrier (F3) together yield an idempotent codec.
func TestG07ByteStable_ExportImportExport(t *testing.T) {
	ctx, store, cleanup := g07NewLiveStore(t)
	if store == nil {
		return
	}
	defer cleanup()

	for _, n := range []string{"bs.b", "bs.a", "bs.c", "bs.d"} {
		g07ImportLeaf(t, ctx, store, n)
	}

	srcTOML := `
[skill]
name = "bs.src"
version = "0.1.0"
title = "byte stable"
content = "content"
kind = "composite"

[skill.dependencies]
requires = ["bs.b", "bs.a"]

[[skill.components]]
name = "bs.d"
order = 2
optional = false

[[skill.components]]
name = "bs.c"
order = 1
optional = true

[[skill.resources]]
url = "https://example.test/z"
title = "z"
resource_type = "official-doc"

[[skill.resources]]
url = "https://example.test/a"
title = "a"
resource_type = "official-doc"
`
	if _, err := store.ImportFromTOML(ctx, []byte(srcTOML)); err != nil {
		t.Fatalf("import bs.src: %v", err)
	}

	e1, err := store.ExportToTOML(ctx, "bs.src")
	if err != nil {
		t.Fatalf("export1: %v", err)
	}
	var w models.TOMLSkillWrapper
	if err := toml.Unmarshal(e1, &w); err != nil {
		t.Fatalf("decode e1: %v", err)
	}
	w.Skill.Name = "bs.src.rt"
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(w); err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	if _, err := store.ImportFromTOML(ctx, buf.Bytes()); err != nil {
		t.Fatalf("re-import: %v", err)
	}
	e2, err := store.ExportToTOML(ctx, "bs.src.rt")
	if err != nil {
		t.Fatalf("export2: %v", err)
	}

	// Normalize: strip the comment header, then the (deliberately different)
	// skill name — everything ELSE must be byte-identical.
	b1 := strings.Replace(g07StripHeader(e1), `name = "bs.src"`, `name = "NAME"`, 1)
	b2 := strings.Replace(g07StripHeader(e2), `name = "bs.src.rt"`, `name = "NAME"`, 1)
	if b1 != b2 {
		t.Errorf("export→import→export not byte-stable.\n--- first export body ---\n%s\n--- second export body ---\n%s", b1, b2)
	}
}

// TestG07ByteStable_DuplicateURLResources (R1 re-review residual): two resources
// that SHARE a url but differ in (title, resource_type) are schema-legal — there
// is NO unique constraint on resources.url (migrations/001_initial.up.sql). The
// GetByName resources query must order such rows by STABLE columns so an
// export→import→export is byte-stable (design §2.3(3), an UNCONDITIONAL
// contract). The pre-fix `ORDER BY url, id` breaks this: id is a v4-random UUID
// re-minted on every ImportFromTOML, so two same-URL/distinct-content rows are
// ordered by an unstable key and reorder across the round-trip. The fixed
// `ORDER BY url, title, resource_type, id` orders by the exact columns the export
// emits, so the residual id-tie only ever breaks a tie between BYTE-IDENTICAL
// rows (swap-invariant) and every export body is identical.
//
// The bug manifests as RANDOMNESS (per-import UUID draw), so a single round-trip
// is stable ~1/2 the time on the buggy code — not a reliable RED. This loops the
// round-trip N times and asserts every export body equals the first: on the
// fixed ordering all N match deterministically; on the buggy `url, id` ordering
// P(all N match) ~ 2^-N (~1e-9 at N=30), so the reorder is detected
// near-deterministically. §1.1: reverting the ORDER BY to `url, id` makes this
// FAIL.
func TestG07ByteStable_DuplicateURLResources(t *testing.T) {
	ctx, store, cleanup := g07NewLiveStore(t)
	if store == nil {
		return
	}
	defer cleanup()

	// Two resources sharing ONE url, distinct (title, resource_type). Titles are
	// chosen so title-ascending order (zzz after aaa) differs from insertion
	// order, and resource_type differs too, so the exported bodies are NOT
	// byte-identical — the id-tiebreak cannot mask a reorder here.
	srcTOML := `
[skill]
name = "dupurl.src.0"
version = "0.1.0"
title = "duplicate-URL resources"
content = "content"

[[skill.resources]]
url = "https://example.test/shared"
title = "zzz title"
resource_type = "article"

[[skill.resources]]
url = "https://example.test/shared"
title = "aaa title"
resource_type = "official-doc"
`
	if _, err := store.ImportFromTOML(ctx, []byte(srcTOML)); err != nil {
		t.Fatalf("import dupurl.src.0: %v", err)
	}

	norm := func(b []byte, name string) string {
		return strings.Replace(g07StripHeader(b), `name = "`+name+`"`, `name = "NAME"`, 1)
	}

	e0, err := store.ExportToTOML(ctx, "dupurl.src.0")
	if err != nil {
		t.Fatalf("export dupurl.src.0: %v", err)
	}
	canonical := norm(e0, "dupurl.src.0")

	const N = 30
	prev := "dupurl.src.0"
	for i := 1; i <= N; i++ {
		exported, err := store.ExportToTOML(ctx, prev)
		if err != nil {
			t.Fatalf("export iter %d (%q): %v", i, prev, err)
		}
		var w models.TOMLSkillWrapper
		if err := toml.Unmarshal(exported, &w); err != nil {
			t.Fatalf("decode iter %d: %v", i, err)
		}
		name := fmt.Sprintf("dupurl.src.%d", i)
		w.Skill.Name = name
		var buf bytes.Buffer
		if err := toml.NewEncoder(&buf).Encode(w); err != nil {
			t.Fatalf("re-encode iter %d: %v", i, err)
		}
		if _, err := store.ImportFromTOML(ctx, buf.Bytes()); err != nil {
			t.Fatalf("re-import iter %d: %v", i, err)
		}
		e, err := store.ExportToTOML(ctx, name)
		if err != nil {
			t.Fatalf("export-after iter %d: %v", i, err)
		}
		if got := norm(e, name); got != canonical {
			t.Fatalf("duplicate-URL resources reordered at iter %d — export→import→export not byte-stable (unstable `url, id` ordering).\n--- canonical body ---\n%s\n--- iter %d body ---\n%s", i, canonical, i, got)
		}
		prev = name
	}
}

// TestG07DedupKey_SameTargetTwoRelations_BothSurvive (R2 re-review residual):
// dedupDepEdges (import_export.go) keys on (targetID, relationType). A single
// target named by BOTH a `requires` edge AND a `recommends` edge — the SAME
// target under two DIFFERENT relation types — must therefore yield TWO distinct
// edges. Collapsing the dedup key to target-only would silently drop the
// second-relation (recommends) edge: the exact G07 silent-loss class the
// remediation exists to close. Every other G07 fixture uses distinct targets per
// relation, so a target-only key leaves them all GREEN — this case is the
// dedicated §1.1 mutation-killer for the relationType component of the key
// (collapse edgeKey to target-only → this test FAILs).
func TestG07DedupKey_SameTargetTwoRelations_BothSurvive(t *testing.T) {
	ctx, store, cleanup := g07NewLiveStore(t)
	if store == nil {
		return
	}
	defer cleanup()

	g07ImportLeaf(t, ctx, store, "dk.x")

	srcTOML := `
[skill]
name = "dk.src"
version = "0.1.0"
title = "same-target two-relation"
content = "content"
kind = "composite"

[skill.dependencies]
requires = ["dk.x"]
recommends = ["dk.x"]
`
	if _, err := store.ImportFromTOML(ctx, []byte(srcTOML)); err != nil {
		t.Fatalf("import dk.src (requires+recommends on one target): %v", err)
	}

	rt, err := store.GetByName(ctx, "dk.src")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	var haveRequires, haveRecommends bool
	for _, d := range rt.Dependencies {
		if d.DependsOnName != "dk.x" {
			continue
		}
		switch d.RelationType {
		case models.DepTypeRequires:
			haveRequires = true
		case models.DepTypeRecommends:
			haveRecommends = true
		}
	}
	if !haveRequires {
		t.Errorf("requires edge to dk.x missing (both relations on one target must survive the dedup fold)")
	}
	if !haveRecommends {
		t.Errorf("recommends edge to dk.x missing — the dedup key dropped a same-target second-relation edge (target-only key regression)")
	}
}
