package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Audit log constants (event names)
// ---------------------------------------------------------------------------

// Common audit event names used throughout the skill graph system.
const (
	AuditEventSkillCreated        = "skill.created"
	AuditEventSkillUpdated        = "skill.updated"
	AuditEventSkillDeleted        = "skill.deleted"
	AuditEventSkillValidated      = "skill.validated"
	AuditEventSkillActivated      = "skill.activated"
	AuditEventSkillDeprecated     = "skill.deprecated"
	AuditEventDepAdded            = "dependency.added"
	AuditEventDepRemoved          = "dependency.removed"
	AuditEventEvidenceAdded       = "evidence.added"
	AuditEventEvidenceValidated   = "evidence.validated"
	AuditEventResourceAdded       = "resource.added"
	AuditEventResourceFetched     = "resource.fetched"
	AuditEventExpansionStarted    = "expansion.started"
	AuditEventExpansionCompleted  = "expansion.completed"
	AuditEventExpansionFailed     = "expansion.failed"
	AuditEventLearningStarted     = "learning.started"
	AuditEventLearningCompleted   = "learning.completed"
	AuditEventMigrationApplied    = "migration.applied"
	AuditEventMigrationRolledBack = "migration.rolled_back"
)

// ---------------------------------------------------------------------------
// Core audit functions
// ---------------------------------------------------------------------------

// LogEvent inserts a single event into the audit_log table.
//
// event should be one of the AuditEvent* constants. skillID may be nil
// for system-level events. details may be nil if no extra JSON data is
// associated with the event.
func LogEvent(
	ctx context.Context,
	pool *Pool,
	event string,
	skillID *uuid.UUID,
	details json.RawMessage,
) error {
	if details == nil {
		details = json.RawMessage("{}")
	}

	const query = `
		INSERT INTO audit_log (event, skill_id, details)
		VALUES ($1, $2, $3)
	`
	if _, err := pool.Exec(ctx, query, event, skillID, details); err != nil {
		return fmt.Errorf("insert audit event %q: %w", event, err)
	}

	return nil
}

// LogEventWithDetails is a convenience helper that marshals a details
// map into JSON and then calls LogEvent.
func LogEventWithDetails(
	ctx context.Context,
	pool *Pool,
	event string,
	skillID *uuid.UUID,
	details map[string]interface{},
) error {
	var raw json.RawMessage
	if len(details) > 0 {
		b, err := json.Marshal(details)
		if err != nil {
			return fmt.Errorf("marshal audit details for %q: %w", event, err)
		}
		raw = b
	}
	return LogEvent(ctx, pool, event, skillID, raw)
}

// ---------------------------------------------------------------------------
// Typed convenience loggers
// ---------------------------------------------------------------------------

// LogSkillChange records a skill create/update/delete event.
func LogSkillChange(
	ctx context.Context,
	pool *Pool,
	event string,
	skillID uuid.UUID,
	changes map[string]interface{},
) error {
	return LogEventWithDetails(ctx, pool, event, &skillID, changes)
}

// LogDependencyChange records a dependency addition or removal.
func LogDependencyChange(
	ctx context.Context,
	pool *Pool,
	event string,
	skillID uuid.UUID,
	dependsOn uuid.UUID,
	relationType string,
) error {
	return LogEventWithDetails(ctx, pool, event, &skillID, map[string]interface{}{
		"depends_on":    dependsOn.String(),
		"relation_type": relationType,
	})
}

// LogEvidenceChange records evidence addition or validation.
func LogEvidenceChange(
	ctx context.Context,
	pool *Pool,
	event string,
	skillID uuid.UUID,
	evidenceID uuid.UUID,
	extra map[string]interface{},
) error {
	details := map[string]interface{}{
		"evidence_id": evidenceID.String(),
	}
	for k, v := range extra {
		details[k] = v
	}
	return LogEventWithDetails(ctx, pool, event, &skillID, details)
}

// LogSystemEvent records a system-level event with no associated skill.
func LogSystemEvent(
	ctx context.Context,
	pool *Pool,
	event string,
	details map[string]interface{},
) error {
	return LogEventWithDetails(ctx, pool, event, nil, details)
}

