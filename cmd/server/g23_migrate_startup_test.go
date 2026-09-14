package main

// G23 — cmd/server startup migration: embedded FS (cwd-independent) + FAIL-CLOSED
// (research/ops_hardening_design.md §G23).
//
// These exercise the REAL startup wiring cmd/server uses — startupMigrationsFS()
// (the embedded source) and migrateOnStartup() (the apply + error-propagation
// step main() fail-fasts on) — against a live throwaway pgvector database.
//
//   - TestMigrateOnStartup_MigratesFromEmbeddedRegardlessOfCwd: run from a
//     DIFFERENT working directory (t.Chdir to an empty temp dir that has NO
//     ./migrations) and prove the embedded migrations still apply to the highest
//     embedded version. The same test captures the RED baseline: the pre-fix
//     cwd-relative db.Migrate(ctx, pool, "./migrations") applies NOTHING from
//     that directory. (§1.1 mutation: point startupMigrationsFS at
//     os.DirFS("./migrations") and this FAILs from the temp cwd.)
//   - TestMigrateOnStartup_FailsFast_ReturnsErrorNotSwallowed: a migration that
//     fails makes migrateOnStartup RETURN a non-nil error (which main() turns
//     into a fatal, no-listener startup). (§1.1 mutation: revert migrateOnStartup
//     to warn-and-continue / return nil and this FAILs.)
//
// Both need a live pgvector instance and honestly t.Skip() when
// SKILL_SYSTEM_TEST_DB_HOST is unset (§11.4.3/§11.4.27) — never a fake PASS.

import (
	"context"
	"crypto/rand"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Live throwaway-DB provisioning (same SKILL_SYSTEM_TEST_DB_* contract as
// internal/db, internal/mcp, etc.; re-implemented here because those helpers are
// unexported _test.go symbols not importable across packages).
// ---------------------------------------------------------------------------

func g23TestDBAdminConfig() (config.DatabaseConfig, bool) {
	host := os.Getenv("SKILL_SYSTEM_TEST_DB_HOST")
	if host == "" {
		return config.DatabaseConfig{}, false
	}
	port := 5432
	if p := os.Getenv("SKILL_SYSTEM_TEST_DB_PORT"); p != "" {
		if v, err := strconv.Atoi(p); err == nil {
			port = v
		}
	}
	user := os.Getenv("SKILL_SYSTEM_TEST_DB_USER")
	if user == "" {
		user = "postgres"
	}
	adminDB := os.Getenv("SKILL_SYSTEM_TEST_DB_ADMIN_DB")
	if adminDB == "" {
		adminDB = "postgres"
	}
	return config.DatabaseConfig{
		Host:           host,
		Port:           port,
		Database:       adminDB,
		User:           user,
		Password:       os.Getenv("SKILL_SYSTEM_TEST_DB_PASSWORD"),
		SSLMode:        "disable",
		MaxConnections: 4,
		ConnectTimeout: 10 * time.Second,
	}, true
}

func g23SkipIfNoTestDB(t *testing.T) (config.DatabaseConfig, bool) {
	t.Helper()
	admin, ok := g23TestDBAdminConfig()
	if !ok {
		t.Skip("SKILL_SYSTEM_TEST_DB_HOST not set: this case requires a live " +
			"pgvector/pgvector:pg16-class PostgreSQL instance to prove cmd/server's " +
			"startup migration path (embedded FS + fail-closed, §G23) end-to-end " +
			"against a real schema — same boundary as this project's other " +
			"_RequiresLiveDatabase tests.")
		return config.DatabaseConfig{}, false
	}
	return admin, true
}

func g23RandomSuffix() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000_000))
	if err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return n.String()
}

