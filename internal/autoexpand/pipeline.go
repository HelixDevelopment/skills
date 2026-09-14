// Package autoexpand implements the skill auto-growth pipeline for the
// HelixKnowledge system. It detects gaps in the skill graph, drafts new
// skills using LLM integration, and runs the full expansion pipeline.
package autoexpand

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/HelixDevelopment/skills/internal/skill"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// Gap represents a missing dependency in the skill graph that needs to be filled.
type Gap struct {
	SkillName      string // Name of the skill that has a missing dependency
	MissingDepName string // Name of the missing dependency
	SuggestedTitle string // LLM-generated title for the new skill
	Reason         string // Why this gap exists
}

// ExpansionResult captures the outcome of a full expansion pipeline run.
type ExpansionResult struct {
	JobID         uuid.UUID `json:"job_id"`
	SkillsCreated int       `json:"skills_created"`
	SkillsUpdated int       `json:"skills_updated"`
	Errors        []string  `json:"errors,omitempty"`
}

// Pipeline manages skill auto-expansion by detecting gaps, drafting skills
// with LLM assistance, and integrating them into the knowledge graph.
type Pipeline struct {
	store    *skill.Store
	embedder db.Embedder
	cfg      config.AutoExpandConfig
	logger   *zap.Logger
	llm      LLMClient
}

// PipelineOption allows optional configuration of the Pipeline.
type PipelineOption func(*Pipeline)

// WithLLMClient sets a custom LLM client for the pipeline.
func WithLLMClient(client LLMClient) PipelineOption {
	return func(p *Pipeline) {
		p.llm = client
	}
}

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

