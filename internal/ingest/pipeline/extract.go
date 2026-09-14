// Package pipeline implements the deterministic, LLM-free EXTRACT ->
// NORMALIZE -> map-to-models.Skill core of the ingestion pipeline
// (docs/research/.../DESIGN.md §2, stages 1-2, plus the "map to
// models.Skill" half of stage 6 CREATE). It deliberately stops BEFORE
// persistence: it never calls skill.Store.Create/AddResource/AddEvidence
// (internal/skill/store.go is flagged as concurrently under active
// integration elsewhere) and it never calls the LLM-REFINE stage (stage 3,
// a separate, later work item -- F2.4 in TRACKED_ITEMS.md -- that calls the
// EXISTING autoexpand.LLMClient interface). Everything in this package is
// pure and fully offline: no network, no database, no LLM call.
package pipeline

import (
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/HelixDevelopment/skills/internal/ingest/source"
)

// ErrUnsupportedContentType is returned by ExtractText when the RawItem's
// ContentType is not one this foundational increment's EXTRACT stage can
// handle. DESIGN.md §2 stage 1 assigns each source class its own
// content-specific extractor (goquery+readability for HTML,
// ledongthuc/pdf for PDF, kin-openapi for API schemas); only the
// plain-text and Markdown classes are implemented here -- HTML/PDF/OpenAPI
// extraction are separate, later work items (F1.3/F1.4/F1.5 +
// F2.1-F2.3 in TRACKED_ITEMS.md) that pull in new third-party
// dependencies this foundational increment deliberately avoids (per its
// "prefer zero new deps for the foundational item" guidance).
var ErrUnsupportedContentType = errors.New("ingest/pipeline: unsupported content type for EXTRACT in this increment")

// ErrEmptyExtractedText is returned when an item of a supported content
// type has no bytes to extract.
var ErrEmptyExtractedText = errors.New("ingest/pipeline: extracted text is empty")

// ErrNotValidUTF8 is returned when a supported-content-type item's bytes
// are not valid UTF-8 text.
var ErrNotValidUTF8 = errors.New("ingest/pipeline: not valid UTF-8 text")

// ExtractText extracts plain text from item's Body for the content types
// this increment supports (text/plain, text/markdown). It fails closed:
// a nil item, invalid UTF-8, an empty body, or an unsupported content type
// all return a typed error rather than a best-effort guess -- a per-item
// failure the caller (IngestAll, pipeline.go) records as a terminal
// outcome, never silently skips (DESIGN.md §2 stage 1's discovery-
// completeness discipline).
func ExtractText(item *source.RawItem) (string, error) {
	if item == nil {
		return "", errors.New("ingest/pipeline: nil RawItem")
	}
	switch item.ContentType {
	case "text/plain", "text/markdown":
		if len(item.Body) == 0 {
			return "", fmt.Errorf("%w: %q", ErrEmptyExtractedText, item.Ref.Path)
		}
		if !utf8.Valid(item.Body) {
			return "", fmt.Errorf("%w: %q", ErrNotValidUTF8, item.Ref.Path)
		}
		return string(item.Body), nil
	default:
		return "", fmt.Errorf("%w: %q (content-type %q)", ErrUnsupportedContentType, item.Ref.Path, item.ContentType)
	}
}
