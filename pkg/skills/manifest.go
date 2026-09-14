package skills

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// manifest is the on-disk `sources:` manifest schema (T-P4.02.2):
// name, root, dialect, precedence, trust tier per source.
//
// JSON — not YAML — is the manifest language, deliberately: pkg/skills is
// stdlib-only (R-25) and encoding/json parses exactly. A YAML manifest
// would need either a third-party dependency (forbidden) or a second
// hand-parser beside the SKILL.md one (unjustified). Roots may be relative;
// they resolve against the manifest's own directory (never CWD —
// CWD-relative roots are a §11.4.111 unstable-identity bluff).
type manifest struct {
	Sources []Source `json:"sources"`
}

// LoadManifest reads, parses and validates the sources manifest at path.
// Unknown dialects/trust tiers fail closed HERE (at registration), with the
// file path in the error — never deferred to load time.
func LoadManifest(path string) ([]Source, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("skills: cannot read sources manifest %q: %w", path, err)
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("skills: manifest %q is not valid JSON: %w", path, err)
	}
	if len(m.Sources) == 0 {
		return nil, fmt.Errorf("skills: manifest %q declares zero sources", path)
	}
	base := filepath.Dir(path)
	seen := map[string]bool{}
	for i, src := range m.Sources {
		if !filepath.IsAbs(src.Root) {
			src.Root = filepath.Join(base, src.Root)
		}
		if err := src.Validate(); err != nil {
			return nil, fmt.Errorf("skills: manifest %q source[%d]: %w", path, i, err)
		}
		if seen[src.Name] {
			return nil, &DuplicateSourceError{Name: src.Name}
		}
		seen[src.Name] = true
		m.Sources[i] = src
	}
	return m.Sources, nil
}
