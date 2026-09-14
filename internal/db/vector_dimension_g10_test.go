package db

// G10 — boot-time embedding-dimension safety assertion, live-DB regression
// guards (research/g10_embedding_provider_design.md §2.2, cases 13/14; M1).
//
// These run against a REAL, reachable pgvector/pgvector:pg16-class PostgreSQL
// instance -- the pg_attribute/format_type catalog lookup under test has no
// faithful in-memory substitute (same documented boundary as
// testdb_helper_test.go's other live-DB suites). Topology detection (§11.4.3):
// every case honestly t.Skip()s with a specific reason when
// SKILL_SYSTEM_TEST_DB_HOST is unset -- never a fake PASS.
//
// RED/GREEN: TestAssertEmbeddingDimension_Mismatch_FailsClosed is the
// §11.4.115 golden-FALSE case -- against a throwaway DB migrated to
// 001_initial (vector(768) columns) it asserts a DELIBERATELY WRONG expected
// dimension (1536) and requires AssertEmbeddingDimension to return a non-nil
// error naming both dimensions. Before this fix existed, no function of this
// name existed at all, so this test (and AssertEmbeddingDimension itself) is
// the RED-then-GREEN artifact: a §1.1 mutation that flips the mismatch
// comparison from `!=` to `==` (or deletes the check entirely, always
// returning nil) makes this test FAIL again.
// TestAssertEmbeddingDimension_Match_Passes is the golden-TRUE companion: the
// REAL column dimension (768, from the applied migration) checked against the
// SAME value must pass cleanly.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// g10RealMigrationsDir is the on-disk migrations directory this test drives,
// relative to internal/db (this package) -- the SAME directory
// testdb_helper_test.go's realMigrationsDir points at and cmd/server/main.go's
// production startup path applies from the embedded FS equivalent of.
const g10RealMigrationsDir = realMigrationsDir

// g10NewMigratedThrowawayDB provisions a throwaway database, applies the full
// real migration set (so skills.embedding / evidences.embedding exist exactly
// as production creates them, both vector(768) per
// migrations/001_initial.up.sql:14,60), and returns a connected *Pool plus a
// single cleanup that closes the pool and drops the database.
func g10NewMigratedThrowawayDB(t *testing.T) (context.Context, *Pool, func()) {
	t.Helper()
	admin, ok := skipIfNoTestDB(t)
	if !ok {
		return nil, nil, nil
	}
	ctx := context.Background()

	dbCfg, cleanupDB := createThrowawayDB(t, admin)
	pool, err := New(dbCfg)
	if err != nil {
		cleanupDB()
		t.Fatalf("db.New(dbCfg): %v", err)
	}
	if err := Migrate(ctx, pool, g10RealMigrationsDir); err != nil {
		pool.Close()
		cleanupDB()
		t.Fatalf("Migrate (full real migrations dir): %v", err)
	}

	cleanup := func() {
		pool.Close()
		cleanupDB()
	}
	return ctx, pool, cleanup
}

// TestQueryColumnVectorDimension_ReadsRealColumnDimension proves the query
// reads the ACTUAL, live-migrated column width (768, from
// migrations/001_initial.up.sql) -- not a hardcoded constant -- for both
// embedding columns the migration declares.
func TestQueryColumnVectorDimension_ReadsRealColumnDimension(t *testing.T) {
	ctx, pool, cleanup := g10NewMigratedThrowawayDB(t)
	if pool == nil {
		return // skipped: no test DB
	}
	defer cleanup()

	for _, tc := range []struct{ table, column string }{
		{"skills", "embedding"},
		{"evidences", "embedding"},
	} {
		dim, err := QueryColumnVectorDimension(ctx, pool, tc.table, tc.column)
		if err != nil {
			t.Fatalf("QueryColumnVectorDimension(%s.%s): unexpected error: %v", tc.table, tc.column, err)
		}
		if dim != 768 {
			t.Errorf("QueryColumnVectorDimension(%s.%s) = %d, want 768 (the REAL migrated column width)", tc.table, tc.column, dim)
		}
	}
}

// TestQueryColumnVectorDimension_UnknownColumn_Errors proves a nonexistent
// column is reported as an error (pgx.ErrNoRows path), never silently
// resolved to a zero/default dimension.
func TestQueryColumnVectorDimension_UnknownColumn_Errors(t *testing.T) {
	ctx, pool, cleanup := g10NewMigratedThrowawayDB(t)
	if pool == nil {
		return
	}
	defer cleanup()

	if _, err := QueryColumnVectorDimension(ctx, pool, "skills", "no_such_column"); err == nil {
		t.Fatal("QueryColumnVectorDimension(skills.no_such_column): expected a non-nil error for a nonexistent column, got nil")
	}
}

