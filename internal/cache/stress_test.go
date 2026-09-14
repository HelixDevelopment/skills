// Package cache provides a Redis-backed caching layer for the HelixKnowledge
// skill graph system. This file contains stress/high-concurrency tests for
// concurrent cache access using the NoopCache (self-contained, no external
// Redis dependency).
package cache

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"sync/atomic"
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
// TestStress_ConcurrentCacheAccess
//
// Spawns N=100 goroutines performing concurrent read/write operations on
// the NoopCache. Verifies no panics, no data races, and records per-op
// latency p50/p95/p99.
// ---------------------------------------------------------------------------

func TestStress_ConcurrentCacheAccess(t *testing.T) {
	const n = 100

	c := NewNoopCache()
	ctx := context.Background()

	var wg sync.WaitGroup
	latencies := make([]time.Duration, n)
	var latMu sync.Mutex
	var ops int64

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			key := fmt.Sprintf("stress:key:%d", idx)
			value := []byte(fmt.Sprintf("value-%d", idx))

			start := time.Now()

			// Write
			if err := c.Set(ctx, key, value, 5*time.Minute); err != nil {
				t.Errorf("goroutine %d: Set: %v", idx, err)
				return
			}
			atomic.AddInt64(&ops, 1)

			// Read
			got, err := c.Get(ctx, key)
			if err != nil {
				t.Errorf("goroutine %d: Get: %v", idx, err)
				return
			}
			// NoopCache always returns nil on Get (cache miss) — that's expected.
			_ = got
			atomic.AddInt64(&ops, 1)

			// Delete
			if err := c.Delete(ctx, key); err != nil {
				t.Errorf("goroutine %d: Delete: %v", idx, err)
				return
			}
			atomic.AddInt64(&ops, 1)

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

	t.Logf("ConcurrentCacheAccess (N=%d, ops=%d): p50=%s p95=%s p99=%s",
		n, atomic.LoadInt64(&ops), roundDuration(p50), roundDuration(p95), roundDuration(p99))
}

// ---------------------------------------------------------------------------
// TestStress_ConcurrentInvalidatePattern
//
// Spawns N=50 goroutines each calling InvalidatePattern concurrently.
// Verifies no races on the NoopCache.
// ---------------------------------------------------------------------------

func TestStress_ConcurrentInvalidatePattern(t *testing.T) {
	const n = 50

	c := NewNoopCache()
	ctx := context.Background()

	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			pattern := fmt.Sprintf("stress:%d:*", idx)
			if err := c.InvalidatePattern(ctx, pattern); err != nil {
				t.Errorf("goroutine %d: InvalidatePattern: %v", idx, err)
			}
		}(i)
	}
	wg.Wait()
}