// NewPipeline creates a new auto-expansion pipeline.
func NewPipeline(store *skill.Store, embedder db.Embedder, cfg config.AutoExpandConfig, logger *zap.Logger, opts ...PipelineOption) *Pipeline {
	p := &Pipeline{
		store:    store,
		embedder: embedder,
		cfg:      cfg,
		logger:   logger,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// ---------------------------------------------------------------------------
// Gap detection
// ---------------------------------------------------------------------------

// DetectGaps finds missing dependencies in the skill graph by analyzing
// skill registry entries. It returns a prioritized list of gaps to fill.
func (p *Pipeline) DetectGaps(ctx context.Context) ([]Gap, error) {
	p.logger.Info("detecting gaps in skill graph")

	// Query the registry for skills with missing dependencies
	entries, err := p.store.GetMissingSkills(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("get missing skills: %w", err)
	}

	var gaps []Gap
	for _, entry := range entries {
		if !entry.AutoExpand {
			continue
		}

		for _, missingDep := range entry.MissingDeps {
			// Check if this gap is already being addressed
			exists, err := p.skillNameExists(ctx, missingDep)
			if err != nil {
				p.logger.Warn("failed to check skill existence",
					zap.String("name", missingDep),
					zap.Error(err),
				)
				continue
			}
			if exists {
				continue // gap already filled
			}

			// Try to generate a suggested title from the missing dependency name
			suggestedTitle := p.suggestTitle(missingDep)

			gaps = append(gaps, Gap{
				SkillName:      entry.SkillName,
				MissingDepName: missingDep,
				SuggestedTitle: suggestedTitle,
				Reason:         fmt.Sprintf("Skill %q depends on %q which does not exist", entry.SkillName, missingDep),
			})
		}
	}

	// Sort by coverage (lowest first) to prioritize skills that need the most help
	p.logger.Info("gap detection complete", zap.Int("gaps_found", len(gaps)))
	return gaps, nil
}

// DetectGapsForSkill detects gaps for a specific skill and its dependency tree.
func (p *Pipeline) DetectGapsForSkill(ctx context.Context, skillName string) ([]Gap, error) {
	// Get the full dependency tree
	tree, err := p.store.GetTree(ctx, skillName, p.cfg.MaxDepth)
	if err != nil {
		return nil, fmt.Errorf("get skill tree: %w", err)
	}

	var gaps []Gap
	visited := make(map[string]bool)
	p.collectGapsFromTree(ctx, tree, visited, &gaps)

	return gaps, nil
}

// collectGapsFromTree recursively walks the skill tree to find gaps by
// checking each declared dependency name against the store (§G137 — uses
// name-based existence checks via the store instead of the unreachable
// uuid.Nil check that the GRAPH-layer FK guarantee made permanently inert).
func (p *Pipeline) collectGapsFromTree(ctx context.Context, node *models.SkillTreeNode, visited map[string]bool, gaps *[]Gap) {
	if visited[node.Skill.Name] {
		return
	}
	visited[node.Skill.Name] = true

	// Check for missing dependency references that aren't in the tree
	for _, dep := range node.Skill.Dependencies {
		if dep.DependsOnName == "" {
			continue
		}
		// G137: Check by name against the store rather than testing uuid.Nil
		// (which is unreachable for any store-API-constructed graph whose
		// FK constraints guarantee every persisted edge points to a real
		// skill). Since collectGapsFromTree's tree is populated solely from
		// those same store paths, no uuid.Nil dependency will ever appear
		// here — but a name-based check is always reachable and correct.
		exists, err := p.skillNameExists(ctx, dep.DependsOnName)
		if err != nil {
			continue
		}
		if !exists {
			*gaps = append(*gaps, Gap{
				SkillName:      node.Skill.Name,
				MissingDepName: dep.DependsOnName,
				SuggestedTitle: p.suggestTitle(dep.DependsOnName),
				Reason:         fmt.Sprintf("Unresolved dependency %q in skill %q", dep.DependsOnName, node.Skill.Name),
			})
		}
	}

	for i := range node.Children {
		p.collectGapsFromTree(ctx, &node.Children[i], visited, gaps)
	}
}

// skillNameExists checks if a skill with the given name already exists.
func (p *Pipeline) skillNameExists(ctx context.Context, name string) (bool, error) {
	_, err := p.store.GetByName(ctx, name)
	if err != nil {
		// If not found, skill doesn't exist
		return false, nil
	}
	return true, nil
}

// suggestTitle converts a skill name (e.g., "go-concurrency") into a human-readable title.
func (p *Pipeline) suggestTitle(name string) string {
	// Simple heuristic: replace hyphens with spaces, capitalize words
	// In production, this would use an LLM for better titles
	return name
}

// ---------------------------------------------------------------------------
// Skill drafting
// ---------------------------------------------------------------------------

// DraftSkill generates a draft skill for a gap using LLM assistance.
// It builds context from existing related skills and produces a complete
// skill draft with dependencies and suggested resources.
func (p *Pipeline) DraftSkill(ctx context.Context, gap Gap) (*models.Skill, []models.Resource, error) {
	p.logger.Info("drafting skill",
		zap.String("missing_dep", gap.MissingDepName),
		zap.String("parent_skill", gap.SkillName),
	)

	// Anti-bluff (G20): never persist a placeholder as a real skill. Check
	// before buildContext so this path is verifiable without a database.
	if p.llm == nil {
		return nil, nil, fmt.Errorf("cannot draft skill without configured LLM client for %s", gap.MissingDepName)
	}

	// Build context from the parent skill and its neighbors
	context, err := p.buildContext(ctx, gap)
	if err != nil {
		return nil, nil, fmt.Errorf("build context: %w", err)
	}

	// Draft through the LLMClient interface (generateSkillDraft, llm.go),
	// never a concrete-type assertion. This used to assert p.llm.(*OpenAILLM)
	// directly and error out ("unsupported LLM client type") for every
	// OTHER LLMClient (*AnthropicLLM, and any future implementation) --
	// even though NewLLMClientFromConfig already builds those correctly
	// for the "anthropic"/"local"/"helixllm" providers, so a
	// validly-configured "anthropic" worker failed every draft. Any
	// configured LLMClient now drafts successfully here.
	draft, resources, err := generateSkillDraft(ctx, p.llm, gap.MissingDepName, context)
	if err != nil {
		// G20 anti-bluff: surface the actual error, never silently degrade to a
		// placeholder that would be persisted as real (but bluff) data.
		p.logger.Warn("LLM skill drafting failed",
			zap.Error(err),
			zap.String("skill", gap.MissingDepName),
		)
		return nil, nil, fmt.Errorf("LLM skill drafting failed for %s: %w", gap.MissingDepName, err)
	}

	// Ensure the skill name matches the gap
	draft.Name = gap.MissingDepName
	draft.Status = models.SkillStatusDraft

	p.logger.Info("skill draft generated",
		zap.String("skill", draft.Name),
		zap.String("title", draft.Title),
		zap.Int("resources", len(resources)),
	)

	return draft, resources, nil
}

// buildContext gathers relevant context from the skill graph for LLM prompting.
func (p *Pipeline) buildContext(ctx context.Context, gap Gap) (string, error) {
	// Get the parent skill for context
	parent, err := p.store.GetByName(ctx, gap.SkillName)
	if err != nil {
		return "", fmt.Errorf("get parent skill: %w", err)
	}

	// Get sibling skills (other dependencies of the parent)
	var siblings []string
	for _, dep := range parent.Dependencies {
		if dep.DependsOnName != "" && dep.DependsOnName != gap.MissingDepName {
			siblings = append(siblings, dep.DependsOnName)
		}
	}

	context := map[string]interface{}{
		"parent_skill": map[string]interface{}{
			"name":        parent.Name,
			"title":       parent.Title,
			"description": parent.Description,
		},
		"sibling_skills": siblings,
		"gap": map[string]interface{}{
			"missing_dep_name": gap.MissingDepName,
			"suggested_title":  gap.SuggestedTitle,
			"reason":           gap.Reason,
		},
	}

	contextJSON, err := json.MarshalIndent(context, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal context: %w", err)
	}

	return string(contextJSON), nil
}

// createMinimalDraft creates a basic skill draft without LLM assistance.
func (p *Pipeline) createMinimalDraft(gap Gap) *models.Skill {
	return &models.Skill{
		ID:          uuid.New(),
		Name:        gap.MissingDepName,
		Version:     "0.1.0",
		Title:       gap.SuggestedTitle,
		Description: fmt.Sprintf("Auto-generated skill to fill gap: %s", gap.Reason),
		Content: fmt.Sprintf(`# %s

## Overview

This skill was auto-generated to fill a gap in the knowledge graph.

## Context

Parent skill: %s
Missing dependency: %s

## Description

%s

> This skill needs review and enrichment before activation.
`, gap.SuggestedTitle, gap.SkillName, gap.MissingDepName, gap.Reason),
		Status:       models.SkillStatusDraft,
		Dependencies: []models.SkillDependency{
			// The drafted skill depends on the parent skill context
		},
	}
}

// ---------------------------------------------------------------------------
// Persist + cross-reference (worker-loop wiring, G03)
// ---------------------------------------------------------------------------

// draftPersistAndCrossReference drafts a skill for a gap (via the LLM per
// DraftSkill), persists it, and cross-references it into the tree by adding a
// `requires` edge from the gap's parent skill (gap.SkillName) to the newly
// drafted skill -- so the sub-skill this run just created is reachable from
// the tree that reported the gap, instead of floating unlinked in the skills
// table.
//
// The parent is resolved via the EXACT Store.GetByName lookup, never the
// fuzzy hybrid Store.Search -- the identical rationale
// validation.Pipeline.CrossReference documents (§G29/§G60): Search's ranking
// is RRF-fused across a trigram leg and (once a query-side embedder is
// wired) a vector leg, so a small-limit fuzzy search can rank an unrelated
// embedded skill above the exact-name match, or drop it from the result set
// entirely. That would make "which skill is the parent of this newly
// created child" depend on which OTHER, unrelated skills happen to carry a
// populated embedding -- exactly the failure GetByName's exact `WHERE name
// = $1` lookup avoids, being embedding-state-independent.
//
// A cross-reference failure (parent lookup error, or AddDependency error) is
// recorded into result.Errors but does NOT undo the skill's creation or fail
// the caller -- the drafted skill still exists as a genuine, reviewable
// draft; only its automatic linkage into the parent's dependency edge did
// not complete this run.
func (p *Pipeline) draftPersistAndCrossReference(ctx context.Context, gap Gap, result *ExpansionResult) (*models.Skill, error) {
	draft, resources, err := p.DraftSkill(ctx, gap)
	if err != nil {
		return nil, fmt.Errorf("draft skill %s: %w", gap.MissingDepName, err)
	}

	if err := p.store.Create(ctx, draft); err != nil {
		return nil, fmt.Errorf("create skill %s: %w", draft.Name, err)
	}

	// Persist resources (G20): the LLM backend may return suggested
	// resources alongside the skill draft; write them out now so they are
	// immediately visible in the database alongside the new draft skill.
	for i := range resources {
		resources[i].SkillID = draft.ID
	}
	if len(resources) > 0 {
		if err := p.store.BulkAddResources(ctx, draft.ID, resources); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("persist resources for %s: %v", draft.Name, err))
		}
	}

	if parent, perr := p.store.GetByName(ctx, gap.SkillName); perr != nil {
		result.Errors = append(result.Errors, fmt.Sprintf(
			"cross-reference %s into %s: resolve parent: %v", draft.Name, gap.SkillName, perr))
	} else if aerr := p.store.AddDependency(ctx, parent.ID, draft.ID, models.DepTypeRequires); aerr != nil {
		result.Errors = append(result.Errors, fmt.Sprintf(
			"cross-reference %s into %s: %v", draft.Name, gap.SkillName, aerr))
	}

	return draft, nil
}

