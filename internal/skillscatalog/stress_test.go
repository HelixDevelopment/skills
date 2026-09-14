package skillscatalog

import (
	"sync"
	"testing"
)

// TestStress_ConcurrentDefaultConfig exercises concurrent DefaultConfig calls.
// N=100 goroutines, no races expected.
func TestStress_ConcurrentDefaultConfig(t *testing.T) {
	const n = 100
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cfg := DefaultConfig()
			// DefaultConfig should return a valid config.
			_ = cfg
		}()
	}
	wg.Wait()
}
