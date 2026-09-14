package skill

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ---------------------------------------------------------------------------
// Resource management
// ---------------------------------------------------------------------------

// AddResource attaches a new external resource to a skill.
func (s *Store) AddResource(ctx context.Context, resource *models.Resource) error {
	if resource.SkillID == uuid.Nil {
		return fmt.Errorf("%w: skill_id is required", ErrInvalidSkill)
	}
	if resource.URL == "" {
		return fmt.Errorf("%w: resource URL is required", ErrInvalidSkill)
	}

	if resource.ID == uuid.Nil {
		resource.ID = uuid.New()
	}

	return s.pool.WithTx(ctx, func(tx pgx.Tx) error {
		// Verify skill exists
		var skillName string
		if err := tx.QueryRow(ctx, `SELECT name FROM skills WHERE id = $1`, resource.SkillID).Scan(&skillName); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: %s", ErrSkillNotFound, resource.SkillID)
			}
			return fmt.Errorf("check skill: %w", err)
		}

		_, err := tx.Exec(ctx, `
			INSERT INTO resources (id, skill_id, url, title, resource_type, fetched_hash, content_cached, last_validated, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
		`, resource.ID, resource.SkillID, resource.URL, resource.Title, resource.ResourceType,
			resource.FetchedHash, resource.ContentCached, resource.LastValidated)
		if err != nil {
			return fmt.Errorf("insert resource: %w", err)
		}

		// Update registry coverage
		if err := s.recalcCoverage(ctx, tx, resource.SkillID); err != nil {
			return fmt.Errorf("recalc coverage: %w", err)
		}

		if err := s.logAudit(ctx, tx, "resource.added", &resource.SkillID, map[string]interface{}{
			"resource_id":   resource.ID,
			"url":           resource.URL,
			"resource_type": resource.ResourceType,
		}); err != nil {
			return fmt.Errorf("log audit: %w", err)
		}

		return nil
	})
}

// GetResources returns all resources attached to a skill.
func (s *Store) GetResources(ctx context.Context, skillID uuid.UUID) ([]models.Resource, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, skill_id, url, title, resource_type, fetched_hash, content_cached, last_validated, created_at
		FROM resources
		WHERE skill_id = $1
		ORDER BY created_at DESC
	`, skillID)
	if err != nil {
		return nil, fmt.Errorf("query resources: %w", err)
	}
	defer rows.Close()

	var resources []models.Resource
	for rows.Next() {
		var r models.Resource
		if err := rows.Scan(
			&r.ID, &r.SkillID, &r.URL, &r.Title, &r.ResourceType,
			&r.FetchedHash, &r.ContentCached, &r.LastValidated, &r.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan resource: %w", err)
		}
		resources = append(resources, r)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate resources: %w", err)
	}

	return resources, nil
}

// UpdateResourceHash updates the content hash and validation timestamp for a resource.
func (s *Store) UpdateResourceHash(ctx context.Context, resourceID uuid.UUID, hash string) error {
	return s.pool.WithTx(ctx, func(tx pgx.Tx) error {
		var skillID uuid.UUID
		err := tx.QueryRow(ctx, `
			UPDATE resources
			SET fetched_hash = $1, last_validated = NOW()
			WHERE id = $2
			RETURNING skill_id
		`, hash, resourceID).Scan(&skillID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("resource not found: %s", resourceID)
			}
			return fmt.Errorf("update resource hash: %w", err)
		}

		if err := s.logAudit(ctx, tx, "resource.validated", &skillID, map[string]interface{}{
			"resource_id": resourceID,
			"hash":        hash,
		}); err != nil {
			return fmt.Errorf("log audit: %w", err)
		}

		return nil
	})
}

// DeleteResource removes a resource from a skill.
func (s *Store) DeleteResource(ctx context.Context, resourceID uuid.UUID) error {
	return s.pool.WithTx(ctx, func(tx pgx.Tx) error {
		var skillID uuid.UUID
		err := tx.QueryRow(ctx, `
			DELETE FROM resources WHERE id = $1 RETURNING skill_id
		`, resourceID).Scan(&skillID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("resource not found: %s", resourceID)
			}
			return fmt.Errorf("delete resource: %w", err)
		}

		// Recalculate coverage
		if err := s.recalcCoverage(ctx, tx, skillID); err != nil {
			return fmt.Errorf("recalc coverage: %w", err)
		}

		if err := s.logAudit(ctx, tx, "resource.deleted", &skillID, map[string]interface{}{
			"resource_id": resourceID,
		}); err != nil {
			return fmt.Errorf("log audit: %w", err)
		}

		return nil
	})
}

// recalcCoverage recalculates the coverage score for a skill based on resources and evidence.
func (s *Store) recalcCoverage(ctx context.Context, tx pgx.Tx, skillID uuid.UUID) error {
	// Coverage = weighted combination of:
	// - Has resources (30%)
	// - Has validated resources (20%)
	// - Has evidence (30%)
	// - Has validated evidence (20%)

	_, err := tx.Exec(ctx, `
		WITH stats AS (
			SELECT
				COALESCE((SELECT COUNT(*) FROM resources WHERE skill_id = $1), 0) AS resource_count,
				COALESCE((SELECT COUNT(*) FROM resources WHERE skill_id = $1 AND last_validated IS NOT NULL), 0) AS validated_resource_count,
				COALESCE((SELECT COUNT(*) FROM evidences WHERE skill_id = $1), 0) AS evidence_count,
				COALESCE((SELECT COUNT(*) FROM evidences WHERE skill_id = $1 AND validated = true), 0) AS validated_evidence_count
		)
		UPDATE skill_registry
		SET coverage = LEAST(1.0, (
			CASE WHEN stats.resource_count > 0 THEN 0.30 ELSE 0 END +
			CASE WHEN stats.validated_resource_count > 0 THEN 0.20 ELSE 0 END +
			CASE WHEN stats.evidence_count > 0 THEN 0.30 ELSE 0 END +
			CASE WHEN stats.validated_evidence_count > 0 THEN 0.20 ELSE 0 END
		))
		FROM stats
		WHERE skill_registry.skill_id = $1
	`, skillID)

	return err
}

// TouchSkillUpdatedAt updates the updated_at timestamp for a skill.
func (s *Store) TouchSkillUpdatedAt(ctx context.Context, skillID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE skills SET updated_at = NOW() WHERE id = $1`, skillID)
	return err
}

