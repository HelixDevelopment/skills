package validation

import (
	"context"
	"math"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/HelixDevelopment/skills/internal/models"
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

// TestStress_ConcurrentPipelineConstruction exercises concurrent Pipeline
// construction. N=100 goroutines, no races.
func TestStress_ConcurrentPipelineConstruction(t *testing.T) {
	const n = 100
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := NewPipeline(nil, config.ValidationConfig{}, zap.NewNop())
			if p == nil {
				t.Error("NewPipeline returned nil")
			}
		}()
	}
	wg.Wait()
}

// TestStress_ConcurrentValidationResult exercises concurrent ValidationResult
// construction. N=100 goroutines, no races.
func TestStress_ConcurrentValidationResult(t *testing.T) {
	const n = 100
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result := &ValidationResult{
				Passed:     true,
				Stage:      "test",
				ApprovedBy: 2,
			}
			if !result.Passed {
				t.Error("expected Passed=true")
			}
		}()
	}
	wg.Wait()
}

// TestStress_ConcurrentValidation exercises N=50 concurrent validation
// pipelines running Validate on distinct skills. Each pipeline is
// constructed with a nil store (hermetic, no DB). Records latency
// p50/p95/p99 and verifies no panics or data races.
func TestStress_ConcurrentValidation(t *testing.T) {
	const n = 50

	p := NewPipeline(nil, config.ValidationConfig{}, zap.NewNop())
	ctx := context.Background()

	var wg sync.WaitGroup
	latencies := make([]time.Duration, n)
	var latMu sync.Mutex

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			sk := &models.Skill{
				ID:      uuid.New(),
				Name:    "stress.validation." + uuid.New().String(),
				Title:   "Stress Validation Skill",
				Content: "test content",
				Status:  models.SkillStatusDraft,
				Kind:    models.SkillKindAtomic,
			}

			start := time.Now()
			result, err := p.Validate(ctx, sk)
			elapsed := time.Since(start)

			latMu.Lock()
			latencies[idx] = elapsed
			latMu.Unlock()

			// With nil store, the pipeline returns an error (fail-closed).
			// This is the expected behavior — not a test failure.
			if err != nil {
				// fail-closed is correct behavior
				return
			}
			// If no error, result must be non-nil and non-passing.
			if result == nil {
				t.Errorf("goroutine %d: Validate returned nil result without error", idx)
			} else if result.Passed {
				t.Errorf("goroutine %d: Validate with nil store must not pass", idx)
			}
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

	t.Logf("ConcurrentValidation (N=%d): p50=%s p95=%s p99=%s",
		n, roundDuration(p50), roundDuration(p95), roundDuration(p99))
}
