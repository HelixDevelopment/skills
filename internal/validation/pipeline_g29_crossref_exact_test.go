package validation

// Fable code-review remediation, finding 1 (BLOCKING): Pipeline.CrossReference
// used the FUZZY hybrid Store.Search for both its dependency-existence check
// (pipeline.go, `p.store.Search(ctx, dep.DependsOnName, 1)`) and its
// naming-conflict check (`p.store.Search(ctx, s.Name, 5)`). Once a query-side
// embedder is wired (§G29), Search's weighted Reciprocal Rank Fusion can rank
// ANY embedded row above an exact-name trigram match: a rank-0 vector hit
// scores vectorRRFWeight/(rrfK+1) = 1.0/61 ~= 0.016393, while an exact-name
// trigram hit tops out at trigramRRFWeight/(rrfK+1) = 0.9/61 ~= 0.014754 --
// strictly lower. With Search's small result-set limit (1 for the existence
// check), that ordering difference is enough to evict the exact match from the
// returned slice entirely, so CrossReference wrongly reports "dependency not
// found" for a dependency that DOES exist -- purely because of an UNRELATED
// skill elsewhere in the graph happening to carry a populated embedding.
//
// The fix (pipeline.go) replaces both Search calls with Store.GetByName, an
// exact `WHERE name = $1` lookup whose answer never depends on embedding
// state. These are the §11.4.115 RED-first regression guards: they reproduce
// the exact math above against a REAL live database (same
// SKILL_SYSTEM_TEST_DB_* harness as internal/skill's own G29 suite) and FAIL
// on the pre-fix fuzzy-Search CrossReference (a §1.1 mutation reverting
// CrossReference to Search makes them FAIL again).

import (
	"context"
	"testing"

	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/HelixDevelopment/skills/internal/skill"
	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"go.uber.org/zap"
)

const g29xrDim = 768 // migrations/001_initial.up.sql: embedding vector(768)

// g29xrEmbedder is a deterministic db.Embedder double that returns the SAME
// fixed vector for every input text, regardless of query content -- so
// whichever skill's embedding column happens to equal that vector is
// GUARANTEED to be the nearest (indeed, only) vector-KNN neighbour for any
// query issued through this embedder. This is the legitimate unit seam
// permitted at the boundary by §11.4.27: the DATABASE and pgvector KNN
// execution are REAL, only the embedding model call is stubbed.
type g29xrEmbedder struct{ vec []float32 }

func (e *g29xrEmbedder) Dimensions() int { return g29xrDim }
func (e *g29xrEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = e.vec
	}
	return out, nil
}

// g29xrVec builds a deterministic non-zero 768-d vector with a single
// dominant "hot" index, matching the convention used by
// internal/skill/hybrid_search_g29_test.go's own g29Vec.
func g29xrVec(hot int) []float32 {
	v := make([]float32, g29xrDim)
	for i := range v {
		v[i] = 0.01
	}
	v[hot] = 0.99
	return v
}

