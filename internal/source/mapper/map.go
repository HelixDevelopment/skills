// Package mapper turns a parsed SKILL.md (internal/source/skillmd) into an
// in-memory models.Skill value, applying the namespacing and license-gate
// rules the GitHub-skills-ingestion feature requires (see
// docs/source_ingestion/WIRING_PLAN.md §3.4 /
// docs/source_ingestion/TRACKED_ITEMS.md G61 for the design this
// implements).
//
// Map is a pure function: it never touches a database, the network, or a
// Store. Persistence, deduplication against existing skills, and
// dependency-graph wiring are all later, separate integration steps (the
// orchestrator/import items this package does not implement) — Map's
// entire job is producing a correct in-memory models.Skill plus the
// provenance a future importer needs.
package mapper

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/HelixDevelopment/skills/internal/source/skillmd"
	"github.com/google/uuid"
)

// Result is Map's output: the constructed models.Skill plus the
// provenance fields a future importer needs to record a source→skill
// mapping row (e.g. the not-yet-implemented skill_source_mappings table
// per docs/source_ingestion/WIRING_PLAN.md §3.7). Result has no
// persistence behavior of its own — it is purely an in-memory hand-off
// structure.
type Result struct {
	// Skill is the mapped, ready-to-import skill. Its ID is freshly
	// generated (mirrors internal/skill.Store.ImportFromTOML's own
	// precedent of assigning uuid.New() at construction time, not
	// deferring ID assignment to the store-insert layer).
	Skill *models.Skill

	// LicenseSkipped is true when parsed.License was not present in the
	// caller's licenseAllowlist, in which case Skill.Content is a stub
	// (never the real upstream body) and Skill.Resources carries a
	// pointer back to the upstream source instead.
	LicenseSkipped bool

	// ContentHash is copied from the source ParsedSkill unchanged — the
	// change-detection value a future re-scan compares against a
	// previously-recorded hash.
	ContentHash string

	// SourcePath is copied from the source ParsedSkill unchanged.
	SourcePath string

	// UpstreamName is the parsed skill's original (non-namespaced) name,
	// e.g. "systematic-debugging" before the sourceSlug prefix was added.
	UpstreamName string

	// UpstreamLicense is the parsed skill's original license string
	// (may be empty), preserved regardless of whether it passed the
	// allowlist gate.
	UpstreamLicense string
}

// licenseStubTemplate is the content a license-gated skill's Content
// field is replaced with. It deliberately never includes any of the
// upstream body text.
const licenseStubTemplate = "This skill's content is not redistributed here " +
	"because its upstream license (%s) is not on the configured allowlist. " +
	"See the upstream source for the full skill definition.%s"

// ErrMetadataTooLarge is returned by Map when a parsed skill's frontmatter
// would, if marshaled to JSON, exceed maxMetadataEstimatedBytes. This is
// the primary defense against a YAML alias-expansion ("billion laughs")
// resource-exhaustion attack (§11.4.85): skillmd.Parse's own byte-size
// cap on the RAW frontmatter text (skillmd.ErrFrontmatterTooLarge) cannot
// bound this on its own, because a handful of YAML anchors/aliases fits
// in well under 1 KiB of source text yet references the SAME
// already-decoded subtree exponentially many times — goccy's decoder
// stores that subtree as a single SHARED Go value (map/slice), so
// skillmd.Parse itself stays cheap (measured: ~0.3ms even for a
// 469-byte, 7-level alias-chain fixture), but encoding/json.Marshal has
// no notion of that sharing: it walks the decoded value naively,
// re-serializing the shared subtree in full at every reference, which is
// exactly where the exponential blow-up in bytes/allocations actually
// happens (measured: the same 469-byte fixture marshals to ~4.8 MB in
// ~47ms; two more alias levels on a 604-byte fixture exhausts multiple
// GB and crashes the process during json.Marshal's internal buffer
// growth — captured during this remediation's own verification, never
// merely assumed per §11.4.6).
//
// estimateMarshaledSize (below) closes this by walking the SAME decoded
// value BEFORE Map ever calls json.Marshal on it, using the exact same
// naive (sharing-unaware) traversal encoding/json would perform — but
// checking the running total against the cap at EVERY node, and
// returning ErrMetadataTooLarge THE MOMENT that total is exceeded, rather
// than after computing an exact result. That early-abort-on-every-node
// property is what bounds the estimator's own worst-case work to
// O(maxMetadataEstimatedBytes), independent of how astronomically large
// the input's true (uncapped) expansion would be — verified during this
// remediation against alias chains up to 30 levels deep (a branching
// factor far beyond anything a real SKILL.md would ever need), each
// rejected in well under 1ms.
var ErrMetadataTooLarge = errors.New("mapper: frontmatter metadata's estimated marshaled size exceeds the safety cap (possible YAML alias-expansion pattern)")

// maxMetadataEstimatedBytes bounds estimateMarshaledSize's running total
// before Map refuses to proceed to json.Marshal. Real SKILL.md
// frontmatter (name/description/license/version/tags/allowed-tools/...)
// marshals to at most a few KiB, so this cap is extremely generous for
// any legitimate input (three orders of magnitude of headroom) while
// still keeping the estimator's own worst-case work small and bounded
// for an adversarial one.
const maxMetadataEstimatedBytes = 1 * 1024 * 1024 // 1 MiB

