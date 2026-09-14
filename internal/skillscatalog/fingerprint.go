package skillscatalog

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// FingerprintFileName is the sidecar filename Verify/Generate persist the
// roster fingerprint to (DESIGN.md §4), relative to the catalog's outputDir.
// It is intentionally COMMITTED (not gitignored) once wired under the real
// docs/skills/ path (a later G126+ item) -- mirroring how the workable-items
// DB itself is tracked per §11.4.95 rather than gitignored -- since a fresh
// clone/session needs it to know, without a live DB connection, what the
// on-disk tree currently reflects.
const FingerprintFileName = ".catalog_fingerprint"

// fieldSep is a NUL byte used in exactly two narrow, still-safe spots that
// pre-date the F-A fix below: joining a skill's sorted Tags into ONE string
// before it is handed to writeTuple as a single opaque field
// (computeRosterFingerprint), and joining Description+Content before
// hashing them into contentHash (computeRosterFingerprint).
//
// The Description+Content pair is safe for the reason the pre-fix comment
// gave: sk.Description and sk.Content are real Postgres `text` column
// content (migrations/001_initial.up.sql), and Postgres `text`/`varchar`
// values can never contain an embedded raw NUL byte -- proven live against
// pgvector:pg16: `INSERT INTO t(x) VALUES (E'a\000b')` on a `text` column
// fails with "invalid byte sequence for encoding \"UTF8\": 0x00".
//
// The Tags-list use is safe for a DIFFERENT mechanism than the pre-fix
// comment claimed (R3-R1(a) review finding, round 4, 2026-07-16 -- WRONG
// DATA PATH: that comment attributed this to "real Postgres text/varchar
// column content", but a skill's Tags never come from a text/varchar
// column at all -- they are decoded out of the skills.metadata JSONB
// column (migrations/001_initial.up.sql:13; model.go's decodeMetadata) via
// json.Unmarshal). The safety CONCLUSION still holds, but by jsonb's own,
// separate NUL restriction: the JSON spec requires every control character
// below U+0020 -- including NUL -- to be represented in a JSON string only
// via an escape sequence ("\u0000" for NUL), and Postgres's jsonb parser
// categorically rejects that escape -- proven live against pgvector:pg16:
// `SELECT '{"a":"b\u0000c"}'::jsonb` fails with "unsupported Unicode escape
// sequence" / DETAIL "\u0000 cannot be converted to text." So a tag decoded
// from this column can never carry a raw NUL byte either, just via jsonb's
// rejection path rather than text/varchar's.
//
// Honest boundary (§11.4.6): this is a LOAD-BEARING invariant of the
// metadata column's CURRENT TYPE, not a universal property of "Postgres
// free text." If a future migration ever changed skills.metadata from
// `jsonb` to plain `json` -- which, unlike jsonb, stores its input text
// verbatim without the reparse/renormalize step that rejects "\u0000" --
// a raw NUL could then reach a decoded tag, and the exact F-A-class
// collision the writeTuple fix below closes at the tuple-encoding layer
// would reopen one level down inside a single Tags field: a tag list
// ["a\x00b"] and a tag list ["a", "b"] join to the identical byte string
// under strings.Join(tags, fieldSep) either way, because fieldSep and a
// smuggled-in raw NUL are the same byte. The metadata column staying
// jsonb (never json) is therefore a real dependency of this file's safety
// reasoning, not an accident of "Postgres text can't hold NUL" holding for
// every column type unconditionally.
//
// R3-R1(b) review finding (round 4, 2026-07-16): the pre-fix comment also
// claimed a NUL-joined PAIR or LIST of real field values is "itself
// unambiguous" -- FALSE for the LIST case, proven: strings.Join(nil,
// fieldSep) and strings.Join([]string{""}, fieldSep) both yield the empty
// string, so a skill with Tags=[] and a skill with Tags=[""] produce
// IDENTICAL fingerprint tuples -- a genuine collision between two
// distinct Tags values, not merely a hypothetical one. It is harmless
// ONLY because renderSkillDetail (render.go) renders those two Tags
// values byte-identically (`strings.Join` of zero escaped tags and of one
// escaped empty-string tag both yield "", so the rendered
// "- **Tags:** " line is the same either way) -- no catalog-visible drift
// results TODAY. That render-equivalence, not tuple "unambiguity", is the
// real load-bearing invariant; it was undocumented and unguarded before
// this round. It is now both documented here and permanently regression-
// guarded by TestSkillsCatalog_TagsFingerprintCollision_RenderEquivalence
// (generate_test.go), which fails RED the moment a future render.go edit
// ever makes the []/[""] Tags cases render differently (e.g. omitting the
// "- **Tags:**" line entirely when len(Tags)==0) while this fingerprint
// collision remains unfixed -- exactly the drift blind spot such an edit
// would otherwise open silently.
//
// fieldSep is NOT, and was PREVIOUSLY WRONGLY DOCUMENTED as, what makes the
// overall tuple ENCODING (writeTuple, below) injective (F-A review finding,
// round 3, 2026-07-16 -- PROVEN WRONG by a captured reviewer collision: a
// single crafted skill whose Tags list smuggled in extra NUL bytes -- one
// "free" NUL per additional tag element, courtesy of the SAME strings.Join
// this constant feeds -- reproduced, byte for byte, two entirely different
// real skills' tuples under the pre-fix delimiter+newline writeTuple).
// Injectivity ACROSS tuple/field boundaries is now the job of writeTuple's
// netstring length-prefixing alone, which does not depend on fieldSep, on
// NUL being special, or on any byte being forbidden in any field's content.
const fieldSep = "\x00"

