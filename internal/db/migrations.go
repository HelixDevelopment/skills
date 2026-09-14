package db

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// Migrate runs all pending "up" migrations found in migrationsDir. It tracks
// applied versions in the schema_migrations table (created automatically).
//
// Migration files must be named as NNN_description.up.sql and
// NNN_description.down.sql where NNN is a zero-padded version number.
// Migrations are executed in version order inside transactions.
//
// Migrate is a thin wrapper over MigrateFS that reads the migrations from an
// on-disk directory (os.DirFS). Production startup (cmd/server) uses MigrateFS
// with the embedded migrations FS so migrations apply independently of the
// process working directory (§G23); this string-path form is retained for the
// test suites that drive a controlled on-disk migration subset.
func Migrate(ctx context.Context, pool *Pool, migrationsDir string) error {
	return MigrateFS(ctx, pool, os.DirFS(migrationsDir))
}

// MigrateFS runs all pending "up" migrations discovered in fsys (an io/fs.FS).
// Both an embed.FS (the compiled-in migrations, cwd-independent) and
// os.DirFS(dir) (an on-disk directory) satisfy fs.FS, so the same discovery and
// apply logic serves the production embedded path AND the on-disk test path.
//
// fsys MUST be rooted at the migrations directory itself, so its entries are
// the bare NNN_description.{up,down}.sql filenames (skillsystem.MigrationsFS and
// os.DirFS(dir) both satisfy this).
//
// It tracks applied versions in the schema_migrations table (created
// automatically) and applies each pending migration in version order inside a
// transaction. Any discovery, read, or apply failure is returned to the caller
// UNMODIFIED-in-severity: it is a hard error, never swallowed — cmd/server
// treats it as fatal and refuses to bind the port (§G23 fail-closed).
func MigrateFS(ctx context.Context, pool *Pool, fsys fs.FS) error {
	log := zap.L()

	// Ensure the migrations tracking table exists.
	if err := ensureMigrationsTable(ctx, pool); err != nil {
		return fmt.Errorf("ensure migrations table: %w", err)
	}

	// Read already-applied versions.
	applied, err := listAppliedVersions(ctx, pool)
	if err != nil {
		return fmt.Errorf("list applied migrations: %w", err)
	}
	appliedSet := make(map[int64]struct{}, len(applied))
	for _, v := range applied {
		appliedSet[v] = struct{}{}
	}

	// Discover migration files in the FS (embedded or on-disk).
	migrations, err := discoverMigrationsFS(fsys)
	if err != nil {
		return fmt.Errorf("discover migrations: %w", err)
	}

	var runCount int
	for _, m := range migrations {
		if _, done := appliedSet[m.version]; done {
			continue // already applied
		}

		log.Info("applying migration",
			zap.Int64("version", m.version),
			zap.String("name", m.name),
		)

		sqlBytes, err := fs.ReadFile(fsys, m.upPath)
		if err != nil {
			return fmt.Errorf("read migration %d: %w", m.version, err)
		}

		if err := runMigrationSQL(ctx, pool, m.version, string(sqlBytes), true); err != nil {
			return fmt.Errorf("apply migration %d: %w", m.version, err)
		}

		runCount++
		log.Info("migration applied", zap.Int64("version", m.version))
	}

	if runCount == 0 {
		log.Info("no pending migrations")
	} else {
		log.Info("migrations complete", zap.Int("applied", runCount))
	}

	return nil
}

// MigrateDown rolls back the last n applied migrations. Use with caution;
// in production prefer targeted down migrations during maintenance windows.
//
// Like Migrate, this is a thin wrapper over MigrateDownFS reading from an
// on-disk directory (os.DirFS); the string-path form is retained for the test
// suites.
func MigrateDown(ctx context.Context, pool *Pool, migrationsDir string, n int) error {
	return MigrateDownFS(ctx, pool, os.DirFS(migrationsDir), n)
}

// MigrateDownFS rolls back the last n applied migrations, reading the down
// migrations from fsys (an io/fs.FS rooted at the migrations directory). See
// MigrateFS for the FS-shape contract.
func MigrateDownFS(ctx context.Context, pool *Pool, fsys fs.FS, n int) error {
	log := zap.L().With(zap.Int("steps", n))

	if n <= 0 {
		return nil
	}

	if err := ensureMigrationsTable(ctx, pool); err != nil {
		return fmt.Errorf("ensure migrations table: %w", err)
	}

	// Read applied versions in descending order.
	applied, err := listAppliedVersionsDesc(ctx, pool)
	if err != nil {
		return fmt.Errorf("list applied migrations: %w", err)
	}

	if len(applied) == 0 {
		log.Info("no migrations to roll back")
		return nil
	}

	// Build lookup of available down migrations.
	available, err := discoverMigrationsFS(fsys)
	if err != nil {
		return fmt.Errorf("discover migrations: %w", err)
	}
	downMap := make(map[int64]string, len(available))
	for _, m := range available {
		if m.downPath != "" {
			downMap[m.version] = m.downPath
		}
	}

	steps := n
	if steps > len(applied) {
		steps = len(applied)
	}

	for i := 0; i < steps; i++ {
		version := applied[i]
		downPath, ok := downMap[version]
		if !ok {
			return fmt.Errorf("no down migration found for version %d", version)
		}

		sqlBytes, err := fs.ReadFile(fsys, downPath)
		if err != nil {
			return fmt.Errorf("read down migration %d: %w", version, err)
		}

		log.Info("rolling back migration", zap.Int64("version", version))

		if err := runMigrationSQL(ctx, pool, version, string(sqlBytes), false); err != nil {
			return fmt.Errorf("rollback migration %d: %w", version, err)
		}

		log.Info("migration rolled back", zap.Int64("version", version))
	}

	return nil
}

