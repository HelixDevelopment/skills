package skills

import (
	"fmt"
	"strings"
)

// Namespaced identity <source>.<name> (spec §8, T-P4.03.1).
//
// Qualification rule: QualifiedName(source, name) = source + "." + name.
// Escaping rule: NONE NEEDED, by construction —
//   - source names are dot-free (Source.Validate rejects dots/spaces/slashes),
//   - bare names may contain anything except "/" (impossible: Dir is a
//     single path segment) and may not be empty,
//   - ParseQualified splits on the FIRST dot, so dots in bare names are
//     preserved verbatim and qualification is unambiguous without an
//     escape character.
//
// Growth in one source can therefore never retroactively shadow another:
// "src-a.same" and "src-b.same" are distinct identities and coexist
// (T-P4.03.2). The fail-closed collision (DuplicateSkillError) now fires
// only on duplicate QUALIFIED identity — the same source+name twice,
// e.g. two directories in one source carrying one front-matter name —
// still naming both directories.
func QualifiedName(source, name string) string {
	return source + "." + name
}

// ParseQualified splits q into (source, name) on the first dot.
func ParseQualified(q string) (source, name string, err error) {
	idx := strings.Index(q, ".")
	if idx <= 0 || idx == len(q)-1 {
		return "", "", fmt.Errorf("skills: %q is not a qualified <source>.<name> identity", q)
	}
	return q[:idx], q[idx+1:], nil
}

// Qualified returns s's namespaced identity.
func (s Skill) Qualified() string {
	return QualifiedName(s.Source, s.Name)
}

// AssertNameMatchesDir reports the skills whose front-matter name differs
// from their directory (T-P4.03.3). A REPORT, not a refusal: legacy corpora
// systematically violate the D-1 authoring convention (measured 4/8 and
// 20/1315 on 2026-09-14) while neither production loader enforces it, so
// consumer gates run this over conforming sources and legacy corpora carry
// the result as a named gap. Empty return = the rule holds within the set.
func AssertNameMatchesDir(skills []Skill) []Skill {
	var bad []Skill
	for _, s := range skills {
		if s.NameMismatch || s.Name != s.Dir {
			bad = append(bad, s)
		}
	}
	return bad
}
