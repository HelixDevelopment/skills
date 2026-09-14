package skillsource

// Audit event constants for skill-source operations. These string values are
// recorded in the audit log (models.AuditLogEntry.Event) to provide a
// structured, greppable history of source lifecycle and sync activity.
// Constants are preferred over free-form strings so that event consumers
// (dashboards, alerting, tests) can compare without risking typos.
//
// Naming convention: "source.<noun>.<verb>" for lifecycle events,
// "source.skill.<verb>" for per-skill import events.
const (
	// EventSourceRegistered is emitted when a new skill source is created in
	// the registry.
	EventSourceRegistered = "source.registered"

	// EventSourceUpdated is emitted when an existing skill source's
	// configuration or metadata is modified.
	EventSourceUpdated = "source.updated"

	// EventSourceDeleted is emitted when a skill source is removed from the
	// registry.
	EventSourceDeleted = "source.deleted"

	// EventSourceSyncStart is emitted at the beginning of a sync cycle for a
	// given source.
	EventSourceSyncStart = "source.sync.start"

	// EventSourceSyncEnd is emitted when a sync cycle completes successfully.
	EventSourceSyncEnd = "source.sync.end"

	// EventSourceSyncFailed is emitted when a sync cycle encounters an error.
	EventSourceSyncFailed = "source.sync.failed"

	// EventSkillImported is emitted for each skill that is successfully
	// imported (new) or updated (variant) during a sync cycle.
	EventSkillImported = "source.skill.imported"

	// EventSkillSkipped is emitted for each skill that is skipped during a
	// sync cycle (e.g. duplicate, license-gated, or invalid).
	EventSkillSkipped = "source.skill.skipped"
)
