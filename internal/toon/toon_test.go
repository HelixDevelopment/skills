package toon

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// goldenVectors are the G08 conformance fixtures. Each pair is drawn from a real
// example in the TOON SPEC / README (research/toon_go_codec.md §5). The expected
// TOON is the codec's ACTUAL output captured under the fixed encoder options
// (2-space indent, comma delimiter, length markers OFF) — asserted byte-for-byte
// (§11.4.6: assert against real output, never illustrative prose).
//
// Two honest deltas from the design-doc §5 illustrative table, recorded here so
// the divergence is never silent:
//   - Vector "empty array": the codec emits the legacy `key[0]:` form, not the
//     `key: []` form the prose shows. Both are spec-valid; we assert what the
//     conforming encoder produces.
//   - Vector "tabular numeric": `14.50` canonicalizes to `14.5` (TOON drops
//     trailing fractional zeros) — the design doc's own ⚠ note predicted this.
var goldenVectors = []struct {
	name string
	json string
	toon string
}{
	{
		name: "simple object (keys sorted deterministically)",
		json: `{"id":1,"name":"Alice","active":true}`,
		toon: "active: true\nid: 1\nname: Alice",
	},
	{
		name: "inline primitive array with [N] count",
		json: `{"tags":["admin","ops","dev"]}`,
		toon: "tags[3]: admin,ops,dev",
	},
	{
		name: "tabular array-of-objects",
		json: `{"users":[{"id":1,"name":"Ada"},{"id":2,"name":"Linus"}]}`,
		toon: "users[2]{id,name}:\n  1,Ada\n  2,Linus",
	},
	{
		name: "tabular with numeric field (14.50 -> 14.5 canonical)",
		json: `{"items":[{"id":1,"name":"Alice","price":9.99},{"id":2,"name":"Bob","price":14.5}]}`,
		toon: "items[2]{id,name,price}:\n  1,Alice,9.99\n  2,Bob,14.5",
	},
	{
		name: "nested objects / significant indentation",
		json: `{"user":{"id":123,"name":"Ada","contact":{"email":"ada@example.com"}}}`,
		toon: "user:\n  contact:\n    email: ada@example.com\n  id: 123\n  name: Ada",
	},
	{
		name: "string quoting (colon / numeric-like / empty)",
		json: `{"url":"http://example.com:8080","version":"3.0","empty":""}`,
		toon: "empty: \"\"\nurl: \"http://example.com:8080\"\nversion: \"3.0\"",
	},
	{
		name: "empty array (legacy key[0]: form)",
		json: `{"key":[]}`,
		toon: "key[0]:",
	},
	{
		name: "root-level primitive array",
		json: `["a","b","c"]`,
		toon: "[3]: a,b,c",
	},
	{
		name: "mixed / non-uniform array (dash list)",
		json: `{"items":[1,{"a":1},"text"]}`,
		toon: "items[3]:\n  - 1\n  - a: 1\n  - text",
	},
	{
		name: "non-tabular objects (varying keys)",
		json: `{"items":[{"id":1,"name":"First"},{"id":2,"name":"Second","extra":true}]}`,
		toon: "items[2]:\n  - id: 1\n    name: First\n  - extra: true\n    id: 2\n    name: Second",
	},
	{
		name: "array of arrays (nested headers)",
		json: `{"pairs":[[1,2],[3,4]]}`,
		toon: "pairs[2]:\n  - [2]: 1,2\n  - [2]: 3,4",
	},
}

// TestGoldenVectors_Encode is the CONTRACT test: JSON input -> Marshal -> exact
// expected TOON, byte-for-byte. This is the fixture the §1.1 mutation pair
// targets — swapping the codec to JSON (or flipping length markers) makes these
// exact-string assertions fail.
func TestGoldenVectors_Encode(t *testing.T) {
	for _, v := range goldenVectors {
		v := v
		t.Run(v.name, func(t *testing.T) {
			var in any
			if err := json.Unmarshal([]byte(v.json), &in); err != nil {
				t.Fatalf("fixture json invalid: %v", err)
			}
			got, err := Marshal(in)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(got) != v.toon {
				t.Errorf("TOON mismatch\n got: %q\nwant: %q", string(got), v.toon)
			}
		})
	}
}

