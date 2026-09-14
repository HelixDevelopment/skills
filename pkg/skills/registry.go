package skills

import (
	"fmt"
	"sort"
)

// DuplicateSkillError is the fail-closed collision verdict (T-P4.02.3,
// R-04, namespaced in T-P4.03): a duplicate QUALIFIED identity
// <source>.<name> — the same source+name twice (e.g. two directories in
// one source carrying one front-matter name). Same bare names from
// DIFFERENT sources coexist (TestCrossSourceCoexistence) and never reach
// this error. It names the skill and BOTH directories — never a bare
// boolean, never a silent resolve.
type DuplicateSkillError struct {
	Name         string
	FirstSource  string
	SecondSource string
	FirstDir     string
	SecondDir    string
}

func (e *DuplicateSkillError) Error() string {
	return fmt.Sprintf("skills: duplicate skill %q (qualified %s.%s): %s collides with %s — refusing to start (fail-closed, R-04)",
		e.Name, e.FirstSource, e.Name, e.FirstDir, e.SecondDir)
}

// DuplicateSourceError fires when the same source name is registered twice.
// A second registration can never silently replace the first.
type DuplicateSourceError struct {
	Name string
}

func (e *DuplicateSourceError) Error() string {
	return fmt.Sprintf("skills: duplicate source %q: already registered — refusing second registration", e.Name)
}

// displayPath prefers the root-relative path (distinguishes same-basename
// directories in deep trees) and falls back to the bare directory.
func displayPath(s Skill) string {
	if s.RelPath != "" {
		return s.RelPath
	}
	return s.Dir
}

// RegistryStats carries the union-count equality proof (T-P4.02.4):
// Total MUST equal the sum of PerSource, or a skill was silently dropped.
type RegistryStats struct {
	PerSource map[string]int
	Total     int
}

// Registry is the single authoritative enumeration over all registered
// sources (spec §8). Fail-closed on collision. Not safe for concurrent
// use — concurrent registrar/loader contention is T-P4.07.1 territory.
type Registry struct {
	sources []Source
	byName  map[string]Skill
	order   map[string]string // bare name -> owning source (for the error)
	counts  map[string]int
	loaded  bool
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		byName: map[string]Skill{},
		order:  map[string]string{},
		counts: map[string]int{},
	}
}

// RegisterSource validates and records src. The source is stored BY PATH
// REFERENCE (T-P4.02.5): no copy, no staging, no migration. Re-registering
// an existing name fails closed with *DuplicateSourceError.
func (r *Registry) RegisterSource(src Source) error {
	if err := src.Validate(); err != nil {
		return err
	}
	for _, s := range r.sources {
		if s.Name == src.Name {
			return &DuplicateSourceError{Name: src.Name}
		}
	}
	r.sources = append(r.sources, src)
	r.loaded = false
	return nil
}

// Sources returns the registered sources in deterministic
// (precedence, name) order.
func (r *Registry) Sources() []Source {
	out := append([]Source(nil), r.sources...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Precedence != out[j].Precedence {
			return out[i].Precedence < out[j].Precedence
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Load enumerates every registered source into one namespace keyed by
// QUALIFIED identity <source>.<name> (T-P4.03). On a duplicate qualified
// identity it returns *DuplicateSkillError and NO partial union —
// refusing to start, never resolving silently. Deterministic output order
// (source precedence, then skill name) per §11.4.50.
func (r *Registry) Load() ([]Skill, error) {
	loader := NewLoader()
	r.byName = map[string]Skill{}
	r.order = map[string]string{}
	r.counts = map[string]int{}
	for _, src := range r.Sources() {
		skills, err := loader.LoadSource(src)
		if err != nil {
			return nil, err
		}
		r.counts[src.Name] = len(skills)
		for _, s := range skills {
			q := s.Qualified()
			if owner, exists := r.byName[q]; exists {
				return nil, &DuplicateSkillError{
					Name:         s.Name,
					FirstSource:  r.order[q],
					SecondSource: src.Name,
					FirstDir:     displayPath(owner),
					SecondDir:    displayPath(s),
				}
			}
			r.byName[q] = s
			r.order[q] = src.Name
		}
	}
	out := make([]Skill, 0, len(r.byName))
	for _, s := range r.byName {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Name < out[j].Name
	})
	r.loaded = true
	return out, nil
}

// Stats reports per-source counts and the union total. Must be read after
// a successful Load; the standing integration test asserts
// Total == sum(PerSource) == len(Load()).
func (r *Registry) Stats() RegistryStats {
	per := map[string]int{}
	sum := 0
	for k, v := range r.counts {
		per[k] = v
		sum += v
	}
	total := sum
	if !r.loaded {
		total = -1 // explicitly not-a-count: Stats before Load is a caller bug
	}
	return RegistryStats{PerSource: per, Total: total}
}
