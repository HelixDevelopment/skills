package skill

// G29 (research/GAPS_AND_RISKS_REGISTER.md §G29): Store.Search advertised a
// "hybrid vector search" in its doc-comment but its body was pg_trgm/ILIKE-only
// and never embedded the query -- a §11.4 code-layer doc-bluff -- while
// Store.VectorSearch (the real pgvector KNN path) had ZERO callers, i.e. the
// flagship semantic-search path was dead (§11.4.124). The fix wires VectorSearch
// into Search: with a query-side embedder configured, Search embeds the query,
// runs cosine-KNN + trigram, and fuses them with weighted Reciprocal Rank Fusion.
//
// These are the §11.4.115 RED-first regression guards for that fix, run against
// the live SKILL_SYSTEM_TEST_DB_* database (same harness contract as
// kind_read_paths_granularity_test.go / migration_granularity_test.go in this
// package). They FAIL on the pre-fix keyword-only Search (which ignores the
// embedder and never returns a non-substring skill) and PASS on the hybrid Search
// -- so a §1.1 mutation reverting Search to keyword-only makes them FAIL again.
//
// The embedder is a small DETERMINISTIC in-test double (fixedEmbedder) that maps
// a known query text to a known 768-d vector; that same vector is written to the
// candidate skill's embedding column via SQL (exactly as the existing
// VectorSearch subtest populates embeddings -- store.Create never sets the column,
// so a real non-zero embedding must be written for pgvector's HNSW index to
// return the row). The DATABASE and pgvector KNN are REAL; only the embedding
// model is stubbed, which is the legitimate unit seam for proving the store's
// hybrid fusion logic without a network call (§11.4.27 permits doubles at the
// unit boundary; the live pgvector KNN + fusion is exercised for real).

import (
	"context"
	"testing"

	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
)

const g29EmbeddingDim = 768 // migrations/001_initial.up.sql: embedding vector(768)

// fixedEmbedder is a deterministic db.Embedder double: it returns a pinned
// vector for each pre-registered query text (and a fixed default otherwise), so
// the test controls the query embedding exactly and can store an equal vector on
// the target skill to make it the nearest KNN neighbour.
type fixedEmbedder struct {
	byText map[string][]float32
	def    []float32
}

func (e *fixedEmbedder) Dimensions() int { return g29EmbeddingDim }

func (e *fixedEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		if v, ok := e.byText[t]; ok {
			out[i] = v
			continue
		}
		out[i] = e.def
	}
	return out, nil
}

// g29Vec builds a deterministic non-zero 768-d vector whose "direction" is set by
// a single dominant index, so two vectors built with different hot indices are
// clearly distinct (and neither is the zero vector, which pgvector's cosine HNSW
// index cannot place).
func g29Vec(hot int) []float32 {
	v := make([]float32, g29EmbeddingDim)
	for i := range v {
		v[i] = 0.01
	}
	v[hot] = 0.99
	return v
}

