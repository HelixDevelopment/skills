package api

import (
	"testing"
)

// TestChaos_ResponseFormatConstants verifies all format constants are distinct
// and non-empty.
func TestChaos_ResponseFormatConstants(t *testing.T) {
	formats := []ResponseFormat{
		FormatJSON,
		FormatTOML,
		FormatTOON,
	}
	seen := make(map[ResponseFormat]bool)
	for _, f := range formats {
		if f == "" {
			t.Error("empty format constant")
		}
		if seen[f] {
			t.Errorf("duplicate format constant: %s", f)
		}
		seen[f] = true
	}
}

// TestChaos_ErrorResponse_EmptyFields verifies ErrorResponse with empty fields
// does not cause issues.
func TestChaos_ErrorResponse_EmptyFields(t *testing.T) {
	resp := ErrorResponse{}
	if resp.Error != "" {
		t.Error("expected empty error")
	}
}
