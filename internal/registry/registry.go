// Package registry provides skill registry management and health monitoring
// for the HelixKnowledge system.
package registry

import (
	"context"
	"fmt"
	"time"

	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/skill"
	"github.com/jackc/pgx/v5"
)

// Registry manages the skill_registry table and provides operations for
// tracking skill health, coverage, and staleness.
type Registry struct {
	pool *db.Pool
}

// NewRegistry creates a new registry manager.
func NewRegistry(pool *db.Pool) *Registry {
	return &Registry{pool: pool}
}

// UpdateCoverage recalculates coverage scores for all skills based on their
// evidence validation status.
func (r *Registry) UpdateCoverage(ctx context.Context) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE skill_registry sr
		SET coverage = COALESCE((
			SELECT CASE
				WHEN COUNT(*) = 0 THEN 0.0
				ELSE COUNT(CASE WHEN e.validated THEN 1 END)::float / COUNT(*)
			END
			FROM evidences e
			WHERE e.skill_id = sr.skill_id
		), 0.0),
		last_review = NOW()
		WHERE EXISTS (
			SELECT 1 FROM skills s
			WHERE s.id = sr.skill_id
			AND s.status IN ('validated', 'active')
		)
	`)
	if err != nil {
		return fmt.Errorf("update coverage: %w", err)
	}
	if tag.RowsAffected() > 0 {
		// Log handled by caller
	}
	return nil
}

// CalculateMissingDeps updates the missing_deps array for all skills by
// checking that each dependency points to a valid, active skill.
func (r *Registry) CalculateMissingDeps(ctx context.Context) error {
	err := r.pool.WithTx(ctx, func(tx pgx.Tx) error {
		// Update all skills' missing deps
		_, err := tx.Exec(ctx, `
			UPDATE skill_registry sr
			SET missing_deps = COALESCE((
				SELECT array_agg(ds.name ORDER BY ds.name)
				FROM skill_dependencies sd
				JOIN skills ds ON sd.depends_on = ds.id
				WHERE sd.skill_id = sr.skill_id
				AND ds.status NOT IN ('validated', 'active')
			), '{}')
		`)
		if err != nil {
			return fmt.Errorf("calculate missing deps: %w", err)
		}

		// Set missing_deps to empty for skills with no dependencies at all
		_, err = tx.Exec(ctx, `
			UPDATE skill_registry sr
			SET missing_deps = '{}'
			WHERE NOT EXISTS (
				SELECT 1 FROM skill_dependencies sd
				WHERE sd.skill_id = sr.skill_id
			)
		`)
		if err != nil {
			return fmt.Errorf("clear empty missing deps: %w", err)
		}

		return nil
	})

	if err != nil {
		return err
	}

	return nil
}

// GetStaleSkills returns skills that have been marked as stale.
func (r *Registry) GetStaleSkills(ctx context.Context, limit int) ([]StaleSkillInfo, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := r.pool.Query(ctx, `
		SELECT sr.skill_id, sr.skill_name, sr.missing_deps, sr.stale, sr.last_review, sr.coverage
		FROM skill_registry sr
		WHERE sr.stale = true
		ORDER BY sr.coverage ASC, sr.skill_name
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("get stale skills: %w", err)
	}
	defer rows.Close()

	var skills []StaleSkillInfo
	for rows.Next() {
		var s StaleSkillInfo
		if err := rows.Scan(&s.SkillID, &s.SkillName, &s.MissingDeps, &s.Stale, &s.LastReview, &s.Coverage); err != nil {
			return nil, fmt.Errorf("scan stale skill: %w", err)
		}
		skills = append(skills, s)
	}

	return skills, nil
}

// StaleSkillInfo holds information about a stale skill.
type StaleSkillInfo struct {
	SkillID     string     `db:"skill_id"`
	SkillName   string     `db:"skill_name"`
	MissingDeps []string   `db:"missing_deps"`
	Stale       bool       `db:"stale"`
	LastReview  *time.Time `db:"last_review"`
	Coverage    float64    `db:"coverage"`
}

// GetCoverageReport returns a coverage report for the given domain. An empty
// domain reports across the entire skill graph. The computation is delegated
// to the skill store, which shares the same connection pool, so the registry
// and the REST/MCP layers all report identical coverage figures.
func (r *Registry) GetCoverageReport(ctx context.Context, domain string) (map[string]interface{}, error) {
	return skill.NewStore(r.pool).GetCoverage(ctx, domain)
}

// RefreshSkill marks a skill as reviewed (not stale) and updates its timestamp.
func (r *Registry) RefreshSkill(ctx context.Context, skillID string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE skill_registry
		SET stale = false, last_review = NOW()
		WHERE skill_id = $1
	`, skillID)
	if err != nil {
		return fmt.Errorf("refresh skill %s: %w", skillID, err)
	}
	return nil
}

// GetRegistryStats returns aggregate statistics about the skill registry.
func (r *Registry) GetRegistryStats(ctx context.Context) (RegistryStats, error) {
	var stats RegistryStats

	err := r.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) as total,
			COUNT(CASE WHEN stale THEN 1 END) as stale_count,
			COUNT(CASE WHEN array_length(missing_deps, 1) > 0 THEN 1 END) as missing_deps_count,
			COALESCE(AVG(coverage), 0.0) as avg_coverage
		FROM skill_registry
	`).Scan(&stats.TotalSkills, &stats.StaleSkills, &stats.MissingDepsCount, &stats.AverageCoverage)
	if err != nil {
		return stats, fmt.Errorf("get registry stats: %w", err)
	}

	return stats, nil
}

// RegistryStats holds aggregate statistics about the skill registry.
type RegistryStats struct {
	TotalSkills      int64   `json:"total_skills"`
	StaleSkills      int64   `json:"stale_skills"`
	MissingDepsCount int64   `json:"missing_deps_count"`
	AverageCoverage  float64 `json:"average_coverage"`
}