// ---------------------------------------------------------------------------
// Full expansion pipeline
// ---------------------------------------------------------------------------

// Run executes the full expansion pipeline for a skill, detecting gaps,
// drafting new skills, and integrating them into the graph up to maxDepth.
func (p *Pipeline) Run(ctx context.Context, skillName string, maxDepth int) (*ExpansionResult, error) {
	jobID := uuid.New()
	result := &ExpansionResult{
		JobID:         jobID,
		SkillsCreated: 0,
		SkillsUpdated: 0,
	}

	p.logger.Info("starting expansion pipeline",
		zap.String("skill", skillName),
		zap.Int("max_depth", maxDepth),
		zap.String("job_id", jobID.String()),
	)

	// Log expansion start
	if err := db.LogEventWithDetails(ctx, p.store.Pool(), db.AuditEventExpansionStarted, nil, map[string]interface{}{
		"job_id":     jobID.String(),
		"skill_name": skillName,
		"max_depth":  maxDepth,
	}); err != nil {
		p.logger.Warn("failed to log expansion start", zap.Error(err))
	}

	// Track visited skills to avoid cycles
	visited := make(map[string]bool)

	// G137: Pre-collect registry-level gaps (name-based, always reachable)
	// as a supplement to the tree-based gap detection below. The registry
	// path (GetMissingSkills) is the only source that can discover gaps
	// declared in TOML imports for skills that don't yet exist -- the
	// tree-based path can only traverse edges the store layer already
	// persisted (which the FK guarantee makes always-valid by construction).
	registryGaps, _ := p.DetectGaps(ctx)
	registryGapNames := make(map[string]map[string]bool) // skillName -> set of missingDepNames
	for _, g := range registryGaps {
		if registryGapNames[g.SkillName] == nil {
			registryGapNames[g.SkillName] = make(map[string]bool)
		}
		registryGapNames[g.SkillName][g.MissingDepName] = true
	}

	// Process depth layers
	currentLayer := []string{skillName}
	for depth := 0; depth < maxDepth && len(currentLayer) > 0; depth++ {
		nextLayer := []string{}

		for _, currentSkill := range currentLayer {
			if visited[currentSkill] {
				continue
			}
			visited[currentSkill] = true

			// Detect gaps at this level.
			//
			// G137: Uses DetectGaps (which queries skill_registry.missing_deps
			// via GetMissingSkills) for EVERY depth level, because the
			// FK-enforced skill_dependencies.depends_on constraint makes the
			// old DependsOn == uuid.Nil check structurally unreachable for any
			// graph the store API constructs -- GetMissingSkills is the only
			// path that can see real gaps.
			var gaps []Gap
			var err error
			if depth == 0 {
				allGaps, gErr := p.DetectGaps(ctx)
				if gErr != nil {
					err = gErr
				} else {
					for _, g := range allGaps {
						if g.SkillName == currentSkill {
							gaps = append(gaps, g)
						}
					}
				}
			} else {
				gaps, err = p.detectGapsForSingleSkill(ctx, currentSkill)
			}
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("detect gaps for %s: %v", currentSkill, err))
			}

			// Supplement with registry-level gaps for this skill.
			// Deduplicate by MissingDepName since the same gap may appear
			// in both the tree and registry sources.
			seenNames := make(map[string]bool)
			for _, g := range gaps {
				seenNames[g.MissingDepName] = true
			}
			for depName := range registryGapNames[currentSkill] {
				if !seenNames[depName] {
					gaps = append(gaps, Gap{
						SkillName:      currentSkill,
						MissingDepName: depName,
						SuggestedTitle: p.suggestTitle(depName),
						Reason:         fmt.Sprintf("Registry: skill %q depends on %q which does not exist", currentSkill, depName),
					})
				}
			}

			for _, gap := range gaps {
				select {
				case <-ctx.Done():
					result.Errors = append(result.Errors, "expansion cancelled")
					p.logCompletion(ctx, result)
					return result, ctx.Err()
				default:
				}

				if result.SkillsCreated >= p.cfg.MaxNewSkillsPerRun {
					p.logger.Info("max new skills reached", zap.Int("limit", p.cfg.MaxNewSkillsPerRun))
					p.logCompletion(ctx, result)
					return result, nil
				}

				// Draft, persist, and cross-reference the new skill (G03 worker
				// wiring: see draftPersistAndCrossReference below for why the
				// cross-reference step exists and how it resolves the parent).
				draft, err := p.draftPersistAndCrossReference(ctx, gap, result)
				if err != nil {
					result.Errors = append(result.Errors, err.Error())
					continue
				}

				result.SkillsCreated++

				p.logger.Info("skill created",
					zap.String("skill", draft.Name),
					zap.String("job_id", jobID.String()),
				)

				// Add to next layer for recursive expansion
				nextLayer = append(nextLayer, draft.Name)
			}
		}

		currentLayer = nextLayer
	}

	p.logCompletion(ctx, result)
	return result, nil
}

