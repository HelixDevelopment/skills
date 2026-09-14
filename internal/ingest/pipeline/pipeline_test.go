package pipeline

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/HelixDevelopment/skills/internal/ingest/source"
	"github.com/HelixDevelopment/skills/internal/models"
)

// fixtureRoot points at the SAME real, offline fixture tree
// internal/ingest/source's own tests use, proving the full chain
// (Source.List/Fetch -> ExtractText -> NormalizeContent -> BuildCandidate
// -> MapToSkill) against real files on a real filesystem -- no network,
// no database, no mocks beyond what §11.4.27(A) permits.
const fixtureRoot = "../source/testdata/docs"

func realFilesystemSource(t *testing.T) source.Source {
	t.Helper()
	root, err := filepath.Abs(fixtureRoot)
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	src, err := source.NewFilesystemSource(root, []string{root}, source.WithExcludePatterns([]string{".git"}))
	if err != nil {
		t.Fatalf("NewFilesystemSource: %v", err)
	}
	return src
}

func TestIngestOne_RealFilesystemSource_SampleMarkdown(t *testing.T) {
	src := realFilesystemSource(t)
	ref := source.ItemRef{SourceID: src.ID(), Path: "sample.md"}

	skill, err := IngestOne(context.Background(), src, ref)
	if err != nil {
		t.Fatalf("IngestOne: %v", err)
	}

	if skill.Status != models.SkillStatusDraft {
		t.Errorf("Status = %q, want draft", skill.Status)
	}
	if want := "ingest.fs.sample"; skill.Name != want {
		t.Errorf("Name = %q, want %q", skill.Name, want)
	}
	if len(skill.Resources) != 1 || skill.Resources[0].FetchedHash == "" {
		t.Fatalf("Resources = %+v, want exactly one with a real non-empty FetchedHash", skill.Resources)
	}
}

func TestIngestOne_RealFilesystemSource_PlainTextGetsSyntheticHeading(t *testing.T) {
	src := realFilesystemSource(t)
	ref := source.ItemRef{SourceID: src.ID(), Path: "plain_notes.txt"}

	skill, err := IngestOne(context.Background(), src, ref)
	if err != nil {
		t.Fatalf("IngestOne: %v", err)
	}
	if skill.Title != "Plain Notes" {
		t.Errorf("Title = %q, want %q", skill.Title, "Plain Notes")
	}
}

func TestIngestAll_RealFilesystemSource_EveryItemHasATerminalOutcome(t *testing.T) {
	src := realFilesystemSource(t)

	outcomes, err := IngestAll(context.Background(), src)
	if err != nil {
		t.Fatalf("IngestAll: %v", err)
	}

	refs, err := src.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(outcomes) != len(refs) {
		t.Fatalf("IngestAll produced %d outcomes, want exactly one per listed item (%d)", len(outcomes), len(refs))
	}

	sawSuccess, sawUnsupportedSkip := false, false
	for _, o := range outcomes {
		// Every outcome MUST be terminal: exactly one of Skill/Err set --
		// never both, never neither (that would be a silently-dropped
		// item).
		if (o.Skill == nil) == (o.Err == nil) {
			t.Fatalf("Outcome for %q is not terminal: Skill=%v Err=%v", o.Ref.Path, o.Skill, o.Err)
		}
		switch {
		case o.Err == nil:
			sawSuccess = true
		case errors.Is(o.Err, ErrUnsupportedContentType):
			sawUnsupportedSkip = true
		}
	}
	if !sawSuccess {
		t.Error("expected at least one successful outcome (sample.md/plain_notes.txt/nested/child_note.md)")
	}
	// The fixture tree includes unsupported_page.html, which
	// contentTypeForExt (internal/ingest/source/filesystem.go) classifies
	// as "text/html" -- a content type this increment's ExtractText does
	// not yet handle (internal/ingest/pipeline/extract.go only supports
	// text/plain and text/markdown) -- so ExtractText must honestly
	// reject it as unsupported rather than silently ingesting garbage.
	// (An earlier version of this comment attributed this outcome to
	// ".git/HEAD"; realFilesystemSource above excludes ".git" via
	// WithExcludePatterns, so no such item is ever listed here, and that
	// fixture path no longer exists as a static file regardless -- see
	// internal/ingest/source/filesystem_test.go's buildTestTreeWithGitDir
	// for where a ".git" fixture is now built at test time instead.)
	if !sawUnsupportedSkip {
		t.Error("expected at least one ErrUnsupportedContentType outcome for the non-text fixture (unsupported_page.html)")
	}
}

func TestIngestOne_FakeSource_NoSourceSpecialCasing(t *testing.T) {
	// Proves the pipeline works against ANY Source implementation, not
	// just FilesystemSource -- the pipeline never branches on concrete
	// source type past the Source interface boundary (DESIGN.md §1).
	fake := newFakeSourceForPipelineTest("url:https://example.com", "docs/page.md", "text/markdown", []byte("# Page\n\nreal content"))

	ref := source.ItemRef{SourceID: fake.ID(), Path: "docs/page.md"}
	skill, err := IngestOne(context.Background(), fake, ref)
	if err != nil {
		t.Fatalf("IngestOne: %v", err)
	}
	if want := "ingest.url.docs.page"; skill.Name != want {
		t.Errorf("Name = %q, want %q", skill.Name, want)
	}
}