func g29xrSetEmbedding(t *testing.T, ctx context.Context, pool *db.Pool, id uuid.UUID, vec []float32) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE skills SET embedding = $1 WHERE id = $2`, pgvector.NewVector(vec), id); err != nil {
		t.Fatalf("UPDATE skills SET embedding (test setup): %v", err)
	}
}

// g29xrNewLivePipeline spins up a throwaway migrated DB and returns a
// live *skill.Store, its pool, a Pipeline wired to that store, and a cleanup.
func g29xrNewLivePipeline(t *testing.T) (context.Context, *skill.Store, *db.Pool, *Pipeline, func()) {
	t.Helper()
	admin, ok := validationSkipIfNoTestDB(t)
	if !ok {
		return nil, nil, nil, nil, nil
	}
	ctx := context.Background()
	pool, cleanup := validationCreateThrowawayDB(t, admin)
	store := skill.NewStore(pool)
	p := NewPipeline(store, config.ValidationConfig{Enabled: true, ApprovalThreshold: 2}, zap.NewNop())
	return ctx, store, pool, p, cleanup
}

// TestG29_CrossReference_DependencyExists_NotDemotedByUnrelatedEmbedding is the
// finding-1 register's literal reproduction recipe: a fixedEmbedder + one
// embedded UNRELATED row + one unembedded exact-name target. It proves the
// dependency-existence check finds the target regardless of an unrelated
// skill's embedding state.
func TestG29_CrossReference_DependencyExists_NotDemotedByUnrelatedEmbedding(t *testing.T) {
	ctx, store, pool, p, cleanup := g29xrNewLivePipeline(t)
	if store == nil {
		return // skipped: no test DB
	}
	defer cleanup()

	// The embedded, semantically/lexically UNRELATED row. Its only role is to
	// occupy vector-leg rank 0 for EVERY query (g29xrEmbedder ignores query
	// text and always returns the same vector), regardless of what is asked.
	unrelated := &models.Skill{
		Name: "g29.xref.unrelated.embedded", Title: "Completely unrelated skill",
		Description: "shares no token with the dependency name", Content: "x",
		Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
	}
	if err := store.Create(ctx, unrelated); err != nil {
		t.Fatalf("create unrelated skill: %v", err)
	}
	fixedVec := g29xrVec(3)
	g29xrSetEmbedding(t, ctx, pool, unrelated.ID, fixedVec)

	// The exact-name dependency TARGET: genuinely exists, but NEVER gets an
	// embedding (store.Create never sets one) -- exactly the ordinary,
	// partially-populated production state.
	target := &models.Skill{
		Name: "g29.xref.target.dependency", Title: "Real dependency target",
		Description: "the dependency that genuinely exists", Content: "y",
		Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
	}
	if err := store.Create(ctx, target); err != nil {
		t.Fatalf("create target skill: %v", err)
	}

	// Wire the embedder AFTER both skills exist (mirrors production: the
	// embedder is wired once at construction, well before any query runs).
	store.WithEmbedder(&g29xrEmbedder{vec: fixedVec})

	subject := &models.Skill{
		ID:   uuid.New(),
		Name: "g29.xref.subject.draft",
		Dependencies: []models.SkillDependency{
			{DependsOn: target.ID, DependsOnName: target.Name, RelationType: models.DepTypeRequires},
		},
	}

	if err := p.CrossReference(ctx, subject); err != nil {
		// RED on pre-fix (fuzzy Search(dep.DependsOnName, 1)): the unrelated
		// embedded skill's vector-leg rank-0 RRF contribution (1.0/61 ~=
		// 0.016393) OUTSCORES the target's exact-name trigram-leg rank-0 RRF
		// contribution (0.9/61 ~= 0.014754), so at limit=1 ONLY the unrelated
		// skill survives the fuse-and-cut and the genuinely-existing target is
		// reported "not found in knowledge graph".
		t.Fatalf("CrossReference reported dependency %q missing even though it "+
			"genuinely exists; got error: %v (an exact-name dependency lookup "+
			"must not depend on an UNRELATED skill's embedding state)", target.Name, err)
	}
}

// TestG29_CrossReference_NamingConflict_NotDemotedByEmbeddedDecoys is the
// naming-conflict-check half of finding 1: with enough embedded decoy rows to
// outrank the exact-name conflicting skill under the SAME weighted-RRF fusion
// (this time at the conflict check's limit=5), the fuzzy-Search-based
// pre-fix check evicts the genuinely-conflicting row from its result set and
// wrongly reports "no conflict". Five decoys are used because limit=5 needs
// five higher-ranked competitors to force the exact match past the cut,
// unlike the existence check's limit=1 (where a single decoy already
// suffices) -- proving the SAME class of bug independently at this call site.
func TestG29_CrossReference_NamingConflict_NotDemotedByEmbeddedDecoys(t *testing.T) {
	ctx, store, pool, p, cleanup := g29xrNewLivePipeline(t)
	if store == nil {
		return // skipped: no test DB
	}
	defer cleanup()

	const conflictingName = "g29.xref.conflict.existing"

	// The pre-existing skill that a fresh draft with the SAME name will
	// collide with.
	existing := &models.Skill{
		Name: conflictingName, Title: "Existing skill owning this name",
		Description: "already registered under this exact name", Content: "z",
		Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
	}
	if err := store.Create(ctx, existing); err != nil {
		t.Fatalf("create existing skill: %v", err)
	}

	// Five embedded decoys, each a DISTINCT vector so each is independently a
	// rank-0 hit for its own dedicated query -- but our embedder always
	// returns decoyVec for ANY query, so all five occupy vector-leg ranks
	// 0..4 simultaneously (RRF still credits each its own rank contribution),
	// collectively outranking the single exact-name trigram hit at limit=5.
	decoyVec := g29xrVec(11)
	for i := 0; i < 5; i++ {
		decoy := &models.Skill{
			Name:  "g29.xref.decoy." + uuid.New().String(),
			Title: "Unrelated embedded decoy",
			// Deliberately shares NO token with conflictingName so it never
			// contaminates the trigram leg.
			Description: "zzz-decoy-zzz", Content: "d",
			Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
		}
		if err := store.Create(ctx, decoy); err != nil {
			t.Fatalf("create decoy skill %d: %v", i, err)
		}
		g29xrSetEmbedding(t, ctx, pool, decoy.ID, decoyVec)
	}

	store.WithEmbedder(&g29xrEmbedder{vec: decoyVec})

	// A fresh draft with a DIFFERENT ID claiming the SAME exact name: a
	// genuine naming conflict.
	subject := &models.Skill{ID: uuid.New(), Name: conflictingName}

	err := p.CrossReference(ctx, subject)
	if err == nil {
		// RED on pre-fix (fuzzy Search(s.Name, 5)): five embedded decoys each
		// score 1.0/(60+rank+1) for rank in 0..4, all five OUTSCORING the
		// single exact-name trigram hit's 0.9/61 -- so the genuinely-conflicting
		// `existing` row is evicted from the top-5 fused result and
		// CrossReference wrongly returns nil (no conflict detected).
		t.Fatalf("CrossReference did not detect the naming conflict with %q "+
			"(existing skill ID %s, draft ID %s); an exact-name conflict check "+
			"must not depend on unrelated skills' embedding state", conflictingName, existing.ID, subject.ID)
	}

	// Sanity/robustness companion: the SAME existing skill re-validated under
	// its OWN ID must NOT be flagged as a conflict with itself.
	self := &models.Skill{ID: existing.ID, Name: conflictingName}
	if err := p.CrossReference(ctx, self); err != nil {
		t.Errorf("CrossReference flagged a skill as conflicting with itself (same ID, same name): %v", err)
	}
}
