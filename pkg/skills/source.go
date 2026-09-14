package skills

import "fmt"

// Dialect is the closed set of source format dialects (spec §8, D-1).
// HelixSkills TOML is deliberately absent: blocked on A6's unimplemented
// cross-skill edge-write (T-P3.01.5 → T-P1.08.5). Adding it without that
// closure would invent a shape — a §11.4.6 violation.
type Dialect string

const (
	// DialectAgent is the canonical Agent Skills open standard
	// (name + description front-matter, uppercase SKILL.md).
	DialectAgent Dialect = "agent"
	// DialectHelixCode is HelixCode's triggers/variables/requires_isolation
	// front-matter with the name from the filename/directory.
	DialectHelixCode Dialect = "helixcode"
	// DialectHelixAgent is HelixAgent's name/allowed-tools/metadata
	// front-matter (internal/skills/types.go:12).
	DialectHelixAgent Dialect = "helixagent"
)

// TrustTier is the closed trust set (FR-022…FR-027; T-P7.05 C5 blocker:
// no third-party-tier source may be activated until a real sandbox
// executor lands upstream).
type TrustTier string

const (
	TrustFirstParty TrustTier = "first-party"
	TrustVendored   TrustTier = "vendored"
	TrustThirdParty TrustTier = "third-party"
)

// Source is a registered corpus (spec §8): name, root, format dialect,
// precedence, trust tier. Roots are PATH REFERENCES — the loader only ever
// reads in place and never copies a corpus (T-P4.02.5; D-6 keeps the
// HelixAgent tree where it is per §11.4.122).
type Source struct {
	Name       string    `json:"name"`
	Root       string    `json:"root"`
	Dialect    Dialect   `json:"dialect"`
	Precedence int       `json:"precedence"`
	Trust      TrustTier `json:"trust"`
}

// Validate rejects the unknown rather than coercing it (fail-closed).
func (s Source) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("skills: source with empty name (root %q)", s.Root)
	}
	for _, c := range s.Name {
		if c == '.' || c == '/' || c == ' ' {
			return fmt.Errorf("skills: source name %q contains %q: namespaced identity <source>.<name> requires dot-free source names", s.Name, string(c))
		}
	}
	if s.Root == "" {
		return fmt.Errorf("skills: source %q has empty root", s.Name)
	}
	switch s.Dialect {
	case DialectAgent, DialectHelixCode, DialectHelixAgent:
	default:
		return fmt.Errorf("skills: source %q has unknown dialect %q", s.Name, s.Dialect)
	}
	switch s.Trust {
	case TrustFirstParty, TrustVendored, TrustThirdParty:
	default:
		return fmt.Errorf("skills: source %q has unknown trust tier %q", s.Name, s.Trust)
	}
	return nil
}