// ---------------------------------------------------------------------------
// Audit log querying
// ---------------------------------------------------------------------------

// AuditLogEntry mirrors models.AuditLogEntry for database scanning.
type AuditLogEntry struct {
	Timestamp time.Time       `db:"ts"`
	Event     string          `db:"event"`
	SkillID   *uuid.UUID      `db:"skill_id"`
	Details   json.RawMessage `db:"details"`
}

// RecentAuditLog returns the most recent audit log entries, up to limit.
func RecentAuditLog(ctx context.Context, pool *Pool, limit int) ([]AuditLogEntry, error) {
	if limit <= 0 {
		limit = 100
	}

	const query = `
		SELECT ts, event, skill_id, details
		FROM audit_log
		ORDER BY ts DESC
		LIMIT $1
	`
	rows, err := pool.Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent audit log: %w", err)
	}
	defer rows.Close()

	var entries []AuditLogEntry
	for rows.Next() {
		var e AuditLogEntry
		if err := rows.Scan(&e.Timestamp, &e.Event, &e.SkillID, &e.Details); err != nil {
			return nil, fmt.Errorf("scan audit log entry: %w", err)
		}
		entries = append(entries, e)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("audit log rows iteration: %w", err)
	}

	return entries, nil
}

// AuditLogForSkill returns audit entries filtered to a specific skill.
func AuditLogForSkill(
	ctx context.Context,
	pool *Pool,
	skillID uuid.UUID,
	limit int,
) ([]AuditLogEntry, error) {
	if limit <= 0 {
		limit = 100
	}

	const query = `
		SELECT ts, event, skill_id, details
		FROM audit_log
		WHERE skill_id = $1
		ORDER BY ts DESC
		LIMIT $2
	`
	rows, err := pool.Query(ctx, query, skillID, limit)
	if err != nil {
		return nil, fmt.Errorf("query audit log for skill %s: %w", skillID, err)
	}
	defer rows.Close()

	var entries []AuditLogEntry
	for rows.Next() {
		var e AuditLogEntry
		if err := rows.Scan(&e.Timestamp, &e.Event, &e.SkillID, &e.Details); err != nil {
			return nil, fmt.Errorf("scan audit log entry: %w", err)
		}
		entries = append(entries, e)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("audit log rows iteration: %w", err)
	}

	return entries, nil
}

// AuditLogForEvent returns audit entries filtered to a specific event type.
func AuditLogForEvent(
	ctx context.Context,
	pool *Pool,
	event string,
	limit int,
) ([]AuditLogEntry, error) {
	if limit <= 0 {
		limit = 100
	}

	const query = `
		SELECT ts, event, skill_id, details
		FROM audit_log
		WHERE event = $1
		ORDER BY ts DESC
		LIMIT $2
	`
	rows, err := pool.Query(ctx, query, event, limit)
	if err != nil {
		return nil, fmt.Errorf("query audit log for event %q: %w", event, err)
	}
	defer rows.Close()

	var entries []AuditLogEntry
	for rows.Next() {
		var e AuditLogEntry
		if err := rows.Scan(&e.Timestamp, &e.Event, &e.SkillID, &e.Details); err != nil {
			return nil, fmt.Errorf("scan audit log entry: %w", err)
		}
		entries = append(entries, e)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("audit log rows iteration: %w", err)
	}

	return entries, nil
}

// ---------------------------------------------------------------------------
// Retention
// ---------------------------------------------------------------------------

// PruneAuditLog deletes audit log entries older than the given duration.
func PruneAuditLog(ctx context.Context, pool *Pool, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-olderThan)

	const query = `DELETE FROM audit_log WHERE ts < $1`
	tag, err := pool.inner.Exec(ctx, query, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune audit log older than %v: %w", olderThan, err)
	}

	rowsDeleted := tag.RowsAffected()
	zap.L().Info("audit log pruned",
		zap.Duration("older_than", olderThan),
		zap.Int64("rows_deleted", rowsDeleted),
	)

	return rowsDeleted, nil
}
