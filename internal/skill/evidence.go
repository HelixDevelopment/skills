package skill

import (
	"context"
	"errors"
	"fmt"

	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ---------------------------------------------------------------------------
// Evidence management
// ---------------------------------------------------------------------------

// AddEvidence attaches a new evidence record to a skill.
func (s *Store) AddEvidence(ctx context.Context, evidence *models.Evidence) error {
	if evidence.SkillID == uuid.Nil {
		return fmt.Errorf("%w: skill_id is required", ErrInvalidSkill)
	}
	if evidence.SourceProject == "" {
		return fmt.Errorf("%w: source_project is required", ErrInvalidSkill)
	}

	if evidence.ID == uuid.Nil {
		evidence.ID = uuid.New()
	}

	return s.pool.WithTx(ctx, func(tx pgx.Tx) error {
		// Verify skill exists
		var skillName string
		if err := tx.QueryRow(ctx, `SELECT name FROM skills WHERE id = $1`, evidence.SkillID).Scan(&skillName); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: %s", ErrSkillNotFound, evidence.SkillID)
			}
			return fmt.Errorf("check skill: %w", err)
		}

		_, err := tx.Exec(ctx, `
			INSERT INTO evidences (id, skill_id, source_project, source_file, code_snippet, pattern, language, validated, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
		`, evidence.ID, evidence.SkillID, evidence.SourceProject, evidence.SourceFile,
			evidence.CodeSnippet, evidence.Pattern, evidence.Language, evidence.Validated)
		if err != nil {
			return fmt.Errorf("insert evidence: %w", err)
		}

		// Update registry coverage
		if err := s.recalcCoverage(ctx, tx, evidence.SkillID); err != nil {
			return fmt.Errorf("recalc coverage: %w", err)
		}

		if err := s.logAudit(ctx, tx, "evidence.added", &evidence.SkillID, map[string]interface{}{
			"evidence_id":    evidence.ID,
			"source_project": evidence.SourceProject,
			"language":       evidence.Language,
			"pattern":        evidence.Pattern,
		}); err != nil {
			return fmt.Errorf("log audit: %w", err)
		}

		return nil
	})
}

// GetEvidence returns all evidence records attached to a skill.
func (s *Store) GetEvidence(ctx context.Context, skillID uuid.UUID) ([]models.Evidence, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, skill_id, source_project, source_file, code_snippet, pattern, language, validated, created_at
		FROM evidences
		WHERE skill_id = $1
		ORDER BY created_at DESC
	`, skillID)
	if err != nil {
		return nil, fmt.Errorf("query evidence: %w", err)
	}
	defer rows.Close()

	return scanEvidences(rows)
}

// GetEvidenceByProject returns all evidence records from a specific source project.
func (s *Store) GetEvidenceByProject(ctx context.Context, project string) ([]models.Evidence, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, skill_id, source_project, source_file, code_snippet, pattern, language, validated, created_at
		FROM evidences
		WHERE source_project = $1
		ORDER BY created_at DESC
	`, project)
	if err != nil {
		return nil, fmt.Errorf("query evidence by project: %w", err)
	}
	defer rows.Close()

	return scanEvidences(rows)
}

// ValidateEvidence marks an evidence record as validated.
func (s *Store) ValidateEvidence(ctx context.Context, evidenceID uuid.UUID) error {
	return s.pool.WithTx(ctx, func(tx pgx.Tx) error {
		var skillID uuid.UUID
		err := tx.QueryRow(ctx, `
			UPDATE evidences
			SET validated = true
			WHERE id = $1
			RETURNING skill_id
		`, evidenceID).Scan(&skillID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("evidence not found: %s", evidenceID)
			}
			return fmt.Errorf("validate evidence: %w", err)
		}

		// Recalculate coverage since validated evidence affects it
		if err := s.recalcCoverage(ctx, tx, skillID); err != nil {
			return fmt.Errorf("recalc coverage: %w", err)
		}

		if err := s.logAudit(ctx, tx, "evidence.validated", &skillID, map[string]interface{}{
			"evidence_id": evidenceID,
		}); err != nil {
			return fmt.Errorf("log audit: %w", err)
		}

		return nil
	})
}

// DeleteEvidence removes an evidence record.
func (s *Store) DeleteEvidence(ctx context.Context, evidenceID uuid.UUID) error {
	return s.pool.WithTx(ctx, func(tx pgx.Tx) error {
		var skillID uuid.UUID
		err := tx.QueryRow(ctx, `
			DELETE FROM evidences WHERE id = $1 RETURNING skill_id
		`, evidenceID).Scan(&skillID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("evidence not found: %s", evidenceID)
			}
			return fmt.Errorf("delete evidence: %w", err)
		}

		if err := s.recalcCoverage(ctx, tx, skillID); err != nil {
			return fmt.Errorf("recalc coverage: %w", err)
		}

		if err := s.logAudit(ctx, tx, "evidence.deleted", &skillID, map[string]interface{}{
			"evidence_id": evidenceID,
		}); err != nil {
			return fmt.Errorf("log audit: %w", err)
		}

		return nil
	})
}

