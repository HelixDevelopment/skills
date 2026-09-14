package mapper

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/HelixDevelopment/skills/internal/source/skillmd"
)

func mustParse(t *testing.T, raw, sourcePath string) *skillmd.ParsedSkill {
	t.Helper()
	ps, err := skillmd.Parse([]byte(raw), sourcePath)
	if err != nil {
		t.Fatalf("skillmd.Parse: %v", err)
	}
	return ps
}

// ---------------------------------------------------------------------------
// Allowed license -> full content
// ---------------------------------------------------------------------------

func TestMap_AllowedLicense_FullContent(t *testing.T) {
	const raw = "---\n" +
		"name: systematic-debugging\n" +
		"description: Debug things systematically.\n" +
		"license: Apache-2.0\n" +
		"---\n" +
		"# Systematic Debugging\n\nThe full real upstream body text.\n"

	ps := mustParse(t, raw, "systematic-debugging/SKILL.md")
	res, err := Map(ps, "anthropics", []string{"Apache-2.0", "MIT", "CC-BY-4.0"}, "https://example.com/permalink")
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if res.LicenseSkipped {
		t.Fatal("expected LicenseSkipped=false for an allowlisted license")
	}
	if !strings.Contains(res.Skill.Content, "The full real upstream body text.") {
		t.Errorf("Content = %q, expected the real upstream body", res.Skill.Content)
	}
	if res.Skill.Name != "anthropics.systematic-debugging" {
		t.Errorf("Name = %q, want %q", res.Skill.Name, "anthropics.systematic-debugging")
	}
	if res.Skill.Title != "Systematic Debugging" {
		t.Errorf("Title = %q, want %q", res.Skill.Title, "Systematic Debugging")
	}
	if res.Skill.Status != models.SkillStatusDraft {
		t.Errorf("Status = %q, want draft", res.Skill.Status)
	}
	if res.Skill.Kind != models.SkillKindAtomic {
		t.Errorf("Kind = %q, want atomic", res.Skill.Kind)
	}
	if res.Skill.ID.String() == "00000000-0000-0000-0000-000000000000" {
		t.Error("expected a freshly generated non-zero ID")
	}
	if len(res.Skill.Resources) != 0 {
		t.Errorf("expected no upstream-source resource for an allowed-license skill, got %+v", res.Skill.Resources)
	}
	if res.ContentHash != ps.ContentHash {
		t.Errorf("ContentHash = %q, want %q (copied from ParsedSkill)", res.ContentHash, ps.ContentHash)
	}
	if res.UpstreamName != "systematic-debugging" {
		t.Errorf("UpstreamName = %q", res.UpstreamName)
	}
	if res.UpstreamLicense != "Apache-2.0" {
		t.Errorf("UpstreamLicense = %q", res.UpstreamLicense)
	}
}

func TestMap_LicenseAllowlist_CaseInsensitive(t *testing.T) {
	const raw = "---\nname: n\ndescription: d\nlicense: apache-2.0\n---\nBody.\n"
	ps := mustParse(t, raw, "p/SKILL.md")
	res, err := Map(ps, "src", []string{"Apache-2.0"}, "")
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if res.LicenseSkipped {
		t.Fatal("expected a case-insensitive license match to be allowed")
	}
}

// ---------------------------------------------------------------------------
// Disallowed / empty / unknown license -> stub, never the real content
// ---------------------------------------------------------------------------

func TestMap_DisallowedLicense_Stub(t *testing.T) {
	const raw = "---\n" +
		"name: docx\n" +
		"description: Work with docx files.\n" +
		"license: source-available\n" +
		"---\n" +
		"SECRET UPSTREAM BODY that must never be redistributed.\n"

	ps := mustParse(t, raw, "docx/SKILL.md")
	res, err := Map(ps, "anthropics", []string{"Apache-2.0", "MIT"}, "https://github.com/anthropics/skills/blob/main/docx/SKILL.md")
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if !res.LicenseSkipped {
		t.Fatal("expected LicenseSkipped=true for a disallowed license")
	}
	if strings.Contains(res.Skill.Content, "SECRET UPSTREAM BODY") {
		t.Fatalf("license-gated skill's Content leaked the real upstream body: %q", res.Skill.Content)
	}
	if len(res.Skill.Resources) != 1 {
		t.Fatalf("expected exactly 1 upstream-source resource, got %d: %+v", len(res.Skill.Resources), res.Skill.Resources)
	}
	if res.Skill.Resources[0].URL != "https://github.com/anthropics/skills/blob/main/docx/SKILL.md" {
		t.Errorf("Resources[0].URL = %q", res.Skill.Resources[0].URL)
	}
	if res.Skill.Resources[0].ResourceType != "upstream_source" {
		t.Errorf("Resources[0].ResourceType = %q", res.Skill.Resources[0].ResourceType)
	}
}

