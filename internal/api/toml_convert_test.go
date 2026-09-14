package api

// G07 (F4b): unit coverage for the pure TOML↔model shaping helpers
// convertTOMLWrapper / exportToTOMLWrapper. These live on the currently-UNWIRED
// hardened REST server (see the package note at the end of skills_handler.go);
// they had ZERO test coverage. They must faithfully carry every one of the six
// typed edges AND the [[skill.components]] optional/sort_order carrier (F3) so
// the eventual G09 wiring has a correct model to persist. No DB required.

import (
	"testing"

	"github.com/HelixDevelopment/skills/internal/models"
)

func intp(i int) *int { return &i }

// TestExportToTOMLWrapper_AllTypesAndComponentCarrier proves exportToTOMLWrapper
// emits every relation type in its list, keeps a plain composes edge in the
// composes list, and routes a composes edge carrying optional/sort_order into
// the [[skill.components]] carrier (F3).
func TestExportToTOMLWrapper_AllTypesAndComponentCarrier(t *testing.T) {
	skill := &models.Skill{
		Name:    "x",
		Version: "0.1.0",
		Title:   "x",
		Content: "x",
		Kind:    models.SkillKindComposite,
		Dependencies: []models.SkillDependency{
			{RelationType: models.DepTypeRequires, DependsOnName: "r"},
			{RelationType: models.DepTypeExtends, DependsOnName: "e"},
			{RelationType: models.DepTypeRecommends, DependsOnName: "m"},
			{RelationType: models.DepTypeComposes, DependsOnName: "cp"},                                     // plain → composes list
			{RelationType: models.DepTypeComposes, DependsOnName: "cc", Optional: true, SortOrder: intp(3)}, // → component
			{RelationType: models.DepTypeRelatedTo, DependsOnName: "rl"},
			{RelationType: models.DepTypeAlternative, DependsOnName: "al"},
		},
		Resources: []models.Resource{{URL: "u", Title: "t", ResourceType: "official-doc"}},
	}

	w := exportToTOMLWrapper(skill)
	d := w.Skill.Dependencies
	eq := func(label string, got []string, want ...string) {
		if len(got) != len(want) {
			t.Errorf("%s = %v, want %v", label, got, want)
			return
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s = %v, want %v", label, got, want)
				return
			}
		}
	}
	eq("requires", d.Requires, "r")
	eq("extends", d.Extends, "e")
	eq("recommends", d.Recommends, "m")
	eq("composes(plain only)", d.Composes, "cp") // NOT "cc" — that went to components
	eq("related_to", d.RelatedTo, "rl")
	eq("alternative_to", d.Alternative, "al")

	if len(w.Skill.Components) != 1 {
		t.Fatalf("components = %+v, want exactly the cc carrier", w.Skill.Components)
	}
	c := w.Skill.Components[0]
	if c.Name != "cc" || c.Order != 3 || !c.Optional {
		t.Errorf("component = %+v, want {Name:cc Order:3 Optional:true}", c)
	}
	if len(w.Skill.Resources) != 1 || w.Skill.Resources[0].URL != "u" {
		t.Errorf("resources = %+v, want one with URL=u", w.Skill.Resources)
	}
}

// TestConvertTOMLWrapper_AllTypesAndComponentCarrier proves convertTOMLWrapper
// reads every typed edge back (carrying DependsOnName) and materializes a
// [[skill.components]] entry as a composes edge with its optional/sort_order.
func TestConvertTOMLWrapper_AllTypesAndComponentCarrier(t *testing.T) {
	w := models.TOMLSkillWrapper{
		Skill: models.TOMLSkillDef{
			Name:    "x",
			Version: "0.1.0",
			Title:   "x",
			Content: "x",
			Kind:    "composite",
			Dependencies: models.TOMLDependencies{
				Requires:    []string{"r"},
				Extends:     []string{"e"},
				Recommends:  []string{"m"},
				Composes:    []string{"cp"},
				RelatedTo:   []string{"rl"},
				Alternative: []string{"al"},
			},
			Components: []models.TOMLComponent{{Name: "cc", Order: 3, Optional: true}},
			Resources:  []models.TOMLResource{{URL: "u", Title: "t", ResourceType: "official-doc"}},
		},
	}

	skill := convertTOMLWrapper(w)
	if skill.Kind != models.SkillKindComposite {
		t.Errorf("kind = %q, want composite", skill.Kind)
	}
	type edge struct {
		rel models.DependencyType
		opt bool
		so  *int
	}
	got := make(map[string]edge, len(skill.Dependencies))
	for _, d := range skill.Dependencies {
		got[d.DependsOnName] = edge{d.RelationType, d.Optional, d.SortOrder}
	}
	wantRel := map[string]models.DependencyType{
		"r": models.DepTypeRequires, "e": models.DepTypeExtends, "m": models.DepTypeRecommends,
		"cp": models.DepTypeComposes, "rl": models.DepTypeRelatedTo, "al": models.DepTypeAlternative,
		"cc": models.DepTypeComposes,
	}
	for name, rel := range wantRel {
		g, ok := got[name]
		if !ok {
			t.Errorf("convert lost edge %q", name)
			continue
		}
		if g.rel != rel {
			t.Errorf("edge %q rel=%q, want %q", name, g.rel, rel)
		}
	}
	// The component edge must carry optional/sort_order.
	cc := got["cc"]
	if !cc.opt || cc.so == nil || *cc.so != 3 {
		t.Errorf("cc component edge opt=%v so=%v, want true/3", cc.opt, cc.so)
	}
	// A plain composes edge must NOT carry a sort_order.
	if cp := got["cp"]; cp.so != nil {
		t.Errorf("plain composes cp got SortOrder=%v, want nil", cp.so)
	}
	if len(skill.Resources) != 1 || skill.Resources[0].URL != "u" {
		t.Errorf("resources = %+v, want one with URL=u", skill.Resources)
	}
}

// TestConvertExportRoundTrip_ComponentAttrsStable proves the pure
// convert↔export pair is attribute-faithful for the component carrier:
// export→convert→export yields the same component entry.
func TestConvertExportRoundTrip_ComponentAttrsStable(t *testing.T) {
	skill := &models.Skill{
		Name: "x", Version: "0.1.0", Title: "x", Content: "x", Kind: models.SkillKindUmbrella,
		Dependencies: []models.SkillDependency{
			{RelationType: models.DepTypeComposes, DependsOnName: "cc", Optional: true, SortOrder: intp(5)},
		},
	}
	w1 := exportToTOMLWrapper(skill)
	s2 := convertTOMLWrapper(w1)
	w2 := exportToTOMLWrapper(&s2)
	if len(w1.Skill.Components) != 1 || len(w2.Skill.Components) != 1 {
		t.Fatalf("component count drift: w1=%d w2=%d", len(w1.Skill.Components), len(w2.Skill.Components))
	}
	if w1.Skill.Components[0] != w2.Skill.Components[0] {
		t.Errorf("component drift: %+v vs %+v", w1.Skill.Components[0], w2.Skill.Components[0])
	}
}
