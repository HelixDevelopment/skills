package dedup

import (
	"testing"

	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// helper to build a minimal models.Skill with the given name.
func skillWithName(name string) *models.Skill {
	return &models.Skill{
		ID:   uuid.New(),
		Name: name,
	}
}

func TestClassify_ExactMatchReturnsDuplicate(t *testing.T) {
	existing := skillWithName("docker.compose")
	c := NewClassifier([]*models.Skill{existing}, zap.NewNop())

	incoming := skillWithName("docker.compose")
	result := c.Classify(incoming)

	if result.Classification != ClassificationDuplicate {
		t.Fatalf("expected duplicate, got %s", result.Classification)
	}
	if result.ExistingID == nil || *result.ExistingID != existing.ID {
		t.Fatal("expected ExistingID to match existing skill ID")
	}
	if result.Confidence != 1.0 {
		t.Fatalf("expected confidence 1.0, got %f", result.Confidence)
	}
}

func TestClassify_NormalizedMatchReturnsVariant(t *testing.T) {
	existing := skillWithName("docker-compose")
	c := NewClassifier([]*models.Skill{existing}, zap.NewNop())

	// "docker_compose" normalizes to the same key as "docker-compose".
	incoming := skillWithName("docker_compose")
	result := c.Classify(incoming)

	if result.Classification != ClassificationVariant {
		t.Fatalf("expected variant, got %s", result.Classification)
	}
	if result.ExistingID == nil || *result.ExistingID != existing.ID {
		t.Fatal("expected ExistingID to match existing skill ID")
	}
	if result.Confidence != 0.8 {
		t.Fatalf("expected confidence 0.8, got %f", result.Confidence)
	}
}

func TestClassify_CaseInsensitiveVariant(t *testing.T) {
	existing := skillWithName("MySkill")
	c := NewClassifier([]*models.Skill{existing}, zap.NewNop())

	incoming := skillWithName("my_skill")
	result := c.Classify(incoming)

	if result.Classification != ClassificationVariant {
		t.Fatalf("expected variant, got %s", result.Classification)
	}
}

func TestClassify_NoMatchReturnsNew(t *testing.T) {
	existing := skillWithName("docker.compose")
	c := NewClassifier([]*models.Skill{existing}, zap.NewNop())

	incoming := skillWithName("kubernetes.helm")
	result := c.Classify(incoming)

	if result.Classification != ClassificationNew {
		t.Fatalf("expected new, got %s", result.Classification)
	}
	if result.ExistingID != nil {
		t.Fatal("expected nil ExistingID for new skill")
	}
	if result.Confidence != 1.0 {
		t.Fatalf("expected confidence 1.0, got %f", result.Confidence)
	}
}

func TestClassify_NilSkillReturnsNew(t *testing.T) {
	c := NewClassifier(nil, zap.NewNop())

	result := c.Classify(nil)

	if result.Classification != ClassificationNew {
		t.Fatalf("expected new, got %s", result.Classification)
	}
}

func TestClassify_EmptyExistingSetReturnsNew(t *testing.T) {
	c := NewClassifier([]*models.Skill{}, zap.NewNop())

	incoming := skillWithName("anything")
	result := c.Classify(incoming)

	if result.Classification != ClassificationNew {
		t.Fatalf("expected new, got %s", result.Classification)
	}
}

func TestClassify_NilExistingSkillsSkipped(t *testing.T) {
	// A nil entry in the existing skills slice should be silently ignored.
	c := NewClassifier([]*models.Skill{nil, skillWithName("a.skill")}, zap.NewNop())

	incoming := skillWithName("a.skill")
	result := c.Classify(incoming)

	if result.Classification != ClassificationDuplicate {
		t.Fatalf("expected duplicate, got %s", result.Classification)
	}
}

func TestClassify_DotVariantsNormalizeTogether(t *testing.T) {
	existing := skillWithName("a.b.c")
	c := NewClassifier([]*models.Skill{existing}, zap.NewNop())

	// All of these normalize to "abc".
	for _, variant := range []string{"a-b-c", "a_b_c", "a b c", "A-B-C"} {
		result := c.Classify(skillWithName(variant))
		if result.Classification != ClassificationVariant {
			t.Errorf("name %q: expected variant, got %s", variant, result.Classification)
		}
	}
}
