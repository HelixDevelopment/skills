package db

// Migration-acceptance suite for P1.T1 (R16 granularity schema migration).
// See research/p1t1_granularity_schema_migration.md §4.1 for the full case
// table (M1-M10 + Mμ1-Mμ3). This file covers the 8 cases that need only a
// *db.Pool / raw SQL, not the skill.Store layer: M1, M2, M3, M4, M5, M7, M8,
// M9. M6 (kind/optional/sort_order round-trip through skill.Store) and M10
// (seed TOML import + validate_dag.py) live in
// internal/skill/migration_granularity_test.go, which can import this
// package's exported helpers... except these test helpers are unexported and
// package-scoped (testdb_helper_test.go), so M6/M10 re-implement the same
// throwaway-DB provisioning locally rather than importing _test.go symbols
// across packages (Go does not allow that).
//
// Every case here is gated on skipIfNoTestDB — absent a configured live
// PostgreSQL, every case honestly t.Skip()s (§11.4.3/§11.4.27); none fake a
// PASS.

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// M1 — migrate up on a fresh pgvector-capable DB applies 001+002 clean;
// schema_migrations records version 2, no error.
func TestP1T1Migration_M1_MigrateUpAppliesCleanOnFreshDB(t *testing.T) {
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

	// Staged to exactly 001+002 (not realMigrationsDir directly): this case
	// asserts the 001->002 upgrade contract specifically (schema_migrations
	// records version 2). realMigrationsDir now also carries 003_pg_trgm.sql
	// (P1.T1 N6 remediation -- migrations/003_pg_trgm.up.sql adds pg_trgm for
	// Store.Search, see internal/skill/kind_read_paths_granularity_test.go),
	// so asserting against the real, ever-growing directory here would make
	// this test re-break on every future migration addition; staging keeps it
	// scoped to the exact upgrade path M1-M9d exist to validate. The real
	// directory's end-to-end apply is separately proven by
	// TestP1T1N6_KindAwareReadPathsWorkLive (internal/skill package) and the
	// manual db.Migrate call in cmd/server/main.go.
	if err := Migrate(ctx, pool, stageMigrationsDir(t, "001", "002")); err != nil {
		t.Fatalf("Migrate (001+002): %v", err)
	}

	version, err := CurrentMigrationVersion(ctx, pool)
	if err != nil {
		t.Fatalf("CurrentMigrationVersion: %v", err)
	}
	if version != 2 {
		t.Errorf("CurrentMigrationVersion = %d, want 2 (both 001 and 002 applied)", version)
	}
}

