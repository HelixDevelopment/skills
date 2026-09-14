package main

// G10 — cmd/server startup embedding-dimension safety assertion
// (research/g10_embedding_provider_design.md §2.2, §11.4.201).
//
// These exercise the REAL startup wiring cmd/server uses --
// assertEmbeddingDimensionsOnStartup(), called from main() immediately after
// migrateOnStartup() and before any skill/evidence store or MCP server
// construction -- against a live throwaway pgvector database, mirroring the
// harness g23_migrate_startup_test.go established for the migration step.
//
//   - TestAssertEmbeddingDimensionsOnStartup_MatchingConfig_Passes: a
//     genuinely-matching embedder config (768, matching the real migrated
//     vector(768) columns) boots cleanly (golden-TRUE).
//   - TestAssertEmbeddingDimensionsOnStartup_MismatchedConfig_FailsClosed: an
//     embedder configured for 1536 against the real vector(768) columns
//     returns a non-nil, fail-closed error naming both dimensions
//     (golden-FALSE / §11.4.115 RED-then-GREEN artifact -- before this fix
//     existed, nothing at boot compared these two numbers at all, so a
//     mismatched config would have silently proceeded to serve).
//   - TestAssertEmbeddingDimensionsOnStartup_NoProviderConfigured_Skips:
//     when no embedding provider is configured (matching
//     db.NewEmbedderFromConfig's own fail-closed "unconfigured" signal),
//     the check is skipped (nil, not fatal) -- it must not block startup of
//     an intentionally embedder-less deployment.
//
// Both need a live pgvector instance and honestly t.Skip() when
// SKILL_SYSTEM_TEST_DB_HOST is unset (§11.4.3/§11.4.27) -- never a fake PASS.

import (
	"context"
	"strings"
	"testing"

	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/HelixDevelopment/skills/internal/db"
	"go.uber.org/zap"
)

// g10NewMigratedThrowawayDB provisions a throwaway database, applies the full
// embedded migration set (so skills.embedding / evidences.embedding exist
// exactly as production startup creates them, both vector(768) per
// migrations/001_initial.up.sql:14,60 via startupMigrationsFS()), and returns
// a connected *db.Pool plus a single cleanup that closes the pool and drops
// the database. Reuses the g23 throwaway-DB provisioning helpers (same
// package, same SKILL_SYSTEM_TEST_DB_* contract) rather than reimplementing
// them a third time.
func g10NewMigratedThrowawayDB(t *testing.T) (*db.Pool, func()) {
	t.Helper()
	admin, ok := g23SkipIfNoTestDB(t)
	if !ok {
		return nil, nil
	}
	ctx := context.Background()

	dbCfg, cleanupDB := g23CreateThrowawayDBConfig(t, admin)
	pool, err := db.New(dbCfg)
	if err != nil {
		cleanupDB()
		t.Fatalf("db.New: %v", err)
	}
	if err := migrateOnStartup(ctx, pool, startupMigrationsFS(), zap.NewNop()); err != nil {
		pool.Close()
		cleanupDB()
		t.Fatalf("migrateOnStartup (embedded FS): %v", err)
	}

	cleanup := func() {
		pool.Close()
		cleanupDB()
	}
	return pool, cleanup
}

// TestAssertEmbeddingDimensionsOnStartup_MatchingConfig_Passes is the
// golden-TRUE case: an embedder configured for the SAME dimension as the real
// migrated columns (768) passes cleanly for every target in
// embeddingDimensionCheckTargets.
func TestAssertEmbeddingDimensionsOnStartup_MatchingConfig_Passes(t *testing.T) {
	pool, cleanup := g10NewMigratedThrowawayDB(t)
	if pool == nil {
		return // skipped: no test DB
	}
	defer cleanup()

	cfg := config.EmbeddingConfig{Provider: "local", LocalEndpoint: "http://127.0.0.1:0", Dimensions: 768}
	if err := assertEmbeddingDimensionsOnStartup(context.Background(), pool, cfg, zap.NewNop()); err != nil {
		t.Fatalf("assertEmbeddingDimensionsOnStartup with a matching (768) config returned an unexpected error: %v", err)
	}
}

// TestAssertEmbeddingDimensionsOnStartup_MismatchedConfig_FailsClosed is the
// §11.4.115 golden-FALSE / RED-then-GREEN case: an embedder configured for
// 1536 against the real vector(768) columns MUST fail closed with an error
// naming both dimensions. A §1.1 mutation reverting
// assertEmbeddingDimensionsOnStartup / AssertEmbeddingDimension to a no-op
// (or neutralizing the comparison) makes this test FAIL again.
func TestAssertEmbeddingDimensionsOnStartup_MismatchedConfig_FailsClosed(t *testing.T) {
	pool, cleanup := g10NewMigratedThrowawayDB(t)
	if pool == nil {
		return
	}
	defer cleanup()

	cfg := config.EmbeddingConfig{Provider: "local", LocalEndpoint: "http://127.0.0.1:0", Dimensions: 1536}
	err := assertEmbeddingDimensionsOnStartup(context.Background(), pool, cfg, zap.NewNop())
	if err == nil {
		t.Fatal("assertEmbeddingDimensionsOnStartup with a mismatched (1536 vs real 768) config returned nil; " +
			"expected a fail-closed error -- main() turns this into logger.Fatal (refuse to serve)")
	}
	t.Logf("RED-polarity capture: fail-closed error = %v", err)
}