// estimateMarshaledSize walks v — a value goccy's YAML decoder can
// produce when unmarshaling into map[string]interface{} (nil, string,
// bool, a numeric type, a nested map[string]interface{}, or a nested
// []interface{}) — accumulating into *used an estimate of what
// encoding/json.Marshal(v) would eventually produce, WITHOUT ever
// constructing that output. See ErrMetadataTooLarge's doc comment for
// why this is the load-bearing defense against a YAML alias-expansion
// attack: this function performs the SAME naive, sharing-unaware
// traversal json.Marshal performs (a shared subtree reached via more
// than one YAML alias is walked once per reference, exactly as
// json.Marshal would re-serialize it once per reference), but it checks
// *used against budget at the START of every call and at every
// iteration step BEFORE recursing further, so it aborts as soon as the
// running total exceeds budget rather than continuing to the true
// (potentially astronomically larger) total. Every branch that can
// recurse (map, slice) is guarded this way; every branch that cannot
// (string and the default case for any other scalar type —
// bool/int/float64/uint64/time.Time/nil/anything else the decoder might
// produce) is a leaf with no further children to walk, so there is no
// expansion path this function fails to bound: a value goccy can
// produce is either one of the two recursive container kinds this
// function walks explicitly, or a terminal scalar this function charges
// a flat cost and never recurses into — there is no third case that
// could hide further nested aliasing. (The parameter is named budget,
// not cap, to avoid shadowing the built-in cap() function.)
func estimateMarshaledSize(v interface{}, used *int64, budget int64) error {
	if *used > budget {
		return ErrMetadataTooLarge
	}
	switch t := v.(type) {
	case nil:
		*used += 4 // `null`
	case string:
		*used += int64(len(t)) + 2 // quotes (escaping is ignored; undercounting here is conservative-unsafe only in the sense that it makes the budget slightly MORE permissive, never less — see the doc comment's "estimate" framing)
	case map[string]interface{}:
		for k, vv := range t {
			*used += int64(len(k)) + 3 // quoted key + colon
			if *used > budget {
				return ErrMetadataTooLarge
			}
			if err := estimateMarshaledSize(vv, used, budget); err != nil {
				return err
			}
		}
	case []interface{}:
		for _, vv := range t {
			if *used > budget {
				return ErrMetadataTooLarge
			}
			if err := estimateMarshaledSize(vv, used, budget); err != nil {
				return err
			}
		}
	default:
		// bool / int / int64 / uint64 / float64 / time.Time / any other
		// scalar goccy's decoder may produce: a flat, bounded cost, and
		// (per the doc comment above) never a value with further
		// children to recurse into.
		*used += 16
	}
	return nil
}

// Map builds an in-memory models.Skill from parsed, namespaced under
// sourceSlug so the result can never collide with an existing skill
// sharing the same base name (skills.name is UNIQUE — namespacing is the
// collision-avoidance mechanism, per docs/source_ingestion/WIRING_PLAN.md
// §3.4 "D1").
//
// licenseAllowlist is matched case-insensitively against parsed.License;
// an empty parsed.License (unknown/undeclared upstream license) is always
// treated as NOT allowed — Map never assumes an undeclared license is
// safe to redistribute (§11.4.122). sourcePermalink (may be empty) is
// used only to build a "see upstream" Resource entry when the license
// gate skips the real content; it is never validated as a URL by this
// package.
//
// Map returns an error for caller-programming-errors (nil parsed, empty
// sourceSlug, or a parsed value whose Name is empty — which should never
// happen for a value that came out of skillmd.Parse, since Parse itself
// rejects an empty name) and for a frontmatter whose estimated marshaled
// size exceeds maxMetadataEstimatedBytes (ErrMetadataTooLarge — see its
// doc comment; a YAML alias-expansion pattern can produce a tiny
// RawFrontmatter Go value that would still marshal to gigabytes). It
// never returns an error purely because of a disallowed license — that
// case is the LicenseSkipped=true path, not a failure.
func Map(parsed *skillmd.ParsedSkill, sourceSlug string, licenseAllowlist []string, sourcePermalink string) (*Result, error) {
	if parsed == nil {
		return nil, fmt.Errorf("mapper: Map requires a non-nil ParsedSkill")
	}
	sourceSlug = strings.TrimSpace(sourceSlug)
	if sourceSlug == "" {
		return nil, fmt.Errorf("mapper: Map requires a non-empty sourceSlug")
	}
	if parsed.Name == "" {
		return nil, fmt.Errorf("mapper: Map requires parsed.Name to be non-empty (an upstream ParsedSkill from skillmd.Parse should never have an empty Name)")
	}

	// Bound the frontmatter's estimated marshaled size BEFORE ever
	// calling json.Marshal on it — see ErrMetadataTooLarge's doc
	// comment. This MUST run before json.Marshal, not after checking
	// its output length: json.Marshal itself is where an exponential
	// YAML alias-expansion pattern actually detonates (it has already
	// paid the full allocation cost by the time it returns), so
	// checking length post-marshal would not "refuse rather than
	// allocate" — it would refuse only AFTER allocating.
	var estimatedSize int64
	if err := estimateMarshaledSize(parsed.RawFrontmatter, &estimatedSize, maxMetadataEstimatedBytes); err != nil {
		return nil, fmt.Errorf("mapper: frontmatter metadata for %s: %w", parsed.SourcePath, err)
	}

	metadata, err := json.Marshal(parsed.RawFrontmatter)
	if err != nil {
		return nil, fmt.Errorf("mapper: marshal frontmatter metadata for %s: %w", parsed.SourcePath, err)
	}

	allowed := licenseAllowed(parsed.License, licenseAllowlist)
	content := parsed.Body
	var resources []models.Resource
	licenseSkipped := false
	if !allowed {
		licenseSkipped = true
		content = fmt.Sprintf(licenseStubTemplate, displayLicense(parsed.License), permalinkSuffix(sourcePermalink))
		if sourcePermalink != "" {
			resources = append(resources, models.Resource{
				URL:          sourcePermalink,
				Title:        "Upstream SKILL.md (license-gated, content not redistributed)",
				ResourceType: "upstream_source",
			})
		}
	}

	sk := &models.Skill{
		ID:          uuid.New(),
		Name:        sourceSlug + "." + parsed.Name,
		Version:     versionOrDefault(parsed.RawFrontmatter),
		Title:       titleize(parsed.Name),
		Description: parsed.Description,
		Content:     content,
		Metadata:    json.RawMessage(metadata),
		Status:      models.SkillStatusDraft,
		Kind:        models.SkillKindAtomic,
		Resources:   resources,
	}

	return &Result{
		Skill:           sk,
		LicenseSkipped:  licenseSkipped,
		ContentHash:     parsed.ContentHash,
		SourcePath:      parsed.SourcePath,
		UpstreamName:    parsed.Name,
		UpstreamLicense: parsed.License,
	}, nil
}

