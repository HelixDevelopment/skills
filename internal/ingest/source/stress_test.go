// Package source defines the addressable-origin abstraction for the skill
// ingestion pipeline. This file contains stress/high-concurrency tests for
// concurrent filesystem source operations.
package source

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

// percentile computes the p-th percentile (0-100) from a sorted slice of
// durations. Returns 0 for an empty slice.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p/100*float64(len(sorted))) - 1)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// roundDuration rounds d to the nearest millisecond for stable display.
func roundDuration(d time.Duration) time.Duration {
	return d.Round(time.Millisecond)
}

// ---------------------------------------------------------------------------
// TestStress_ConcurrentListAndFetch
//
// Spawns N=30 concurrent goroutines, each listing and fetching from a
// filesystem source backed by a temp directory. Verifies no races, no
// panics, and records per-op latency p50/p95/p99.
// ---------------------------------------------------------------------------

func TestStress_ConcurrentListAndFetch(t *testing.T) {
	const n = 30

	// Create a temp directory with some files.
	tmpDir := t.TempDir()
	for i := 0; i < 10; i++ {
		fname := filepath.Join(tmpDir, "file"+string(rune('a'+i))+".md")
		if err := os.WriteFile(fname, []byte("content"), 0644); err != nil {
			t.Fatalf("setup: WriteFile: %v", err)
		}
	}

	fs, err := NewFilesystemSource(tmpDir, []string{tmpDir})
	if err != nil {
		t.Fatalf("NewFilesystemSource: %v", err)
	}

	ctx := context.Background()
	var wg sync.WaitGroup
	latencies := make([]time.Duration, n)
	var latMu sync.Mutex

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			start := time.Now()

			// List items.
			items, err := fs.List(ctx)
			if err != nil {
				t.Errorf("goroutine %d: List: %v", idx, err)
				return
			}
			if len(items) == 0 {
				t.Errorf("goroutine %d: List returned 0 items", idx)
				return
			}

			// Fetch first item.
			_, err = fs.Fetch(ctx, items[0])
			if err != nil {
				t.Errorf("goroutine %d: Fetch: %v", idx, err)
				return
			}

			elapsed := time.Since(start)
			latMu.Lock()
			latencies[idx] = elapsed
			latMu.Unlock()
		}(i)
	}
	wg.Wait()

	// Compute and log percentiles.
	sorted := make([]time.Duration, n)
	copy(sorted, latencies)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	p50 := percentile(sorted, 50)
	p95 := percentile(sorted, 95)
	p99 := percentile(sorted, 99)

	t.Logf("ConcurrentListAndFetch (N=%d): p50=%s p95=%s p99=%s",
		n, roundDuration(p50), roundDuration(p95), roundDuration(p99))
}
