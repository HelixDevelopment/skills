package skillsource

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Live-database integration tests
//
// These tests require a real pgvector/pgvector:pg16-class PostgreSQL instance.
// They are gated by SKILL_SYSTEM_TEST_DB_HOST — when unset, every case calls
// t.Skip with an honest reason (mirrors internal/db/testdb_helper_test.go).
// ---------------------------------------------------------------------------

// testDBConfig reads the SKILL_SYSTEM_TEST_DB_* environment variables and
// returns a DatabaseConfig plus whether the suite is enabled.
func testDBConfig() (config.DatabaseConfig, bool) {
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

	password := os.Getenv("SKILL_SYSTEM_TEST_DB_PASSWORD")
	dbName := os.Getenv("SKILL_SYSTEM_TEST_DB_NAME")
	if dbName == "" {
		dbName = "postgres"
	}

	return config.DatabaseConfig{
		Host:           host,
		Port:           port,
		Database:       dbName,
		User:           user,
		Password:       password,
		SSLMode:        "disable",
		MaxConnections: 4,
		ConnectTimeout: 10 * time.Second,
	}, true
}

// setupTestStore creates a Store connected to the test database and ensures
// the skill_sources table exists. Returns the store and a cleanup function.
// If no test DB is configured, the test is skipped.
func setupTestStore(t *testing.T) (*Store, func()) {
	t.Helper()
	cfg, ok := testDBConfig()
	if !ok {
		t.Skip("SKILL_SYSTEM_TEST_DB_HOST not set: this case requires a live " +
			"pgvector/pgvector:pg16-class PostgreSQL instance.")
	}

	pool, err := db.New(cfg)
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}

	// Ensure the table exists (idempotent).
	ctx := context.Background()
	_, err = pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS skill_sources (
			id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			name          TEXT NOT NULL UNIQUE,
			source_type   TEXT NOT NULL CHECK (source_type IN ('github', 'filesystem', 'url')),
			config        JSONB NOT NULL DEFAULT '{}',
			enabled       BOOLEAN NOT NULL DEFAULT TRUE,
			last_sync     TIMESTAMPTZ,
			sync_status   TEXT NOT NULL DEFAULT 'pending' CHECK (sync_status IN ('pending', 'syncing', 'completed', 'failed')),
			error_message TEXT NOT NULL DEFAULT '',
			created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	if err != nil {
		pool.Close()
		t.Fatalf("create skill_sources table: %v", err)
	}

	cleanup := func() {
		// Truncate test data and close pool.
		_, _ = pool.Exec(context.Background(), `TRUNCATE skill_sources`)
		pool.Close()
	}

	store := NewStore(pool, zap.NewNop())
	return store, cleanup
}

// uniqueName returns a unique name for test sources to avoid collisions
// between parallel test runs.
func uniqueName(prefix string) string {
	return prefix + "_" + uuid.New().String()[:8]
}

// newTestSource creates a minimal valid SkillSource for testing.
func newTestSource(name string, st SourceType) *SkillSource {
	return &SkillSource{
		Name:       name,
		SourceType: st,
		Config:     json.RawMessage(`{"owner":"test","repo":"skills"}`),
		Enabled:    true,
		SyncStatus: SyncStatusPending,
	}
}

// ---------------------------------------------------------------------------
// CRUD integration tests
// ---------------------------------------------------------------------------

