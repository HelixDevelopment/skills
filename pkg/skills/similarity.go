package skills

import (
	"fmt"
	"strings"
)

// Description-similarity gate (HXC-159 T-P2.04, wired into load by T-P4.04.4).
//
// METRIC (Go reimplementation — stdlib only, never shelling out to
// python): Jaccard similarity over token sets. Tokens are lowercased
// alphanumeric runs (`[a-z0-9]+` on the lowered text — byte-equivalent to
// the calibration script's `re.findall(r"[a-z0-9]+", text.lower())`),
// with stopwords and single characters removed. Chosen for this corpus
// because descriptions are short prose where shared content words (not
// word order) drive confusion; deterministic, dependency-free,
// explainable per pair. Justification recorded here, not in literature.
//
// CALIBRATION (threshold.json): the metric is NON-SEPARABLE on this
// corpus (max known-distinct 0.0294 > min known-confusable 0.0256), so
// the rule falls back to threshold = min(known_confusable) = 0.0256 with
// fp_risk_accepted=true. The fallback is honest per the stated rule —
// and the accepted false-positive risk is why the refusal names both
// skills: the operator, not the gate, has the last word.

// SimilarityThreshold is the calibrated refusal threshold. NON-SEPARABLE
// fallback; fp risk accepted and documented (threshold.json).
const SimilarityThreshold = 0.0256

// stopwords is the exact calibration set from scripts/benchmark_skills/
// similarity.py (STOPWORDS). The Go metric MUST use this set verbatim —
// any drift silently re-calibrates the threshold (§11.4.6).
var stopwords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true,
	"of": true, "to": true, "in": true, "on": true, "for": true,
	"with": true, "as": true, "by": true, "at": true, "from": true,
	"is": true, "are": true, "was": true, "were": true, "be": true,
	"been": true, "it": true, "its": true, "this": true, "that": true,
	"these": true, "those": true, "you": true, "your": true, "we": true,
	"our": true, "they": true, "their": true, "he": true, "she": true,
	"him": true, "her": true, "them": true, "his": true, "hers": true,
	"ours": true, "theirs": true, "my": true, "mine": true, "use": true,
	"using": true, "used": true, "when": true, "whenever": true,
	"where": true, "which": true, "who": true, "whom": true,
	"whose": true, "what": true, "how": true, "why": true, "not": true,
	"no": true, "nor": true, "if": true, "then": true, "than": true,
	"so": true, "such": true, "can": true, "could": true, "should": true,
	"would": true, "may": true, "might": true, "must": true, "will": true,
	"shall": true, "do": true, "does": true, "did": true, "done": true,
	"have": true, "has": true, "had": true, "having": true, "any": true,
	"all": true, "each": true, "every": true, "both": true, "either": true,
	"neither": true, "one": true, "two": true, "more": true, "most": true,
	"other": true, "some": true, "over": true, "under": true, "into": true,
	"out": true, "about": true, "above": true, "below": true,
	"between": true, "through": true, "during": true, "before": true,
	"after": true, "per": true, "via": true, "also": true, "only": true,
	"just": true, "very": true, "too": true, "vs": true, "etc": true,
	"including": true, "include": true, "includes": true, "within": true,
	"without": true, "name": true, "skill": true, "system": true,
}

// Tokens returns the content-token set of text: lowercased [a-z0-9]+
// runs, stopwords and single characters removed.
func Tokens(text string) map[string]bool {
	lowered := strings.ToLower(text)
	set := map[string]bool{}
	start := -1
	flush := func(end int) {
		if start < 0 {
			return
		}
		tok := lowered[start:end]
		if len(tok) > 1 && !stopwords[tok] {
			set[tok] = true
		}
		start = -1
	}
	for i := 0; i < len(lowered); i++ {
		c := lowered[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			if start < 0 {
				start = i
			}
		} else {
			flush(i)
		}
	}
	flush(len(lowered))
	return set
}

// Jaccard returns the Jaccard similarity of the token sets of a and b.
// Both-empty scores 1.0, one-empty scores 0.0 (calibration-script parity).
func Jaccard(a, b string) float64 {
	ta, tb := Tokens(a), Tokens(b)
	if len(ta) == 0 && len(tb) == 0 {
		return 1.0
	}
	if len(ta) == 0 || len(tb) == 0 {
		return 0.0
	}
	inter := 0
	for t := range ta {
		if tb[t] {
			inter++
		}
	}
	union := len(ta) + len(tb) - inter
	return float64(inter) / float64(union)
}

// SimilarityConflictError is the RS-06 verdict: two active skills above
// the threshold refuse load, naming both (fp risk accepted — the operator
// resolves the pair, the gate never truncates silently).
type SimilarityConflictError struct {
	A, B      string
	Score     float64
	Threshold float64
}

func (e *SimilarityConflictError) Error() string {
	return fmt.Sprintf("skills: active pair %q || %q scores %.4f above similarity threshold %.4f (T-P2.04 NON-SEPARABLE fallback, fp risk accepted) — refusing; drop or rename one description",
		e.A, e.B, e.Score, e.Threshold)
}
