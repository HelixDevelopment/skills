package skillsource

import (
	"context"
	"fmt"
	"time"

	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
)

// Store provides data access for skill sources and their sync state.
type Store struct {
	pool   *db.Pool
	logger *zap.Logger
}

// NewStore creates a new skill source store. The logger defaults to a no-op
// so callers that do not need Store diagnostics can omit WithLogger.
func NewStore(pool *db.Pool, logger *zap.Logger) *Store {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Store{pool: pool, logger: logger}
}

// Create inserts a new skill source into the database. It assigns a new UUID
// if s.ID is zero, validates the source, and sets CreatedAt/UpdatedAt to now.
// Returns ErrSourceExists if a source with the same name already exists.
func (st *Store) Create(ctx context.Context, s *SkillSource) error {
	if err := s.Validate(); err != nil {
		return err
	}

	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}

	now := time.Now().UTC()
	s.CreatedAt = now
	s.UpdatedAt = now

	if s.SyncStatus == "" {
		s.SyncStatus = SyncStatusPending
	}

	const sql = `
		INSERT INTO skill_sources (id, name, source_type, config, enabled, last_sync, sync_status, error_message, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	_, err := st.pool.Exec(ctx, sql,
		s.ID, s.Name, string(s.SourceType), s.Config,
		s.Enabled, s.LastSync, string(s.SyncStatus), s.ErrorMessage,
		s.CreatedAt, s.UpdatedAt,
	)
	if err != nil {
		// pgx reports unique-violation as a pgconn.PgError with Code "23505".
		// Rather than importing pgconn, match on the error message pattern that
		// pgx surfaces for unique constraint violations.
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: %s", ErrSourceExists, s.Name)
		}
		return fmt.Errorf("create skill source: %w", err)
	}

	st.logger.Info("skill source created",
		zap.String("id", s.ID.String()),
		zap.String("name", s.Name),
		zap.String("source_type", string(s.SourceType)),
	)

	return nil
}

// GetByID retrieves a skill source by its unique identifier.
// Returns ErrSourceNotFound if no source with that ID exists.
func (st *Store) GetByID(ctx context.Context, id uuid.UUID) (*SkillSource, error) {
	const sql = `
		SELECT id, name, source_type, config, enabled, last_sync, sync_status, error_message, created_at, updated_at
		FROM skill_sources
		WHERE id = $1
	`
	return st.scanOne(st.pool.QueryRow(ctx, sql, id))
}

// GetByName retrieves a skill source by its unique name.
// Returns ErrSourceNotFound if no source with that name exists.
func (st *Store) GetByName(ctx context.Context, name string) (*SkillSource, error) {
	const sql = `
		SELECT id, name, source_type, config, enabled, last_sync, sync_status, error_message, created_at, updated_at
		FROM skill_sources
		WHERE name = $1
	`
	return st.scanOne(st.pool.QueryRow(ctx, sql, name))
}

// List returns all skill sources, optionally filtered to only enabled sources.
// Results are ordered by name for deterministic output.
func (st *Store) List(ctx context.Context, enabledOnly bool) ([]*SkillSource, error) {
	sql := `
		SELECT id, name, source_type, config, enabled, last_sync, sync_status, error_message, created_at, updated_at
		FROM skill_sources
	`
	if enabledOnly {
		sql += ` WHERE enabled = true`
	}
	sql += ` ORDER BY name`

	rows, err := st.pool.Query(ctx, sql)
	if err != nil {
		return nil, fmt.Errorf("list skill sources: %w", err)
	}
	defer rows.Close()

	var sources []*SkillSource
	for rows.Next() {
		s, err := st.scanRow(rows)
		if err != nil {
			return nil, err
		}
		sources = append(sources, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list skill sources rows: %w", err)
	}

	return sources, nil
}

// Update modifies an existing skill source. It refreshes UpdatedAt and
// validates the source before persisting. Returns ErrSourceNotFound if the
// source does not exist, or ErrSourceExists if the new name conflicts with
// another source.
func (st *Store) Update(ctx context.Context, s *SkillSource) error {
	if err := s.Validate(); err != nil {
		return err
	}

	s.UpdatedAt = time.Now().UTC()

	const sql = `
		UPDATE skill_sources
		SET name = $2, source_type = $3, config = $4, enabled = $5,
		    last_sync = $6, sync_status = $7, error_message = $8, updated_at = $9
		WHERE id = $1
	`
	tag, err := st.pool.Exec(ctx, sql,
		s.ID, s.Name, string(s.SourceType), s.Config,
		s.Enabled, s.LastSync, string(s.SyncStatus), s.ErrorMessage,
		s.UpdatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("%w: %s", ErrSourceExists, s.Name)
		}
		return fmt.Errorf("update skill source: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", ErrSourceNotFound, s.ID)
	}

	st.logger.Info("skill source updated",
		zap.String("id", s.ID.String()),
		zap.String("name", s.Name),
	)

	return nil
}

// Delete removes a skill source by its unique identifier.
// Returns ErrSourceNotFound if no source with that ID exists.
func (st *Store) Delete(ctx context.Context, id uuid.UUID) error {
	const sql = `DELETE FROM skill_sources WHERE id = $1`
	tag, err := st.pool.Exec(ctx, sql, id)
	if err != nil {
		return fmt.Errorf("delete skill source: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", ErrSourceNotFound, id)
	}

	st.logger.Info("skill source deleted", zap.String("id", id.String()))
	return nil
}

// UpdateSyncStatus is a targeted update that changes only the sync state
// fields (sync_status, error_message, last_sync) without touching the
// source's configuration. This is the hot-path method used by the sync
// worker to report progress without risking a stale-config overwrite.
//
// When status is SyncStatusCompleted or SyncStatusFailed, LastSync is
// automatically set to now. ErrorMessage is cleared on success and set on
// failure.
func (st *Store) UpdateSyncStatus(ctx context.Context, id uuid.UUID, status SyncStatus, errMsg string) error {
	if !status.IsValid() {
		return fmt.Errorf("%w: invalid sync status %q", ErrInvalidSource, status)
	}

	var lastSync *time.Time
	if status == SyncStatusCompleted || status == SyncStatusFailed {
		now := time.Now().UTC()
		lastSync = &now
	}
	if status == SyncStatusCompleted {
		errMsg = "" // clear stale error on success
	}

	const sql = `
		UPDATE skill_sources
		SET sync_status = $2, error_message = $3, last_sync = $4, updated_at = NOW()
		WHERE id = $1
	`
	tag, err := st.pool.Exec(ctx, sql, id, string(status), errMsg, lastSync)
	if err != nil {
		return fmt.Errorf("update sync status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", ErrSourceNotFound, id)
	}

	st.logger.Debug("skill source sync status updated",
		zap.String("id", id.String()),
		zap.String("status", string(status)),
	)

	return nil
}

// scanOne reads a single SkillSource from a pgx.Row. It translates
// pgx.ErrNoRows into ErrSourceNotFound.
func (st *Store) scanOne(row pgx.Row) (*SkillSource, error) {
	var s SkillSource
	var syncStatus string
	err := row.Scan(
		&s.ID, &s.Name, &s.SourceType, &s.Config,
		&s.Enabled, &s.LastSync, &syncStatus, &s.ErrorMessage,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrSourceNotFound
		}
		return nil, fmt.Errorf("scan skill source: %w", err)
	}
	s.SyncStatus = SyncStatus(syncStatus)
	return &s, nil
}

// scanRow reads a SkillSource from a pgx.Rows iterator.
func (st *Store) scanRow(rows pgx.Rows) (*SkillSource, error) {
	var s SkillSource
	var syncStatus string
	err := rows.Scan(
		&s.ID, &s.Name, &s.SourceType, &s.Config,
		&s.Enabled, &s.LastSync, &syncStatus, &s.ErrorMessage,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("scan skill source row: %w", err)
	}
	s.SyncStatus = SyncStatus(syncStatus)
	return &s, nil
}

// isUniqueViolation checks whether an error is a PostgreSQL unique constraint
// violation (SQLSTATE 23505). This avoids importing pgconn directly.
func isUniqueViolation(err error) bool {
	// pgx wraps pgconn.PgError whose Error() contains "SQLSTATE 23505".
	// A type-assert to the pgconn interface would be cleaner but requires
	// importing pgconn; matching the string is sufficient and keeps the
	// import surface minimal.
	if err == nil {
		return false
	}
	errStr := err.Error()
	return len(errStr) > 10 && (errStr[:10] == "ERROR: dup" || contains(errStr, "SQLSTATE 23505") || contains(errStr, "duplicate key"))
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
