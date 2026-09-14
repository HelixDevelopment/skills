package registry

// Package-local live-database provisioning helper for this package's
// integration tests, following the SAME pattern (and the SAME
// SKILL_SYSTEM_TEST_DB_* environment contract) already established by
// internal/db/testdb_helper_test.go, internal/skill/migration_granularity_test.go,
// and internal/worker/testdb_helper_test.go.
//
// Gated on SKILL_SYSTEM_TEST_DB_HOST: absent a configured live PostgreSQL,
// every case in this package honestly t.Skip()s (§11.4.3/§11.4.27) --
// never a fake PASS.

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

const registryRealMigrationsDir = "../../migrations"

func registryTestDBAdminConfig() (config.DatabaseConfig, bool) {
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

func registrySkipIfNoTestDB(t *testing.T) (config.DatabaseConfig, bool) {
	t.Helper()
	admin, ok := registryTestDBAdminConfig()
	if !ok {
		t.Skip("SKILL_SYSTEM_TEST_DB_HOST not set: this case requires a live " +
			"pgvector/pgvector:pg16-class PostgreSQL instance to prove RunReviewOnce's " +
			"and performReview's real effect on skill_registry rows (research/" +
			"p05_high_defect_fix_designs.md §4.3 step 2/§4.6; same boundary as this " +
			"project's other _RequiresLiveDatabase tests).")
		return config.DatabaseConfig{}, false
	}
	return admin, true
}

func registryCreateThrowawayDB(t *testing.T, admin config.DatabaseConfig) (config.DatabaseConfig, func()) {
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
	dbName := "skillsys_test_" + suffix

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