// M2 — post-002 schema shape: skills.kind (+ CHECK + idx_skills_kind);
// skill_dependencies 6-value CHECK, optional, sort_order, triple PK.
func TestP1T1Migration_M2_SchemaShapeMatchesDesign(t *testing.T) {
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

	// skills.kind column: present, NOT NULL, default 'atomic'.
	var dataType, isNullable, columnDefault string
	err = pool.QueryRow(ctx, `
		SELECT data_type, is_nullable, column_default
		FROM information_schema.columns
		WHERE table_name = 'skills' AND column_name = 'kind'
	`).Scan(&dataType, &isNullable, &columnDefault)
	if err != nil {
		t.Fatalf("query skills.kind column: %v", err)
	}
	if isNullable != "NO" {
		t.Errorf("skills.kind is_nullable = %q, want NO", isNullable)
	}
	if !strings.Contains(columnDefault, "atomic") {
		t.Errorf("skills.kind column_default = %q, want it to contain 'atomic'", columnDefault)
	}

	// idx_skills_kind exists.
	var idxCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM pg_indexes WHERE indexname = 'idx_skills_kind'`).Scan(&idxCount); err != nil {
		t.Fatalf("query idx_skills_kind: %v", err)
	}
	if idxCount != 1 {
		t.Errorf("idx_skills_kind index count = %d, want 1", idxCount)
	}

	// skills_kind_check CHECK constraint contains all 3 values.
	var kindCheckDef string
	if err := pool.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'skills_kind_check'`).Scan(&kindCheckDef); err != nil {
		t.Fatalf("query skills_kind_check definition: %v", err)
	}
	for _, want := range []string{"atomic", "composite", "umbrella"} {
		if !strings.Contains(kindCheckDef, want) {
			t.Errorf("skills_kind_check definition = %q, want it to contain %q", kindCheckDef, want)
		}
	}

	// skill_dependencies.optional / sort_order columns.
	var optionalNullable string
	if err := pool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns WHERE table_name = 'skill_dependencies' AND column_name = 'optional'`).Scan(&optionalNullable); err != nil {
		t.Fatalf("query skill_dependencies.optional column: %v", err)
	}
	if optionalNullable != "NO" {
		t.Errorf("skill_dependencies.optional is_nullable = %q, want NO", optionalNullable)
	}
	var sortOrderCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM information_schema.columns WHERE table_name = 'skill_dependencies' AND column_name = 'sort_order'`).Scan(&sortOrderCount); err != nil {
		t.Fatalf("query skill_dependencies.sort_order column: %v", err)
	}
	if sortOrderCount != 1 {
		t.Errorf("skill_dependencies.sort_order column count = %d, want 1", sortOrderCount)
	}

	// relation_type CHECK widened to the 6-value set.
	var relCheckDef string
	if err := pool.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'skill_dependencies_relation_type_check'`).Scan(&relCheckDef); err != nil {
		t.Fatalf("query skill_dependencies_relation_type_check definition: %v", err)
	}
	for _, want := range []string{"requires", "extends", "recommends", "composes", "related_to", "alternative_to"} {
		if !strings.Contains(relCheckDef, want) {
			t.Errorf("skill_dependencies_relation_type_check definition = %q, want it to contain %q", relCheckDef, want)
		}
	}

	// PK widened to the triple (skill_id, depends_on, relation_type).
	var pkDef string
	if err := pool.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'skill_dependencies_pkey'`).Scan(&pkDef); err != nil {
		t.Fatalf("query skill_dependencies_pkey definition: %v", err)
	}
	if !strings.Contains(pkDef, "skill_id") || !strings.Contains(pkDef, "depends_on") || !strings.Contains(pkDef, "relation_type") {
		t.Errorf("skill_dependencies_pkey definition = %q, want it to reference skill_id, depends_on, AND relation_type", pkDef)
	}
}

// N6 (Fable code-review remediation, pg_trgm fix) — db-package migration-
// shape assertion mirroring M2's pattern above: after Migrate applies the
// real migrations dir (which now carries migrations/003_pg_trgm.up.sql),
// the pg_trgm extension AND both GIN trigram indexes it creates
// (idx_skills_name_trgm, idx_skills_title_trgm) must exist. This is the
// permanent §11.4.135 regression guard on 003's effect: internal/skill/
// kind_read_paths_granularity_test.go's Search subtest exercises the
// EFFECT (Store.Search's `%`/similarity(...) query succeeds) through the
// skill.Store layer; this test asserts the CAUSE (the extension + indexes
// genuinely exist) at the db-package/migration-shape layer, so a
// regression that dropped 003 (or broke its DDL) fails here even before
// any skill.Store-level symptom would be exercised.
func TestP1T1N6_PgTrgmExtensionAndIndexesExist(t *testing.T) {
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

	// pg_trgm extension present (migrations/003_pg_trgm.up.sql: CREATE
	// EXTENSION IF NOT EXISTS pg_trgm).
	var extCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM pg_extension WHERE extname = 'pg_trgm'`).Scan(&extCount); err != nil {
		t.Fatalf("query pg_extension for pg_trgm: %v", err)
	}
	if extCount != 1 {
		t.Errorf("pg_trgm extension count = %d, want 1 (migrations/003_pg_trgm.up.sql CREATE EXTENSION)", extCount)
	}

	// Both GIN trigram indexes 003_pg_trgm.up.sql creates on skills.name /
	// skills.title (the columns Store.Search's `%` operator queries).
	for _, idxName := range []string{"idx_skills_name_trgm", "idx_skills_title_trgm"} {
		var idxCount int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM pg_indexes WHERE indexname = $1`, idxName).Scan(&idxCount); err != nil {
			t.Fatalf("query %s: %v", idxName, err)
		}
		if idxCount != 1 {
			t.Errorf("%s index count = %d, want 1", idxName, idxCount)
		}
	}
}

