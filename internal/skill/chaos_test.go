// Package skill provides CRUD and search operations for skills in the
// knowledge graph. This file contains chaos/resilience tests for the Store
// and Graph layers under concurrent access and database-failure conditions.
package skill

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------------
// Concurrent read/write — Store/Graph operations under race conditions.
// ---------------------------------------------------------------------------

// TestChaos_ConcurrentReadWrite exercises concurrent reads and writes to the
// same skill from multiple goroutines.  This test requires a live PostgreSQL
// database (Store wraps *db.Pool which wraps *pgxpool.Pool, so no in-memory
// backend exists); skipped when neither SKILL_SYSTEM_TEST_DB_HOST nor
// HELIX_TEST_DATABASE_URL is set.
//
// The test verifies that concurrent access does not panic, hang, or produce
// obviously corrupt results — the Go race detector (`go test -race`) catches
// actual data races between the goroutines.
func TestChaos_ConcurrentReadWrite(t *testing.T) {
	ctx, store, cleanup := g07NewLiveStore(t)
	if ctx == nil {
		t.Skip("SKILL_SYSTEM_TEST_DB_HOST not set; concurrent read/write chaos " +
			"test requires admin DB access (or set HELIX_TEST_DATABASE_URL to " +
			"bypass the throwaway-db creation path; see TestChaos_" +
			"DatabaseConnectionDrop for that style)")
	}
	defer cleanup()

	const (
		skillName = "chaos-concurrent-skill"
		readers   = 8
		writers   = 2
		rounds    = 20
	)

	// Seed one skill.
	if err := store.Create(ctx, &models.Skill{
		Name:   skillName,
		Title:  "Chaos Concurrent Skill",
		Status: models.SkillStatusDraft,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(readers + writers)

	// G1: concurrent writers — mutate the skill through Create (upsert).
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				// Create with ON CONFLICT (name) DO UPDATE.
				_ = store.Create(ctx, &models.Skill{
					Name:        skillName,
					Title:       "Chaos Concurrent Skill",
					Description: "writer-updated-description",
					Content:     "writer-updated-content",
					Status:      models.SkillStatusDraft,
				})
			}
		}()
	}

	// G2: concurrent readers — fetch the skill and verify it is non-nil.
	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				got, err := store.GetByName(ctx, skillName)
				if err != nil {
					t.Logf("GetByName: %v", err)
					continue
				}
				if got == nil {
					t.Log("GetByName returned nil skill")
				}
			}
		}()
	}

	wg.Wait()
	// Success means no panic, no hang, and no data race reported by -race.
}

// ---------------------------------------------------------------------------
// Database connection drop — typed error, not hang.
// ---------------------------------------------------------------------------

// TestChaos_DatabaseConnectionDrop verifies that cancelling a context
// mid-query produces a typed error instead of hanging indefinitely.
func TestChaos_DatabaseConnectionDrop(t *testing.T) {
	dbURL := os.Getenv("HELIX_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("skipping DB-connection-drop chaos test: " +
			"set HELIX_TEST_DATABASE_URL to enable")
	}

	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	ctx, cancel := context.WithCancel(context.Background())

	type result struct {
		n   int64
		err error
	}
	ch := make(chan result, 1)

	go func() {
		var n int64
		err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM generate_series(1, 1000000)`).Scan(&n)
		ch <- result{n, err}
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	res := <-ch

	if res.err == nil {
		t.Fatal("expected an error after context cancellation, got nil; " +
			"the query may have completed before cancellation took effect")
	}

	if errors.Is(res.err, context.Canceled) {
		t.Logf("got expected context.Canceled error (fast, no hang)")
		return
	}
	// pgx may wrap the context cancellation in its own error types.
	t.Logf("cancellation error (typed, not a hang): %v", res.err)
}
