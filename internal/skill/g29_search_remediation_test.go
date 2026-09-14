package skill

// Fable code-review remediation regression guards for the G29 hybrid-search
// findings NOT already covered by hybrid_search_g29_test.go (the F1/F2
// BLOCKING-finding guards, kept intact per the remediation instructions):
//
//   - finding 3 (WARNING): a per-query embedding failure was swallowed with
//     zero telemetry -- Search silently degraded to keyword-only with no
//     observable signal an operator could act on during a real
//     embedding-provider outage.
//   - finding 4 (WARNING): three row-iteration loops (textSearch's primary and
//     ILIKE-fallback legs, and VectorSearch) never checked rows.Err() after
//     the loop, so a mid-stream pgx/driver error silently truncated the
//     result set and returned a nil error instead of surfacing the failure.
//   - finding 7 (NIT): the NULL-safety fix already landed for the score
//     column (F2) did not extend to the nullable `description` column; a
//     direct-SQL row with a NULL description still panicked/errored on scan
//     in all three of the same query legs.
//
// All three guards are exercised here against the REAL live database (same
// SKILL_SYSTEM_TEST_DB_* harness as hybrid_search_g29_test.go) -- no fakes
// beyond the embedder double already established as the legitimate unit seam
// (§11.4.27).

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// ---------------------------------------------------------------------------
// Finding 3: throttled telemetry on query-embedding failure.
// ---------------------------------------------------------------------------

// g29FailingEmbedder is a db.Embedder double that always fails -- simulating
// an unreachable/erroring embedding provider.
type g29FailingEmbedder struct{}

func (g29FailingEmbedder) Dimensions() int { return g29EmbeddingDim }
func (g29FailingEmbedder) Embed(_ context.Context, _ []string) ([][]float32, error) {
	return nil, errG29FailingEmbedder
}

var errG29FailingEmbedder = &g29FailingEmbedderError{}

type g29FailingEmbedderError struct{}

func (*g29FailingEmbedderError) Error() string { return "g29 simulated embedding provider failure" }