// M3 — a skill + dependency row created under 001-only, then upgraded to 002,
// acquire the new columns' defaults (kind='atomic', optional=FALSE,
// sort_order IS NULL) with the row count unchanged.
func TestP1T1Migration_M3_PreExistingRowsGetDefaultsAfterUpgrade(t *testing.T) {
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

	// Stage 1: 001 only.
	if err := Migrate(ctx, pool, stageMigrationsDir(t, "001")); err != nil {
		t.Fatalf("Migrate (001 only): %v", err)
	}

	idA, idB := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO skills (id, name, title, content) VALUES ($1, 'p1t1.m3.a', 'A', 'content A')`, idA); err != nil {
		t.Fatalf("insert skill A pre-002: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO skills (id, name, title, content) VALUES ($1, 'p1t1.m3.b', 'B', 'content B')`, idB); err != nil {
		t.Fatalf("insert skill B pre-002: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO skill_dependencies (skill_id, depends_on, relation_type) VALUES ($1, $2, 'requires')`, idA, idB); err != nil {
		t.Fatalf("insert dependency pre-002: %v", err)
	}

	// Stage 2: apply 002 on top (001 already applied+recorded, skipped).
	if err := Migrate(ctx, pool, stageMigrationsDir(t, "001", "002")); err != nil {
		t.Fatalf("Migrate (001+002, 002 newly applied): %v", err)
	}

	var skillCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM skills WHERE id IN ($1, $2)`, idA, idB).Scan(&skillCount); err != nil {
		t.Fatalf("count skills post-002: %v", err)
	}
	if skillCount != 2 {
		t.Errorf("skill row count post-002 = %d, want 2 (unchanged)", skillCount)
	}

	var kindA, kindB string
	if err := pool.QueryRow(ctx, `SELECT kind FROM skills WHERE id = $1`, idA).Scan(&kindA); err != nil {
		t.Fatalf("query kind for A: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT kind FROM skills WHERE id = $1`, idB).Scan(&kindB); err != nil {
		t.Fatalf("query kind for B: %v", err)
	}
	if kindA != "atomic" || kindB != "atomic" {
		t.Errorf("kind(A)=%q kind(B)=%q, want both 'atomic'", kindA, kindB)
	}

	var depCount int
	var optional bool
	var sortOrder *int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM skill_dependencies WHERE skill_id = $1 AND depends_on = $2`, idA, idB).Scan(&depCount); err != nil {
		t.Fatalf("count dependency rows post-002: %v", err)
	}
	if depCount != 1 {
		t.Errorf("dependency row count post-002 = %d, want 1 (unchanged)", depCount)
	}
	if err := pool.QueryRow(ctx, `SELECT optional, sort_order FROM skill_dependencies WHERE skill_id = $1 AND depends_on = $2`, idA, idB).Scan(&optional, &sortOrder); err != nil {
		t.Fatalf("query optional/sort_order: %v", err)
	}
	if optional != false {
		t.Errorf("optional = %v, want false (column DEFAULT)", optional)
	}
	if sortOrder != nil {
		t.Errorf("sort_order = %v, want nil (column has no DEFAULT, stays NULL)", *sortOrder)
	}
}

// M4 — the RED->GREEN flip (§11.4.115): a `composes` relation_type INSERT
// errors with a CHECK violation pre-002, and succeeds on the identical
// statement post-002.
func TestP1T1Migration_M4_ComposesEdgeRejectedPre002AcceptedPost002(t *testing.T) {
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

	if err := Migrate(ctx, pool, stageMigrationsDir(t, "001")); err != nil {
		t.Fatalf("Migrate (001 only): %v", err)
	}

	idA, idB := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO skills (id, name, title, content) VALUES ($1, 'p1t1.m4.a', 'A', 'content A')`, idA); err != nil {
		t.Fatalf("insert skill A: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO skills (id, name, title, content) VALUES ($1, 'p1t1.m4.b', 'B', 'content B')`, idB); err != nil {
		t.Fatalf("insert skill B: %v", err)
	}

	// RED baseline: pre-002, 'composes' violates the 3-value CHECK.
	_, err = pool.Exec(ctx, `INSERT INTO skill_dependencies (skill_id, depends_on, relation_type) VALUES ($1, $2, 'composes')`, idA, idB)
	if err == nil {
		t.Fatal("composes insert pre-002: expected a CHECK-violation error, got nil (RED baseline did not reproduce)")
	}
	if !strings.Contains(err.Error(), "relation_type_check") && !strings.Contains(strings.ToLower(err.Error()), "check constraint") {
		t.Errorf("composes insert pre-002 error = %v, want it to reference the relation_type CHECK constraint", err)
	}

	// Apply 002.
	if err := Migrate(ctx, pool, stageMigrationsDir(t, "001", "002")); err != nil {
		t.Fatalf("Migrate (001+002): %v", err)
	}

	// GREEN: identical statement now succeeds.
	if _, err := pool.Exec(ctx, `INSERT INTO skill_dependencies (skill_id, depends_on, relation_type) VALUES ($1, $2, 'composes')`, idA, idB); err != nil {
		t.Fatalf("composes insert post-002: expected success, got error: %v", err)
	}
}