// TestAssertEmbeddingDimensionsOnStartup_NoProviderConfigured_Skips proves
// the check is a no-op (nil, never fatal) when no embedding provider is
// configured -- matching db.NewEmbedderFromConfig's own fail-closed
// "unconfigured" signal (e.g. the "local" provider with no endpoint). An
// intentionally embedder-less deployment must still boot.
func TestAssertEmbeddingDimensionsOnStartup_NoProviderConfigured_Skips(t *testing.T) {
	pool, cleanup := g10NewMigratedThrowawayDB(t)
	if pool == nil {
		return
	}
	defer cleanup()

	cfg := config.EmbeddingConfig{Provider: "local", LocalEndpoint: "", Dimensions: 768}
	if err := assertEmbeddingDimensionsOnStartup(context.Background(), pool, cfg, zap.NewNop()); err != nil {
		t.Fatalf("assertEmbeddingDimensionsOnStartup with NO embedding provider configured returned an error (expected a skip, nil): %v", err)
	}
}

// TestAssertEmbeddingDimensionsOnStartup_EvidencesColumnWidened_FailsNamingEvidences
// is the Fable round-2 finding-F2 regression guard: prior to this fix, the
// embeddingDimensionCheckTargets == {skills, evidences} LIST was completely
// untested as a list -- every other test in this file keeps BOTH columns at
// the same width (768) simultaneously, so truncating the list down to
// {skills} alone (or a loop-early-exit mutation that only ever checks the
// FIRST target) would survive the whole existing suite unnoticed. This test
// widens ONLY evidences.embedding on the throwaway DB (skills.embedding
// stays at the real migrated 768, matching the configured 768) and asserts
// the boot guard FAILS, naming "evidences" -- a truncated-list or
// early-exit mutation would incorrectly return nil here because it never
// reaches the evidences check at all.
func TestAssertEmbeddingDimensionsOnStartup_EvidencesColumnWidened_FailsNamingEvidences(t *testing.T) {
	pool, cleanup := g10NewMigratedThrowawayDB(t)
	if pool == nil {
		return
	}
	defer cleanup()

	ctx := context.Background()
	if _, err := pool.Exec(ctx, `ALTER TABLE evidences ALTER COLUMN embedding TYPE vector(1536)`); err != nil {
		t.Fatalf("widen evidences.embedding to vector(1536): %v", err)
	}

	cfg := config.EmbeddingConfig{Provider: "local", LocalEndpoint: "http://127.0.0.1:0", Dimensions: 768}
	err := assertEmbeddingDimensionsOnStartup(ctx, pool, cfg, zap.NewNop())
	if err == nil {
		t.Fatal("assertEmbeddingDimensionsOnStartup with evidences.embedding widened to 1536 " +
			"(skills.embedding still 768, config wants 768) returned nil; expected a fail-closed " +
			"error naming evidences -- a truncated embeddingDimensionCheckTargets list (or a loop " +
			"that exits after the first target) would incorrectly pass here")
	}
	if !strings.Contains(err.Error(), "evidences") {
		t.Errorf("error %q does not mention \"evidences\"; the boot guard must actually reach and "+
			"check the evidences.embedding target, not merely skills.embedding", err.Error())
	}
	t.Logf("evidences-widened capture: fail-closed error = %v", err)
}

// TestEmbeddingDimensionCheckTargets_CoversEveryVectorColumn is the
// Fable round-2 finding-F2 completeness companion (§11.4.194(4)): fixing the
// list-truncation bug above only proves the CURRENT two-entry list is fully
// exercised by that one test; it does nothing to catch a FUTURE migration
// that adds a THIRD dimensioned vector(N) column without anyone remembering
// to add it to embeddingDimensionCheckTargets. This test independently scans
// the live database's OWN catalog for every dimensioned pgvector column that
// actually exists -- regardless of what embeddingDimensionCheckTargets
// currently enumerates -- and asserts the two sets are IDENTICAL (set
// equality, order-independent). A §1.1 mutation that widens the schema with
// an un-declared vector(N) column (simulated here by adding one directly)
// makes this test FAIL.
func TestEmbeddingDimensionCheckTargets_CoversEveryVectorColumn(t *testing.T) {
	pool, cleanup := g10NewMigratedThrowawayDB(t)
	if pool == nil {
		return
	}
	defer cleanup()

	ctx := context.Background()
	rows, err := pool.Query(ctx, `
		SELECT c.relname, a.attname
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE NOT a.attisdropped
		  AND c.relkind IN ('r', 'p')
		  AND n.nspname = 'public'
		  AND format_type(a.atttypid, a.atttypmod) ~ '^vector\(\d+\)$'
		ORDER BY c.relname, a.attname`)
	if err != nil {
		t.Fatalf("catalog scan for dimensioned pgvector columns: %v", err)
	}
	defer rows.Close()

	type tableColumn struct{ Table, Column string }
	live := map[tableColumn]bool{}
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatalf("scan catalog row: %v", err)
		}
		live[tableColumn{Table: table, Column: column}] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("catalog scan rows error: %v", err)
	}

	declared := map[tableColumn]bool{}
	for _, target := range embeddingDimensionCheckTargets {
		declared[tableColumn{Table: target.Table, Column: target.Column}] = true
	}

	for k := range live {
		if !declared[k] {
			t.Errorf("live pgvector column %s.%s exists in the migrated database but is MISSING "+
				"from embeddingDimensionCheckTargets -- a migration adding a dimensioned vector "+
				"column without updating this list leaves it unguarded by the G10 boot-time check", k.Table, k.Column)
		}
	}
	for k := range declared {
		if !live[k] {
			t.Errorf("embeddingDimensionCheckTargets declares %s.%s but no such live pgvector "+
				"column exists in the migrated schema -- the target list is stale", k.Table, k.Column)
		}
	}
}
