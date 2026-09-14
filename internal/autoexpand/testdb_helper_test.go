package autoexpand

// Package-local live-database provisioning helper for this package's
// DB-backed tests, following the SAME pattern (and the SAME
// SKILL_SYSTEM_TEST_DB_* environment contract) already established by
// internal/db/testdb_helper_test.go, internal/worker/testdb_helper_test.go,
// and internal/skill/migration_granularity_test.go. Each _test.go-only
// helper file in this codebase duplicates a small, package-local copy of
// this helper rather than exporting it from the db package, since these are
// test-only symbols and the db package's own copy is unexported
// test-file-scoped code that cannot be imported.
//
// Gated on SKILL_SYSTEM_TEST_DB_HOST: absent a configured live PostgreSQL,
// every case that needs it MUST t.Skip (§11.4.3/§11.4.27) -- never a fake
// PASS.
//
// This file carries NO build tag (unlike
// pipeline_crossreference_integration_test.go) because it is shared by both
// the network-free, DB-only cross-reference test
// (pipeline_crossreference_test.go) and the live-LLM-gated integration test
// -- only the latter also touches the network and therefore needs the
// `integration` tag.

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
	"github.com/jackc/pgx/v5"
)

const aeRealMigrationsDir = "../../migrations"

func aeTestDBAdminConfig() (config.DatabaseConfig, bool) {
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

func aeSkipIfNoTestDB(t *testing.T) (config.DatabaseConfig, bool) {
	t.Helper()
	admin, ok := aeTestDBAdminConfig()
	if !ok {
		t.Skip("SKILL_SYSTEM_TEST_DB_HOST not set: this case requires a live " +
			"pgvector/pgvector:pg16-class PostgreSQL instance to prove the " +
			"draft+persist+cross-reference chain against real skills/skill_dependencies " +
			"rows (§11.4.3; same boundary as internal/worker and internal/db's own " +
			"_RequiresLiveDatabase tests).")
		return config.DatabaseConfig{}, false
	}
	return admin, true
}

func aeCreateThrowawayDB(t *testing.T, admin config.DatabaseConfig) (config.DatabaseConfig, func()) {
	t.Helper()
	ctx := context.Background()

	adminConn, err := pgx.Connect(ctx, admin.DSNWithTimeout())
	if err != nil {
		t.Fatalf("connect to admin database %q: %v", admin.Database, err)
	}
	defer adminConn.Close(ctx)

	n, rerr := rand.Int(rand.Reader, big.NewInt(1_000_000_000))
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	if rerr == nil {
		suffix = n.String()
	}
	dbName := "skillsys_test_ae_" + suffix

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
