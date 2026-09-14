package db

// Test-only PostgreSQL provisioning helper for the P1.T1 granularity-schema
// migration acceptance suite (research/p1t1_granularity_schema_migration.md
// §4 — cases M1/M2/M7/M8/M9). Every test in this suite requires a REAL,
// reachable `pgvector/pgvector:pg16`-class PostgreSQL instance (the recursive
// CTEs, CHECK constraints, and transactional rollback semantics under test
// have no faithful in-memory substitute — mirroring the pre-existing,
// documented boundary at internal/skill/graph_test.go:58-82).
//
// Topology detection (§11.4.3): tests dispatch on whether
// SKILL_SYSTEM_TEST_DB_HOST is set. When unset, every case in this suite
// MUST t.Skip with an honest reason — never assume a DB is present, never
// fake a PASS. When set, each test provisions its OWN throwaway database
// (CREATE DATABASE per test, DROP DATABASE on cleanup) against that host so
// tests never share or corrupt state, and are safe to run with `-count=1`
// repeatedly (§11.4.98 re-runnability) or in parallel.
//
// Environment contract (test-only; distinct from the application's own
// DB_HOST/DB_PORT/... docker-compose variables to avoid collision):
//
//	SKILL_SYSTEM_TEST_DB_HOST      (required to enable this suite)
//	SKILL_SYSTEM_TEST_DB_PORT      (default 5432)
//	SKILL_SYSTEM_TEST_DB_USER      (default postgres)
//	SKILL_SYSTEM_TEST_DB_PASSWORD  (default "")
//	SKILL_SYSTEM_TEST_DB_ADMIN_DB  (default postgres; DB used to CREATE/DROP the throwaway test DB)

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/jackc/pgx/v5"
)

// realMigrationsDir is the on-disk migrations directory this test package
// exercises, relative to internal/db (this package). It is the SAME
// directory db.Migrate/db.MigrateDown read in production
// (cmd/server/main.go:86 calls db.Migrate(ctx, pool, "./migrations")).
const realMigrationsDir = "../../migrations"

// stageMigrationsDir copies only the migration files whose version prefix is
// in versions (e.g. "001", "002") from realMigrationsDir into a fresh temp
// directory and returns its path. Used to drive db.Migrate/db.MigrateDown
// against a controlled subset of the real migration files (e.g. apply 001
// alone, insert data, THEN add 002 and apply it) without ever hand-writing
// migration SQL in the test (the test always exercises the REAL
// 002_granularity.up/down.sql files under migrations/).
func stageMigrationsDir(t *testing.T, versions ...string) string {
	t.Helper()
	dir := t.TempDir()

	entries, err := os.ReadDir(realMigrationsDir)
	if err != nil {
		t.Fatalf("read real migrations dir %q: %v", realMigrationsDir, err)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		matched := false
		for _, v := range versions {
			if strings.HasPrefix(name, v+"_") {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		src, err := os.ReadFile(filepath.Join(realMigrationsDir, name))
		if err != nil {
			t.Fatalf("read migration file %q: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), src, 0o644); err != nil {
			t.Fatalf("stage migration file %q: %v", name, err)
		}
	}

	return dir
}

// testDBAdminConfig reads the SKILL_SYSTEM_TEST_DB_* environment variables
// and returns a DatabaseConfig pointed at the admin/maintenance database
// (default "postgres"), plus whether the suite is enabled at all.
func testDBAdminConfig() (config.DatabaseConfig, bool) {
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

// skipIfNoTestDB is called first by every test in this suite. It returns the
// admin config when a live test database is configured, or calls t.Skip with
// an honest, specific reason and returns ok=false.
func skipIfNoTestDB(t *testing.T) (config.DatabaseConfig, bool) {
	t.Helper()
	admin, ok := testDBAdminConfig()
	if !ok {
		t.Skip("SKILL_SYSTEM_TEST_DB_HOST not set: this case requires a live " +
			"pgvector/pgvector:pg16-class PostgreSQL instance to prove migration " +
			"DDL, CHECK-constraint, and transactional-rollback behaviour that has " +
			"no faithful in-memory substitute (research/p1t1_granularity_schema_" +
			"migration.md §5.3 honest gap; same boundary as internal/skill/" +
			"graph_test.go's _RequiresLiveDatabase tests).")
		return config.DatabaseConfig{}, false
	}
	return admin, true
}

// randomSuffix returns a short random hex-ish decimal suffix for throwaway
// database names, so parallel/repeated test runs never collide.
func randomSuffix() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000_000))
	if err != nil {
		// crypto/rand failure is exceptionally rare; fall back to a
		// wall-clock-derived value rather than fail the whole suite.
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return n.String()
}

// createThrowawayDB creates a uniquely-named database on the configured test
// host, pre-creates the `vector` + `uuid-ossp` extensions inside it (mirroring
// what docker-entrypoint-initdb.d does before the app boots — cmd/server/
// main.go calls db.New(), which registers pgvector types, BEFORE db.Migrate()
// ever runs 001's own `CREATE EXTENSION`), and returns a DatabaseConfig
// pointed at the new database plus a cleanup func that drops it.
func createThrowawayDB(t *testing.T, admin config.DatabaseConfig) (config.DatabaseConfig, func()) {
	t.Helper()
	ctx := context.Background()

	adminConn, err := pgx.Connect(ctx, admin.DSNWithTimeout())
	if err != nil {
		t.Fatalf("connect to admin database %q: %v", admin.Database, err)
	}
	defer adminConn.Close(ctx)

	dbName := "skillsys_test_" + randomSuffix()
	createSQL := fmt.Sprintf("CREATE DATABASE %s", pgx.Identifier{dbName}.Sanitize())
	if _, err := adminConn.Exec(ctx, createSQL); err != nil {
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
			return // best-effort cleanup; a leaked throwaway test DB is not fatal
		}
		defer c.Close(cctx)
		// Terminate any lingering backends so DROP DATABASE doesn't fail with
		// "database is being accessed by other users".
		_, _ = c.Exec(cctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, dbName)
		_, _ = c.Exec(cctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", pgx.Identifier{dbName}.Sanitize()))
	}

	return dbCfg, cleanup
}
