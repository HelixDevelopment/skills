// Package skillmd parses SKILL.md files — the YAML-frontmatter-plus-
// markdown-body format Claude Code and the broader open agent-skills
// ecosystem use to define a skill (see docs/source_ingestion/CATALOG.md §2 for the authoritative field table
// this parser implements, verified against code.claude.com/docs/en/skills
// 2026-07-16).
//
// Parse is a pure function: no I/O, no database access. It turns the raw
// bytes of one SKILL.md file into a ParsedSkill value; nothing in this
// package writes to disk, the network, or a database.
package skillmd

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

// Sentinel errors returned by Parse. Callers should use errors.Is to
// classify a parse failure rather than string-matching the message
// (mirrors the internal/skill package's own sentinel-error convention,
// e.g. skill.ErrSkillNotFound).
var (
	// ErrNoFrontmatter is returned when the input has no
	// "---\n...\n---\n" YAML frontmatter block at all. A SKILL.md with
	// no frontmatter carries no `name`, which every downstream consumer
	// (the mapper, the store) requires — this is treated as a hard
	// parse failure, not a permissive fallback.
	ErrNoFrontmatter = errors.New("skillmd: no YAML frontmatter block found (expected a leading \"---\" delimiter)")

	// ErrUnterminatedFrontmatter is returned when an opening "---"
	// delimiter is present but no closing "---" line follows it.
	ErrUnterminatedFrontmatter = errors.New("skillmd: unterminated frontmatter block (opening \"---\" with no closing \"---\")")

	// ErrMissingName is returned when the frontmatter parses but has no
	// (non-empty) "name" field. Per docs/source_ingestion/CATALOG.md §2,
	// name+description are the portable open-standard minimum; name is
	// non-negotiable since it becomes part of skills.name (UNIQUE) once
	// mapped.
	ErrMissingName = errors.New("skillmd: frontmatter missing required \"name\" field")

	// ErrFrontmatterTooLarge is returned when the RAW (pre-decode) YAML
	// frontmatter block exceeds maxFrontmatterBytes. Real SKILL.md
	// frontmatter (name/description/license/version/tags/allowed-tools/
	// ...) is small — the CATALOG.md §2 field table is a couple dozen
	// short scalar/list fields at most — so a frontmatter block this
	// large is never legitimate. This is checked BEFORE
	// yaml.Unmarshal ever runs, as the first of two independent,
	// defense-in-depth bounds against a YAML alias-expansion ("billion
	// laughs") resource-exhaustion pattern (§11.4.85): goccy's decoder
	// keeps aliased subtrees as SHARED Go references rather than
	// deep-copying them, so Unmarshal itself stays cheap even for a
	// deeply-aliased document (measured: a hand-crafted 469-byte,
	// 7-level alias-chain fixture unmarshals in ~0.3ms) — the actual
	// exponential blow-up happens downstream, the moment something
	// naively walks that shared-reference graph (e.g.
	// encoding/json.Marshal, which mapper.Map calls on
	// ParsedSkill.RawFrontmatter and which does not detect or dedupe the
	// sharing). A byte-size cap here cannot by itself bound that
	// downstream blow-up (a handful of alias levels fits in well under
	// 1 KiB of source text yet can reference the same subtree
	// exponentially many times), so this is a SECOND-independent, not
	// SOLE, defense: the primary bound against the exponential-expansion
	// case lives in mapper.Map (see its estimateMarshaledSize), which
	// bounds the walk itself rather than the input's raw byte count.
	// This cap's own job is narrower and still worth having on its own
	// merits: it rejects any frontmatter block that is simply too large
	// to be a real SKILL.md file, independent of whether it uses
	// aliases at all.
	ErrFrontmatterTooLarge = errors.New("skillmd: frontmatter block exceeds the maximum allowed size")
)

// maxFrontmatterBytes bounds the RAW (pre-decode) YAML frontmatter block
// size Parse will attempt to decode. See ErrFrontmatterTooLarge's doc
// comment for why this is a generous-but-safe belt, not the primary
// defense against a YAML alias-expansion attack (that lives in
// mapper.Map).
const maxFrontmatterBytes = 64 * 1024 // 64 KiB

