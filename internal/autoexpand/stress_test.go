package autoexpand

import (
	"sync"
	"testing"

	"github.com/HelixDevelopment/skills/internal/config"
	"go.uber.org/zap"
)

// TestStress_ConcurrentPipelineConstruction exercises concurrent Pipeline
// construction with nil store and nil LLM. N=100 goroutines, no races.
func TestStress_ConcurrentPipelineConstruction(t *testing.T) {
	const n = 100
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := NewPipeline(nil, nil, config.AutoExpandConfig{}, zap.NewNop())
			if p == nil {
				t.Error("NewPipeline returned nil")
			}
		}()
	}
	wg.Wait()
}

// TestStress_ConcurrentGapConstruction exercises concurrent Gap struct
// construction. N=100 goroutines, no races.
func TestStress_ConcurrentGapConstruction(t *testing.T) {
	const n = 100
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			gap := Gap{
				SkillName:      "parent",
				MissingDepName: "child",
				SuggestedTitle: "Child Skill",
				Reason:         "test gap",
			}
			if gap.SkillName != "parent" {
				t.Error("unexpected SkillName")
			}
		}()
	}
	wg.Wait()
}