// TestG29_Search_EmbeddingFailure_LogsThrottledWarning is the RED-first
// regression guard for finding 3: it proves (a) Search still degrades
// gracefully to the trigram-only result set on an embedding failure (no
// behaviour change), AND (b) the failure is OBSERVABLE via the Store's
// INJECTED logger (WithLogger) -- the exact sink production code writes to,
// via internal/mcp.NewMCPServer's store.WithLogger(logger) wiring -- with
// throttling to exactly one warning per embedDegradeWarnInterval.
//
// Re-review remediation (MAJOR finding, post-G29): this test originally
// captured the warning by installing an observer core as the process-GLOBAL
// zap logger (zap.ReplaceGlobals), which is exactly what let the underlying
// defect through green: warnEmbeddingDegraded called the package-level
// zap.L(), and this codebase's real construction path never calls
// zap.ReplaceGlobals (it threads an explicit *zap.Logger everywhere instead --
// see internal/api/server.go, internal/mcp/server.go), so in every deployed
// binary zap.L() is zap's no-op default and the warning was dead at the
// runtime layer. A test that replaces the GLOBAL logger observes its own
// override, not production behaviour, and stayed green through that defect.
// The fix: install the observer core via store.WithLogger(...) -- the same
// injection point NewMCPServer uses -- so this test now proves the warning
// reaches the Store's OWN configured sink, not a global the real binary never
// touches. Confirmed RED against the pre-fix zap.L() version (this test
// wired via WithLogger, warnEmbeddingDegraded still calling zap.L(): zero
// entries observed, test fails) and GREEN after routing warnEmbeddingDegraded
// through s.logger. A §1.1 mutation removing the warnEmbeddingDegraded call
// (or reverting it to zap.L()) makes this test FAIL again, while condition
// (a) (graceful degradation) alone would still pass.
func TestG29_Search_EmbeddingFailure_LogsThrottledWarning(t *testing.T) {
	ctx, store, _, cleanup := g29NewLiveStore(t)
	if store == nil {
		return // skipped: no test DB
	}
	defer cleanup()

	lexical := &models.Skill{
		Name: "g29.f3.lexical.telemetry", Title: "telemetry probe target",
		Description: "matches the query lexically", Content: "x",
		Status: models.SkillStatusActive, Kind: models.SkillKindAtomic,
	}
	if err := store.Create(ctx, lexical); err != nil {
		t.Fatalf("create lexical skill: %v", err)
	}
	store.WithEmbedder(g29FailingEmbedder{})

	// Install an observer core via the Store's OWN injected-logger seam
	// (WithLogger) -- the same seam internal/mcp.NewMCPServer wires a real
	// logger through -- so this test observes exactly what a production
	// deployment's logger would receive, not a process-global override.
	core, logs := observer.New(zap.WarnLevel)
	store.WithLogger(zap.New(core))

	results, err := store.Search(ctx, "telemetry probe target", 10)
	if err != nil {
		t.Fatalf("Search with a failing embedder returned an error (should degrade to keyword-only): %v", err)
	}
	found := false
	for _, r := range results {
		if r.Skill.Name == lexical.Name {
			found = true
		}
	}
	if !found {
		t.Fatalf("Search with a failing embedder dropped the lexical match %q; results=%v", lexical.Name, names(results))
	}

	entries := logs.FilterMessageSnippet("query embedding failed").All()
	if len(entries) != 1 {
		t.Fatalf("expected EXACTLY ONE WARN log for the failing query embedding (finding 3), got %d; "+
			"all captured log entries: %s", len(entries), logsSummary(logs))
	}
	if entries[0].Level != zap.WarnLevel {
		t.Errorf("embedding-degradation log level = %v, want %v", entries[0].Level, zap.WarnLevel)
	}

	// Throttling: a SECOND Search, called rapidly (well under
	// embedDegradeWarnInterval), must NOT log a second warning -- the
	// observed count must stay at exactly one.
	if _, err := store.Search(ctx, "telemetry probe target", 10); err != nil {
		t.Fatalf("second Search with a failing embedder returned an error: %v", err)
	}
	entriesAfterSecondCall := logs.FilterMessageSnippet("query embedding failed").All()
	if len(entriesAfterSecondCall) != 1 {
		t.Errorf("two rapid Search calls within the throttle interval logged %d total warning(s), want EXACTLY 1; "+
			"expected the second call's warning to be throttled (embedDegradeWarnInterval=%v)",
			len(entriesAfterSecondCall), embedDegradeWarnInterval)
	}
}

