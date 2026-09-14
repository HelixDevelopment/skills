package pipeline

import (
	"errors"
	"testing"
	"time"

	"github.com/HelixDevelopment/skills/internal/ingest/source"
)

func rawItem(contentType string, body []byte) *source.RawItem {
	return &source.RawItem{
		Ref:         source.ItemRef{SourceID: "fs:/root", Path: "a.md"},
		ContentType: contentType,
		Body:        body,
		FetchedAt:   time.Unix(0, 0).UTC(),
		FetchedHash: "deadbeef",
	}
}

func TestExtractText_MarkdownAndPlain_Supported(t *testing.T) {
	cases := []struct {
		contentType string
		body        string
	}{
		{"text/markdown", "# Title\n\nbody"},
		{"text/plain", "plain body"},
	}
	for _, tc := range cases {
		got, err := ExtractText(rawItem(tc.contentType, []byte(tc.body)))
		if err != nil {
			t.Fatalf("ExtractText(%q): %v", tc.contentType, err)
		}
		if got != tc.body {
			t.Errorf("ExtractText(%q) = %q, want %q", tc.contentType, got, tc.body)
		}
	}
}

func TestExtractText_UnsupportedContentType_Rejected(t *testing.T) {
	for _, ct := range []string{"text/html", "application/pdf", "application/json", "application/octet-stream", ""} {
		_, err := ExtractText(rawItem(ct, []byte("something")))
		if err == nil {
			t.Fatalf("ExtractText(%q) = nil error, want rejection", ct)
		}
		if !errors.Is(err, ErrUnsupportedContentType) {
			t.Errorf("ExtractText(%q) error = %v, want it to wrap ErrUnsupportedContentType", ct, err)
		}
	}
}

func TestExtractText_EmptyBody_Rejected(t *testing.T) {
	_, err := ExtractText(rawItem("text/markdown", []byte{}))
	if err == nil {
		t.Fatal("ExtractText with empty body = nil error, want rejection")
	}
	if !errors.Is(err, ErrEmptyExtractedText) {
		t.Errorf("error = %v, want it to wrap ErrEmptyExtractedText", err)
	}
}

func TestExtractText_InvalidUTF8_Rejected(t *testing.T) {
	invalid := []byte{0xff, 0xfe, 0xfd}
	_, err := ExtractText(rawItem("text/plain", invalid))
	if err == nil {
		t.Fatal("ExtractText with invalid UTF-8 = nil error, want rejection")
	}
	if !errors.Is(err, ErrNotValidUTF8) {
		t.Errorf("error = %v, want it to wrap ErrNotValidUTF8", err)
	}
}

func TestExtractText_NilItem_Rejected(t *testing.T) {
	if _, err := ExtractText(nil); err == nil {
		t.Fatal("ExtractText(nil) = nil error, want rejection")
	}
}
