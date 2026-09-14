package autoexpand

import (
	"context"
	"testing"

	"github.com/HelixDevelopment/skills/internal/config"
	"go.uber.org/zap"
)

// TestChaos_NilStoreNilLLM_PipelineConstruction verifies that a Pipeline
// constructed with nil store and nil LLM does not panic on construction.
func TestChaos_NilStoreNilLLM_PipelineConstruction(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NewPipeline(nil,nil,...) panicked: %v", r)
		}
	}()
	p := NewPipeline(nil, nil, config.AutoExpandConfig{}, zap.NewNop())
	if p == nil {
		t.Error("expected non-nil pipeline")
	}
}

// TestChaos_NilStore_Run_PanicsOrErrors verifies that Pipeline.Run with a nil
// store either returns an error or panics (both are acceptable — a nil store
// is an invalid state). The test asserts it does NOT silently succeed.
func TestChaos_NilStore_Run_PanicsOrErrors(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			// Panic on nil store is acceptable — the test passes.
			t.Logf("Run(nil store) panicked as expected: %v", r)
			return
		}
	}()
	p := NewPipeline(nil, nil, config.AutoExpandConfig{}, zap.NewNop())
	_, err := p.Run(context.Background(), "some-skill", 2)
	if err == nil {
		t.Error("expected error when running with nil store, got nil error and no panic")
	}
}

// TestChaos_EmptyGap_FieldsAreZero verifies that a zero-value Gap has
// empty string fields (no panic on access).
func TestChaos_EmptyGap_FieldsAreZero(t *testing.T) {
	gap := Gap{}
	if gap.SkillName != "" {
		t.Error("expected empty SkillName")
	}
	if gap.MissingDepName != "" {
		t.Error("expected empty MissingDepName")
	}
}
