// Package source defines the addressable-origin abstraction the skill
// ingestion pipeline pulls raw content from (F1.1 in
// docs/research/.../skill_ingestion_research/TRACKED_ITEMS.md; the design
// is docs/research/.../DESIGN.md §1).
//
// Every source class the ingestion feature will eventually support --
// local filesystem, http/website, PDF upload, OpenAPI schema, FTP, SMB,
// NFS, WebDAV -- implements the SAME Source interface defined here, so the
// pipeline that consumes a Source never branches on source type past this
// boundary. This package intentionally ships ONLY the contract plus the
// filesystem concrete implementation (the foundational, lowest-dependency
// concrete ingester); every other concrete Source is a separate,
// independently-landable work item (F1.3 http, F1.4 pdf, F1.5 api, F1.6
// ftp, F1.7 smb, F1.8 webdav per TRACKED_ITEMS.md) and MUST NOT be added
// to this file.
package source

import (
	"context"
	"time"
)

// Source is one addressable origin the ingestion pipeline can pull raw
// content from. Every source class implements this same interface.
type Source interface {
	// ID is a stable, deterministic identifier for this source instance
	// (e.g. "fs:/watched/root", "url:https://example.com/docs",
	// "ftp:ftp.example.com/pub") -- used as the dedup/idempotency key and
	// as the audit-trail correlation field once the pipeline persists
	// results (a later, separately-landed item).
	ID() string

	// List enumerates every currently-available Item under this source
	// (a one-shot bulk-ingest pass). For a directory-class source this is
	// a full walk; for a single URL/PDF-upload/API-schema source this
	// returns exactly one Item.
	List(ctx context.Context) ([]ItemRef, error)

	// Fetch retrieves the raw bytes + content-type for one ItemRef. Fetch
	// MUST NOT silently truncate an over-cap item -- it MUST reject with
	// an error instead, so a caller never persists a partially-read item.
	Fetch(ctx context.Context, ref ItemRef) (*RawItem, error)

	// Watchable reports whether this source supports Watch. A source that
	// only supports a one-shot bulk pass (e.g. a static upload, or a
	// filesystem source before its real-time watcher increment has
	// landed) returns false.
	Watchable() bool

	// Watch streams ItemRef change events (created/modified) until ctx is
	// cancelled. Watch is only ever called by a caller that has already
	// checked Watchable() == true; an implementation whose Watchable()
	// returns false MUST return a clear, typed error from Watch rather
	// than silently blocking or panicking, since a caller bug (calling
	// Watch anyway) must still fail safely and observably.
	Watch(ctx context.Context, events chan<- ItemRef) error
}

// ItemRef identifies one item a Source can Fetch, without yet reading its
// bytes.
type ItemRef struct {
	// SourceID is the owning Source's ID(), duplicated onto the ref so a
	// ref can be handed to a different Source instance's Fetch and be
	// rejected (defensive; a ref is only valid against the Source that
	// produced it).
	SourceID string
	// Path is the source-relative path/URL/filename of the item. For a
	// filesystem source this is a slash-separated path relative to the
	// source's root, never an absolute host path.
	Path string
	// Size is a best-effort byte-size hint known ahead of Fetch; 0 if
	// unknown.
	Size int64
	// ModTime is a best-effort last-modified hint; the zero time if
	// unknown.
	ModTime time.Time
}

// RawItem is the raw fetched content for one ItemRef.
type RawItem struct {
	Ref ItemRef
	// ContentType is a MIME-ish content class ("text/plain",
	// "text/markdown", "text/html", "application/pdf",
	// "application/json", ...). Which classes a given pipeline stage can
	// actually EXTRACT text from is a property of that stage, not of this
	// type -- RawItem itself never rejects a content type.
	ContentType string
	// Body is the fetched bytes, capped by the Source's own configured
	// max-item-size (fail-closed -- Fetch errors rather than truncating).
	Body []byte
	// FetchedAt is when Fetch actually ran.
	FetchedAt time.Time
	// FetchedHash is sha256(Body), hex-encoded.
	FetchedHash string
}