// TestGoldenVectors_RoundTrip is the UNIT round-trip test: for every fixture,
// JSON -> Marshal -> Unmarshal deep-equals the original JSON data model. Proves
// the codec is value-preserving in both directions.
func TestGoldenVectors_RoundTrip(t *testing.T) {
	for _, v := range goldenVectors {
		v := v
		t.Run(v.name, func(t *testing.T) {
			var in any
			if err := json.Unmarshal([]byte(v.json), &in); err != nil {
				t.Fatalf("fixture json invalid: %v", err)
			}
			toonBytes, err := Marshal(in)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var back any
			if err := Unmarshal(toonBytes, &back); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !reflect.DeepEqual(in, back) {
				t.Errorf("round-trip mismatch\n orig: %#v\n back: %#v", in, back)
			}
		})
	}
}

// TestGoldenVectors_DecodeMatchesJSON proves the decode half independently:
// expected TOON -> Decode deep-equals the JSON input's data model.
func TestGoldenVectors_DecodeMatchesJSON(t *testing.T) {
	for _, v := range goldenVectors {
		v := v
		t.Run(v.name, func(t *testing.T) {
			var want any
			if err := json.Unmarshal([]byte(v.json), &want); err != nil {
				t.Fatalf("fixture json invalid: %v", err)
			}
			got, err := Decode([]byte(v.toon))
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if !reflect.DeepEqual(want, got) {
				t.Errorf("decode mismatch\n want: %#v\n got: %#v", want, got)
			}
		})
	}
}

// skillLike mirrors an API model: json-tagged, no toon tags. Proves Marshal
// keys the TOON output by the JSON field names (via jsonNormalize), NOT the Go
// field names — the invariant that keeps application/toon and application/json
// responses in lock-step. Without normalization the underlying library would
// emit `ID:`/`Name:` (Go field names).
type skillLike struct {
	ID     int      `json:"id"`
	Name   string   `json:"name"`
	Tags   []string `json:"tags"`
	Active bool     `json:"active"`
}

func TestMarshal_UsesJSONFieldNames(t *testing.T) {
	got, err := MarshalString(skillLike{ID: 7, Name: "Go", Tags: []string{"x", "y"}, Active: true})
	if err != nil {
		t.Fatalf("MarshalString: %v", err)
	}
	// Keys must be the json-tag names (lower-case), never the Go field names.
	for _, wantKey := range []string{"id: 7", "name: Go", "tags[2]: x,y", "active: true"} {
		if !strings.Contains(got, wantKey) {
			t.Errorf("output missing %q\n---\n%s", wantKey, got)
		}
	}
	for _, badKey := range []string{"ID:", "Name:", "Tags", "Active:"} {
		if strings.Contains(got, badKey) {
			t.Errorf("output leaked Go field name %q (json normalization broken)\n---\n%s", badKey, got)
		}
	}
}

// TestUnmarshal_IntoStruct proves TOON decodes into a json-tagged struct
// (round-trip struct -> TOON -> struct), honoring the json tags symmetrically.
func TestUnmarshal_IntoStruct(t *testing.T) {
	in := skillLike{ID: 42, Name: "Rust", Tags: []string{"sys", "safe"}, Active: false}
	b, err := Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out skillLike
	if err := Unmarshal(b, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("struct round-trip mismatch\n in: %+v\nout: %+v", in, out)
	}
}