// GetEvidenceByID retrieves a single evidence record by its ID.
func (s *Store) GetEvidenceByID(ctx context.Context, evidenceID uuid.UUID) (*models.Evidence, error) {
	var e models.Evidence
	err := s.pool.QueryRow(ctx, `
		SELECT id, skill_id, source_project, source_file, code_snippet, pattern, language, validated, created_at
		FROM evidences
		WHERE id = $1
	`, evidenceID).Scan(
		&e.ID, &e.SkillID, &e.SourceProject, &e.SourceFile,
		&e.CodeSnippet, &e.Pattern, &e.Language, &e.Validated, &e.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("evidence not found: %s", evidenceID)
		}
		return nil, fmt.Errorf("query evidence: %w", err)
	}
	return &e, nil
}

// GetEvidenceByLanguage returns all evidence records for a specific programming language.
func (s *Store) GetEvidenceByLanguage(ctx context.Context, language string) ([]models.Evidence, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, skill_id, source_project, source_file, code_snippet, pattern, language, validated, created_at
		FROM evidences
		WHERE language = $1
		ORDER BY created_at DESC
	`, language)
	if err != nil {
		return nil, fmt.Errorf("query evidence by language: %w", err)
	}
	defer rows.Close()

	return scanEvidences(rows)
}

// GetEvidenceByPattern returns evidence records matching a pattern (substring search).
func (s *Store) GetEvidenceByPattern(ctx context.Context, pattern string) ([]models.Evidence, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, skill_id, source_project, source_file, code_snippet, pattern, language, validated, created_at
		FROM evidences
		WHERE pattern ILIKE $1
		ORDER BY created_at DESC
	`, "%"+pattern+"%")
	if err != nil {
		return nil, fmt.Errorf("query evidence by pattern: %w", err)
	}
	defer rows.Close()

	return scanEvidences(rows)
}

// BulkAddEvidence adds multiple evidence records to a skill in a single transaction.
func (s *Store) BulkAddEvidence(ctx context.Context, skillID uuid.UUID, evidences []models.Evidence) error {
	if len(evidences) == 0 {
		return nil
	}

	return s.pool.WithTx(ctx, func(tx pgx.Tx) error {
		for i := range evidences {
			ev := &evidences[i]
			ev.SkillID = skillID
			if ev.ID == uuid.Nil {
				ev.ID = uuid.New()
			}

			_, err := tx.Exec(ctx, `
				INSERT INTO evidences (id, skill_id, source_project, source_file, code_snippet, pattern, language, validated, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
			`, ev.ID, ev.SkillID, ev.SourceProject, ev.SourceFile,
				ev.CodeSnippet, ev.Pattern, ev.Language, ev.Validated)
			if err != nil {
				return fmt.Errorf("insert evidence %d: %w", i, err)
			}
		}

		if err := s.recalcCoverage(ctx, tx, skillID); err != nil {
			return fmt.Errorf("recalc coverage: %w", err)
		}

		if err := s.logAudit(ctx, tx, "evidence.bulk_added", &skillID, map[string]interface{}{
			"count": len(evidences),
		}); err != nil {
			return fmt.Errorf("log audit: %w", err)
		}

		return nil
	})
}

// InvalidateEvidence marks an evidence record as not validated.
func (s *Store) InvalidateEvidence(ctx context.Context, evidenceID uuid.UUID) error {
	return s.pool.WithTx(ctx, func(tx pgx.Tx) error {
		var skillID uuid.UUID
		err := tx.QueryRow(ctx, `
			UPDATE evidences
			SET validated = false
			WHERE id = $1
			RETURNING skill_id
		`, evidenceID).Scan(&skillID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("evidence not found: %s", evidenceID)
			}
			return fmt.Errorf("invalidate evidence: %w", err)
		}

		if err := s.recalcCoverage(ctx, tx, skillID); err != nil {
			return fmt.Errorf("recalc coverage: %w", err)
		}

		if err := s.logAudit(ctx, tx, "evidence.invalidated", &skillID, map[string]interface{}{
			"evidence_id": evidenceID,
		}); err != nil {
			return fmt.Errorf("log audit: %w", err)
		}

		return nil
	})
}

// scanEvidences is a helper to scan pgx.Rows into a slice of Evidence.
func scanEvidences(rows pgx.Rows) ([]models.Evidence, error) {
	var evidences []models.Evidence
	for rows.Next() {
		var e models.Evidence
		if err := rows.Scan(
			&e.ID, &e.SkillID, &e.SourceProject, &e.SourceFile,
			&e.CodeSnippet, &e.Pattern, &e.Language, &e.Validated, &e.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan evidence: %w", err)
		}
		evidences = append(evidences, e)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate evidence: %w", err)
	}

	return evidences, nil
}
