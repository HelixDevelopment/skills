package skillscatalog

// Real-DB (§11.4.27, no mocks beyond unit-test-class fixtures), TDD RED-first
// (§11.4.115) test suite for the skillscatalog generator (G125, DESIGN.md §6
// anti-bluff proof plan). Every test that needs a live PostgreSQL creates its
// OWN uniquely-named throwaway database via catalogCreateThrowawayDB
// (testdb_helper_test.go) -- never a fixed/shared DB owner (§11.4.119) -- and
// honestly t.Skip()s when SKILL_SYSTEM_TEST_DB_HOST is unset (§11.4.3).

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/HelixDevelopment/skills/internal/skill"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Fixture builders
// ---------------------------------------------------------------------------

// goldenGoodFixture seeds the DESIGN.md §6 item 1 golden-good fixture: 5
// skills spanning all 3 SkillKind values, all 4 SkillStatus values, all 6
// DependencyType relation types, >=1 resource, and >=1 skill with an empty
// Domain (to exercise the _unclassified bucket). Returns the created skills
// keyed by name (with .ID populated by Store.Create).
func goldenGoodFixture(ctx context.Context, t *testing.T, store *skill.Store) map[string]*models.Skill {
	t.Helper()

	mkSkill := func(name, title, desc, content string, kind models.SkillKind, status models.SkillStatus, domain, complexity string, tags []string) *models.Skill {
		md, err := jsonMarshalMetadata(models.SkillMetadata{Tags: tags, Domain: domain, Complexity: complexity})
		if err != nil {
			t.Fatalf("marshal metadata for %s: %v", name, err)
		}
		sk := &models.Skill{
			Name:        name,
			Version:     "1.0.0",
			Title:       title,
			Description: desc,
			Content:     content,
			Metadata:    md,
			Status:      status,
			Kind:        kind,
		}
		if err := store.Create(ctx, sk); err != nil {
			t.Fatalf("create skill %s: %v", name, err)
		}
		return sk
	}

	helper := mkSkill("core.helper", "Core Helper", "A small atomic helper skill.", "# Core Helper\n\nBody content for helper.\n",
		models.SkillKindAtomic, models.SkillStatusDraft, "core", "beginner", []string{"helper", "core"})
	util := mkSkill("core.util", "Core Util", "A composite utility skill.", "# Core Util\n\nBody content for util.\n",
		models.SkillKindComposite, models.SkillStatusValidated, "core", "intermediate", []string{"util", "core"})
	addon := mkSkill("core.addon", "Core Addon", "An optional addon skill.", "# Core Addon\n\nBody content for addon.\n",
		models.SkillKindAtomic, models.SkillStatusDeprecated, "core", "beginner", []string{"addon"})
	alt := mkSkill("core.alt", "Core Alt", "An alternative skill in a different domain.", "# Core Alt\n\nBody content for alt.\n",
		models.SkillKindAtomic, models.SkillStatusActive, "extra", "advanced", []string{"alt", "extra"})

	// core.foundation is created LAST, with its hard-closure + advisory
	// dependencies embedded directly (Store.Create's own dependency-insert
	// path, store.go:397-409) so the `composes` edge can carry
	// Optional=true + a non-nil SortOrder -- Store.AddDependency's INSERT
	// (graph.go:98-101) does not set those two columns at all, so this is
	// the only existing Store write path that can produce them.
	one := 1
	foundationMD, err := jsonMarshalMetadata(models.SkillMetadata{Tags: []string{"foundation"}, Domain: "", Complexity: "advanced"})
	if err != nil {
		t.Fatalf("marshal foundation metadata: %v", err)
	}
	foundation := &models.Skill{
		Name:        "core.foundation",
		Version:     "1.0.0",
		Title:       "Core Foundation",
		Description: "The umbrella foundation skill exercising all six relation types.",
		Content:     "# Core Foundation\n\nBody content for foundation.\n",
		Metadata:    foundationMD,
		Status:      models.SkillStatusActive,
		Kind:        models.SkillKindUmbrella,
		Dependencies: []models.SkillDependency{
			{DependsOn: helper.ID, RelationType: models.DepTypeRequires},
			{DependsOn: util.ID, RelationType: models.DepTypeExtends},
			{DependsOn: addon.ID, RelationType: models.DepTypeComposes, Optional: true, SortOrder: &one},
			{DependsOn: alt.ID, RelationType: models.DepTypeRecommends},
		},
	}
	if err := store.Create(ctx, foundation); err != nil {
		t.Fatalf("create skill core.foundation: %v", err)
	}

	if err := store.AddDependency(ctx, util.ID, alt.ID, models.DepTypeRelatedTo); err != nil {
		t.Fatalf("add related_to util->alt: %v", err)
	}
	if err := store.AddDependency(ctx, helper.ID, addon.ID, models.DepTypeAlternative); err != nil {
		t.Fatalf("add alternative_to helper->addon: %v", err)
	}

	if err := store.AddResource(ctx, &models.Resource{
		SkillID: foundation.ID, URL: "https://example.com/b-doc", Title: "B Doc", ResourceType: "article",
	}); err != nil {
		t.Fatalf("add resource b-doc: %v", err)
	}
	if err := store.AddResource(ctx, &models.Resource{
		SkillID: foundation.ID, URL: "https://example.com/a-doc", Title: "A Doc", ResourceType: "official-doc",
	}); err != nil {
		t.Fatalf("add resource a-doc: %v", err)
	}

	return map[string]*models.Skill{
		"core.foundation": foundation,
		"core.util":       util,
		"core.helper":     helper,
		"core.addon":      addon,
		"core.alt":        alt,
	}
}

// ---------------------------------------------------------------------------
// T1: golden-good structural + content + determinism proof
// ---------------------------------------------------------------------------

func TestSkillsCatalog_GoldenGood(t *testing.T) {
	admin, ok := catalogSkipIfNoTestDB(t)
	if !ok {
		return
	}
	pool, cleanup := catalogCreateThrowawayDB(t, admin)
	defer cleanup()

	ctx := context.Background()
	store := skill.NewStore(pool)
	goldenGoodFixture(ctx, t, store)

	outDir := t.TempDir()
	cfg := DefaultConfig()

	regenerated, fp1, err := Generate(ctx, store, outDir, cfg)
	if err != nil {
		t.Fatalf("Generate (1st call): %v", err)
	}
	if !regenerated {
		t.Fatalf("Generate (1st call): want regenerated=true on a brand-new outputDir, got false")
	}
	if len(fp1) != 64 {
		t.Fatalf("Generate (1st call): want a 64-hex-char sha256 fingerprint, got %q (len=%d)", fp1, len(fp1))
	}

	t.Run("TreeStructure", func(t *testing.T) {
		mustExist := []string{
			"README.md",
			"INDEX.md",
			".catalog_fingerprint",
			filepath.Join("by-domain", "core.md"),
			filepath.Join("by-domain", "extra.md"),
			filepath.Join("by-domain", "_unclassified.md"),
			filepath.Join("by-kind", "atomic.md"),
			filepath.Join("by-kind", "composite.md"),
			filepath.Join("by-kind", "umbrella.md"),
			filepath.Join("skill", "core_foundation.md"),
			filepath.Join("skill", "core_util.md"),
			filepath.Join("skill", "core_helper.md"),
			filepath.Join("skill", "core_addon.md"),
			filepath.Join("skill", "core_alt.md"),
		}
		for _, rel := range mustExist {
			if _, err := os.Stat(filepath.Join(outDir, rel)); err != nil {
				t.Errorf("expected generated file %s to exist: %v", rel, err)
			}
		}

		// Domain grouping is data-driven, not hardcoded: by-domain/core.md
		// must contain core.util/core.helper/core.addon, NOT core.alt
		// (domain "extra") or core.foundation (unclassified).
		coreDomain := mustReadFile(t, filepath.Join(outDir, "by-domain", "core.md"))
		for _, want := range []string{"core.util", "core.helper", "core.addon"} {
			if !strings.Contains(coreDomain, want) {
				t.Errorf("by-domain/core.md missing member %q", want)
			}
		}
		for _, wantAbsent := range []string{"core.alt", "core.foundation"} {
			if strings.Contains(coreDomain, wantAbsent) {
				t.Errorf("by-domain/core.md wrongly contains non-member %q", wantAbsent)
			}
		}

		unclassified := mustReadFile(t, filepath.Join(outDir, "by-domain", "_unclassified.md"))
		if !strings.Contains(unclassified, "core.foundation") {
			t.Errorf("by-domain/_unclassified.md missing core.foundation")
		}

		umbrella := mustReadFile(t, filepath.Join(outDir, "by-kind", "umbrella.md"))
		if !strings.Contains(umbrella, "core.foundation") {
			t.Errorf("by-kind/umbrella.md missing core.foundation")
		}
		composite := mustReadFile(t, filepath.Join(outDir, "by-kind", "composite.md"))
		if !strings.Contains(composite, "core.util") {
			t.Errorf("by-kind/composite.md missing core.util")
		}
	})

	t.Run("SkillDetailPage_RealFieldsNotPlaceholders", func(t *testing.T) {
		page := mustReadFile(t, filepath.Join(outDir, "skill", "core_foundation.md"))
		for _, want := range []string{
			"core.foundation",
			"Core Foundation",
			"umbrella",
			"active",
			"The umbrella foundation skill exercising all six relation types.",
			"Body content for foundation.",
		} {
			if !strings.Contains(page, want) {
				t.Errorf("core_foundation.md missing real field content %q", want)
			}
		}
		for _, forbidden := range []string{"TODO", "PLACEHOLDER", "lorem ipsum"} {
			if strings.Contains(page, forbidden) {
				t.Errorf("core_foundation.md contains placeholder marker %q", forbidden)
			}
		}
	})

	t.Run("CanonicalRelationOrder_AllSixTypesRoundTrip", func(t *testing.T) {
		page := mustReadFile(t, filepath.Join(outDir, "skill", "core_foundation.md"))
		idxRequires := strings.Index(page, "### Requires")
		idxExtends := strings.Index(page, "### Extends")
		idxComposes := strings.Index(page, "### Composes")
		idxRecommends := strings.Index(page, "### Recommends")
		if idxRequires < 0 || idxExtends < 0 || idxComposes < 0 || idxRecommends < 0 {
			t.Fatalf("core_foundation.md missing one or more expected relation-type subsections:\n%s", page)
		}
		if !(idxRequires < idxExtends && idxExtends < idxComposes && idxComposes < idxRecommends) {
			t.Fatalf("core_foundation.md relation-type subsections are NOT in canonical order "+
				"(requires=%d extends=%d composes=%d recommends=%d)", idxRequires, idxExtends, idxComposes, idxRecommends)
		}
		// Zero-entry relation types (related_to, alternative_to) must be
		// OMITTED for this skill, never rendered as an empty heading.
		if strings.Contains(page, "### Related To") || strings.Contains(page, "### Alternative To") {
			t.Errorf("core_foundation.md rendered an empty relation-type subsection it has zero edges for")
		}
		if !strings.Contains(page, "core.helper") || !strings.Contains(page, "core.util") ||
			!strings.Contains(page, "core.addon") || !strings.Contains(page, "core.alt") {
			t.Errorf("core_foundation.md dependency links missing one or more real targets")
		}
		if !strings.Contains(page, "optional") {
			t.Errorf("core_foundation.md composes->core.addon edge should render its Optional=true attribute")
		}
		if !strings.Contains(page, "sort_order=1") {
			t.Errorf("core_foundation.md composes->core.addon edge should render its SortOrder=1 attribute")
		}
	})

	t.Run("Dependents_ReverseEdgeView", func(t *testing.T) {
		addonPage := mustReadFile(t, filepath.Join(outDir, "skill", "core_addon.md"))
		if !strings.Contains(addonPage, "## Dependents") {
			t.Fatalf("core_addon.md should have a Dependents section (2 real dependents)")
		}
		if !strings.Contains(addonPage, "core.foundation") || !strings.Contains(addonPage, "core.helper") {
			t.Errorf("core_addon.md Dependents section missing an expected dependent")
		}

		foundationPage := mustReadFile(t, filepath.Join(outDir, "skill", "core_foundation.md"))
		if strings.Contains(foundationPage, "## Dependents") {
			t.Errorf("core_foundation.md has zero dependents and must OMIT the Dependents heading")
		}
	})

	t.Run("Resources_SortedByURL", func(t *testing.T) {
		page := mustReadFile(t, filepath.Join(outDir, "skill", "core_foundation.md"))
		idxA := strings.Index(page, "https://example.com/a-doc")
		idxB := strings.Index(page, "https://example.com/b-doc")
		if idxA < 0 || idxB < 0 {
			t.Fatalf("core_foundation.md missing one or both resource URLs")
		}
		if idxA >= idxB {
			t.Errorf("resources not sorted by URL ascending: a-doc at %d, b-doc at %d", idxA, idxB)
		}
	})

	t.Run("README_And_Index_Counts", func(t *testing.T) {
		readme := mustReadFile(t, filepath.Join(outDir, "README.md"))
		if !strings.Contains(readme, "Total skills: 5") {
			t.Errorf("README.md does not report the correct total skill count:\n%s", readme)
		}
		if !strings.Contains(readme, fp1) {
			t.Errorf("README.md does not surface the roster fingerprint")
		}

		index := mustReadFile(t, filepath.Join(outDir, "INDEX.md"))
		for _, name := range []string{"core.foundation", "core.util", "core.helper", "core.addon", "core.alt"} {
			if !strings.Contains(index, name) {
				t.Errorf("INDEX.md missing row for %s", name)
			}
		}
	})

	t.Run("Determinism_ByteStableAcrossRepeatedRuns", func(t *testing.T) {
		before := snapshotTree(t, outDir)

		regenerated2, fp2, err := Generate(ctx, store, outDir, cfg)
		if err != nil {
			t.Fatalf("Generate (2nd call, no Force): %v", err)
		}
		if regenerated2 {
			t.Errorf("Generate (2nd call, no Force): want regenerated=false (fingerprint short-circuit), got true")
		}
		if fp2 != fp1 {
			t.Errorf("Generate (2nd call): fingerprint changed with no DB mutation: %s != %s", fp2, fp1)
		}
		after2 := snapshotTree(t, outDir)
		assertSameTree(t, before, after2, "2nd call (short-circuit)")

		forceCfg := cfg
		forceCfg.Force = true
		regenerated3, fp3, err := Generate(ctx, store, outDir, forceCfg)
		if err != nil {
			t.Fatalf("Generate (3rd call, Force=true): %v", err)
		}
		if !regenerated3 {
			t.Errorf("Generate (3rd call, Force=true): want regenerated=true, got false")
		}
		if fp3 != fp1 {
			t.Errorf("Generate (3rd call): fingerprint changed with no DB mutation: %s != %s", fp3, fp1)
		}
		after3 := snapshotTree(t, outDir)
		assertSameTree(t, before, after3, "3rd call (forced full rewrite)")
	})

	t.Run("Verify_ReportsInSync", func(t *testing.T) {
		// recordedFingerprint is now the composite sidecar IDENTITY (F6
		// review finding, round 2, 2026-07-16 -- fingerprint.go's
		// computeSidecarIdentity), NOT the pure roster fingerprint, so it is
		// deliberately NOT compared against fp1 here; only currentFingerprint
		// (the pure roster fingerprint Verify also returns) is.
		inSync, current, _, err := Verify(ctx, store, outDir, cfg)
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if !inSync {
			t.Errorf("Verify: want inSync=true, got false (current=%s)", current)
		}
		if current != fp1 {
			t.Errorf("Verify: want currentFingerprint == %s, got %s", fp1, current)
		}
	})
}

