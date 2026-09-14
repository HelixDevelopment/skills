// Package dedup provides a classifier that determines whether a parsed skill
// is NEW (no match found), DUPLICATE (exact name match), or VARIANT (normalized
// name match with different content). This implements G79 from the source
// ingestion tracked items.
package dedup

import (
	"strings"

	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Classification represents the dedup classification of a parsed skill.
type Classification string

const (
	// ClassificationNew indicates no match was found in the existing skill set.
	ClassificationNew Classification = "new"
	// ClassificationDuplicate indicates an exact name match was found.
	ClassificationDuplicate Classification = "duplicate"
	// ClassificationVariant indicates a similar (normalized) name match was found.
	ClassificationVariant Classification = "variant"
)

// ClassifyResult holds the classification outcome for a single skill.
type ClassifyResult struct {
	// Classification is the determined class (new, duplicate, or variant).
	Classification Classification
	// ExistingID is the ID of the existing skill if the classification is
	// duplicate or variant. Nil for new skills.
	ExistingID *uuid.UUID
	// Confidence is a 0.0-1.0 score indicating confidence in the classification.
	Confidence float64
	// Reason is a human-readable explanation of the classification decision.
	Reason string
}

// Classifier determines if a parsed skill is new, duplicate, or variant by
// comparing it against a known set of existing skills. It is safe for
// concurrent read access after construction (the underlying map is never
// mutated after NewClassifier returns).
type Classifier struct {
	// exact maps original skill name to the existing skill for O(1) lookup.
	exact map[string]*models.Skill
	// normalized maps normalized skill name to the existing skill for
	// variant detection.
	normalized map[string]*models.Skill
	logger     *zap.Logger
}

// NewClassifier creates a classifier seeded with the given existing skills.
// The caller must supply the full set of skills already persisted so the
// classifier can distinguish new from duplicate/variant entries.
func NewClassifier(existingSkills []*models.Skill, logger *zap.Logger) *Classifier {
	c := &Classifier{
		exact:      make(map[string]*models.Skill, len(existingSkills)),
		normalized: make(map[string]*models.Skill, len(existingSkills)),
		logger:     logger,
	}
	for _, s := range existingSkills {
		if s == nil {
			continue
		}
		c.exact[s.Name] = s
		norm := normalizeName(s.Name)
		// Only store the first skill seen for a given normalized name; this
		// mirrors the "first wins" behaviour of a real import pipeline.
		if _, exists := c.normalized[norm]; !exists {
			c.normalized[norm] = s
		}
	}
	return c
}

// Classify determines the classification of the given skill. The skill must
// have a non-empty Name; results are undefined for empty-name skills.
func (c *Classifier) Classify(skill *models.Skill) *ClassifyResult {
	if skill == nil {
		return &ClassifyResult{
			Classification: ClassificationNew,
			Confidence:     0.0,
			Reason:         "nil skill provided",
		}
	}

	name := skill.Name

	// 1. Exact match -> DUPLICATE.
	if existing, ok := c.exact[name]; ok {
		c.logger.Debug("dedup: exact name match",
			zap.String("name", name),
			zap.String("existing_id", existing.ID.String()),
		)
		return &ClassifyResult{
			Classification: ClassificationDuplicate,
			ExistingID:     &existing.ID,
			Confidence:     1.0,
			Reason:         "exact name match with existing skill",
		}
	}

	// 2. Normalized match -> VARIANT.
	norm := normalizeName(name)
	if existing, ok := c.normalized[norm]; ok {
		c.logger.Debug("dedup: normalized name match (variant)",
			zap.String("name", name),
			zap.String("normalized", norm),
			zap.String("existing_id", existing.ID.String()),
		)
		return &ClassifyResult{
			Classification: ClassificationVariant,
			ExistingID:     &existing.ID,
			Confidence:     0.8,
			Reason:         "similar name (normalized match) with existing skill",
		}
	}

	// 3. No match -> NEW.
	c.logger.Debug("dedup: no match found (new skill)",
		zap.String("name", name),
	)
	return &ClassifyResult{
		Classification: ClassificationNew,
		ExistingID:     nil,
		Confidence:     1.0,
		Reason:         "no matching skill found",
	}
}

// normalizeName lowercases the name and strips common separators (dots,
// hyphens, underscores, spaces) so that "my-skill", "my_skill", "my.skill",
// and "My Skill" all normalize to the same key.
func normalizeName(name string) string {
	n := strings.ToLower(name)
	n = strings.ReplaceAll(n, "-", "")
	n = strings.ReplaceAll(n, "_", "")
	n = strings.ReplaceAll(n, ".", "")
	n = strings.ReplaceAll(n, " ", "")
	return n
}
