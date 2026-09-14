package pipeline

import (
	"sync"
	"testing"

	"github.com/HelixDevelopment/skills/internal/ingest/source"
)

// TestStress_ConcurrentSlugFromPath exercises concurrent SlugFromPath calls.
// N=100 goroutines, no races expected.
func TestStress_ConcurrentSlugFromPath(t *testing.T) {
	const n = 100
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slug := SlugFromPath("path/to/my-skill.md")
			if slug == "" {
				t.Error("SlugFromPath returned empty")
			}
		}()
	}
	wg.Wait()
}

// TestStress_ConcurrentNormalizeContent exercises concurrent NormalizeContent
// calls. N=100 goroutines, no races expected.
func TestStress_ConcurrentNormalizeContent(t *testing.T) {
	const n = 100
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := NormalizeContent("# Title\n\nSome content.", source.ItemRef{})
			if result == "" {
				t.Error("NormalizeContent returned empty")
			}
		}()
	}
	wg.Wait()
}

// TestStress_ConcurrentDescriptionFrom exercises concurrent descriptionFrom
// calls. N=100 goroutines, no races expected.
func TestStress_ConcurrentDescriptionFrom(t *testing.T) {
	const n = 100
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			desc := descriptionFrom("This is a skill description that should be extracted.")
			if desc == "" {
				t.Error("descriptionFrom returned empty")
			}
		}()
	}
	wg.Wait()
}