// writeTuple appends fields to sb using netstring-style length-prefixing
// (D. J. Bernstein's netstring format: each field is written as its own
// decimal byte length, a ':' delimiter, then exactly that many literal
// bytes) -- the F-A review-finding fix, round 3, 2026-07-16.
//
// The record-type tag ("SKILL"/"DEP"/"RES", always fields[0] at every call
// site in computeRosterFingerprint/computeSidecarIdentity) is length-prefixed
// IDENTICALLY to every other field: no field, whatever bytes its content
// happens to contain -- NUL, "\n", ':', digits, another field's entire
// would-be encoding, anything -- can ever be misread as (part of) a
// neighbouring field or as a tuple/record boundary, because the length
// prefix states, up front, exactly how many bytes belong to that field; a
// reader (or, for our purposes, an injectivity proof) never needs to scan
// for a delimiter byte inside the field's own content at all.
//
// This replaces (and permanently closes) the PRE-FIX scheme -- fields
// joined by fieldSep's NUL byte, each tuple terminated by a bare "\n" --
// which was injective ONLY within the NUL-joined fields of a single
// writeTuple call (real Postgres text/varchar can never contain an embedded
// NUL, so THAT much held) but NOT across tuple/record boundaries: the "\n"
// tuple terminator was never NUL-guarded, and a real TEXT column CAN
// contain an embedded literal newline; worse, a skill's Tags array was
// flattened into ONE outer field via strings.Join(tags, fieldSep)
// (computeRosterFingerprint) -- so a skill with N tags silently contributed
// N-1 "free" fieldSep bytes, each indistinguishable, under the old scheme,
// from a genuine inter-field boundary anywhere ELSE in the stream. A single
// crafted skill exploiting exactly this (fabricated Tags entries + an
// embedded "\n") reproduced, byte for byte, the old encoding of TWO entirely
// different real skills -- proven, and permanently regression-guarded, by
// TestFingerprint_LengthPrefix_PreventsBoundaryForgedCollision
// (generate_test.go). Netstring length-prefixing removes the ambiguity at
// its root: with every field's exact byte length stated up front, no
// combination or regrouping of field CONTENT (however many list elements it
// hides, however it is split) can ever be re-parsed as a different sequence
// of fields.
//
// NOTE (§11.4.6 -- this fix changes the fingerprint's BYTE VALUE for every
// roster, including ones with no adversarial intent whatsoever): acceptable
// and expected here -- this package has no shipped production sidecar yet
// (doc.go), so there is no pre-existing on-disk fingerprint this change
// needs to stay compatible with. GeneratorVersion (model.go) is bumped
// alongside this fix for the same reason DESIGN.md ties it to
// computeSidecarIdentity: the fingerprint COMPUTATION itself changed, and
// GeneratorVersion is the user-visible (rendered in every page's Footer/
// README) provenance marker downstream readers can use to tell a pre-fix
// sidecar/footer apart from a post-fix one.
//
// The trailing "\n" this function still writes after every tuple is PURELY
// a human-legibility aid for anyone eyeballing the raw hash preimage in a
// debugger or log -- it carries no parsing or injectivity significance
// whatsoever (a literal "\n" is just one more ordinary byte a length-
// prefixed field is free to contain, inside or outside this trailing one)
// and is NEVER relied upon to delimit anything.
func writeTuple(sb *strings.Builder, fields ...string) {
	for _, f := range fields {
		sb.WriteString(strconv.Itoa(len(f)))
		sb.WriteByte(':')
		sb.WriteString(f)
	}
	sb.WriteByte('\n')
}

