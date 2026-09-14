// Package github provides a minimal REST client for the GitHub API.
// This file contains stress/high-concurrency tests for concurrent
// repository fetch operations using a mock HTTP server.
package github

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
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
// TestStress_ConcurrentRepoFetch
//
// Spawns N=30 concurrent goroutines, each fetching a repository tree from
// a mock HTTP server. Verifies no races, no panics, and records per-fetch
// latency p50/p95/p99.
// ---------------------------------------------------------------------------

func TestStress_ConcurrentRepoFetch(t *testing.T) {
	const n = 30

	// Mock GitHub API server that returns a small tree.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := treeResponse{
			SHA: "abc123",
			Tree: []TreeEntry{
				{Path: "README.md", Type: "blob", SHA: "def456"},
				{Path: "src", Type: "tree", SHA: "ghi789"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient("", zap.NewNop())
	c.SetBaseURL(srv.URL)

	ctx := context.Background()
	var wg sync.WaitGroup
	latencies := make([]time.Duration, n)
	var latMu sync.Mutex

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			start := time.Now()
			result, err := c.ListTreeRecursive(ctx, "owner", "repo", "main")
			elapsed := time.Since(start)

			latMu.Lock()
			latencies[idx] = elapsed
			latMu.Unlock()

			if err != nil {
				t.Errorf("goroutine %d: ListTreeRecursive: %v", idx, err)
				return
			}
			if result == nil {
				t.Errorf("goroutine %d: ListTreeRecursive returned nil", idx)
			}
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

	t.Logf("ConcurrentRepoFetch (N=%d): p50=%s p95=%s p99=%s",
		n, roundDuration(p50), roundDuration(p95), roundDuration(p99))
}

// ---------------------------------------------------------------------------
// TestStress_ConcurrentGetHeadSHA
//
// Spawns N=30 concurrent goroutines, each resolving a head SHA from a mock
// HTTP server. Verifies no races.
// ---------------------------------------------------------------------------

func TestStress_ConcurrentGetHeadSHA(t *testing.T) {
	const n = 30

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := commitResponse{SHA: "abc123def456"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	c := NewClient("", zap.NewNop())
	c.SetBaseURL(srv.URL)

	ctx := context.Background()
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sha, err := c.GetHeadSHA(ctx, "owner", "repo", "main")
			if err != nil {
				t.Errorf("goroutine %d: GetHeadSHA: %v", idx, err)
				return
			}
			if sha == "" {
				t.Errorf("goroutine %d: GetHeadSHA returned empty sha", idx)
			}
		}(i)
	}
	wg.Wait()
}
