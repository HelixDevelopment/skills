// Package metrics provides Prometheus instrumentation for the HelixKnowledge
// skill graph system. This file contains stress/high-concurrency tests for
// concurrent metric recording.
package metrics

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
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
// TestStress_ConcurrentMetricRecord
//
// Spawns N=200 goroutines each recording multiple metric types concurrently.
// Verifies no panics, no data races, and records per-op latency p50/p95/p99.
// ---------------------------------------------------------------------------

func TestStress_ConcurrentMetricRecord(t *testing.T) {
	const n = 200

	m := NewRegistry(true)

	var wg sync.WaitGroup
	latencies := make([]time.Duration, n)
	var latMu sync.Mutex

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			start := time.Now()

			endpoint := fmt.Sprintf("/api/v1/skills/%d", idx%10)
			method := "GET"
			status := "200"
			if idx%5 == 0 {
				status = "500"
			}

			// Record API request.
			m.ObserveAPIRequest(endpoint, method, status, 50*time.Millisecond)

			// Record search latency.
			m.ObserveSearch(25 * time.Millisecond)

			// Record worker job.
			jobType := "autoexpand"
			if idx%2 == 0 {
				jobType = "validate"
			}
			m.ObserveWorkerJob(jobType, "completed")

			// Record embedding latency.
			m.ObserveEmbedding(10 * time.Millisecond)

			// Record DB connections.
			m.SetDBConnections("primary", idx%20)

			// Record cache hit/miss.
			if idx%3 == 0 {
				m.RecordCacheHit()
			} else {
				m.RecordCacheMiss()
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

	t.Logf("ConcurrentMetricRecord (N=%d): p50=%s p95=%s p99=%s",
		n, roundDuration(p50), roundDuration(p95), roundDuration(p99))
}

// ---------------------------------------------------------------------------
// TestStress_ConcurrentHTTPMiddleware
//
// Spawns N=100 goroutines each exercising the HTTP middleware concurrently
// via the statusWriter wrapper. Verifies no races on the status capture.
// ---------------------------------------------------------------------------

func TestStress_ConcurrentHTTPMiddleware(t *testing.T) {
	const n = 100

	m := NewRegistry(true)
	middleware := HTTPMiddleware(m)

	// Simple handler that writes a response.
	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))

	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := httptest.NewRequest("GET", fmt.Sprintf("/api/v1/skills/%d", idx), nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Errorf("goroutine %d: status %d, want 200", idx, w.Code)
			}
		}(i)
	}
	wg.Wait()
}
