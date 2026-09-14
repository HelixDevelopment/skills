package models

import "testing"

func TestIsHardClosure(t *testing.T) {
	hard := []DependencyType{DepTypeRequires, DepTypeComposes, DepTypeExtends}
	advisory := []DependencyType{DepTypeRecommends, DepTypeRelatedTo, DepTypeAlternative}

	for _, dt := range hard {
		if !IsHardClosure(dt) {
			t.Errorf("IsHardClosure(%q) = false, want true", dt)
		}
	}
	for _, dt := range advisory {
		if IsHardClosure(dt) {
			t.Errorf("IsHardClosure(%q) = true, want false", dt)
		}
	}
}

func TestNormalizeOrAtomic(t *testing.T) {
	tests := []struct {
		in   SkillKind
		want SkillKind
	}{
		{"", SkillKindAtomic},
		{SkillKindAtomic, SkillKindAtomic},
		{SkillKindComposite, SkillKindComposite},
		{SkillKindUmbrella, SkillKindUmbrella},
		{"custom", "custom"},
	}
	for _, tt := range tests {
		got := tt.in.NormalizeOrAtomic()
		if got != tt.want {
			t.Errorf("SkillKind(%q).NormalizeOrAtomic() = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestHardClosureTypes_Length(t *testing.T) {
	if len(HardClosureTypes) != 3 {
		t.Errorf("HardClosureTypes length = %d, want 3", len(HardClosureTypes))
	}
}
