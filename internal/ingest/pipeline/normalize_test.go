package pipeline

import (
	"strings"
	"testing"

	"github.com/HelixDevelopment/skills/internal/ingest/source"
)

func TestNormalizeContent_PreservesExistingHeading(t *testing.T) {
	ref := source.ItemRef{Path: "sample.md"}
	in := "# Sample Skill Document\n\nbody text\n\n\n"
	got := NormalizeContent(in, ref)

	if !strings.HasPrefix(got, "# Sample Skill Document") {
		t.Fatalf("normalized content = %q, want it to start with the existing heading", got)
	}
	if strings.Count(got, "# Sample Skill Document") != 1 {
		t.Fatalf("normalized content duplicated the existing heading: %q", got)
	}
	if !strings.HasSuffix(got, "\n") || strings.HasSuffix(got, "\n\n") {
		t.Fatalf("normalized content must end with EXACTLY one trailing newline, got %q", got)
	}
}

func TestNormalizeContent_PrependsSyntheticHeading_WhenAbsent(t *testing.T) {
	ref := source.ItemRef{Path: "nested/plain_notes.txt"}
	in := "plain text notes about retry backoff\n\nmore body"
	got := NormalizeContent(in, ref)

	wantHeading := "# " + TitleFromPath(ref.Path)
	if !strings.HasPrefix(got, wantHeading) {
		t.Fatalf("normalized content = %q, want it to start with synthetic heading %q", got, wantHeading)
	}
	if !strings.Contains(got, in) {
		t.Fatalf("normalized content = %q, want it to still contain the original body verbatim", got)
	}
}

func TestNormalizeContent_HashWithoutSpace_NotTreatedAsExistingHeading(t *testing.T) {
	// "#include <stdio.h>" starts with '#' but is NOT a Markdown ATX
	// heading (no space/tab after the '#'s) -- a looser bare-HasPrefix("#")
	// check would misclassify it as an existing heading and skip
	// prepending the synthetic title (§11.4.194).
	ref := source.ItemRef{Path: "nested/snippet.txt"}
	in := "#include <stdio.h>\n\nint main(void) { return 0; }\n"
	got := NormalizeContent(in, ref)

	wantHeading := "# " + TitleFromPath(ref.Path)
	if !strings.HasPrefix(got, wantHeading) {
		t.Fatalf("normalized content = %q, want it to start with synthetic heading %q (a bare '#include' must not be mistaken for an existing heading)", got, wantHeading)
	}
	if !strings.Contains(got, "#include <stdio.h>") {
		t.Fatalf("normalized content = %q, want it to still contain the original body verbatim", got)
	}
}

func TestNormalizeContent_MultiHashHeading_StillRecognizedAsExisting(t *testing.T) {
	// A level-2+ ATX heading ("## ...") must still be recognized as an
	// existing heading -- the stricter check must not regress the
	// multi-'#' case while fixing the no-space case above.
	ref := source.ItemRef{Path: "sample.md"}
	in := "## Sub Heading\n\nbody\n"
	got := NormalizeContent(in, ref)

	if !strings.HasPrefix(got, "## Sub Heading") {
		t.Fatalf("normalized content = %q, want it to start with the existing \"## Sub Heading\" (not a synthetic one)", got)
	}
}

func TestNormalizeContent_WhitespaceOnlyInput_ExactlyOneTrailingNewline(t *testing.T) {
	ref := source.ItemRef{Path: "nested/blank.txt"}
	in := "   \n\t  \n"
	got := NormalizeContent(in, ref)

	want := "# " + TitleFromPath(ref.Path) + "\n"
	if got != want {
		t.Fatalf("NormalizeContent(whitespace-only) = %q, want %q", got, want)
	}
	if strings.HasSuffix(got, "\n\n") {
		t.Fatalf("normalized content = %q, want EXACTLY one trailing newline for whitespace-only input", got)
	}
}

func TestNormalizeContent_Deterministic(t *testing.T) {
	ref := source.ItemRef{Path: "a/b/c.txt"}
	in := "one\ntwo\nthree"
	got1 := NormalizeContent(in, ref)
	got2 := NormalizeContent(in, ref)
	if got1 != got2 {
		t.Fatalf("NormalizeContent is not deterministic: %q != %q", got1, got2)
	}
}

// TestIsMarkdownHeadingStart pins the three CommonMark ATX-heading branches
// of isMarkdownHeadingStart (normalize.go) that a prior review round found
// surviving mutation while the suite stayed GREEN (F-NEW-1, §11.4.135/§1.1
// round-3 remediation). isMarkdownHeadingStart is unexported but this file
// is `package pipeline` (white-box), so it is called directly rather than
// indirectly through NormalizeContent's TrimRight/TrimSpace preprocessing.
func TestIsMarkdownHeadingStart(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		// A bare "#" at end-of-string IS a valid (empty) ATX heading per
		// CommonMark 0.30 -- kills the `i == len(s)` -> `false` mutation,
		// which would wrongly return false here.
		{"bare hash at end of string is an empty ATX heading", "#", true},
		// Seven '#' characters exceed the CommonMark six-level ATX-heading
		// maximum -- kills the `i > 6` -> `i > 7` mutation, which would
		// wrongly return true (falling through to the space/tab check) at
		// exactly seven hashes.
		{"seven hashes exceed the six-level ATX heading max", "####### x", false},
		// A tab immediately after the hash run is a valid ATX-heading
		// separator (whitespace), not only a literal space -- kills
		// dropping the `|| s[i] == '\t'` branch, which would wrongly
		// return false for a tab-separated heading marker.
		{"tab immediately after hash still starts a heading", "#\tx", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isMarkdownHeadingStart(tc.in); got != tc.want {
				t.Errorf("isMarkdownHeadingStart(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestTitleFromPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"sample.md", "Sample"},
		{"plain_notes.txt", "Plain Notes"},
		{"nested/child_note.md", "Child Note"},
		{"a-b-c.md", "A B C"},
		{"", "Untitled"},
	}
	for _, tc := range cases {
		if got := TitleFromPath(tc.path); got != tc.want {
			t.Errorf("TitleFromPath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}