// ParsedSkill is the in-memory result of parsing one SKILL.md file. It
// carries the split frontmatter/body, the specific fields downstream
// consumers need directly (Name/Description/License), the full raw
// frontmatter map (so unknown/extension fields — e.g. Claude-Code-specific
// `allowed-tools`, or a marketplace's `compatibility`/`tags` — are
// preserved losslessly rather than dropped), and a best-effort inventory
// of scripts/references/assets the body text references by their
// conventional directory prefix.
type ParsedSkill struct {
	// Name is the frontmatter "name" field, trimmed. Required.
	Name string

	// Description is the frontmatter "description" field, trimmed. If
	// absent or empty, it is derived from the body's first non-empty
	// paragraph (per docs/source_ingestion/CATALOG.md §2's documented
	// fallback: "If omitted, first markdown paragraph is used").
	Description string

	// License is the frontmatter "license" field, trimmed. May be empty
	// — docs/source_ingestion/CATALOG.md §2 documents license as
	// optional; the license-gate decision (an allowlist check) is the
	// mapper's job, not this parser's.
	License string

	// RawFrontmatter is the FULL decoded frontmatter map, including
	// Name/Description/License duplicated under their original keys.
	// This is the lossless-preservation contract: any field a
	// marketplace/tool extension adds (allowed-tools, disallowed-tools,
	// context, arguments, user-invocable, version, author,
	// compatibility, tags, ...) survives here even though this parser
	// has no first-class Go field for it.
	RawFrontmatter map[string]interface{}

	// Body is the markdown content after the closing frontmatter
	// delimiter, with line endings normalized to "\n".
	Body string

	// Scripts, References, Assets are a best-effort, body-text-only
	// inventory of paths the SKILL.md body references under the
	// conventional scripts/, references/, assets/ directory prefixes
	// (docs/source_ingestion/CATALOG.md §2's documented directory
	// layout). This is NOT a
	// directory listing — Parse never sees sibling files, only the one
	// SKILL.md's bytes — so this is honestly a heuristic over the body
	// text, not an authoritative file inventory (a real inventory needs
	// the fetcher's tree listing, which is a separate, later
	// integration step this package does not implement).
	Scripts    []string
	References []string
	Assets     []string

	// SourcePath is the caller-supplied path this SKILL.md was read
	// from (e.g. "systematic-debugging/SKILL.md"), carried through
	// unchanged for error messages and for the caller's own
	// provenance-tracking.
	SourcePath string

	// ContentHash is sha256(text), hex-encoded, where text is the FULL
	// raw file content Parse received — the entire frontmatter block AND
	// body, verbatim — after only the BOM-strip and CRLF/CR->LF
	// line-ending normalization (normalizeText) has been applied. This
	// is the change-detection value a re-scan compares against a
	// previously-recorded hash to decide whether an upstream file's
	// substantive content actually changed.
	//
	// Hashing the full normalized file (rather than a hand-picked field
	// list) is deliberate, closing two defects a narrower formula had:
	//
	//  1. Coverage (of the FILE-CONTENT-derived inputs only — see the
	//     "Scope" paragraph below for what this deliberately excludes):
	//     it covers every field Parse itself derives from the upstream
	//     file's bytes that the mapper carries into models.Skill FROM
	//     this ParsedSkill (Name, Description, License, Version, and any
	//     other frontmatter key carried into Metadata via
	//     RawFrontmatter) — a description-only or version-only upstream
	//     edit now flips the hash, so a hash-gated re-scan can never skip
	//     re-mapping a file whose mapped output actually changed FROM
	//     THE FILE'S OWN CONTENT. A formula covering only Name+License+
	//     Body missed exactly this: a description-only edit produced an
	//     IDENTICAL hash, so a re-scan gated on ContentHash would
	//     silently keep serving the stale description forever.
	//  2. No forgeable separator: hashing discrete fields joined by a
	//     delimiter byte (e.g. "\x00") is vulnerable to a crafted
	//     delimiter-valued byte embedded inside one field's value
	//     absorbing the join and producing an identical concatenation
	//     for two inputs whose field values genuinely differ (observed:
	//     a License ending in an embedded NUL escape absorbing the
	//     "\x00" join byte the old formula used, producing the SAME
	//     hash for two files with different License values). Hashing the
	//     whole file removes the join entirely — there is no delimiter
	//     left to forge.
	//
	// Acceptable direction: this can OVER-refresh on a purely cosmetic
	// change (e.g. re-indented YAML, an added trailing blank line) that
	// does not change any mapped field's value — that is the SAFE
	// conservative direction. A change-detection gate must never
	// UNDER-refresh (miss a real content change and keep serving
	// stale/gated content indefinitely); an occasional harmless extra
	// re-map is the acceptable cost of never under-refreshing.
	//
	// Scope (§11.4.194(2)/§11.4.6 — an unproven completeness claim is an
	// overclaim, not a finding): ContentHash is complete over
	// FILE-CONTENT-derived inputs ONLY. It is NOT complete over every
	// input mapper.Map consumes. Map() also takes THREE caller-supplied
	// CONFIG inputs that are entirely outside this hash and that this
	// hash therefore CANNOT detect a change in:
	//
	//   - sourceSlug, which becomes the Name PREFIX
	//     (sourceSlug + "." + parsed.Name);
	//   - licenseAllowlist, which decides the ALLOW/DENY verdict — i.e.
	//     whether Result.Skill.Content is the real upstream body or a
	//     license-gated stub, and whether Result.Skill.Resources carries
	//     the "see upstream" pointer;
	//   - sourcePermalink, which becomes the Resource.URL when the
	//     license gate fires.
	//
	// A change to ANY of those three config inputs, with the upstream
	// file's bytes completely UNCHANGED, produces an IDENTICAL
	// ContentHash. A re-scan mechanism gated on ContentHash ALONE (as
	// currently drafted in docs/source_ingestion/DESIGN.md §2.D/§3 D6 —
	// "(source_id, source_path, content_hash) determines the action")
	// would therefore silently keep serving a STALE ALLOW/DENY verdict
	// (or a stale Name prefix, or a stale permalink) after an operator
	// edits the license allowlist or a source's configured slug/
	// permalink, without ever re-running Map. This is a DOCUMENTED GAP,
	// not a hidden defect: no re-scan mechanism is implemented in this
	// package or anywhere else in this tree yet (Parse/Map are both pure
	// functions with no re-scan logic of their own) — see
	// docs/source_ingestion/DESIGN.md §3 D9 for the config-axis rule the
	// not-yet-implemented re-scan orchestrator MUST apply: a config
	// change forces a full remap independent of whether ContentHash
	// changed; the content-hash skip keys ONLY file content.
	ContentHash string
}

