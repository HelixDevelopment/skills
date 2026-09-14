// T-P4.02.1 RED: a name collision currently resolves silently
// (last-writer-wins). RED_MODE=1 asserts the silent world and FAILS on the
// fixed tree — proving the test discriminates. RED_MODE=0 is the standing
// guard: collision yields a typed ErrDuplicateSkill naming both sides.
package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func writeSkillDir(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCollisionFailsClosed(t *testing.T) {
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
		// Pre-fix world: silent last-writer-wins, union of 1.
		if err != nil || len(got) != 1 {
			t.Fatalf("RED_MODE=1 expects silent resolve (nil error, 1 skill); got err=%v n=%d — defect absent", err, len(got))
		}
		return
	}
	if err == nil {
		t.Fatalf("collision resolved silently with %d skills; want typed ErrDuplicateSkill", len(got))
	}
	var dup *DuplicateSkillError
	if !errors.As(err, &dup) {
		t.Fatalf("error type = %T; want *DuplicateSkillError", err)
	}
	if dup.Name != "same" || dup.FirstSource == "" || dup.SecondSource == "" || dup.FirstSource == dup.SecondSource {
		t.Fatalf("error must name the skill and BOTH sides: %+v", dup)
	}
}

func TestUnionCountEquality(t *testing.T) {
	t.Parallel()
	rootA, rootB := t.TempDir(), t.TempDir()
	writeSkillDir(t, rootA, "one", "---\nname: one\ndescription: One.\n---\n\n# One\n")
	writeSkillDir(t, rootA, "two", "---\nname: two\ndescription: Two.\n---\n\n# Two\n")
	writeSkillDir(t, rootB, "three", "---\nname: three\ndescription: Three.\n---\n\n# Three\n")
	reg := NewRegistry()
	if err := reg.RegisterSource(Source{Name: "src-a", Root: rootA, Dialect: DialectAgent, Precedence: 10, Trust: TrustVendored}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterSource(Source{Name: "src-b", Root: rootB, Dialect: DialectAgent, Precedence: 20, Trust: TrustVendored}); err != nil {
		t.Fatal(err)
	}
	got, err := reg.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	stats := reg.Stats()
	sum := 0
	for src, n := range stats.PerSource {
		sum += n
		t.Logf("source %s: %d", src, n)
	}
	if stats.Total != len(got) || stats.Total != sum {
		t.Fatalf("union %d != enumerated %d != per-source sum %d (silent drop?)", stats.Total, len(got), sum)
	}
	if stats.Total != 3 {
		t.Fatalf("union = %d, want 2+1=3", stats.Total)
	}
}

func TestDuplicateSourceNameRefused(t *testing.T) {
	t.Parallel()
	reg := NewRegistry()
	src := Source{Name: "dup", Root: t.TempDir(), Dialect: DialectAgent, Precedence: 10, Trust: TrustVendored}
	if err := reg.RegisterSource(src); err != nil {
		t.Fatal(err)
	}
	err := reg.RegisterSource(src)
	if redMode() {
		if err != nil {
			t.Fatalf("RED_MODE=1 expects silent re-registration; got %v", err)
		}
		return
	}
	var dup *DuplicateSourceError
	if !errors.As(err, &dup) {
		t.Fatalf("error type = %T; want *DuplicateSourceError", err)
	}
}

func TestManifestLoad(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	writeSkillDir(t, filepath.Join(base, "corpus"), "m1", "---\nname: m1\ndescription: M one.\n---\n\n# M1\n")
	manifest := `{"sources": [{"name": "m", "root": "corpus", "dialect": "agent", "precedence": 5, "trust": "first-party"}]}`
	mp := filepath.Join(base, "sources.json")
	if err := os.WriteFile(mp, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	srcs, err := LoadManifest(mp)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(srcs) != 1 || srcs[0].Name != "m" || srcs[0].Dialect != DialectAgent || srcs[0].Trust != TrustFirstParty {
		t.Fatalf("manifest parsed = %+v", srcs)
	}
	if !filepath.IsAbs(srcs[0].Root) {
		t.Fatalf("relative root not resolved against manifest dir: %q", srcs[0].Root)
	}
	reg := NewRegistry()
	if err := reg.RegisterSource(srcs[0]); err != nil {
		t.Fatal(err)
	}
	got, err := reg.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 || got[0].Name != "m1" {
		t.Fatalf("manifest-registered source enumerated %+v", got)
	}
}

func TestManifestRejectsUnknownDialect(t *testing.T) {
	t.Parallel()
	mp := filepath.Join(t.TempDir(), "sources.json")
	manifest := `{"sources": [{"name": "m", "root": ".", "dialect": "toml", "precedence": 5, "trust": "first-party"}]}`
	if err := os.WriteFile(mp, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(mp); err == nil {
		t.Fatal("unknown dialect accepted; want fail-closed error (TOML blocked on A6)")
	}
}

// snapshotDir records the full file inventory under root (relative paths +
// sizes + contents hash) so the in-place test can prove zero writes.
func snapshotDir(t *testing.T, root string) string {
	t.Helper()
	var rows []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if info.IsDir() {
			rows = append(rows, "d "+rel)
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rows = append(rows, fmt.Sprintf("f %s %d %x", rel, len(raw), raw))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(rows)
	out := ""
	for _, r := range rows {
		out += r + "\n"
	}
	return out
}

// TestLoadWritesNothingProvesInPlace (T-P4.02.5): the loader registers a
// corpus by path reference — it must not create, modify or delete any file
// under the source root. Corpus 2 (HelixAgent tree) stays where it is;
// this property is what makes that possible.
func TestLoadWritesNothingProvesInPlace(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeSkillDir(t, root, "s1", "---\nname: s1\ndescription: S one.\n---\n\n# S1\n")
	writeSkillDir(t, root, "s2", "---\nname: s2\ndescription: S two.\n---\n\n# S2\n")
	before := snapshotDir(t, root)
	reg := NewRegistry()
	if err := reg.RegisterSource(Source{Name: "ref", Root: root, Dialect: DialectAgent, Precedence: 10, Trust: TrustVendored}); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Load(); err != nil {
		t.Fatal(err)
	}
	after := snapshotDir(t, root)
	if before != after {
		t.Fatalf("source tree mutated by load:\nbefore %s\nafter  %s", before, after)
	}
}