func TestStore_Create(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	t.Run("create succeeds", func(t *testing.T) {
		s := newTestSource(uniqueName("create-ok"), SourceTypeGitHub)
		if err := store.Create(ctx, s); err != nil {
			t.Fatalf("Create() error: %v", err)
		}
		if s.ID == uuid.Nil {
			t.Fatal("Create() did not assign an ID")
		}
		if s.CreatedAt.IsZero() {
			t.Fatal("Create() did not set CreatedAt")
		}
		if s.SyncStatus != SyncStatusPending {
			t.Errorf("Create() SyncStatus = %q, want %q", s.SyncStatus, SyncStatusPending)
		}
	})

	t.Run("duplicate name returns ErrSourceExists", func(t *testing.T) {
		name := uniqueName("dup")
		s1 := newTestSource(name, SourceTypeGitHub)
		if err := store.Create(ctx, s1); err != nil {
			t.Fatalf("first Create() error: %v", err)
		}
		s2 := newTestSource(name, SourceTypeFilesystem)
		err := store.Create(ctx, s2)
		if err == nil {
			t.Fatal("second Create() expected ErrSourceExists")
		}
		if !isErrSourceExists(err) {
			t.Errorf("second Create() error = %v, want wrapped ErrSourceExists", err)
		}
	})

	t.Run("empty name fails validation", func(t *testing.T) {
		s := &SkillSource{
			SourceType: SourceTypeGitHub,
			Config:     json.RawMessage(`{}`),
			SyncStatus: SyncStatusPending,
		}
		err := store.Create(ctx, s)
		if err == nil {
			t.Fatal("Create() expected validation error for empty name")
		}
	})

	t.Run("invalid source type fails validation", func(t *testing.T) {
		s := newTestSource(uniqueName("bad-type"), SourceType("gitlab"))
		err := store.Create(ctx, s)
		if err == nil {
			t.Fatal("Create() expected validation error for invalid source type")
		}
	})
}

func TestStore_GetByID(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	t.Run("existing source found", func(t *testing.T) {
		s := newTestSource(uniqueName("getbyid"), SourceTypeGitHub)
		if err := store.Create(ctx, s); err != nil {
			t.Fatalf("Create() error: %v", err)
		}
		got, err := store.GetByID(ctx, s.ID)
		if err != nil {
			t.Fatalf("GetByID() error: %v", err)
		}
		if got.ID != s.ID {
			t.Errorf("GetByID() ID = %s, want %s", got.ID, s.ID)
		}
		if got.Name != s.Name {
			t.Errorf("GetByID() Name = %q, want %q", got.Name, s.Name)
		}
		if got.SourceType != SourceTypeGitHub {
			t.Errorf("GetByID() SourceType = %q, want %q", got.SourceType, SourceTypeGitHub)
		}
	})

	t.Run("non-existent ID returns ErrSourceNotFound", func(t *testing.T) {
		_, err := store.GetByID(ctx, uuid.New())
		if err == nil {
			t.Fatal("GetByID() expected ErrSourceNotFound")
		}
		if !isErrSourceNotFound(err) {
			t.Errorf("GetByID() error = %v, want ErrSourceNotFound", err)
		}
	})
}

func TestStore_GetByName(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	t.Run("existing source found", func(t *testing.T) {
		name := uniqueName("getbyname")
		s := newTestSource(name, SourceTypeFilesystem)
		if err := store.Create(ctx, s); err != nil {
			t.Fatalf("Create() error: %v", err)
		}
		got, err := store.GetByName(ctx, name)
		if err != nil {
			t.Fatalf("GetByName() error: %v", err)
		}
		if got.Name != name {
			t.Errorf("GetByName() Name = %q, want %q", got.Name, name)
		}
		if got.SourceType != SourceTypeFilesystem {
			t.Errorf("GetByName() SourceType = %q, want %q", got.SourceType, SourceTypeFilesystem)
		}
	})

	t.Run("non-existent name returns ErrSourceNotFound", func(t *testing.T) {
		_, err := store.GetByName(ctx, "does-not-exist-"+uuid.New().String()[:8])
		if err == nil {
			t.Fatal("GetByName() expected ErrSourceNotFound")
		}
		if !isErrSourceNotFound(err) {
			t.Errorf("GetByName() error = %v, want ErrSourceNotFound", err)
		}
	})
}

func TestStore_List(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	// Seed a few sources.
	names := []string{uniqueName("list-a"), uniqueName("list-b"), uniqueName("list-c")}
	for i, name := range names {
		s := newTestSource(name, SourceTypeGitHub)
		s.Enabled = i != 2 // last one disabled
		if err := store.Create(ctx, s); err != nil {
			t.Fatalf("Create(%s) error: %v", name, err)
		}
	}

	t.Run("list all", func(t *testing.T) {
		sources, err := store.List(ctx, false)
		if err != nil {
			t.Fatalf("List(false) error: %v", err)
		}
		// At least our 3 sources (other tests may have added more).
		if len(sources) < 3 {
			t.Errorf("List(false) returned %d sources, want >= 3", len(sources))
		}
	})

	t.Run("list enabled only", func(t *testing.T) {
		sources, err := store.List(ctx, true)
		if err != nil {
			t.Fatalf("List(true) error: %v", err)
		}
		for _, s := range sources {
			if !s.Enabled {
				t.Errorf("List(true) returned disabled source %q", s.Name)
			}
		}
	})
}