// ---------------------------------------------------------------------------
// Migration discovery
// ---------------------------------------------------------------------------

// migration holds metadata about a single migration version.
type migration struct {
	version  int64
	name     string
	upPath   string
	downPath string
}

// discoverMigrationsFS scans fsys for .up.sql and .down.sql files at its root
// (".") and returns them sorted by version number. fsys is any io/fs.FS —
// embed.FS (compiled-in migrations, cwd-independent) or os.DirFS(dir) (an
// on-disk directory) — so discovery never depends on the process working
// directory. The returned migration paths (upPath/downPath) are FS-relative
// base filenames suitable for fs.ReadFile(fsys, path).
func discoverMigrationsFS(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations directory: %w", err)
	}

	ups := make(map[int64]string)
	downs := make(map[int64]string)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}

		// Expected format: NNN_description.up.sql or NNN_description.down.sql
		parts := strings.SplitN(name, "_", 2)
		if len(parts) < 2 {
			continue // skip files without version prefix
		}

		version, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			continue // skip non-numeric prefixes
		}

		if strings.HasSuffix(name, ".up.sql") {
			ups[version] = name
		} else if strings.HasSuffix(name, ".down.sql") {
			downs[version] = name
		}
	}

	// Build ordered list.
	var versions []int64
	for v := range ups {
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })

	var result []migration
	for _, v := range versions {
		upName := ups[v]
		base := strings.TrimPrefix(upName, fmt.Sprintf("%03d_", v))
		base = strings.TrimSuffix(base, ".up.sql")

		result = append(result, migration{
			version:  v,
			name:     base,
			upPath:   upName,
			downPath: downs[v],
		})
	}

	return result, nil
}

// ---------------------------------------------------------------------------
// Schema migrations table
// ---------------------------------------------------------------------------

const ensureMigrationsTableSQL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    BIGINT PRIMARY KEY,
    applied_at TIMESTAMPTZ DEFAULT NOW()
);
`

func ensureMigrationsTable(ctx context.Context, pool *Pool) error {
	_, err := pool.Exec(ctx, ensureMigrationsTableSQL)
	return err
}

// ---------------------------------------------------------------------------
// Applied version queries
// ---------------------------------------------------------------------------

const listAppliedVersionsSQL = `SELECT version FROM schema_migrations ORDER BY version ASC`

func listAppliedVersions(ctx context.Context, pool *Pool) ([]int64, error) {
	rows, err := pool.Query(ctx, listAppliedVersionsSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var versions []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		versions = append(versions, v)
	}
	return versions, rows.Err()
}

const listAppliedVersionsDescSQL = `SELECT version FROM schema_migrations ORDER BY version DESC`

func listAppliedVersionsDesc(ctx context.Context, pool *Pool) ([]int64, error) {
	rows, err := pool.Query(ctx, listAppliedVersionsDescSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var versions []int64
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		versions = append(versions, v)
	}
	return versions, rows.Err()
}

// ---------------------------------------------------------------------------
// Migration execution
// ---------------------------------------------------------------------------

const insertMigrationRecordSQL = `INSERT INTO schema_migrations (version) VALUES ($1)`
const deleteMigrationRecordSQL = `DELETE FROM schema_migrations WHERE version = $1`

// runMigrationSQL executes migration SQL inside a transaction.
// If up=true it records the version; if up=false it deletes the record.
func runMigrationSQL(ctx context.Context, pool *Pool, version int64, sql string, up bool) error {
	conn, err := pool.inner.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	// Execute migration SQL.
	if _, err := tx.Exec(ctx, sql); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("exec migration sql: %w", err)
	}

	// Record or remove migration tracking.
	if up {
		if _, err := tx.Exec(ctx, insertMigrationRecordSQL, version); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration version: %w", err)
		}
	} else {
		if _, err := tx.Exec(ctx, deleteMigrationRecordSQL, version); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("remove migration version: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Version info
// ---------------------------------------------------------------------------

// CurrentMigrationVersion returns the highest applied migration version,
// or -1 if no migrations have been applied.
func CurrentMigrationVersion(ctx context.Context, pool *Pool) (int64, error) {
	const sql = `SELECT COALESCE(MAX(version), -1) FROM schema_migrations`
	var version int64
	err := pool.QueryRow(ctx, sql).Scan(&version)
	if err != nil {
		return -1, fmt.Errorf("query current migration version: %w", err)
	}
	return version, nil
}
