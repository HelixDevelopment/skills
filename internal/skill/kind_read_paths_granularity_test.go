package skill

// N6 (Fable code-review remediation, P1.T1): the P1.T1 granularity migration
// added `kind` to the SELECT/scan lists of several previously-untested read
// paths -- GetDependencyTree/GetDependents/GetAllDependencies (graph.go) and
// Search/VectorSearch (store.go) -- but none of them had ever been run
// against a live database to confirm the modified CTEs/queries actually scan
// `kind` without error. This file exercises all five against the live
// SKILL_SYSTEM_TEST_DB_* database (same contract as
// migration_granularity_test.go in this package), using skills whose Kind is
// deliberately NON-default (composite/umbrella) so a silently-dropped or
// mis-scanned `kind` column would surface as a wrong value, not just a
// coincidental zero-value match against the 'atomic' DEFAULT.
//
// Each function is its own subtest so one finding never masks or blocks the
// others (§11.4.6/§11.4.194 -- an aggregate PASS/FAIL would not pinpoint
// which of the five queries actually broke).
//
// Formerly-captured, now-fixed finding: Search's primary query depends on
// the pg_trgm `%` similarity operator (`s.name % $1`, `similarity(...)`).
// migrations/001_initial.up.sql only creates `vector` and `uuid-ossp`, and
// migrations/002_granularity.up.sql creates no extension either, so on any
// database migrated with only 001+002 Search() errored with `function
// similarity(text, unknown) does not exist (SQLSTATE 42883)` before it ever
// reached the `s.kind` column in its SELECT list. This is now fixed by
// migrations/003_pg_trgm.up.sql (P1.T1 N6 remediation), which
// `CREATE EXTENSION IF NOT EXISTS pg_trgm` (plus the two supporting GIN
// trigram indexes idx_skills_name_trgm/idx_skills_title_trgm) on the SAME
// real migrations directory this test applies
// (realMigrationsDirFromSkillPkg) -- so Search's `%`/`similarity(...)`
// dependency is satisfied on any freshly migrated database, this throwaway
// DB included, and the TestP1T1N6_KindAwareReadPathsWorkLive/Search subtest
// below asserts it outright via t.Fatalf rather than honestly-skipping a
// gap that no longer exists (§11.4.6/§11.4.115/§11.4.135 -- a permanent
// regression guard on 003's effect, not a documented-and-tolerated gap).
// internal/db/migrations_granularity_test.go additionally asserts, at the
// db-package/migration-shape layer, that the pg_trgm extension AND both GIN
// trigram indexes exist after Migrate applies the real migrations dir.
//
// A second, distinct captured finding surfaced the same way: VectorSearch's
// `ORDER BY embedding <=> $1` is served by the HNSW index
// (idx_skills_embedding), which never returns a NULL-embedding OR an
// all-zero-embedding row (cosine distance is undefined at zero magnitude) --
// and store.Create never sets the embedding column -- so VectorSearch
// returns ZERO rows for any skill that has not had a real, non-zero
// embedding separately written. The VectorSearch subtest below works around
// this (by writing a real non-zero embedding directly via SQL before
// querying) so it can still confirm the s.kind scan works, and documents the
// gap inline rather than silently depending on undocumented setup.
import (
	"context"
	"testing"

	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
)

