// T-P4.07 e2e: the consumer seam. An EXTERNAL package imports pkg/skills
// and drives manifest → register → load → activate → capability-check
// over real on-disk corpora. This is the T-P4.01 runtime signature
// ("a consumer imports pkg/skills and enumerates a real corpus") held
// end to end, plus the P4.04/P4.05 seams at the same boundary.
package skills_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/HelixDevelopment/skills/pkg/skills"
)

func writeConsumerTree(t *testing.T) (manifestPath string) {
	t.Helper()
	base := t.TempDir()
	mk := func(sub, name, extra string) {
		dir := filepath.Join(base, sub)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + name + "\ndescription: Consumer e2e fixture " + name + " with real content words.\ncapabilities:\n  - filesystem:read\n" + extra + "---\n\n# " + name + "\n"
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("consumer/skills/reader", "reader", "")
	mk("consumer/skills/greeter", "greeter", "")
	manifest := `{"sources": [{"name": "consumer", "root": "skills", "dialect": "agent", "precedence": 10, "trust": "first-party"}]}`
	manifestPath = filepath.Join(base, "consumer", "sources.json")
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return manifestPath
}

func TestConsumerEndToEnd(t *testing.T) {
	sources, err := skills.LoadManifest(writeConsumerTree(t))
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	r := skills.NewRegistry()
	for _, s := range sources {
		if err := r.RegisterSource(s); err != nil {
			t.Fatalf("RegisterSource: %v", err)
		}
	}
	registered, err := r.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	st := r.Stats()
	if st.Total != 2 || st.Total != len(registered) {
		t.Fatalf("union-count equality broken: total=%d len=%d", st.Total, len(registered))
	}
	// Empty allowlist: registered but dark.
	active, err := r.Activate(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("default active = %d, want 0", len(active))
	}
	// Allowlisted: active, capability-checked at the boundary.
	active, err = r.Activate([]string{"consumer.reader"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("active = %d, want 1", len(active))
	}
	log := skills.NewAuditLog()
	if err := skills.Use(active[0], "filesystem:read", log); err != nil {
		t.Fatalf("declared use refused: %v", err)
	}
	if err := skills.Use(active[0], "shell:exec", log); err == nil {
		t.Fatal("undeclared shell:exec succeeded at the consumer boundary")
	}
	entries := log.Entries()
	if len(entries) != 2 || entries[0].Verdict != "allowed" || entries[1].Verdict != "refused" {
		t.Fatalf("audit trail wrong: %+v", entries)
	}
}
