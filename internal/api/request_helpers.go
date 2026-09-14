package api

// request_helpers.go — standalone request parsing and TOML conversion utilities.
//
// Extracted from skills_handler.go during G01 dead-code cleanup. These functions
// are NOT methods on the dead *Server struct; they are general-purpose helpers
// used by tests and (future) alive handler paths.

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/HelixDevelopment/skills/internal/toon"
)

// parseRequestBody reads and parses the request body based on Content-Type.
// Supports JSON (default), TOML, and TOON formats.
func parseRequestBody(c *gin.Context, dst interface{}) error {
	contentType := c.ContentType()
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return fmt.Errorf("failed to read body: %w", err)
	}

	// Re-create body for potential re-reading
	c.Request.Body = io.NopCloser(strings.NewReader(string(bodyBytes)))

	switch {
	case strings.Contains(contentType, "application/toon") || strings.Contains(contentType, "text/x-toon"):
		return toon.Unmarshal(bodyBytes, dst)
	case strings.Contains(contentType, "application/toml") || strings.Contains(contentType, "text/x-toml"):
		return toml.Unmarshal(bodyBytes, dst)
	default:
		return json.Unmarshal(bodyBytes, dst)
	}
}

// defaultString returns val if non-empty, otherwise def.
func defaultString(val, def string) string {
	if val == "" {
		return def
	}
	return val
}

// defaultStatus returns val if non-empty, otherwise def.
func defaultStatus(val, def models.SkillStatus) models.SkillStatus {
	if val == "" {
		return def
	}
	return val
}

// convertTOMLWrapper converts a TOML skill wrapper to a Skill model.
func convertTOMLWrapper(w models.TOMLSkillWrapper) models.Skill {
	skill := models.Skill{
		ID:          uuid.New(),
		Name:        w.Skill.Name,
		Version:     defaultString(w.Skill.Version, "0.1.0"),
		Title:       w.Skill.Title,
		Description: w.Skill.Description,
		Content:     w.Skill.Content,
		Kind:        models.SkillKind(w.Skill.Kind).NormalizeOrAtomic(),
		Status:      models.SkillStatusDraft,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}

	// Convert metadata
	if w.Skill.Metadata.Domain != "" || len(w.Skill.Metadata.Tags) > 0 {
		metaBytes, _ := json.Marshal(w.Skill.Metadata)
		skill.Metadata = metaBytes
	}

	// Convert dependencies — carry the edge TARGET NAME (DependsOnName) for
	// every relation type. DependsOn (UUID) is left zero — name→ID resolution
	// + edge persistence is the store layer's job.
	appendDepNames := func(names []string, rel models.DependencyType) {
		for _, depName := range names {
			skill.Dependencies = append(skill.Dependencies, models.SkillDependency{
				RelationType:  rel,
				DependsOnName: depName,
			})
		}
	}
	appendDepNames(w.Skill.Dependencies.Requires, models.DepTypeRequires)
	appendDepNames(w.Skill.Dependencies.Extends, models.DepTypeExtends)
	appendDepNames(w.Skill.Dependencies.Recommends, models.DepTypeRecommends)
	appendDepNames(w.Skill.Dependencies.Composes, models.DepTypeComposes)
	appendDepNames(w.Skill.Dependencies.RelatedTo, models.DepTypeRelatedTo)
	appendDepNames(w.Skill.Dependencies.Alternative, models.DepTypeAlternative)

	// [[skill.components]] entries → composes edges with ordering/optionality
	for _, comp := range w.Skill.Components {
		order := comp.Order
		skill.Dependencies = append(skill.Dependencies, models.SkillDependency{
			RelationType:  models.DepTypeComposes,
			DependsOnName: comp.Name,
			Optional:      comp.Optional,
			SortOrder:     &order,
		})
	}

	// Convert resources
	for _, r := range w.Skill.Resources {
		skill.Resources = append(skill.Resources, models.Resource{
			ID:           uuid.New(),
			URL:          r.URL,
			Title:        r.Title,
			ResourceType: r.ResourceType,
		})
	}

	return skill
}

// convertTOMLBatch converts a batch of TOML wrappers to Skill models.
func convertTOMLBatch(wrappers []models.TOMLSkillWrapper) []models.Skill {
	skills := make([]models.Skill, 0, len(wrappers))
	for _, w := range wrappers {
		skills = append(skills, convertTOMLWrapper(w))
	}
	return skills
}

// exportToTOMLWrapper converts a Skill to a TOML export wrapper.
func exportToTOMLWrapper(skill *models.Skill) models.TOMLSkillWrapper {
	var meta models.SkillMetadata
	if skill.Metadata != nil {
		_ = json.Unmarshal(skill.Metadata, &meta)
	}

	wrapper := models.TOMLSkillWrapper{
		Skill: models.TOMLSkillDef{
			Name:        skill.Name,
			Version:     skill.Version,
			Title:       skill.Title,
			Description: skill.Description,
			Content:     skill.Content,
			Kind:        string(skill.Kind),
			Metadata:    meta,
		},
	}

	// Group dependencies by type. A composes edge carrying ordering/optionality
	// exports through the [[skill.components]] carrier so those attrs survive
	// the round-trip; a plain composes edge stays in the composes list.
	for _, dep := range skill.Dependencies {
		switch dep.RelationType {
		case models.DepTypeRequires:
			wrapper.Skill.Dependencies.Requires = append(wrapper.Skill.Dependencies.Requires, dep.DependsOnName)
		case models.DepTypeExtends:
			wrapper.Skill.Dependencies.Extends = append(wrapper.Skill.Dependencies.Extends, dep.DependsOnName)
		case models.DepTypeRecommends:
			wrapper.Skill.Dependencies.Recommends = append(wrapper.Skill.Dependencies.Recommends, dep.DependsOnName)
		case models.DepTypeComposes:
			if dep.SortOrder != nil || dep.Optional {
				order := 0
				if dep.SortOrder != nil {
					order = *dep.SortOrder
				}
				wrapper.Skill.Components = append(wrapper.Skill.Components, models.TOMLComponent{
					Name:     dep.DependsOnName,
					Order:    order,
					Optional: dep.Optional,
				})
			} else {
				wrapper.Skill.Dependencies.Composes = append(wrapper.Skill.Dependencies.Composes, dep.DependsOnName)
			}
		case models.DepTypeRelatedTo:
			wrapper.Skill.Dependencies.RelatedTo = append(wrapper.Skill.Dependencies.RelatedTo, dep.DependsOnName)
		case models.DepTypeAlternative:
			wrapper.Skill.Dependencies.Alternative = append(wrapper.Skill.Dependencies.Alternative, dep.DependsOnName)
		}
	}

	// Convert resources
	for _, r := range skill.Resources {
		wrapper.Skill.Resources = append(wrapper.Skill.Resources, models.TOMLResource{
			URL:          r.URL,
			Title:        r.Title,
			ResourceType: r.ResourceType,
		})
	}

	return wrapper
}