func TestStore_Update(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	t.Run("update succeeds", func(t *testing.T) {
		s := newTestSource(uniqueName("update"), SourceTypeGitHub)
		if err := store.Create(ctx, s); err != nil {
			t.Fatalf("Create() error: %v", err)
		}
		s.Name = uniqueName("updated")
		s.Enabled = false
		s.Config = json.RawMessage(`{"owner":"new","repo":"skills"}`)
		if err := store.Update(ctx, s); err != nil {
			t.Fatalf("Update() error: %v", err)
		}
		got, err := store.GetByID(ctx, s.ID)
		if err != nil {
			t.Fatalf("GetByID() error: %v", err)
		}
		if got.Name != s.Name {
			t.Errorf("Update() Name = %q, want %q", got.Name, s.Name)
		}
		if got.Enabled {
			t.Error("Update() Enabled should be false")
		}
	})

	t.Run("non-existent ID returns ErrSourceNotFound", func(t *testing.T) {
		s := newTestSource(uniqueName("nope"), SourceTypeGitHub)
		s.ID = uuid.New()
		err := store.Update(ctx, s)
		if err == nil {
			t.Fatal("Update() expected ErrSourceNotFound")
		}
		if !isErrSourceNotFound(err) {
			t.Errorf("Update() error = %v, want ErrSourceNotFound", err)
		}
	})
}

func TestStore_Delete(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	t.Run("delete succeeds", func(t *testing.T) {
		s := newTestSource(uniqueName("delete"), SourceTypeGitHub)
		if err := store.Create(ctx, s); err != nil {
			t.Fatalf("Create() error: %v", err)
		}
		if err := store.Delete(ctx, s.ID); err != nil {
			t.Fatalf("Delete() error: %v", err)
		}
		_, err := store.GetByID(ctx, s.ID)
		if !isErrSourceNotFound(err) {
			t.Errorf("GetByID after Delete() error = %v, want ErrSourceNotFound", err)
		}
	})

	t.Run("non-existent ID returns ErrSourceNotFound", func(t *testing.T) {
		err := store.Delete(ctx, uuid.New())
		if err == nil {
			t.Fatal("Delete() expected ErrSourceNotFound")
		}
		if !isErrSourceNotFound(err) {
			t.Errorf("Delete() error = %v, want ErrSourceNotFound", err)
		}
	})
}

// ---------------------------------------------------------------------------
// Sync status transitions
// ---------------------------------------------------------------------------

