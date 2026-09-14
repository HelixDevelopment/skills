package validation

import (
	"context"
	"testing"

	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// TestChaos_NilStore_PipelineConstruction verifies that a Pipeline constructed
// with nil store does not panic.
func TestChaos_NilStore_PipelineConstruction(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NewPipeline(nil,...) panicked: %v", r)
		}
	}()
	p := NewPipeline(nil, config.ValidationConfig{}, zap.NewNop())
	if p == nil {
		t.Error("expected non-nil pipeline")
	}
}

// TestChaos_ZeroValidationResult verifies that a zero-value ValidationResult
// has sensible defaults.
func TestChaos_ZeroValidationResult(t *testing.T) {
	result := &ValidationResult{}
	if result.Passed {
		t.Error("zero-value Passed should be false")
	}
	if result.Stage != "" {
		t.Error("zero-value Stage should be empty")
	}
}

// TestChaos_NilStoreInput verifies that the validation pipeline fails closed
// when constructed with a nil store and asked to validate a skill. The
// pipeline must NOT panic — it should return an error or a non-passing result.
func TestChaos_NilStoreInput(t *testing.T) {
	p := NewPipeline(nil, config.ValidationConfig{}, zap.NewNop())
	ctx := context.Background()

	sk := &models.Skill{
		ID:      uuid.New(),
		Name:    "chaos.nil.store.skill",
		Title:   "Chaos Nil Store Skill",
		Content: "test content",
		Status:  models.SkillStatusDraft,
		Kind:    models.SkillKindAtomic,
	}

	// Validate must not panic with a nil store.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Validate with nil store panicked: %v", r)
		}
	}()

	result, err := p.Validate(ctx, sk)

	// Recovery verification: the pipeline must either error or return a
	// non-passing result (fail-closed).
	if err != nil {
		// Error is acceptable — fail-closed.
		t.Logf("Validate with nil store returned error (fail-closed): %v", err)
		return
	}
	if result != nil && result.Passed {
		t.Error("Validate with nil store must NOT pass — fail-closed expected")
	}
}
