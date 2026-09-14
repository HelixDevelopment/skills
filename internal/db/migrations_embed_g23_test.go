package db

// G23 — embed.FS migrations + fail-CLOSED startup (research/ops_hardening_design.md §G23).
//
// This file guards the fs.FS refactor of the migration engine:
//   - discovery is fs.FS-based (embed.FS OR os.DirFS), so it never depends on
//     the process working directory (the pre-G23 hazard: db.Migrate(ctx, pool,
//     "./migrations") applied NOTHING when the binary ran from another dir);
//   - a migration failure surfaces as a returned error (MigrateFS never
//     swallows it), which cmd/server turns into a fatal, fail-closed startup.
//
// The determinism case needs no database (testing/fstest, §11.4.108 static
// proof). The fail-closed case needs a live pgvector instance and honestly
// t.Skip()s when SKILL_SYSTEM_TEST_DB_HOST is unset (§11.4.3/§11.4.27), reusing
// the package-local throwaway-DB helper (testdb_helper_test.go).

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

// TestDiscoverMigrationsFS_IsFsBasedAndDeterministic proves migration discovery
// runs over an io/fs.FS (here an in-memory fstest.MapFS with NO working
// directory at all), returns migrations ordered by version, yields FS-relative
// base-name paths suitable for fs.ReadFile, and skips non-migration / unversioned
// files. Because an fstest.MapFS has no cwd, a PASS here is direct evidence that
// discovery is cwd-independent — the core of the §G23 fix. (§1.1 mutation: revert
// discovery to os.ReadDir(cwd-relative dir) and this stops being cwd-independent.)
func TestDiscoverMigrationsFS_IsFsBasedAndDeterministic(t *testing.T) {
	// Deliberately out-of-order map keys + noise files that MUST be ignored.
	fsys := fstest.MapFS{
		"003_gamma.up.sql":   {Data: []byte("-- up 3")},
		"003_gamma.down.sql": {Data: []byte("-- down 3")},
		"001_alpha.up.sql":   {Data: []byte("-- up 1")},
		"001_alpha.down.sql": {Data: []byte("-- down 1")},
		"002_beta.up.sql":    {Data: []byte("-- up 2")},
		"002_beta.down.sql":  {Data: []byte("-- down 2")},
		"README.md":          {Data: []byte("not a migration")},
		"notes.sql":          {Data: []byte("-- no version prefix")},
	}

	migrations, err := discoverMigrationsFS(fsys)
	if err != nil {
		t.Fatalf("discoverMigrationsFS: %v", err)
	}

	if len(migrations) != 3 {
		t.Fatalf("discovered %d migrations, want 3 (README.md + notes.sql must be skipped); got %+v", len(migrations), migrations)
	}

	wantVersions := []int64{1, 2, 3}
	wantNames := []string{"alpha", "beta", "gamma"}
	for i, m := range migrations {
		if m.version != wantVersions[i] {
			t.Errorf("migrations[%d].version = %d, want %d (must be version-ordered)", i, m.version, wantVersions[i])
		}
		if m.name != wantNames[i] {
			t.Errorf("migrations[%d].name = %q, want %q", i, m.name, wantNames[i])
		}
		// FS paths MUST be bare base names (fs.ReadFile(fsys, path) requires an
		// unrooted, ".."-free path). A leftover filepath.Join(dir, name) would
		// re-introduce a directory prefix here.
		if strings.ContainsRune(m.upPath, '/') || !strings.HasSuffix(m.upPath, ".up.sql") {
			t.Errorf("migrations[%d].upPath = %q, want a bare NNN_*.up.sql base name", i, m.upPath)
		}
		if strings.ContainsRune(m.downPath, '/') || !strings.HasSuffix(m.downPath, ".down.sql") {
			t.Errorf("migrations[%d].downPath = %q, want a bare NNN_*.down.sql base name", i, m.downPath)
		}
		// The discovered path MUST actually read back from the same FS.
		if _, err := readAllFromFS(t, fsys, m.upPath); err != nil {
			t.Errorf("fs.ReadFile(fsys, %q): %v", m.upPath, err)
		}
	}
}

// readAllFromFS is a tiny helper that reads a file from an fs.FS via the exact
// fs.ReadFile call MigrateFS uses on the production read path
// (fs.ReadFile(fsys, m.upPath)), proving discovered paths are genuinely
// FS-addressable — not merely map-key-present. fstest.MapFS satisfies fs.FS, so
// the same call that reads the compiled-in embed.FS reads the in-memory test FS
// here.
func readAllFromFS(t *testing.T, fsys fs.FS, name string) ([]byte, error) {
	t.Helper()
	return fs.ReadFile(fsys, name)
}

// TestMigrateFS_FailsClosedOnBrokenMigration_RequiresLiveDatabase proves that a
// migration whose SQL fails causes MigrateFS to RETURN a non-nil error (never a
// silent success) and leaves the schema NOT advanced. cmd/server turns exactly
// this returned error into a fatal, fail-closed startup — the §G23 replacement
// for the pre-fix warn-and-continue. (§11.4.3: t.Skip without a live DB.)
func TestMigrateFS_FailsClosedOnBrokenMigration_RequiresLiveDatabase(t *testing.T) {
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

	// A single deliberately-invalid migration.
	brokenFS := fstest.MapFS{
		"001_broken.up.sql": {Data: []byte("THIS IS NOT VALID SQL;")},
	}

	if err := MigrateFS(ctx, pool, brokenFS); err == nil {
		t.Fatal("MigrateFS with a broken migration: expected a non-nil error (fail-closed), got nil — the failure was swallowed")
	}

	// Schema must NOT be marked as advanced: no version recorded.
	version, err := CurrentMigrationVersion(ctx, pool)
	if err != nil {
		t.Fatalf("CurrentMigrationVersion after broken migration: %v", err)
	}
	if version != -1 {
		t.Errorf("CurrentMigrationVersion after broken migration = %d, want -1 (nothing applied — fail-closed, no partial advance)", version)
	}
}