// licenseAllowed reports whether license case-insensitively matches one
// of allowlist's entries. An empty license (undeclared upstream license)
// always returns false — Map never assumes an undeclared license permits
// redistribution.
func licenseAllowed(license string, allowlist []string) bool {
	license = strings.TrimSpace(license)
	if license == "" {
		return false
	}
	for _, a := range allowlist {
		if strings.EqualFold(strings.TrimSpace(a), license) {
			return true
		}
	}
	return false
}

// defaultSkillVersion is used when the upstream SKILL.md's frontmatter
// declares no "version" field (or a non-string one) — the open-standard
// minimum (docs/source_ingestion/CATALOG.md §2) requires only
// name+description, so a mapped skill still needs SOME version string.
const defaultSkillVersion = "1.0.0"

// versionOrDefault returns fm's "version" frontmatter field, trimmed, when
// it is present and a non-empty string — e.g. the jeremylongshore-style
// eight-field superset's `version: "1.2.3"` — or defaultSkillVersion
// otherwise. A non-string "version" value (an unexpected YAML type) is
// treated the same as an absent one, mirroring skillmd's own lenient
// stringField convention rather than erroring the whole mapping.
func versionOrDefault(fm map[string]interface{}) string {
	if v, ok := fm["version"]; ok {
		if s, ok := v.(string); ok {
			if trimmed := strings.TrimSpace(s); trimmed != "" {
				return trimmed
			}
		}
	}
	return defaultSkillVersion
}

// displayLicense returns license, or a human-readable placeholder when
// it is empty (undeclared upstream license).
func displayLicense(license string) string {
	if strings.TrimSpace(license) == "" {
		return "undeclared"
	}
	return license
}

// permalinkSuffix returns a trailing " (<permalink>)" fragment for the
// license stub, or "" when no permalink is available.
func permalinkSuffix(permalink string) string {
	if permalink == "" {
		return ""
	}
	return " (" + permalink + ")"
}

// titleize turns a kebab-case skill name (e.g. "systematic-debugging")
// into a human-readable title ("Systematic Debugging"). It is a simple,
// dependency-free heuristic — a frontmatter that already supplies a
// richer display form has no first-class field for it per
// docs/source_ingestion/CATALOG.md §2, so this is a best-effort
// derivation, not an authoritative title source.
func titleize(name string) string {
	// strings.FieldsFunc never includes an empty string in its result —
	// it splits at runs of separator runes and omits the empty fields
	// between them — so every parts[i] here is guaranteed non-empty and
	// runes[0] is always a safe index; a `p == ""` guard would be dead
	// code, never reachable.
	parts := strings.FieldsFunc(name, func(r rune) bool {
		return r == '-' || r == '_'
	})
	for i, p := range parts {
		runes := []rune(p)
		runes[0] = toUpperRune(runes[0])
		parts[i] = string(runes)
	}
	return strings.Join(parts, " ")
}

func toUpperRune(r rune) rune {
	if r >= 'a' && r <= 'z' {
		return r - ('a' - 'A')
	}
	return r
}
