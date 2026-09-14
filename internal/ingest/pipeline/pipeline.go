package pipeline

import (
	"context"
	"fmt"

	"github.com/HelixDevelopment/skills/internal/ingest/source"
	"github.com/HelixDevelopment/skills/internal/models"
)

// IngestOne runs the full source-side chain for exactly one item: Fetch ->
// ExtractText -> NormalizeContent -> BuildCandidate -> MapToSkill. It
// returns a fully-formed, in-memory *models.Skill (Status always Draft)
// ready for a LATER, separately-landed stage to DEDUP/persist/wire into
// the graph -- IngestOne itself never touches a database or the network
// beyond whatever src.Fetch itself does.
func IngestOne(ctx context.Context, src source.Source, ref source.ItemRef) (*models.Skill, error) {
	raw, err := src.Fetch(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("fetch %q: %w", ref.Path, err)
	}

	text, err := ExtractText(raw)
	if err != nil {
		return nil, fmt.Errorf("extract %q: %w", ref.Path, err)
	}

	normalized := NormalizeContent(text, ref)

	candidate, err := BuildCandidate(ref, raw.FetchedHash, normalized)
	if err != nil {
		return nil, fmt.Errorf("build candidate for %q: %w", ref.Path, err)
	}

	skill, err := MapToSkill(candidate)
	if err != nil {
		return nil, fmt.Errorf("map %q to skill: %w", ref.Path, err)
	}
	return skill, nil
}

// Outcome is the terminal, per-item result of one IngestOne call inside an
// IngestAll batch. Exactly one of Skill/Err is non-nil for every attempted
// item -- IngestAll enumerates every item List returned with a terminal
// outcome, it never silently drops one (DESIGN.md §2 stage 1's discovery-
// completeness discipline: "every extraction failure is captured ...
// NEVER silently skipped").
type Outcome struct {
	Ref   source.ItemRef
	Skill *models.Skill
	Err   error
}

// IngestAll lists every item in src and runs IngestOne against each one,
// collecting a terminal Outcome per item. A per-item failure (unsupported
// content type, empty file, escaped path, ...) does NOT abort the batch --
// it is recorded in that item's Outcome.Err and the batch continues, so
// one bad item among many good ones never loses the good ones' results.
func IngestAll(ctx context.Context, src source.Source) ([]Outcome, error) {
	refs, err := src.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", src.ID(), err)
	}

	outcomes := make([]Outcome, 0, len(refs))
	for _, ref := range refs {
		skill, err := IngestOne(ctx, src, ref)
		outcomes = append(outcomes, Outcome{Ref: ref, Skill: skill, Err: err})
	}
	return outcomes, nil
}
