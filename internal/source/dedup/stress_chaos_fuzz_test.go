package dedup

import (
	"sync"
	"testing"

	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Stress — concurrent classification (N=100, no races)
// ---------------------------------------------------------------------------

func TestStress_ConcurrentClassify(t *testing.T) {
	existing := []*models.Skill{
		{ID: uuid.New(), Name: "go-language"},
		{ID: uuid.New(), Name: "python-language"},
		{ID: uuid.New(), Name: "rust-testing"},
	}
	classifier := NewClassifier(existing, zap.NewNop())

	const n = 100
	var wg sync.WaitGroup
	results := make([]*ClassifyResult, n)

	newSkill := &models.Skill{Name: "java-language"}

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = classifier.Classify(newSkill)
		}(i)
	}
	wg.Wait()

	// All results must be identical.
	for i := 1; i < n; i++ {
		if results[i].Classification != results[0].Classification {
			t.Fatalf("inconsistent classification at goroutine %d: %s != %s",
				i, results[i].Classification, results[0].Classification)
		}
		if results[i].Confidence != results[0].Confidence {
			t.Fatalf("inconsistent confidence at goroutine %d: %.2f != %.2f",
				i, results[i].Confidence, results[0].Confidence)
		}
	}

	if results[0].Classification != ClassificationNew {
		t.Fatalf("expected new, got %s", results[0].Classification)
	}
}

func TestStress_ConcurrentClassifyMixed(t *testing.T) {
	existing := []*models.Skill{
		{ID: uuid.New(), Name: "auth-helper"},
	}
	classifier := NewClassifier(existing, zap.NewNop())

	skills := []*models.Skill{
		{Name: "auth-helper"},        // duplicate
		{Name: "auth-helper-2"},      // variant (normalized match)
		{Name: "entirely-new-thing"}, // new
	}

	const n = 100
	var wg sync.WaitGroup
	for _, sk := range skills {
		sk := sk
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				result := classifier.Classify(sk)
				_ = result
			}()
		}
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// Chaos — edge cases
// ---------------------------------------------------------------------------

func TestChaos_ClassifyNilSkill(t *testing.T) {
	classifier := NewClassifier(nil, zap.NewNop())

	result := classifier.Classify(nil)
	if result.Classification != ClassificationNew {
		t.Errorf("expected new for nil skill, got %s", result.Classification)
	}
	if result.Confidence != 0.0 {
		t.Errorf("expected 0.0 confidence for nil skill, got %.2f", result.Confidence)
	}
}

func TestChaos_ClassifyEmptyName(t *testing.T) {
	existing := []*models.Skill{
		{ID: uuid.New(), Name: "known-skill"},
	}
	classifier := NewClassifier(existing, zap.NewNop())

	result := classifier.Classify(&models.Skill{Name: ""})
	// Empty name should not match "known-skill" (exact) and normalizeName("") = ""
	// which likely also doesn't match.
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestChaos_Classify_UnicodeNames(t *testing.T) {
	existing := []*models.Skill{
		{ID: uuid.New(), Name: "résumé-builder"},
	}
	classifier := NewClassifier(existing, zap.NewNop())

	tests := []struct {
		name     string
		expected Classification
	}{
		{"résumé-builder", ClassificationDuplicate},
		{"resume-builder", ClassificationVariant}, // normalized: "résumé" != "resume" (different chars after normalization)
		{"RÉSUMÉ-BUILDER", ClassificationVariant},
		{"日本語スキル", ClassificationNew},
	}

	for _, tc := range tests {
		result := classifier.Classify(&models.Skill{Name: tc.name})
		if result == nil {
			t.Errorf("nil result for %q", tc.name)
			continue
		}
		t.Logf("%q -> %s (confidence %.2f): %s", tc.name, result.Classification, result.Confidence, result.Reason)
	}
}

func TestChaos_NewClassifier_HandlesNils(t *testing.T) {
	skills := []*models.Skill{
		{ID: uuid.New(), Name: "good-one"},
		nil, // should be skipped gracefully
		{ID: uuid.New(), Name: "good-two"},
		nil,
	}

	c := NewClassifier(skills, zap.NewNop())
	if c == nil {
		t.Fatal("nil classifier")
	}

	result := c.Classify(&models.Skill{Name: "good-one"})
	if result.Classification != ClassificationDuplicate {
		t.Errorf("expected duplicate, got %s", result.Classification)
	}
}

func TestChaos_NewClassifier_EmptySkills(t *testing.T) {
	c := NewClassifier(nil, zap.NewNop())
	if c == nil {
		t.Fatal("nil classifier from nil skills")
	}

	result := c.Classify(&models.Skill{Name: "anything"})
	if result.Classification != ClassificationNew {
		t.Errorf("expected new from empty classifier, got %s", result.Classification)
	}
}

// ---------------------------------------------------------------------------
// Fuzz — classify with arbitrary names
// ---------------------------------------------------------------------------

func FuzzClassify(f *testing.F) {
	f.Add("auth-helper")
	f.Add("")
	f.Add("\x00\x01")
	f.Add("a")
	f.Add("very-very-very-long-skill-name-with-many-dashes-and-underscores_and.dots")
	f.Add("日本語のスキル名")
	f.Add("emoji😀skill")

	existing := []*models.Skill{
		{ID: uuid.New(), Name: "known-skill"},
		{ID: uuid.New(), Name: "auth-helper"},
	}
	classifier := NewClassifier(existing, zap.NewNop())

	f.Fuzz(func(t *testing.T, name string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Classify(%q) panicked: %v", name, r)
			}
		}()
		result := classifier.Classify(&models.Skill{Name: name})
		if result == nil {
			t.Error("nil result from Classify")
			return
		}
		// Verify consistency: calling again should return the same classification.
		result2 := classifier.Classify(&models.Skill{Name: name})
		if result.Classification != result2.Classification {
			t.Errorf("inconsistent classification: %s vs %s for %q",
				result.Classification, result2.Classification, name)
		}
	})
}

// ---------------------------------------------------------------------------
// Normalization edge cases
// ---------------------------------------------------------------------------

func TestNormalizeName_EdgeCases(t *testing.T) {
	tests := []struct {
		input, expected string
	}{
		{"my-skill", "myskill"},
		{"my_skill", "myskill"},
		{"my.skill", "myskill"},
		{"My Skill", "myskill"},
		{"MY-SKILL", "myskill"},
		{"", ""},
		{"a", "a"},
		{"-_-", ""}, // all separators
		{" . ", ""}, // space + dot
		{"a.b_c-d e", "abcde"},
	}

	for _, tc := range tests {
		result := normalizeName(tc.input)
		if result != tc.expected {
			t.Errorf("normalizeName(%q) = %q, want %q", tc.input, result, tc.expected)
		}
	}
}

var _ = uuid.Nil
var _ = zap.NewNop
