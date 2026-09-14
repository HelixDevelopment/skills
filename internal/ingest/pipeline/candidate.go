package pipeline

import (
	"errors"
	"strings"

	"github.com/HelixDevelopment/skills/internal/ingest/source"
)

// ErrEmptySourcePath is returned by BuildCandidate when ref.Path is empty.
var ErrEmptySourcePath = errors.New("ingest/pipeline: empty source item path")

// ErrEmptySlug is returned by BuildCandidate when ref.Path sanitizes down
// to an empty slug (e.g. a path made entirely of characters this
// increment's slugifier strips).
var ErrEmptySlug = errors.New("ingest/pipeline: path sanitizes to an empty skill name slug")

// descriptionMaxLen bounds CandidateSkill.Description so a very long
// document does not produce an unbounded description string; truncation
// is rune-safe and marked with an ellipsis.
const descriptionMaxLen = 280

// CandidateSkill is the deterministic, LLM-free candidate produced by
// EXTRACT + NORMALIZE + BuildCandidate (DESIGN.md §2 stages 1-2). It is
// the input to MapToSkill (map.go).
//
// This increment does NOT run DESIGN.md §2 stage 3 (LLM-REFINE) -- that
// stage is a separate, later work item (F2.4 in TRACKED_ITEMS.md) that
// calls the EXISTING autoexpand.LLMClient interface to enrich/restructure
// this candidate. BuildCandidate produces a fully valid, if unrefined,
// candidate on its own, so the Source -> Skill chain is genuinely
// exercisable end-to-end without an LLM call, network access, or a
// database.
type CandidateSkill struct {
	Name        string
	Title       string
	Description string
	Content     string
	Tags        []string
	Domain      string
	Complexity  string
	SourceID    string
	SourcePath  string
	FetchedHash string
}

// BuildCandidate builds a CandidateSkill from a fetched item's
// already-normalized content. Name/Title/Description/Tags/Domain are all
// derived purely and deterministically from ref and normalizedContent --
// no network call, no LLM call, no randomness.
func BuildCandidate(ref source.ItemRef, fetchedHash, normalizedContent string) (*CandidateSkill, error) {
	if strings.TrimSpace(ref.Path) == "" {
		return nil, ErrEmptySourcePath
	}

	slug := SlugFromPath(ref.Path)
	if slug == "" {
		return nil, ErrEmptySlug
	}
	scheme := schemeFromSourceID(ref.SourceID)

	dirTags := tagsFromPath(ref.Path)
	domain := "general"
	if len(dirTags) > 0 {
		domain = dirTags[0]
	}

	return &CandidateSkill{
		Name:        "ingest." + scheme + "." + slug,
		Title:       TitleFromPath(ref.Path),
		Description: descriptionFrom(normalizedContent),
		Content:     normalizedContent,
		Tags:        dirTags,
		Domain:      domain,
		// Complexity is a documented, deliberately-generic default in
		// this increment -- a real assessment needs either LLM-REFINE
		// (F2.4, not run here) or an author-supplied hint neither of
		// which exists for a bare ingested file. Recording anything more
		// specific here would be a §11.4.6 guess.
		Complexity:  "beginner",
		SourceID:    ref.SourceID,
		SourcePath:  ref.Path,
		FetchedHash: fetchedHash,
	}, nil
}

// schemeFromSourceID extracts the scheme prefix of a Source.ID() value
// (e.g. "fs" from "fs:/abs/root", "url" from "url:https://example.com")
// so the generated skill name is namespaced by source CLASS without
// candidate.go ever needing to special-case a concrete Source
// implementation -- the pipeline never branches on source type past the
// Source interface boundary (DESIGN.md §1).
func schemeFromSourceID(sourceID string) string {
	if idx := strings.IndexByte(sourceID, ':'); idx > 0 {
		return sanitizeSegment(sourceID[:idx])
	}
	return "source"
}

// SlugFromPath converts a source-relative path into a dotted, lowercase,
// namespace-safe slug (e.g. "nested/child_note.md" -> "nested.child_note"),
// mirroring the dotted skill-name convention already used by
// seed/skills/*.toml (e.g. "android.overview"). It is pure: the SAME path
// always produces the SAME slug.
func SlugFromPath(path string) string {
	segments := strings.Split(strings.Trim(filepathToSlash(path), "/"), "/")
	out := make([]string, 0, len(segments))
	for i, seg := range segments {
		if i == len(segments)-1 {
			seg = stripExt(seg)
		}
		s := sanitizeSegment(seg)
		if s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, ".")
}

// tagsFromPath returns the sanitized directory segments of path (every
// segment except the final file-name segment) as candidate tags. A
// top-level file (no directory component) yields no tags.
func tagsFromPath(path string) []string {
	segments := strings.Split(strings.Trim(filepathToSlash(path), "/"), "/")
	if len(segments) <= 1 {
		return nil
	}
	tags := make([]string, 0, len(segments)-1)
	for _, seg := range segments[:len(segments)-1] {
		if s := sanitizeSegment(seg); s != "" {
			tags = append(tags, s)
		}
	}
	return tags
}

// descriptionFrom derives a short description from normalized content: it
// skips a single leading Markdown heading line (if present) and returns
// the first non-empty subsequent line, whitespace-collapsed and rune-safe
// truncated to descriptionMaxLen with a trailing ellipsis when truncated.
func descriptionFrom(content string) string {
	lines := strings.Split(content, "\n")
	skippedHeading := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !skippedHeading && strings.HasPrefix(trimmed, "#") {
			skippedHeading = true
			continue
		}
		return truncateRunes(collapseWhitespace(trimmed), descriptionMaxLen)
	}
	return ""
}

func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}

// sanitizeSegment lower-cases s and replaces every rune that is not
// [a-z0-9_] with '_', collapses consecutive '_' into one, and trims
// leading/trailing '_'.
func sanitizeSegment(s string) string {
	lower := strings.ToLower(s)
	var b strings.Builder
	lastUnderscore := false
	for _, r := range lower {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastUnderscore = false
		default:
			if !lastUnderscore {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

func stripExt(name string) string {
	if idx := strings.LastIndexByte(name, '.'); idx > 0 {
		return name[:idx]
	}
	return name
}

func filepathToSlash(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}
