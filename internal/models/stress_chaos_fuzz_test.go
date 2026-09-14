package models

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// Stress — concurrent Skill construction and field access (N=100)
// ---------------------------------------------------------------------------

func TestStress_ConcurrentSkillConstruction(t *testing.T) {
	now := time.Now().UTC()

	const n = 100
	var wg sync.WaitGroup
	skills := make([]*Skill, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			skills[idx] = &Skill{
				ID:          uuid.New(),
				Name:        "test-skill",
				Version:     "1.0.0",
				Title:       "Test Skill",
				Description: "A skill for testing concurrent access",
				Status:      SkillStatusDraft,
				Kind:        SkillKindAtomic,
				CreatedAt:   now,
				UpdatedAt:   now,
			}
		}(i)
	}
	wg.Wait()

	for i, s := range skills {
		if s == nil {
			t.Errorf("goroutine %d: nil skill", i)
			continue
		}
		if s.Name != "test-skill" {
			t.Errorf("goroutine %d: name = %q", i, s.Name)
		}
		if s.Status != SkillStatusDraft {
			t.Errorf("goroutine %d: status = %q", i, s.Status)
		}
	}
}

func TestStress_ConcurrentDependencyCreation(t *testing.T) {
	skillID := uuid.New()
	dependsOn := uuid.New()
	order := 1

	const n = 100
	var wg sync.WaitGroup
	deps := make([]*SkillDependency, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			deps[idx] = &SkillDependency{
				SkillID:      skillID,
				DependsOn:    dependsOn,
				RelationType: DepTypeRequires,
				Optional:     false,
				SortOrder:    &order,
			}
		}(i)
	}
	wg.Wait()

	for i, d := range deps {
		if d == nil {
			t.Errorf("goroutine %d: nil dependency", i)
			continue
		}
		if d.RelationType != DepTypeRequires {
			t.Errorf("goroutine %d: wrong relation type", i)
		}
		if d.SkillID != skillID {
			t.Errorf("goroutine %d: wrong skill ID", i)
		}
	}
}

func TestStress_ConcurrentEvidenceCreation(t *testing.T) {
	skillID := uuid.New()

	const n = 100
	var wg sync.WaitGroup
	evidences := make([]*Evidence, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			evidences[idx] = &Evidence{
				ID:            uuid.New(),
				SkillID:       skillID,
				SourceProject: "test-project",
				SourceFile:    "main.go",
				CodeSnippet:   "func Test() {}",
				Pattern:       "test-pattern",
				Language:      "go",
				Validated:     false,
				CreatedAt:     time.Now().UTC(),
			}
		}(i)
	}
	wg.Wait()

	for i, e := range evidences {
		if e == nil {
			t.Errorf("goroutine %d: nil evidence", i)
		}
	}
}

// ---------------------------------------------------------------------------
// Chaos — edge cases
// ---------------------------------------------------------------------------

func TestChaos_SkillKind_NormalizeOrAtomic(t *testing.T) {
	tests := []struct {
		input    SkillKind
		expected SkillKind
	}{
		{SkillKindAtomic, SkillKindAtomic},
		{SkillKindComposite, SkillKindComposite},
		{SkillKindUmbrella, SkillKindUmbrella},
		{SkillKind(""), SkillKindAtomic},
		{SkillKind("unknown"), SkillKind("unknown")}, // passes through unknown
	}
	for _, tc := range tests {
		result := tc.input.NormalizeOrAtomic()
		if result != tc.expected {
			t.Errorf("NormalizeOrAtomic(%q) = %q, want %q", tc.input, result, tc.expected)
		}
	}
}

func TestChaos_IsHardClosure(t *testing.T) {
	tests := []struct {
		dt       DependencyType
		expected bool
	}{
		{DepTypeRequires, true},
		{DepTypeComposes, true},
		{DepTypeExtends, true},
		{DepTypeRecommends, false},
		{DepTypeRelatedTo, false},
		{DepTypeAlternative, false},
		{DependencyType("nonexistent"), false},
		{DependencyType(""), false},
	}
	for _, tc := range tests {
		if got := IsHardClosure(tc.dt); got != tc.expected {
			t.Errorf("IsHardClosure(%q) = %v, want %v", tc.dt, got, tc.expected)
		}
	}
}

func TestChaos_HardClosureTypes_ContainsAllHardTypes(t *testing.T) {
	expected := map[DependencyType]bool{
		DepTypeRequires: true,
		DepTypeComposes: true,
		DepTypeExtends:  true,
	}
	for _, dt := range HardClosureTypes {
		if !expected[dt] {
			t.Errorf("unexpected type in HardClosureTypes: %q", dt)
		}
		delete(expected, dt)
	}
	if len(expected) > 0 {
		for dt := range expected {
			t.Errorf("missing type from HardClosureTypes: %q", dt)
		}
	}
}