// M5 — the widened PK (skill_id, depends_on, relation_type) admits two typed
// edges for the same (skill_id, depends_on) pair.
func TestP1T1Migration_M5_PKWideningAllowsTwoEdgesPerPair(t *testing.T) {
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

	idA, idB := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO skills (id, name, title, content) VALUES ($1, 'p1t1.m5.a', 'A', 'content A')`, idA); err != nil {
		t.Fatalf("insert skill A: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO skills (id, name, title, content) VALUES ($1, 'p1t1.m5.b', 'B', 'content B')`, idB); err != nil {
		t.Fatalf("insert skill B: %v", err)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO skill_dependencies (skill_id, depends_on, relation_type) VALUES ($1, $2, 'requires')`, idA, idB); err != nil {
		t.Fatalf("insert requires edge: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO skill_dependencies (skill_id, depends_on, relation_type) VALUES ($1, $2, 'recommends')`, idA, idB); err != nil {
		t.Fatalf("insert recommends edge on the SAME (skill_id, depends_on) pair: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM skill_dependencies WHERE skill_id = $1 AND depends_on = $2`, idA, idB).Scan(&count); err != nil {
		t.Fatalf("count edges for the pair: %v", err)
	}
	if count != 2 {
		t.Errorf("edge count for (A,B) pair = %d, want 2 (requires + recommends coexisting)", count)
	}
}

// M7 — CHECK constraints reject bogus skills.kind and
// skill_dependencies.relation_type values.
func TestP1T1Migration_M7_CheckConstraintsRejectBogusValues(t *testing.T) {
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

	idA := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO skills (id, name, title, content) VALUES ($1, 'p1t1.m7.a', 'A', 'content A')`, idA); err != nil {
		t.Fatalf("insert skill A: %v", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE skills SET kind = 'bogus' WHERE id = $1`, idA); err == nil {
		t.Error("UPDATE skills SET kind='bogus': expected a CHECK-violation error, got nil")
	}

	idB := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO skills (id, name, title, content) VALUES ($1, 'p1t1.m7.b', 'B', 'content B')`, idB); err != nil {
		t.Fatalf("insert skill B: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO skill_dependencies (skill_id, depends_on, relation_type) VALUES ($1, $2, 'bogus')`, idA, idB); err == nil {
		t.Error("INSERT ... relation_type='bogus': expected a CHECK-violation error, got nil")
	}
}

