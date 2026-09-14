package db

// Live-DB regression suite for G53 — WaitForVectorIndexReady catalog-query
// correctness (§11.4.115 RED-on-the-broken-query + §11.4.135 permanent guard).
//
// The defect (confirmed against a live PostgreSQL 16 / pgvector instance): the
// readiness probe ran `SELECT indisvalid FROM pg_index WHERE indexrelname = $1`,
// but pg_index has NO indexrelname column (the index name lives on pg_class;
// pg_index only carries indexrelid). Postgres rejects it with
// `column "indexrelname" does not exist`, and the previous swallow-everything
// `continue` masked that error, so the function could ONLY ever time out — it
// never detected a ready index. The fix joins pg_index to pg_class on
// indexrelid and matches c.relname, and surfaces a genuine query error instead
// of spinning on it (§11.4.201).
//
// Honest scope note (§11.4.6): WaitForVectorIndexReady currently has ZERO
// production callers (a G29-class unwired pgvector surface). These tests
// exercise the exported function directly against a real database — they prove
// a real latent correctness bug is fixed, not a live application code path.
//
// Topology dispatch (§11.4.3): gated on skipIfNoTestDB — absent a configured
// live PostgreSQL, every case honestly t.Skip()s; none fake a PASS. Each test
// provisions its OWN throwaway database (createThrowawayDB) and is safe under
// -count=N and in parallel (§11.4.98).

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// G53-READY — the primary RED→GREEN discriminator. With the real
// idx_skills_embedding present and valid (created by migration 001), a correct
// probe detects readiness on its first poll tick and returns nil PROMPTLY —
// well within the timeout. Under the pre-fix broken query the probe errors on
// every tick and the function can only ever time out, so this test FAILs (RED)
// on the broken query and PASSes (GREEN) on the fix.
func TestG53_WaitForVectorIndexReady_ReadyIndexReturnsPromptly(t *testing.T) {
	admin, ok := skipIfNoTestDB(t)
	if !ok {
		return
	}
	ctx := context.Background()

	dbCfg, cleanup := createThrowawayDB(t, admin)
	defer cleanup()

	pool, err := New(dbCfg)
	if err != nil {
		t.Fatalf("New(dbCfg): %v", err)
	}
	defer pool.Close()

	// The REAL migrations create the skills table AND
	// `CREATE INDEX idx_skills_embedding ON skills USING hnsw(...)`
	// (migrations/001_initial.up.sql). A normally-created HNSW index over the
	// empty table is immediately indisvalid = true.
	if err := Migrate(ctx, pool, realMigrationsDir); err != nil {
		t.Fatalf("Migrate (full real migrations dir): %v", err)
	}

	// The internal poll ticker fires every 2s, so the earliest a ready index
	// can be detected is ~2s. A 10s timeout leaves generous margin for one or
	// two ticks while still proving the function does NOT run to the deadline.
	const timeout = 10 * time.Second
	start := time.Now()
	err = WaitForVectorIndexReady(ctx, pool, "skills", timeout)
	elapsed := time.Since(start)

	if err != nil {
		// On the broken query this is a timeout after ~10s (old swallow-all
		// handling) or an immediately-surfaced query error (broken query under
		// the fixed error handling) — either way a non-nil error here is the
		// RED signal the pg_index.indexrelname query is present.
		t.Fatalf("WaitForVectorIndexReady(skills) returned error for a present, valid index "+
			"after %s: %v\n(RED on the broken `pg_index.indexrelname` query — the index "+
			"exists and is valid, a correct probe MUST return nil)", elapsed, err)
	}
	if elapsed >= timeout {
		t.Fatalf("WaitForVectorIndexReady(skills) took %s (>= timeout %s): it ran to the "+
			"deadline instead of detecting the ready index promptly", elapsed, timeout)
	}
	t.Logf("GREEN: WaitForVectorIndexReady(skills) returned nil in %s for the valid "+
		"idx_skills_embedding (well under the %s timeout)", elapsed, timeout)
}

// G53-ABSENT — designed-behavior guard: a genuinely-absent index name is the
// real "not built yet" condition (zero catalog rows → pgx.ErrNoRows), so the
// function keeps polling and returns the TIMEOUT error — never a masked or
// mis-classified query error. "absent_index_tbl" is a valid SQL identifier
// (passes validateTableName) whose derived index name idx_absent_index_tbl_
// embedding does not exist. This asserts the ErrNoRows "keep waiting" branch is
// taken and the timeout is reported with its own clear message.
func TestG53_WaitForVectorIndexReady_AbsentIndexTimesOutNotMaskedError(t *testing.T) {
	admin, ok := skipIfNoTestDB(t)
	if !ok {
		return
	}
	ctx := context.Background()

	dbCfg, cleanup := createThrowawayDB(t, admin)
	defer cleanup()

	pool, err := New(dbCfg)
	if err != nil {
		t.Fatalf("New(dbCfg): %v", err)
	}
	defer pool.Close()

	if err := Migrate(ctx, pool, realMigrationsDir); err != nil {
		t.Fatalf("Migrate (full real migrations dir): %v", err)
	}

	// Short timeout: enough for ≥1 poll tick (2s) to hit the absent-index /
	// ErrNoRows branch, then time out — keeps this designed-timeout guard fast.
	const timeout = 5 * time.Second
	start := time.Now()
	err = WaitForVectorIndexReady(ctx, pool, "absent_index_tbl", timeout)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("WaitForVectorIndexReady(absent_index_tbl) returned nil for a non-existent "+
			"index (elapsed %s): an absent index is NOT ready and must time out", elapsed)
	}
	// It must be the deadline/timeout — the ErrNoRows path was taken and the
	// function polled until the deadline, NOT a surfaced/masked query error.
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitForVectorIndexReady(absent_index_tbl) error does not wrap "+
			"context.DeadlineExceeded (elapsed %s): got %v\n(an absent index must reach the "+
			"timeout via the pgx.ErrNoRows keep-waiting branch, not be surfaced as a query error)",
			elapsed, err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "timeout waiting for index") {
		t.Errorf("absent-index error message = %q, want it to name the timeout "+
			"(\"timeout waiting for index ...\")", msg)
	}
	// Defence against a regression to the broken query: its signature error text
	// must never appear, and the error must not be the surfaced-query-error form.
	if strings.Contains(msg, "indexrelname") {
		t.Errorf("absent-index error mentions %q — the broken pg_index.indexrelname "+
			"query has regressed: %q", "indexrelname", msg)
	}
	if strings.Contains(msg, "query readiness of index") {
		t.Errorf("absent-index error is the surfaced-query-error form %q — an absent "+
			"index must be the ErrNoRows keep-waiting/timeout path, not a query error", msg)
	}
	t.Logf("GREEN: WaitForVectorIndexReady(absent_index_tbl) timed out correctly in %s "+
		"via the ErrNoRows keep-waiting branch: %v", elapsed, err)
}
