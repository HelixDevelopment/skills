package pipeline

import (
	"path/filepath"
	"strings"

	"github.com/HelixDevelopment/skills/internal/ingest/source"
)

// NormalizeContent converts extracted text into the Markdown-ish shape
// models.Skill.Content already expects (DESIGN.md §2 stage 2, matching the
// LLM-generated skill shape in autoexpand.GeneratePrompt and every
// seed/skills/*.toml's `content` field): trailing whitespace is trimmed to
// exactly one trailing newline, and -- if the text does not already start
// with a Markdown heading -- a deterministic `# <Title>` heading derived
// from the source item's file name is prepended, so every produced Skill
// has a heading regardless of whether the original document did.
//
// NormalizeContent is pure: the SAME (text, ref) always produces the SAME
// output.
func NormalizeContent(text string, ref source.ItemRef) string {
	trimmed := strings.TrimRight(text, "\n\r\t ")
	body := strings.TrimSpace(trimmed)
	if !isMarkdownHeadingStart(body) {
		if body == "" {
			// Whitespace-only (or empty) input has no body to preserve --
			// emit the synthetic heading alone, so the output still ends
			// with EXACTLY one trailing newline as documented above
			// (appending "\n\n"+trimmed here, as the non-empty branch
			// does, would leave two blank lines then the final "+\n",
			// i.e. THREE trailing newlines, contradicting that contract;
			// §11.4.194).
			return "# " + TitleFromPath(ref.Path) + "\n"
		}
		trimmed = "# " + TitleFromPath(ref.Path) + "\n\n" + trimmed
	}
	return trimmed + "\n"
}

// isMarkdownHeadingStart reports whether s begins with a CommonMark ATX
// heading marker: one to six '#' characters immediately followed by
// either whitespace or the end of the string
// (https://spec.commonmark.org/0.30/#atx-headings). This deliberately
// does NOT match a bare leading '#' with no following space/tab, which
// would otherwise misclassify content such as a C preprocessor directive
// ("#include <stdio.h>") as an existing Markdown heading and skip
// prepending the synthetic title (§11.4.194).
func isMarkdownHeadingStart(s string) bool {
	i := 0
	for i < len(s) && s[i] == '#' {
		i++
	}
	if i == 0 || i > 6 {
		return false
	}
	if i == len(s) {
		return true
	}
	return s[i] == ' ' || s[i] == '\t'
}

// TitleFromPath derives a human-readable title from a source-relative
// path's file name: the extension is stripped, path separators/
// underscores/hyphens become spaces, and each word is title-cased. It is
// pure and deterministic.
func TitleFromPath(path string) string {
	base := filepath.Base(filepath.ToSlash(path))
	base = strings.TrimSuffix(base, filepath.Ext(base))
	base = strings.NewReplacer("_", " ", "-", " ", "/", " ").Replace(base)
	words := strings.Fields(base)
	if len(words) == 0 {
		return "Untitled"
	}
	for i, w := range words {
		words[i] = titleCaseWord(w)
	}
	return strings.Join(words, " ")
}

// titleCaseWord upper-cases only the first rune of w, leaving the rest
// unchanged (so acronyms embedded in a file name, e.g. "api", stay
// readable rather than being forced into a single canonical case beyond
// the leading letter).
func titleCaseWord(w string) string {
	if w == "" {
		return w
	}
	r := []rune(w)
	r[0] = []rune(strings.ToUpper(string(r[0])))[0]
	return string(r)
}