// M8 — migrate down cleanly reverts an UNUSED-feature DB back to the exact
// 001 shape.
func TestP1T1Migration_M8_MigrateDownCleanOnUnusedFeatureDB(t *testing.T) {
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

	// Staged to exactly 001+002 (see M1's comment for why realMigrationsDir
	// -- which now also carries 003_pg_trgm.sql -- is not used directly here):
	// MigrateDown(1) below must roll back exactly the 002 down-migration
	// under test, not whatever migration happens to be highest-versioned in
	// the ever-growing real directory.
	migDir := stageMigrationsDir(t, "001", "002")
	if err := Migrate(ctx, pool, migDir); err != nil {
		t.Fatalf("Migrate (001+002): %v", err)
	}

	if err := MigrateDown(ctx, pool, migDir, 1); err != nil {
		t.Fatalf("MigrateDown(1) on an unused-feature DB: expected clean rollback, got error: %v", err)
	}

	version, err := CurrentMigrationVersion(ctx, pool)
	if err != nil {
		t.Fatalf("CurrentMigrationVersion post-down: %v", err)
	}
	if version != 1 {
		t.Errorf("CurrentMigrationVersion post-down = %d, want 1", version)
	}

	var kindColCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM information_schema.columns WHERE table_name = 'skills' AND column_name = 'kind'`).Scan(&kindColCount); err != nil {
		t.Fatalf("query skills.kind post-down: %v", err)
	}
	if kindColCount != 0 {
		t.Error("skills.kind still present after MigrateDown(1); want it dropped (reverted to 001 shape)")
	}

	var relCheckDef string
	if err := pool.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'skill_dependencies_relation_type_check'`).Scan(&relCheckDef); err != nil {
		t.Fatalf("query relation_type_check post-down: %v", err)
	}
	if strings.Contains(relCheckDef, "composes") {
		t.Errorf("relation_type_check post-down still contains 'composes': %q, want the narrowed 001 3-value set", relCheckDef)
	}

	var pkDef string
	if err := pool.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'skill_dependencies_pkey'`).Scan(&pkDef); err != nil {
		t.Fatalf("query skill_dependencies_pkey post-down: %v", err)
	}
	if strings.Contains(pkDef, "relation_type") {
		t.Errorf("skill_dependencies_pkey post-down still references relation_type: %q, want the narrowed (skill_id, depends_on) pair", pkDef)
	}
}

// NEW-5 (Fable code-review round-2, §11.4.6 honest-boundary note): a DB whose
// ONLY "used" state is >1 LEGACY-typed edge for the same (skill_id,
// depends_on) pair (e.g. a `requires` edge AND an `extends` edge between the
// same A->B, both with default attrs -- optional=false, sort_order=NULL, so
// neither the M9b/M9c edge-attribute guard nor the M9d kind guard fires) is
// NOT separately asserted by its own Go test here. Its down-refusal is
// guaranteed by a DIFFERENT mechanism than the explicit DO-block guards
// covered by M9/M9b/M9c/M9d above: migrations/002_granularity.down.sql's
// "inverse (4)" step re-adds the NARROWED (skill_id, depends_on) PRIMARY KEY
// (`ALTER TABLE skill_dependencies ADD CONSTRAINT skill_dependencies_pkey
// PRIMARY KEY (skill_id, depends_on)`), and PostgreSQL itself raises a
// duplicate-key error (rolling back the whole down transaction, same
// fail-closed outcome) the instant that narrowed constraint would collide
// with >1 row per pair -- this is PG-ENFORCED at the ALTER TABLE step, not a
// condition this codebase's Go/SQL guard code decides. Documenting this
// honestly here rather than adding a redundant DO-block + test that would
// only re-prove what PostgreSQL's own constraint-uniqueness already
// guarantees.