func TestP1T1N6_KindAwareReadPathsWorkLive(t *testing.T) {
	admin, ok := skillSkipIfNoTestDB(t)
	if !ok {
		return
	}
	ctx := context.Background()

	dbCfg, cleanup := skillCreateThrowawayDB(t, admin)
	defer cleanup()

	pool, err := db.New(dbCfg)
	if err != nil {
		t.Fatalf("db.New(dbCfg): %v", err)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool, realMigrationsDirFromSkillPkg); err != nil {
		t.Fatalf("db.Migrate (full real migrations dir): %v", err)
	}

	store := NewStore(pool)

	child := &models.Skill{
		Name:    "p1t1.n6.child",
		Title:   "N6 Child",
		Content: "child content",
		Status:  models.SkillStatusActive,
		Kind:    models.SkillKindComposite,
	}
	if err := store.Create(ctx, child); err != nil {
		t.Fatalf("create child skill: %v", err)
	}

	root := &models.Skill{
		Name:    "p1t1.n6.root",
		Title:   "N6 Root",
		Content: "root content",
		Status:  models.SkillStatusActive,
		Kind:    models.SkillKindUmbrella,
		Dependencies: []models.SkillDependency{
			{DependsOn: child.ID, RelationType: models.DepTypeRequires},
		},
	}
	if err := store.Create(ctx, root); err != nil {
		t.Fatalf("create root skill with requires edge: %v", err)
	}

	t.Run("GetDependencyTree", func(t *testing.T) {
		tree, err := store.GetDependencyTree(ctx, "p1t1.n6.root", 5)
		if err != nil {
			t.Fatalf("GetDependencyTree: %v", err)
		}
		if tree.Skill.Kind != models.SkillKindUmbrella {
			t.Errorf("root Kind = %q, want %q", tree.Skill.Kind, models.SkillKindUmbrella)
		}
		if len(tree.Children) != 1 {
			t.Fatalf("root has %d children, want 1", len(tree.Children))
		}
		if tree.Children[0].Skill.Kind != models.SkillKindComposite {
			t.Errorf("child Kind = %q, want %q", tree.Children[0].Skill.Kind, models.SkillKindComposite)
		}
	})

	t.Run("GetDependents", func(t *testing.T) {
		dependents, err := store.GetDependents(ctx, child.ID)
		if err != nil {
			t.Fatalf("GetDependents: %v", err)
		}
		found := false
		for _, d := range dependents {
			if d.Name == "p1t1.n6.root" {
				found = true
				if d.Kind != models.SkillKindUmbrella {
					t.Errorf("root Kind = %q, want %q", d.Kind, models.SkillKindUmbrella)
				}
			}
		}
		if !found {
			t.Error("GetDependents(child.ID) did not include p1t1.n6.root")
		}
	})

	t.Run("GetAllDependencies", func(t *testing.T) {
		allDeps, err := store.GetAllDependencies(ctx, root.ID)
		if err != nil {
			t.Fatalf("GetAllDependencies: %v", err)
		}
		found := false
		for _, d := range allDeps {
			if d.Name == "p1t1.n6.child" {
				found = true
				if d.Kind != models.SkillKindComposite {
					t.Errorf("child Kind = %q, want %q", d.Kind, models.SkillKindComposite)
				}
			}
		}
		if !found {
			t.Error("GetAllDependencies(root.ID) did not include p1t1.n6.child")
		}
	})

	t.Run("Search", func(t *testing.T) {
		results, err := store.Search(ctx, "p1t1.n6.root", 10)
		if err != nil {
			// migrations/003_pg_trgm.up.sql (P1.T1 N6 remediation) now
			// CREATE EXTENSIONs pg_trgm on the real migrations dir this test
			// runs against (realMigrationsDirFromSkillPkg), so Search's
			// `similarity(...)` / `%` operator dependency is satisfied on
			// any freshly migrated database -- there is no longer a
			// pre-existing gap to honestly SKIP around (§11.4.6): any
			// Search error, including a similarity/operator-does-not-exist
			// regression of 003, now genuinely FAILs this subtest rather
			// than being masked as a skip.
			t.Fatalf("Search: %v", err)
		}
		found := false
		for _, r := range results {
			if r.Skill.Name == "p1t1.n6.root" {
				found = true
				if r.Skill.Kind != models.SkillKindUmbrella {
					t.Errorf("result Kind = %q, want %q", r.Skill.Kind, models.SkillKindUmbrella)
				}
			}
		}
		if !found {
			t.Error(`Search("p1t1.n6.root") did not return p1t1.n6.root`)
		}
	})

	t.Run("VectorSearch", func(t *testing.T) {
		// store.Create's INSERT never sets the embedding column (see
		// store.go:Create), so child/root above both carry a NULL
		// embedding. Captured finding: pgvector's HNSW index
		// (idx_skills_embedding, migrations/001_initial.up.sql) never
		// includes NULL-embedding rows in its graph, so an index-driven
		// `ORDER BY embedding <=> $1 LIMIT n` scan returns ZERO rows for a
		// skill that has never had a real embedding written -- proven live
		// (against a standalone throwaway single-column HNSW table, not
		// this project's schema) via psql: `SELECT ... FROM t ORDER BY
		// embedding <=> '<query>' LIMIT 5` returned 0 rows for a stored
		// NULL-embedding row, and separately for a stored ALL-ZERO
		// embedding row (cosine distance is undefined at zero magnitude --
		// `vector_cosine_ops` cannot place a zero vector in the HNSW graph
		// either) -- but DID return a row once its STORED embedding was a
		// non-zero vector (dist=0 against itself), regardless of whether
		// the QUERY vector was zero or non-zero. That is a genuine, real
		// gap (VectorSearch is unusable for any skill lacking a
		// separately-populated, non-zero embedding) but it is, like
		// Search's pg_trgm gap above, NOT a kind-scanning regression and
		// out of this remediation's assigned scope. To still confirm what
		// N6 asks -- that the modified SELECT/scan list (which includes
		// s.kind) works live -- give both rows a real NON-ZERO embedding
		// directly via SQL before querying, which is sufficient for the
		// HNSW index to return them.
		const embeddingDim = 768 // migrations/001_initial.up.sql: embedding vector(768)
		nonZeroEmbedding := make([]float32, embeddingDim)
		for i := range nonZeroEmbedding {
			nonZeroEmbedding[i] = 0.001
		}
		vec := pgvector.NewVector(nonZeroEmbedding)
		if _, err := pool.Exec(ctx, `UPDATE skills SET embedding = $1 WHERE id = ANY($2)`, vec, []uuid.UUID{child.ID, root.ID}); err != nil {
			t.Fatalf("UPDATE skills SET embedding (test setup): %v", err)
		}

		vresults, err := store.VectorSearch(ctx, nonZeroEmbedding, 10)
		if err != nil {
			t.Fatalf("VectorSearch: %v", err)
		}
		if len(vresults) == 0 {
			t.Fatal("VectorSearch returned 0 results, want at least the 2 skills just given a real embedding")
		}
		foundChild, foundRoot := false, false
		for _, r := range vresults {
			if r.Skill.Kind == "" {
				t.Errorf("result %q has empty Kind (kind column scan failed)", r.Skill.Name)
			}
			switch r.Skill.Name {
			case "p1t1.n6.child":
				foundChild = true
				if r.Skill.Kind != models.SkillKindComposite {
					t.Errorf("child Kind = %q, want %q", r.Skill.Kind, models.SkillKindComposite)
				}
			case "p1t1.n6.root":
				foundRoot = true
				if r.Skill.Kind != models.SkillKindUmbrella {
					t.Errorf("root Kind = %q, want %q", r.Skill.Kind, models.SkillKindUmbrella)
				}
			}
		}
		if !foundChild || !foundRoot {
			t.Errorf("VectorSearch results = %+v, want both p1t1.n6.child and p1t1.n6.root", vresults)
		}
	})
}