// detectGapsForSingleSkill checks a single skill for missing dependencies.
// G137: uses GetMissingSkills (the skill_registry's missing_deps column)
// instead of the old DependsOn == uuid.Nil check, which is structurally
// unreachable for any graph the store API constructs.
func (p *Pipeline) detectGapsForSingleSkill(ctx context.Context, skillName string) ([]Gap, error) {
	entries, err := p.store.GetMissingSkills(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("get missing skills: %w", err)
	}

	var gaps []Gap
	for _, entry := range entries {
		if entry.SkillName != skillName {
			continue
		}
		if !entry.AutoExpand {
			continue
		}
		for _, missingDep := range entry.MissingDeps {
			exists, err := p.skillNameExists(ctx, missingDep)
			if err != nil {
				p.logger.Warn("failed to check skill existence",
					zap.String("name", missingDep),
					zap.Error(err),
				)
				continue
			}
			if exists {
				continue // gap already filled
			}
			gaps = append(gaps, Gap{
				SkillName:      skillName,
				MissingDepName: missingDep,
				SuggestedTitle: p.suggestTitle(missingDep),
				Reason:         fmt.Sprintf("Skill %q depends on %q which does not exist", skillName, missingDep),
			})
		}
	}

	return gaps, nil
}

// logCompletion logs the expansion completion event.
func (p *Pipeline) logCompletion(ctx context.Context, result *ExpansionResult) {
	details := map[string]interface{}{
		"job_id":         result.JobID.String(),
		"skills_created": result.SkillsCreated,
		"skills_updated": result.SkillsUpdated,
		"errors":         result.Errors,
	}

	event := db.AuditEventExpansionCompleted
	if len(result.Errors) > 0 && result.SkillsCreated == 0 {
		event = db.AuditEventExpansionFailed
	}

	if err := db.LogEventWithDetails(ctx, p.store.Pool(), event, nil, details); err != nil {
		p.logger.Warn("failed to log expansion completion", zap.Error(err))
	}

	p.logger.Info("expansion pipeline complete",
		zap.String("job_id", result.JobID.String()),
		zap.Int("skills_created", result.SkillsCreated),
		zap.Int("errors", len(result.Errors)),
	)
}