// M9 — migrate down on a USED-feature DB (a composes row present) fails
// closed: the whole down transaction rolls back, schema stays fully 002-shaped.
func TestP1T1Migration_M9_MigrateDownFailsClosedOnUsedFeatureDB(t *testing.T) {
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

	// Staged to exactly 001+002 -- see M1's comment for why realMigrationsDir
	// (which now also carries 003_pg_trgm.sql) is not used directly here.
	migDir := stageMigrationsDir(t, "001", "002")
	if err := Migrate(ctx, pool, migDir); err != nil {
		t.Fatalf("Migrate (001+002): %v", err)
	}

	idA, idB := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO skills (id, name, title, content) VALUES ($1, 'p1t1.m9.a', 'A', 'content A')`, idA); err != nil {
		t.Fatalf("insert skill A: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO skills (id, name, title, content) VALUES ($1, 'p1t1.m9.b', 'B', 'content B')`, idB); err != nil {
		t.Fatalf("insert skill B: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO skill_dependencies (skill_id, depends_on, relation_type) VALUES ($1, $2, 'composes')`, idA, idB); err != nil {
		t.Fatalf("insert composes edge (marks the feature as used): %v", err)
	}

	if err := MigrateDown(ctx, pool, migDir, 1); err == nil {
		t.Fatal("MigrateDown(1) on a used-feature DB (composes row present): expected a fail-closed error, got nil")
	}

	// Post-abort: schema MUST still be fully 002-shaped -- no partial drop.
	version, err := CurrentMigrationVersion(ctx, pool)
	if err != nil {
		t.Fatalf("CurrentMigrationVersion post-abort: %v", err)
	}
	if version != 2 {
		t.Errorf("CurrentMigrationVersion post-abort = %d, want 2 (down rolled back, record untouched)", version)
	}

	var kindColCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM information_schema.columns WHERE table_name = 'skills' AND column_name = 'kind'`).Scan(&kindColCount); err != nil {
		t.Fatalf("query skills.kind post-abort: %v", err)
	}
	if kindColCount != 1 {
		t.Errorf("skills.kind column count post-abort = %d, want 1 (still present, not partially dropped)", kindColCount)
	}

	var pkDef string
	if err := pool.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'skill_dependencies_pkey'`).Scan(&pkDef); err != nil {
		t.Fatalf("query skill_dependencies_pkey post-abort: %v", err)
	}
	if !strings.Contains(pkDef, "relation_type") {
		t.Errorf("skill_dependencies_pkey post-abort = %q, want it STILL widened to include relation_type (no partial narrowing)", pkDef)
	}

	var composesCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM skill_dependencies WHERE relation_type = 'composes'`).Scan(&composesCount); err != nil {
		t.Fatalf("count composes rows post-abort: %v", err)
	}
	if composesCount != 1 {
		t.Errorf("composes row count post-abort = %d, want 1 (data untouched by the aborted down)", composesCount)
	}
}

// M9b — W1 fix (Fable code-review remediation): migrate down on a DB whose
// ONLY "used" state is a SINGLE edge, using a LEGACY relation_type
// (`requires`), with optional=TRUE set. This passes BOTH of M9's original
// guards (the relation_type CHECK narrowing never fires, since 'requires' is
// a 001-legacy value; the PK narrowing never fires, since there is only ONE
// edge for the pair) -- so pre-W1-fix this down SUCCEEDED, silently
// DROPPING the optional=TRUE flag via `DROP COLUMN optional`. Post-fix the
// new DO-block guard must reject it before any narrowing step runs.
func TestP1T1Migration_M9b_MigrateDownFailsClosedOnOptionalEdgeAttribute(t *testing.T) {
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

	// Staged to exactly 001+002 -- see M1's comment for why realMigrationsDir
	// (which now also carries 003_pg_trgm.sql) is not used directly here.
	migDir := stageMigrationsDir(t, "001", "002")
	if err := Migrate(ctx, pool, migDir); err != nil {
		t.Fatalf("Migrate (001+002): %v", err)
	}

	idA, idB := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO skills (id, name, title, content) VALUES ($1, 'p1t1.m9b.a', 'A', 'content A')`, idA); err != nil {
		t.Fatalf("insert skill A: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO skills (id, name, title, content) VALUES ($1, 'p1t1.m9b.b', 'B', 'content B')`, idB); err != nil {
		t.Fatalf("insert skill B: %v", err)
	}
	// Legacy relation_type (passes the CHECK-narrowing guard) + single edge
	// for the pair (passes the PK-narrowing guard) + optional=TRUE (the gap).
	if _, err := pool.Exec(ctx, `INSERT INTO skill_dependencies (skill_id, depends_on, relation_type, optional) VALUES ($1, $2, 'requires', TRUE)`, idA, idB); err != nil {
		t.Fatalf("insert requires edge with optional=TRUE: %v", err)
	}

	if err := MigrateDown(ctx, pool, migDir, 1); err == nil {
		t.Fatal("MigrateDown(1) on a DB with a legacy-type edge that has optional=TRUE: expected a fail-closed error (W1), got nil -- pre-fix this down silently dropped the optional column, losing the flag")
	}

	version, err := CurrentMigrationVersion(ctx, pool)
	if err != nil {
		t.Fatalf("CurrentMigrationVersion post-abort: %v", err)
	}
	if version != 2 {
		t.Errorf("CurrentMigrationVersion post-abort = %d, want 2 (down rolled back, record untouched)", version)
	}

	var optionalColCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM information_schema.columns WHERE table_name = 'skill_dependencies' AND column_name = 'optional'`).Scan(&optionalColCount); err != nil {
		t.Fatalf("query skill_dependencies.optional post-abort: %v", err)
	}
	if optionalColCount != 1 {
		t.Errorf("skill_dependencies.optional column count post-abort = %d, want 1 (still present, not dropped)", optionalColCount)
	}

	var optionalValue bool
	if err := pool.QueryRow(ctx, `SELECT optional FROM skill_dependencies WHERE skill_id = $1 AND depends_on = $2`, idA, idB).Scan(&optionalValue); err != nil {
		t.Fatalf("query optional value post-abort: %v", err)
	}
	if !optionalValue {
		t.Error("optional value post-abort = false, want true (data untouched by the aborted down)")
	}
}

// M9c — W1 fix (Fable code-review remediation): the sibling of M9b for
// sort_order instead of optional. A single legacy-typed edge with
// sort_order set passes both of M9's original guards too, and pre-fix the
// down silently dropped the sort_order column, losing the value.
func TestP1T1Migration_M9c_MigrateDownFailsClosedOnSortOrderEdgeAttribute(t *testing.T) {
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

	// Staged to exactly 001+002 -- see M1's comment for why realMigrationsDir
	// (which now also carries 003_pg_trgm.sql) is not used directly here.
	migDir := stageMigrationsDir(t, "001", "002")
	if err := Migrate(ctx, pool, migDir); err != nil {
		t.Fatalf("Migrate (001+002): %v", err)
	}

	idA, idB := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO skills (id, name, title, content) VALUES ($1, 'p1t1.m9c.a', 'A', 'content A')`, idA); err != nil {
		t.Fatalf("insert skill A: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO skills (id, name, title, content) VALUES ($1, 'p1t1.m9c.b', 'B', 'content B')`, idB); err != nil {
		t.Fatalf("insert skill B: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO skill_dependencies (skill_id, depends_on, relation_type, sort_order) VALUES ($1, $2, 'extends', 3)`, idA, idB); err != nil {
		t.Fatalf("insert extends edge with sort_order=3: %v", err)
	}

	if err := MigrateDown(ctx, pool, migDir, 1); err == nil {
		t.Fatal("MigrateDown(1) on a DB with a legacy-type edge that has sort_order set: expected a fail-closed error (W1), got nil -- pre-fix this down silently dropped the sort_order column, losing the value")
	}

	version, err := CurrentMigrationVersion(ctx, pool)
	if err != nil {
		t.Fatalf("CurrentMigrationVersion post-abort: %v", err)
	}
	if version != 2 {
		t.Errorf("CurrentMigrationVersion post-abort = %d, want 2 (down rolled back, record untouched)", version)
	}

	var sortOrderColCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM information_schema.columns WHERE table_name = 'skill_dependencies' AND column_name = 'sort_order'`).Scan(&sortOrderColCount); err != nil {
		t.Fatalf("query skill_dependencies.sort_order post-abort: %v", err)
	}
	if sortOrderColCount != 1 {
		t.Errorf("skill_dependencies.sort_order column count post-abort = %d, want 1 (still present, not dropped)", sortOrderColCount)
	}

	var sortOrderValue *int
	if err := pool.QueryRow(ctx, `SELECT sort_order FROM skill_dependencies WHERE skill_id = $1 AND depends_on = $2`, idA, idB).Scan(&sortOrderValue); err != nil {
		t.Fatalf("query sort_order value post-abort: %v", err)
	}
	if sortOrderValue == nil || *sortOrderValue != 3 {
		t.Errorf("sort_order value post-abort = %v, want pointer to 3 (data untouched by the aborted down)", sortOrderValue)
	}
}

// M9d — W1 fix (Fable code-review remediation): a skills row with
// kind <> 'atomic' (e.g. 'composite') that has ZERO outgoing composes
// edges. `kind` is a column on `skills`, entirely independent of
// skill_dependencies, so NEITHER of M9's original dependency-shaped guards
// (relation_type CHECK, PK width) ever inspects it -- pre-fix this down
// succeeded, silently dropping the kind column and losing the
// composite/umbrella classification.
func TestP1T1Migration_M9d_MigrateDownFailsClosedOnNonAtomicSkillKind(t *testing.T) {
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

	// Staged to exactly 001+002 -- see M1's comment for why realMigrationsDir
	// (which now also carries 003_pg_trgm.sql) is not used directly here.
	migDir := stageMigrationsDir(t, "001", "002")
	if err := Migrate(ctx, pool, migDir); err != nil {
		t.Fatalf("Migrate (001+002): %v", err)
	}

	idA := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO skills (id, name, title, content, kind) VALUES ($1, 'p1t1.m9d.a', 'A', 'content A', 'composite')`, idA); err != nil {
		t.Fatalf("insert composite skill A (zero composes edges): %v", err)
	}

	if err := MigrateDown(ctx, pool, migDir, 1); err == nil {
		t.Fatal("MigrateDown(1) on a DB with a kind<>'atomic' skill and zero composes edges: expected a fail-closed error (W1), got nil -- pre-fix this down silently dropped the kind column, losing the classification")
	}

	version, err := CurrentMigrationVersion(ctx, pool)
	if err != nil {
		t.Fatalf("CurrentMigrationVersion post-abort: %v", err)
	}
	if version != 2 {
		t.Errorf("CurrentMigrationVersion post-abort = %d, want 2 (down rolled back, record untouched)", version)
	}

	var kindValue string
	if err := pool.QueryRow(ctx, `SELECT kind FROM skills WHERE id = $1`, idA).Scan(&kindValue); err != nil {
		t.Fatalf("query kind post-abort: %v", err)
	}
	if kindValue != "composite" {
		t.Errorf("kind post-abort = %q, want %q (data untouched by the aborted down)", kindValue, "composite")
	}
}
