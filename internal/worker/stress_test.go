// Package worker provides background job execution for the HelixKnowledge
// skill graph system. This file contains stress/high-concurrency tests for
// the worker job dispatch and supervisor lifecycle.
package worker

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
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
// TestStress_ConcurrentJobDispatch
//
// Spawns N=50 concurrent goroutines each submitting a job to the runner's
// job channel. Verifies no panics, no data races, and records per-dispatch
// latency p50/p95/p99.
// ---------------------------------------------------------------------------

func TestStress_ConcurrentJobDispatch(t *testing.T) {
	const n = 50

	r := &Runner{
		logger:             zap.NewNop(),
		jobChan:            make(chan Job, n),
		restartBackoffBase: time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	latencies := make([]time.Duration, n)
	var latMu sync.Mutex

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			start := time.Now()
			payload, _ := json.Marshal(map[string]string{"skill_name": "test"})
			job := Job{
				ID:      uuid.New(),
				Type:    JobTypeValidate,
				Payload: payload,
				Status:  JobStatusPending,
				Created: time.Now().UTC(),
			}

			select {
			case r.jobChan <- job:
				// dispatched
			case <-ctx.Done():
				t.Errorf("goroutine %d: context cancelled before dispatch", idx)
				return
			}

			elapsed := time.Since(start)
			latMu.Lock()
			latencies[idx] = elapsed
			latMu.Unlock()
		}(i)
	}
	wg.Wait()

	// Drain the channel to verify all jobs arrived.
	drained := 0
	for len(r.jobChan) > 0 {
		<-r.jobChan
		drained++
	}
	if drained != n {
		t.Errorf("drained %d jobs, want %d", drained, n)
	}

	// Compute and log percentiles.
	sorted := make([]time.Duration, n)
	copy(sorted, latencies)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	p50 := percentile(sorted, 50)
	p95 := percentile(sorted, 95)
	p99 := percentile(sorted, 99)

	t.Logf("ConcurrentJobDispatch (N=%d): p50=%s p95=%s p99=%s",
		n, roundDuration(p50), roundDuration(p95), roundDuration(p99))
}

// ---------------------------------------------------------------------------
// TestStress_ConcurrentSupervise
//
// Spawns N=20 concurrent supervisor goroutines, each running a short-lived
// function that completes after a brief delay. Verifies all supervisors
// exit cleanly and the WaitGroup reaches zero.
// ---------------------------------------------------------------------------

func TestStress_ConcurrentSupervise(t *testing.T) {
	const n = 20

	r := &Runner{
		logger:             zap.NewNop(),
		restartBackoffBase: time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var started int32

	for i := 0; i < n; i++ {
		r.wg.Add(1)
		go r.supervise(ctx, "stress_supervise", func(fnCtx context.Context) {
			atomic.AddInt32(&started, 1)
			// Short-lived work then exit cleanly.
			select {
			case <-fnCtx.Done():
			case <-time.After(20 * time.Millisecond):
			}
		})
	}

	// Wait for all supervisors to complete.
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// success
	case <-time.After(10 * time.Second):
		t.Fatal("supervise goroutines did not exit within 10s")
	}

	if got := atomic.LoadInt32(&started); got != int32(n) {
		t.Errorf("started %d supervisors, want %d", got, n)
	}
}