// GetResourceByID retrieves a single resource by its ID.
func (s *Store) GetResourceByID(ctx context.Context, resourceID uuid.UUID) (*models.Resource, error) {
	var r models.Resource
	err := s.pool.QueryRow(ctx, `
		SELECT id, skill_id, url, title, resource_type, fetched_hash, content_cached, last_validated, created_at
		FROM resources
		WHERE id = $1
	`, resourceID).Scan(
		&r.ID, &r.SkillID, &r.URL, &r.Title, &r.ResourceType,
		&r.FetchedHash, &r.ContentCached, &r.LastValidated, &r.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("resource not found: %s", resourceID)
		}
		return nil, fmt.Errorf("query resource: %w", err)
	}
	return &r, nil
}

// InvalidateResourceCache marks a resource's cached content as stale.
func (s *Store) InvalidateResourceCache(ctx context.Context, resourceID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE resources SET content_cached = '', fetched_hash = '', last_validated = NULL WHERE id = $1
	`, resourceID)
	return err
}

// BulkAddResources adds multiple resources to a skill in a single transaction.
func (s *Store) BulkAddResources(ctx context.Context, skillID uuid.UUID, resources []models.Resource) error {
	if len(resources) == 0 {
		return nil
	}

	return s.pool.WithTx(ctx, func(tx pgx.Tx) error {
		for i := range resources {
			resources[i].SkillID = skillID
			if resources[i].ID == uuid.Nil {
				resources[i].ID = uuid.New()
			}

			_, err := tx.Exec(ctx, `
				INSERT INTO resources (id, skill_id, url, title, resource_type, fetched_hash, content_cached, last_validated, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
			`, resources[i].ID, resources[i].SkillID, resources[i].URL, resources[i].Title,
				resources[i].ResourceType, resources[i].FetchedHash, resources[i].ContentCached,
				resources[i].LastValidated)
			if err != nil {
				return fmt.Errorf("insert resource %d: %w", i, err)
			}
		}

		if err := s.recalcCoverage(ctx, tx, skillID); err != nil {
			return fmt.Errorf("recalc coverage: %w", err)
		}

		if err := s.logAudit(ctx, tx, "resource.bulk_added", &skillID, map[string]interface{}{
			"count": len(resources),
		}); err != nil {
			return fmt.Errorf("log audit: %w", err)
		}

		return nil
	})
}

// GetResourcesNeedingValidation returns resources that haven't been validated recently.
func (s *Store) GetResourcesNeedingValidation(ctx context.Context, olderThan time.Duration) ([]models.Resource, error) {
	cutoff := time.Now().UTC().Add(-olderThan)
	rows, err := s.pool.Query(ctx, `
		SELECT id, skill_id, url, title, resource_type, fetched_hash, content_cached, last_validated, created_at
		FROM resources
		WHERE last_validated IS NULL OR last_validated < $1
		ORDER BY last_validated NULLS FIRST, created_at DESC
	`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("query stale resources: %w", err)
	}
	defer rows.Close()

	var resources []models.Resource
	for rows.Next() {
		var r models.Resource
		if err := rows.Scan(
			&r.ID, &r.SkillID, &r.URL, &r.Title, &r.ResourceType,
			&r.FetchedHash, &r.ContentCached, &r.LastValidated, &r.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan resource: %w", err)
		}
		resources = append(resources, r)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate resources: %w", err)
	}

	return resources, nil
}