func TestMap_EmptyLicense_TreatedAsDisallowed(t *testing.T) {
	// An undeclared license must NEVER be assumed safe to redistribute.
	const raw = "---\nname: n\ndescription: d\n---\nSECRET BODY.\n"
	ps := mustParse(t, raw, "p/SKILL.md")
	res, err := Map(ps, "src", []string{"Apache-2.0", "MIT", ""}, "")
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if !res.LicenseSkipped {
		t.Fatal("expected an empty/undeclared license to be treated as disallowed even if the allowlist itself contains an empty entry")
	}
	if strings.Contains(res.Skill.Content, "SECRET BODY") {
		t.Fatalf("license-gated skill leaked real content: %q", res.Skill.Content)
	}
}

func TestMap_DisallowedLicense_NoPermalink_NoResource(t *testing.T) {
	const raw = "---\nname: n\ndescription: d\nlicense: proprietary\n---\nBody.\n"
	ps := mustParse(t, raw, "p/SKILL.md")
	res, err := Map(ps, "src", []string{"MIT"}, "")
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if !res.LicenseSkipped {
		t.Fatal("expected LicenseSkipped=true")
	}
	if len(res.Skill.Resources) != 0 {
		t.Errorf("expected no resource when sourcePermalink is empty, got %+v", res.Skill.Resources)
	}
}

// ---------------------------------------------------------------------------
// Namespacing / metadata / titleize
// ---------------------------------------------------------------------------

func TestMap_Namespacing_AvoidsCollisionWithNativeSkill(t *testing.T) {
	const raw = "---\nname: systematic-debugging\ndescription: d\nlicense: MIT\n---\nBody.\n"
	ps := mustParse(t, raw, "p/SKILL.md")

	resA, err := Map(ps, "obra", []string{"MIT"}, "")
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	resB, err := Map(ps, "anthropics", []string{"MIT"}, "")
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if resA.Skill.Name == resB.Skill.Name {
		t.Fatalf("two different sources mapping the same upstream name produced colliding names: %q", resA.Skill.Name)
	}
	// Neither collides with a hypothetical native (non-namespaced) skill
	// of the same base name.
	nativeName := "systematic-debugging"
	if resA.Skill.Name == nativeName || resB.Skill.Name == nativeName {
		t.Fatalf("namespaced name collided with the bare upstream/native name")
	}
}

func TestMap_MetadataLosslessRoundTrip(t *testing.T) {
	const raw = "---\n" +
		"name: n\n" +
		"description: d\n" +
		"allowed-tools: [Read, Edit]\n" +
		"custom-field: 42\n" +
		"license: MIT\n" +
		"---\n" +
		"Body.\n"
	ps := mustParse(t, raw, "p/SKILL.md")
	res, err := Map(ps, "src", []string{"MIT"}, "")
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(res.Skill.Metadata, &decoded); err != nil {
		t.Fatalf("unmarshal Metadata: %v", err)
	}
	for _, k := range []string{"name", "description", "allowed-tools", "custom-field", "license"} {
		if _, ok := decoded[k]; !ok {
			t.Errorf("Metadata missing key %q; got %v", k, decoded)
		}
	}
}

// TestMap_Version_UsesDeclaredFrontmatterVersion is the W2 remediation
// test: when the upstream SKILL.md frontmatter declares a "version"
// field, the mapped Skill.Version MUST use it rather than the hardcoded
// default.
func TestMap_Version_UsesDeclaredFrontmatterVersion(t *testing.T) {
	const raw = "---\n" +
		"name: bulk-refactor\n" +
		"description: d\n" +
		"version: \"1.2.3\"\n" +
		"license: MIT\n" +
		"---\n" +
		"Body.\n"
	ps := mustParse(t, raw, "p/SKILL.md")
	res, err := Map(ps, "src", []string{"MIT"}, "")
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if res.Skill.Version != "1.2.3" {
		t.Errorf("Version = %q, want %q (declared in frontmatter)", res.Skill.Version, "1.2.3")
	}
}

// TestMap_Version_DefaultsWhenAbsent is the W2 remediation test's
// complement: when the frontmatter declares no "version" field at all,
// Skill.Version falls back to the "1.0.0" default.
func TestMap_Version_DefaultsWhenAbsent(t *testing.T) {
	const raw = "---\nname: n\ndescription: d\nlicense: MIT\n---\nBody.\n"
	ps := mustParse(t, raw, "p/SKILL.md")
	res, err := Map(ps, "src", []string{"MIT"}, "")
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if res.Skill.Version != "1.0.0" {
		t.Errorf("Version = %q, want default %q", res.Skill.Version, "1.0.0")
	}
}