// TestQueryColumnVectorDimension_NonVectorColumn_Errors proves a real,
// existing, but non-pgvector column (e.g. skills.name, a TEXT column) is
// rejected rather than silently reporting some meaningless "dimension" --
// this guard is specifically about pgvector columns.
func TestQueryColumnVectorDimension_NonVectorColumn_Errors(t *testing.T) {
	ctx, pool, cleanup := g10NewMigratedThrowawayDB(t)
	if pool == nil {
		return
	}
	defer cleanup()

	if _, err := QueryColumnVectorDimension(ctx, pool, "skills", "name"); err == nil {
		t.Fatal("QueryColumnVectorDimension(skills.name): expected a non-nil error for a non-pgvector column, got nil")
	}
}

// TestAssertEmbeddingDimension_Match_Passes is the golden-TRUE case: the
// REAL, live-migrated column dimension (768) checked against the SAME wanted
// dimension must pass cleanly for both embedding columns.
func TestAssertEmbeddingDimension_Match_Passes(t *testing.T) {
	ctx, pool, cleanup := g10NewMigratedThrowawayDB(t)
	if pool == nil {
		return
	}
	defer cleanup()

	for _, tc := range []struct{ table, column string }{
		{"skills", "embedding"},
		{"evidences", "embedding"},
	} {
		if err := AssertEmbeddingDimension(ctx, pool, tc.table, tc.column, 768); err != nil {
			t.Errorf("AssertEmbeddingDimension(%s.%s, want=768) returned an unexpected error against a genuinely-matching column: %v", tc.table, tc.column, err)
		}
	}
}

// TestAssertEmbeddingDimension_Mismatch_FailsClosed is the §11.4.115
// golden-FALSE / RED-then-GREEN case: asserting a WRONG expected dimension
// (1536) against the real vector(768) column MUST fail closed with a
// descriptive error naming both dimensions. A §1.1 mutation that neutralizes
// the `dbDim != wantDim` comparison (e.g. flips it to `==`, or deletes the
// branch so the function always returns nil) makes this test FAIL.
func TestAssertEmbeddingDimension_Mismatch_FailsClosed(t *testing.T) {
	ctx, pool, cleanup := g10NewMigratedThrowawayDB(t)
	if pool == nil {
		return
	}
	defer cleanup()

	const wrongDim = 1536
	err := AssertEmbeddingDimension(ctx, pool, "skills", "embedding", wrongDim)
	if err == nil {
		t.Fatal("AssertEmbeddingDimension(skills.embedding, want=1536) against a real vector(768) column returned nil; " +
			"expected a fail-closed error naming both the real (768) and wanted (1536) dimensions")
	}
	for _, want := range []string{"768", "1536"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("mismatch error %q does not mention %q; the error must name BOTH dimensions and their sources", err.Error(), want)
		}
	}
	t.Logf("RED-polarity capture: fail-closed error = %v", err)
}