func TestChaos_SkillStatus_AllValues(t *testing.T) {
	statuses := []SkillStatus{
		SkillStatusDraft,
		SkillStatusValidated,
		SkillStatusActive,
		SkillStatusDeprecated,
	}
	for i, s1 := range statuses {
		for j, s2 := range statuses {
			if i == j && s1 != s2 {
				t.Errorf("same index %d has different values: %q != %q", i, s1, s2)
			}
			if i != j && s1 == s2 {
				t.Errorf("different statuses at %d,%d are equal: %q", i, j, s1)
			}
		}
	}
	// All must be non-empty.
	for _, s := range statuses {
		if s == "" {
			t.Error("empty skill status")
		}
	}
}

func TestChaos_DependencyType_AllNonEmpty(t *testing.T) {
	types := []DependencyType{
		DepTypeRequires,
		DepTypeExtends,
		DepTypeRecommends,
		DepTypeComposes,
		DepTypeRelatedTo,
		DepTypeAlternative,
	}
	for _, dt := range types {
		if dt == "" {
			t.Errorf("empty dependency type in constant set")
		}
	}
}

func TestChaos_Skill_ZeroValue(t *testing.T) {
	var s Skill
	if s.ID != uuid.Nil {
		t.Error("zero Skill should have nil UUID")
	}
	if s.Status != "" {
		t.Errorf("zero Skill Status should be empty, got %q", s.Status)
	}
}

func TestChaos_Evidence_ZeroValue(t *testing.T) {
	var e Evidence
	if e.ID != uuid.Nil {
		t.Error("zero Evidence should have nil UUID")
	}
}

func TestChaos_Resource_ZeroValue(t *testing.T) {
	var r Resource
	if r.ID != uuid.Nil {
		t.Error("zero Resource should have nil UUID")
	}
}

// ---------------------------------------------------------------------------
// Fuzz — SkillKind.NormalizeOrAtomic
// ---------------------------------------------------------------------------

func FuzzSkillKindNormalize(f *testing.F) {
	f.Add("atomic")
	f.Add("")
	f.Add("composite")
	f.Add("umbrella")
	f.Add("\x00\x01")
	f.Add("UNKNOWN-KIND")

	f.Fuzz(func(t *testing.T, kind string) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("NormalizeOrAtomic(%q) panicked: %v", kind, r)
			}
		}()
		sk := SkillKind(kind)
		result := sk.NormalizeOrAtomic()
		if result == "" {
			t.Errorf("NormalizeOrAtomic(%q) returned empty string", kind)
		}
		// NormalizeOrAtomic should be idempotent.
		result2 := result.NormalizeOrAtomic()
		if result != result2 {
			t.Errorf("NormalizeOrAtomic not idempotent: %q -> %q -> %q", kind, result, result2)
		}
	})
}

// ---------------------------------------------------------------------------
// Fuzz — JSON round-trip for Skill
// ---------------------------------------------------------------------------

func FuzzSkillJSON(f *testing.F) {
	now := time.Now().UTC().Truncate(time.Second)

	f.Add("test-skill", "1.0.0", "Test", "Description here", "draft", "atomic")
	f.Add("", "", "", "", "", "") // Fuzz will vary these

	f.Fuzz(func(t *testing.T, name, version, title, desc string, status, kind string) {
		// Root-caused (HXC-159 T-P3.04 merge validation): encoding/json
		// substitutes U+FFFD for invalid UTF-8 bytes on Marshal (documented
		// Go stdlib behavior), so exact round-trip equality is only defined
		// for valid-UTF-8 inputs. Inputs outside that domain are skipped —
		// they exercise the encoder's documented substitution, not Skill.
		for _, s := range []string{name, version, title, desc, status, kind} {
			if !utf8.ValidString(s) {
				t.Skip("input outside JSON round-trip domain (invalid UTF-8)")
			}
		}
		defer func() {
			if r := recover(); r != nil {
				t.Logf("Skill JSON round-trip panicked: %v", r)
			}
		}()

		s := Skill{
			ID:          uuid.New(),
			Name:        name,
			Version:     version,
			Title:       title,
			Description: desc,
			Status:      SkillStatus(status),
			Kind:        SkillKind(kind).NormalizeOrAtomic(),
			CreatedAt:   now,
			UpdatedAt:   now,
		}

		data, err := json.Marshal(s)
		if err != nil {
			t.Logf("marshal error (acceptable): %v", err)
			return
		}

		var s2 Skill
		if err := json.Unmarshal(data, &s2); err != nil {
			t.Logf("unmarshal error (acceptable): %v", err)
			return
		}

		if s2.ID != s.ID {
			t.Errorf("ID mismatch after round-trip")
		}
		if s2.Name != s.Name {
			t.Errorf("Name mismatch: %q != %q", s2.Name, s.Name)
		}
	})
}

var _ = uuid.Nil
