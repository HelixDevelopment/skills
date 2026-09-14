// T-P4.01.1 RED-first: the loader skeleton MUST be importable and enumerate a
// real on-disk corpus. RED_MODE=1 asserts the pre-fix world (nothing loadable)
// and therefore FAILS on the fixed tree — proving the test discriminates.
// RED_MODE=0 (default) is the standing regression guard (§11.4.135).
package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func redMode() bool { return os.Getenv("RED_MODE") == "1" }

// writeFixtureCorpus builds real on-disk corpora, one root per dialect:
// rootA/alpha (Agent dialect shape) + rootB/beta (HelixAgent dialect shape,
// folded scalars transcribed from submodules/helix_agent/.../toolset-design).
func writeFixtureCorpus(t *testing.T) (rootA, rootB string) {
	t.Helper()
	rootA, rootB = t.TempDir(), t.TempDir()
	alpha := "---\nname: alpha\ndescription: Alpha skill for loader tests.\nversion: 1.0.0\n---\n\n# Alpha\n\nBody of alpha.\n"
	beta := "---\nname: beta\ndescription: >\n  Beta skill with a folded scalar description spanning\n  two lines, HelixAgent dialect shape.\nversion: 2.1.0\nlicense: Apache-2.0\nallowed-tools: Read, Write\n---\n\n# Beta\n\nBody of beta.\n"
	for _, dir := range [][3]string{{rootA, "alpha", alpha}, {rootB, "beta", beta}} {
		d := filepath.Join(dir[0], dir[1])
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(dir[2]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return rootA, rootB
}

func TestLoaderEnumeratesFixtureCorpus(t *testing.T) {
	rootA, rootB := writeFixtureCorpus(t)
	srcA := Source{Name: "fixture-a", Root: rootA, Dialect: DialectAgent, Precedence: 10, Trust: TrustVendored}
	srcB := Source{Name: "fixture-b", Root: rootB, Dialect: DialectHelixAgent, Precedence: 20, Trust: TrustVendored}
	for _, s := range []Source{srcA, srcB} {
		if err := s.Validate(); err != nil {
			t.Fatalf("fixture source invalid: %v", err)
		}
	}
	gotA, err := NewLoader().LoadSource(srcA)
	if err != nil {
		t.Fatalf("LoadSource A: %v", err)
	}
	gotB, err := NewLoader().LoadSource(srcB)
	if err != nil {
		t.Fatalf("LoadSource B: %v", err)
	}
	got := append(gotA, gotB...)
	if redMode() {
		// Pre-fix world: no importable loader, nothing loadable.
		if len(got) != 0 {
			t.Fatalf("RED_MODE=1 expects the defect (0 skills); got %d — defect absent, fix present", len(got))
		}
		return
	}
	if len(got) != 2 {
		t.Fatalf("union count = %d, want 2 (no silent drops)", len(got))
	}
	byName := map[string]Skill{}
	for _, s := range got {
		byName[s.Name] = s
	}
	a, ok := byName["alpha"]
	if !ok {
		t.Fatal("skill alpha not enumerated")
	}
	if a.Description != "Alpha skill for loader tests." {
		t.Fatalf("alpha description = %q", a.Description)
	}
	if a.Body == "" || a.Hash == "" {
		t.Fatal("alpha must carry body and content hash")
	}
	if a.Source != "fixture-a" || a.Dir != "alpha" {
		t.Fatalf("alpha provenance = %+v", a)
	}
	b, ok := byName["beta"]
	if !ok {
		t.Fatal("skill beta not enumerated")
	}
	if b.Version != "2.1.0" || b.License != "Apache-2.0" {
		t.Fatalf("beta version/license = %q/%q", b.Version, b.License)
	}
	want := "Beta skill with a folded scalar description spanning two lines, HelixAgent dialect shape."
	if b.Description != want {
		t.Fatalf("beta folded description = %q, want %q", b.Description, want)
	}
	if b.XHelixAgent == nil || b.XHelixAgent.AllowedTools != "Read, Write" {
		t.Fatalf("beta HelixAgent profile carriage lost: %+v", b.XHelixAgent)
	}
}

func TestLoaderRejectsNameDirectoryMismatch(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "wrongdir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: rightname\ndescription: Mismatch fixture.\n---\n\n# X\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	src := Source{Name: "fixture", Root: root, Dialect: DialectAgent, Precedence: 10, Trust: TrustVendored}
	_, err := NewLoader().LoadSource(src)
	if redMode() {
		if err == nil {
			t.Fatal("RED_MODE=1 expects silent acceptance of the mismatch (the defect)")
		}
		return
	}
	if err == nil {
		t.Fatal("name-matches-directory violation accepted silently; want fail-closed error")
	}
}

func TestLoaderRequiresExactCaseManifest(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "lower")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: lower\ndescription: Lowercase manifest filename.\n---\n\n# X\n"
	if err := os.WriteFile(filepath.Join(dir, "skill.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	src := Source{Name: "fixture", Root: root, Dialect: DialectAgent, Precedence: 10, Trust: TrustVendored}
	got, err := NewLoader().LoadSource(src)
	if err != nil {
		t.Fatalf("LoadSource: %v", err)
	}
	if redMode() {
		if len(got) != 0 {
			t.Fatalf("RED_MODE=1 expects the lowercase manifest to load (the defect); got %d", len(got))
		}
		return
	}
	if len(got) != 0 {
		t.Fatalf("lowercase skill.md enumerated (%d); want 0 — exact-case SKILL.md only", len(got))
	}
}
