package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Loader enumerates registered sources into canonical Skills.
// Pure data: directory walk + front-matter read. No network, no exec,
// no GUI, no third-party imports (R-25, T-P4.06 early).
type Loader struct{}

// NewLoader returns a Loader. There is deliberately no configuration:
// source roots are config-injected per Source (plan §4, §11.4.28(B)).
func NewLoader() *Loader { return &Loader{} }

// LoadSource enumerates every skill under src.Root and returns them sorted
// by directory name (deterministic, §11.4.50). Fail-closed:
//
//   - src must Validate (unknown dialect/trust rejected, never coerced)
//   - every skill directory must hold an exact-case SKILL.md
//     (case-insensitive filesystems cannot fake a pass: the check is
//     name-exact, not open-and-hope)
//   - the front-matter `name` must equal the directory name (spec §8)
//   - malformed front-matter is an error, never a silent skip (T-P6.04
//     direction: operator-visible failures)
//
// The source tree is only ever READ. LoadSource creates no files, so a
// corpus registered by path reference stays in place (T-P4.02.5).
func (l *Loader) LoadSource(src Source) ([]Skill, error) {
	if err := src.Validate(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(src.Root)
	if err != nil {
		return nil, fmt.Errorf("skills: source %q: cannot read root %q: %w", src.Name, src.Root, err)
	}
	var out []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		skill, err := l.loadDir(src, filepath.Join(src.Root, e.Name()), e.Name())
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				// Not a skill directory (no manifest at all): skip.
				// A PRESENT-but-malformed manifest is an error, never a
				// silent skip (T-P6.04 direction).
				continue
			}
			return nil, err
		}
		out = append(out, skill)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir < out[j].Dir })
	return out, nil
}

func (l *Loader) loadDir(src Source, dir, base string) (Skill, error) {
	manifest := filepath.Join(dir, "SKILL.md")
	raw, err := os.ReadFile(manifest)
	if err != nil {
		return Skill{}, fmt.Errorf("skills: source %q: directory %q has no exact-case SKILL.md: %w", src.Name, base, err)
	}
	fmBlock, body, err := splitFrontmatter(string(raw))
	if err != nil {
		return Skill{}, fmt.Errorf("skills: source %q: %s: %w", src.Name, manifest, err)
	}
	fm := parseFrontmatter(fmBlock)
	scalars, lists, maps, _ := fm.partition()

	name := scalars["name"]
	if src.Dialect == DialectHelixCode && name == "" {
		// HelixCode carries the name in the filename/directory, never
		// the front-matter (markdown_skills.go).
		name = base
	}
	if name == "" {
		return Skill{}, fmt.Errorf("skills: source %q: %s: front-matter carries no name", src.Name, manifest)
	}
	if name != base {
		return Skill{}, fmt.Errorf("skills: source %q: name %q does not match directory %q (spec §8)", src.Name, name, base)
	}

	sum := sha256.Sum256(raw)
	skill := Skill{
		Name:        name,
		Description: scalars["description"],
		Version:     scalars["version"],
		License:     scalars["license"],
		Body:        strings.TrimSpace(body),
		Source:      src.Name,
		Dir:         base,
		Trust:       src.Trust,
		Hash:        hex.EncodeToString(sum[:]),
	}
	if skill.Description == "" {
		return Skill{}, fmt.Errorf("skills: source %q: skill %q has empty description", src.Name, name)
	}

	switch src.Dialect {
	case DialectHelixCode:
		prof := &HelixCodeProfile{Variables: maps["variables"]}
		prof.Triggers = append(prof.Triggers, lists["triggers"]...)
		if scalars["requires_isolation"] == "true" {
			prof.RequiresIsolation = true
		}
		skill.XHelixCode = prof
		skill.Capabilities = append(skill.Capabilities, lists["capabilities"]...)
	case DialectHelixAgent:
		prof := &HelixAgentProfile{
			AllowedTools: scalars["allowed-tools"],
			Author:       scalars["author"],
			Category:     scalars["category"],
		}
		prof.Tags = append(prof.Tags, lists["tags"]...)
		skill.XHelixAgent = prof
		if prof.AllowedTools != "" {
			for _, t := range strings.Split(prof.AllowedTools, ",") {
				if t = strings.TrimSpace(t); t != "" {
					skill.Capabilities = append(skill.Capabilities, t)
				}
			}
		}
	default: // DialectAgent: canonical core only, nothing profiled.
		skill.Capabilities = append(skill.Capabilities, lists["capabilities"]...)
	}
	return skill, nil
}

// splitFrontmatter splits raw SKILL.md into its YAML front-matter block
// (between the leading `---` lines) and the body.
func splitFrontmatter(raw string) (block, body string, err error) {
	if !strings.HasPrefix(raw, "---\n") && !strings.HasPrefix(raw, "---\r\n") {
		return "", "", errNoFrontmatter
	}
	rest := raw[4:]
	if strings.HasPrefix(raw, "---\r\n") {
		rest = raw[5:]
	}
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return "", "", errNoFrontmatter
	}
	after := rest[idx+4:]
	after = strings.TrimPrefix(after, "\r")
	if !strings.HasPrefix(after, "\n") && after != "" {
		return "", "", errNoFrontmatter
	}
	return rest[:idx], strings.TrimPrefix(after, "\n"), nil
}
