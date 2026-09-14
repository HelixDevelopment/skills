package skills

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Capability declaration and fail-closed refusal (HXC-159 T-P4.05, R-14).
//
// DECLARATION: each skill carries Capabilities (front-matter
// `capabilities:` for agent/helixcode dialects; HelixAgent `allowed-tools`
// mapped through MapAllowedTools below). Declaration is recorded at
// parse/load — never enforced there.
//
// ENFORCEMENT: Use(skill, capability, log) is the call boundary. A use
// the skill did not declare returns *CapabilityRefusedError and is
// recorded in the audit log. Undeclared use succeeding silently was the
// T-P4.05.1 defect; `03` §7.6 records the production analogue
// (HelixAgent's AllowedTools parsed and enforced nowhere).
//
// SINGLE POLICY LANGUAGE (T-P4.05 constraint, T-P6.03.3 direction):
// there is exactly one capability vocabulary — the four classes below.
// HelixAgent `allowed-tools` values are MAPPED onto it (MapAllowedTools),
// never a second enforcement language. The mapping table is data in this
// file; T-P6.03 consumes it when AllowedTools becomes enforcing.

// Canonical capability classes (closed set).
const (
	CapFilesystemRead  = "filesystem:read"
	CapFilesystemWrite = "filesystem:write"
	CapNetworkHTTP     = "network:http"
	CapShellExec       = "shell:exec"
)

// IsCapability reports whether c is a member of the closed vocabulary.
func IsCapability(c string) bool {
	switch c {
	case CapFilesystemRead, CapFilesystemWrite, CapNetworkHTTP, CapShellExec:
		return true
	}
	return false
}

// allowedToolsMap is the single-policy-language bridge: HelixAgent tool
// names (as parsed by ParseAllowedTools, e.g. "Read, Write, Bash(cmd:*)")
// onto canonical capability classes. Scoped forms (`Bash(cmd:*)`) reduce
// to their base tool — scope narrows WHAT the tool may touch, never
// WHETHER the capability class is needed.
var allowedToolsMap = map[string]string{
	"read":      CapFilesystemRead,
	"glob":      CapFilesystemRead,
	"grep":      CapFilesystemRead,
	"write":     CapFilesystemWrite,
	"edit":      CapFilesystemWrite,
	"bash":      CapShellExec,
	"webfetch":  CapNetworkHTTP,
	"websearch": CapNetworkHTTP,
}

// MapAllowedTools maps one HelixAgent allowed-tool token onto the
// canonical vocabulary. ok=false means unmapped — never invent a
// permission for an unknown tool (§11.4.6).
func MapAllowedTools(tool string) (cap string, ok bool) {
	base := strings.TrimSpace(tool)
	if i := strings.Index(base, "("); i >= 0 {
		base = base[:i]
	}
	base = strings.ToLower(strings.TrimSpace(base))
	cap, ok = allowedToolsMap[base]
	return cap, ok
}

// Allows reports whether s declares capability c — directly, or via the
// AllowedTools mapping (a skill declaring `Bash` declares shell:exec).
// Case-insensitive on both sides; surrounding whitespace ignored.
func (s Skill) Allows(c string) bool {
	want := strings.ToLower(strings.TrimSpace(c))
	for _, d := range s.Capabilities {
		dn := strings.ToLower(strings.TrimSpace(d))
		if dn == want {
			return true
		}
		if m, ok := MapAllowedTools(d); ok && strings.ToLower(m) == want {
			return true
		}
		if want == strings.ToLower(mappedBack(d)) {
			return true
		}
	}
	return false
}

// mappedBack covers the reverse spelling: a skill declaring the canonical
// class satisfies a request for the raw tool name mapping to it.
func mappedBack(declared string) string {
	dn := strings.ToLower(strings.TrimSpace(declared))
	for tool, class := range allowedToolsMap {
		if dn == class {
			return tool
		}
	}
	return ""
}

// CapabilityRefusedError is the RS-14 verdict: a fixture skill requesting
// an undeclared capability is refused at load/call time, typed and
// operator-visible. It names the skill, the refused capability, and what
// the skill DID declare — never a bare boolean.
type CapabilityRefusedError struct {
	Skill      string
	Capability string
	Declared   []string
}

func (e *CapabilityRefusedError) Error() string {
	return fmt.Sprintf("skills: %s requests undeclared capability %q (declared: [%s]) — refusing at the call boundary (fail-closed, R-14)",
		e.Skill, e.Capability, strings.Join(e.Declared, ", "))
}

// Use is the call boundary (T-P4.05.3): the single seam through which a
// caller exercises a skill capability. Declared use passes; undeclared
// use is refused AND audited. log may be nil (refusal still returned,
// audit skipped — callers that need the trail pass a log).
func Use(s Skill, capability string, log *AuditLog) error {
	if s.Allows(capability) {
		if log != nil {
			log.Record(s.Qualified(), capability, "allowed")
		}
		return nil
	}
	if log != nil {
		log.Record(s.Qualified(), capability, "refused")
	}
	return &CapabilityRefusedError{
		Skill:      s.Qualified(),
		Capability: capability,
		Declared:   append([]string(nil), s.Capabilities...),
	}
}

// AuditEntry is one call-boundary verdict (RS-14: the refusal appears in
// the audit record).
type AuditEntry struct {
	Time       string `json:"time"`
	Skill      string `json:"skill"`
	Capability string `json:"capability"`
	Verdict    string `json:"verdict"` // "allowed" | "refused"
}

// AuditLog is the append-only call-boundary trail. Mutex-guarded:
// concurrent registrar writes + loader reads contend here (T-P4.07.1).
type AuditLog struct {
	mu      sync.Mutex
	entries []AuditEntry
}

// NewAuditLog returns an empty audit log.
func NewAuditLog() *AuditLog { return &AuditLog{} }

// Record appends one verdict with a UTC timestamp.
func (l *AuditLog) Record(skill, capability, verdict string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, AuditEntry{
		Time:       time.Now().UTC().Format(time.RFC3339),
		Skill:      skill,
		Capability: capability,
		Verdict:    verdict,
	})
}

// Entries returns a copy of the trail in record order.
func (l *AuditLog) Entries() []AuditEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]AuditEntry(nil), l.entries...)
}

// SaveAtomic persists the trail as JSON without a torn-write window:
// write temp in the same directory, fsync, rename over the target. A
// SIGKILL at any point leaves either the old complete file or the new
// complete file — never a half-written one (T-P4.07.3 surface).
func (l *AuditLog) SaveAtomic(path string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return saveJSONAtomic(path, l.entries)
}

// writeJSON encodes v as indented JSON to f. A short write or encode
// error aborts the atomic save — the temp file is discarded, never renamed.
func writeJSON(f *os.File, v any) error {
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
// saveJSONAtomic writes v as indented JSON via temp-file + rename. It is
// the package's only file-writing primitive (the loader itself only
// reads): disk-full (ENOSPC, e.g. /dev/full) surfaces here as a real
// error from WriteFile/Sync, never a silent short write.
func saveJSONAtomic(path string, v any) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*.json")
	if err != nil {
		return fmt.Errorf("skills: audit save %q: %w", path, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("skills: audit save %q: %w", path, err)
	}
	if err := writeJSON(tmp, v); err != nil {
		tmp.Close()
		return fmt.Errorf("skills: audit save %q: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("skills: audit save %q: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("skills: audit save %q: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("skills: audit save %q: %w", path, err)
	}
	return nil
}