// g23CreateThrowawayDBConfig creates a uniquely-named database, pre-creates the
// vector + uuid-ossp extensions (mirroring docker-entrypoint-initdb.d), and
// returns a DatabaseConfig pointed at it plus a cleanup func. It does NOT open a
// pool or apply migrations — each test drives db.New / the startup path itself.
func g23CreateThrowawayDBConfig(t *testing.T, admin config.DatabaseConfig) (config.DatabaseConfig, func()) {
	t.Helper()
	ctx := context.Background()

	adminConn, err := pgx.Connect(ctx, admin.DSNWithTimeout())
	if err != nil {
		t.Fatalf("connect to admin database %q: %v", admin.Database, err)
	}
	defer adminConn.Close(ctx)

	dbName := "skillsys_test_g23_" + g23RandomSuffix()
	if _, err := adminConn.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s", pgx.Identifier{dbName}.Sanitize())); err != nil {
		t.Fatalf("create throwaway database %q: %v", dbName, err)
	}

	dbCfg := admin
	dbCfg.Database = dbName

	extConn, err := pgx.Connect(ctx, dbCfg.DSNWithTimeout())
	if err != nil {
		t.Fatalf("connect to throwaway database %q: %v", dbName, err)
	}
	if _, err := extConn.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector; CREATE EXTENSION IF NOT EXISTS "uuid-ossp";`); err != nil {
		extConn.Close(ctx)
		t.Fatalf("create extensions in throwaway database %q: %v", dbName, err)
	}
	extConn.Close(ctx)

	cleanup := func() {
		cctx := context.Background()
		c, err := pgx.Connect(cctx, admin.DSNWithTimeout())
		if err != nil {
			return // best-effort; a leaked throwaway test DB is not fatal
		}
		defer c.Close(cctx)
		_, _ = c.Exec(cctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, dbName)
		_, _ = c.Exec(cctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", pgx.Identifier{dbName}.Sanitize()))
	}

	return dbCfg, cleanup
}

// maxEmbeddedUpVersion computes the highest NNN among the embedded *.up.sql
// migrations, so the assertions below do not hardcode "3" and stay correct as
// migrations are added.
func maxEmbeddedUpVersion(t *testing.T, fsys fs.FS) int64 {
	t.Helper()
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		t.Fatalf("read embedded migrations FS: %v", err)
	}
	var max int64 = -1
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		parts := strings.SplitN(name, "_", 2)
		if len(parts) < 2 {
			continue
		}
		v, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			continue
		}
		if v > max {
			max = v
		}
	}
	if max < 1 {
		t.Fatalf("no embedded *.up.sql migrations found (embed directive broken?)")
	}
	return max
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestMigrateOnStartup_MigratesFromEmbeddedRegardlessOfCwd is the §G23 cwd-
// independence + RED→GREEN proof: from an empty temp working directory (no
// ./migrations), the embedded startup path migrates the schema to the highest
// embedded version (GREEN), while the pre-fix cwd-relative "./migrations" path
// applies nothing from that same directory (RED baseline).
func TestMigrateOnStartup_MigratesFromEmbeddedRegardlessOfCwd_RequiresLiveDatabase(t *testing.T) {
	admin, ok := g23SkipIfNoTestDB(t)
	if !ok {
		return
	}
	ctx := context.Background()

	wantMax := maxEmbeddedUpVersion(t, startupMigrationsFS())

	// Move to an empty working directory that has NO ./migrations subtree, so a
	// cwd-relative loader would find nothing. t.Chdir restores the cwd at test
	// end and forbids t.Parallel.
	t.Chdir(t.TempDir())

	// RED baseline: the pre-fix cwd-relative path applies nothing from here.
	redCfg, redCleanup := g23CreateThrowawayDBConfig(t, admin)
	defer redCleanup()
	redPool, err := db.New(redCfg)
	if err != nil {
		t.Fatalf("db.New (RED baseline pool): %v", err)
	}
	defer redPool.Close()
	// db.Migrate(ctx, pool, "./migrations") from this cwd fails to discover the
	// dir; the pre-fix main() swallowed this with a Warn and served anyway.
	redErr := db.Migrate(ctx, redPool, "./migrations")
	redVersion, verr := db.CurrentMigrationVersion(ctx, redPool)
	if verr != nil {
		t.Fatalf("CurrentMigrationVersion (RED baseline): %v", verr)
	}
	if redErr == nil && redVersion == wantMax {
		t.Fatalf("RED baseline did not reproduce: cwd-relative \"./migrations\" applied to version %d from an empty cwd — expected it to fail/apply-nothing", redVersion)
	}
	t.Logf("RED baseline captured: cwd-relative db.Migrate(\"./migrations\") from empty cwd → err=%v, version=%d (nothing applied)", redErr, redVersion)

	// GREEN: the embedded startup path applies the full migration set from the
	// SAME (wrong) working directory.
	greenCfg, greenCleanup := g23CreateThrowawayDBConfig(t, admin)
	defer greenCleanup()
	greenPool, err := db.New(greenCfg)
	if err != nil {
		t.Fatalf("db.New (GREEN pool): %v", err)
	}
	defer greenPool.Close()

	if err := migrateOnStartup(ctx, greenPool, startupMigrationsFS(), zap.NewNop()); err != nil {
		t.Fatalf("migrateOnStartup from embedded FS (empty cwd): expected success, got error: %v", err)
	}
	greenVersion, err := db.CurrentMigrationVersion(ctx, greenPool)
	if err != nil {
		t.Fatalf("CurrentMigrationVersion (GREEN): %v", err)
	}
	if greenVersion != wantMax {
		t.Errorf("CurrentMigrationVersion after embedded startup migrate = %d, want %d (highest embedded version — cwd-independent apply)", greenVersion, wantMax)
	}

	// The schema is genuinely present: the skills table from 001 exists.
	var skillsTables int
	if err := greenPool.QueryRow(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'skills'`).Scan(&skillsTables); err != nil {
		t.Fatalf("query for skills table: %v", err)
	}
	if skillsTables != 1 {
		t.Errorf("skills table count after embedded startup migrate = %d, want 1 (schema really applied)", skillsTables)
	}
	t.Logf("GREEN: embedded startup migrate from empty cwd → version=%d, skills table present", greenVersion)
}

// TestMigrateOnStartup_FailsFast_ReturnsErrorNotSwallowed proves migrateOnStartup
// returns a non-nil error when a migration fails (main() turns this into a fatal,
// no-listener startup — §G23 fail-closed). A conflicting pre-existing skills
// table makes 001's `CREATE TABLE skills (...)` fail deterministically.
func TestMigrateOnStartup_FailsFast_ReturnsErrorNotSwallowed_RequiresLiveDatabase(t *testing.T) {
	admin, ok := g23SkipIfNoTestDB(t)
	if !ok {
		return
	}
	ctx := context.Background()

	dbCfg, cleanup := g23CreateThrowawayDBConfig(t, admin)
	defer cleanup()

	pool, err := db.New(dbCfg)
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	defer pool.Close()

	// Pre-create a conflicting skills table so 001_initial.up.sql's
	// `CREATE TABLE skills (...)` (no IF NOT EXISTS) fails with "already exists".
	if _, err := pool.Exec(ctx, `CREATE TABLE skills (id integer)`); err != nil {
		t.Fatalf("pre-create conflicting skills table: %v", err)
	}

	err = migrateOnStartup(ctx, pool, startupMigrationsFS(), zap.NewNop())
	if err == nil {
		t.Fatal("migrateOnStartup with a failing migration: expected a non-nil error (fail-closed; main() fail-fasts on it), got nil — the failure was swallowed (pre-G23 warn-and-continue)")
	}
	t.Logf("fail-fast confirmed: migrateOnStartup returned error = %v", err)
}
