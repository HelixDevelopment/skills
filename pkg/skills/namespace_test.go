// T-P4.03: namespaced identity <source>.<name> (spec §8).
//
// T-P4.03.1 the qualification rule + its escaping (namespace.go).
// T-P4.03.2 two same-named skills from different sources coexist.
// T-P4.03.3 AssertNameMatchesDir reports legacy violators without refusing.
package skills

import (
	"strings"
	"testing"
)

func TestQualifiedNameRule(t *testing.T) {
	t.Parallel()
	q := QualifiedName("src-a", "same")
	if q != "src-a.same" {
		t.Fatalf("QualifiedName = %q, want %q", q, "src-a.same")
	}
	s, n, err := ParseQualified(q)
	if err != nil {
		t.Fatalf("ParseQualified: %v", err)
	}
	if s != "src-a" || n != "same" {
		t.Fatalf("round-trip = (%q,%q), want (src-a,same)", s, n)
	}
	// Dots in bare names are preserved verbatim: the split is on the FIRST
	// dot and source names are dot-free (Source.Validate), so qualification
	// is unambiguous with no escape character.
	q2 := QualifiedName("src", "my.skill")
	s2, n2, err := ParseQualified(q2)
	if err != nil || s2 != "src" || n2 != "my.skill" {
		t.Fatalf("dotted bare name round-trip = (%q,%q,%v)", s2, n2, err)
	}
	for _, bad := range []string{"", "nodot", ".leadingdot", "trailingdot."} {
		if _, _, err := ParseQualified(bad); err == nil {
			t.Fatalf("ParseQualified(%q) accepted; want error", bad)
		}
	}
	// Skill carries its qualified identity.
	sk := Skill{Source: "src-a", Name: "same"}
	if sk.Qualified() != "src-a.same" {
		t.Fatalf("Skill.Qualified = %q", sk.Qualified())
	}
}

func TestCrossSourceCoexistence(t *testing.T) {
	t.Parallel()
	rootA, rootB := t.TempDir(), t.TempDir()
	writeSkillDir(t, rootA, "same", "---\nname: same\ndescription: Same from A.\n---\n\n# A\n")
	writeSkillDir(t, rootB, "same", "---\nname: same\ndescription: Same from B.\n---\n\n# B\n")
	reg := NewRegistry()
	if err := reg.RegisterSource(Source{Name: "src-a", Root: rootA, Dialect: DialectAgent, Precedence: 10, Trust: TrustVendored}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterSource(Source{Name: "src-b", Root: rootB, Dialect: DialectAgent, Precedence: 20, Trust: TrustVendored}); err != nil {
		t.Fatal(err)
	}
	got, err := reg.Load()
	if redMode() {
		// Pre-fix world: fail-closed on the BARE name (T-P4.02 behavior).
		if err == nil {
			t.Fatal("RED_MODE=1 expects the bare-name collision refusal (pre-namespacing defect)")
		}
		return
	}
	if err != nil {
		t.Fatalf("cross-source same-name refused: %v (growth in one source must never retroactively shadow another)", err)
	}
	if len(got) != 2 {
		t.Fatalf("union = %d, want 2 coexisting", len(got))
	}
	ids := map[string]string{}
	for _, s := range got {
		ids[s.Qualified()] = s.Description
	}
	if ids["src-a.same"] != "Same from A." || ids["src-b.same"] != "Same from B." {
		t.Fatalf("qualified identities wrong: %v", ids)
	}
	stats := reg.Stats()
	if stats.Total != 2 || stats.PerSource["src-a"] != 1 || stats.PerSource["src-b"] != 1 {
		t.Fatalf("union-count equality broken by coexistence: %+v", stats)
	}
}

func TestSameSourceDuplicateStillRefused(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSkillDir(t, root, "d1", "---\nname: same\ndescription: Same d1.\n---\n\n# D1\n")
	writeSkillDir(t, root, "d2", "---\nname: same\ndescription: Same d2.\n---\n\n# D2\n")
	reg := NewRegistry()
	if err := reg.RegisterSource(Source{Name: "src", Root: root, Dialect: DialectAgent, Precedence: 10, Trust: TrustVendored}); err != nil {
		t.Fatal(err)
	}
	_, err := reg.Load()
	if redMode() {
		if err != nil && strings.Contains(err.Error(), "duplicate skill") {
			t.Fatal("RED_MODE=1 expects silent resolve within one source (pre-fix)")
		}
		return
	}
	if err == nil {
		t.Fatal("duplicate qualified identity src.same resolved silently; want typed error")
	}
}

func TestAssertNameMatchesDir(t *testing.T) {
	t.Parallel()
	conforming := []Skill{{Source: "s", Name: "alpha", Dir: "alpha"}}
	if bad := AssertNameMatchesDir(conforming); len(bad) != 0 {
		t.Fatalf("conforming source reported violators: %+v", bad)
	}
	legacy := []Skill{
		{Source: "s", Name: "Action Prefix System", Dir: "action-prefix-system", NameMismatch: true},
		{Source: "s", Name: "beta", Dir: "beta"},
	}
	bad := AssertNameMatchesDir(legacy)
	if len(bad) != 1 || bad[0].Name != "Action Prefix System" {
		t.Fatalf("legacy violators = %+v, want exactly [Action Prefix System]", bad)
	}
}
