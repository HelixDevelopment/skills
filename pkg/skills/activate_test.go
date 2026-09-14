// T-P4.04.1 RED-first: a directory walk MUST NOT imply activation.
//
// Defect (present before T-P4.04): Registry.Load() returns every registered
// skill and there is no activation seam at all — enumeration IS activation.
// The fixed world: Activate(allow, ceiling) is the ONLY path to an active
// set; empty/nil allow yields zero active skills despite N registered;
// ceiling+1 refuses with a typed error naming the offender; an active pair
// above the T-P2.04 similarity threshold refuses naming both.
//
// RED_MODE=1 asserts the defect (walk implies activation) and therefore
// FAILS on the fixed tree — proving these tests discriminate. RED_MODE=0
// (default) is the standing regression guard (§11.4.135).
package skills

import (
	"os"
	"strings"
	"testing"
)

// activateFixture loads the two-dialect fixture corpus into a registry.
func activateFixture(t *testing.T) *Registry {
	t.Helper()
	rootA, rootB := writeFixtureCorpus(t)
	r := NewRegistry()
	if err := r.RegisterSource(Source{Name: "fixture-a", Root: rootA, Dialect: DialectAgent, Precedence: 10, Trust: TrustFirstParty}); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterSource(Source{Name: "fixture-b", Root: rootB, Dialect: DialectHelixAgent, Precedence: 20, Trust: TrustFirstParty}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Load(); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestActivationDefaultEmpty(t *testing.T) {
	r := activateFixture(t)
	registered, err := r.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(registered) != 2 {
		t.Fatalf("fixture must register 2 skills, got %d", len(registered))
	}
	active, err := r.Activate(nil, 0)
	if err != nil {
		t.Fatalf("Activate(nil) must not error, it must yield empty: %v", err)
	}
	if redMode() {
		// Pre-fix world: no activation seam — the walk IS the active set.
		if len(active) == 0 {
			t.Fatal("RED_MODE=1 expects the defect (walk implies activation); got empty — defect absent, fix present")
		}
		return
	}
	if len(active) != 0 {
		t.Fatalf("default active set = %d, want 0 (empty allowlist activates nothing)", len(active))
	}
}

func TestActivationAllowlistSelects(t *testing.T) {
	if redMode() {
		t.Skip("RED_MODE=1: Activate does not exist pre-fix (compile-level RED, see red.log)")
	}
	r := activateFixture(t)
	active, err := r.Activate([]string{"fixture-b.beta"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].Qualified() != "fixture-b.beta" {
		t.Fatalf("allowlist selected %+v, want exactly [fixture-b.beta]", active)
	}
}

func TestActivationUnknownAllowEntryRefused(t *testing.T) {
	if redMode() {
		t.Skip("RED_MODE=1: Activate does not exist pre-fix (compile-level RED, see red.log)")
	}
	r := activateFixture(t)
	_, err := r.Activate([]string{"fixture-a.nosuchskill"}, 0)
	if err == nil {
		t.Fatal("unknown allowlist entry must fail closed, got nil error")
	}
	if _, ok := err.(*UnknownAllowEntryError); !ok {
		t.Fatalf("want *UnknownAllowEntryError, got %T: %v", err, err)
	}
}

func TestActivationCeilingRefusesAndNamesOffender(t *testing.T) {
	if redMode() {
		t.Skip("RED_MODE=1: Activate does not exist pre-fix (compile-level RED, see red.log)")
	}
	r := activateFixture(t)
	// Ceiling 1 with 2 allowlisted: the 2nd in deterministic order refuses.
	_, err := r.Activate([]string{"fixture-a.alpha", "fixture-b.beta"}, 1)
	if err == nil {
		t.Fatal("ceiling+1 must refuse, got nil error")
	}
	cerr, ok := err.(*CeilingExceededError)
	if !ok {
		t.Fatalf("want *CeilingExceededError, got %T: %v", err, err)
	}
	if cerr.Offender == "" || !strings.Contains(err.Error(), cerr.Offender) {
		t.Fatalf("refusal must NAME the offender: %v", err)
	}
	if cerr.Ceiling != 1 || cerr.Count != 2 {
		t.Fatalf("ceiling/count = %d/%d, want 1/2", cerr.Ceiling, cerr.Count)
	}
}

func TestActivationDefaultCeilingIsCalibrated(t *testing.T) {
	// T-P2.03 measured output (ceiling.json): FLAT curve, provisional
	// ceiling at the largest tested N=20. The default MUST be that number —
	// importing any other value is a §11.4.6 guess.
	if DefaultCeiling != 20 {
		t.Fatalf("DefaultCeiling = %d, want 20 (ceiling.json, T-P2.03)", DefaultCeiling)
	}
}

func TestActivationSimilarityGateFires(t *testing.T) {
	if redMode() {
		t.Skip("RED_MODE=1: Activate does not exist pre-fix (compile-level RED, see red.log)")
	}
	// Near-duplicate descriptions: shared content words drive Jaccard up.
	root := t.TempDir()
	mk := func(dir, name, desc string) {
		if err := os.MkdirAll(root+"/"+dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n# X\n"
		if err := os.WriteFile(root+"/"+dir+"/SKILL.md", []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("rep-a", "rep-a", "Migrate postgres tables to hypertables with compression policy and retention rules.")
	mk("rep-b", "rep-b", "Migrate postgres tables to hypertables with compression policy and chunk intervals.")
	r := NewRegistry()
	if err := r.RegisterSource(Source{Name: "sim", Root: root, Dialect: DialectAgent, Precedence: 10, Trust: TrustFirstParty}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Load(); err != nil {
		t.Fatal(err)
	}
	_, err := r.Activate([]string{"sim.rep-a", "sim.rep-b"}, 0)
	if err == nil {
		t.Fatal("confusable active pair must trip the similarity gate, got nil error")
	}
	serr, ok := err.(*SimilarityConflictError)
	if !ok {
		t.Fatalf("want *SimilarityConflictError, got %T: %v", err, err)
	}
	if serr.Score <= serr.Threshold {
		t.Fatalf("conflict score %.4f must exceed threshold %.4f", serr.Score, serr.Threshold)
	}
}

func TestCorpusTwoStaysUnactivated(t *testing.T) {
	// T-P4.04.5: corpus 2 is vendored-tier and UNACTIVATED pending D1.
	// D1=A (2026-09-13) approved EXTRACTION (referencing), activation stays
	// allowlist-only. So: a vendored source registers fine, but an empty
	// allowlist leaves it fully inactive — registration ≠ activation.
	if redMode() {
		t.Skip("RED_MODE=1: Activate does not exist pre-fix (compile-level RED, see red.log)")
	}
	r := activateFixture(t)
	if err := r.RegisterSource(Source{Name: "corpus2", Root: "/nonexistent", Dialect: DialectHelixAgent, Precedence: 30, Trust: TrustVendored}); err != nil {
		t.Fatal(err)
	}
	active, err := r.Activate(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range active {
		if s.Trust == TrustVendored && s.Source == "corpus2" {
			t.Fatalf("vendored corpus-2 skill active without allowlist entry: %s", s.Qualified())
		}
	}
	if len(active) != 0 {
		t.Fatalf("default active set = %d, want 0", len(active))
	}
}
