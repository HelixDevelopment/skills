// Package worker provides background job execution for the HelixKnowledge
// skill graph system. This file contains chaos/resilience tests for the
// worker goroutine lifecycle, panic recovery, and leak detection.
package worker

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Worker panic safety — the process must survive a panicking handler.
// ---------------------------------------------------------------------------

// TestChaos_WorkerPanicSafety verifies that a panic in a handler is recovered
// and does not crash the process. Every sub-test exercises the real production
// supervise/recoverJob firewalls (G11).
func TestChaos_WorkerPanicSafety(t *testing.T) {
	t.Run("malformed coverage map logs error not panic", func(t *testing.T) {
		r := &Runner{
			logger:             zap.NewNop(),
			restartBackoffBase: time.Millisecond,
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		r.wg.Add(1)
		go r.supervise(ctx, "chaos_coverage", func(fnCtx context.Context) {
			// Simulate a nil-map write — assignment to entry in nil map panics.
			var m map[string]int
			m["coverage"] = 100
			<-fnCtx.Done()
		})

		// Let the panic fire and the restart-backoff cycle begin, then cancel.
		time.Sleep(50 * time.Millisecond)
		cancel()

		done := make(chan struct{})
		go func() { r.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("supervise did not return after ctx cancellation")
		}

		if got := r.GetMetrics().PanicsRecovered; got < 1 {
			t.Fatalf("PanicsRecovered = %d, want >= 1: a recovered nil-map write must be counted", got)
		}
	})

	t.Run("nil channel in select recovered", func(t *testing.T) {
		r := &Runner{
			logger:             zap.NewNop(),
			restartBackoffBase: time.Millisecond,
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		r.wg.Add(1)
		go r.supervise(ctx, "chaos_nilchan", func(fnCtx context.Context) {
			// close(nil) panics: "close of nil channel"
			var nilChan chan struct{}
			close(nilChan)
			<-fnCtx.Done()
		})

		time.Sleep(50 * time.Millisecond)
		cancel()

		done := make(chan struct{})
		go func() { r.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("supervise did not return after ctx cancellation")
		}

		if got := r.GetMetrics().PanicsRecovered; got < 1 {
			t.Fatalf("PanicsRecovered = %d, want >= 1: a recovered nil-close must be counted", got)
		}
	})

	t.Run("division by zero in handler recovered", func(t *testing.T) {
		r := &Runner{
			logger:             zap.NewNop(),
			restartBackoffBase: time.Millisecond,
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		r.wg.Add(1)
		go r.supervise(ctx, "chaos_divzero", func(fnCtx context.Context) {
			var a, b int = 1, 0
			_ = a / b // integer division by zero panics
			<-fnCtx.Done()
		})

		time.Sleep(50 * time.Millisecond)
		cancel()

		done := make(chan struct{})
		go func() { r.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("supervise did not return after ctx cancellation")
		}

		if got := r.GetMetrics().PanicsRecovered; got < 1 {
			t.Fatalf("PanicsRecovered = %d, want >= 1: a recovered div-by-zero must be counted", got)
		}
	})
}

// ---------------------------------------------------------------------------
// Goroutine leak detection — cancellation must terminate all goroutines.
// ---------------------------------------------------------------------------

// TestChaos_GoroutineLeakOnCancel creates a context, spawns goroutines that
// loop doing work, cancels the context, and verifies every goroutine exits
// within a timeout. Uses t.Cleanup to register a late check and
// sync.WaitGroup for deterministic rendezvous.
func TestChaos_GoroutineLeakOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	const numGoroutines = 10
	var wg sync.WaitGroup

	// Track how many goroutines observed ctx.Done.
	var exited int32

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					atomic.AddInt32(&exited, 1)
					return
				default:
					// Simulate a small unit of work.
					time.Sleep(5 * time.Millisecond)
				}
			}
		}(i)
	}

	// Cancel the context — all goroutines should unblock from their select.
	cancel()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All goroutines exited — success.
	case <-time.After(5 * time.Second):
		t.Fatal("goroutines did not exit within 5s after context cancellation; possible leak")
	}

	if got := atomic.LoadInt32(&exited); got != numGoroutines {
		t.Fatalf("exited goroutines = %d, want %d: not all workers observed ctx.Done", got, numGoroutines)
	}
}