// TestMap_Version_NonStringFrontmatterValue_UsesDefault covers a "version"
// field of an unexpected YAML type (e.g. a number, unquoted in YAML) —
// treated the same as absent, never a type-assertion panic.
func TestMap_Version_NonStringFrontmatterValue_UsesDefault(t *testing.T) {
	const raw = "---\nname: n\ndescription: d\nversion: 1\nlicense: MIT\n---\nBody.\n"
	ps := mustParse(t, raw, "p/SKILL.md")
	res, err := Map(ps, "src", []string{"MIT"}, "")
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if res.Skill.Version != "1.0.0" {
		t.Errorf("Version = %q, want default %q for a non-string frontmatter version", res.Skill.Version, "1.0.0")
	}
}

func TestMap_Titleize(t *testing.T) {
	cases := map[string]string{
		"systematic-debugging":    "Systematic Debugging",
		"test_driven_development": "Test Driven Development",
		"a":                       "A",
		"already-Mixed-case":      "Already Mixed Case",
	}
	for name, want := range cases {
		got := titleize(name)
		if got != want {
			t.Errorf("titleize(%q) = %q, want %q", name, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Error cases
// ---------------------------------------------------------------------------

func TestMap_NilParsed_Errors(t *testing.T) {
	if _, err := Map(nil, "src", nil, ""); err == nil {
		t.Fatal("expected error for nil ParsedSkill")
	}
}

func TestMap_EmptySourceSlug_Errors(t *testing.T) {
	const raw = "---\nname: n\ndescription: d\n---\nBody.\n"
	ps := mustParse(t, raw, "p/SKILL.md")
	if _, err := Map(ps, "  ", nil, ""); err == nil {
		t.Fatal("expected error for a blank sourceSlug")
	}
}

// TestMap_EmptyParsedName_Errors is a W4 missing-coverage addition for the
// parsed.Name == "" guard: skillmd.Parse itself never produces a
// ParsedSkill with an empty Name (it rejects that at the frontmatter
// layer via ErrMissingName), so this constructs a *skillmd.ParsedSkill
// directly — bypassing Parse — to exercise Map's own defensive guard
// against that caller-programming-error case.
func TestMap_EmptyParsedName_Errors(t *testing.T) {
	ps := &skillmd.ParsedSkill{
		Name:       "",
		SourcePath: "p/SKILL.md",
	}
	if _, err := Map(ps, "src", nil, ""); err == nil {
		t.Fatal("expected error for a ParsedSkill with an empty Name")
	}
}

// ---------------------------------------------------------------------------
// YAML alias-expansion ("billion laughs") resource-exhaustion guard
// (Fable-xhigh re-review round-5, finding 1, §11.4.85/§11.4.115)
// ---------------------------------------------------------------------------

// aliasChainFrontmatter builds a SKILL.md frontmatter block whose body is
// a classic "billion laughs" YAML anchor/alias chain: levlN references
// levl(N-1) nine times, so the number of distinct leaf values an
// alias-unaware consumer (e.g. encoding/json.Marshal) would re-serialize
// grows as 9^levels. The RAW TEXT stays linear in levels (a constant
// number of bytes per level) — this is the entire point of the attack:
// a tiny source file, a huge decoded-and-then-naively-walked expansion.
// "bomb" is an extra top-level key that ALSO references the deepest
// level, mirroring how a real attacker-controlled frontmatter field
// would carry the payload.
func aliasChainFrontmatter(levels int) string {
	fm := "name: alias-bomb\ndescription: d\nlevl0: &levl0 \"x\"\n"
	prev := "levl0"
	for i := 1; i <= levels; i++ {
		cur := fmt.Sprintf("levl%d", i)
		fm += fmt.Sprintf(
			"%s: &%s [*%s,*%s,*%s,*%s,*%s,*%s,*%s,*%s,*%s]\n",
			cur, cur, prev, prev, prev, prev, prev, prev, prev, prev, prev,
		)
		prev = cur
	}
	fm += fmt.Sprintf("bomb: *%s\n", prev)
	return "---\n" + fm + "---\nBody.\n"
}

// TestMap_AliasExpansionFrontmatter_RejectedFastNotAllocated is the
// finding-1 RED-first regression guard: a crafted level-7-alias
// frontmatter fixture (a few hundred bytes — well under
// skillmd's own 64 KiB raw-frontmatter cap, so skillmd.Parse succeeds;
// the raw byte-size cap cannot by itself bound this class of attack,
// see ErrFrontmatterTooLarge's doc comment) must be REJECTED by Map, and
// REJECTED FAST — this test's own wall-clock bound is the "not
// allocated" proof: this exact fixture, run through the PRE-FIX pipeline
// (json.Marshal(parsed.RawFrontmatter) with no estimate-first guard),
// was measured during this remediation to marshal to ~4.8 MB in ~47ms
// for this SAME 7-level construction (captured via a throwaway harness
// against the pinned goccy v1.18.0 + encoding/json, 2026-07-16) — and a
// mere ONE additional level (an 8-level chain, 604 raw bytes) was enough
// to exhaust multiple GB and crash the process with "fatal error: out of
// memory" during json.Marshal's internal buffer growth, captured the
// same way. This test deliberately keeps the fixture at the requested
// "level-7" size (levl0..levl6) rather than reproducing that crash
// in CI.
//
// Mutation-proof (verified by hand during remediation, per this file's
// existing convention — see e.g. TestMap_Version_UsesDeclaredFrontmatterVersion's
// neighbors): commenting out the estimateMarshaledSize call in Map
// (letting json.Marshal run unguarded) makes this exact test hang/
// allocate for the ~47ms-plus-4.8MB the fixture below actually needs,
// and the DEEPER companion fixture in
// TestMap_AliasExpansionFrontmatter_DepthIndependentRejection would
// instead attempt an allocation of an order of magnitude no real
// process can satisfy — RED; restoring the guard makes both return
// ErrMetadataTooLarge in well under a second — GREEN.
func TestMap_AliasExpansionFrontmatter_RejectedFastNotAllocated(t *testing.T) {
	raw := aliasChainFrontmatter(6) // levl0..levl6 == 7 alias levels, ~469 raw bytes.
	if len(raw) > 2000 {
		t.Fatalf("test fixture bug: expected a few-hundred-byte fixture, got %d bytes", len(raw))
	}

	ps, err := skillmd.Parse([]byte(raw), "alias-bomb/SKILL.md")
	if err != nil {
		t.Fatalf("skillmd.Parse unexpectedly rejected the small (%d-byte) alias-chain fixture: %v", len(raw), err)
	}

	start := time.Now()
	_, mapErr := Map(ps, "src", []string{"MIT"}, "")
	elapsed := time.Since(start)

	if mapErr == nil {
		t.Fatalf("expected Map to reject a YAML alias-expansion frontmatter (raw=%d bytes), got no error", len(raw))
	}
	if !errors.Is(mapErr, ErrMetadataTooLarge) {
		t.Errorf("expected errors.Is(err, ErrMetadataTooLarge), got: %v", mapErr)
	}
	// The bound below is generous for a loaded CI host (the fixed code
	// measures in the ~1ms range) yet far below what an unguarded
	// json.Marshal of the true expansion would require — see the doc
	// comment above for the measured pre-fix numbers.
	const maxAllowed = 2 * time.Second
	if elapsed > maxAllowed {
		t.Fatalf("Map took %v to reject the alias-expansion fixture, want under %v — "+
			"the estimate-before-marshal guard should reject in well under a millisecond, "+
			"a multi-second rejection suggests the guard ran AFTER an expensive allocation rather than before it",
			elapsed, maxAllowed)
	}
}

// TestMap_AliasExpansionFrontmatter_DepthIndependentRejection proves the
// load-bearing property that makes this a genuine fix rather than a
// fixture-specific patch: estimateMarshaledSize's rejection cost is
// bounded by maxMetadataEstimatedBytes, NOT by how deep/adversarial the
// alias chain is. A 20-level chain (9^20 ≈ 1.2×10^19 theoretical leaf
// expansion — far beyond anything encoding/json.Marshal or any real
// process could ever complete) must be rejected in comparable time to
// the 7-level fixture above, never taking longer merely because the
// input is "more extreme" — that depth-independence is exactly what
// distinguishes "refuse rather than allocate" (this fix) from "check the
// length of an already-completed marshal" (which could never even reach
// this depth without crashing first).
func TestMap_AliasExpansionFrontmatter_DepthIndependentRejection(t *testing.T) {
	raw := aliasChainFrontmatter(20)

	ps, err := skillmd.Parse([]byte(raw), "alias-bomb-deep/SKILL.md")
	if err != nil {
		t.Fatalf("skillmd.Parse unexpectedly rejected the small (%d-byte) 20-level alias-chain fixture: %v", len(raw), err)
	}

	start := time.Now()
	_, mapErr := Map(ps, "src", []string{"MIT"}, "")
	elapsed := time.Since(start)

	if mapErr == nil {
		t.Fatalf("expected Map to reject a 20-level YAML alias-expansion frontmatter (raw=%d bytes), got no error", len(raw))
	}
	if !errors.Is(mapErr, ErrMetadataTooLarge) {
		t.Errorf("expected errors.Is(err, ErrMetadataTooLarge), got: %v", mapErr)
	}
	const maxAllowed = 2 * time.Second
	if elapsed > maxAllowed {
		t.Fatalf("Map took %v to reject a 20-level alias-expansion fixture, want under %v — "+
			"rejection cost must be bounded by the estimate cap, independent of alias-chain depth",
			elapsed, maxAllowed)
	}
}