// computeRosterFingerprint implements the roster definition of DESIGN.md §4
// (as corrected by the F1 review finding, 2026-07-16, and further extended
// by the F2 review finding, round 2, 2026-07-16 -- see the Title/ID notes
// below): for every skill (already sorted by Name by the caller, load.go's
// loadRoster), its catalog-visible tuple
// (id/name/TITLE/version/kind/status/domain/complexity/sorted-tags/content-hash)
// followed by its dependency edges (grouped+sorted by canonical relation
// type, then DependsOnName) and its resources (sorted by URL), all
// concatenated and hashed once.
//
// Title is INCLUDED in this tuple (F1 fix): Title is real, rendered content
// -- it appears verbatim in every by-domain/by-kind grouping table
// (render.go's renderGroupingPage) and on the skill's own detail-page header
// (render.go's renderSkillDetail) -- so a Title-only edit is exactly as
// catalog-VISIBLE as a Description/Content edit and MUST re-arm the drift
// detector; omitting it (the pre-fix behaviour) made Verify/Generate report
// false "in sync" after a Title-only mutation while the rendered page still
// showed the stale Title (captured evidence: TestSkillsCatalog_FingerprintDrift_TitleOnlyChange_Detected).
//
// sk.ID is ALSO included (F2 fix, round 2, 2026-07-16): render.go's
// renderSkillDetail embeds sk.ID verbatim into the excerpt-mode
// (cfg.EmbedFullContent=false) export-URL text ("use ... or `GET
// /skills/%s/export` ..."), so it is catalog-VISIBLE rendered content
// exactly like Title -- a skill dropped and re-created under the identical
// Name/Title/Version/Description/Content/Metadata but a BRAND-NEW UUID
// (e.g. via an external delete+re-import cycle) changes that rendered
// export URL while every OTHER fingerprinted field stays byte-identical.
// Omitting ID left that recreation invisible to Verify/Generate, pointing
// the stale rendered export URL at a UUID that no longer resolves
// (captured evidence: TestSkillsCatalog_FingerprintDrift_IDChange_SameContent_Detected).
//
// created_at/updated_at are DELIBERATELY EXCLUDED from this hash input
// (DESIGN.md §3.2) -- unlike Title/ID, they are NOT rendered anywhere in
// this generator's output (F1 remediation, round 2, 2026-07-16 --
// render.go's renderSkillDetail Footer no longer embeds them either, for
// the identical reason: a churn field must be excluded from BOTH the
// fingerprint AND the rendered output, never just one). Including them here
// would make the fingerprint change on every idempotent re-import touch
// even when nothing catalog-VISIBLE changed.
func computeRosterFingerprint(records []skillRecord) string {
	var sb strings.Builder
	for _, r := range records {
		sk := r.Skill
		tags := append([]string(nil), r.Metadata.Tags...)
		sort.Strings(tags)

		// A NUL separator between description and content avoids the
		// (admittedly unlikely at this project's scale, but real) ambiguity
		// of plain concatenation: description="ab"+content="c" would
		// otherwise hash identically to description="a"+content="bc".
		contentHash := sha256Hex(sk.Description + fieldSep + sk.Content)

		writeTuple(&sb, "SKILL",
			sk.ID.String(), sk.Name, sk.Title, sk.Version, string(sk.Kind), string(sk.Status),
			r.Metadata.Domain, r.Metadata.Complexity,
			strings.Join(tags, fieldSep), contentHash)

		for _, relType := range canonicalRelationOrder {
			for _, dep := range r.DepsByType[relType] {
				writeTuple(&sb, "DEP", string(relType), dep.DependsOnName,
					strconv.FormatBool(dep.Optional), sortOrderString(dep.SortOrder))
			}
		}
		for _, res := range r.Resources {
			writeTuple(&sb, "RES", res.URL, res.Title, res.ResourceType)
		}
	}
	return sha256Hex(sb.String())
}