func TestStore_UpdateSyncStatus(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	t.Run("pending -> syncing", func(t *testing.T) {
		s := newTestSource(uniqueName("sync-1"), SourceTypeGitHub)
		if err := store.Create(ctx, s); err != nil {
			t.Fatalf("Create() error: %v", err)
		}
		if err := store.UpdateSyncStatus(ctx, s.ID, SyncStatusSyncing, ""); err != nil {
			t.Fatalf("UpdateSyncStatus(syncing) error: %v", err)
		}
		got, err := store.GetByID(ctx, s.ID)
		if err != nil {
			t.Fatalf("GetByID() error: %v", err)
		}
		if got.SyncStatus != SyncStatusSyncing {
			t.Errorf("SyncStatus = %q, want %q", got.SyncStatus, SyncStatusSyncing)
		}
	})

	t.Run("syncing -> completed sets last_sync", func(t *testing.T) {
		s := newTestSource(uniqueName("sync-2"), SourceTypeGitHub)
		if err := store.Create(ctx, s); err != nil {
			t.Fatalf("Create() error: %v", err)
		}
		_ = store.UpdateSyncStatus(ctx, s.ID, SyncStatusSyncing, "")
		if err := store.UpdateSyncStatus(ctx, s.ID, SyncStatusCompleted, ""); err != nil {
			t.Fatalf("UpdateSyncStatus(completed) error: %v", err)
		}
		got, err := store.GetByID(ctx, s.ID)
		if err != nil {
			t.Fatalf("GetByID() error: %v", err)
		}
		if got.SyncStatus != SyncStatusCompleted {
			t.Errorf("SyncStatus = %q, want %q", got.SyncStatus, SyncStatusCompleted)
		}
		if got.LastSync == nil {
			t.Error("LastSync should be set after completed sync")
		}
		if got.ErrorMessage != "" {
			t.Errorf("ErrorMessage = %q, should be cleared on success", got.ErrorMessage)
		}
	})

	t.Run("syncing -> failed sets error_message and last_sync", func(t *testing.T) {
		s := newTestSource(uniqueName("sync-3"), SourceTypeFilesystem)
		if err := store.Create(ctx, s); err != nil {
			t.Fatalf("Create() error: %v", err)
		}
		_ = store.UpdateSyncStatus(ctx, s.ID, SyncStatusSyncing, "")
		errMsg := "connection timeout: rate limit exceeded"
		if err := store.UpdateSyncStatus(ctx, s.ID, SyncStatusFailed, errMsg); err != nil {
			t.Fatalf("UpdateSyncStatus(failed) error: %v", err)
		}
		got, err := store.GetByID(ctx, s.ID)
		if err != nil {
			t.Fatalf("GetByID() error: %v", err)
		}
		if got.SyncStatus != SyncStatusFailed {
			t.Errorf("SyncStatus = %q, want %q", got.SyncStatus, SyncStatusFailed)
		}
		if got.LastSync == nil {
			t.Error("LastSync should be set after failed sync")
		}
		if got.ErrorMessage != errMsg {
			t.Errorf("ErrorMessage = %q, want %q", got.ErrorMessage, errMsg)
		}
	})

	t.Run("failed -> completed clears error_message", func(t *testing.T) {
		s := newTestSource(uniqueName("sync-4"), SourceTypeURL)
		if err := store.Create(ctx, s); err != nil {
			t.Fatalf("Create() error: %v", err)
		}
		_ = store.UpdateSyncStatus(ctx, s.ID, SyncStatusFailed, "old error")
		if err := store.UpdateSyncStatus(ctx, s.ID, SyncStatusCompleted, ""); err != nil {
			t.Fatalf("UpdateSyncStatus(completed) error: %v", err)
		}
		got, err := store.GetByID(ctx, s.ID)
		if err != nil {
			t.Fatalf("GetByID() error: %v", err)
		}
		if got.ErrorMessage != "" {
			t.Errorf("ErrorMessage = %q, should be cleared on success", got.ErrorMessage)
		}
	})

	t.Run("invalid status returns error", func(t *testing.T) {
		s := newTestSource(uniqueName("sync-5"), SourceTypeGitHub)
		if err := store.Create(ctx, s); err != nil {
			t.Fatalf("Create() error: %v", err)
		}
		err := store.UpdateSyncStatus(ctx, s.ID, SyncStatus("running"), "")
		if err == nil {
			t.Fatal("UpdateSyncStatus(running) expected error")
		}
	})

	t.Run("non-existent ID returns ErrSourceNotFound", func(t *testing.T) {
		err := store.UpdateSyncStatus(ctx, uuid.New(), SyncStatusCompleted, "")
		if err == nil {
			t.Fatal("UpdateSyncStatus(expected ErrSourceNotFound")
		}
		if !isErrSourceNotFound(err) {
			t.Errorf("UpdateSyncStatus() error = %v, want ErrSourceNotFound", err)
		}
	})
}

// ---------------------------------------------------------------------------
// Error classification helpers
// ---------------------------------------------------------------------------

func isErrSourceNotFound(err error) bool {
	return err != nil && contains(err.Error(), ErrSourceNotFound.Error())
}

func isErrSourceExists(err error) bool {
	return err != nil && contains(err.Error(), ErrSourceExists.Error())
}

// Ensure pgx import is used (the test helper connects via db.New, but the
// store_test file itself references pgx indirectly through the db package).
// This blank import prevents the compiler from flagging unused imports if
// future tests need direct pgx access.
var _ = pgx.ErrNoRows
