package pipeline

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/HelixDevelopment/skills/internal/models"
)

func validCandidate() *CandidateSkill {
	return &CandidateSkill{
		Name:        "ingest.fs.sample",
		Title:       "Sample",
		Description: "a description",
		Content:     "# Sample\n\nbody\n",
		Tags:        []string{"nested"},
		Domain:      "nested",
		Complexity:  "beginner",
		SourceID:    "fs:/root",
		SourcePath:  "sample.md",
		FetchedHash: "abc123",
	}
}

func TestMapToSkill_HappyPath(t *testing.T) {
	c := validCandidate()
	skill, err := MapToSkill(c)
	if err != nil {
		t.Fatalf("MapToSkill: %v", err)
	}

	if skill.Name != c.Name {
		t.Errorf("Name = %q, want %q", skill.Name, c.Name)
	}
	if skill.Title != c.Title {
		t.Errorf("Title = %q, want %q", skill.Title, c.Title)
	}
	if skill.Description != c.Description {
		t.Errorf("Description = %q, want %q", skill.Description, c.Description)
	}
	if skill.Content != c.Content {
		t.Errorf("Content = %q, want %q", skill.Content, c.Content)
	}
	if skill.Status != models.SkillStatusDraft {
		t.Errorf("Status = %q, want %q (a mapped skill is ALWAYS draft, never self-promoted)", skill.Status, models.SkillStatusDraft)
	}
	if skill.Kind != models.SkillKindAtomic {
		t.Errorf("Kind = %q, want %q", skill.Kind, models.SkillKindAtomic)
	}
	var zero [16]byte
	if skill.ID.String() == "" || [16]byte(skill.ID) == zero {
		t.Errorf("ID must be a real, non-zero generated UUID")
	}
	if skill.CreatedAt.IsZero() || skill.UpdatedAt.IsZero() {
		t.Errorf("CreatedAt/UpdatedAt must be set, got %v/%v", skill.CreatedAt, skill.UpdatedAt)
	}

	var meta models.SkillMetadata
	if err := json.Unmarshal(skill.Metadata, &meta); err != nil {
		t.Fatalf("unmarshal Metadata: %v", err)
	}
	if meta.Domain != c.Domain || meta.Complexity != c.Complexity {
		t.Errorf("Metadata = %+v, want Domain=%q Complexity=%q", meta, c.Domain, c.Complexity)
	}
	if len(meta.Tags) != 1 || meta.Tags[0] != "nested" {
		t.Errorf("Metadata.Tags = %v, want [\"nested\"]", meta.Tags)
	}

	if len(skill.Resources) != 1 {
		t.Fatalf("Resources = %v, want exactly 1", skill.Resources)
	}
	res := skill.Resources[0]
	if want := ResourceLocator(c.SourceID, c.SourcePath); res.URL != want {
		t.Errorf("Resource.URL = %q, want %q", res.URL, want)
	}
	if res.FetchedHash != c.FetchedHash {
		t.Errorf("Resource.FetchedHash = %q, want %q", res.FetchedHash, c.FetchedHash)
	}
	if res.ResourceType != "article" {
		t.Errorf("Resource.ResourceType = %q, want %q (must be a value already valid under the CURRENT resources.resource_type CHECK constraint, since T0.2's widening migration has not landed)", res.ResourceType, "article")
	}
}

func TestMapToSkill_NilCandidate_Rejected(t *testing.T) {
	_, err := MapToSkill(nil)
	if err == nil {
		t.Fatal("MapToSkill(nil) = nil error, want rejection")
	}
	if !errors.Is(err, ErrNilCandidate) {
		t.Errorf("error = %v, want it to wrap ErrNilCandidate", err)
	}
}

func TestMapToSkill_EmptyName_Rejected(t *testing.T) {
	c := validCandidate()
	c.Name = ""
	_, err := MapToSkill(c)
	if err == nil {
		t.Fatal("MapToSkill with empty Name = nil error, want rejection")
	}
	if !errors.Is(err, ErrEmptyCandidateName) {
		t.Errorf("error = %v, want it to wrap ErrEmptyCandidateName", err)
	}
}

func TestMapToSkill_TwoCallsProduceDifferentIDs(t *testing.T) {
	c := validCandidate()
	s1, err := MapToSkill(c)
	if err != nil {
		t.Fatalf("MapToSkill (1): %v", err)
	}
	s2, err := MapToSkill(c)
	if err != nil {
		t.Fatalf("MapToSkill (2): %v", err)
	}
	if s1.ID == s2.ID {
		t.Errorf("two independent MapToSkill calls produced the SAME ID %v, want distinct generated UUIDs", s1.ID)
	}
}

func TestResourceLocator(t *testing.T) {
	got := ResourceLocator("fs:/root", "nested/child_note.md")
	want := "fs:/root#nested/child_note.md"
	if got != want {
		t.Errorf("ResourceLocator = %q, want %q", got, want)
	}
}
