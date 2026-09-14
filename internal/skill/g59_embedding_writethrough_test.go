package skill

// G59: db.StoreSkillEmbedding (internal/db/vector.go) had ZERO callers
// project-wide -- Store.Create (and therefore CreateFromTOML, which delegates
// to Create; see store.go's doc comment on CreateFromTOML) never wrote a
// skill's embedding column, so every NEWLY CREATED skill silently degraded to
// trigram-only search even when a query-side embedder was configured. The
// vector-KNN leg of §G29's hybrid search never had anything of its OWN to
// retrieve for a skill created after that fix landed: every populated
// embedding in the existing G29 test suite (hybrid_search_g29_test.go,
// g29_search_remediation_test.go, internal/validation's
// pipeline_g29_crossref_exact_test.go) is written by a raw SQL
// `UPDATE skills SET embedding = ...` in test SETUP, specifically because
// Store.Create itself never does it -- that gap is exactly what G59 closes.
//
// These are the §11.4.115 RED-first regression guards for the write-through
// fix. Unlike the sibling G29 tests, they configure a query-side embedder
// BEFORE calling store.Create and never manually UPDATE the embedding column
// -- proving CREATE ITSELF now writes a retrievable embedding. They FAIL on
// the pre-fix Create (which never calls db.StoreSkillEmbedding, so the
// embedding column stays NULL and the vector-KNN leg has nothing to find), and
// a §1.1 mutation reverting Create to skip the embedding write makes them FAIL
// again.

