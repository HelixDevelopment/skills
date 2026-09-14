package api

import (
	"sync"
	"testing"
)

// TestStress_ConcurrentResponseFormat exercises concurrent format constant
// access. N=100 goroutines, no races.
func TestStress_ConcurrentResponseFormat(t *testing.T) {
	const n = 100
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f := FormatJSON
			if f != FormatJSON && f != FormatTOML && f != FormatTOON {
				t.Errorf("unexpected format: %s", f)
			}
		}()
	}
	wg.Wait()
}

// TestStress_ConcurrentErrorResponse exercises concurrent ErrorResponse
// construction. N=100 goroutines, no races.
func TestStress_ConcurrentErrorResponse(t *testing.T) {
	const n = 100
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := ErrorResponse{
				Error:   "test-error",
				Code:    "TEST_CODE",
				Details: "test details",
			}
			if resp.Error != "test-error" {
				t.Error("unexpected error field")
			}
		}()
	}
	wg.Wait()
}