// TestDecode_RejectsMalformed is the negative contract for the self-describing
// invariants that G08 exists to protect: an array/tabular/list whose declared
// [N] count or field arity does not match its rows, and an unterminated quote,
// MUST return a non-nil error and NEVER a silent partial value — that silent
// fallback is danger-zone #2. Cases below are the ACTUAL rejections the codec
// produces (verified against the library, §11.4.6), covering inline count
// (over AND under), tabular row width, list length, and quote termination.
//
// Honest boundary (§11.4.6): the codec is intentionally lenient on some
// structurally-odd-but-unambiguous inputs (re-indented sibling keys, a bare
// scalar line, duplicate keys) — those are NOT treated as errors and are
// exercised only for no-panic in TestDecode_FuzzDoesNotPanic. The invariant
// G08 depends on — the self-describing [N]/{fields} contract — IS enforced.
func TestDecode_RejectsMalformed(t *testing.T) {
	malformed := []struct {
		name string
		body string
	}{
		{"inline count too high (declares 3, has 2)", "tags[3]: a,b"},
		{"inline count too low (declares 2, has 3)", "a[2]: 1,2,3"},
		{"tabular row width mismatch", "u[1]{a,b}:\n  1"},
		{"tabular row too many fields", "x[2]{a,b}:\n  1,2\n  3"},
		{"list length mismatch", "list[2]:\n  - 1"},
		{"unterminated quoted string", `x: "abc`},
	}
	for _, m := range malformed {
		m := m
		t.Run(m.name, func(t *testing.T) {
			if _, err := Decode([]byte(m.body)); err == nil {
				t.Errorf("Decode(%q) returned nil error — malformed input accepted (silent-fallback bluff)", m.body)
			}
			var into map[string]any
			if err := Unmarshal([]byte(m.body), &into); err == nil {
				t.Errorf("Unmarshal(%q) returned nil error — malformed input silently accepted", m.body)
			}
		})
	}
}

// TestDecode_FuzzDoesNotPanic feeds a spread of hostile/edge inputs and only
// requires that the codec never panics — it must return (value,nil) or
// (nil,err), never crash the process.
func TestDecode_FuzzDoesNotPanic(t *testing.T) {
	inputs := []string{
		"", "   ", "\n\n", "{}", "[]", "[0]:", "a:", "a: ", ":", "[]: ",
		"a[-1]: x", "a[999999999]: x", strings.Repeat("a:\n  ", 200) + "1",
		"\t\t\t", "a: \"\\u\"", "x[2]{a}:\n 1\n 2\n 3",
	}
	for _, in := range inputs {
		in := in
		t.Run(sanitize(in), func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Decode(%q) panicked: %v", in, r)
				}
			}()
			_, _ = Decode([]byte(in))
		})
	}
}

func sanitize(s string) string {
	if len(s) > 20 {
		s = s[:20]
	}
	return strings.Map(func(r rune) rune {
		if r < 32 {
			return '_'
		}
		return r
	}, s)
}

// TestLengthMarkerCountIsAccurate asserts the self-describing [N] header count
// equals the real element count for inline and tabular arrays (the
// self-describing-length invariant from the G08 test-coverage requirement).
func TestLengthMarkerCountIsAccurate(t *testing.T) {
	got, err := MarshalString(map[string]any{
		"tags":  []any{"a", "b", "c", "d"},
		"users": []any{map[string]any{"id": 1, "n": "x"}, map[string]any{"id": 2, "n": "y"}},
	})
	if err != nil {
		t.Fatalf("MarshalString: %v", err)
	}
	if !strings.Contains(got, "tags[4]:") {
		t.Errorf("inline array count wrong, want tags[4]:\n%s", got)
	}
	if !strings.Contains(got, "users[2]{") {
		t.Errorf("tabular header count wrong, want users[2]{...}:\n%s", got)
	}
}

// TestMediaTypeConstant pins the advertised media type (used by the API content
// negotiation). A change here must be a deliberate, reviewed edit.
func TestMediaTypeConstant(t *testing.T) {
	if MediaType != "application/toon" {
		t.Errorf("MediaType = %q, want application/toon", MediaType)
	}
	if AltMediaType != "text/x-toon" {
		t.Errorf("AltMediaType = %q, want text/x-toon", AltMediaType)
	}
}
