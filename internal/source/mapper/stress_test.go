package mapper

import (
	"sync"
	"testing"

	"github.com/HelixDevelopment/skills/internal/source/skillmd"
)

// TestStress_ConcurrentMap exercises concurrent Map calls. N=100 goroutines,
// no races expected.
func TestStress_ConcurrentMap(t *testing.T) {
	parsed := &skillmd.ParsedSkill{
		Name:        "test-skill",
		Description: "A test skill for stress testing",
	}

	const n = 100
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := Map(parsed, "test-skill", nil, "github.com/test/repo")
			if err != nil {
				t.Errorf("Map: %v", err)
				return
			}
			if result == nil {
				t.Error("Map returned nil result")
			}
		}()
	}
	wg.Wait()
}
