package skill

// G07 — TOML dependency + resource round-trip integrity (live-DB).
//
// Register: GAPS_AND_RISKS_REGISTER.md G07 ("TOML/JSON dependency+resource
// round-trip is broken (edges silently dropped on import)").
// Design:   research/g06_g07_skilltree_dag_design.md §2 (the acceptance oracle
//           §2.3: edge preservation, resource preservation, no silent loss).
//
// §11.4.115 RED-first: these cases MUST fail on the pre-fix code — ExportToTOML
// only emitted requires/extends/recommends (dropping composes/related_to/
// alternative_to), and ImportFromTOML only resolved requires/extends/recommends
// (dropping the same on the way back in). A `composes`/`related_to` edge
// therefore did NOT survive an export->import round-trip, and importing a TOML
// whose composes target is absent silently created a skill with the edge
// missing instead of failing. The GREEN post-fix behaviour is asserted here.
//
// Gated on the same SKILL_SYSTEM_TEST_DB_* contract as the rest of this
// package's live-DB suite (skillSkipIfNoTestDB); absent a configured
// PostgreSQL it honestly t.Skip()s (§11.4.3/§11.4.27).

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/models"
)

// g07NewLiveStore spins up a throwaway migrated DB + Store for a G07 case.
func g07NewLiveStore(t *testing.T) (context.Context, *Store, func()) {
	t.Helper()
	admin, ok := skillSkipIfNoTestDB(t)
	if !ok {
		return nil, nil, func() {}
	}
	ctx := context.Background()

	dbCfg, cleanupDB := skillCreateThrowawayDB(t, admin)
	pool, err := db.New(dbCfg)
	if err != nil {
		cleanupDB()
		t.Fatalf("db.New(dbCfg): %v", err)
	}
	if err := db.Migrate(ctx, pool, realMigrationsDirFromSkillPkg); err != nil {
		pool.Close()
		cleanupDB()
		t.Fatalf("db.Migrate (full real migrations dir): %v", err)
	}
	cleanup := func() {
		pool.Close()
		cleanupDB()
	}
	return ctx, NewStore(pool), cleanup
}

// g07ImportLeaf imports an atomic, dependency-free skill (a valid dep target).
func g07ImportLeaf(t *testing.T, ctx context.Context, store *Store, name string) {
	t.Helper()
	body := `
[skill]
name = "` + name + `"
version = "0.1.0"
title = "leaf ` + name + `"
content = "leaf content for ` + name + `"

[skill.dependencies]
requires = []
`
	if _, err := store.ImportFromTOML(ctx, []byte(body)); err != nil {
		t.Fatalf("import leaf %q: %v", name, err)
	}
}

