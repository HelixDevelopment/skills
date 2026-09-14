package skills

import (
	"fmt"
	"sort"
)

// Allowlist activation (HXC-159 T-P4.04, R-19, R-28).
//
// The default active set is EMPTY: Registry.Load enumerates what is
// REGISTERED; only Activate admits skills into the ACTIVE set, and only
// for qualified identities named in the caller's allowlist. A directory
// walk therefore never implies activation.
//
// Per-consumer allowlist declaration: a JSON list of qualified identities
// `<source>.<name>` (see allowlist.go LoadAllowlist — manifest-adjacent,
// resolved against the same sources manifest). Unknown entries fail closed
// (a typo that silently matched nothing would be a §11.4.1 bluff).

// DefaultCeiling is the T-P2.03 measured output (ceiling.json): the
// shadowing curve was FLAT (spread 0.000 < 0.05, no knee), so the ceiling
// is PROVISIONAL at the largest tested N=20 — an honest finding per
// T-P2.03.3, not a buried one. Importing any other number here would be a
// §11.4.6 guess.
const DefaultCeiling = 20

// CeilingExceededError is the RS-05 verdict: startup with ceiling+1
// refuses and NAMES the offender. Never truncates silently.
type CeilingExceededError struct {
	Ceiling  int
	Count    int
	Offender string // qualified identity that crossed the ceiling
}

func (e *CeilingExceededError) Error() string {
	return fmt.Sprintf("skills: active set %d exceeds ceiling %d — refusing; offender %q (drop it from the allowlist or raise the ceiling with a re-measured curve, T-P2.03)",
		e.Count, e.Ceiling, e.Offender)
}

// UnknownAllowEntryError fires when the allowlist names a qualified
// identity that is not registered. Fail-closed: a misspelled entry that
// silently matched nothing would leave the operator believing a skill is
// active when it is not.
type UnknownAllowEntryError struct {
	Entry string
}

func (e *UnknownAllowEntryError) Error() string {
	return fmt.Sprintf("skills: allowlist entry %q matches no registered skill — refusing (fail-closed, typo would silently deactivate)", e.Entry)
}

// Activate admits exactly the allowlisted skills into the active set.
// allow holds qualified identities (<source>.<name>); nil/empty yields
// zero active skills. ceiling <= 0 selects DefaultCeiling.
//
// Order of enforcement (deterministic, §11.4.50):
//  1. resolve every entry or fail closed (unknown entry);
//  2. ceiling: active count > ceiling refuses, naming the offender —
//     the (ceiling+1)-th identity in sorted order;
//  3. similarity: any active pair above SimilarityThreshold refuses,
//     naming both (T-P2.04, RS-06).
//
// The registry must have been Load()ed; activating an unloaded registry
// yields the empty set (nothing registered, nothing active — never an
// error, since emptiness here is the safe default).
func (r *Registry) Activate(allow []string, ceiling int) ([]Skill, error) {
	if ceiling <= 0 {
		ceiling = DefaultCeiling
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.loaded {
		if len(allow) == 0 {
			return nil, nil
		}
		return nil, &UnknownAllowEntryError{Entry: allow[0]}
	}
	byQualified := map[string]Skill{}
	for _, s := range r.byName {
		byQualified[s.Qualified()] = s
	}
	var active []Skill
	for _, entry := range allow {
		s, ok := byQualified[entry]
		if !ok {
			return nil, &UnknownAllowEntryError{Entry: entry}
		}
		active = append(active, s)
	}
	sort.Slice(active, func(i, j int) bool { return active[i].Qualified() < active[j].Qualified() })
	if len(active) > ceiling {
		return nil, &CeilingExceededError{
			Ceiling:  ceiling,
			Count:    len(active),
			Offender: active[ceiling].Qualified(),
		}
	}
	if pair, score, ok := firstConfusablePair(active); ok {
		return nil, &SimilarityConflictError{
			A:         pair[0],
			B:         pair[1],
			Score:     score,
			Threshold: SimilarityThreshold,
		}
	}
	return active, nil
}

// firstConfusablePair returns the first active pair (sorted-identity order)
// whose description similarity exceeds the calibrated threshold.
func firstConfusablePair(active []Skill) ([2]string, float64, bool) {
	for i := 0; i < len(active); i++ {
		for j := i + 1; j < len(active); j++ {
			if s := Jaccard(active[i].Description, active[j].Description); s > SimilarityThreshold {
				return [2]string{active[i].Qualified(), active[j].Qualified()}, s, true
			}
		}
	}
	return [2]string{}, 0, false
}

// CorpusTwoPolicy documents T-P4.04.5 in the only place that can enforce
// it: corpus 2 (the HelixAgent tree) registers with TrustVendored and is
// NEVER added to a default allowlist. D1=A (2026-09-13) approved
// EXTRACTION — referencing the tree in place — while activation stays
// allowlist-only pending the T-P0.04.1 operator decision. A caller that
// wants corpus-2 skills names them explicitly; the empty default keeps
// them registered-but-dark.
//
// VendoredTierEnforced reports whether every skill in active comes from a
// non-third-party tier — the T-P7.05 C5 precondition (no third-party-tier
// source may be activated until a real sandbox executor lands upstream).
// It is a report, not a refusal: the refusal belongs to the sandbox gate,
// which does not exist yet; silently refusing here would conflate two
// mechanisms (§11.4.227 extend-don't-duplicate).
func VendoredTierEnforced(active []Skill) (thirdParty []string) {
	for _, s := range active {
		if s.Trust == TrustThirdParty {
			thirdParty = append(thirdParty, s.Qualified())
		}
	}
	return thirdParty
}