// pathRefPattern finds body-text references to the three conventional
// skill-subdirectory prefixes docs/source_ingestion/CATALOG.md §2
// documents.
var pathRefPattern = regexp.MustCompile(`\b(?:scripts|references|assets)/[A-Za-z0-9._\-/]+`)

// Parse splits raw's YAML frontmatter from its markdown body and returns
// the resulting ParsedSkill. sourcePath is used only for error messages
// and is copied into the result's SourcePath field.
//
// Parse never panics: any unexpected failure inside the YAML decoder (or
// this function itself) is recovered and returned as a wrapped error, per
// the requirement that a corrupt/malformed input error cleanly rather than
// crash the caller's batch scan.
func Parse(raw []byte, sourcePath string) (parsed *ParsedSkill, err error) {
	defer func() {
		if r := recover(); r != nil {
			parsed = nil
			err = fmt.Errorf("skillmd: panic parsing %s: %v", sourcePath, r)
		}
	}()

	text := normalizeText(string(raw))

	frontmatterYAML, body, splitErr := splitFrontmatter(text)
	if splitErr != nil {
		return nil, fmt.Errorf("%s: %w", sourcePath, splitErr)
	}

	if len(frontmatterYAML) > maxFrontmatterBytes {
		return nil, fmt.Errorf("%s: %w (%d bytes, cap %d)", sourcePath, ErrFrontmatterTooLarge, len(frontmatterYAML), maxFrontmatterBytes)
	}

	var fm map[string]interface{}
	if err := yaml.Unmarshal([]byte(frontmatterYAML), &fm); err != nil {
		return nil, fmt.Errorf("skillmd: parse frontmatter in %s: %w", sourcePath, err)
	}
	if fm == nil {
		fm = map[string]interface{}{}
	}

	name := strings.TrimSpace(stringField(fm, "name"))
	if name == "" {
		return nil, fmt.Errorf("%s: %w", sourcePath, ErrMissingName)
	}

	description := strings.TrimSpace(stringField(fm, "description"))
	if description == "" {
		description = firstParagraph(body)
	}

	license := strings.TrimSpace(stringField(fm, "license"))

	scripts, references, assets := extractPathRefs(body)

	// ContentHash covers the FULL normalized file text (frontmatter +
	// body) — see the ContentHash field doc for why hashing the whole
	// file, rather than a hand-picked field list joined by a delimiter,
	// is required to (a) cover every mapper-materialized field and (b)
	// eliminate the forgeable-separator collision a discrete-field
	// formula is exposed to.
	sum := sha256.Sum256([]byte(text))

	return &ParsedSkill{
		Name:           name,
		Description:    description,
		License:        license,
		RawFrontmatter: fm,
		Body:           body,
		Scripts:        scripts,
		References:     references,
		Assets:         assets,
		SourcePath:     sourcePath,
		ContentHash:    hex.EncodeToString(sum[:]),
	}, nil
}

