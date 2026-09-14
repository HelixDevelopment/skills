package skill

// Tests for pure (non-DB) helper functions in the skill package.
// These functions have no database dependency and can be unit-tested directly.

import (
	"testing"

	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/google/uuid"
)

func TestHardClosureRelationTypeStrings(t *testing.T) {
	got := hardClosureRelationTypeStrings()
	if len(got) != len(models.HardClosureTypes) {
		t.Fatalf("len = %d, want %d", len(got), len(models.HardClosureTypes))
	}
	for i, s := range got {
		want := string(models.HardClosureTypes[i])
		if s != want {
			t.Errorf("index %d = %q, want %q", i, s, want)
		}
	}
}

func TestCollectIDs(t *testing.T) {
	id1 := uuid.New()
	id2 := uuid.New()
	nodes := []flatNode{
		{skill: models.Skill{ID: id1}},
		{skill: models.Skill{ID: id2}},
	}
	ids := collectIDs(nodes)
	if len(ids) != 2 {
		t.Fatalf("len = %d, want 2", len(ids))
	}
	if ids[0] != id1 || ids[1] != id2 {
		t.Errorf("ids = %v, want [%s, %s]", ids, id1, id2)
	}
}

func TestCollectIDs_Empty(t *testing.T) {
	ids := collectIDs(nil)
	if ids == nil {
		t.Fatal("collectIDs(nil) returned nil, want non-nil empty slice")
	}
	if len(ids) != 0 {
		t.Errorf("len = %d, want 0", len(ids))
	}
}

func TestFuseSearchResults_Dedup(t *testing.T) {
	skill1 := models.Skill{ID: uuid.New(), Name: "alpha"}
	skill2 := models.Skill{ID: uuid.New(), Name: "beta"}

	vector := []models.SearchResult{
		{Skill: skill1, Score: 0.9},
		{Skill: skill2, Score: 0.7},
	}
	trigram := []models.SearchResult{
		{Skill: skill1, Score: 0.8}, // duplicate
	}

	got := fuseSearchResults(vector, trigram, 10)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (dedup)", len(got))
	}
	// Both results should have scores (fused)
	for _, r := range got {
		if r.Score == 0 {
			t.Errorf("skill %q has zero score after fusion", r.Skill.Name)
		}
	}
}

func TestFuseSearchResults_Limit(t *testing.T) {
	skills := make([]models.SearchResult, 5)
	for i := range skills {
		skills[i] = models.SearchResult{Skill: models.Skill{ID: uuid.New(), Name: string(rune('a' + i))}}
	}

	got := fuseSearchResults(skills, nil, 3)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (limit)", len(got))
	}
}

func TestFuseSearchResults_EmptyInputs(t *testing.T) {
	got := fuseSearchResults(nil, nil, 10)
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestFuseSearchResults_SortedByScore(t *testing.T) {
	s1 := models.Skill{ID: uuid.New(), Name: "low"}
	s2 := models.Skill{ID: uuid.New(), Name: "high"}

	vector := []models.SearchResult{
		{Skill: s1, Score: 0.1},
		{Skill: s2, Score: 0.9},
	}

	got := fuseSearchResults(vector, nil, 10)
	if len(got) < 2 {
		t.Fatalf("len = %d, want >= 2", len(got))
	}
	if got[0].Score < got[1].Score {
		t.Errorf("not sorted by score descending: %f < %f", got[0].Score, got[1].Score)
	}
}

func TestBuildSkillEmbedText(t *testing.T) {
	sk := &models.Skill{
		Name:        "test-skill",
		Title:       "Test Skill",
		Description: "A test skill for unit testing",
		Content:     "# Test\nSome content here",
	}

	text := buildSkillEmbedText(sk)
	if text == "" {
		t.Fatal("buildSkillEmbedText returned empty string")
	}
	if len(text) > 8000 {
		t.Errorf("text length = %d, want <= 8000", len(text))
	}
}

func TestBuildSkillEmbedText_Empty(t *testing.T) {
	sk := &models.Skill{}
	text := buildSkillEmbedText(sk)
	// Should not panic, may return empty or minimal text
	_ = text
}
