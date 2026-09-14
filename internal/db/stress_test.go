package db

import (
	"sync"
	"testing"

	"github.com/HelixDevelopment/skills/internal/config"
)

// TestStress_ConcurrentNewEmbedderFromConfig exercises concurrent
// NewEmbedderFromConfig calls with invalid config (no API key). N=100
// goroutines, no races expected — all should return an error.
func TestStress_ConcurrentNewEmbedderFromConfig(t *testing.T) {
	const n = 100
	var wg sync.WaitGroup
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := NewEmbedderFromConfig(config.EmbeddingConfig{
				Provider:   "openai",
				Dimensions: 1536,
				// No API key — should error.
			})
			if err == nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err == nil {
			t.Error("expected error for missing API key, got nil")
		}
	}
}

// TestStress_ConcurrentEmbeddingConfig exercises concurrent construction of
// EmbeddingConfig values. N=100 goroutines, no races.
func TestStress_ConcurrentEmbeddingConfig(t *testing.T) {
	const n = 100
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cfg := config.EmbeddingConfig{
				Provider:   "openai",
				Dimensions: 1536,
			}
			if cfg.Provider != "openai" {
				t.Error("unexpected provider")
			}
		}()
	}
	wg.Wait()
}