// stringField returns fm[key] as a string, or "" if the key is absent or
// not a string (e.g. a YAML boolean/number/list under that key). This is
// deliberately lenient rather than an error —
// docs/source_ingestion/CATALOG.md §2 documents the frontmatter as "a
// loose contract"; a field of an unexpected type should not abort the
// whole parse.
func stringField(fm map[string]interface{}, key string) string {
	v, ok := fm[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

// normalizeText strips a leading UTF-8 BOM and normalizes CRLF/CR line
// endings to LF, so downstream line-based splitting behaves identically
// regardless of the source file's original line-ending convention.
func normalizeText(s string) string {
	s = strings.TrimPrefix(s, "\uFEFF")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return s
}

// splitFrontmatter splits text into its YAML frontmatter block and
// markdown body. text must already be normalized (normalizeText) before
// calling this. Returns ErrNoFrontmatter if the first line is not exactly
// "---", and ErrUnterminatedFrontmatter if no closing "---" line is found.
func splitFrontmatter(text string) (frontmatterYAML, body string, err error) {
	// strings.Split always returns at least one element (a []string{""}
	// for empty input) — a len(lines) == 0 guard here would be dead code,
	// never reachable; lines[0] is always a safe index.
	lines := strings.Split(text, "\n")
	if strings.TrimSpace(lines[0]) != "---" {
		return "", "", ErrNoFrontmatter
	}
	endIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			endIdx = i
			break
		}
	}
	if endIdx == -1 {
		return "", "", ErrUnterminatedFrontmatter
	}
	frontmatterYAML = strings.Join(lines[1:endIdx], "\n")
	body = strings.Join(lines[endIdx+1:], "\n")
	return frontmatterYAML, body, nil
}

// firstParagraph returns the first non-empty, non-heading paragraph of
// body (blank-line-separated blocks), with internal whitespace/newlines
// collapsed to single spaces, for use as a derived Description when the
// frontmatter omits one. A heading LINE (starts with "#") is never itself
// used as paragraph content — a heading is a title, not a description —
// but text sharing a heading's block with NO blank line between them
// (e.g. "# Heading\nText.", a common SKILL.md shape) is not thereby
// discarded: the heading line is stripped and the remainder of that same
// block is considered.
func firstParagraph(body string) string {
	blocks := strings.Split(body, "\n\n")
	for _, b := range blocks {
		trimmed := strings.TrimSpace(b)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			lines := strings.SplitN(trimmed, "\n", 2)
			if len(lines) < 2 {
				// The block is ONLY a heading line — nothing follows it
				// in this block at all.
				continue
			}
			trimmed = strings.TrimSpace(lines[1])
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
		}
		collapsed := strings.Join(strings.Fields(trimmed), " ")
		if collapsed != "" {
			return collapsed
		}
	}
	return ""
}

// extractPathRefs scans body for references to the scripts/, references/,
// and assets/ conventional directory prefixes and returns each bucket,
// deduplicated and sorted for deterministic output.
func extractPathRefs(body string) (scripts, references, assets []string) {
	scriptSet := map[string]struct{}{}
	refSet := map[string]struct{}{}
	assetSet := map[string]struct{}{}

	for _, raw := range pathRefPattern.FindAllString(body, -1) {
		// The regex's path-character class includes "." (needed for file
		// extensions like ".sh"/".md"/".png"), which also greedily
		// consumes a sentence-terminating period right after a path
		// mentioned at the end of a sentence (e.g. "...see assets/logo.png.").
		// Trim trailing prose punctuation that is never a legitimate
		// trailing character of a real path.
		m := strings.TrimRight(raw, ".,;:)]}")
		if m == "" {
			continue
		}
		switch {
		case strings.HasPrefix(m, "scripts/"):
			scriptSet[m] = struct{}{}
		case strings.HasPrefix(m, "references/"):
			refSet[m] = struct{}{}
		case strings.HasPrefix(m, "assets/"):
			assetSet[m] = struct{}{}
		}
	}
	return sortedKeys(scriptSet), sortedKeys(refSet), sortedKeys(assetSet)
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
