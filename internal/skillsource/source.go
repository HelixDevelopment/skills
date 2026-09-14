// Package skillsource provides CRUD and sync-status management for registered
// skill sources — GitHub repos, filesystem paths, and URLs that supply
// SKILL.md files for the source-ingestion pipeline
// (internal/source/github, internal/source/mapper, internal/source/skillmd).
//
// A SkillSource is a persistent registry entry: it records WHERE skills come
// from, WHEN they were last synced, and WHETHER the sync succeeded. The actual
// fetch/parse/map work lives in sibling packages; this package owns only the
// registry bookkeeping.
package skillsource

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Sentinel errors returned by the skillsource store. Callers should compare
// against these with errors.Is rather than matching strings (mirrors the
// internal/skill package's own sentinel-error convention).
var (
	// ErrSourceNotFound indicates the requested skill source does not exist.
	ErrSourceNotFound = errors.New("skill source not found")
	// ErrSourceExists indicates a source with the same unique name already exists.
	ErrSourceExists = errors.New("skill source already exists")
	// ErrInvalidSource indicates a source failed structural or semantic validation.
	ErrInvalidSource = errors.New("invalid skill source")
)

// SourceType classifies where a skill source lives.
type SourceType string

const (
	// SourceTypeGitHub is a GitHub repository (owner/repo + optional ref/path).
	SourceTypeGitHub SourceType = "github"
	// SourceTypeFilesystem is a local or mounted directory path.
	SourceTypeFilesystem SourceType = "filesystem"
	// SourceTypeURL is a remote URL hosting one or more SKILL.md files.
	SourceTypeURL SourceType = "url"
)

// ValidSourceTypes is the exhaustive set of SourceType values the database
// CHECK constraint and Go validation accept.
var ValidSourceTypes = []SourceType{SourceTypeGitHub, SourceTypeFilesystem, SourceTypeURL}

// IsValid reports whether st is one of the recognized source types.
func (st SourceType) IsValid() bool {
	switch st {
	case SourceTypeGitHub, SourceTypeFilesystem, SourceTypeURL:
		return true
	default:
		return false
	}
}

// SyncStatus represents the lifecycle state of a source sync operation.
type SyncStatus string

const (
	// SyncStatusPending indicates the source has not been synced yet or is
	// waiting for the next sync cycle.
	SyncStatusPending SyncStatus = "pending"
	// SyncStatusSyncing indicates a sync is currently in progress.
	SyncStatusSyncing SyncStatus = "syncing"
	// SyncStatusCompleted indicates the last sync finished successfully.
	SyncStatusCompleted SyncStatus = "completed"
	// SyncStatusFailed indicates the last sync encountered an error.
	SyncStatusFailed SyncStatus = "failed"
)

// ValidSyncStatuses is the exhaustive set of SyncStatus values the database
// CHECK constraint and Go validation accept.
var ValidSyncStatuses = []SyncStatus{SyncStatusPending, SyncStatusSyncing, SyncStatusCompleted, SyncStatusFailed}

// IsValid reports whether ss is one of the recognized sync statuses.
func (ss SyncStatus) IsValid() bool {
	switch ss {
	case SyncStatusPending, SyncStatusSyncing, SyncStatusCompleted, SyncStatusFailed:
		return true
	default:
		return false
	}
}

// SkillSource represents a registered skill source in the knowledge graph.
// It records where skills come from (a GitHub repo, a filesystem path, or a
// URL), the source-type-specific configuration, and the state of the most
// recent sync attempt.
type SkillSource struct {
	// ID is the unique identifier for this source (auto-generated on create).
	ID uuid.UUID `json:"id" db:"id"`
	// Name is a human-readable, unique name for this source (e.g.
	// "claude-code-official-skills", "internal-knowledge-base").
	Name string `json:"name" db:"name"`
	// SourceType classifies the source kind (github, filesystem, url).
	SourceType SourceType `json:"source_type" db:"source_type"`
	// Config holds source-type-specific configuration as raw JSON. For GitHub
	// sources this includes owner, repo, ref, and path; for filesystem sources
	// it includes the root path; for URL sources it includes the endpoint.
	Config json.RawMessage `json:"config" db:"config"`
	// Enabled controls whether this source participates in sync cycles.
	Enabled bool `json:"enabled" db:"enabled"`
	// LastSync is the timestamp of the most recent completed (or failed) sync,
	// or nil if the source has never been synced.
	LastSync *time.Time `json:"last_sync,omitempty" db:"last_sync"`
	// SyncStatus is the state of the most recent sync operation.
	SyncStatus SyncStatus `json:"sync_status" db:"sync_status"`
	// ErrorMessage is the error string from the most recent failed sync, or
	// empty if the last sync succeeded or has not yet run.
	ErrorMessage string `json:"error_message,omitempty" db:"error_message"`
	// CreatedAt is the timestamp when this source was registered.
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	// UpdatedAt is the timestamp of the most recent modification.
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

// GitHubConfig is the structured Config shape for SourceTypeGitHub sources.
// Callers may marshal this to JSON and set it as SkillSource.Config.
type GitHubConfig struct {
	// Owner is the GitHub user or organization (e.g. "anthropics").
	Owner string `json:"owner"`
	// Repo is the repository name (e.g. "claude-code-skills").
	Repo string `json:"repo"`
	// Ref is the git ref to sync (branch, tag, or SHA). Defaults to "main"
	// when empty.
	Ref string `json:"ref,omitempty"`
	// Path is the subdirectory within the repo that contains SKILL.md files.
	// Defaults to the repo root when empty.
	Path string `json:"path,omitempty"`
}

// FilesystemConfig is the structured Config shape for SourceTypeFilesystem
// sources.
type FilesystemConfig struct {
	// RootPath is the absolute directory path to scan for SKILL.md files.
	RootPath string `json:"root_path"`
}

// URLConfig is the structured Config shape for SourceTypeURL sources.
type URLConfig struct {
	// Endpoint is the base URL hosting SKILL.md files.
	Endpoint string `json:"endpoint"`
}

// Validate performs structural validation on a SkillSource. It checks that
// required fields are present and that enum values are recognized. It does
// NOT check database-level constraints (uniqueness, foreign keys).
func (s *SkillSource) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidSource)
	}
	if !s.SourceType.IsValid() {
		return fmt.Errorf("%w: invalid source type %q", ErrInvalidSource, s.SourceType)
	}
	if s.Config == nil {
		// Nil config is treated as empty object — downstream JSON consumers
		// (e.g. GitHubConfig unmarshal) handle empty gracefully.
		s.Config = json.RawMessage("{}")
	}
	if s.SyncStatus != "" && !s.SyncStatus.IsValid() {
		return fmt.Errorf("%w: invalid sync status %q", ErrInvalidSource, s.SyncStatus)
	}
	return nil
}