// g29NewLiveStore spins up a throwaway migrated DB and returns a Store plus a
// cleanup. Mirrors the harness used by the other live tests in this package.
func g29NewLiveStore(t *testing.T) (context.Context, *Store, *db.Pool, func()) {
	t.Helper()
	admin, ok := skillSkipIfNoTestDB(t)
	if !ok {
		return nil, nil, nil, nil
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
	return ctx, NewStore(pool), pool, cleanup
}

func g29SetEmbedding(t *testing.T, ctx context.Context, pool *db.Pool, id uuid.UUID, vec []float32) {
	t.Helper()
	if _, err := pool.Exec(ctx, `UPDATE skills SET embedding = $1 WHERE id = $2`, pgvector.NewVector(vec), id); err != nil {
		t.Fatalf("UPDATE skills SET embedding (test setup): %v", err)
	}
}

// TestG29_HybridSearch_SemanticRecall proves the core anti-bluff fix: a query
// that lexically matches NOTHING (no substring, no trigram similarity) still
// surfaces a semantically-near skill through the vector-KNN leg. The pre-fix
// keyword-only Search returns zero rows for such a query, so this FAILs before
// the fix and PASSes after (and after a §1.1 revert-to-keyword-only mutation it
// FAILs again).
func TestG29_HybridSearch_SemanticRecall(t *testing.T) {
	ctx, store, pool, cleanup := g29NewLiveStore(t)
	if store == nil {
		return // skipped: no test DB
	}
	defer cleanup()

	// A purely lexical skill: it will NOT match the semantic probe query.
	lexical := &models.Skill{
		Name: "g29.lexical.widgets", Title: "Widget assembly manual",
		Description: "how to assemble widgets", Content: "widgets",
		Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
	}
	if err := store.Create(ctx, lexical); err != nil {
		t.Fatalf("create lexical skill: %v", err)
	}
	// The semantically-near skill: its text shares NO token with the probe query,
	// so only the vector leg can retrieve it.
	semantic := &models.Skill{
		Name: "g29.semantic.consensus", Title: "Distributed consensus algorithms",
		Description: "raft and paxos", Content: "consensus",
		Status: models.SkillStatusActive, Kind: models.SkillKindComposite,
	}
	if err := store.Create(ctx, semantic); err != nil {
		t.Fatalf("create semantic skill: %v", err)
	}

	// A query string that is a substring of NEITHER skill. Map it to a vector and
	// store that exact vector on the semantic skill so it is the nearest neighbour.
	const probe = "qzx-vector-only-probe"
	probeVec := g29Vec(7)
	g29SetEmbedding(t, ctx, pool, semantic.ID, probeVec)

	store.WithEmbedder(&fixedEmbedder{
		byText: map[string][]float32{probe: probeVec},
		def:    g29Vec(500), // unrelated default direction
	})

	results, err := store.Search(ctx, probe, 10)
	if err != nil {
		t.Fatalf("hybrid Search: %v", err)
	}

	found := false
	for _, r := range results {
		if r.Skill.Name == semantic.Name {
			found = true
			break
		}
	}
	if !found {
		// RED on pre-fix (and on a keyword-only §1.1 mutation): the probe query
		// has no lexical match, so keyword-only Search returns nothing and the
		// semantically-near skill is invisible -- the exact doc-bluff G29 fixes.
		t.Fatalf("hybrid Search(%q) did not return semantically-near skill %q via VectorSearch; results=%v (keyword-only search cannot surface a non-substring match)", probe, semantic.Name, names(results))
	}
	// A pure-semantic query yields only the vector hit, so it must rank first.
	if results[0].Skill.Name != semantic.Name {
		t.Errorf("semantic hit not ranked first: results[0]=%q, want %q; results=%v", results[0].Skill.Name, semantic.Name, names(results))
	}
}

// TestG29_HybridSearch_RanksAboveTrigramOnly is the register's literal
// requirement: with BOTH a trigram-only match and a semantically-near
// non-substring match present for the same query, the semantic match is
// retrieved AND ranks above the trigram-only one (weighted RRF favours the
// vector leg on a rank tie). Pre-fix, the semantic skill is absent entirely.
//
// Re-review remediation (NIT, post-G29): the semantic fixture's name is
// chosen to sort ALPHABETICALLY AFTER the trigram fixture's name ("g29.zsem…"
// > "g29.trig…"). Both fixtures land at rank 0 of their respective single
// list (sem ONLY in the vector list, trig ONLY in the trigram list -- see
// below), so their fused scores are exactly
// vectorRRFWeight/(rrfK+1) and trigramRRFWeight/(rrfK+1). fuseSearchResults'
// tiebreak on an exact score tie is ascending Skill.Name. With the ORIGINAL
// naming ("g29.sem.scheduling" < "g29.trig.orchestration") that tiebreak
// ALSO happened to rank sem before trig -- so a §1.1 mutation neutralizing
// vectorRRFWeight/trigramRRFWeight to equal values produced a tied score,
// fell through to the name tiebreak, and sem STILL ranked first: the test
// passed for the wrong reason, genuinely pinning only a weight INVERSION
// (vector < trigram), never a neutralization to EQUAL. Naming sem to sort
// AFTER trig flips the tiebreak's direction, so on a neutralized-to-equal
// mutation the tiebreak now (correctly) ranks trig first and idxSem < idxTrig
// fails -- the assertion is now genuinely pinned to the documented weight
// TILT (vectorRRFWeight > trigramRRFWeight), not a coincidental name order.
func TestG29_HybridSearch_RanksAboveTrigramOnly(t *testing.T) {
	ctx, store, pool, cleanup := g29NewLiveStore(t)
	if store == nil {
		return
	}
	defer cleanup()

	const query = "orchestration"

	// Trigram-only match: title contains the query substring; no embedding.
	trig := &models.Skill{
		Name: "g29.trig.orchestration", Title: "orchestration basics",
		Description: "intro to orchestration", Content: "x",
		Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
	}
	if err := store.Create(ctx, trig); err != nil {
		t.Fatalf("create trigram skill: %v", err)
	}
	// Semantic-only match: no "orchestration" substring anywhere; retrievable
	// ONLY via the vector leg. Named "g29.zsem…" (not "g29.sem…") so it sorts
	// ALPHABETICALLY AFTER trig's "g29.trig…" -- see the test's doc comment.
	sem := &models.Skill{
		Name: "g29.zsem.scheduling", Title: "Container scheduling systems",
		Description: "pods and nodes", Content: "y",
		Status: models.SkillStatusActive, Kind: models.SkillKindComposite,
	}
	if err := store.Create(ctx, sem); err != nil {
		t.Fatalf("create semantic skill: %v", err)
	}

	queryVec := g29Vec(11)
	g29SetEmbedding(t, ctx, pool, sem.ID, queryVec)

	store.WithEmbedder(&fixedEmbedder{
		byText: map[string][]float32{query: queryVec},
		def:    g29Vec(500),
	})

	results, err := store.Search(ctx, query, 10)
	if err != nil {
		t.Fatalf("hybrid Search: %v", err)
	}

	idxSem, idxTrig := -1, -1
	for i, r := range results {
		switch r.Skill.Name {
		case sem.Name:
			idxSem = i
		case trig.Name:
			idxTrig = i
		}
	}
	if idxSem < 0 {
		// RED on pre-fix / keyword-only mutation: the non-substring semantic
		// skill is never returned by keyword-only Search.
		t.Fatalf("hybrid Search(%q) did not return non-substring semantic skill %q; results=%v", query, sem.Name, names(results))
	}
	if idxTrig < 0 {
		t.Fatalf("hybrid Search(%q) dropped the trigram match %q (hybrid must UNION, not replace, the lexical leg); results=%v", query, trig.Name, names(results))
	}
	if idxSem >= idxTrig {
		t.Errorf("semantic match %q (idx %d) did not rank above trigram-only match %q (idx %d); results=%v", sem.Name, idxSem, trig.Name, idxTrig, names(results))
	}
}

// TestG29_HybridSearch_NullEmbeddingRow_SeqscanNoScanError is the F2 (BLOCKING
// code-review finding) regression guard, and the §11.4.194 multi-factor case the
// two tests above structurally cannot reach: they only ever exercise the
// HNSW-INDEX-SCAN plan, which silently skips NULL-embedding rows, so they never
// touch the NULL-embedding factor of Store.VectorSearch's KNN query.
//
// The defect: Store.VectorSearch's SQL had NO `WHERE s.embedding IS NOT NULL`
// guard (unlike the reference sibling internal/db/vector.go). skills.embedding is
// NULL until a separate population path runs (store.Create never sets it), so in
// the ordinary partially-/un-populated production state NULL-embedding rows exist.
// On the HNSW index-scan plan those NULLs are skipped and the bug is invisible;
// but the cost-based planner CAN pick a seqscan/top-N plan (small table, or a
// table with no usable index), and there `ORDER BY s.embedding <=> $1 LIMIT $2`
// sorts NULL distances LAST and, once LIMIT exceeds the non-NULL row count,
// RETURNS the NULL-embedding row with a NULL score. pgx v5 then errors
// `cannot scan NULL into *float64`. Because Store.Search deliberately does NOT
// mask a vector-leg error (store.go: "A KNN query error is a real internal
// fault"), that single NULL row turns EVERY hybrid Search into a HARD FAILURE and
// discards the already-computed trigram results -- correctness must not depend on
// a cost-based plan choice.
//
// To make the failing plan DETERMINISTIC (not left to the planner) this test
// DROPs the HNSW index in its own throwaway DB, forcing the exact seqscan/top-N
// path the finding names. Dropping the index is global to the throwaway DB, so it
// applies regardless of which pooled connection Store.Search lands on (a
// per-connection SET LOCAL/SET would not reliably reach the query's connection).
//
// RED (pre-fix, no WHERE guard): the vector-leg seqscan returns the NULL-embedding
// row, pgx errors "cannot scan NULL into *float64", Store.Search returns
// "hybrid search vector leg: ...", and the `if err != nil` assertion FAILs.
// GREEN (post-fix): the WHERE s.embedding IS NOT NULL guard excludes the NULL row
// from the vector leg, Search succeeds, the embedded skill surfaces via the vector
// leg, AND the NULL-embedding skill still surfaces via the trigram leg (its text
// matches) -- proving the fix is surgical (it removes the NULL row from the VECTOR
// leg only, it does not make the skill invisible). A §1.1 mutation deleting the
// WHERE clause reinstates the scan error and FAILs this test again.
func TestG29_HybridSearch_NullEmbeddingRow_SeqscanNoScanError(t *testing.T) {
	ctx, store, pool, cleanup := g29NewLiveStore(t)
	if store == nil {
		return // skipped: no test DB
	}
	defer cleanup()

	const query = "telemetry"

	// Embedded, semantically-near skill: text shares NO token with the query, so
	// it is retrievable ONLY via the vector leg.
	semantic := &models.Skill{
		Name: "g29.null.semantic.observability", Title: "Distributed tracing spans",
		Description: "spans and traces", Content: "z",
		Status: models.SkillStatusActive, Kind: models.SkillKindComposite,
	}
	if err := store.Create(ctx, semantic); err != nil {
		t.Fatalf("create semantic skill: %v", err)
	}

	// NULL-embedding skill: store.Create never sets embedding, so this row's
	// embedding is NULL. Its text MATCHES the query, so the trigram leg must still
	// surface it after the fix. Under the forced seqscan, the UNGUARDED vector leg
	// would return this exact row with a NULL score.
	nullrow := &models.Skill{
		Name: "g29.null.telemetry", Title: "telemetry", Description: "telemetry ingestion",
		Content: "telemetry", Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
	}
	if err := store.Create(ctx, nullrow); err != nil {
		t.Fatalf("create null-embedding skill: %v", err)
	}

	queryVec := g29Vec(23)
	g29SetEmbedding(t, ctx, pool, semantic.ID, queryVec)

	// Force the seqscan/top-N plan (the finding's failing plan) DETERMINISTICALLY:
	// with no HNSW index, `ORDER BY s.embedding <=> $1 LIMIT $2` must seqscan and,
	// with LIMIT (10) > the single non-NULL row, returns the NULL-embedding row
	// too. Global to this throwaway DB, so it holds for every pooled connection.
	if _, err := pool.Exec(ctx, `DROP INDEX IF EXISTS idx_skills_embedding`); err != nil {
		t.Fatalf("drop HNSW index (force seqscan plan): %v", err)
	}

	store.WithEmbedder(&fixedEmbedder{
		byText: map[string][]float32{query: queryVec},
		def:    g29Vec(500),
	})

	results, err := store.Search(ctx, query, 10)
	if err != nil {
		// RED on pre-fix (unguarded VectorSearch): the seqscan returns the
		// NULL-embedding row, pgx errors "cannot scan NULL into *float64", and the
		// unmasked vector-leg error hard-fails the whole hybrid Search.
		t.Fatalf("hybrid Search(%q) hard-failed on a NULL-embedding row under the seqscan plan: %v; VectorSearch must exclude NULL embeddings (WHERE s.embedding IS NOT NULL) so correctness does not depend on the query plan", query, err)
	}

	// The embedded skill must surface through the (now NULL-safe) vector leg.
	foundSemantic := false
	// The NULL-embedding skill must STILL surface through the trigram leg (its text
	// matches) -- the fix removes it from the vector leg only, not from Search.
	foundNull := false
	for _, r := range results {
		switch r.Skill.Name {
		case semantic.Name:
			foundSemantic = true
		case nullrow.Name:
			foundNull = true
		}
	}
	if !foundSemantic {
		t.Errorf("embedded skill %q missing from hybrid Search(%q) results %v; the vector leg must still return non-NULL embeddings after the NULL guard", semantic.Name, query, names(results))
	}
	if !foundNull {
		t.Errorf("NULL-embedding skill %q (text matches %q) missing from results %v; the WHERE s.embedding IS NOT NULL guard must exclude the row from the VECTOR leg ONLY, the trigram leg must still surface it", nullrow.Name, query, names(results))
	}
}

func names(results []models.SearchResult) []string {
	out := make([]string, len(results))
	for i, r := range results {
		out[i] = r.Skill.Name
	}
	return out
}
