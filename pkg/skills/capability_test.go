// T-P4.05.1 RED-first: an undeclared capability MUST be refused at the
// call boundary — and the refusal MUST be audited.
//
// Defect (present before T-P4.05): the loader records Capabilities but
// nothing enforces them — an undeclared use succeeds silently. The fixed
// world: Use(skill, capability, log) is the call boundary; undeclared use
// returns *CapabilityRefusedError and appends a refused audit entry.
// Parse/load NEVER refuses (declaration recorded, not enforced at parse).
//
// RED_MODE=1 asserts the defect (undeclared use succeeds) and therefore
// FAILS on the fixed tree. RED_MODE=0 (default) is the standing guard.
package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func capFixtureSkill() Skill {
	return Skill{
		Name:         "reader",
		Description:  "Reads files.",
		Source:       "fixture",
		Dir:          "reader",
		Trust:        TrustFirstParty,
		Capabilities: []string{"filesystem:read"},
	}
}

func TestUndeclaredCapabilityRefusedAtCallBoundary(t *testing.T) {
	log := NewAuditLog()
	err := Use(capFixtureSkill(), "shell:exec", log)
	if redMode() {
		// Pre-fix world: no enforcement — the use succeeds silently.
		if err != nil {
			t.Fatalf("RED_MODE=1 expects the defect (silent success); got refusal %v — defect absent, fix present", err)
		}
		return
	}
	refused, ok := err.(*CapabilityRefusedError)
	if !ok {
		t.Fatalf("want *CapabilityRefusedError, got %T (%v)", err, err)
	}
	if refused.Skill != "fixture.reader" || refused.Capability != "shell:exec" {
		t.Fatalf("refusal must name skill + capability: %+v", refused)
	}
}

func TestDeclaredCapabilityAllowedAndAudited(t *testing.T) {
	if redMode() {
		t.Skip("RED_MODE=1: Use does not exist pre-fix (compile-level RED, see red.log)")
	}
	log := NewAuditLog()
	if err := Use(capFixtureSkill(), "filesystem:read", log); err != nil {
		t.Fatalf("declared use refused: %v", err)
	}
	entries := log.Entries()
	if len(entries) != 1 || entries[0].Verdict != "allowed" {
		t.Fatalf("allowed use must audit exactly one allowed entry: %+v", entries)
	}
}

func TestRefusalIsAudited(t *testing.T) {
	if redMode() {
		t.Skip("RED_MODE=1: Use does not exist pre-fix (compile-level RED, see red.log)")
	}
	log := NewAuditLog()
	_ = Use(capFixtureSkill(), "shell:exec", log)
	entries := log.Entries()
	if len(entries) != 1 {
		t.Fatalf("refused use must audit exactly one entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Verdict != "refused" || e.Skill != "fixture.reader" || e.Capability != "shell:exec" {
		t.Fatalf("audit entry must name skill+capability+verdict: %+v", e)
	}
}

func TestParseTimeDoesNotRefuse(t *testing.T) {
	if redMode() {
		t.Skip("RED_MODE=1: capability set does not exist pre-fix (compile-level RED, see red.log)")
	}
	// Enforcement lives at the call boundary, NOT at parse time: a skill
	// declaring nothing still LOADS (declaration recorded, possibly empty).
	root := t.TempDir()
	dir := filepath.Join(root, "bare")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: bare\ndescription: Declares no capabilities.\n---\n\n# X\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	src := Source{Name: "fixture", Root: root, Dialect: DialectAgent, Precedence: 10, Trust: TrustFirstParty}
	got, err := NewLoader().LoadSource(src)
	if err != nil {
		t.Fatalf("load must not refuse undeclared capabilities: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 skill, got %d", len(got))
	}
	// ...but the call boundary refuses everything it did not declare.
	log := NewAuditLog()
	if err := Use(got[0], "shell:exec", log); err == nil {
		t.Fatal("undeclared shell:exec on a bare skill must be refused at the call boundary")
	}
}

func TestAllowedToolsMapping(t *testing.T) {
	if redMode() {
		t.Skip("RED_MODE=1: mapping does not exist pre-fix (compile-level RED, see red.log)")
	}
	cases := map[string]string{
		"Read":        "filesystem:read",
		"Glob":        "filesystem:read",
		"Grep":        "filesystem:read",
		"Write":       "filesystem:write",
		"Edit":        "filesystem:write",
		"Bash":        "shell:exec",
		"Bash(cmd:*)": "shell:exec",
		"WebFetch":    "network:http",
		"WebSearch":   "network:http",
	}
	for in, want := range cases {
		got, ok := MapAllowedTools(in)
		if !ok || got != want {
			t.Fatalf("MapAllowedTools(%q) = %q,%v; want %q,true", in, got, ok, want)
		}
	}
	if _, ok := MapAllowedTools("NoSuchTool"); ok {
		t.Fatal("unknown tool must not map (never invent a permission)")
	}
	// A HelixAgent skill declaring `Bash` effectively declares shell:exec —
	// one policy language, not two (T-P6.03.3 direction).
	agentSkill := Skill{Name: "doer", Source: "agent", Capabilities: []string{"Bash"}}
	if !agentSkill.Allows("shell:exec") {
		t.Fatal("AllowedTools 'Bash' must imply capability shell:exec")
	}
	if agentSkill.Allows("network:http") {
		t.Fatal("AllowedTools 'Bash' must not imply network:http")
	}
}