func logsSummary(logs *observer.ObservedLogs) string {
	var b strings.Builder
	for _, e := range logs.All() {
		b.WriteString(e.Level.String())
		b.WriteString(": ")
		b.WriteString(e.Message)
		b.WriteString(" | ")
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Finding 7: NULL description does not break any of the three search legs.
// ---------------------------------------------------------------------------

// g29InsertDirectSkill inserts a row directly via SQL (bypassing Store.Create,
// which always writes a Go zero-value "" for Description, never a SQL NULL)
// so the row's description column is genuinely NULL -- the direct-SQL,
// migration-permitted state finding 7 concerns (migrations/001_initial.up.sql:
// `description TEXT` has no NOT NULL constraint).
func g29InsertDirectSkill(t *testing.T, ctx context.Context, pool *db.Pool, name, title string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO skills (id, name, title, description, content, status, kind) VALUES ($1, $2, $3, NULL, 'content', 'active', 'atomic')`,
		id, name, title,
	); err != nil {
		t.Fatalf("direct-SQL insert of NULL-description skill %q: %v", name, err)
	}
	return id
}

// TestG29_NullDescription_TextSearchPrimaryLeg is the RED-first regression
// guard for finding 7's textSearch PRIMARY leg: a NULL description must not
// break the scan. Pre-fix, scanning NULL directly into the plain string
// Description field errors "cannot scan NULL into *string"; a §1.1 mutation
// reverting the NullString scan makes this FAIL again.
func TestG29_NullDescription_TextSearchPrimaryLeg(t *testing.T) {
	ctx, store, pool, cleanup := g29NewLiveStore(t)
	if store == nil {
		return
	}
	defer cleanup()

	const token = "g29f7primary"
	id := g29InsertDirectSkill(t, ctx, pool, "g29.f7.primary."+token, token)

	results, err := store.textSearch(ctx, token, 10)
	if err != nil {
		t.Fatalf("textSearch (primary leg) with a NULL-description row returned an error: %v", err)
	}
	found := false
	for _, r := range results {
		if r.Skill.ID == id {
			found = true
			if r.Skill.Description != "" {
				t.Errorf("NULL description scanned as %q, want empty string", r.Skill.Description)
			}
		}
	}
	if !found {
		t.Fatalf("NULL-description skill %q missing from textSearch(%q) primary-leg results=%v", id, token, names(results))
	}
}

// TestG29_NullDescription_TextSearchFallbackLeg is finding 7's textSearch
// FALLBACK leg guard. The fallback only runs when the primary trigram/ILIKE
// query returns zero rows, so this test's throwaway DB carries ONLY the one
// NULL-description row, engineered so its name/title trigram-similarity to
// the query is low (a long unrelated prefix) while still ILIKE-matching --
// forcing the primary leg to miss and the fallback to be the one that
// actually retrieves and scans the row.
func TestG29_NullDescription_TextSearchFallbackLeg(t *testing.T) {
	ctx, store, pool, cleanup := g29NewLiveStore(t)
	if store == nil {
		return
	}
	defer cleanup()

	const token = "g29f7fallback"
	// A long, low-trigram-similarity name that still ILIKE-contains the
	// token, so it is invisible to the primary `%` operator (well below the
	// pg_trgm default 0.3 similarity threshold) but visible to the fallback's
	// substring ILIKE match. A single REPEATED character (e.g. "zzz...z") is
	// NOT sufficient padding: pg_trgm similarity is a Jaccard ratio over
	// DISTINCT trigrams, and a run of one repeated character contributes only
	// ~1 distinct trigram no matter how long the run is, so the token's own
	// internal trigrams still dominate the ratio and similarity stays ABOVE
	// threshold (verified empirically: 0.78, not the assumed "below 0.3" --
	// §11.4.6/§11.4.199). Padding with several random UUIDs on each side
	// contributes many DISTINCT trigrams, driving the ratio down (verified
	// empirically: ~0.07, safely below the 0.3 default threshold).
	pad := func() string {
		return uuid.New().String() + uuid.New().String() + uuid.New().String()
	}
	longName := pad() + "-" + token + "-" + pad()
	id := g29InsertDirectSkill(t, ctx, pool, longName, "unrelated title")

	results, err := store.textSearch(ctx, token, 10)
	if err != nil {
		t.Fatalf("textSearch (fallback leg) with a NULL-description row returned an error: %v", err)
	}
	found := false
	for _, r := range results {
		if r.Skill.ID == id {
			found = true
			if r.Skill.Description != "" {
				t.Errorf("NULL description scanned as %q, want empty string", r.Skill.Description)
			}
		}
	}
	if !found {
		t.Fatalf("NULL-description skill %q missing from textSearch(%q) fallback-leg results=%v (primary leg may not have missed as engineered)", id, token, names(results))
	}
}

// TestG29_NullDescription_VectorSearchLeg is finding 7's VectorSearch guard:
// a NULL-description row that ALSO carries a populated embedding must not
// break the KNN scan.
func TestG29_NullDescription_VectorSearchLeg(t *testing.T) {
	ctx, store, pool, cleanup := g29NewLiveStore(t)
	if store == nil {
		return
	}
	defer cleanup()

	id := g29InsertDirectSkill(t, ctx, pool, "g29.f7.vector.nulldesc", "vector leg null description")
	vec := g29Vec(19)
	g29SetEmbedding(t, ctx, pool, id, vec)

	results, err := store.VectorSearch(ctx, vec, 10)
	if err != nil {
		t.Fatalf("VectorSearch with a NULL-description row returned an error: %v", err)
	}
	found := false
	for _, r := range results {
		if r.Skill.ID == id {
			found = true
			if r.Skill.Description != "" {
				t.Errorf("NULL description scanned as %q, want empty string", r.Skill.Description)
			}
		}
	}
	if !found {
		t.Fatalf("NULL-description embedded skill %q missing from VectorSearch results=%v", id, names(results))
	}
}

// ---------------------------------------------------------------------------
// Finding 4: rows.Err() after each of the three row-iteration loops.
// ---------------------------------------------------------------------------

// g29RowsErrCutoff self-calibrates a context timeout to a fraction of a
// measured baseline duration, so the forced-cancellation call below is
// reliably interrupted PARTWAY through row iteration regardless of host
// speed, rather than relying on a hardcoded constant that could be flaky on a
// faster or slower machine (§11.4.6: no guessed timing constant).
func g29RowsErrCutoff(baseline time.Duration) time.Duration {
	cutoff := baseline / 4
	const floor = 2 * time.Millisecond
	if cutoff < floor {
		return floor
	}
	return cutoff
}

// minBaselineForRowsErrTest is the floor below which this host's query is too
// fast to reliably force a mid-iteration cancellation without risking a flaky
// pass/fail; below it the case honestly SKIPs rather than risk a flake
// (§11.4.6/§11.4.98).
const minBaselineForRowsErrTest = 40 * time.Millisecond

// g29RowsErrPayloadSize and g29RowsErrRowCount are sized from REAL,
// live-DB-measured behaviour (§11.4.6/§11.4.199 -- captured evidence, not a
// guessed constant), NOT from the first (flawed) design of these tests:
//
// The FIRST attempt padded the row's `description` column to force a slow
// query. That FAILED to reach rows.Err() at all: Store's three SQL statements
// each ORDER BY a computed expression (a trigram similarity() over
// name+title+description, or a pgvector distance over embedding) with no
// covering index, so PostgreSQL must fully materialize+sort EVERY qualifying
// row before it can emit even the FIRST one -- confirmed empirically
// (EXPLAIN ANALYZE: a blocking Sort node with startup cost ~= total cost) and
// via direct pgx instrumentation (pool.Query() itself blocked for the ENTIRE
// query duration and returned with the whole result already available;
// rows.Next() then drained it in microseconds). A context cancelled during
// that dominant "still sorting" window is observed by pool.Query()'s OWN
// error return, never reaching the loop -- so a mutation deleting the
// rows.Err() check made NO observable difference (caught before being
// trusted, per §11.4.194: prove the fix against captured evidence, don't
// assume one plausible design reaches the code under test).
//
// The FIX: pad `content` instead of `description`/`name`/`title`. `content`
// feeds NEITHER leg's sort key (textSearch's ORDER BY score / fallback's
// ORDER BY s.name; VectorSearch's ORDER BY embedding distance) -- only the
// SELECT list. This decouples sort cost (cheap: small name/title/description,
// or a fixed-size embedding) from transmission cost (the bulk `content`
// payload), so pool.Query() now returns QUICKLY (confirmed: single-digit
// milliseconds) while the SEPARATE, SLOWER row-by-row transmission of
// g29RowsErrRowCount rows carrying a g29RowsErrPayloadSize-byte `content`
// each gives a genuine, multi-hundred-millisecond in-progress-iteration
// window (confirmed: cancelling partway through leaves rows.Err() reporting
// "context deadline exceeded" AFTER hundreds of rows were already scanned).
const (
	g29RowsErrPayloadSize = 200_000 // bytes of `content` per row
	g29RowsErrRowCount    = 2000
)

// TestG29_RowsErr_TextSearchPrimaryLeg_NotSilentlyMasked is the RED-first
// regression guard for finding 4's textSearch PRIMARY-leg rows.Err() check.
// See g29RowsErrPayloadSize's doc comment for why `content` (not
// `description`) carries the padding. On the pre-fix code (no rows.Err()
// check) the cancelled call returns a partial result slice with a NIL error
// -- masking the failure. Post-fix it returns a non-nil error. A §1.1
// mutation removing the added `if err := rows.Err(); err != nil {...}` check
// makes this test FAIL again.
func TestG29_RowsErr_TextSearchPrimaryLeg_NotSilentlyMasked(t *testing.T) {
	ctx, store, pool, cleanup := g29NewLiveStore(t)
	if store == nil {
		return
	}
	defer cleanup()

	const token = "g29rowserrprimary"
	if _, err := pool.Exec(ctx,
		`INSERT INTO skills (id, name, title, description, content, status, kind)
		 SELECT gen_random_uuid(), $1 || '-' || g, $1, 'd', repeat('x', $3), 'active', 'atomic'
		 FROM generate_series(1, $2) AS g`,
		token, g29RowsErrRowCount, g29RowsErrPayloadSize,
	); err != nil {
		t.Fatalf("bulk-insert %d matching rows: %v", g29RowsErrRowCount, err)
	}

	baseline := time.Now()
	if _, err := store.textSearch(context.Background(), token, g29RowsErrRowCount); err != nil {
		t.Fatalf("uncancelled baseline textSearch call failed unexpectedly: %v", err)
	}
	elapsed := time.Since(baseline)
	t.Logf("uncancelled baseline textSearch (primary leg) over %d rows took %v", g29RowsErrRowCount, elapsed)
	if elapsed < minBaselineForRowsErrTest {
		t.Skipf("baseline textSearch over %d rows took only %v on this host (< %v floor); "+
			"too fast to reliably force a mid-iteration cancellation without risking a flaky test (§11.4.6)",
			g29RowsErrRowCount, elapsed, minBaselineForRowsErrTest)
	}

	cctx, cancel := context.WithTimeout(context.Background(), g29RowsErrCutoff(elapsed))
	defer cancel()
	if _, err := store.textSearch(cctx, token, g29RowsErrRowCount); err == nil {
		t.Fatalf("textSearch under a context cancelled mid-iteration (baseline=%v, cutoff=%v) returned a nil error; "+
			"rows.Err() must surface the cut-short iteration instead of silently truncating the result set", elapsed, g29RowsErrCutoff(elapsed))
	}
}

// TestG29_RowsErr_TextSearchFallbackLeg_NotSilentlyMasked is finding 4's
// textSearch FALLBACK-leg counterpart: the row set is engineered (as in
// TestG29_NullDescription_TextSearchFallbackLeg) so the primary leg misses
// and the fallback leg is the one under test; see g29RowsErrPayloadSize's doc
// comment for why `content` (not `description`) carries the padding.
func TestG29_RowsErr_TextSearchFallbackLeg_NotSilentlyMasked(t *testing.T) {
	ctx, store, pool, cleanup := g29NewLiveStore(t)
	if store == nil {
		return
	}
	defer cleanup()

	const token = "g29rowserrfallback"
	// Low-trigram-similarity names so the primary `%`/ILIKE-on-description
	// query misses every row and the fallback's plain ILIKE-on-name/title is
	// the one exercised. A per-row md5(random()) padding (verified
	// empirically: similarity ~0.07, well below the 0.3 default threshold) is
	// used rather than a repeated single character -- a run of ONE repeated
	// character contributes only ~1 DISTINCT trigram no matter its length, so
	// it does NOT dilute the token's own trigrams below threshold (verified
	// empirically: repeat('z',60) padding measured 0.78 similarity, still
	// ABOVE threshold -- §11.4.6/§11.4.199, this exact miscalibration was
	// caught and corrected while building these tests).
	if _, err := pool.Exec(ctx,
		`INSERT INTO skills (id, name, title, description, content, status, kind)
		 SELECT gen_random_uuid(),
		        md5(random()::text) || md5(random()::text) || md5(random()::text) ||
		        '-' || $1 || '-' || g || '-' ||
		        md5(random()::text) || md5(random()::text) || md5(random()::text),
		        'unrelated title', 'unrelated description', repeat('x', $3), 'active', 'atomic'
		 FROM generate_series(1, $2) AS g`,
		token, g29RowsErrRowCount, g29RowsErrPayloadSize,
	); err != nil {
		t.Fatalf("bulk-insert %d fallback-only-matching rows: %v", g29RowsErrRowCount, err)
	}

	baseline := time.Now()
	if _, err := store.textSearch(context.Background(), token, g29RowsErrRowCount); err != nil {
		t.Fatalf("uncancelled baseline textSearch (fallback leg) call failed unexpectedly: %v", err)
	}
	elapsed := time.Since(baseline)
	t.Logf("uncancelled baseline textSearch (fallback leg) over %d rows took %v", g29RowsErrRowCount, elapsed)
	if elapsed < minBaselineForRowsErrTest {
		t.Skipf("baseline fallback-leg textSearch over %d rows took only %v on this host (< %v floor); "+
			"too fast to reliably force a mid-iteration cancellation without risking a flaky test (§11.4.6)",
			g29RowsErrRowCount, elapsed, minBaselineForRowsErrTest)
	}

	cctx, cancel := context.WithTimeout(context.Background(), g29RowsErrCutoff(elapsed))
	defer cancel()
	if _, err := store.textSearch(cctx, token, g29RowsErrRowCount); err == nil {
		t.Fatalf("textSearch fallback leg under a context cancelled mid-iteration (baseline=%v, cutoff=%v) returned a nil error; "+
			"rows.Err() must surface the cut-short iteration instead of silently truncating the result set", elapsed, g29RowsErrCutoff(elapsed))
	}
}

// TestG29_RowsErr_VectorSearchLeg_NotSilentlyMasked is finding 4's
// VectorSearch counterpart; see g29RowsErrPayloadSize's doc comment for why
// `content` (not `description`) carries the padding.
func TestG29_RowsErr_VectorSearchLeg_NotSilentlyMasked(t *testing.T) {
	ctx, store, pool, cleanup := g29NewLiveStore(t)
	if store == nil {
		return
	}
	defer cleanup()

	insertStart := time.Now()
	if _, err := pool.Exec(ctx,
		`INSERT INTO skills (id, name, title, description, content, status, kind, embedding)
		 SELECT gen_random_uuid(), 'g29rowserrvector-' || g, 'title', 'd', repeat('x', $2), 'active', 'atomic',
		        ('[' || repeat('0.01,', 767) || '0.01]')::vector
		 FROM generate_series(1, $1) AS g`,
		g29RowsErrRowCount, g29RowsErrPayloadSize,
	); err != nil {
		t.Fatalf("bulk-insert %d embedded rows: %v", g29RowsErrRowCount, err)
	}
	t.Logf("bulk-insert of %d embedded rows took %v", g29RowsErrRowCount, time.Since(insertStart))

	probe := g29Vec(41)

	baseline := time.Now()
	if _, err := store.VectorSearch(context.Background(), probe, g29RowsErrRowCount); err != nil {
		t.Fatalf("uncancelled baseline VectorSearch call failed unexpectedly: %v", err)
	}
	elapsed := time.Since(baseline)
	t.Logf("uncancelled baseline VectorSearch over %d rows took %v", g29RowsErrRowCount, elapsed)
	if elapsed < minBaselineForRowsErrTest {
		t.Skipf("baseline VectorSearch over %d rows took only %v on this host (< %v floor); "+
			"too fast to reliably force a mid-iteration cancellation without risking a flaky test (§11.4.6)",
			g29RowsErrRowCount, elapsed, minBaselineForRowsErrTest)
	}

	cctx, cancel := context.WithTimeout(context.Background(), g29RowsErrCutoff(elapsed))
	defer cancel()
	if _, err := store.VectorSearch(cctx, probe, g29RowsErrRowCount); err == nil {
		t.Fatalf("VectorSearch under a context cancelled mid-iteration (baseline=%v, cutoff=%v) returned a nil error; "+
			"rows.Err() must surface the cut-short iteration instead of silently truncating the result set", elapsed, g29RowsErrCutoff(elapsed))
	}
}