// ---------------------------------------------------------------------------
// T2: fingerprint drift on add/modify/remove, stable otherwise
// ---------------------------------------------------------------------------

func TestSkillsCatalog_FingerprintDrift_AddModifyRemove_StableOtherwise(t *testing.T) {
	admin, ok := catalogSkipIfNoTestDB(t)
	if !ok {
		return
	}
	pool, cleanup := catalogCreateThrowawayDB(t, admin)
	defer cleanup()

	ctx := context.Background()
	store := skill.NewStore(pool)
	outDir := t.TempDir()
	cfg := DefaultConfig()

	// Before ANY Generate call, Verify must honestly report out-of-sync
	// (no sidecar yet) rather than error or false-positive PASS.
	inSync, _, recorded, err := Verify(ctx, store, outDir, cfg)
	if err != nil {
		t.Fatalf("Verify (before any Generate): %v", err)
	}
	if inSync {
		t.Fatalf("Verify (before any Generate): want inSync=false, got true")
	}
	if recorded != "" {
		t.Fatalf("Verify (before any Generate): want recordedFingerprint=\"\", got %q", recorded)
	}

	seedMD, err := jsonMarshalMetadata(models.SkillMetadata{Domain: "d1"})
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	a := &models.Skill{Name: "drift.a", Version: "1.0.0", Title: "A", Description: "desc-a", Content: "content-a", Metadata: seedMD, Status: models.SkillStatusActive, Kind: models.SkillKindAtomic}
	if err := store.Create(ctx, a); err != nil {
		t.Fatalf("create drift.a: %v", err)
	}
	b := &models.Skill{Name: "drift.b", Version: "1.0.0", Title: "B", Description: "desc-b", Content: "content-b", Metadata: seedMD, Status: models.SkillStatusActive, Kind: models.SkillKindAtomic}
	if err := store.Create(ctx, b); err != nil {
		t.Fatalf("create drift.b: %v", err)
	}

	_, fp1, err := Generate(ctx, store, outDir, cfg)
	if err != nil {
		t.Fatalf("Generate (initial 2-skill state): %v", err)
	}

	// Stable when nothing changes.
	regeneratedNoChange, fpNoChange, err := Generate(ctx, store, outDir, cfg)
	if err != nil {
		t.Fatalf("Generate (no-change re-run): %v", err)
	}
	if regeneratedNoChange {
		t.Errorf("Generate (no-change re-run): want regenerated=false, got true")
	}
	if fpNoChange != fp1 {
		t.Errorf("fingerprint drifted with no DB mutation: %s != %s", fpNoChange, fp1)
	}

	// ADD: fingerprint must change.
	c := &models.Skill{Name: "drift.c", Version: "1.0.0", Title: "C", Description: "desc-c", Content: "content-c", Metadata: seedMD, Status: models.SkillStatusActive, Kind: models.SkillKindAtomic}
	if err := store.Create(ctx, c); err != nil {
		t.Fatalf("create drift.c: %v", err)
	}
	inSyncAfterAdd, currentAfterAdd, _, err := Verify(ctx, store, outDir, cfg)
	if err != nil {
		t.Fatalf("Verify (after add): %v", err)
	}
	if inSyncAfterAdd {
		t.Fatalf("Verify (after add): want inSync=false, got true")
	}
	if currentAfterAdd == fp1 {
		t.Fatalf("fingerprint did NOT change after adding a skill")
	}
	regeneratedAdd, fp2, err := Generate(ctx, store, outDir, cfg)
	if err != nil {
		t.Fatalf("Generate (after add): %v", err)
	}
	if !regeneratedAdd || fp2 != currentAfterAdd {
		t.Fatalf("Generate (after add): want regenerated=true and fp==%s, got regenerated=%v fp=%s", currentAfterAdd, regeneratedAdd, fp2)
	}

	// MODIFY: fingerprint must change again.
	if _, err := pool.Exec(ctx, `UPDATE skills SET description = $1 WHERE name = $2`, "desc-c-modified", "drift.c"); err != nil {
		t.Fatalf("modify drift.c description: %v", err)
	}
	regeneratedModify, fp3, err := Generate(ctx, store, outDir, cfg)
	if err != nil {
		t.Fatalf("Generate (after modify): %v", err)
	}
	if !regeneratedModify {
		t.Fatalf("Generate (after modify): want regenerated=true, got false")
	}
	if fp3 == fp2 {
		t.Fatalf("fingerprint did NOT change after modifying a skill's description")
	}

	// REMOVE: fingerprint must change again (back toward, but per content
	// hash inputs not necessarily identical to, the pre-add state).
	// skill_registry.skill_id REFERENCES skills(id) with NO cascade
	// (migrations/001_initial.up.sql:69, unlike skill_dependencies/
	// resources/evidences which DO cascade) -- its row must be removed
	// FIRST or the skills DELETE violates that FK.
	if _, err := pool.Exec(ctx, `DELETE FROM skill_registry WHERE skill_id = (SELECT id FROM skills WHERE name = $1)`, "drift.c"); err != nil {
		t.Fatalf("remove drift.c registry entry: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM skills WHERE name = $1`, "drift.c"); err != nil {
		t.Fatalf("remove drift.c: %v", err)
	}
	regeneratedRemove, fp4, err := Generate(ctx, store, outDir, cfg)
	if err != nil {
		t.Fatalf("Generate (after remove): %v", err)
	}
	if !regeneratedRemove {
		t.Fatalf("Generate (after remove): want regenerated=true, got false")
	}
	if fp4 != fp1 {
		t.Fatalf("fingerprint after removing drift.c should equal the original 2-skill fingerprint %s, got %s", fp1, fp4)
	}
	if _, err := os.Stat(filepath.Join(outDir, "skill", "drift_c.md")); !os.IsNotExist(err) {
		t.Errorf("skill/drift_c.md should have been removed from the tree after drift.c was deleted, stat err=%v", err)
	}
}

// ---------------------------------------------------------------------------
// T2b: F1 review-finding regression guard -- a Title-ONLY mutation (every
// other column untouched) MUST be detected as drift and MUST re-render the
// affected page with the new Title. Captured evidence for the review
// finding: on the pre-fix generator this test's RED_MODE=1-equivalent run
// (i.e. code BEFORE the fingerprint.go Title fix landed) reproduced exactly
// what the Fable reviewer reported -- `UPDATE skills SET title=...` left
// Verify.inSync=true, Generate.regenerated=false, and the rendered page
// showing the stale title. With the fix this test asserts the polarity-
// flipped GREEN outcome (§11.4.115): drift IS detected and the page IS
// regenerated with the new title.
// ---------------------------------------------------------------------------

func TestSkillsCatalog_FingerprintDrift_TitleOnlyChange_Detected(t *testing.T) {
	admin, ok := catalogSkipIfNoTestDB(t)
	if !ok {
		return
	}
	pool, cleanup := catalogCreateThrowawayDB(t, admin)
	defer cleanup()

	ctx := context.Background()
	store := skill.NewStore(pool)
	outDir := t.TempDir()
	cfg := DefaultConfig()

	seedMD, err := jsonMarshalMetadata(models.SkillMetadata{Domain: "titledrift"})
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	sk := &models.Skill{
		Name: "titledrift.a", Version: "1.0.0", Title: "Original Title",
		Description: "desc-unchanged", Content: "content-unchanged", Metadata: seedMD,
		Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
	}
	if err := store.Create(ctx, sk); err != nil {
		t.Fatalf("create titledrift.a: %v", err)
	}

	_, fp1, err := Generate(ctx, store, outDir, cfg)
	if err != nil {
		t.Fatalf("Generate (initial): %v", err)
	}
	initialPage := mustReadFile(t, filepath.Join(outDir, "skill", "titledrift_a.md"))
	if !strings.Contains(initialPage, "Original Title") {
		t.Fatalf("initial generated page missing the original title:\n%s", initialPage)
	}

	// The mutation touches ONLY the title column -- description/content/
	// version/kind/status/metadata are all left exactly as created.
	if _, err := pool.Exec(ctx, `UPDATE skills SET title = $1 WHERE name = $2`, "CHANGED TITLE", "titledrift.a"); err != nil {
		t.Fatalf("mutate titledrift.a title: %v", err)
	}

	inSync, currentFP, recordedFP, err := Verify(ctx, store, outDir, cfg)
	if err != nil {
		t.Fatalf("Verify (after title-only change): %v", err)
	}
	if inSync {
		t.Fatalf("Verify (after title-only change): want inSync=false -- Title MUST participate in the "+
			"roster fingerprint, got inSync=true (current=%s recorded=%s); this is the exact false-negative "+
			"drift the F1 review finding proved (a Title-only DB edit left Verify reporting in-sync)",
			currentFP, recordedFP)
	}
	if currentFP == fp1 {
		t.Fatalf("fingerprint did NOT change after a Title-only mutation: %s == %s", currentFP, fp1)
	}

	regenerated, fp2, err := Generate(ctx, store, outDir, cfg)
	if err != nil {
		t.Fatalf("Generate (after title-only change): %v", err)
	}
	if !regenerated {
		t.Fatalf("Generate (after title-only change): want regenerated=true, got false")
	}
	if fp2 != currentFP {
		t.Fatalf("Generate's fingerprint (%s) does not match Verify's precomputed current fingerprint (%s)", fp2, currentFP)
	}

	regeneratedPage := mustReadFile(t, filepath.Join(outDir, "skill", "titledrift_a.md"))
	if !strings.Contains(regeneratedPage, "CHANGED TITLE") {
		t.Errorf("regenerated page does not reflect the new title:\n%s", regeneratedPage)
	}
	if strings.Contains(regeneratedPage, "Original Title") {
		t.Errorf("regenerated page still contains the stale original title -- the catalog went silently stale")
	}
}

// ---------------------------------------------------------------------------
// T3/T4: golden-bad defensive checks (DESIGN.md §6 item 2)
// ---------------------------------------------------------------------------

func TestSkillsCatalog_GoldenBad_EmptySkillName(t *testing.T) {
	admin, ok := catalogSkipIfNoTestDB(t)
	if !ok {
		return
	}
	pool, cleanup := catalogCreateThrowawayDB(t, admin)
	defer cleanup()

	ctx := context.Background()
	store := skill.NewStore(pool)

	md, err := jsonMarshalMetadata(models.SkillMetadata{})
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	// Empty Name is not blocked by any Go-level check in Store.Create (only
	// the DB's NOT NULL constraint applies, and "" satisfies NOT NULL) --
	// CODEBASE_MAP.md §2's "NOT NULL UNIQUE" note describes the schema
	// constraint, not an application-level non-empty check, so this really
	// does reach a live row (mirroring the G33 empty-dependency-name lesson
	// this generator's OWN defensive check must not repeat).
	bogus := &models.Skill{Name: "", Version: "1.0.0", Title: "Bogus", Description: "d", Content: "c", Metadata: md, Status: models.SkillStatusDraft, Kind: models.SkillKindAtomic}
	if err := store.Create(ctx, bogus); err != nil {
		t.Fatalf("create empty-name skill fixture: %v", err)
	}

	outDir := t.TempDir()
	_, _, err = Generate(ctx, store, outDir, DefaultConfig())
	if err == nil {
		t.Fatalf("Generate: want an error for an empty-name skill row, got nil")
	}
	if !isDefensiveCheckError(err) {
		t.Errorf("Generate: want an ErrDefensiveCheck-wrapped error, got: %v", err)
	}
}

func TestSkillsCatalog_GoldenBad_DanglingDependencyEdge(t *testing.T) {
	admin, ok := catalogSkipIfNoTestDB(t)
	if !ok {
		return
	}
	pool, cleanup := catalogCreateThrowawayDB(t, admin)
	defer cleanup()

	ctx := context.Background()
	store := skill.NewStore(pool)

	md, err := jsonMarshalMetadata(models.SkillMetadata{})
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	src := &models.Skill{Name: "dangling.src", Version: "1.0.0", Title: "Src", Description: "d", Content: "c", Metadata: md, Status: models.SkillStatusActive, Kind: models.SkillKindAtomic}
	if err := store.Create(ctx, src); err != nil {
		t.Fatalf("create dangling.src: %v", err)
	}
	target := &models.Skill{Name: "dangling.target", Version: "1.0.0", Title: "Target", Description: "d", Content: "c", Metadata: md, Status: models.SkillStatusActive, Kind: models.SkillKindAtomic}
	if err := store.Create(ctx, target); err != nil {
		t.Fatalf("create dangling.target: %v", err)
	}
	if err := store.AddDependency(ctx, src.ID, target.ID, models.DepTypeRequires); err != nil {
		t.Fatalf("add dependency: %v", err)
	}

	// Simulate the "stale in-memory read racing a concurrent delete"
	// scenario DESIGN.md §6 item 2(a) describes: the schema's
	// `ON DELETE CASCADE` FK (migrations/001_initial.up.sql:26-27) makes a
	// dangling edge unreachable through any normal Store-level delete.
	// Postgres implements the CASCADE action as a trigger on the REFERENCED
	// (parent) table -- `skills`, not the child `skill_dependencies` -- so
	// producing a genuinely orphaned edge for this test requires disabling
	// triggers on `skills` itself before deleting the target row (disabling
	// triggers on skill_dependencies, the child side, would only suppress
	// its own insert/update RI check and would NOT prevent the parent-side
	// cascade from still firing on delete).
	if _, err := pool.Exec(ctx, `ALTER TABLE skills DISABLE TRIGGER ALL`); err != nil {
		t.Fatalf("disable FK cascade trigger on skills: %v", err)
	}
	// skill_registry.skill_id REFERENCES skills(id) with NO cascade
	// (migrations/001_initial.up.sql:69) -- unaffected by the trigger
	// disable above (that trigger lives on `skills`, not `skill_registry`),
	// so its row must still be removed first or the skills DELETE below
	// violates that separate FK.
	if _, err := pool.Exec(ctx, `DELETE FROM skill_registry WHERE skill_id = $1`, target.ID); err != nil {
		t.Fatalf("remove target registry entry: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM skills WHERE id = $1`, target.ID); err != nil {
		t.Fatalf("delete target skill (leaving orphan edge): %v", err)
	}

	outDir := t.TempDir()
	_, _, err = Generate(ctx, store, outDir, DefaultConfig())
	if err == nil {
		t.Fatalf("Generate: want an error for a dangling dependency edge, got nil")
	}
	if !isDefensiveCheckError(err) {
		t.Errorf("Generate: want an ErrDefensiveCheck-wrapped error, got: %v", err)
	}
}

// TestSkillsCatalog_GoldenBad_NameSlugCollision is the F2 review-finding
// regression guard: skills.name is TEXT UNIQUE with NO charset constraint
// beyond that (model.go's slugify docstring), so two DISTINCT, both
// schema-legal names can slugify (model.go's slugify) to the IDENTICAL
// filename -- here "foo.bar" and "foo_bar" both become "foo_bar" ('.' and
// '_' both map to themselves-or-underscore). Without the load.go
// checkNoSlugCollisions guard, writeSkillPages (generate.go) would silently
// let the second-processed record's page overwrite the first's.
func TestSkillsCatalog_GoldenBad_NameSlugCollision(t *testing.T) {
	admin, ok := catalogSkipIfNoTestDB(t)
	if !ok {
		return
	}
	pool, cleanup := catalogCreateThrowawayDB(t, admin)
	defer cleanup()

	ctx := context.Background()
	store := skill.NewStore(pool)

	md, err := jsonMarshalMetadata(models.SkillMetadata{})
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	a := &models.Skill{Name: "foo.bar", Version: "1.0.0", Title: "A", Description: "d", Content: "c", Metadata: md, Status: models.SkillStatusActive, Kind: models.SkillKindAtomic}
	if err := store.Create(ctx, a); err != nil {
		t.Fatalf("create foo.bar: %v", err)
	}
	b := &models.Skill{Name: "foo_bar", Version: "1.0.0", Title: "B", Description: "d", Content: "c", Metadata: md, Status: models.SkillStatusActive, Kind: models.SkillKindAtomic}
	if err := store.Create(ctx, b); err != nil {
		t.Fatalf("create foo_bar: %v", err)
	}

	outDir := t.TempDir()
	_, _, err = Generate(ctx, store, outDir, DefaultConfig())
	if err == nil {
		t.Fatalf("Generate: want an error for a NameSlug collision between %q and %q, got nil", a.Name, b.Name)
	}
	if !isDefensiveCheckError(err) {
		t.Errorf("Generate: want an ErrDefensiveCheck-wrapped error, got: %v", err)
	}
}

// TestSkillsCatalog_GoldenBad_DomainSlugCollision is the F2 review-finding
// regression guard's by-domain counterpart: Metadata.Domain is free-text
// JSONB with no charset constraint, so two skills in domains "foo.bar" and
// "foo_bar" would, absent the load.go checkNoSlugCollisions guard, have
// writeByDomain (generate.go) silently let one by-domain grouping page
// overwrite the other's.
func TestSkillsCatalog_GoldenBad_DomainSlugCollision(t *testing.T) {
	admin, ok := catalogSkipIfNoTestDB(t)
	if !ok {
		return
	}
	pool, cleanup := catalogCreateThrowawayDB(t, admin)
	defer cleanup()

	ctx := context.Background()
	store := skill.NewStore(pool)

	mdA, err := jsonMarshalMetadata(models.SkillMetadata{Domain: "foo.bar"})
	if err != nil {
		t.Fatalf("marshal metadata a: %v", err)
	}
	mdB, err := jsonMarshalMetadata(models.SkillMetadata{Domain: "foo_bar"})
	if err != nil {
		t.Fatalf("marshal metadata b: %v", err)
	}
	a := &models.Skill{Name: "domaincollide.a", Version: "1.0.0", Title: "A", Description: "d", Content: "c", Metadata: mdA, Status: models.SkillStatusActive, Kind: models.SkillKindAtomic}
	if err := store.Create(ctx, a); err != nil {
		t.Fatalf("create domaincollide.a: %v", err)
	}
	b := &models.Skill{Name: "domaincollide.b", Version: "1.0.0", Title: "B", Description: "d", Content: "c", Metadata: mdB, Status: models.SkillStatusActive, Kind: models.SkillKindAtomic}
	if err := store.Create(ctx, b); err != nil {
		t.Fatalf("create domaincollide.b: %v", err)
	}

	outDir := t.TempDir()
	_, _, err = Generate(ctx, store, outDir, DefaultConfig())
	if err == nil {
		t.Fatalf("Generate: want an error for a DomainSlug collision between %q and %q, got nil", "foo.bar", "foo_bar")
	}
	if !isDefensiveCheckError(err) {
		t.Errorf("Generate: want an ErrDefensiveCheck-wrapped error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// T5: real-DB end-to-end via ImportFromTOML + link-integrity (DESIGN.md §6 item 5)
// ---------------------------------------------------------------------------

func TestSkillsCatalog_RealSeedImport_NoDanglingInternalLinks(t *testing.T) {
	admin, ok := catalogSkipIfNoTestDB(t)
	if !ok {
		return
	}
	pool, cleanup := catalogCreateThrowawayDB(t, admin)
	defer cleanup()

	ctx := context.Background()
	store := skill.NewStore(pool)

	// Import order matters: java.language has no hard-closure requires;
	// kotlin.language requires java.language; android.overview requires
	// both -- ImportFromTOML hard-errors on a missing hard-closure target
	// (CODEBASE_MAP.md §3), so java -> kotlin -> android is the only order
	// that imports cleanly.
	for _, seedFile := range []string{"java.toml", "kotlin.toml", "android.toml"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "seed", "skills", seedFile))
		if err != nil {
			t.Fatalf("read seed fixture %s: %v", seedFile, err)
		}
		if _, err := store.ImportFromTOML(ctx, data); err != nil {
			t.Fatalf("ImportFromTOML(%s): %v", seedFile, err)
		}
	}

	outDir := t.TempDir()
	if _, _, err := Generate(ctx, store, outDir, DefaultConfig()); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	androidPage := mustReadFile(t, filepath.Join(outDir, "skill", "android_overview.md"))
	if !strings.Contains(androidPage, "java.language") || !strings.Contains(androidPage, "kotlin.language") {
		t.Fatalf("android_overview.md missing its real java.language/kotlin.language Requires links:\n%s", androidPage)
	}
	if _, err := os.Stat(filepath.Join(outDir, "skill", "java_language.md")); err != nil {
		t.Errorf("linked target skill/java_language.md does not exist on disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "skill", "kotlin_language.md")); err != nil {
		t.Errorf("linked target skill/kotlin_language.md does not exist on disk: %v", err)
	}

	kotlinPage := mustReadFile(t, filepath.Join(outDir, "skill", "kotlin_language.md"))
	if !strings.Contains(kotlinPage, "java.language") {
		t.Errorf("kotlin_language.md missing its real java.language Requires link")
	}
	if !strings.Contains(kotlinPage, "android.overview") {
		t.Errorf("kotlin_language.md missing android.overview in its Dependents section")
	}
}

// ---------------------------------------------------------------------------
// Small local test helpers (test-file scoped, not production code)
// ---------------------------------------------------------------------------

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// snapshotTree walks dir and returns relPath -> file bytes for every regular
// file, so two generations can be compared byte-for-byte.
func snapshotTree(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[rel] = data
		return nil
	})
	if err != nil {
		t.Fatalf("snapshotTree(%s): %v", dir, err)
	}
	return out
}

func assertSameTree(t *testing.T, before, after map[string][]byte, label string) {
	t.Helper()
	names := make([]string, 0, len(before))
	for k := range before {
		names = append(names, k)
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		b, okB := before[name]
		a, okA := after[name]
		if okB != okA {
			t.Errorf("%s: file %s present=%v before, present=%v after", label, name, okB, okA)
			continue
		}
		if string(a) != string(b) {
			t.Errorf("%s: file %s changed bytes across regeneration", label, name)
		}
	}
}

// isDefensiveCheckError reports whether err (or any error it wraps) is
// ErrDefensiveCheck (F5 review finding, 2026-07-16: use errors.Is, matching
// the sentinel's own doc comment in load.go, "Callers should compare with
// errors.Is" -- rather than a hand-rolled string-contains + manual Unwrap
// loop, which is both more code AND a weaker check: it would false-POSITIVE
// on any unrelated error whose message happens to CONTAIN
// ErrDefensiveCheck's text, and it duplicates logic errors.Is already gets
// right, including honoring a custom Is method should one ever be added).
func isDefensiveCheckError(err error) bool {
	return errors.Is(err, ErrDefensiveCheck)
}

func jsonMarshalMetadata(md models.SkillMetadata) ([]byte, error) {
	return json.Marshal(md)
}

// ---------------------------------------------------------------------------
// Round-2 review-finding regression guards (2026-07-16): F1 (BLOCKING),
// F2 (HIGH), F3 (MEDIUM), F4 (MEDIUM), F5 (LOW), F6 (LOW), F7 (NIT).
// ---------------------------------------------------------------------------

// TestSkillsCatalog_TimestampChurn_TouchOnlyUpdatedAt_NoDrift is the F1
// review-finding regression guard, round 2 (2026-07-16): CreatedAt/UpdatedAt
// were rendered verbatim in the per-skill Footer (render.go's
// renderSkillDetail) but excluded from the fingerprint, so an idempotent
// re-import bumping ONLY updated_at (internal/skill/store.go's Create
// upsert path + migrations/001_initial.up.sql's BEFORE-UPDATE trigger) left
// Verify reporting inSync=true while the rendered Footer showed a stale
// timestamp -- the F1 staleness bug was still live because timestamps were
// excluded from the FINGERPRINT but not from the RENDERED OUTPUT. This
// round's fix removes them from BOTH (render.go's Footer no longer renders
// Created/Updated at all). This test proves: (a) the initial page never
// renders a Created/Updated line, (b) Verify reports inSync=true after a
// touch-only updated_at bump (now CORRECTLY -- nothing catalog-visible
// changed), and (c) a FORCED regeneration after that touch produces
// byte-IDENTICAL output (no timestamp churn leaks into the rendered page).
func TestSkillsCatalog_TimestampChurn_TouchOnlyUpdatedAt_NoDrift(t *testing.T) {
	admin, ok := catalogSkipIfNoTestDB(t)
	if !ok {
		return
	}
	pool, cleanup := catalogCreateThrowawayDB(t, admin)
	defer cleanup()

	ctx := context.Background()
	store := skill.NewStore(pool)
	outDir := t.TempDir()
	cfg := DefaultConfig()

	seedMD, err := jsonMarshalMetadata(models.SkillMetadata{Domain: "churn"})
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	sk := &models.Skill{
		Name: "churn.a", Version: "1.0.0", Title: "Churn A",
		Description: "desc-unchanged", Content: "content-unchanged", Metadata: seedMD,
		Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
	}
	if err := store.Create(ctx, sk); err != nil {
		t.Fatalf("create churn.a: %v", err)
	}

	_, fp1, err := Generate(ctx, store, outDir, cfg)
	if err != nil {
		t.Fatalf("Generate (initial): %v", err)
	}
	pagePath := filepath.Join(outDir, "skill", "churn_a.md")
	before := mustReadFile(t, pagePath)
	if strings.Contains(before, "**Created:**") || strings.Contains(before, "**Updated:**") {
		t.Fatalf("initial generated page must NOT render per-skill Created/Updated timestamps "+
			"(F1 remediation, round 2 -- churn metadata must never appear in the deterministic catalog artifact):\n%s", before)
	}

	// A raw UPDATE ... SET updated_at = NOW() simulates an idempotent
	// re-import that touches ONLY the churn column -- every other column
	// (description/content/title/version/status/metadata) stays exactly as
	// created.
	if _, err := pool.Exec(ctx, `UPDATE skills SET updated_at = NOW() WHERE name = $1`, "churn.a"); err != nil {
		t.Fatalf("touch-only update updated_at: %v", err)
	}

	inSync, currentFP, _, err := Verify(ctx, store, outDir, cfg)
	if err != nil {
		t.Fatalf("Verify (after touch-only update): %v", err)
	}
	if !inSync {
		t.Fatalf("Verify (after touch-only updated_at bump): want inSync=true -- updated_at is a "+
			"non-semantic churn field excluded from both the fingerprint AND the rendered output, "+
			"got inSync=false (current=%s recorded-fp1=%s)", currentFP, fp1)
	}
	if currentFP != fp1 {
		t.Fatalf("fingerprint drifted after a touch-only updated_at bump: %s != %s", currentFP, fp1)
	}

	forceCfg := cfg
	forceCfg.Force = true
	regenerated, fp2, err := Generate(ctx, store, outDir, forceCfg)
	if err != nil {
		t.Fatalf("Generate (forced regen after touch-only update): %v", err)
	}
	if !regenerated {
		t.Fatalf("Generate (Force=true): want regenerated=true, got false")
	}
	if fp2 != fp1 {
		t.Fatalf("fingerprint changed on forced regen with no catalog-visible mutation: %s != %s", fp2, fp1)
	}
	after := mustReadFile(t, pagePath)
	if after != before {
		t.Fatalf("regenerated page bytes changed after a touch-only updated_at bump (timestamp churn "+
			"leaked into rendered output):\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestSkillsCatalog_FingerprintDrift_IDChange_SameContent_Detected is the
// F2 review-finding regression guard, round 2 (2026-07-16): sk.ID is
// rendered into the excerpt-mode (cfg.EmbedFullContent=false) export URL
// (render.go's renderSkillDetail) but was NOT part of the fingerprint tuple
// -- a skill dropped and re-created under the IDENTICAL Name/Title/Version/
// Description/Content/Metadata but a brand-new UUID left the fingerprint
// UNCHANGED, so Verify falsely reported inSync=true while the rendered
// export URL pointed at a UUID that no longer resolves. This test proves
// Verify now honestly reports drift for that exact scenario.
func TestSkillsCatalog_FingerprintDrift_IDChange_SameContent_Detected(t *testing.T) {
	admin, ok := catalogSkipIfNoTestDB(t)
	if !ok {
		return
	}
	pool, cleanup := catalogCreateThrowawayDB(t, admin)
	defer cleanup()

	ctx := context.Background()
	store := skill.NewStore(pool)
	outDir := t.TempDir()
	cfg := DefaultConfig()

	seedMD, err := jsonMarshalMetadata(models.SkillMetadata{Domain: "idchurn"})
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	mkSkill := func() *models.Skill {
		return &models.Skill{
			Name: "idchurn.a", Version: "1.0.0", Title: "ID Churn A",
			Description: "desc-stable", Content: "content-stable", Metadata: seedMD,
			Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
		}
	}

	first := mkSkill()
	if err := store.Create(ctx, first); err != nil {
		t.Fatalf("create idchurn.a (first): %v", err)
	}
	firstID := first.ID

	_, fp1, err := Generate(ctx, store, outDir, cfg)
	if err != nil {
		t.Fatalf("Generate (initial): %v", err)
	}

	// Drop the skill entirely (registry entry first -- skill_registry has
	// NO cascade, migrations/001_initial.up.sql:69) and recreate it under
	// the SAME Name/Title/Version/Description/Content/Metadata but a
	// BRAND-NEW random UUID (Store.Create assigns one when skill.ID ==
	// uuid.Nil and the row genuinely does not exist -- unlike an ON
	// CONFLICT(name) DO UPDATE path, which would keep the OLD id).
	if _, err := pool.Exec(ctx, `DELETE FROM skill_registry WHERE skill_id = $1`, firstID); err != nil {
		t.Fatalf("remove idchurn.a registry entry: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM skills WHERE id = $1`, firstID); err != nil {
		t.Fatalf("delete idchurn.a: %v", err)
	}

	second := mkSkill()
	if err := store.Create(ctx, second); err != nil {
		t.Fatalf("re-create idchurn.a (second, new id): %v", err)
	}
	if second.ID == firstID {
		t.Fatalf("test fixture invalid: recreated skill got the SAME id %s as the deleted one -- cannot "+
			"exercise the id-change scenario", firstID)
	}

	inSync, currentFP, recordedFP, err := Verify(ctx, store, outDir, cfg)
	if err != nil {
		t.Fatalf("Verify (after id-only churn): %v", err)
	}
	if inSync {
		t.Fatalf("Verify (after drop+recreate with identical content but a NEW id): want inSync=false -- "+
			"sk.ID is rendered into the excerpt-mode export URL and MUST participate in the roster "+
			"fingerprint, got inSync=true (current=%s recorded=%s)", currentFP, recordedFP)
	}
	if currentFP == fp1 {
		t.Fatalf("fingerprint did NOT change after a same-content, new-id drop+recreate: %s == %s", currentFP, fp1)
	}
}

// ---------------------------------------------------------------------------
// F3: Markdown-injection escaping on NON-TABLE surfaces.
// ---------------------------------------------------------------------------

// TestEscapeMDInline is a fast, DB-independent unit test of escapeMDInline
// (render.go) -- the F3 review-finding remediation, round 2 (2026-07-16).
func TestEscapeMDInline(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain text unchanged", "hello world", "hello world"},
		{"embedded newline collapsed to space", "a\nb", "a b"},
		{"embedded crlf collapsed to space", "a\r\nb", "a b"},
		{"backtick escaped", "a`b", "a" + `\` + "`b"},
		{"asterisk escaped", "a*b", `a\*b`},
		{"underscore escaped", "a_b", `a\_b`},
		{"tilde escaped", "a~b", `a\~b`},
		{"leading hash escaped", "#heading", `\#heading`},
		{"backslash escaped first", `a\b`, `a\\b`},
		// Finding 2 review finding, round 5, 2026-07-16: escapeMDInline now
		// ALSO escapes '['/']' (previously this case asserted brackets were
		// "left untouched (not link text)" -- that assumption was FALSE, see
		// escapeMDInline's own doc comment: GFM/CommonMark recognize
		// "[text](url)" link syntax in EVERY inline context, not only where
		// escapeMDLinkText used to be called).
		{"brackets escaped (Finding 2 fix)", "a[b]c", `a\[b\]c`},
		{"full link-injection payload neutralized", "[Download update](https://evil.example)",
			`\[Download update\](https://evil.example)`},
		// F-C review finding, round 3, 2026-07-16: raw HTML passthrough.
		{"less-than escaped", "a<b", "a&lt;b"},
		{"greater-than escaped", "a>b", "a&gt;b"},
		{"raw HTML tag escaped", "<img src=x onerror=alert(1)>", "&lt;img src=x onerror=alert(1)&gt;"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := escapeMDInline(tc.in); got != tc.want {
				t.Errorf("escapeMDInline(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestEscapeMDCell is a fast, DB-independent unit test of escapeMDCell
// (render.go). escapeMDCell had no dedicated pure unit test before the F-C
// review finding, round 3, 2026-07-16 -- only the DB-backed
// TestSkillsCatalog_TableCellEscaping_PipeBackslashNewline (below)
// exercised it indirectly; this test adds the fast, direct coverage
// (mirroring TestEscapeMDInline's own pattern) and proves the new "<"/">"
// HTML-entity escaping specifically.
func TestEscapeMDCell(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain text unchanged", "hello world", "hello world"},
		{"pipe escaped", "a|b", `a\|b`},
		{"backslash escaped first", `a\b`, `a\\b`},
		{"embedded newline collapsed to space", "a\nb", "a b"},
		{"embedded crlf collapsed to space", "a\r\nb", "a b"},
		// F-C review finding, round 3, 2026-07-16: raw HTML passthrough.
		{"less-than escaped", "a<b", "a&lt;b"},
		{"greater-than escaped", "a>b", "a&gt;b"},
		{"raw HTML tag escaped", "<img src=x onerror=alert(1)>", "&lt;img src=x onerror=alert(1)&gt;"},
		// Finding 2 review finding, round 5, 2026-07-16: a table cell can
		// ALSO host a free-standing "[text](url)" inline link -- escapeMDCell
		// previously left '['/']' unescaped, exactly like the pre-fix
		// escapeMDInline.
		{"brackets escaped (Finding 2 fix)", "a[b]c", `a\[b\]c`},
		{"full link-injection payload neutralized", "[Download update](https://evil.example)",
			`\[Download update\](https://evil.example)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := escapeMDCell(tc.in); got != tc.want {
				t.Errorf("escapeMDCell(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestEscapeMDLinkText is a fast, DB-independent unit test of
// escapeMDLinkText (render.go) -- the F3 review-finding remediation,
// round 2 (2026-07-16).
func TestEscapeMDLinkText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"brackets escaped", "a[b]c", `a\[b\]c`},
		{"backtick still escaped", "a`b", "a" + `\` + "`b"},
		{"newline still collapsed", "a\nb", "a b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := escapeMDLinkText(tc.in); got != tc.want {
				t.Errorf("escapeMDLinkText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSkillsCatalog_MarkdownInjection_SkillDetailSurfaces_Escaped is the F3
// review-finding regression guard, round 2 (2026-07-16), end-to-end via a
// real DB: escapeMDCell (F4, round 1) only ever covered TABLE-CELL
// surfaces. A Title containing "X\n# Injected Heading" must not manufacture
// a bogus extra heading on the skill detail page, and a dependency Name
// containing a backtick + "](" must not corrupt the Dependencies section's
// link markup (both legal input -- model.go's slugify docstring notes
// skills.name has no charset constraint beyond TEXT UNIQUE).
func TestSkillsCatalog_MarkdownInjection_SkillDetailSurfaces_Escaped(t *testing.T) {
	admin, ok := catalogSkipIfNoTestDB(t)
	if !ok {
		return
	}
	pool, cleanup := catalogCreateThrowawayDB(t, admin)
	defer cleanup()

	ctx := context.Background()
	store := skill.NewStore(pool)
	outDir := t.TempDir()

	depMD, err := jsonMarshalMetadata(models.SkillMetadata{Domain: "core"})
	if err != nil {
		t.Fatalf("marshal dep metadata: %v", err)
	}
	// dep's Name embeds a backtick AND a "](" sequence -- the two character
	// pairs that terminate/corrupt Markdown inline-code and link syntax.
	dep := &models.Skill{
		Name: "inject.dep`x](evil)", Version: "1.0.0", Title: "Dep", Description: "d", Content: "c",
		Metadata: depMD, Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
	}
	if err := store.Create(ctx, dep); err != nil {
		t.Fatalf("create dep: %v", err)
	}

	srcMD, err := jsonMarshalMetadata(models.SkillMetadata{Domain: "core", Complexity: "beginner", Tags: []string{"tag"}})
	if err != nil {
		t.Fatalf("marshal src metadata: %v", err)
	}
	src := &models.Skill{
		Name:        "inject.src",
		Version:     "1.0.0",
		Title:       "X\n# Injected Heading",
		Description: "desc", Content: "content",
		Metadata: srcMD, Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
		Dependencies: []models.SkillDependency{
			{DependsOn: dep.ID, RelationType: models.DepTypeRequires},
		},
	}
	if err := store.Create(ctx, src); err != nil {
		t.Fatalf("create src: %v", err)
	}

	if _, _, err := Generate(ctx, store, outDir, DefaultConfig()); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	page := mustReadFile(t, filepath.Join(outDir, "skill", "inject_src.md"))

	for _, ln := range strings.Split(page, "\n") {
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, "#") && strings.Contains(trimmed, "Injected Heading") {
			t.Errorf("Title's embedded newline+'#' manufactured a NEW heading line: %q", ln)
		}
	}
	if !strings.Contains(page, "Injected Heading") {
		t.Errorf("page lost the (now-inline, non-heading) Title text entirely:\n%s", page)
	}

	rawCorrupted := "x" + "]" + "(evil)"
	if strings.Contains(page, rawCorrupted) {
		t.Errorf("dependency Name's embedded \"](\" corrupted the Dependencies link markup -- found a "+
			"literal unescaped %q in:\n%s", rawCorrupted, page)
	}
	escapedBracket := "x" + `\` + "]"
	if !strings.Contains(page, escapedBracket) {
		t.Errorf("dependency Name's ']' was not escaped to %q in the Dependencies link text:\n%s", escapedBracket, page)
	}
	escapedBacktick := "dep" + `\` + "`x"
	if !strings.Contains(page, escapedBacktick) {
		t.Errorf("dependency Name's backtick was not escaped to %q in the Dependencies link text:\n%s", escapedBacktick, page)
	}
}

// TestSkillsCatalog_MarkdownInjection_DomainSurfaces_Escaped is the F3
// review-finding regression guard's README/by-domain counterpart, round 2
// (2026-07-16): a Metadata.Domain value containing "]" (link-markup-
// significant) and an embedded newline (heading-line-significant) must not
// corrupt either README.md's "By Domain" link list or the by-domain page's
// own H1 heading.
func TestSkillsCatalog_MarkdownInjection_DomainSurfaces_Escaped(t *testing.T) {
	admin, ok := catalogSkipIfNoTestDB(t)
	if !ok {
		return
	}
	pool, cleanup := catalogCreateThrowawayDB(t, admin)
	defer cleanup()

	ctx := context.Background()
	store := skill.NewStore(pool)
	outDir := t.TempDir()

	trickyDomain := "Weird]Domain(evil)\nInjected"
	md, err := jsonMarshalMetadata(models.SkillMetadata{Domain: trickyDomain})
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	sk := &models.Skill{
		Name: "domaininject.a", Version: "1.0.0", Title: "A", Description: "d", Content: "c",
		Metadata: md, Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
	}
	if err := store.Create(ctx, sk); err != nil {
		t.Fatalf("create domaininject.a: %v", err)
	}

	if _, _, err := Generate(ctx, store, outDir, DefaultConfig()); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	readme := mustReadFile(t, filepath.Join(outDir, "README.md"))
	if strings.Contains(readme, "Weird]Domain") {
		t.Errorf("README.md's By-Domain link text left the domain name's ']' unescaped, corrupting the "+
			"link markup:\n%s", readme)
	}
	escapedDomainLink := "Weird" + `\` + "]Domain"
	if !strings.Contains(readme, escapedDomainLink) {
		t.Errorf("README.md's By-Domain link text did not escape the domain name's ']' to %q:\n%s", escapedDomainLink, readme)
	}

	domainPage := mustReadFile(t, filepath.Join(outDir, "by-domain", slugify(trickyDomain)+".md"))
	domainLines := strings.Split(domainPage, "\n")
	if len(domainLines) == 0 || !strings.HasPrefix(domainLines[0], "# Domain:") {
		first := ""
		if len(domainLines) > 0 {
			first = domainLines[0]
		}
		t.Fatalf("by-domain page missing its expected H1 heading, got first line %q, full page:\n%s", first, domainPage)
	}
	if !strings.Contains(domainLines[0], "Injected") {
		t.Errorf("by-domain page's H1 was split by the domain name's embedded newline -- \"Injected\" did "+
			"not stay on the SAME H1 line: first line=%q, full page:\n%s", domainLines[0], domainPage)
	}
}

// TestSkillsCatalog_ForgedSectionHeading_SentinelDistinguishesAuthenticFooter
// is the F-B review-finding regression guard, round 3, 2026-07-16:
// render.go emits raw, unescaped Description/Content prose (by design --
// see renderSkillDetail's own "## Description"/"## Content" comments), so a
// skill Description containing its own "## Footer" heading + a fake
// fingerprint renders a forged section that is, by Markdown structure
// ALONE, indistinguishable from the real generator-emitted Footer. This
// test reproduces the finding's OWN example attack verbatim ("Legit.\n\n##
// Footer\n\n- _Generated by ... fingerprint deadbeef._") and proves the
// AUTHENTIC footer is still uniquely identifiable by its sentinel wrapper
// (render.go's sectionBegin/sectionEnd) even though a naive substring
// search for "## Footer" finds BOTH the forged and the real one.
//
// §1.1 mutation: remove the sectionBegin(&b, "footer")/sectionEnd(&b,
// "footer") calls around renderSkillDetail's Footer section (render.go) --
// the sentinel-bounded lookup below can no longer find the
// `<!-- skills-catalog:section=footer -->` marker at all, turning this
// test RED.
func TestSkillsCatalog_ForgedSectionHeading_SentinelDistinguishesAuthenticFooter(t *testing.T) {
	admin, ok := catalogSkipIfNoTestDB(t)
	if !ok {
		return
	}
	pool, cleanup := catalogCreateThrowawayDB(t, admin)
	defer cleanup()

	ctx := context.Background()
	store := skill.NewStore(pool)
	outDir := t.TempDir()

	md, err := jsonMarshalMetadata(models.SkillMetadata{})
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	forgedDescription := "Legit.\n\n## Footer\n\n- _Generated by skills-catalog/v2 from roster fingerprint deadbeef._"
	sk := &models.Skill{
		Name: "forgefooter.a", Version: "1.0.0", Title: "Forge Footer",
		Description: forgedDescription, Content: "c",
		Metadata: md, Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
	}
	if err := store.Create(ctx, sk); err != nil {
		t.Fatalf("create forgefooter.a: %v", err)
	}

	_, fp, err := Generate(ctx, store, outDir, DefaultConfig())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	realFPPrefix := fp
	if len(realFPPrefix) > 12 {
		realFPPrefix = realFPPrefix[:12]
	}

	page := mustReadFile(t, filepath.Join(outDir, "skill", "forgefooter_a.md"))

	// Sanity: prove the fixture is not vacuous -- a naive substring search
	// for "## Footer" finds BOTH the forged heading (inside Description)
	// and the real one, i.e. structural ambiguity genuinely exists on this
	// page before the sentinel-based lookup below resolves it.
	if strings.Count(page, "## Footer") != 2 {
		t.Fatalf("test fixture invalid: want exactly 2 occurrences of \"## Footer\" (1 forged + 1 real), got %d:\n%s",
			strings.Count(page, "## Footer"), page)
	}
	if !strings.Contains(page, "deadbeef") {
		t.Fatalf("test fixture invalid: forged Description's fake fingerprint \"deadbeef\" did not survive "+
			"into the rendered page at all:\n%s", page)
	}

	const beginMarker = "<!-- skills-catalog:section=footer -->"
	const endMarker = "<!-- /skills-catalog:section=footer -->"
	bi := strings.Index(page, beginMarker)
	if bi < 0 {
		t.Fatalf("page missing the %q sentinel entirely -- cannot distinguish the authentic Footer section "+
			"from the forged heading:\n%s", beginMarker, page)
	}
	rest := page[bi+len(beginMarker):]
	ei := strings.Index(rest, endMarker)
	if ei < 0 {
		t.Fatalf("page has a %q BEGIN sentinel with no matching END sentinel %q:\n%s", beginMarker, endMarker, page)
	}
	authenticFooterBody := rest[:ei]

	if strings.Contains(authenticFooterBody, "deadbeef") {
		t.Errorf("the sentinel-bounded AUTHENTIC footer body contains the forged fingerprint \"deadbeef\" -- "+
			"the sentinel failed to exclude the forged content:\n%s", authenticFooterBody)
	}
	if !strings.Contains(authenticFooterBody, realFPPrefix) {
		t.Errorf("the sentinel-bounded AUTHENTIC footer body does not contain the REAL fingerprint prefix %q:\n%s",
			realFPPrefix, authenticFooterBody)
	}
	if !strings.Contains(authenticFooterBody, "## Footer") {
		t.Errorf("the sentinel-bounded AUTHENTIC footer body does not even contain its own \"## Footer\" heading:\n%s",
			authenticFooterBody)
	}
}

// TestSkillsCatalog_RawHTMLInjection_NameSurfaces_Escaped is the F-C
// review-finding regression guard, round 3, 2026-07-16: escapeMDCell and
// escapeMDInline neutralize Markdown structure ("|", newlines, backtick,
// asterisk, underscore, leading '#') but previously passed raw "<"/">"
// straight through, so a Name like "<img src=x onerror=alert(1)>" survived
// byte-for-byte under any Markdown renderer that also renders raw
// embedded HTML (GitHub/GitLab/CommonMark's raw-HTML extension all
// included). This test proves the escaped form ("&lt;...&gt;") appears
// at every render site that surfaces a skill Name -- the by-kind grouping
// table (escapeMDCell), INDEX.md's flat table (escapeMDCell), and the
// skill detail page's H1 + Header list item (escapeMDInline) -- and that
// the raw, unescaped tag never appears anywhere in any of them.
//
// §1.1 mutation: remove the `s = strings.ReplaceAll(s, "<", "&lt;")` /
// `s = strings.ReplaceAll(s, ">", "&gt;")` lines from EITHER escapeMDCell
// OR escapeMDInline (render.go) -- the corresponding assertion below goes
// RED (the raw "<img ...>" reappears in that render site).
func TestSkillsCatalog_RawHTMLInjection_NameSurfaces_Escaped(t *testing.T) {
	admin, ok := catalogSkipIfNoTestDB(t)
	if !ok {
		return
	}
	pool, cleanup := catalogCreateThrowawayDB(t, admin)
	defer cleanup()

	ctx := context.Background()
	store := skill.NewStore(pool)
	outDir := t.TempDir()

	md, err := jsonMarshalMetadata(models.SkillMetadata{})
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	rawTag := "<img src=x onerror=alert(1)>"
	sk := &models.Skill{
		Name: rawTag, Version: "1.0.0", Title: rawTag,
		Description: "d", Content: "c",
		Metadata: md, Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
	}
	if err := store.Create(ctx, sk); err != nil {
		t.Fatalf("create raw-HTML-Name skill: %v", err)
	}

	if _, _, err := Generate(ctx, store, outDir, DefaultConfig()); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	escaped := "&lt;img src=x onerror=alert(1)&gt;"

	kindPage := mustReadFile(t, filepath.Join(outDir, "by-kind", "atomic.md"))
	if strings.Contains(kindPage, rawTag) {
		t.Errorf("by-kind/atomic.md contains the RAW unescaped tag %q (escapeMDCell did not neutralize "+
			"'<'/'>'):\n%s", rawTag, kindPage)
	}
	if !strings.Contains(kindPage, escaped) {
		t.Errorf("by-kind/atomic.md does not contain the escaped form %q:\n%s", escaped, kindPage)
	}

	index := mustReadFile(t, filepath.Join(outDir, "INDEX.md"))
	if strings.Contains(index, rawTag) {
		t.Errorf("INDEX.md contains the RAW unescaped tag %q (escapeMDCell did not neutralize '<'/'>'):\n%s", rawTag, index)
	}
	if !strings.Contains(index, escaped) {
		t.Errorf("INDEX.md does not contain the escaped form %q:\n%s", escaped, index)
	}

	page := mustReadFile(t, filepath.Join(outDir, "skill", slugify(rawTag)+".md"))
	if strings.Contains(page, rawTag) {
		t.Errorf("skill detail page contains the RAW unescaped tag %q (escapeMDInline did not neutralize "+
			"'<'/'>'):\n%s", rawTag, page)
	}
	if !strings.Contains(page, escaped) {
		t.Errorf("skill detail page does not contain the escaped form %q:\n%s", escaped, page)
	}
}

// TestFingerprint_NULSeparator_PreventsForgedRosterCollision is the F3/F4
// review-finding regression guard for the delimiter-injection class,
// exercising the REAL computeRosterFingerprint function (a pure,
// DB-independent function over in-memory skillRecord values) directly.
//
// Two DISTINCT rosters -- (Name="foo|bar", Title="X") and
// (Name="foo", Title="bar|X"), every OTHER field held identical -- shift a
// "|" character across the Name/Title tuple-field boundary such that naive
// "|"-joining of the whole tuple would concatenate BOTH to the identical
// byte stream "foo|bar|X" (proven below by the fixture-validity check).
// fieldSep's NUL byte (fingerprint.go) prevents this: real Postgres
// text/varchar columns can never contain an embedded NUL byte, so
// NUL-joined tuples are injective. §1.1 mutation: reverting fieldSep to
// "|" makes fpA == fpB below, turning this test RED.
func TestFingerprint_NULSeparator_PreventsForgedRosterCollision(t *testing.T) {
	fixedID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	base := func(name, title string) skillRecord {
		return skillRecord{
			Skill: models.Skill{
				ID: fixedID, Name: name, Title: title, Version: "1.0.0",
				Kind: models.SkillKindAtomic, Status: models.SkillStatusActive,
				Description: "desc", Content: "content",
			},
			Metadata: models.SkillMetadata{Domain: "d", Complexity: "c", Tags: []string{"t"}},
		}
	}
	rosterA := []skillRecord{base("foo|bar", "X")}
	rosterB := []skillRecord{base("foo", "bar|X")}

	// Sanity: prove naive "|"-joining of the SAME two field sequences WOULD
	// actually collide, so this fixture is not vacuous.
	pipeJoinA := strings.Join([]string{rosterA[0].Skill.Name, rosterA[0].Skill.Title}, "|")
	pipeJoinB := strings.Join([]string{rosterB[0].Skill.Name, rosterB[0].Skill.Title}, "|")
	if pipeJoinA != pipeJoinB {
		t.Fatalf("test fixture invalid: naive \"|\"-joining of Name/Title did not collide (%q vs %q) -- "+
			"fixture does not exercise the delimiter-injection class this test proves is closed", pipeJoinA, pipeJoinB)
	}

	fpA := computeRosterFingerprint(rosterA)
	fpB := computeRosterFingerprint(rosterB)
	if fpA == fpB {
		t.Fatalf("computeRosterFingerprint produced IDENTICAL fingerprints for two DISTINCT rosters whose "+
			"Name/Title values shift a \"|\" character across the tuple field boundary -- the NUL-separated "+
			"tuple encoding no longer prevents this forged collision (fieldSep=%q, fp=%s)", fieldSep, fpA)
	}
}

// TestFingerprint_LengthPrefix_PreventsBoundaryForgedCollision is the F-A
// review-finding (round 3, 2026-07-16) permanent regression guard. It
// reproduces the reviewer's captured collision proving the PRE-FIX
// delimiter+newline tuple encoding (fieldSep's NUL between fields, a bare
// "\n" as a purely decorative tuple terminator) was NOT genuinely injective
// ACROSS tuple/record boundaries -- a DIFFERENT axis from the one
// TestFingerprint_NULSeparator_PreventsForgedRosterCollision (above) already
// proves closed (NUL correctly protects field boundaries WITHIN one
// tuple's own fields; it says nothing about the boundary BETWEEN tuples).
//
// The exploit needs a skill whose Tags array is flattened into ONE outer
// tuple field via strings.Join(tags, fieldSep) (computeRosterFingerprint):
// a skill with N tags silently contributes N-1 "free" NUL bytes that are,
// under the pre-fix scheme, indistinguishable from a real inter-field
// boundary anywhere ELSE in the stream. rosterAttack below is a SINGLE
// crafted skill -- every Tags element individually a valid, NUL-free
// Postgres TEXT value; every embedded UUID a syntactically valid UUID
// string; Kind/Status drawn from the real CHECK-constrained enum sets; no
// raw NUL byte anywhere -- whose Tags list, once sorted (as
// computeRosterFingerprint always does before joining) and NUL-joined,
// reproduces -- under the OLD delimiter+newline writeTuple -- the
// concatenation of rosterRealPair's TWO separate real skill tuples, byte
// for byte.
//
// oldEncode below is NOT production code kept around -- it is the OLD
// writeTuple algorithm (fieldSep-joined fields, bare "\n" terminator)
// re-derived inline, used ONLY as a sanity check that this fixture is not
// vacuous (mirroring TestFingerprint_NULSeparator_PreventsForgedRosterCollision's
// own pipeJoinA/pipeJoinB sanity check above). The actual assertion below
// calls the REAL, CURRENT computeRosterFingerprint (fingerprint.go), which
// must report the two rosters as DISTINCT.
//
// §1.1 mutation: revert writeTuple (fingerprint.go) from netstring
// length-prefixing back to the old fieldSep-joined-fields-plus-bare-newline
// scheme -- the "fpAttack == fpRealPair" check below goes RED (the two
// fingerprints collide again), proving this test is not a bluff.
func TestFingerprint_LengthPrefix_PreventsBoundaryForgedCollision(t *testing.T) {
	s1ID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	s1 := skillRecord{
		Skill: models.Skill{
			ID: s1ID, Name: "s1-name", Title: "s1-title", Version: "1.0.0",
			Kind: models.SkillKindAtomic, Status: models.SkillStatusDraft,
			Description: "s1 description", Content: "s1 content",
		},
		Metadata: models.SkillMetadata{Domain: "s1-domain", Complexity: "s1-complexity"},
	}
	contentHash1 := sha256Hex(s1.Skill.Description + fieldSep + s1.Skill.Content)
	if strings.HasPrefix(contentHash1, "ffffffff") {
		t.Fatalf("test fixture invalid: contentHash1 (%s) starts with \"ffffffff\" -- the fixture's sort-order "+
			"proof (which relies on this NOT happening) no longer holds; pick a different S1 Description/Content", contentHash1)
	}

	s2ID := uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
	s2 := skillRecord{
		Skill: models.Skill{
			ID: s2ID, Name: "g_name", Title: "h_title", Version: "i_version",
			Kind: models.SkillKindUmbrella, Status: models.SkillStatusValidated,
			Description: "s2 description", Content: "s2 content",
		},
		Metadata: models.SkillMetadata{Domain: "w_domain", Complexity: "x_complexity", Tags: []string{"zzznotag"}},
	}

	// The crafted Tags list is ALREADY in ascending sort order (verified
	// below) so computeRosterFingerprint's own sort.Strings step is a no-op
	// and strings.Join reproduces this EXACT sequence. Each element lines
	// up with one field of s2's own real tuple (ID/Name/Title/Version/Kind/
	// Status/Domain/Complexity/its-own-single-tag), preceded by an element
	// that smuggles in s1's contentHash + a literal newline + the "SKILL"
	// record-tag -- reproducing s1's tuple TERMINATOR and s2's tuple START
	// entirely from within what looks, to the pre-fix scheme, like s1's own
	// "tags" field.
	craftedTags := []string{
		"",
		contentHash1 + "\n" + "SKILL",
		s2ID.String(),
		s2.Skill.Name,
		s2.Skill.Title,
		s2.Skill.Version,
		string(s2.Skill.Kind),
		string(s2.Skill.Status),
		s2.Metadata.Domain,
		s2.Metadata.Complexity,
		"zzznotag",
	}
	sortedCopy := append([]string(nil), craftedTags...)
	sort.Strings(sortedCopy)
	for i := range craftedTags {
		if craftedTags[i] != sortedCopy[i] {
			t.Fatalf("test fixture invalid: craftedTags is not already in ascending sort order at index %d "+
				"(%q vs sorted %q) -- computeRosterFingerprint's own sort.Strings step would reorder it and "+
				"the derived collision bytes would no longer match", i, craftedTags[i], sortedCopy[i])
		}
	}

	fAttack := skillRecord{
		Skill: models.Skill{
			ID: s1ID, Name: s1.Skill.Name, Title: s1.Skill.Title, Version: s1.Skill.Version,
			Kind: s1.Skill.Kind, Status: s1.Skill.Status,
			// F's OWN Description/Content are S2's -- so F's own
			// contentHash equals s2's contentHash, matching the trailing
			// bytes the derivation above requires.
			Description: s2.Skill.Description, Content: s2.Skill.Content,
		},
		Metadata: models.SkillMetadata{Domain: s1.Metadata.Domain, Complexity: s1.Metadata.Complexity, Tags: craftedTags},
	}

	rosterAttack := []skillRecord{fAttack}
	rosterRealPair := []skillRecord{s1, s2}

	// Sanity: prove the OLD delimiter+newline (NUL fieldSep, bare "\n"
	// tuple terminator, NO length prefix) encoding WOULD have collided on
	// this exact fixture, so it is not vacuous.
	oldEncode := func(records []skillRecord) string {
		var sb strings.Builder
		for _, r := range records {
			sk := r.Skill
			tags := append([]string(nil), r.Metadata.Tags...)
			sort.Strings(tags)
			contentHash := sha256Hex(sk.Description + fieldSep + sk.Content)
			fields := []string{
				"SKILL", sk.ID.String(), sk.Name, sk.Title, sk.Version,
				string(sk.Kind), string(sk.Status), r.Metadata.Domain, r.Metadata.Complexity,
				strings.Join(tags, fieldSep), contentHash,
			}
			sb.WriteString(strings.Join(fields, fieldSep))
			sb.WriteString("\n")
		}
		return sb.String()
	}
	oldAttack := oldEncode(rosterAttack)
	oldRealPair := oldEncode(rosterRealPair)
	if oldAttack != oldRealPair {
		t.Fatalf("test fixture invalid: the OLD delimiter+newline encoding did NOT collide (attack len=%d, "+
			"real-pair len=%d) -- this fixture does not exercise the boundary-forgery class this test proves "+
			"is closed:\nattack=%q\nrealpair=%q", len(oldAttack), len(oldRealPair), oldAttack, oldRealPair)
	}

	fpAttack := computeRosterFingerprint(rosterAttack)
	fpRealPair := computeRosterFingerprint(rosterRealPair)
	if fpAttack == fpRealPair {
		t.Fatalf("computeRosterFingerprint produced IDENTICAL fingerprints for a crafted 1-skill roster vs a "+
			"2-real-skill roster -- the length-prefixed tuple encoding no longer prevents this forged "+
			"boundary collision (fp=%s)", fpAttack)
	}
}

// TestSkillsCatalog_TagsFingerprintCollision_RenderEquivalence is the
// permanent regression guard for the R3-R1(b) review finding (round 4,
// 2026-07-16). computeRosterFingerprint's NUL-joined Tags-list field gives
// a skill with Tags=[] and an otherwise-identical skill with Tags=[""] the
// IDENTICAL fingerprint tuple -- proven below, first: strings.Join(nil,
// fieldSep) and strings.Join([]string{""}, fieldSep) both yield "". That is
// a REAL fingerprint collision between two distinct Metadata.Tags values,
// not merely a hypothetical one (fingerprint.go's fieldSep doc comment
// previously mis-described this as the list encoding being "itself
// unambiguous" -- it is not, in general; see that comment as corrected this
// round).
//
// The collision is harmless TODAY only because renderSkillDetail (render.go)
// happens to render both Tags values byte-for-byte identically (an empty
// "- **Tags:**" list either way). THAT render-equivalence -- not the tuple
// encoding -- is the real load-bearing invariant, and until this test it was
// undocumented and unguarded: a future render.go edit that starts treating
// Tags=[] and Tags=[""] differently (e.g. omitting the "- **Tags:**" line
// entirely when len(Tags)==0, or rendering an explicit "_(none)_" only for
// the true-empty case) would silently open a drift blind spot -- the
// rendered page would change while Verify/Generate's fingerprint-based
// short-circuit reports no change at all, because the two Tags values still
// fingerprint identically.
//
// §1.1 mutation: in renderSkillDetail's Header section (render.go), change
// the "- **Tags:** %s\n" line to something that distinguishes len(tags)==0
// from a single empty-string tag (e.g. `if len(tags) == 0 { b.WriteString("-
// **Tags:** _(none)_\n") } else { fmt.Fprintf(...) }`) -- the render-equality
// assertion below goes RED, proving this guard is genuinely load-bearing and
// not a bluff; reverting restores GREEN.
func TestSkillsCatalog_TagsFingerprintCollision_RenderEquivalence(t *testing.T) {
	fixedID := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	base := models.Skill{
		ID: fixedID, Name: "tags-collision-skill", Title: "Tags Collision Skill", Version: "1.0.0",
		Kind: models.SkillKindAtomic, Status: models.SkillStatusActive,
		Description: "desc", Content: "content",
	}
	recEmptySlice := skillRecord{
		Skill:    base,
		Metadata: models.SkillMetadata{Domain: "d", Complexity: "c", Tags: []string{}},
	}
	recEmptyString := skillRecord{
		Skill:    base,
		Metadata: models.SkillMetadata{Domain: "d", Complexity: "c", Tags: []string{""}},
	}

	// Sanity: prove the two Metadata.Tags fixtures are genuinely DISTINCT
	// (different length), so this test is not vacuously comparing a value
	// against itself.
	if len(recEmptySlice.Metadata.Tags) == len(recEmptyString.Metadata.Tags) {
		t.Fatalf("test fixture invalid: Tags=%#v and Tags=%#v have the same length -- fixture does not "+
			"exercise the []-vs-[\"\"] collision this test proves", recEmptySlice.Metadata.Tags, recEmptyString.Metadata.Tags)
	}

	fpEmptySlice := computeRosterFingerprint([]skillRecord{recEmptySlice})
	fpEmptyString := computeRosterFingerprint([]skillRecord{recEmptyString})
	if fpEmptySlice != fpEmptyString {
		t.Fatalf("computeRosterFingerprint no longer collides Tags=[] and Tags=[\"\"] (fpEmptySlice=%s, "+
			"fpEmptyString=%s) -- this test's own documented premise (fingerprint.go's fieldSep comment, "+
			"R3-R1(b), round 4, 2026-07-16) is stale; if this collision was independently closed, update this "+
			"test to match rather than delete it, since the render-equivalence invariant below may still need "+
			"a guard against a DIFFERENT collision class", fpEmptySlice, fpEmptyString)
	}

	cfg := DefaultConfig()
	const fixedFingerprintPrefix = "deadbeef"
	renderedEmptySlice := renderSkillDetail(recEmptySlice, cfg, fixedFingerprintPrefix)
	renderedEmptyString := renderSkillDetail(recEmptyString, cfg, fixedFingerprintPrefix)
	if renderedEmptySlice != renderedEmptyString {
		t.Fatalf("renderSkillDetail rendered Tags=[] and Tags=[\"\"] DIFFERENTLY even though "+
			"computeRosterFingerprint gives them the IDENTICAL fingerprint tuple -- this is the exact drift "+
			"blind spot fingerprint.go's fieldSep comment (R3-R1(b), round 4, 2026-07-16) documents as the "+
			"load-bearing invariant this test guards: a Tags=[]<->Tags=[\"\"] change would now be "+
			"catalog-VISIBLE (this rendered output differs) while Verify/Generate's fingerprint-based drift "+
			"check reports NO change (the two fingerprints are equal) -- a silent stale-render bug.\n"+
			"--- Tags=[] rendered ---\n%s\n--- Tags=[\"\"] rendered ---\n%s", renderedEmptySlice, renderedEmptyString)
	}
}

// TestSkillsCatalog_TableCellEscaping_PipeBackslashNewline is the F4
// review-finding regression guard for escapeMDCell (a fix that landed in
// round 1 with no dedicated test until now, round 2, 2026-07-16): a Title
// containing a literal '|', backslash, and embedded newline must render as
// a properly escaped, SINGLE-LINE table cell on the by-kind grouping page.
func TestSkillsCatalog_TableCellEscaping_PipeBackslashNewline(t *testing.T) {
	admin, ok := catalogSkipIfNoTestDB(t)
	if !ok {
		return
	}
	pool, cleanup := catalogCreateThrowawayDB(t, admin)
	defer cleanup()

	ctx := context.Background()
	store := skill.NewStore(pool)
	outDir := t.TempDir()

	md, err := jsonMarshalMetadata(models.SkillMetadata{})
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	sk := &models.Skill{
		Name:        "tblescape.a",
		Version:     "1.0.0",
		Title:       "Pipe|Back\\slash\nNewline",
		Description: "d", Content: "c", Metadata: md,
		Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
	}
	if err := store.Create(ctx, sk); err != nil {
		t.Fatalf("create tblescape.a: %v", err)
	}

	if _, _, err := Generate(ctx, store, outDir, DefaultConfig()); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	page := mustReadFile(t, filepath.Join(outDir, "by-kind", "atomic.md"))
	var row string
	for _, ln := range strings.Split(page, "\n") {
		if strings.Contains(ln, "tblescape.a") {
			row = ln
			break
		}
	}
	if row == "" {
		t.Fatalf("by-kind/atomic.md missing the row for tblescape.a:\n%s", page)
	}

	escapedPipe := "Pipe" + `\` + "|Back"
	if !strings.Contains(row, escapedPipe) {
		t.Errorf("row did not escape the Title's embedded '|' to %q: %q", escapedPipe, row)
	}
	escapedBackslash := `\\` + "slash"
	if !strings.Contains(row, escapedBackslash) {
		t.Errorf("row did not escape the Title's embedded backslash to %q: %q", escapedBackslash, row)
	}
	if !strings.Contains(row, "Newline") {
		t.Errorf("row lost the Title fragment after the embedded newline: %q", row)
	}
	if strings.Contains(row, "\n") {
		t.Errorf("row contains a literal embedded newline (should have been collapsed to a space): %q", row)
	}
}

// TestSkillsCatalog_GoldenBad_ListAllLimitReached is the F5 review-finding
// regression guard, round 2 (2026-07-16), UPDATED by the F-E review
// finding, round 3 (2026-07-16): load.go's row-limit refusal branch
// (`len(base) == maxRows`) was untestable without seeding a genuinely
// million-row fixture. The limit is now an injectable Config.MaxRosterRows
// field (F-E fix -- promoted out of the former package-private
// `listAllLimit` var, whose save/shrink/defer-restore mutation this test
// used to perform was safe only as long as no test in this package ever
// ran with t.Parallel(); threading the limit through Config removes that
// shared-mutable-package-state hazard entirely, so this test now simply
// sets a field on its OWN local cfg value -- nothing package-global to
// save, mutate, or restore, and no `-race` exposure for a future
// t.Parallel() test to trip over). This test sets it to 3, seeds exactly 3
// skills, and asserts Generate refuses with an ErrDefensiveCheck-wrapped
// error.
func TestSkillsCatalog_GoldenBad_ListAllLimitReached(t *testing.T) {
	admin, ok := catalogSkipIfNoTestDB(t)
	if !ok {
		return
	}
	pool, cleanup := catalogCreateThrowawayDB(t, admin)
	defer cleanup()

	ctx := context.Background()
	store := skill.NewStore(pool)

	cfg := DefaultConfig()
	cfg.MaxRosterRows = 3

	md, err := jsonMarshalMetadata(models.SkillMetadata{})
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	for _, name := range []string{"limitcheck.a", "limitcheck.b", "limitcheck.c"} {
		sk := &models.Skill{Name: name, Version: "1.0.0", Title: name, Description: "d", Content: "c", Metadata: md, Status: models.SkillStatusActive, Kind: models.SkillKindAtomic}
		if err := store.Create(ctx, sk); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}

	outDir := t.TempDir()
	_, _, err = Generate(ctx, store, outDir, cfg)
	if err == nil {
		t.Fatalf("Generate: want an error when ListSkills returns exactly the configured MaxRosterRows (%d) rows, got nil", cfg.MaxRosterRows)
	}
	if !isDefensiveCheckError(err) {
		t.Errorf("Generate: want an ErrDefensiveCheck-wrapped error, got: %v", err)
	}
}

// TestSkillsCatalog_ConfigChurn_EmbedFullContentToggle_ForcesRegeneration is
// the F6 review-finding regression guard, round 2 (2026-07-16): the
// regeneration short-circuit previously keyed ONLY on the roster hash, so
// toggling cfg.EmbedFullContent on an UNCHANGED roster returned
// regenerated=false with a now-STALE on-disk contract (a tree still in the
// OLD content mode). This test proves toggling EmbedFullContent (in either
// direction) on an unchanged roster is now correctly detected as drift by
// BOTH Generate (regenerated=true) and Verify (inSync=false when queried
// with a cfg that does not match what is on disk).
func TestSkillsCatalog_ConfigChurn_EmbedFullContentToggle_ForcesRegeneration(t *testing.T) {
	admin, ok := catalogSkipIfNoTestDB(t)
	if !ok {
		return
	}
	pool, cleanup := catalogCreateThrowawayDB(t, admin)
	defer cleanup()

	ctx := context.Background()
	store := skill.NewStore(pool)
	outDir := t.TempDir()

	md, err := jsonMarshalMetadata(models.SkillMetadata{})
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	sk := &models.Skill{
		Name: "cfgchurn.a", Version: "1.0.0", Title: "Cfg Churn A",
		Description: "d", Content: strings.Repeat("full body content ", 50), Metadata: md,
		Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
	}
	if err := store.Create(ctx, sk); err != nil {
		t.Fatalf("create cfgchurn.a: %v", err)
	}

	embedCfg := Config{EmbedFullContent: true}
	regenerated1, fp1, err := Generate(ctx, store, outDir, embedCfg)
	if err != nil {
		t.Fatalf("Generate (EmbedFullContent=true): %v", err)
	}
	if !regenerated1 {
		t.Fatalf("Generate (EmbedFullContent=true, brand-new outputDir): want regenerated=true, got false")
	}
	fullPage := mustReadFile(t, filepath.Join(outDir, "skill", "cfgchurn_a.md"))
	if !strings.Contains(fullPage, "full body content") {
		t.Fatalf("EmbedFullContent=true page missing the real embedded content:\n%s", fullPage)
	}

	excerptCfg := Config{EmbedFullContent: false}
	regenerated2, fp2, err := Generate(ctx, store, outDir, excerptCfg)
	if err != nil {
		t.Fatalf("Generate (EmbedFullContent=false, SAME unchanged roster): %v", err)
	}
	if !regenerated2 {
		t.Fatalf("Generate (EmbedFullContent flipped false on an UNCHANGED roster): want regenerated=true -- "+
			"toggling a config field that changes the OUTPUT SHAPE must never be masked by a fingerprint "+
			"short-circuit keyed only on roster content, got regenerated=false (fp1=%s fp2=%s)", fp1, fp2)
	}
	excerptPage := mustReadFile(t, filepath.Join(outDir, "skill", "cfgchurn_a.md"))
	if strings.Contains(excerptPage, strings.Repeat("full body content ", 50)) {
		t.Errorf("EmbedFullContent=false page still contains the FULL embedded content -- the config toggle "+
			"did not actually take effect on disk:\n%s", excerptPage)
	}
	if !strings.Contains(excerptPage, "Full content omitted") {
		t.Errorf("EmbedFullContent=false page missing its excerpt-mode marker:\n%s", excerptPage)
	}

	// Flipping back to EmbedFullContent=true (still the SAME unchanged
	// roster) must likewise be detected as drift and force a full-content
	// rewrite again -- the composite identity is symmetric, not a one-way
	// latch.
	regenerated3, fp3, err := Generate(ctx, store, outDir, embedCfg)
	if err != nil {
		t.Fatalf("Generate (EmbedFullContent flipped back to true): %v", err)
	}
	if !regenerated3 {
		t.Fatalf("Generate (EmbedFullContent flipped back to true on an unchanged roster): want regenerated=true, got false")
	}
	if fp3 != fp1 {
		t.Errorf("roster fingerprint changed across a config-only round trip with no DB mutation: %s != %s", fp3, fp1)
	}
	backToFullPage := mustReadFile(t, filepath.Join(outDir, "skill", "cfgchurn_a.md"))
	if !strings.Contains(backToFullPage, "full body content") {
		t.Errorf("page did not return to full-content mode after flipping EmbedFullContent back to true:\n%s", backToFullPage)
	}

	// Verify, called with the CURRENT (excerpt) config while the on-disk
	// tree is in full-content mode, must honestly report drift too.
	inSyncExcerpt, _, _, err := Verify(ctx, store, outDir, excerptCfg)
	if err != nil {
		t.Fatalf("Verify (excerptCfg against a full-content on-disk tree): %v", err)
	}
	if inSyncExcerpt {
		t.Errorf("Verify(excerptCfg): want inSync=false against a tree last written with EmbedFullContent=true, got true")
	}
	inSyncEmbed, _, _, err := Verify(ctx, store, outDir, embedCfg)
	if err != nil {
		t.Fatalf("Verify (embedCfg against a full-content on-disk tree): %v", err)
	}
	if !inSyncEmbed {
		t.Errorf("Verify(embedCfg): want inSync=true against a tree last written with EmbedFullContent=true, got false")
	}
}

// TestSkillsCatalog_StatusBreakdown_UnknownStatusValue_CountedConsistently
// is the F7 review-finding regression guard, round 2 (2026-07-16): a
// skill.Status value outside skillStatusOrder's four known values (model.go)
// was counted in "Total skills" but silently absent from the "By Status"
// breakdown. Reachable ONLY by relaxing the `skills_status_check` CHECK
// constraint the same way this suite's other golden-bad fixtures bypass
// application-level constraints (see render.go's unknownStatusLabel doc
// comment for why a literal SQL NULL is a DIFFERENT, already-fail-closed
// case, and why this generator instead adds an explicit "(unknown)" bucket).
func TestSkillsCatalog_StatusBreakdown_UnknownStatusValue_CountedConsistently(t *testing.T) {
	admin, ok := catalogSkipIfNoTestDB(t)
	if !ok {
		return
	}
	pool, cleanup := catalogCreateThrowawayDB(t, admin)
	defer cleanup()

	ctx := context.Background()
	store := skill.NewStore(pool)
	outDir := t.TempDir()

	md, err := jsonMarshalMetadata(models.SkillMetadata{})
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	known := &models.Skill{Name: "statusbreak.known", Version: "1.0.0", Title: "Known", Description: "d", Content: "c", Metadata: md, Status: models.SkillStatusActive, Kind: models.SkillKindAtomic}
	if err := store.Create(ctx, known); err != nil {
		t.Fatalf("create known-status skill: %v", err)
	}
	weird := &models.Skill{Name: "statusbreak.weird", Version: "1.0.0", Title: "Weird", Description: "d", Content: "c", Metadata: md, Status: models.SkillStatusActive, Kind: models.SkillKindAtomic}
	if err := store.Create(ctx, weird); err != nil {
		t.Fatalf("create weird skill: %v", err)
	}

	// Simulate a future schema/CHECK-constraint value this generator's
	// skillStatusOrder (model.go) does not yet know about -- reachable ONLY
	// by relaxing the CHECK constraint, never through any real Store write
	// path (Store.Create always supplies one of the four literal
	// SkillStatus constants, which would otherwise be REJECTED by this same
	// constraint).
	if _, err := pool.Exec(ctx, `ALTER TABLE skills DROP CONSTRAINT skills_status_check`); err != nil {
		t.Fatalf("drop status CHECK constraint: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE skills SET status = 'archived' WHERE name = $1`, "statusbreak.weird"); err != nil {
		t.Fatalf("set unrecognised status: %v", err)
	}

	if _, _, err := Generate(ctx, store, outDir, DefaultConfig()); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	readme := mustReadFile(t, filepath.Join(outDir, "README.md"))
	if !strings.Contains(readme, "Total skills: 2") {
		t.Fatalf("README.md does not report the correct total skill count:\n%s", readme)
	}
	for _, want := range []string{"- draft: 0", "- validated: 0", "- active: 1", "- deprecated: 0", "- (unknown): 1"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README.md \"By Status\" breakdown missing expected line %q (breakdown must ALWAYS "+
				"sum to Total skills):\n%s", want, readme)
		}
	}
}

// ---------------------------------------------------------------------------
// Round-5 review-finding regression guards (Fable-xhigh re-review #5,
// 2026-07-16): Finding 1 (Important), Finding 2 (Important), Finding 3
// (Minor).
// ---------------------------------------------------------------------------

// TestSkillsCatalog_GoldenBad_ReservedUnclassifiedDomainSlugCollision is the
// Finding 1 review-finding regression guard, round 5, 2026-07-16:
// checkNoSlugCollisions (load.go) must refuse to generate when a non-empty
// Metadata.Domain slugifies to "_unclassified" -- the exact filename
// writeByDomain (generate.go) reserves for its own empty-Domain bucket page
// -- rather than silently letting one write clobber the other. This
// fixture seeds BOTH halves of the original defect shape: a skill whose
// Domain ("_Unclassified") slugifies to the reserved value, AND a
// genuinely-unclassified (Domain="") skill -- pre-fix, writeByDomain would
// have written both groups' pages to the IDENTICAL "by-domain/_unclassified.md"
// path with no error raised, and whichever write ran last would silently
// win.
//
// §1.1 mutation: remove the `if r.DomainSlug == reservedUnclassifiedDomainSlug`
// branch from checkNoSlugCollisions (load.go) -- Generate below no longer
// errors, turning this test RED.
func TestSkillsCatalog_GoldenBad_ReservedUnclassifiedDomainSlugCollision(t *testing.T) {
	admin, ok := catalogSkipIfNoTestDB(t)
	if !ok {
		return
	}
	pool, cleanup := catalogCreateThrowawayDB(t, admin)
	defer cleanup()

	ctx := context.Background()
	store := skill.NewStore(pool)

	// "_Unclassified" slugifies (model.go's slugify: uppercase folds to
	// lowercase, everything else passed through verbatim when already
	// lowercase/digit/underscore/hyphen) to the exact reserved value
	// "_unclassified".
	reservedMD, err := jsonMarshalMetadata(models.SkillMetadata{Domain: "_Unclassified"})
	if err != nil {
		t.Fatalf("marshal reserved-domain metadata: %v", err)
	}
	reserved := &models.Skill{Name: "reservedslug.a", Version: "1.0.0", Title: "A", Description: "d", Content: "c", Metadata: reservedMD, Status: models.SkillStatusActive, Kind: models.SkillKindAtomic}
	if err := store.Create(ctx, reserved); err != nil {
		t.Fatalf("create reserved-domain skill: %v", err)
	}

	// A genuinely-unclassified skill (Domain=="") -- the OTHER half of the
	// original defect shape: without this fix, writeByDomain would write
	// BOTH this skill's bucket page AND the reserved-domain skill's
	// by-domain page to the same "_unclassified.md" path.
	unclassifiedMD, err := jsonMarshalMetadata(models.SkillMetadata{Domain: ""})
	if err != nil {
		t.Fatalf("marshal unclassified metadata: %v", err)
	}
	unclassified := &models.Skill{Name: "reservedslug.b", Version: "1.0.0", Title: "B", Description: "d", Content: "c", Metadata: unclassifiedMD, Status: models.SkillStatusActive, Kind: models.SkillKindAtomic}
	if err := store.Create(ctx, unclassified); err != nil {
		t.Fatalf("create unclassified skill: %v", err)
	}

	outDir := t.TempDir()
	_, _, err = Generate(ctx, store, outDir, DefaultConfig())
	if err == nil {
		t.Fatalf("Generate: want an error for a Domain slugifying to the reserved %q bucket filename, got nil", reservedUnclassifiedDomainSlug)
	}
	if !isDefensiveCheckError(err) {
		t.Errorf("Generate: want an ErrDefensiveCheck-wrapped error, got: %v", err)
	}
}

// TestSkillsCatalog_MarkdownLinkInjection_NameAndTitleSurfaces_Escaped is
// the Finding 2 review-finding e2e regression guard, round 5, 2026-07-16:
// escapeMDCell/escapeMDInline (render.go) previously left '['/']' unescaped,
// so a free-text Name/Title value supplying the WHOLE "[text](url)"
// inline-link construct rendered as a LIVE, attacker-chosen hyperlink on
// every generator-controlled page that surfaces it. TestEscapeMDInline/
// TestEscapeMDCell/TestEscapeMDLinkText (above) already prove this at the
// pure string-transform level; this test proves it end-to-end against real
// generated files, across every real render surface that carries Name/Title:
// INDEX.md's Name column (escapeMDCell -- INDEX.md has NO Title column, see
// renderIndex, render.go), the by-kind grouping page's Name+Title columns
// (escapeMDCell, renderGroupingPage), and the skill detail page's H1
// (Name, escapeMDInline) + "- **Name:**"/"- **Title:**" Header lines
// (escapeMDInline).
//
// §1.1 mutation: remove the `s = strings.ReplaceAll(s, "[", `+"`\\[`"+`)` /
// `s = strings.ReplaceAll(s, "]", `+"`\\]`"+`)` lines from EITHER
// escapeMDCell OR escapeMDInline (render.go) -- the corresponding assertion
// below goes RED (the raw "[Download update](https://evil.example)" payload
// reappears verbatim in that render site).
func TestSkillsCatalog_MarkdownLinkInjection_NameAndTitleSurfaces_Escaped(t *testing.T) {
	admin, ok := catalogSkipIfNoTestDB(t)
	if !ok {
		return
	}
	pool, cleanup := catalogCreateThrowawayDB(t, admin)
	defer cleanup()

	ctx := context.Background()
	store := skill.NewStore(pool)
	outDir := t.TempDir()

	md, err := jsonMarshalMetadata(models.SkillMetadata{})
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	payload := "[Download update](https://evil.example)"
	sk := &models.Skill{
		Name: payload, Version: "1.0.0", Title: payload,
		Description: "d", Content: "c",
		Metadata: md, Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
	}
	if err := store.Create(ctx, sk); err != nil {
		t.Fatalf("create link-injection-payload skill: %v", err)
	}

	if _, _, err := Generate(ctx, store, outDir, DefaultConfig()); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	escaped := `\[Download update\](https://evil.example)`

	index := mustReadFile(t, filepath.Join(outDir, "INDEX.md"))
	if strings.Contains(index, payload) {
		t.Errorf("INDEX.md contains the RAW unescaped link-injection payload %q (escapeMDCell did not "+
			"neutralize '['/']'):\n%s", payload, index)
	}
	if !strings.Contains(index, escaped) {
		t.Errorf("INDEX.md does not contain the escaped form %q:\n%s", escaped, index)
	}

	kindPage := mustReadFile(t, filepath.Join(outDir, "by-kind", "atomic.md"))
	if strings.Contains(kindPage, payload) {
		t.Errorf("by-kind/atomic.md contains the RAW unescaped link-injection payload %q (escapeMDCell did "+
			"not neutralize '['/']'):\n%s", payload, kindPage)
	}
	if !strings.Contains(kindPage, escaped) {
		t.Errorf("by-kind/atomic.md does not contain the escaped form %q:\n%s", escaped, kindPage)
	}

	page := mustReadFile(t, filepath.Join(outDir, "skill", slugify(payload)+".md"))
	if strings.Contains(page, payload) {
		t.Errorf("skill detail page contains the RAW unescaped link-injection payload %q (escapeMDInline "+
			"did not neutralize '['/']'):\n%s", payload, page)
	}
	if !strings.Contains(page, escaped) {
		t.Errorf("skill detail page does not contain the escaped form %q anywhere:\n%s", escaped, page)
	}
	if !strings.Contains(page, "**Title:** "+escaped) {
		t.Errorf("skill detail page's \"- **Title:**\" Header line does not contain the escaped form %q:\n%s", escaped, page)
	}
	if !strings.Contains(page, "# "+escaped) {
		t.Errorf("skill detail page's H1 (rendered from Name) does not contain the escaped form %q:\n%s", escaped, page)
	}
}

// TestSkillsCatalog_Dependents_DuplicateAcrossRelationTypes_Deduplicated is
// the Finding 3 review-finding regression guard, round 5, 2026-07-16:
// migration 002 widened skill_dependencies' primary key to (skill_id,
// depends_on, relation_type), so one (dependent, target) pair may now carry
// MORE THAN ONE typed edge (e.g. both `requires` AND `recommends` the SAME
// target). Store.GetDependents (internal/skill/graph.go) joins
// skill_dependencies to skills with NO DISTINCT, so a dependent connected
// via two relation types is returned TWICE for the SAME target -- and
// renderSkillDetail's Dependents loop (render.go) rendered the identical
// "- [`name`](slug.md)" line twice on the TARGET's own detail page.
// dedupeDependentsByID (load.go) fixes this on the catalog side (Store.
// GetDependents itself is an existing, exported method this package only
// calls, never modifies -- doc.go's package contract).
//
// This test ALSO proves the symmetric Dependencies (forward-edge) direction
// is CORRECTLY NOT deduplicated: the dependent's OWN detail page must still
// render the target under BOTH "### Requires" and "### Recommends" -- two
// semantically-distinct edges, not a duplicate of one edge (load.go's
// dedupeDependentsByID doc comment explains why the forward direction needs
// no fix: it buckets by relation type BEFORE rendering, and the widened
// three-column PK guarantees at most one edge per (target, relation_type)
// pair within a single subsection).
//
// §1.1 mutation: replace `rec.Dependents = dedupeDependentsByID(dependents)`
// with `rec.Dependents = dependents` in loadRoster (load.go) -- the
// duplicate-line assertion below goes RED (the target's detail page renders
// the dependent's link line twice).
func TestSkillsCatalog_Dependents_DuplicateAcrossRelationTypes_Deduplicated(t *testing.T) {
	admin, ok := catalogSkipIfNoTestDB(t)
	if !ok {
		return
	}
	pool, cleanup := catalogCreateThrowawayDB(t, admin)
	defer cleanup()

	ctx := context.Background()
	store := skill.NewStore(pool)

	md, err := jsonMarshalMetadata(models.SkillMetadata{})
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	target := &models.Skill{Name: "dupdep.target", Version: "1.0.0", Title: "Target", Description: "d", Content: "c", Metadata: md, Status: models.SkillStatusActive, Kind: models.SkillKindAtomic}
	if err := store.Create(ctx, target); err != nil {
		t.Fatalf("create target skill: %v", err)
	}

	// dependent depends on target via TWO distinct relation types --
	// legal under the widened (skill_id, depends_on, relation_type) PK
	// (migrations/002_granularity.up.sql).
	dependent := &models.Skill{
		Name: "dupdep.dependent", Version: "1.0.0", Title: "Dependent", Description: "d", Content: "c",
		Metadata: md, Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
		Dependencies: []models.SkillDependency{
			{DependsOn: target.ID, RelationType: models.DepTypeRequires},
			{DependsOn: target.ID, RelationType: models.DepTypeRecommends},
		},
	}
	if err := store.Create(ctx, dependent); err != nil {
		t.Fatalf("create dependent skill: %v", err)
	}

	outDir := t.TempDir()
	if _, _, err := Generate(ctx, store, outDir, DefaultConfig()); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	targetPage := mustReadFile(t, filepath.Join(outDir, "skill", "dupdep_target.md"))
	depLine := "- [`dupdep.dependent`](dupdep_dependent.md)\n"
	if count := strings.Count(targetPage, depLine); count != 1 {
		t.Errorf("dupdep_target.md's Dependents section should render dupdep.dependent exactly ONCE "+
			"(it reaches target via TWO relation types, requires+recommends), got %d occurrences of %q:\n%s",
			count, depLine, targetPage)
	}

	// Symmetric check: the forward Dependencies direction must NOT be
	// deduplicated -- the SAME target must appear under BOTH canonical
	// relation-type subsections on the dependent's OWN detail page.
	dependentPage := mustReadFile(t, filepath.Join(outDir, "skill", "dupdep_dependent.md"))
	idxRequires := strings.Index(dependentPage, "### Requires")
	idxRecommends := strings.Index(dependentPage, "### Recommends")
	if idxRequires < 0 || idxRecommends < 0 {
		t.Fatalf("dupdep_dependent.md missing one or both expected relation-type subsections:\n%s", dependentPage)
	}
	targetLine := "- [`dupdep.target`](dupdep_target.md)\n"
	if count := strings.Count(dependentPage, targetLine); count != 2 {
		t.Errorf("dupdep_dependent.md should render dupdep.target ONCE under \"### Requires\" AND ONCE "+
			"under \"### Recommends\" (2 distinct relation-type edges, not a duplicate of one) -- got %d "+
			"occurrences of %q:\n%s", count, targetLine, dependentPage)
	}
}