// TestQueryColumnVectorDimension_ResolvesViaSearchPath_NotAmbiguousAcrossSchemas
// is the Fable round-2 finding-F1 regression guard. It reproduces the exact
// divergence the review flagged -- "the guard's global-relname resolution
// != the app's search_path resolution" -- with a scenario proven, live
// against a real Postgres instance (§11.4.199 exact-reproduction; never
// assumed), to make the pre-fix query DETERMINISTICALLY wrong, not merely
// theoretically ambiguous:
//
//   - appschema.skills(embedding vector(768)) is the relation the
//     CONNECTION'S OWN search_path resolves the unqualified name "skills"
//     to (set via `ALTER DATABASE ... SET search_path = appschema, public`,
//     exactly how a real multi-schema deployment configures it) -- i.e. the
//     SAME relation the application itself would read/write for an
//     unqualified "FROM skills" reference under this search_path.
//   - public.skills(embedding vector(1536)) is a same-named DECOY sitting
//     in the default 'public' schema, a DIFFERENT dimension.
//
// Under the pre-fix bare-relname query (`c.relname = $1 AND a.attname = $2`,
// no schema pin), PostgreSQL resolves the relname ambiguity via
// pg_class_relname_nsp_index -- ordered by (relname, relnamespace) -- and
// 'public' always has a lower, FIXED system namespace oid than any
// user-created schema, so the pre-fix query ALWAYS picks public.skills
// (1536) here, regardless of search_path -- verified empirically (`EXPLAIN
// (COSTS OFF)` confirms the index-scan plan; direct SQL against this exact
// setup returns vector(1536) for the bare-relname form and vector(768) for
// the to_regclass form). The fix's to_regclass($1) resolution follows
// search_path exactly as the application's own unqualified queries do, so
// it correctly resolves appschema.skills (768). A §1.1 mutation reverting
// to the bare-relname query makes this test FAIL (returns 1536, the decoy,
// instead of 768).
func TestQueryColumnVectorDimension_ResolvesViaSearchPath_NotAmbiguousAcrossSchemas(t *testing.T) {
	admin, ok := skipIfNoTestDB(t)
	if !ok {
		return // skipped: no test DB
	}
	ctx := context.Background()

	dbCfg, cleanupDB := createThrowawayDB(t, admin)
	defer cleanupDB()

	setupPool, err := New(dbCfg)
	if err != nil {
		t.Fatalf("db.New(dbCfg) for setup: %v", err)
	}

	// The relation the connection's OWN search_path resolves "skills" to --
	// what the application itself would use.
	if _, err := setupPool.Exec(ctx, `CREATE SCHEMA appschema`); err != nil {
		setupPool.Close()
		t.Fatalf("create schema appschema: %v", err)
	}
	if _, err := setupPool.Exec(ctx, `CREATE TABLE appschema.skills (embedding vector(768))`); err != nil {
		setupPool.Close()
		t.Fatalf("create appschema.skills(embedding vector(768)): %v", err)
	}
	// The DECOY: a same-named, differently-sized relation in 'public' --
	// public's namespace oid is fixed and always lower than any
	// user-created schema's, so a bare-relname resolution's namespace-oid
	// tiebreak always prefers it, independent of search_path.
	if _, err := setupPool.Exec(ctx, `CREATE TABLE public.skills (embedding vector(1536))`); err != nil {
		setupPool.Close()
		t.Fatalf("create decoy public.skills(embedding vector(1536)): %v", err)
	}
	if _, err := setupPool.Exec(ctx, fmt.Sprintf(
		"ALTER DATABASE %s SET search_path = appschema, public", pgx.Identifier{dbCfg.Database}.Sanitize(),
	)); err != nil {
		setupPool.Close()
		t.Fatalf("ALTER DATABASE ... SET search_path: %v", err)
	}
	setupPool.Close()

	// A FRESH pool, opened strictly AFTER the ALTER DATABASE commits, so
	// every connection it establishes is a brand-new session that picks up
	// the new default search_path (ALTER DATABASE ... SET only takes effect
	// for sessions started after it runs -- verified empirically against
	// this exact setup, §11.4.199).
	pool, err := New(dbCfg)
	if err != nil {
		t.Fatalf("db.New(dbCfg) post-ALTER: %v", err)
	}
	defer pool.Close()

	dim, err := QueryColumnVectorDimension(ctx, pool, "skills", "embedding")
	if err != nil {
		t.Fatalf("QueryColumnVectorDimension(skills.embedding) with a search_path-preferred "+
			"appschema.skills(768) and a same-named public.skills(1536) decoy: unexpected error: %v", err)
	}
	if dim != 768 {
		t.Errorf("QueryColumnVectorDimension(skills.embedding) = %d, want 768 (appschema.skills, "+
			"the relation this connection's OWN search_path resolves \"skills\" to -- exactly what "+
			"the application itself would read/write). Got %d instead, which is the public.skills "+
			"DECOY's dimension -- proof the query resolved the WRONG relation, diverging from the "+
			"application's own search_path-based resolution", dim, dim)
	}
}

// TestAssertEmbeddingDimension_UnknownColumn_FailsClosed proves an
// unresolvable column dimension (the column does not exist) is ALSO
// fail-closed -- §11.4.201(4): an unresolvable signal refuses, it never
// assumes OK.
func TestAssertEmbeddingDimension_UnknownColumn_FailsClosed(t *testing.T) {
	ctx, pool, cleanup := g10NewMigratedThrowawayDB(t)
	if pool == nil {
		return
	}
	defer cleanup()

	if err := AssertEmbeddingDimension(ctx, pool, "skills", "no_such_column", 768); err == nil {
		t.Fatal("AssertEmbeddingDimension(skills.no_such_column): expected a non-nil, fail-closed error for an unresolvable column, got nil")
	}
}
