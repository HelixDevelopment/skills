package mcp

// Package-local live-database provisioning helper for this package's
// integration-style tests, following the SAME pattern (and the SAME
// SKILL_SYSTEM_TEST_DB_* environment contract) already established by
// internal/db/testdb_helper_test.go, internal/registry/testdb_helper_test.go,
// internal/skill/migration_granularity_test.go, and
// internal/worker/testdb_helper_test.go.
//
// Gated on SKILL_SYSTEM_TEST_DB_HOST: absent a configured live PostgreSQL,
// every case requiring it honestly t.Skip()s (§11.4.3/§11.4.27) -- never a
// fake PASS.

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/jackc/pgx/v5"
)

// mcpRealMigrationsDir is the on-disk migrations directory this test package
// exercises, relative to internal/mcp (this package) -- the same directory
// db.Migrate reads in production (cmd/server/main.go).
const mcpRealMigrationsDir = "../../migrations"

func mcpTestDBAdminConfig() (config.DatabaseConfig, bool) {
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

// mcpSkipIfNoTestDB is called first by every test in this suite that needs a
// live database. It returns the admin config when configured, or calls
// t.Skip with an honest, specific reason and returns ok=false.
func mcpSkipIfNoTestDB(t *testing.T) (config.DatabaseConfig, bool) {
	t.Helper()
	admin, ok := mcpTestDBAdminConfig()
	if !ok {
		t.Skip("SKILL_SYSTEM_TEST_DB_HOST not set: this case requires a live " +
			"pgvector/pgvector:pg16-class PostgreSQL instance to prove the " +
			"learn_from_project handler's ACCEPT path really submits a learning " +
			"job (audit_log INSERT) end-to-end after the §G31 guard passes -- " +
			"same boundary as this project's other _RequiresLiveDatabase tests.")
		return config.DatabaseConfig{}, false
	}
	return admin, true
}

func mcpRandomSuffix() string {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000_000))
	if err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return n.String()
}

// mcpCreateThrowawayDB creates a uniquely-named database on the configured
// test host, pre-creates the `vector` + `uuid-ossp` extensions, applies the
// real migrations, and returns a *db.Pool pointed at it plus a cleanup func.
func mcpCreateThrowawayDB(t *testing.T, admin config.DatabaseConfig) (*db.Pool, func()) {
	t.Helper()
	ctx := context.Background()

	adminConn, err := pgx.Connect(ctx, admin.DSNWithTimeout())
	if err != nil {
		t.Fatalf("connect to admin database %q: %v", admin.Database, err)
	}
	defer adminConn.Close(ctx)

	dbName := "skillsys_test_" + mcpRandomSuffix()
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
			return
		}
		defer c.Close(cctx)
		_, _ = c.Exec(cctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, dbName)
		_, _ = c.Exec(cctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", pgx.Identifier{dbName}.Sanitize()))
	}

	pool, err := db.New(dbCfg)
	if err != nil {
		cleanup()
		t.Fatalf("db.New(%q): %v", dbName, err)
	}
	if err := db.Migrate(ctx, pool, mcpRealMigrationsDir); err != nil {
		pool.Close()
		cleanup()
		t.Fatalf("db.Migrate(%q): %v", dbName, err)
	}

	return pool, func() {
		pool.Close()
		cleanup()
	}
}

// mcpCreateThrowawayDBConfig is the config-returning sibling of
// mcpCreateThrowawayDB: it creates a uniquely-named database on the configured
// test host and pre-creates the `vector` + `uuid-ossp` extensions, but does
// NOT open a *db.Pool or apply migrations -- it returns the DatabaseConfig
// pointed at the fresh database plus a cleanup func, leaving pool creation and
// migration to the caller. Used by callers (e.g. the ACP adapter live-DB test)
// that need to drive db.New/db.Migrate themselves against the throwaway DB,
// rather than receiving an already-open, already-migrated pool.
func mcpCreateThrowawayDBConfig(t *testing.T, admin config.DatabaseConfig) (config.DatabaseConfig, func()) {
	t.Helper()
	ctx := context.Background()

	adminConn, err := pgx.Connect(ctx, admin.DSNWithTimeout())
	if err != nil {
		t.Fatalf("connect to admin database %q: %v", admin.Database, err)
	}
	defer adminConn.Close(ctx)

	dbName := "skillsys_test_" + mcpRandomSuffix()
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
			return
		}
		defer c.Close(cctx)
		_, _ = c.Exec(cctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, dbName)
		_, _ = c.Exec(cctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s", pgx.Identifier{dbName}.Sanitize()))
	}

	return dbCfg, cleanup
}
