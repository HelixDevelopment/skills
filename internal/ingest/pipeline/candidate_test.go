package pipeline

import (
	"errors"
	"strings"
	"testing"

	"github.com/HelixDevelopment/skills/internal/ingest/source"
)

func TestSlugFromPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"sample.md", "sample"},
		{"plain_notes.txt", "plain_notes"},
		{"nested/child_note.md", "nested.child_note"},
		{"A B/C.D.txt", "a_b.c_d"},
		{"!!!.md", ""},
	}
	for _, tc := range cases {
		if got := SlugFromPath(tc.path); got != tc.want {
			t.Errorf("SlugFromPath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestBuildCandidate_HappyPath(t *testing.T) {
	ref := source.ItemRef{SourceID: "fs:/root", Path: "nested/child_note.md"}
	content := "# Child Note\n\nfirst real line of body text\n\nmore"

	c, err := BuildCandidate(ref, "abc123", content)
	if err != nil {
		t.Fatalf("BuildCandidate: %v", err)
	}
	if want := "ingest.fs.nested.child_note"; c.Name != want {
		t.Errorf("Name = %q, want %q", c.Name, want)
	}
	if want := "Child Note"; c.Title != want {
		t.Errorf("Title = %q, want %q", c.Title, want)
	}
	if want := "first real line of body text"; c.Description != want {
		t.Errorf("Description = %q, want %q", c.Description, want)
	}
	if c.Content != content {
		t.Errorf("Content = %q, want the normalized content unchanged", c.Content)
	}
	if len(c.Tags) != 1 || c.Tags[0] != "nested" {
		t.Errorf("Tags = %v, want [\"nested\"]", c.Tags)
	}
	if c.Domain != "nested" {
		t.Errorf("Domain = %q, want %q", c.Domain, "nested")
	}
	if c.SourceID != ref.SourceID || c.SourcePath != ref.Path {
		t.Errorf("SourceID/SourcePath = %q/%q, want %q/%q", c.SourceID, c.SourcePath, ref.SourceID, ref.Path)
	}
	if c.FetchedHash != "abc123" {
		t.Errorf("FetchedHash = %q, want %q", c.FetchedHash, "abc123")
	}
}

func TestBuildCandidate_TopLevelFile_NoTags_GeneralDomain(t *testing.T) {
	ref := source.ItemRef{SourceID: "fs:/root", Path: "sample.md"}
	c, err := BuildCandidate(ref, "hash", "# Sample\n\nbody")
	if err != nil {
		t.Fatalf("BuildCandidate: %v", err)
	}
	if len(c.Tags) != 0 {
		t.Errorf("Tags = %v, want empty for a top-level file", c.Tags)
	}
	if c.Domain != "general" {
		t.Errorf("Domain = %q, want %q", c.Domain, "general")
	}
}

func TestBuildCandidate_EmptyPath_Rejected(t *testing.T) {
	_, err := BuildCandidate(source.ItemRef{SourceID: "fs:/root", Path: ""}, "hash", "content")
	if err == nil {
		t.Fatal("BuildCandidate with empty Path = nil error, want rejection")
	}
	if !errors.Is(err, ErrEmptySourcePath) {
		t.Errorf("error = %v, want it to wrap ErrEmptySourcePath", err)
	}
}

func TestBuildCandidate_PathSanitizesToEmptySlug_Rejected(t *testing.T) {
	// "!!!.md" sanitizes to an empty slug: stripExt gives "!!!", and
	// sanitizeSegment replaces every rune with '_' then trims leading/
	// trailing '_', leaving "". BuildCandidate must reject this rather
	// than produce a degenerate "ingest.fs." skill name.
	ref := source.ItemRef{SourceID: "fs:/root", Path: "!!!.md"}
	_, err := BuildCandidate(ref, "hash", "content")
	if err == nil {
		t.Fatal("BuildCandidate with a path that sanitizes to an empty slug = nil error, want rejection")
	}
	if !errors.Is(err, ErrEmptySlug) {
		t.Errorf("error = %v, want it to wrap ErrEmptySlug", err)
	}
}

func TestBuildCandidate_DifferentSourceScheme_NamespacesByScheme(t *testing.T) {
	// The pipeline must never special-case a concrete Source
	// implementation -- the generated Name namespace comes purely from
	// the ItemRef.SourceID scheme prefix, so a hypothetical future
	// "url:" source is namespaced automatically and identically, with no
	// candidate.go change required.
	ref := source.ItemRef{SourceID: "url:https://example.com", Path: "docs/page.md"}
	c, err := BuildCandidate(ref, "hash", "# Page\n\nbody")
	if err != nil {
		t.Fatalf("BuildCandidate: %v", err)
	}
	if !strings.HasPrefix(c.Name, "ingest.url.") {
		t.Errorf("Name = %q, want prefix %q", c.Name, "ingest.url.")
	}
}

func TestDescriptionFrom_SkipsLeadingHeading(t *testing.T) {
	got := descriptionFrom("# Title\n\n  first real paragraph line  \n\nsecond")
	if got != "first real paragraph line" {
		t.Errorf("descriptionFrom = %q, want %q", got, "first real paragraph line")
	}
}

func TestDescriptionFrom_TruncatesLongLine(t *testing.T) {
	long := strings.Repeat("a", descriptionMaxLen+50)
	got := descriptionFrom(long)
	if len([]rune(got)) != descriptionMaxLen {
		t.Fatalf("len(descriptionFrom) = %d, want %d", len([]rune(got)), descriptionMaxLen)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated description = %q, want it to end with an ellipsis", got)
	}
}

func TestDescriptionFrom_Empty(t *testing.T) {
	if got := descriptionFrom(""); got != "" {
		t.Errorf("descriptionFrom(\"\") = %q, want empty", got)
	}
}
