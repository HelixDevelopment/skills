package skillmd

import (
	"errors"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Golden-good fixtures
// ---------------------------------------------------------------------------

func TestParse_TwoFieldMinimum(t *testing.T) {
	// The open-standard floor per docs/source_ingestion/CATALOG.md §2:
	// name+description only.
	const raw = "---\n" +
		"name: systematic-debugging\n" +
		"description: Debug things systematically.\n" +
		"---\n" +
		"# Systematic Debugging\n\n" +
		"Body text.\n"

	ps, err := Parse([]byte(raw), "systematic-debugging/SKILL.md")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ps.Name != "systematic-debugging" {
		t.Errorf("Name = %q, want %q", ps.Name, "systematic-debugging")
	}
	if ps.Description != "Debug things systematically." {
		t.Errorf("Description = %q", ps.Description)
	}
	if ps.License != "" {
		t.Errorf("License = %q, want empty", ps.License)
	}
	if ps.SourcePath != "systematic-debugging/SKILL.md" {
		t.Errorf("SourcePath = %q", ps.SourcePath)
	}
	if ps.ContentHash == "" {
		t.Error("ContentHash is empty")
	}
	if len(ps.ContentHash) != 64 { // sha256 hex
		t.Errorf("ContentHash length = %d, want 64", len(ps.ContentHash))
	}
}

func TestParse_EightFieldSuperset(t *testing.T) {
	// jeremylongshore-style superset per docs/source_ingestion/CATALOG.md
	// §2: name, description, allowed-tools, version, author, license,
	// compatibility, tags.
	const raw = "---\n" +
		"name: bulk-refactor\n" +
		"description: Refactor a codebase in bulk.\n" +
		"allowed-tools:\n" +
		"  - Read\n" +
		"  - Edit\n" +
		"version: \"1.2.3\"\n" +
		"author: jeremylongshore\n" +
		"license: MIT\n" +
		"compatibility: [\"claude-code\", \"cursor\"]\n" +
		"tags:\n" +
		"  - refactor\n" +
		"  - bulk\n" +
		"---\n" +
		"Body of the bulk-refactor skill.\n"

	ps, err := Parse([]byte(raw), "bulk-refactor/SKILL.md")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ps.Name != "bulk-refactor" {
		t.Errorf("Name = %q", ps.Name)
	}
	if ps.License != "MIT" {
		t.Errorf("License = %q, want MIT", ps.License)
	}
	wantKeys := []string{"name", "description", "allowed-tools", "version", "author", "license", "compatibility", "tags"}
	for _, k := range wantKeys {
		if _, ok := ps.RawFrontmatter[k]; !ok {
			t.Errorf("RawFrontmatter missing key %q; got keys %v", k, mapKeys(ps.RawFrontmatter))
		}
	}
}

func TestParse_RawFrontmatterRoundTrips_KeySetLossless(t *testing.T) {
	// Paired §1.1-style regression guard for the "lossless unknown-field
	// capture" contract: every frontmatter key present in the source MUST
	// survive into RawFrontmatter, including keys this parser has no
	// first-class field for. A mutation that dropped unknown keys (e.g.
	// only copying name/description/license into a fresh map instead of
	// keeping the full decoded map) would make this test fail — see the
	// mutate-and-confirm-RED note in the implementer's report.
	const raw = "---\n" +
		"name: n\n" +
		"description: d\n" +
		"license: Apache-2.0\n" +
		"disable-model-invocation: true\n" +
		"disallowed-tools: [Bash]\n" +
		"context: fork\n" +
		"arguments: [issue, branch]\n" +
		"user-invocable: false\n" +
		"custom-marketplace-field: something-unknown\n" +
		"---\n" +
		"Body.\n"

	ps, err := Parse([]byte(raw), "p/SKILL.md")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	wantKeys := []string{
		"name", "description", "license", "disable-model-invocation",
		"disallowed-tools", "context", "arguments", "user-invocable",
		"custom-marketplace-field",
	}
	gotKeys := mapKeys(ps.RawFrontmatter)
	sort.Strings(wantKeys)
	sort.Strings(gotKeys)
	if len(gotKeys) != len(wantKeys) {
		t.Fatalf("RawFrontmatter key count = %d, want %d\n got: %v\nwant: %v", len(gotKeys), len(wantKeys), gotKeys, wantKeys)
	}
	for i := range wantKeys {
		if gotKeys[i] != wantKeys[i] {
			t.Fatalf("RawFrontmatter key set mismatch:\n got: %v\nwant: %v", gotKeys, wantKeys)
		}
	}
}

func TestParse_MissingDescription_DerivedFromFirstParagraph(t *testing.T) {
	const raw = "---\n" +
		"name: no-desc-skill\n" +
		"---\n" +
		"# Heading Line\n\n" +
		"This is the first real paragraph that should become the description.\n\n" +
		"A second paragraph that must NOT be used.\n"

	ps, err := Parse([]byte(raw), "p/SKILL.md")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := "This is the first real paragraph that should become the description."
	if ps.Description != want {
		t.Errorf("Description = %q, want %q", ps.Description, want)
	}
}

func TestParse_CRLFLineEndings(t *testing.T) {
	raw := "---\r\n" +
		"name: crlf-skill\r\n" +
		"description: Uses CRLF endings.\r\n" +
		"---\r\n" +
		"Body with\r\nCRLF newlines.\r\n"

	ps, err := Parse([]byte(raw), "p/SKILL.md")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ps.Name != "crlf-skill" {
		t.Errorf("Name = %q", ps.Name)
	}
	if strings.Contains(ps.Body, "\r") {
		t.Errorf("Body still contains CR bytes: %q", ps.Body)
	}
}

func TestParse_UnicodeContent(t *testing.T) {
	const raw = "---\n" +
		"name: unicode-skill\n" +
		"description: \"Handles émoji 🎉 and 中文 correctly\"\n" +
		"---\n" +
		"Body with ünïcödé and 日本語 content.\n"

	ps, err := Parse([]byte(raw), "p/SKILL.md")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ps.Description != "Handles émoji 🎉 and 中文 correctly" {
		t.Errorf("Description = %q", ps.Description)
	}
	if !strings.Contains(ps.Body, "日本語") {
		t.Errorf("Body lost unicode content: %q", ps.Body)
	}
}

func TestParse_ScriptsReferencesAssetsExtracted(t *testing.T) {
	const raw = "---\n" +
		"name: path-refs\n" +
		"description: d\n" +
		"---\n" +
		"Run `scripts/setup.sh` first, then read references/guide.md and " +
		"see assets/logo.png. Also scripts/setup.sh again (duplicate).\n"

	ps, err := Parse([]byte(raw), "p/SKILL.md")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(ps.Scripts) != 1 || ps.Scripts[0] != "scripts/setup.sh" {
		t.Errorf("Scripts = %v, want [scripts/setup.sh] (deduplicated)", ps.Scripts)
	}
	if len(ps.References) != 1 || ps.References[0] != "references/guide.md" {
		t.Errorf("References = %v", ps.References)
	}
	if len(ps.Assets) != 1 || ps.Assets[0] != "assets/logo.png" {
		t.Errorf("Assets = %v", ps.Assets)
	}
}

// TestParse_MissingDescription_HeadingAndTextSameBlock is the W3
// remediation test: firstParagraph must derive a description from a
// heading immediately followed by body text with NO blank line between
// them (a single "\n\n"-delimited block) — a common SKILL.md shape the
// prior implementation silently discarded entirely because the whole
// block started with "#".
func TestParse_MissingDescription_HeadingAndTextSameBlock(t *testing.T) {
	const raw = "---\n" +
		"name: no-blank-line-skill\n" +
		"---\n" +
		"# Heading\n" +
		"Text that should become the description.\n"

	ps, err := Parse([]byte(raw), "p/SKILL.md")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := "Text that should become the description."
	if ps.Description != want {
		t.Errorf("Description = %q, want %q", ps.Description, want)
	}
}

// TestParse_ContentHash_LicenseOnlyChange_FlipsHash is the F2 remediation
// test: ContentHash is documented as the change-detection value a re-scan
// compares to decide whether re-mapping is needed, and the mapper's
// license-gate decision depends on License — so a license-only change
// (e.g. "MIT" -> "proprietary") with an otherwise byte-identical body
// MUST change ContentHash. Before this fix, ContentHash excluded License
// entirely, so this exact scenario produced an IDENTICAL hash and a
// hash-gated re-scan would have skipped re-mapping, keeping gated content
// redistributed under a stale ALLOW decision.
func TestParse_ContentHash_LicenseOnlyChange_FlipsHash(t *testing.T) {
	const rawMIT = "---\n" +
		"name: n\n" +
		"description: d\n" +
		"license: MIT\n" +
		"---\n" +
		"Identical body text.\n"
	const rawProprietary = "---\n" +
		"name: n\n" +
		"description: d\n" +
		"license: proprietary\n" +
		"---\n" +
		"Identical body text.\n"

	psMIT, err := Parse([]byte(rawMIT), "p/SKILL.md")
	if err != nil {
		t.Fatalf("Parse (MIT): %v", err)
	}
	psProprietary, err := Parse([]byte(rawProprietary), "p/SKILL.md")
	if err != nil {
		t.Fatalf("Parse (proprietary): %v", err)
	}
	if psMIT.Name != psProprietary.Name || strings.TrimSpace(psMIT.Body) != strings.TrimSpace(psProprietary.Body) {
		t.Fatalf("test fixture bug: Name/Body must be identical across the two variants (only License differs)")
	}
	if psMIT.ContentHash == psProprietary.ContentHash {
		t.Fatalf("ContentHash unchanged across a license-only flip (MIT -> proprietary), got %q for both — "+
			"a hash-gated re-scan would skip re-mapping and keep redistributing gated content under a stale ALLOW decision", psMIT.ContentHash)
	}
}

// TestParse_ContentHash_DescriptionOnlyChange_FlipsHash is the N1
// remediation test: ContentHash previously covered only
// Name + License + Body, so a description-only frontmatter edit produced
// an IDENTICAL hash even though the mapper materializes Description
// directly into models.Skill.Description — a hash-gated re-scan would
// have silently kept serving the stale description forever. ContentHash
// now covers the full raw file (frontmatter + body), so it MUST flip on
// a description-only change.
//
// Mutation-proof: reverting ContentHash to the old
// name+"\x00"+license+"\x00"+normalizedBody formula (which never reads
// the description at all) makes this test fail (RED) — psA.ContentHash
// == psB.ContentHash since Name/License/Body are identical between the
// two fixtures; restoring the full-file-hash fix makes it pass (GREEN).
func TestParse_ContentHash_DescriptionOnlyChange_FlipsHash(t *testing.T) {
	const rawA = "---\n" +
		"name: n\n" +
		"description: Original description.\n" +
		"license: MIT\n" +
		"---\n" +
		"Identical body text.\n"
	const rawB = "---\n" +
		"name: n\n" +
		"description: Updated description that differs.\n" +
		"license: MIT\n" +
		"---\n" +
		"Identical body text.\n"

	psA, err := Parse([]byte(rawA), "p/SKILL.md")
	if err != nil {
		t.Fatalf("Parse (A): %v", err)
	}
	psB, err := Parse([]byte(rawB), "p/SKILL.md")
	if err != nil {
		t.Fatalf("Parse (B): %v", err)
	}
	if psA.Name != psB.Name || psA.License != psB.License || strings.TrimSpace(psA.Body) != strings.TrimSpace(psB.Body) {
		t.Fatalf("test fixture bug: Name/License/Body must be identical across the two variants (only Description differs)")
	}
	if psA.Description == psB.Description {
		t.Fatalf("test fixture bug: Description must actually differ between the two variants")
	}
	if psA.ContentHash == psB.ContentHash {
		t.Fatalf("ContentHash unchanged across a description-only edit, got %q for both — "+
			"a hash-gated re-scan would skip re-mapping and keep serving the stale description forever", psA.ContentHash)
	}
}

// TestParse_ContentHash_VersionOnlyChange_FlipsHash is the N1 remediation
// test's version-field analogue: the mapper (internal/source/mapper.Map)
// materializes the frontmatter "version" field directly into
// models.Skill.Version (mapper.versionOrDefault), but the old
// Name+License+Body formula never read "version" at all — a
// version-only upstream bump produced an IDENTICAL ContentHash.
func TestParse_ContentHash_VersionOnlyChange_FlipsHash(t *testing.T) {
	const rawA = "---\n" +
		"name: n\n" +
		"description: d\n" +
		"license: MIT\n" +
		"version: \"1.0.0\"\n" +
		"---\n" +
		"Identical body text.\n"
	const rawB = "---\n" +
		"name: n\n" +
		"description: d\n" +
		"license: MIT\n" +
		"version: \"2.0.0\"\n" +
		"---\n" +
		"Identical body text.\n"

	psA, err := Parse([]byte(rawA), "p/SKILL.md")
	if err != nil {
		t.Fatalf("Parse (A): %v", err)
	}
	psB, err := Parse([]byte(rawB), "p/SKILL.md")
	if err != nil {
		t.Fatalf("Parse (B): %v", err)
	}
	if psA.Name != psB.Name || psA.License != psB.License || strings.TrimSpace(psA.Body) != strings.TrimSpace(psB.Body) {
		t.Fatalf("test fixture bug: Name/License/Body must be identical across the two variants (only version differs)")
	}
	if psA.ContentHash == psB.ContentHash {
		t.Fatalf("ContentHash unchanged across a version-only edit, got %q for both — "+
			"a hash-gated re-scan would skip re-mapping a file whose mapped Version actually changed", psA.ContentHash)
	}
}

// TestParse_ContentHash_ForgeableSeparatorCollision_NowDistinct is the N3
// remediation test: the old formula
// sha256(name + "\x00" + license + "\x00" + strings.TrimSpace(body))
// concatenated three fields with a "\x00" join byte, which is forgeable —
// a License value ending in an embedded NUL (a real, valid YAML escape:
// `"MIT\0"` decodes to the 4-byte string "MIT\x00") can absorb the join
// byte and produce a BYTE-IDENTICAL concatenation to a second file whose
// License and Body differ:
//
//	fixture A: license="MIT\x00" (decoded), body="X"
//	  old formula bytes: "n" + \x00 + "MIT\x00" + \x00 + "X" = n\x00MIT\x00\x00X
//	fixture B: license="MIT",     body="\x00X"
//	  old formula bytes: "n" + \x00 + "MIT"     + \x00 + "\x00X" = n\x00MIT\x00\x00X
//
// identical bytes, identical hash, despite License and Body both
// genuinely differing between A and B. Hashing the full raw file (this
// fix) removes the join entirely, so A's and B's ContentHash now differ.
//
// Mutation-proof: reverting to the old formula makes this test fail
// (RED) — psA.ContentHash == psB.ContentHash reproduces exactly the
// collision above; the full-file-hash fix makes it pass (GREEN).
func TestParse_ContentHash_ForgeableSeparatorCollision_NowDistinct(t *testing.T) {
	rawA := "---\n" +
		"name: n\n" +
		"description: d\n" +
		"license: \"MIT\\0\"\n" +
		"---\n" +
		"X\n"
	rawB := "---\n" +
		"name: n\n" +
		"description: d\n" +
		"license: \"MIT\"\n" +
		"---\n" +
		"\x00X\n"

	psA, err := Parse([]byte(rawA), "p/SKILL.md")
	if err != nil {
		t.Fatalf("Parse (A): %v", err)
	}
	psB, err := Parse([]byte(rawB), "p/SKILL.md")
	if err != nil {
		t.Fatalf("Parse (B): %v", err)
	}
	if psA.License == psB.License {
		t.Fatalf("test fixture bug: License must actually differ (A=%q decoded with an embedded NUL, B=%q) to demonstrate the collision", psA.License, psB.License)
	}
	if strings.TrimSpace(psA.Body) == strings.TrimSpace(psB.Body) {
		t.Fatalf("test fixture bug: Body must actually differ (A=%q, B=%q) to demonstrate the collision", psA.Body, psB.Body)
	}
	if psA.ContentHash == psB.ContentHash {
		t.Fatalf("ContentHash collided across two files with genuinely different License AND Body values "+
			"(A: License=%q Body=%q; B: License=%q Body=%q), got %q for both — "+
			"the forgeable \"\\x00\" join separator let a NUL byte inside License absorb the join and mask "+
			"a real License+Body difference", psA.License, psA.Body, psB.License, psB.Body, psA.ContentHash)
	}
}

// TestParse_ContentHash_CRLFvsLF_Stable is a W4 missing-coverage addition
// proving ContentHash's stated purpose: two inputs carrying identical
// logical name/license/body content, differing ONLY in body line-ending
// convention (CRLF vs LF), must produce an IDENTICAL ContentHash — Parse
// normalizes CRLF/CR to LF before hashing (normalizeText), so a re-scan
// never treats a pure line-ending change as substantive content drift.
func TestParse_ContentHash_CRLFvsLF_Stable(t *testing.T) {
	rawLF := "---\n" +
		"name: n\n" +
		"description: d\n" +
		"license: MIT\n" +
		"---\n" +
		"Line one.\nLine two.\n"
	rawCRLF := "---\r\n" +
		"name: n\r\n" +
		"description: d\r\n" +
		"license: MIT\r\n" +
		"---\r\n" +
		"Line one.\r\nLine two.\r\n"

	psLF, err := Parse([]byte(rawLF), "p/SKILL.md")
	if err != nil {
		t.Fatalf("Parse (LF): %v", err)
	}
	psCRLF, err := Parse([]byte(rawCRLF), "p/SKILL.md")
	if err != nil {
		t.Fatalf("Parse (CRLF): %v", err)
	}
	if psLF.ContentHash != psCRLF.ContentHash {
		t.Fatalf("ContentHash differs between LF (%q) and CRLF (%q) variants of the same logical content", psLF.ContentHash, psCRLF.ContentHash)
	}
}

// TestParse_BOMFixture is a W4 missing-coverage addition: a SKILL.md file
// beginning with a UTF-8 byte-order-mark must still parse correctly (the
// BOM must not become part of the "---" frontmatter delimiter check).
func TestParse_BOMFixture(t *testing.T) {
	raw := "\uFEFF---\n" +
		"name: bom-skill\n" +
		"description: Starts with a UTF-8 BOM.\n" +
		"---\n" +
		"Body text.\n"

	ps, err := Parse([]byte(raw), "p/SKILL.md")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ps.Name != "bom-skill" {
		t.Errorf("Name = %q, want %q", ps.Name, "bom-skill")
	}
	if ps.Description != "Starts with a UTF-8 BOM." {
		t.Errorf("Description = %q", ps.Description)
	}
}

// TestParse_NonStringName_TreatedAsMissing is a W4 missing-coverage
// addition: a frontmatter "name" field of a non-string YAML type (e.g. a
// list) must be treated the same as an absent name — stringField returns
// "" for a non-string value, so this must surface as ErrMissingName, not
// a panic or a silently-stringified value.
func TestParse_NonStringName_TreatedAsMissing(t *testing.T) {
	const raw = "---\n" +
		"name: [not, a, string]\n" +
		"description: d\n" +
		"---\n" +
		"Body.\n"

	_, err := Parse([]byte(raw), "p/SKILL.md")
	if err == nil {
		t.Fatal("expected an error for a non-string \"name\" field")
	}
	if !errors.Is(err, ErrMissingName) {
		t.Errorf("expected errors.Is(err, ErrMissingName), got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Golden-bad fixtures — must error cleanly, never panic
// ---------------------------------------------------------------------------

func TestParse_NoFrontmatter_Errors(t *testing.T) {
	const raw = "# Just a markdown file\n\nNo frontmatter here at all.\n"
	_, err := Parse([]byte(raw), "p/SKILL.md")
	if err == nil {
		t.Fatal("expected an error for a file with no frontmatter")
	}
	if !errors.Is(err, ErrNoFrontmatter) {
		t.Errorf("expected errors.Is(err, ErrNoFrontmatter), got: %v", err)
	}
}

func TestParse_UnterminatedFrontmatter_Errors(t *testing.T) {
	const raw = "---\nname: x\ndescription: y\n" // no closing "---"
	_, err := Parse([]byte(raw), "p/SKILL.md")
	if err == nil {
		t.Fatal("expected an error for an unterminated frontmatter block")
	}
	if !errors.Is(err, ErrUnterminatedFrontmatter) {
		t.Errorf("expected errors.Is(err, ErrUnterminatedFrontmatter), got: %v", err)
	}
}

func TestParse_MissingName_Errors(t *testing.T) {
	const raw = "---\ndescription: has no name field\n---\nBody.\n"
	_, err := Parse([]byte(raw), "p/SKILL.md")
	if err == nil {
		t.Fatal("expected an error for frontmatter missing \"name\"")
	}
	if !errors.Is(err, ErrMissingName) {
		t.Errorf("expected errors.Is(err, ErrMissingName), got: %v", err)
	}
}

func TestParse_CorruptYAML_ErrorsNotPanics(t *testing.T) {
	// Malformed YAML: an unterminated flow sequence + a tab character
	// (illegal as YAML indentation) — must produce a clean error, never a
	// panic that would abort an entire batch scan over one bad file.
	const raw = "---\n" +
		"name: [unterminated\n" +
		"\tdescription: bad-indent\n" +
		"---\n" +
		"Body.\n"

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Parse panicked on corrupt YAML: %v", r)
			}
		}()
		_, err := Parse([]byte(raw), "p/SKILL.md")
		if err == nil {
			t.Fatal("expected an error for corrupt YAML frontmatter")
		}
	}()
}

// TestParse_OversizedFrontmatter_Errors is the finding-1 fix-(a) coverage
// (round-5 Fable-xhigh re-review §11.4.85 remediation): a raw frontmatter
// block larger than maxFrontmatterBytes must be rejected with
// ErrFrontmatterTooLarge BEFORE yaml.Unmarshal ever runs on it, never
// silently accepted. Real SKILL.md frontmatter never needs anywhere near
// this much text (docs/source_ingestion/CATALOG.md §2's whole field
// table is a couple dozen short fields), so this is a belt-and-braces
// bound on plain oversized input — the load-bearing defense against a
// compact YAML alias-expansion attack lives in
// internal/source/mapper.Map (see mapper.ErrMetadataTooLarge), since a
// handful of alias levels fits in well under this cap's budget yet can
// still reference the same decoded subtree exponentially many times.
//
// Mutation-proof: removing the len(frontmatterYAML) > maxFrontmatterBytes
// check in Parse makes this test fail (RED) — yaml.Unmarshal would
// happily decode a >64 KiB block of repeated "keyN: value\n" lines (a
// simple large-but-non-exponential frontmatter, not an alias bomb) and
// Parse would return success instead of ErrFrontmatterTooLarge; restoring
// the check makes it pass (GREEN).
func TestParse_OversizedFrontmatter_Errors(t *testing.T) {
	var body strings.Builder
	body.WriteString("---\nname: n\ndescription: d\n")
	// Pad well past maxFrontmatterBytes with ordinary (non-aliased,
	// non-adversarial) scalar fields, so this test exercises the plain
	// raw-size cap in isolation from the alias-expansion case.
	line := "padkey0123456789: value0123456789value0123456789value0123456789\n"
	for body.Len() < maxFrontmatterBytes+len(line)*2 {
		body.WriteString(line)
	}
	body.WriteString("---\nBody.\n")

	_, err := Parse([]byte(body.String()), "p/SKILL.md")
	if err == nil {
		t.Fatal("expected an error for a frontmatter block exceeding maxFrontmatterBytes")
	}
	if !errors.Is(err, ErrFrontmatterTooLarge) {
		t.Errorf("expected errors.Is(err, ErrFrontmatterTooLarge), got: %v", err)
	}
}

func TestParse_EmptyInput_ErrorsNotPanics(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Parse panicked on empty input: %v", r)
		}
	}()
	_, err := Parse([]byte(""), "p/SKILL.md")
	if err == nil {
		t.Fatal("expected an error for empty input")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func mapKeys(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
