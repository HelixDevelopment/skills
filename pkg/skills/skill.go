package skills

// Skill is the canonical skill unit (spec §8, D-1): Agent Skills open
// standard core {name, description, version, body} plus profiled
// extension namespaces. Identity is <source>.<name> (T-P4.03); the
// bare Name still matches its directory within its source.
//
// The skill carries its trust tier, its DECLARED capability set and its
// content hash (spec §8). Declaration is not enforcement: the fail-closed
// refusal lands in T-P4.05 (RS-14). A skill requesting an undeclared
// capability today loads with the declaration recorded — never silently.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version,omitempty"`
	License     string `json:"license,omitempty"`
	Body        string `json:"body"`

	// Source is the owning source name; Dir is the skill directory.
	// Name USUALLY equals Dir (D-1 authoring convention for new portable
	// skills) but legacy corpora systematically differ ("Action Prefix
	// System" in dir "action-prefix-system"; "amazon aurora dsql" in
	// "amazon-aurora-dsql-skill" — measured 2026-09-14 on both production
	// corpora, and NEITHER production loader enforces the equality:
	// helix_agent/internal/skills/loader.go:44-99 registers by
	// front-matter name with no directory comparison). Rejecting the
	// mismatch at load would refuse both corpora the loader exists to
	// serve — a CONST-035 usability defect. So the mismatch is RECORDED
	// here and reported by AssertNameMatchesDir (T-P4.03.3), never a
	// load-time refusal.
	Source string `json:"source"`
	Dir    string `json:"dir"`

	// NameMismatch is true when Name != Dir. Consumer gates fail on it
	// for conforming sources; legacy corpora carry it as a named gap.
	NameMismatch bool `json:"name_mismatch,omitempty"`

	// Trust is the owning source's tier, carried per skill so consumers
	// can enforce tier policy without re-resolving the source.
	Trust TrustTier `json:"trust"`

	// Capabilities is the declared set (front-matter `capabilities:` or
	// the dialect-native allowance, e.g. HelixAgent `allowed-tools`).
	Capabilities []string `json:"capabilities,omitempty"`

	// Hash is the hex SHA-256 of the raw SKILL.md bytes (provenance
	// pinning lands in T-P7.03; the hash is captured at load already).
	Hash string `json:"hash"`

	// XHelixCode / XHelixAgent carry the dialect-native fields with no
	// standard equivalent. The HelixCode regex-with-named-captures
	// trigger model rides here as opaque strings, preserved byte-for-byte
	// (skillconv corpus finding: constitution skills are Agent-shaped,
	// HelixCode regex triggers opaque in x-helixcode).
	XHelixCode  *HelixCodeProfile  `json:"x-helixcode,omitempty"`
	XHelixAgent *HelixAgentProfile `json:"x-helixagent,omitempty"`
}

// HelixCodeProfile carries the HelixCode trigger/variable model.
type HelixCodeProfile struct {
	Triggers          []string          `json:"triggers,omitempty"`
	Variables         map[string]string `json:"variables,omitempty"`
	RequiresIsolation bool              `json:"requires_isolation,omitempty"`
}

// HelixAgentProfile carries the HelixAgent allowance/metadata model.
type HelixAgentProfile struct {
	AllowedTools string   `json:"allowed_tools,omitempty"`
	Author       string   `json:"author,omitempty"`
	Category     string   `json:"category,omitempty"`
	Tags         []string `json:"tags,omitempty"`
}
