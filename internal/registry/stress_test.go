package registry

import (
	"sync"
	"testing"
)

// TestStress_ConcurrentReviewScheduler exercises concurrent ReviewScheduler
// construction. N=100 goroutines, no races.
func TestStress_ConcurrentReviewScheduler(t *testing.T) {
	const n = 100
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// ReviewScheduler construction requires a *Registry (nil here is
			// fine for the type-level exercise — no methods are called).
			rs := &ReviewScheduler{}
			if rs.IsRunning() {
				t.Error("new ReviewScheduler should not be running")
			}
		}()
	}
	wg.Wait()
}
