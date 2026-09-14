package db

// G27 — sanitizeTableName silently stripped invalid characters (turning
// "skills; DROP" into the wrong-but-valid "skillsDROP") instead of rejecting
// the name outright. This suite is the permanent regression guard (§11.4.135)
// for the reject-not-strip fix (§11.4.201): validateTableName MUST return a
// non-nil error for any non-conforming identifier and MUST NOT mutate the
// input into a passable-but-wrong table name.
//
// RED baseline (§11.4.115): revert validateTableName to the old strip-based
// body — return (stripped, nil) — and TestValidateTableName_RejectsInvalid
// FAILs, because "skills; DROP" then yields ("skillsDROP", nil) and the
// "expect error, empty name" assertions do not hold. Restoring the reject
// body flips it back to GREEN. This is the §1.1 paired mutation.

import (
	"strings"
	"testing"
)

// TestValidateTableName_RejectsInvalid proves that a non-conforming table name
// is REJECTED with an error and is NOT silently sanitised into a different,
// wrong-but-valid name.
func TestValidateTableName_RejectsInvalid(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"sql_injection_semicolon", "skills; DROP"},
		{"sql_injection_full", "skills; DROP TABLE users; --"},
		{"leading_digit", "1skills"},
		{"empty", ""},
		{"hyphen", "a-b"},
		{"embedded_space", "skills evidences"},
		{"single_quote", "skills'"},
		{"parenthesis", "skills(1)"},
		{"dot_qualified", "public.skills"},
		{"over_length_64", strings.Repeat("a", maxTableNameLen+1)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateTableName(tc.in)
			if err == nil {
				t.Fatalf("validateTableName(%q): expected a non-nil error (invalid name must be REJECTED, not stripped), got nil with result %q", tc.in, got)
			}
			if got != "" {
				t.Fatalf("validateTableName(%q): rejected names must return an empty string, got %q (a mutated/stripped name leaking through is the G27 defect)", tc.in, got)
			}
		})
	}
}

// TestValidateTableName_RejectsInjectionUnchanged pins the exact forensic
// defect: the injection payload must never be transformed into the stripped
// "skillsDROP" form and returned.
func TestValidateTableName_RejectsInjectionUnchanged(t *testing.T) {
	const payload = "skills; DROP"
	got, err := validateTableName(payload)
	if err == nil {
		t.Fatalf("validateTableName(%q): expected rejection, got nil error", payload)
	}
	if got == "skillsDROP" {
		t.Fatalf("validateTableName(%q): returned the stripped wrong-but-valid name %q — this is exactly the G27 strip-based foot-gun the fix removes", payload, got)
	}
}

// TestValidateTableName_AcceptsValid proves valid identifiers pass through
// UNCHANGED with a nil error.
func TestValidateTableName_AcceptsValid(t *testing.T) {
	valid := []string{
		"skills",
		"evidences",
		"a_b1",
		"_x",
		"Skills",
		strings.Repeat("a", maxTableNameLen), // exactly at the length bound
	}

	for _, in := range valid {
		t.Run(in, func(t *testing.T) {
			got, err := validateTableName(in)
			if err != nil {
				t.Fatalf("validateTableName(%q): expected nil error for a valid identifier, got %v", in, err)
			}
			if got != in {
				t.Fatalf("validateTableName(%q): valid name must be returned unchanged, got %q", in, got)
			}
		})
	}
}