// TestG07RoundTrip_EdgeNamesAndResourcesSurvive is the core acceptance oracle:
// a skill carrying edges of every hard-closure + advisory type plus a resource,
// exported to TOML and re-imported, must come back with every edge NAME and the
// resource intact (research/g06_g07_skilltree_dag_design.md §2.3 (1)+(2)).
func TestG07RoundTrip_EdgeNamesAndResourcesSurvive(t *testing.T) {
	ctx, store, cleanup := g07NewLiveStore(t)
	if store == nil {
		return
	}
	defer cleanup()

	const (
		leafLang = "rt.dep.lang"
		leafUI   = "rt.dep.ui"
		leafDoc  = "rt.dep.doc"
		srcName  = "rt.src"
		rtName   = "rt.src.roundtrip"
		resURL   = "https://example.test/g07/resource"
	)

	// Dependency targets first, so hard-closure edges resolve.
	g07ImportLeaf(t, ctx, store, leafLang)
	g07ImportLeaf(t, ctx, store, leafUI)
	g07ImportLeaf(t, ctx, store, leafDoc)

	// Original source skill: kind=composite, one requires edge + a resource,
	// built through the (already-working) import path so it is well-formed
	// regardless of the export/import fix under test.
	srcTOML := `
[skill]
name = "` + srcName + `"
version = "0.2.0"
title = "round-trip source"
description = "carries every typed edge + a resource"
content = "source content"
kind = "composite"

[skill.dependencies]
requires = ["` + leafLang + `"]

[[skill.resources]]
url = "` + resURL + `"
title = "G07 resource"
resource_type = "official-doc"
`
	src, err := store.ImportFromTOML(ctx, []byte(srcTOML))
	if err != nil {
		t.Fatalf("import source skill: %v", err)
	}

	// Attach a hard-closure (composes) + an advisory (related_to) edge via the
	// store's own edge API — both are valid relation types post-002 migration.
	ui, err := store.GetByName(ctx, leafUI)
	if err != nil {
		t.Fatalf("GetByName(%q): %v", leafUI, err)
	}
	doc, err := store.GetByName(ctx, leafDoc)
	if err != nil {
		t.Fatalf("GetByName(%q): %v", leafDoc, err)
	}
	if err := store.AddDependency(ctx, src.ID, ui.ID, models.DepTypeComposes); err != nil {
		t.Fatalf("AddDependency composes: %v", err)
	}
	if err := store.AddDependency(ctx, src.ID, doc.ID, models.DepTypeRelatedTo); err != nil {
		t.Fatalf("AddDependency related_to: %v", err)
	}

	// Export the fully-formed source.
	exported, err := store.ExportToTOML(ctx, srcName)
	if err != nil {
		t.Fatalf("ExportToTOML(%q): %v", srcName, err)
	}

	// Re-import under a fresh name so it does not collide with the original.
	// Round-trip the bytes through the same codec (never a brittle text edit).
	var wrapper models.TOMLSkillWrapper
	if err := toml.Unmarshal(exported, &wrapper); err != nil {
		t.Fatalf("decode exported TOML: %v", err)
	}
	wrapper.Skill.Name = rtName
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(wrapper); err != nil {
		t.Fatalf("re-encode renamed TOML: %v", err)
	}
	if _, err := store.ImportFromTOML(ctx, buf.Bytes()); err != nil {
		t.Fatalf("re-import round-trip skill: %v", err)
	}

	// Read the re-imported skill and assert the full edge + resource identity.
	rt, err := store.GetByName(ctx, rtName)
	if err != nil {
		t.Fatalf("GetByName(%q): %v", rtName, err)
	}

	if rt.Kind != models.SkillKindComposite {
		t.Errorf("round-trip kind = %q, want %q", rt.Kind, models.SkillKindComposite)
	}

	relByName := make(map[string]models.DependencyType, len(rt.Dependencies))
	for _, d := range rt.Dependencies {
		relByName[d.DependsOnName] = d.RelationType
	}

	wantEdges := map[string]models.DependencyType{
		leafLang: models.DepTypeRequires,  // survived pre-fix
		leafUI:   models.DepTypeComposes,  // RED pre-fix: dropped by export+import
		leafDoc:  models.DepTypeRelatedTo, // RED pre-fix: dropped by export+import
	}
	for name, wantRel := range wantEdges {
		gotRel, ok := relByName[name]
		if !ok {
			t.Errorf("round-trip lost the edge to %q (want relation_type=%q); surviving edges: %v",
				name, wantRel, relByName)
			continue
		}
		if gotRel != wantRel {
			t.Errorf("round-trip edge to %q has relation_type=%q, want %q", name, gotRel, wantRel)
		}
	}

	if len(rt.Resources) != 1 {
		t.Fatalf("round-trip skill has %d resources, want 1", len(rt.Resources))
	}
	if rt.Resources[0].URL != resURL {
		t.Errorf("round-trip resource URL = %q, want %q", rt.Resources[0].URL, resURL)
	}
}

// TestG07Import_MissingComposesTarget_HardErrors is the no-silent-loss guard
// (design §2.3 (4)): a `composes` (hard-closure) edge whose target skill does
// not exist must abort the import with ErrDependencyNotFound — never a silent
// partial import that creates the skill with the edge missing.
func TestG07Import_MissingComposesTarget_HardErrors(t *testing.T) {
	ctx, store, cleanup := g07NewLiveStore(t)
	if store == nil {
		return
	}
	defer cleanup()

	body := `
[skill]
name = "rt.missing.composes"
version = "0.1.0"
title = "missing composes target"
content = "content"
kind = "composite"

[skill.dependencies]
composes = ["rt.does.not.exist"]
`
	_, err := store.ImportFromTOML(ctx, []byte(body))
	if err == nil {
		t.Fatalf("import with a missing composes target succeeded; want a hard error (silent partial import is forbidden)")
	}
	if !errors.Is(err, ErrDependencyNotFound) {
		t.Errorf("import error = %v, want it to wrap ErrDependencyNotFound", err)
	}
	// And the skill must NOT have been partially created.
	if _, gerr := store.GetByName(ctx, "rt.missing.composes"); gerr == nil {
		t.Errorf("skill was partially created despite the failed import (transaction not rolled back)")
	}
}
