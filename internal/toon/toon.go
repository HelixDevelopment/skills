// Package toon is the HelixKnowledge Skill Graph System's adapter for the
// Token-Oriented Object Notation (TOON) wire format (register gap G08).
//
// TOON is a compact, human-readable, schema-aware encoding of the JSON data
// model, purpose-built to reduce LLM token counts (no repeated keys in
// arrays-of-objects, no braces/brackets clutter). It is a distinct format,
// unrelated to TOML — the earlier "interpret TOON as TOML" note is superseded
// (see REQUIREMENTS.md and research/toon_go_codec.md).
//
// # Why a wrapper (and not a hand-rolled codec)
//
// Per the G08 decision doc (research/toon_go_codec.md §4), the codec is the
// org-official, MIT-licensed github.com/toon-format/toon-go, pinned by its exact
// pseudo-version in go.mod for reproducible builds (§11.4.108). This package is
// a thin adapter — it does NOT re-implement the grammar (a from-scratch encoder
// is a bug farm, §11.4.124). It isolates every direct call site behind a stable
// two-function surface so a future upstream API shift touches one file.
//
// # The JSON-data-model invariant (why Marshal normalizes through JSON)
//
// TOON represents the SAME logical document as JSON — only the wire bytes
// differ. Callers pass ordinary API model structs whose field naming lives in
// `json` struct tags (the codebase has no `toon` tags). The underlying library
// keys objects by Go FIELD NAME when no `toon` tag is present, which would make
// a `application/toon` response diverge from the `application/json` one (`ID:`
// vs `"id"`). To guarantee key/shape parity, Marshal first normalizes v through
// the JSON data model (json.Marshal → generic any) and then TOON-encodes that,
// so a client that asks for TOON receives exactly the document it would receive
// as JSON, in the compact TOON encoding. Unmarshal is the symmetric inverse.
//
// # Encoder options (spec-canonical, matches the golden fixtures)
//
// 2-space indent, comma delimiter, length markers OFF. The self-describing
// `[N]` element count is present in the array header in BOTH modes; the
// optional length-marker feature only adds a redundant `#` prefix (`[#2]`),
// which the canonical SPEC examples and the G08 golden vectors
// (research/toon_go_codec.md §5) do NOT use. We emit the canonical `[2]` form
// that matches those documented fixtures (§11.4.6 — assert against the real,
// documented output, not the illustrative prose).
package toon

import (
	"encoding/json"
	"fmt"

	toonlib "github.com/toon-format/toon-go"
)

// MediaType is the media type advertised and honored for the TOON wire format.
const MediaType = "application/toon"

// AltMediaType is an accepted alias for MediaType (mirrors the text/x-toml
// alias the TOML path accepts).
const AltMediaType = "text/x-toon"

// encoderOptions returns the fixed TOON encoder configuration (see package doc).
// Kept as a constructor (not a package var) so the option slice cannot be
// mutated by a caller and each call gets an independent slice.
func encoderOptions() []toonlib.EncoderOption {
	return []toonlib.EncoderOption{
		toonlib.WithIndent(2),
		toonlib.WithArrayDelimiter(toonlib.DelimiterComma),
		toonlib.WithLengthMarkers(false),
	}
}

// decoderOptions returns the fixed TOON decoder configuration, symmetric with
// encoderOptions (2-space indent step).
func decoderOptions() []toonlib.DecoderOption {
	return []toonlib.DecoderOption{
		toonlib.WithDecoderIndent(2),
	}
}

// jsonNormalize projects v onto the generic JSON data model
// (map[string]any / []any / float64 / string / bool / nil) so that TOON
// encoding sees the same keys/shape the JSON encoder would emit (json struct
// tags honored). This is the invariant that keeps application/toon and
// application/json responses in lock-step (see package doc).
func jsonNormalize(v any) (any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("toon: normalize via json: %w", err)
	}
	var norm any
	if err := json.Unmarshal(raw, &norm); err != nil {
		return nil, fmt.Errorf("toon: normalize via json: %w", err)
	}
	return norm, nil
}

// Marshal encodes v as a TOON document. v is normalized through the JSON data
// model first (see package doc), so the TOON output represents the identical
// logical document as the JSON output would.
func Marshal(v any) ([]byte, error) {
	norm, err := jsonNormalize(v)
	if err != nil {
		return nil, err
	}
	b, err := toonlib.Marshal(norm, encoderOptions()...)
	if err != nil {
		return nil, fmt.Errorf("toon: encode: %w", err)
	}
	return b, nil
}

// MarshalString is Marshal returning a string.
func MarshalString(v any) (string, error) {
	b, err := Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Decode parses a TOON document into a generic Go value (map[string]any /
// []any / float64 / string / bool / nil). A malformed document returns a
// non-nil error and MUST NOT be treated as an empty/zero value (§11.4.6 — no
// silent-fallback bluff, the exact danger G08 guards against).
func Decode(data []byte) (any, error) {
	decoded, err := toonlib.Decode(data, decoderOptions()...)
	if err != nil {
		return nil, fmt.Errorf("toon: decode: %w", err)
	}
	return decoded, nil
}

// Unmarshal decodes a TOON document into v (a non-nil pointer). It is the
// symmetric inverse of Marshal: the document is decoded to the JSON data model
// then re-encoded and json.Unmarshaled into v, so v's json struct tags are
// honored exactly as they are on the JSON request path.
func Unmarshal(data []byte, v any) error {
	decoded, err := Decode(data)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(decoded)
	if err != nil {
		return fmt.Errorf("toon: reproject via json: %w", err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("toon: bind into target: %w", err)
	}
	return nil
}