// configDigest returns a short, stable string identifying every
// output-AFFECTING Config field (F6 review finding, round 2, 2026-07-16).
// Currently just EmbedFullContent, whose two values produce STRUCTURALLY
// different Content sections (render.go's renderSkillDetail -- full body
// verbatim vs. a truncated excerpt + an export-URL pointer). cfg.Force is
// DELIBERATELY EXCLUDED: it is a behavioural flag controlling WHETHER the
// short-circuit is bypassed, never WHAT gets written -- Force=true and
// Force=false produce byte-IDENTICAL trees for the same roster+EmbedFullContent
// (this package's own Determinism_ByteStableAcrossRepeatedRuns test proves
// exactly that), so folding it into this digest would needlessly force a
// rewrite every time Force flips without any actual output difference.
func configDigest(cfg Config) string {
	return "embed=" + strconv.FormatBool(cfg.EmbedFullContent)
}

// computeSidecarIdentity is the composite identity persisted to (and
// compared against) the fingerprint sidecar -- distinct from, and a strict
// superset of, computeRosterFingerprint's pure roster-CONTENT hash (F6
// review finding, round 2, 2026-07-16). It combines three independent
// components, any ONE of which changing means "the tree Generate would
// write right now differs from what is currently on disk":
//
//  1. GeneratorVersion (model.go) -- bumped when the rendering CONTRACT
//     itself changes (file layout, section shape, banner text).
//  2. configDigest(cfg) -- the output-affecting Config fields for THIS
//     call.
//  3. rosterFingerprint -- the roster's catalog-visible CONTENT hash
//     (computeRosterFingerprint).
//
// Keying the short-circuit/drift-check purely off the roster hash (the
// pre-fix behaviour) missed components 1 and 2 entirely: toggling
// cfg.EmbedFullContent on an UNCHANGED roster left the OLD excerpt-mode (or
// full-content-mode) tree on disk while Generate/Verify silently reported
// "already up to date" (captured evidence:
// TestSkillsCatalog_ConfigChurn_EmbedFullContentToggle_ForcesRegeneration).
//
// This composite value is used ONLY for the internal short-circuit/drift
// decision and the on-disk sidecar file -- it is NEVER what README.md's
// "Roster fingerprint:" line or the per-skill Footer's "roster fingerprint"
// reference display; those keep showing the pure rosterFingerprint
// (generate.go's Generate/Verify both return that, not this), so the
// user-visible "roster fingerprint" label keeps its honest, narrower
// meaning (purely about roster content, unaffected by generator version or
// config) even though the sidecar file on disk now stores this broader
// identity.
func computeSidecarIdentity(cfg Config, rosterFingerprint string) string {
	var sb strings.Builder
	writeTuple(&sb, "IDENTITY", GeneratorVersion, configDigest(cfg), rosterFingerprint)
	return sha256Hex(sb.String())
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func sortOrderString(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}

// readFingerprintSidecar reads the persisted fingerprint from outputDir, or
// returns ("", false, nil) when it does not exist yet (a brand-new
// outputDir -- honestly "not in sync", never an error).
func readFingerprintSidecar(outputDir string) (fingerprint string, exists bool, err error) {
	data, err := os.ReadFile(filepath.Join(outputDir, FingerprintFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read fingerprint sidecar: %w", err)
	}
	return strings.TrimSpace(string(data)), true, nil
}

func writeFingerprintSidecar(outputDir, fingerprint string) error {
	if err := os.WriteFile(filepath.Join(outputDir, FingerprintFileName), []byte(fingerprint+"\n"), 0o644); err != nil {
		return fmt.Errorf("write fingerprint sidecar: %w", err)
	}
	return nil
}
