package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/google/uuid"
)

// ErrNilCandidate is returned by MapToSkill when c is nil.
var ErrNilCandidate = errors.New("ingest/pipeline: nil CandidateSkill")

// ErrEmptyCandidateName is returned by MapToSkill when c.Name is empty.
var ErrEmptyCandidateName = errors.New("ingest/pipeline: CandidateSkill.Name is empty")

// resourceTypeIngested is the models.Resource.ResourceType value this
// increment assigns to every ingested resource. DESIGN.md's T0.2 work
// item widens the resources.resource_type CHECK constraint to add
// ingestion-specific values ("website-page", "pdf-book", ...) via a new
// migration -- that migration is a separate, not-yet-landed work item
// (T0.2 in TRACKED_ITEMS.md) and this additive-only increment must not
// add one. "article" is chosen deliberately because it is already a
// valid value under the CURRENT 001_initial.up.sql CHECK constraint (and
// is also that column's SQL DEFAULT), so a Skill produced here remains
// insertable without waiting on T0.2.
const resourceTypeIngested = "article"

// MapToSkill maps a CandidateSkill onto a NEW, in-memory *models.Skill +
// its single originating Resource (DESIGN.md §2 stage 6's "map to
// models.Skill" half of CREATE). It does NOT call skill.Store.Create --
// persistence, DEDUP-driven CREATE-vs-EXTEND branching, and graph-edge
// wiring are DESIGN.md §2 stages 5-7, separate, later work items (F2.5,
// F2.6, F2.6/F2.7) that this foundational increment deliberately does not
// implement (internal/skill/store.go is flagged as concurrently under
// active integration elsewhere).
//
// The returned Skill's Status is ALWAYS models.SkillStatusDraft -- never
// self-promoted -- mirroring the existing skill_create discipline
// (internal/mcp/tools.go) DESIGN.md §2 stage 6 cites. Its Kind is
// NormalizeOrAtomic()'d to models.SkillKindAtomic, matching the "each item
// becomes its own atomic/composite skill" rule for a single ingested
// file; grouping items under an umbrella skill is DESIGN.md §2 stage 6's
// multi-item-source case, itself part of the not-yet-implemented CREATE
// stage.
func MapToSkill(c *CandidateSkill) (*models.Skill, error) {
	if c == nil {
		return nil, ErrNilCandidate
	}
	if c.Name == "" {
		return nil, ErrEmptyCandidateName
	}

	metadata, err := json.Marshal(models.SkillMetadata{
		Tags:       c.Tags,
		Domain:     c.Domain,
		Complexity: c.Complexity,
	})
	if err != nil {
		return nil, fmt.Errorf("ingest/pipeline: marshal metadata: %w", err)
	}

	now := time.Now().UTC()
	resourceID := uuid.New()

	skill := &models.Skill{
		ID:          uuid.New(),
		Name:        c.Name,
		Version:     "0.1.0",
		Title:       c.Title,
		Description: c.Description,
		Content:     c.Content,
		Metadata:    metadata,
		Status:      models.SkillStatusDraft,
		Kind:        models.SkillKindAtomic.NormalizeOrAtomic(),
		CreatedAt:   now,
		UpdatedAt:   now,
		Resources: []models.Resource{
			{
				ID:           resourceID,
				URL:          ResourceLocator(c.SourceID, c.SourcePath),
				Title:        c.Title,
				ResourceType: resourceTypeIngested,
				FetchedHash:  c.FetchedHash,
				CreatedAt:    now,
			},
		},
	}
	return skill, nil
}

// ResourceLocator builds a stable, source-class-agnostic location string
// for an ingested item's originating Resource: "<sourceID>#<sourcePath>".
// A plain "file://" form is deliberately NOT used because sourceID is
// already the source's own opaque, protocol-carrying identifier
// (Source.ID(), DESIGN.md §1: "fs:...", "url:...", "ftp:...", ...) --
// concatenating it with the item's source-relative path gives one
// canonical location key that works identically across every future
// source class without this package special-casing any of them.
func ResourceLocator(sourceID, sourcePath string) string {
	return sourceID + "#" + sourcePath
}