import (
	"context"
	"errors"
	"testing"

	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// g59FixedEmbedder is a deterministic db.Embedder double that returns the SAME
// fixed vector for every input text, regardless of content (mirrors
// internal/validation's g29xrEmbedder). Using a content-independent embedder
// means these tests do not need to know -- and do not assert on -- the exact
// textual representation Create embeds internally; that formula
// (buildSkillEmbedText in store.go) is an implementation detail, not part of
// this fix's observable contract. What the fix DOES guarantee, and what these
// tests DO assert, is that SOME embedding gets durably written by Create and
// that it is retrievable via vector-KNN.
//
// failAfter simulates an embedding-provider outage: -1 means never fail; 0
// means fail starting from the very first call (used by the degrade-path
// test below).
//
// callVecs (F2, code-review MAJOR remediation) optionally overrides vec on a
// PER-CALL basis: the (n+1)-th Embed invocation (0-indexed) returns
// callVecs[n] when present, falling back to vec once callVecs is exhausted (or
// when callVecs is nil, the default -- every EXISTING G59 test constructs
// g59FixedEmbedder without setting it and relies on every call returning the
// SAME vec, which this preserves byte-for-byte). This lets a test that spans
// TWO Create calls against the SAME skill name (the insert, then the
// ON CONFLICT (name) DO UPDATE) assert the update path's embed call produced a
// DIFFERENT stored vector -- proof the store re-invoked the embedder on
// update rather than the update path silently reusing (or never overwriting)
// whatever the insert wrote.
type g59FixedEmbedder struct {
	vec       []float32
	calls     int
	failAfter int
	callVecs  [][]float32
}

func (e *g59FixedEmbedder) Dimensions() int { return g29EmbeddingDim } // shares hybrid_search_g29_test.go's const (768, migrations/001_initial.up.sql)

func (e *g59FixedEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.calls++
	if e.failAfter >= 0 && e.calls > e.failAfter {
		return nil, errG59FixedEmbedderFailure
	}
	v := e.vec
	if idx := e.calls - 1; idx < len(e.callVecs) {
		v = e.callVecs[idx]
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = v
	}
	return out, nil
}

var errG59FixedEmbedderFailure = errors.New("g59FixedEmbedder: simulated embedding provider outage")

// TestG59_Create_WritesEmbedding_RetrievableByVectorKNN is the core RED-first
// guard: Store.Create, with an embedder configured BEFORE the call (never via
// a manual SQL UPDATE), must durably persist a non-NULL embedding for the new
// skill that is retrievable through the vector-KNN leg of hybrid Search for a
// probe query sharing NO lexical token with the skill's own text.
func TestG59_Create_WritesEmbedding_RetrievableByVectorKNN(t *testing.T) {
	ctx, store, pool, cleanup := g29NewLiveStore(t) // reuse the G29 live-DB harness (hybrid_search_g29_test.go)
	if store == nil {
		return // skipped: no test DB
	}
	defer cleanup()

	probeVec := g29Vec(42)
	emb := &g59FixedEmbedder{vec: probeVec, failAfter: -1}
	store.WithEmbedder(emb)

	// Semantically-targeted skill: its text shares NO token with the probe
	// query below, so ONLY the vector leg (fed by the embedding Create must now
	// write) can retrieve it.
	target := &models.Skill{
		Name: "g59.writethrough.target", Title: "Distributed consensus algorithms",
		Description: "raft and paxos", Content: "consensus",
		Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
	}
	if err := store.Create(ctx, target); err != nil {
		t.Fatalf("create target skill: %v", err)
	}

	// Direct DB-level proof the embedding column is non-NULL -- the exact
	// signal db.StoreSkillEmbedding is supposed to set -- independent of
	// Search's fusion logic, so this assertion pins the WRITE side precisely.
	var embeddingIsNull bool
	if err := pool.QueryRow(ctx, `SELECT embedding IS NULL FROM skills WHERE id = $1`, target.ID).Scan(&embeddingIsNull); err != nil {
		t.Fatalf("query embedding column: %v", err)
	}
	if embeddingIsNull {
		t.Fatalf("skills.embedding is NULL for skill %q after store.Create with an embedder configured; "+
			"Create must call db.StoreSkillEmbedding so the vector-KNN leg of hybrid Search has something "+
			"to retrieve for newly created skills (§G59)", target.Name)
	}

	const probe = "qzx-g59-vector-only-probe"
	results, err := store.Search(ctx, probe, 10)
	if err != nil {
		t.Fatalf("hybrid Search: %v", err)
	}
	found := false
	for _, r := range results {
		if r.Skill.Name == target.Name {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("hybrid Search(%q) did not return %q via the vector-KNN leg; "+
			"store.Create never wrote a retrievable embedding (results=%v)", probe, target.Name, names(results))
	}
	if emb.calls < 2 {
		// One call from Create's write-side embed, one from Search's query-side
		// embed. Fewer than 2 means Create never actually invoked the embedder.
		t.Errorf("embedder was called %d time(s); expected >= 2 (one at Create-time, one at Search query-time)", emb.calls)
	}
}

// TestG59_CreateFromTOML_WritesEmbedding proves CreateFromTOML inherits the
// write-through fix via its delegation to Create (store.go's CreateFromTOML
// calls s.Create(ctx, skill) internally; there is no separate embedding-write
// call needed there). Same probe-query design as the Create test above.
func TestG59_CreateFromTOML_WritesEmbedding(t *testing.T) {
	ctx, store, pool, cleanup := g29NewLiveStore(t)
	if store == nil {
		return // skipped: no test DB
	}
	defer cleanup()

	probeVec := g29Vec(55)
	store.WithEmbedder(&g59FixedEmbedder{vec: probeVec, failAfter: -1})

	wrapper := &models.TOMLSkillWrapper{}
	wrapper.Skill.Name = "g59.toml.writethrough.target"
	wrapper.Skill.Title = "Container orchestration schedulers"
	wrapper.Skill.Description = "kubernetes and nomad"
	wrapper.Skill.Content = "scheduling"
	wrapper.Skill.Kind = "atomic"

	created, err := store.CreateFromTOML(ctx, wrapper)
	if err != nil {
		t.Fatalf("CreateFromTOML: %v", err)
	}

	var embeddingIsNull bool
	if err := pool.QueryRow(ctx, `SELECT embedding IS NULL FROM skills WHERE id = $1`, created.ID).Scan(&embeddingIsNull); err != nil {
		t.Fatalf("query embedding column: %v", err)
	}
	if embeddingIsNull {
		t.Fatalf("skills.embedding is NULL for skill %q after store.CreateFromTOML with an embedder configured; "+
			"CreateFromTOML delegates to Create and must inherit the embedding write (§G59)", created.Name)
	}

	const probe = "qzx-g59-toml-vector-only-probe"
	results, err := store.Search(ctx, probe, 10)
	if err != nil {
		t.Fatalf("hybrid Search: %v", err)
	}
	found := false
	for _, r := range results {
		if r.Skill.Name == created.Name {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("hybrid Search(%q) did not return %q via the vector-KNN leg after CreateFromTOML; results=%v",
			probe, created.Name, names(results))
	}
}

// TestG59_Create_EmbedderFails_SkillStillCreated_DegradeWarned proves the
// documented failure posture (see embedWriteThrough's doc comment in
// store.go): an embedder call failure AT CREATE TIME must NOT fail skill
// creation -- the skill still lands, remains trigram-searchable, and the
// degradation is observable via a warning log -- mirroring Search's own
// posture toward a query-time embedder outage (warnEmbeddingDegraded),
// applied symmetrically to the write side.
func TestG59_Create_EmbedderFails_SkillStillCreated_DegradeWarned(t *testing.T) {
	ctx, store, pool, cleanup := g29NewLiveStore(t)
	if store == nil {
		return // skipped: no test DB
	}
	defer cleanup()

	// Install an observer core via the Store's OWN injected-logger seam
	// (WithLogger) -- the same seam internal/mcp.NewMCPServer wires a real
	// logger through in production, and the same pattern
	// g29_search_remediation_test.go already established for the query-side
	// degrade warning.
	core, logs := observer.New(zap.WarnLevel)
	store.WithLogger(zap.New(core))

	failingEmb := &g59FixedEmbedder{vec: g29Vec(1), failAfter: 0} // fails starting from the very first call
	store.WithEmbedder(failingEmb)

	// The skill's own title DOES lexically match the probe query below, so a
	// trigram-only retrieval remains possible even with a NULL embedding --
	// proving the skill is degraded to trigram-only, never lost entirely.
	sk := &models.Skill{
		Name: "g59.writethrough.degrade", Title: "g59-degrade-probe-title",
		Description: "embedder outage during create", Content: "x",
		Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
	}
	if err := store.Create(ctx, sk); err != nil {
		t.Fatalf("store.Create must NOT fail when the embedder call fails at create time "+
			"(degrade gracefully, don't reject the skill); got: %v", err)
	}

	// Snapshot embedder-call-count and log-count IMMEDIATELY after Create
	// returns, BEFORE calling Search below. Search ALSO consults s.embedder
	// when one is configured (that path predates this fix, §G29) and would
	// itself add calls/log entries -- if these assertions ran only after
	// Search, a pre-fix Create (which never touches the embedder at all)
	// could still pass them for the WRONG reason (Search's own pre-existing
	// degrade path satisfying "embedder was called" / "a warning was
	// logged"), which would make this guard a false RED-first proof. Snapshotting
	// here isolates CREATE's own behaviour from SEARCH's.
	callsAfterCreate := failingEmb.calls
	logsAfterCreate := logs.Len()

	var embeddingIsNull bool
	if err := pool.QueryRow(ctx, `SELECT embedding IS NULL FROM skills WHERE id = $1`, sk.ID).Scan(&embeddingIsNull); err != nil {
		t.Fatalf("query embedding column: %v", err)
	}
	if !embeddingIsNull {
		t.Errorf("expected skills.embedding to remain NULL after a simulated create-time embedder " +
			"failure, got non-NULL")
	}

	// The embedder must have been genuinely INVOKED by Create itself (proving
	// Create attempted the write, not that it silently skipped because it
	// never wires an embedder call at all).
	if callsAfterCreate == 0 {
		t.Fatalf("embedder was never called during store.Create; Create must attempt the write-side "+
			"embed (and degrade on failure) rather than never invoking the embedder at all (calls=%d)",
			callsAfterCreate)
	}
	// The create-time degrade must be OBSERVABLE (mirrors Search's
	// warnEmbeddingDegraded contract): at least one warning log entry BY THE
	// TIME Create RETURNS, not merely by the time some later Search call
	// happens to also degrade.
	if logsAfterCreate == 0 {
		t.Fatalf("expected at least one warning log entry immediately after store.Create degraded on " +
			"a failing embedder; degradation must be observable from Create itself, not silent")
	}

	// The skill must still be trigram-retrievable (never silently dropped).
	results, err := store.Search(ctx, "g59-degrade-probe-title", 10)
	if err != nil {
		t.Fatalf("Search after degrade: %v", err)
	}
	found := false
	for _, r := range results {
		if r.Skill.Name == sk.Name {
			found = true
		}
	}
	if !found {
		t.Errorf("skill %q not retrievable via trigram search after a create-time embedder failure -- "+
			"the fix must degrade gracefully, not drop the skill; results=%v", sk.Name, names(results))
	}
}

// TestG59_Create_NoEmbedderConfigured_SkipsCleanly_NoPanic proves the
// documented no-embedder mode: Store.Create with NO embedder configured
// (the zero-value/default Store, mirroring Search's own `s.embedder == nil`
// early return) must behave EXACTLY as before this fix -- the skill is
// created successfully, its embedding column stays NULL, and no embedder
// method is ever invoked (there is none to invoke). This is the every-existing-
// test-helper path (NewStore never calls WithEmbedder) and must never panic
// nor error.
func TestG59_Create_NoEmbedderConfigured_SkipsCleanly(t *testing.T) {
	ctx, store, pool, cleanup := g29NewLiveStore(t)
	if store == nil {
		return // skipped: no test DB
	}
	defer cleanup()

	// Deliberately do NOT call store.WithEmbedder -- the default/no-embedder
	// mode every pre-G59 test helper and deployment-without-a-provider relies
	// on.
	sk := &models.Skill{
		Name: "g59.writethrough.noembedder", Title: "no embedder configured",
		Description: "must skip embedding cleanly", Content: "y",
		Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
	}
	if err := store.Create(ctx, sk); err != nil {
		t.Fatalf("store.Create with no embedder configured must succeed unchanged: %v", err)
	}

	var embeddingIsNull bool
	if err := pool.QueryRow(ctx, `SELECT embedding IS NULL FROM skills WHERE id = $1`, sk.ID).Scan(&embeddingIsNull); err != nil {
		t.Fatalf("query embedding column: %v", err)
	}
	if !embeddingIsNull {
		t.Errorf("expected skills.embedding to remain NULL when no embedder is configured, got non-NULL")
	}
}

// ---------------------------------------------------------------------------
// Fable code-review remediation (fix round): F1 (BLOCKING) + F2 (MAJOR) +
// F3 (MEDIUM) + F4 (MEDIUM). Each closes a gap the original G59 landing left
// open; see the accompanying store.go/import_export.go/vector.go doc comments
// for the production-code side of each fix.
// ---------------------------------------------------------------------------

// g59ReadEmbedding reads a skill's raw embedding vector directly (bypassing
// Search's fusion/scoring entirely), for tests (F2) that need to assert the
// STORED vector's exact identity -- not merely "non-NULL" -- which a stale
// leftover vector from an earlier write would also satisfy.
func g59ReadEmbedding(t *testing.T, ctx context.Context, pool *db.Pool, id uuid.UUID) []float32 {
	t.Helper()
	var vec pgvector.Vector
	if err := pool.QueryRow(ctx, `SELECT embedding FROM skills WHERE id = $1`, id).Scan(&vec); err != nil {
		t.Fatalf("read embedding for skill %s: %v", id, err)
	}
	return vec.Slice()
}

// g59VecEqual reports whether two embedding vectors are identical, component
// for component. Used by F2's re-embed-on-update proof.
func g59VecEqual(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestG59_ImportFromTOML_WritesEmbedding_RetrievableByVectorKNN closes F1
// (code-review BLOCKING finding): the LIVE, deployed MCP skill_create tool
// (internal/mcp/tools.go registerSkillCreate) calls store.ImportFromTOML
// DIRECTLY -- `s.skillStore.ImportFromTOML(ctx, []byte(tomlStr))` -- never
// store.Create. Proving Create/CreateFromTOML write-through embed (the four
// tests above) is therefore NOT sufficient to prove the path end users
// actually exercise is vector-KNN retrievable; a skill created through the
// deployed MCP tool landed with a NULL embedding regardless of embedder
// configuration until this fix. Same probe-query design as
// TestG59_Create_WritesEmbedding_RetrievableByVectorKNN above (a semantic
// query sharing NO lexical token with the skill's own text, so only the
// vector leg can retrieve it), driven through ImportFromTOML instead of
// Create.
func TestG59_ImportFromTOML_WritesEmbedding_RetrievableByVectorKNN(t *testing.T) {
	ctx, store, pool, cleanup := g29NewLiveStore(t)
	if store == nil {
		return // skipped: no test DB
	}
	defer cleanup()

	probeVec := g29Vec(90)
	store.WithEmbedder(&g59FixedEmbedder{vec: probeVec, failAfter: -1})

	tomlDoc := `
[skill]
name = "g59.importfromtoml.writethrough.target"
version = "0.1.0"
title = "Byzantine fault tolerant consensus"
description = "pbft and tendermint replication"
content = "consensus algorithms for distributed systems"
kind = "atomic"
`
	created, err := store.ImportFromTOML(ctx, []byte(tomlDoc))
	if err != nil {
		t.Fatalf("ImportFromTOML: %v", err)
	}

	var embeddingIsNull bool
	if err := pool.QueryRow(ctx, `SELECT embedding IS NULL FROM skills WHERE id = $1`, created.ID).Scan(&embeddingIsNull); err != nil {
		t.Fatalf("query embedding column: %v", err)
	}
	if embeddingIsNull {
		t.Fatalf("skills.embedding is NULL for skill %q after store.ImportFromTOML with an embedder "+
			"configured; ImportFromTOML is the function the LIVE MCP skill_create tool calls directly "+
			"(tools.go registerSkillCreate) -- it must write the embedding just like Create, or every "+
			"skill created via the deployed MCP tool is invisible to vector-KNN (§G59 F1)", created.Name)
	}

	const probe = "qzx-g59-importfromtoml-vector-only-probe"
	results, err := store.Search(ctx, probe, 10)
	if err != nil {
		t.Fatalf("hybrid Search: %v", err)
	}
	found := false
	for _, r := range results {
		if r.Skill.Name == created.Name {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("hybrid Search(%q) did not return %q via the vector-KNN leg after ImportFromTOML; "+
			"store.ImportFromTOML never wrote a retrievable embedding (results=%v)", probe, created.Name, names(results))
	}
}

// TestG59_Create_ConflictUpdate_ReembedsWithNewContent closes F2 (code-review
// MAJOR finding): every pre-existing G59 test only ever Creates a brand-new
// skill NAME -- none of them exercises the ON CONFLICT (name) DO UPDATE
// branch of store.go's Create -- so a future regression that wired the
// embedding write into the INSERT path only (never the update path) would
// pass every existing G59 guard undetected. g59FixedEmbedder's per-call
// callVecs option (see its doc comment) lets this test pin exactly which
// vector each of the two Create calls' embed invocations returns, so it can
// assert the update call genuinely re-invoked the embedder AND persisted a
// DIFFERENT vector -- not merely "still non-NULL", which a stale leftover
// vector from the FIRST insert would also satisfy.
func TestG59_Create_ConflictUpdate_ReembedsWithNewContent(t *testing.T) {
	ctx, store, pool, cleanup := g29NewLiveStore(t)
	if store == nil {
		return // skipped: no test DB
	}
	defer cleanup()

	vecA := g29Vec(80)
	vecB := g29Vec(81)
	emb := &g59FixedEmbedder{callVecs: [][]float32{vecA, vecB}, failAfter: -1}
	store.WithEmbedder(emb)

	name := "g59.conflict.reembed"
	first := &models.Skill{
		Name: name, Title: "first version", Description: "d1", Content: "c1",
		Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
	}
	if err := store.Create(ctx, first); err != nil {
		t.Fatalf("initial create: %v", err)
	}
	if got := g59ReadEmbedding(t, ctx, pool, first.ID); !g59VecEqual(got, vecA) {
		t.Fatalf("after the initial INSERT, stored embedding = %v, want the first call's vecA %v", got, vecA)
	}

	// Re-Create the SAME name with different content -- this hits the
	// ON CONFLICT (name) DO UPDATE branch of Create's upsert.
	second := &models.Skill{
		Name: name, Title: "second version", Description: "d2", Content: "c2",
		Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
	}
	if err := store.Create(ctx, second); err != nil {
		t.Fatalf("update via ON CONFLICT (name): %v", err)
	}

	if emb.calls < 2 {
		t.Fatalf("embedder invoked %d time(s) across an insert + an update targeting the same name; "+
			"want >= 2 (the ON CONFLICT (name) DO UPDATE path must re-invoke the embedder on update, not "+
			"only on the initial insert)", emb.calls)
	}
	if got := g59ReadEmbedding(t, ctx, pool, second.ID); !g59VecEqual(got, vecB) {
		t.Fatalf("after ON CONFLICT (name) DO UPDATE, stored embedding = %v, want the SECOND call's vecB "+
			"%v (found the stale first-insert vector instead -- the update path never re-embedded)", got, vecB)
	}
}

// TestG59_Create_ConflictUpdate_EmbedFails_ClearsStaleEmbedding closes F3
// (code-review MEDIUM finding): Create's upsert SET list never touches
// `embedding` (store.go), so on an ON CONFLICT (name) DO UPDATE call whose
// re-embed attempt fails, the PREVIOUS content's vector -- written by an
// earlier successful Create -- would otherwise survive untouched, now stale
// against the skill's NEW content and silently served as a vector-KNN match
// for content that no longer exists. The fix (embedWriteThrough's
// clearStaleEmbedding calls, store.go) must clear the column to NULL on this
// failure path instead of leaving the stale vector in place.
func TestG59_Create_ConflictUpdate_EmbedFails_ClearsStaleEmbedding(t *testing.T) {
	ctx, store, pool, cleanup := g29NewLiveStore(t)
	if store == nil {
		return // skipped: no test DB
	}
	defer cleanup()

	vecA := g29Vec(85)
	emb := &g59FixedEmbedder{vec: vecA, failAfter: -1}
	store.WithEmbedder(emb)

	name := "g59.conflict.clearstale"
	first := &models.Skill{
		Name: name, Title: "v1", Description: "d1", Content: "c1",
		Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
	}
	if err := store.Create(ctx, first); err != nil {
		t.Fatalf("initial create: %v", err)
	}
	if got := g59ReadEmbedding(t, ctx, pool, first.ID); !g59VecEqual(got, vecA) {
		t.Fatalf("precondition failed: after the initial insert, stored embedding = %v, want vecA %v (the "+
			"subsequent failing-update assertion is meaningless unless this precondition holds)", got, vecA)
	}

	// From this point on, every embedder call fails -- simulating an
	// embedding-provider outage occurring exactly during the update's
	// re-embed attempt.
	emb.failAfter = emb.calls

	second := &models.Skill{
		Name: name, Title: "v2", Description: "d2", Content: "c2 -- different content entirely",
		Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
	}
	if err := store.Create(ctx, second); err != nil {
		t.Fatalf("store.Create must NOT fail when the update-time re-embed fails (degrade gracefully): %v", err)
	}

	var embeddingIsNull bool
	if err := pool.QueryRow(ctx, `SELECT embedding IS NULL FROM skills WHERE id = $1`, second.ID).Scan(&embeddingIsNull); err != nil {
		t.Fatalf("query embedding column: %v", err)
	}
	if !embeddingIsNull {
		t.Fatalf("expected skills.embedding to be cleared to NULL after a failing re-embed on an "+
			"ON CONFLICT (name) DO UPDATE (§G59 F3); found the STALE vector from the skill's PREVIOUS "+
			"content (%q) still present instead of an honest degrade-to-trigram-only", first.Content)
	}
}

// TestG59_Create_ConflictUpdate_EmbedSucceedsButStoreFails_ClearsStaleEmbedding
// closes the F3 round-2 Fable-xhigh re-review finding (MEDIUM): the ORIGINAL
// F3 fix (TestG59_Create_ConflictUpdate_EmbedFails_ClearsStaleEmbedding above)
// only exercises the branch where s.embedder.Embed itself returns an error.
// embedWriteThrough (store.go) has a FOURTH failure/skip branch its own doc
// comment claims is covered but, before this fix, was not: Embed() SUCCEEDS
// and returns a usable (non-empty) vector, yet the subsequent
// db.StoreSkillEmbedding call fails -- e.g. the embedder's returned vector has
// the wrong dimension against the `embedding vector(768)` column
// (migrations/001_initial.up.sql). That branch only called
// warnEmbeddingDegraded and returned, leaving the PREVIOUS content's vector
// (vecA below, written by the FIRST Create) stored and vector-KNN-servable
// for content that no longer exists -- exactly the stale-vector defect F3
// exists to close, on the one branch the original fix missed. Note this is
// NOT a case where "the clear would fail anyway": ClearSkillEmbedding writes
// NULL (no dimension to violate), so the clear succeeds even though the store
// that preceded it failed on a dimension mismatch.
func TestG59_Create_ConflictUpdate_EmbedSucceedsButStoreFails_ClearsStaleEmbedding(t *testing.T) {
	ctx, store, pool, cleanup := g29NewLiveStore(t)
	if store == nil {
		return // skipped: no test DB
	}
	defer cleanup()

	vecA := g29Vec(87)
	// Deliberately WRONG dimension (5, not g29EmbeddingDim=768): Embed()
	// returns this with NO error (a real "usable, non-empty vector" from the
	// provider's point of view), so embedWriteThrough proceeds to
	// db.StoreSkillEmbedding, which then fails against the vector(768) column
	// ("expected 768 dimensions, not 5").
	wrongDimVec := make([]float32, 5)
	for i := range wrongDimVec {
		wrongDimVec[i] = 0.5
	}
	emb := &g59FixedEmbedder{callVecs: [][]float32{vecA, wrongDimVec}, failAfter: -1}
	store.WithEmbedder(emb)

	name := "g59.conflict.storefails.clearstale"
	first := &models.Skill{
		Name: name, Title: "v1", Description: "d1", Content: "c1",
		Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
	}
	if err := store.Create(ctx, first); err != nil {
		t.Fatalf("initial create: %v", err)
	}
	if got := g59ReadEmbedding(t, ctx, pool, first.ID); !g59VecEqual(got, vecA) {
		t.Fatalf("precondition failed: after the initial insert, stored embedding = %v, want vecA %v (the "+
			"subsequent failing-store assertion is meaningless unless this precondition holds)", got, vecA)
	}

	// Re-Create the SAME name with different content -- this hits the
	// ON CONFLICT (name) DO UPDATE branch of Create's upsert. The embedder's
	// SECOND call (the update path's re-embed) succeeds (no error) but
	// returns wrongDimVec, so Embed() itself never fails -- only the
	// subsequent db.StoreSkillEmbedding call does.
	second := &models.Skill{
		Name: name, Title: "v2", Description: "d2", Content: "c2 -- different content entirely",
		Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
	}
	if err := store.Create(ctx, second); err != nil {
		t.Fatalf("store.Create must NOT fail when the update-time db.StoreSkillEmbedding write fails "+
			"(degrade gracefully, same posture as every other embedWriteThrough failure/skip branch): %v", err)
	}

	if emb.calls < 2 {
		t.Fatalf("embedder invoked %d time(s) across an insert + an update targeting the same name; "+
			"want >= 2 (precondition for this test: the update path must have re-invoked the embedder)",
			emb.calls)
	}

	var embeddingIsNull bool
	if err := pool.QueryRow(ctx, `SELECT embedding IS NULL FROM skills WHERE id = $1`, second.ID).Scan(&embeddingIsNull); err != nil {
		t.Fatalf("query embedding column: %v", err)
	}
	if !embeddingIsNull {
		t.Fatalf("expected skills.embedding to be cleared to NULL after Embed() SUCCEEDED but the "+
			"subsequent db.StoreSkillEmbedding call FAILED (dimension mismatch) on an ON CONFLICT (name) "+
			"DO UPDATE (F3 round-2 Fable-xhigh re-review finding, MEDIUM); found the STALE vector from "+
			"the skill's PREVIOUS content (%q) still present instead of an honest degrade-to-trigram-only -- "+
			"embedWriteThrough's StoreSkillEmbedding-error branch (store.go) must call clearStaleEmbedding "+
			"too, matching its own doc-comment's claim that EVERY failure/skip branch does so", first.Content)
	}
}

// ---------------------------------------------------------------------------
// Round-3 Fable-xhigh re-review remediation: F5 (MEDIUM, PROVEN LIVE) + F6
// (LOW). See store.go's clearStaleEmbedding/warnEmbeddingClearFailed doc
// comments for the production-code side of each fix.
// ---------------------------------------------------------------------------

// g59CancelingEmbedder is a db.Embedder double that, on its (cancelOnCall+1)-th
// invocation (0-indexed), CANCELS the ctx it was called with and then returns
// ctx.Err() -- reproducing the round-3 reviewer's PROVEN LIVE F5 scenario: an
// Embed call's own ctx is canceled by an upstream client disconnect DURING the
// (slow) embedding call, and Embed observes + returns that cancellation as its
// error. cancel is wired in by the test AFTER constructing the cancellable
// context that will be passed to the Create call this embedder is attached
// to (see TestG59_ConflictUpdate_CtxCanceledMidEmbed_ClearsStaleEmbedding).
type g59CancelingEmbedder struct {
	vec          []float32
	cancelOnCall int
	cancel       context.CancelFunc
	calls        int
}

func (e *g59CancelingEmbedder) Dimensions() int { return g29EmbeddingDim }

func (e *g59CancelingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	idx := e.calls
	e.calls++
	if idx == e.cancelOnCall {
		e.cancel()
		return nil, ctx.Err()
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = e.vec
	}
	return out, nil
}

// TestG59_ConflictUpdate_CtxCanceledMidEmbed_ClearsStaleEmbedding is the
// §11.4.199 exact-reproduction-sequence RED-first guard for F5 (round-3
// Fable-xhigh re-review, MEDIUM, PROVEN LIVE): the reviewer's probe scenario
// is -- a skill has vecA stored; the caller re-Creates it with new content;
// the skill-row upsert COMMITS; then the caller's OWN ctx is canceled DURING
// the update path's (slow) s.embedder.Embed call (a client disconnect is the
// realistic production trigger); Embed observes the cancellation and returns
// ctx.Err(); embedWriteThrough's Embed-error branch (store.go) calls
// clearStaleEmbedding with that SAME already-canceled ctx. Pre-fix, branches
// 1-3 of embedWriteThrough (including this one) passed that raw, dead ctx
// straight through to db.ClearSkillEmbedding, whose UPDATE then failed
// instantly on the dead context -- leaving vecA (the PREVIOUS content's
// vector) stored and vector-KNN-servable for content that Create's own upsert
// had ALREADY overwritten. This test FAILS on that pre-fix code (clear
// silently fails on the canceled ctx, vecA survives) and PASSES post-fix
// (clearStaleEmbedding detaches + bounds its own clear internally, so the
// caller's ctx cancellation can no longer defeat it).
func TestG59_ConflictUpdate_CtxCanceledMidEmbed_ClearsStaleEmbedding(t *testing.T) {
	ctx, store, pool, cleanup := g29NewLiveStore(t)
	if store == nil {
		return // skipped: no test DB
	}
	defer cleanup()

	vecA := g29Vec(93)
	// cancelOnCall: 1 -- the FIRST Embed call (index 0, during the initial
	// insert below) must succeed normally so vecA is genuinely stored first;
	// the SECOND Embed call (index 1, during the update's re-embed) is the one
	// that cancels its own ctx and returns ctx.Err(), reproducing the exact
	// sequence the reviewer proved live.
	emb := &g59CancelingEmbedder{vec: vecA, cancelOnCall: 1}
	store.WithEmbedder(emb)

	name := "g59.conflict.ctxcanceled.clearstale"
	first := &models.Skill{
		Name: name, Title: "v1", Description: "d1", Content: "c1",
		Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
	}
	if err := store.Create(ctx, first); err != nil {
		t.Fatalf("initial create: %v", err)
	}
	if got := g59ReadEmbedding(t, ctx, pool, first.ID); !g59VecEqual(got, vecA) {
		t.Fatalf("precondition failed: after the initial insert, stored embedding = %v, want vecA %v (the "+
			"subsequent ctx-canceled-mid-embed assertion is meaningless unless this precondition holds)", got, vecA)
	}

	// A SEPARATE cancellable context for the SECOND Create call, standing in
	// for a per-request context (e.g. an HTTP handler's r.Context()) whose
	// lifetime is independent of the skill row this Create will durably
	// commit. Canceling updateCtx does NOT cancel its parent ctx (used below
	// to verify the post-condition), mirroring how a request's own context
	// cancellation is unrelated to the store's already-committed state.
	updateCtx, cancel := context.WithCancel(ctx)
	emb.cancel = cancel

	second := &models.Skill{
		Name: name, Title: "v2", Description: "d2", Content: "c2 -- different content entirely",
		Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
	}
	if err := store.Create(updateCtx, second); err != nil {
		t.Fatalf("store.Create must NOT fail when the update-time re-embed's OWN ctx is canceled mid-call "+
			"(degrade gracefully, same posture as every other embedWriteThrough failure/skip branch): %v", err)
	}

	var embeddingIsNull bool
	if err := pool.QueryRow(ctx, `SELECT embedding IS NULL FROM skills WHERE id = $1`, second.ID).Scan(&embeddingIsNull); err != nil {
		t.Fatalf("query embedding column: %v", err)
	}
	if !embeddingIsNull {
		t.Fatalf("expected skills.embedding to be cleared to NULL after the caller's ctx was canceled DURING "+
			"the update-time re-embed call (§G59 F5, round-3 Fable-xhigh re-review, MEDIUM, PROVEN LIVE); "+
			"found the STALE vector from the skill's PREVIOUS content (%q) still present instead of an honest "+
			"degrade-to-trigram-only -- clearStaleEmbedding's clear attempt must run on a context detached from "+
			"the CALLER's cancellation (the pre-fix code passed the raw, already-canceled ctx straight through "+
			"on this branch, so the clear's own UPDATE failed instantly on the dead context and the STALE "+
			"vector from the LOSING/previous content remained vector-KNN-servable)", first.Content)
	}
}

// TestG59_ClearFailureWarning_NotSuppressedByPrecedingDegradeWarning closes F6
// (LOW, round-3 Fable-xhigh re-review): warnEmbeddingDegraded is throttled to
// at most once per embedDegradeWarnInterval PER STORE (store.go). Two of
// embedWriteThrough's branches call warnEmbeddingDegraded IMMEDIATELY before
// calling clearStaleEmbedding, so if a clear failure's warning shared
// warnEmbeddingDegraded's OWN throttle counter, it would ALWAYS lose the
// CompareAndSwap race against the warning that just fired nanoseconds
// earlier -- permanently suppressing every clear-failure warning and making
// the stale-vector-retained condition invisible in logs exactly when an
// operator needs it most. This is a hermetic (no live DB required) unit test
// directly against Store's two warn helpers, proving they use INDEPENDENT
// throttle counters: calling warnEmbeddingDegraded (as embedWriteThrough's
// branches 2/4 do right before a clear attempt) does NOT suppress an
// IMMEDIATELY-following warnEmbeddingClearFailed call within the same
// throttle window.
func TestG59_ClearFailureWarning_NotSuppressedByPrecedingDegradeWarning(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	s := (&Store{}).WithLogger(zap.New(core))

	s.warnEmbeddingDegraded(errors.New("simulated embed/store failure"))
	if logs.Len() != 1 {
		t.Fatalf("precondition failed: warnEmbeddingDegraded should have logged exactly once, got %d entries", logs.Len())
	}

	// Immediately (same nanosecond-scale window) report a clear failure, the
	// exact sequencing embedWriteThrough's branches 2 and 4 produce. Pre-fix
	// (a shared throttle counter), this call would lose the CompareAndSwap
	// race and log NOTHING -- the exact §F6 defect.
	s.warnEmbeddingClearFailed(errors.New("simulated clear-stale-embedding failure"))
	if logs.Len() != 2 {
		t.Fatalf("expected a clear-failure warning to be logged even immediately after a warnEmbeddingDegraded "+
			"call within the SAME throttle window (§G59 F6, round-3 Fable-xhigh re-review, LOW); got %d log "+
			"entries, want 2 (the clear-failure warning must use its OWN throttle counter, never "+
			"warnEmbeddingDegraded's, or it is permanently suppressed by the warning that always immediately "+
			"precedes it)", logs.Len())
	}
}

// ---------------------------------------------------------------------------
// F4 (code-review MEDIUM finding): buildSkillEmbedText's exact formula was
// unpinned -- every other G59 test uses a CONTENT-INDEPENDENT embedder double
// (g59FixedEmbedder returns the same/next-scheduled vector regardless of what
// text it was called with), so dropping ANY single field
// (Name/Title/Description/Content) from buildSkillEmbedText changes ZERO
// existing test outcomes. These two hermetic (no-DB) unit tests pin the exact
// concatenation AND each field's individual contribution, so a future
// accidental (or deliberate) removal of any one field is caught here even
// though it is invisible to every live-DB embedder-double test above.
// ---------------------------------------------------------------------------

// TestBuildSkillEmbedText_ExactFormula pins buildSkillEmbedText's output for a
// fully-populated skill to the documented formula (store.go: Name, then
// Title, then Description, then Content, space-joined). Any reordering or
// dropping of a field changes this exact string.
func TestBuildSkillEmbedText_ExactFormula(t *testing.T) {
	sk := &models.Skill{
		Name:        "skill.name",
		Title:       "Skill Title",
		Description: "Skill description text",
		Content:     "Skill content body",
	}
	got := buildSkillEmbedText(sk)
	want := "skill.name Skill Title Skill description text Skill content body"
	if got != want {
		t.Fatalf("buildSkillEmbedText(fully-populated skill) = %q, want %q", got, want)
	}
}

// TestBuildSkillEmbedText_PerFieldContribution proves EACH field
// (Name/Title/Description/Content) individually contributes to the embedded
// text -- a skill carrying ONLY that one field must embed to EXACTLY that
// field's value, and a skill carrying NONE of them must embed to "". Dropping
// any single field from buildSkillEmbedText's formula breaks this test's case
// for that field (its "want" would go from the field's value to "") AND
// TestBuildSkillEmbedText_ExactFormula above (the fully-populated string would
// shrink) -- so this pair together makes every field load-bearing, unlike the
// content-independent live-DB tests elsewhere in this file.
func TestBuildSkillEmbedText_PerFieldContribution(t *testing.T) {
	tests := []struct {
		name string
		sk   *models.Skill
		want string
	}{
		{"name only", &models.Skill{Name: "N-value"}, "N-value"},
		{"title only", &models.Skill{Title: "T-value"}, "T-value"},
		{"description only", &models.Skill{Description: "D-value"}, "D-value"},
		{"content only", &models.Skill{Content: "C-value"}, "C-value"},
		{"all fields empty", &models.Skill{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildSkillEmbedText(tt.sk)
			if got != tt.want {
				t.Errorf("buildSkillEmbedText(%+v) = %q, want %q", tt.sk, got, tt.want)
			}
		})
	}
}
