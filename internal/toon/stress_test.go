package toon

import (
	"sync"
	"testing"
)

// TestStress_ConcurrentMarshal exercises concurrent Marshal calls on the same
// input. N=100 goroutines, no races expected.
func TestStress_ConcurrentMarshal(t *testing.T) {
	input := map[string]interface{}{
		"id":     1,
		"name":   "test",
		"active": true,
		"tags":   []interface{}{"a", "b", "c"},
	}

	const n = 100
	var wg sync.WaitGroup
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := Marshal(input)
			if err != nil {
				errs <- err
				return
			}
			if len(out) == 0 {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("concurrent Marshal failed: %v", err)
		}
	}
}

// TestStress_ConcurrentRoundTrip exercises concurrent Marshal+Unmarshal
// round-trips. N=100 goroutines, no races expected.
func TestStress_ConcurrentRoundTrip(t *testing.T) {
	input := map[string]interface{}{
		"skills": []interface{}{
			map[string]interface{}{"name": "skill-a", "version": "1.0"},
			map[string]interface{}{"name": "skill-b", "version": "2.0"},
		},
	}

	const n = 100
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			encoded, err := Marshal(input)
			if err != nil {
				t.Errorf("Marshal: %v", err)
				return
			}
			var decoded interface{}
			if err := Unmarshal(encoded, &decoded); err != nil {
				t.Errorf("Unmarshal: %v", err)
			}
		}()
	}
	wg.Wait()
}
