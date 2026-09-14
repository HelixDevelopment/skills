package github

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// newTestClient builds a Client pointed at srv with a captured zap logger,
// so tests can assert on both behavior and on what (never) got logged.
func newTestClient(t *testing.T, srv *httptest.Server, token string) (*Client, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)
	c := NewClient(token, logger)
	c.SetBaseURL(srv.URL)
	return c, logs
}

// fieldStringValue renders a captured zap field's value as a string for
// leak-scanning, covering every zapcore field-encoding path zap defines —
// not just the two storage locations (Field.String for zap.String, or a
// string stashed directly in Field.Interface) a narrower helper might
// check.
//
// N2 remediation: a prior version of this helper only checked those two
// locations. It FAILED OPEN — silently treated as "no string value here"
// — for every other field-encoding path zap ships: zap.ByteString stores
// a []byte in Field.Interface (Type == ByteStringType), never a string;
// zap.Stringer stores an fmt.Stringer in Field.Interface (Type ==
// StringerType) — the string only exists after calling .String();
// zap.Error stores an error in Field.Interface (Type == ErrorType) — the
// string only exists after calling .Error(); zap.Reflect stores an
// arbitrary value (Type == ReflectType). A token logged via any of those
// four constructors would never be scanned at all by either leak-scan
// loop in this file, independent of whether the token itself was
// present.
//
// The fix: render the field through the SAME code path zap itself uses
// to serialize a field into any real encoder — zapcore.Field.AddTo —
// against a zapcore.NewMapObjectEncoder(). AddTo already knows how to
// turn EVERY zapcore field type into that encoder's representation
// (ByteStringType -> string(bytes), StringerType -> the Stringer's
// .String() output, ErrorType -> the error's .Error() string, ReflectType
// -> the raw reflected value, etc. — see go.uber.org/zap/zapcore/field.go
// AddTo + go.uber.org/zap/zapcore/error.go encodeError, verified against
// the pinned go.uber.org/zap v1.27.0 module source 2026-07-16). This
// helper therefore never needs its own type switch mirroring zap's
// internals, and so it cannot silently omit a field-encoding path zap
// adds in the future the way the two-location check above did for four
// of them today.
//
// F1 remediation (round 4): the N2 fix above still had a fail-open gap
// of its own — it looked up ONLY enc.Fields[f.Key] after calling AddTo.
// That is correct for every field type EXCEPT zap.Inline: per
// go.uber.org/zap@v1.27.0 field.go's Inline() constructor, an Inline
// field's Key is the EMPTY STRING, and its AddTo branch
// (InlineMarshalerType) calls `f.Interface.(ObjectMarshaler).
// MarshalLogObject(enc)` DIRECTLY against the shared top-level encoder —
// it never wraps the call in a sub-map keyed by f.Key the way AddObject
// does (verified against zapcore/field.go's AddTo switch and
// zapcore/memory_encoder.go's AddObject vs the InlineMarshalerType
// branch in the same switch, go.uber.org/zap v1.27.0, 2026-07-16).
// Whatever keys the inlined marshaler writes (e.g.
// enc.AddString("innerKey", secretToken)) therefore land in enc.Fields
// UNDER THE MARSHALER'S OWN KEYS, never under f.Key (which is "" and is
// never populated). A single-key lookup at enc.Fields[f.Key] (==
// enc.Fields[""]) is never set by that branch, so ok came back false and
// a token embedded anywhere inside a zap.Inline(...) value was invisible
// to this scanner regardless of whether the token was present — a
// second fail-open path, of equal severity to the four N2 closed, this
// one specific to Inline (proven RED/GREEN by
// TestFieldStringValue_DetectsInlineLeak below).
//
// A second, independent fail-open gap the N2 fix never closed: even for
// field types that DO land their value under f.Key (AddObject's nested
// sub-map, AddReflected's raw value), a []byte value NESTED one or more
// levels deep inside that value (e.g. a zap.Object-wrapped marshaler
// that itself calls enc.AddBinary("inner", tokenBytes), or
// zap.Reflect(map[string]interface{}{"inner": tokenBytes})) was rendered
// via the top-level fmt.Sprintf("%v", ...) fallback, which prints a
// []byte as a bracketed list of DECIMAL BYTE VALUES (e.g.
// "[102 49 ...]"), never as the literal token text — so a substring
// check against the rendered string never matches, even though the
// token bytes are genuinely present in the encoded field tree (proven by
// TestFieldStringValue_DetectsNestedBinaryLeak and
// TestFieldStringValue_DetectsReflectLeak below).
//
// The fix: walk the ENTIRE enc.Fields map — every key AddTo populated,
// not only f.Key — and recurse into every nested map[string]interface{}
// (from AddObject/OpenNamespace) and []interface{} (from AddArray) at
// ANY depth, converting a []byte value to a string at EVERY depth
// (mirroring what AddByteString already does automatically for a
// TOP-level ByteStringType field). This closes both gaps at once: an
// Inline field's marshaler-chosen keys are now included because the scan
// no longer looks up f.Key specifically, and a nested []byte is now
// substring-scannable at any depth because the recursion converts it
// explicitly instead of falling through to fmt's numeric-slice
// formatting.
//
// Correcting a prior claim in this comment: NamespaceType does NOT
// "carry no scannable value" the way SkipType does. zapcore's
// OpenNamespace (the NamespaceType AddTo branch) executes
// `m.cur[k] = ns; m.cur = ns` — i.e. it DOES store a (possibly still
// empty) map under f.Key before redirecting subsequent writes into it
// (verified against zapcore/memory_encoder.go's OpenNamespace,
// go.uber.org/zap v1.27.0). A bare zap.Namespace(key) field scanned in
// isolation (this helper's own per-field, fresh-encoder calling
// convention — every caller in this file invokes fieldStringValue once
// per zapcore.Field, never replaying a whole field slice through one
// shared encoder) therefore yields ok=true with an empty-content
// rendering (the old code's %v would have rendered this as the literal
// text "map[]"), NEVER ok=false. Only SkipType (whose AddTo branch is a
// bare `break`, touching the encoder not at all) genuinely adds nothing
// and yields ok=false. This function now returns ok=false ONLY when
// AddTo recorded literally zero keys anywhere in enc.Fields (SkipType, or
// a future zap field type that behaves the same way) — never because a
// value existed somewhere in the encoded tree but this helper declined
// to walk down to it, closing the "OR fail the test outright on any
// field whose value cannot be introspected" alternative the round-3
// remediation brief allows by always succeeding at rendering instead.
func fieldStringValue(f zapcore.Field) (string, bool) {
	enc := zapcore.NewMapObjectEncoder()
	f.AddTo(enc)
	if len(enc.Fields) == 0 {
		return "", false
	}
	var b strings.Builder
	for k, v := range enc.Fields {
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(stringifyEncoded(v))
		b.WriteString(" ")
	}
	return b.String(), true
}

// stringifyEncoded recursively renders a value as stored by
// zapcore.MapObjectEncoder (see go.uber.org/zap/zapcore/memory_encoder.go)
// into a single substring-scannable string. It descends into every
// nested container at ANY depth and converts a []byte value to a string
// at EVERY depth — the F1 remediation (round 4): a []byte value buried
// inside a nested structure must never be rendered only through Go's
// default %v formatting, which prints a byte slice as space-separated
// decimal values rather than as text (see fieldStringValue's doc comment
// for the full forensic detail on why this matters for
// zap.Inline/zap.Object/zap.Reflect).
//
// F2 remediation (round 5): the F1 fix above still failed open for a
// []byte value carried inside a TYPED container — a value whose dynamic
// Go type is something OTHER than the two dynamically-typed containers
// (map[string]interface{}, []interface{}) the pre-fix type switch
// matched explicitly. zapcore.MapObjectEncoder.AddReflected (the
// ReflectType field-encoding path — see AddReflected in
// go.uber.org/zap@v1.27.0/zapcore/memory_encoder.go) stores its argument
// COMPLETELY UNCONVERTED: `m.cur[k] = v`. So zap.Reflect("p",
// map[string][]byte{"tok": tokenBytes}) lands in enc.Fields under its
// OWN concrete type map[string][]byte — NOT map[string]interface{} —
// and a Go type switch's `case map[string]interface{}:` arm does NOT
// match a differently-typed map, regardless of whether the two types
// happen to be structurally similar. The same gap applied to
// [][]byte (vs. []interface{}), and to any struct (e.g.
// struct{Token []byte}) or pointer-to-struct carrying a []byte field —
// none of those are one of the two dynamically-typed containers the
// pre-fix switch matched, so every one of them fell through to the
// `default: fmt.Sprintf("%v", val)` branch, which renders a nested
// []byte as a bracketed list of decimal byte values (e.g.
// "[76 69 65 75 ...]"), never as the literal token text — hiding the
// token from every substring check in this file exactly as thoroughly
// as the gaps N2 and F1 already closed for OTHER field-encoding paths.
// This was proven empirically during this remediation (never assumed,
// per §11.4.6): a throwaway harness against the pinned
// go.uber.org/zap v1.27.0 confirmed all four of
// map[string][]byte / [][]byte / struct{Token []byte} /
// *struct{Token []byte} rendered as digit lists under the pre-fix
// type-switch-only walk, and as the literal token text once rewritten
// to walk via reflection (see below).
//
// The fix: stop pattern-matching on specific CONCRETE Go types
// (map[string]interface{}, []interface{}) and instead walk the value's
// reflect.Kind — Map, Slice/Array, Struct, Ptr/Interface — which covers
// every container shape Go's type system can express, independent of
// its element/field/key types, closing the typed-container gap once and
// for all rather than adding one more type-switch case per container
// type callers might use. A []byte (or [N]byte) is converted to a
// string at every depth by checking the element Kind
// (reflect.Uint8) rather than the exact type, so a NAMED byte-slice type
// is covered too. This SUBSUMES the pre-fix map[string]interface{} and
// []interface{} fast paths (a map[string]interface{} IS reflect.Kind
// Map; a []interface{} IS reflect.Kind Slice) — there is no longer a
// separate type-switch arm for them, only the generic Kind-based walk,
// so there is a single code path to keep correct rather than two that
// could silently diverge.
func stringifyEncoded(v interface{}) string {
	return stringifyReflectValue(reflect.ValueOf(v))
}

// stringifyReflectValue is stringifyEncoded's recursive worker. rv may be
// the zero Value (an untyped nil interface{} — e.g. a map value that was
// itself nil, or a nil pointer's Elem()), which IsValid() reports false
// for; every other Kind this function does not explicitly recurse into
// (Bool/Int*/Uint* other than a byte-slice element/Float*/Complex*/
// Chan/Func/UnsafePointer/time.Time-as-a-Struct-is-handled-by-the-Struct-
// case-above) is a terminal leaf with no further children to walk, so
// falling through to fmt is safe: it can never hide a nested []byte,
// because every Kind capable of CONTAINING a nested []byte (Map, Slice,
// Array, Struct, Ptr, Interface) is handled explicitly above the
// default branch. fmt.Sprintf("%v", rv) — passing the reflect.Value
// itself rather than rv.Interface() — is deliberate: it renders
// correctly even for a value reached through an UNEXPORTED struct field
// (rv.CanInterface() == false in that case; calling .Interface() would
// panic), because fmt specially recognizes a reflect.Value argument and
// formats its underlying value directly without requiring
// CanInterface() (verified empirically against this pinned Go
// toolchain during this remediation, per §11.4.6 — never assumed).
func stringifyReflectValue(rv reflect.Value) string {
	if !rv.IsValid() {
		return "<nil>"
	}
	switch rv.Kind() {
	case reflect.String:
		return rv.String()
	case reflect.Slice, reflect.Array:
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			// A []byte (or [N]byte, or any named type sharing that
			// underlying shape) at ANY depth — read byte-by-byte via the
			// Uint() accessor rather than calling Bytes() (which panics
			// for a non-addressable Array) or Interface() (which panics
			// for an unexported-field-derived Value); Index(i).Uint() is
			// safe in both of those cases.
			n := rv.Len()
			bs := make([]byte, n)
			for i := 0; i < n; i++ {
				bs[i] = byte(rv.Index(i).Uint())
			}
			return string(bs)
		}
		var b strings.Builder
		for i := 0; i < rv.Len(); i++ {
			b.WriteString(stringifyReflectValue(rv.Index(i)))
			b.WriteString(" ")
		}
		return b.String()
	case reflect.Map:
		var b strings.Builder
		iter := rv.MapRange()
		for iter.Next() {
			b.WriteString(stringifyReflectValue(iter.Key()))
			b.WriteString("=")
			b.WriteString(stringifyReflectValue(iter.Value()))
			b.WriteString(" ")
		}
		return b.String()
	case reflect.Struct:
		var b strings.Builder
		t := rv.Type()
		for i := 0; i < rv.NumField(); i++ {
			b.WriteString(t.Field(i).Name)
			b.WriteString("=")
			b.WriteString(stringifyReflectValue(rv.Field(i)))
			b.WriteString(" ")
		}
		return b.String()
	case reflect.Ptr, reflect.Interface:
		if rv.IsNil() {
			return "<nil>"
		}
		return stringifyReflectValue(rv.Elem())
	default:
		return fmt.Sprintf("%v", rv)
	}
}

// ---------------------------------------------------------------------------
// fieldStringValue — direct N2 remediation proof that the leak-scanning
// helper no longer fails open for the field-encoding paths a bare
// f.String / f.Interface.(string) check misses.
// ---------------------------------------------------------------------------

// TestFieldStringValue_DetectsByteStringLeak proves the N2 remediation:
// a token logged via zap.ByteString (Field.Interface holds a []byte,
// never a string, under the pre-fix helper) must now be surfaced.
//
// Mutation-proof: reverting fieldStringValue to the pre-fix version
// (checking only f.Type == zapcore.StringType and
// f.Interface.(string)) makes this test fail (RED) — a ByteStringType
// field matches neither branch, so ok comes back false and the fake
// token is never seen; restoring the AddTo-based fix makes it pass
// (GREEN). This was verified by hand during remediation (see the
// implementer's report for the captured go test output of both runs).
func TestFieldStringValue_DetectsByteStringLeak(t *testing.T) {
	const fakeToken = "n2-bytestring-leak-token-abc123"
	f := zap.ByteString("authz", []byte(fakeToken))
	got, ok := fieldStringValue(f)
	if !ok {
		t.Fatalf("fieldStringValue returned ok=false for a ByteStringType field; " +
			"a fail-open scanner would silently miss any token logged via zap.ByteString")
	}
	if !strings.Contains(got, fakeToken) {
		t.Fatalf("fieldStringValue(%+v) = %q, want it to contain the fake token %q", f, got, fakeToken)
	}
}

// TestFieldStringValue_DetectsErrorLeak is the zap.Error() analogue: a
// token embedded in a wrapped error's message must also be surfaced —
// zap.Error stores an `error` value in Field.Interface (Type ==
// ErrorType), never a bare string in either of the two locations the
// pre-fix helper checked.
func TestFieldStringValue_DetectsErrorLeak(t *testing.T) {
	const fakeToken = "n2-error-leak-token-def456"
	f := zap.Error(fmt.Errorf("auth failed for token %s", fakeToken))
	got, ok := fieldStringValue(f)
	if !ok {
		t.Fatalf("fieldStringValue returned ok=false for an ErrorType field; " +
			"a fail-open scanner would silently miss any token logged via zap.Error")
	}
	if !strings.Contains(got, fakeToken) {
		t.Fatalf("fieldStringValue(%+v) = %q, want it to contain the fake token %q", f, got, fakeToken)
	}
}

// fakeStringer is a minimal fmt.Stringer used only to exercise
// zap.Stringer in TestFieldStringValue_DetectsStringerLeak.
type fakeStringer string

func (s fakeStringer) String() string { return string(s) }

// TestFieldStringValue_DetectsStringerLeak covers zap.Stringer, the
// third fail-open path the pre-fix helper missed (Field.Interface holds
// an fmt.Stringer, Type == StringerType — the string only exists after
// calling .String(), which neither pre-fix branch ever did).
func TestFieldStringValue_DetectsStringerLeak(t *testing.T) {
	const fakeToken = "n2-stringer-leak-token-ghi789"
	f := zap.Stringer("authz", fakeStringer(fakeToken))
	got, ok := fieldStringValue(f)
	if !ok {
		t.Fatalf("fieldStringValue returned ok=false for a StringerType field; " +
			"a fail-open scanner would silently miss any token logged via zap.Stringer")
	}
	if !strings.Contains(got, fakeToken) {
		t.Fatalf("fieldStringValue(%+v) = %q, want it to contain the fake token %q", f, got, fakeToken)
	}
}

// fakeInlineMarshaler is a minimal zapcore.ObjectMarshaler used only to
// exercise zap.Inline in TestFieldStringValue_DetectsInlineLeak. It
// writes its payload directly onto whatever encoder it is handed, under
// a key of its OWN choosing — exactly mirroring how a real caller's
// zap.Inline(someStruct) leaks a value nested inside that struct's own
// MarshalLogObject implementation.
type fakeInlineMarshaler struct {
	token string
}

func (m fakeInlineMarshaler) MarshalLogObject(enc zapcore.ObjectEncoder) error {
	enc.AddString("inner_field_not_the_fields_own_key", m.token)
	return nil
}

// TestFieldStringValue_DetectsInlineLeak is the F1 remediation proof:
// zap.Inline(marshaler) constructs a Field whose Key is the EMPTY STRING
// (see go.uber.org/zap@v1.27.0 field.go's Inline()) and whose AddTo
// branch calls the marshaler directly on the shared encoder — so the
// marshaler's fields land under ITS OWN keys, never under f.Key (there
// is no f.Key to land under). The pre-F1 helper's single-key lookup
// `enc.Fields[f.Key]` (== enc.Fields[""]) was therefore never populated
// by an Inline field, regardless of whether the token was present:
// fail-open.
//
// Mutation-proof: reverting fieldStringValue to the pre-F1 single-key
// lookup (`v, ok := enc.Fields[f.Key]` with the string/[]byte/%v type
// switch) makes this test fail (RED) — enc.Fields[""] is never set by
// the InlineMarshalerType branch, so ok comes back false and the fake
// token is never seen; restoring the F1 fix (walking every key in
// enc.Fields) makes it pass (GREEN). This was verified by hand during
// remediation (mutate → RED captured → revert → GREEN captured; see the
// implementer's report for the exact go test output of both runs).
func TestFieldStringValue_DetectsInlineLeak(t *testing.T) {
	const fakeToken = "f1-inline-leak-token-jkl012"
	f := zap.Inline(fakeInlineMarshaler{token: fakeToken})
	got, ok := fieldStringValue(f)
	if !ok {
		t.Fatalf("fieldStringValue returned ok=false for an InlineMarshalerType field; " +
			"a fail-open scanner would silently miss any token an Inline-marshaled value emits under its own inner key")
	}
	if !strings.Contains(got, fakeToken) {
		t.Fatalf("fieldStringValue(%+v) = %q, want it to contain the fake token %q "+
			"(emitted by the marshaler under its own inner key, never under f.Key)", f, got, fakeToken)
	}
}

// fakeNestedBinaryMarshaler is used only by
// TestFieldStringValue_DetectsNestedBinaryLeak (F4) — it embeds its
// payload as a []byte via AddBinary, one level deep inside a
// zap.Object-wrapped marshaler, proving the F1 recursive walk closes the
// nested-map + nested-[]byte gap that
// TestFieldStringValue_DetectsByteStringLeak (a TOP-level ByteStringType
// field) does not exercise.
type fakeNestedBinaryMarshaler struct {
	token []byte
}

func (m fakeNestedBinaryMarshaler) MarshalLogObject(enc zapcore.ObjectEncoder) error {
	enc.AddBinary("nested_binary", m.token)
	return nil
}

// TestFieldStringValue_DetectsNestedBinaryLeak is the F4 remediation
// test: a token written via AddBinary ONE LEVEL DEEP inside a
// zap.Object-wrapped marshaler (Type == ObjectMarshalerType, whose AddTo
// branch creates a NESTED map[string]interface{} under f.Key — see
// zapcore/memory_encoder.go's AddObject) must still be found.
//
// Mutation-proof: reverting stringifyEncoded's nested-[]byte handling to
// fall through to the generic fmt.Sprintf("%v", ...) branch at any depth
// below the top level makes this test fail (RED) — Go's default %v
// formatting of a []byte prints space-separated decimal values, never
// the literal token text, so the substring check never matches;
// restoring the explicit []byte-at-every-depth conversion makes it pass
// (GREEN).
func TestFieldStringValue_DetectsNestedBinaryLeak(t *testing.T) {
	const fakeToken = "f4-nested-binary-leak-token-mno345"
	f := zap.Object("outer", fakeNestedBinaryMarshaler{token: []byte(fakeToken)})
	got, ok := fieldStringValue(f)
	if !ok {
		t.Fatalf("fieldStringValue returned ok=false for an ObjectMarshalerType field with a nested AddBinary payload")
	}
	if !strings.Contains(got, fakeToken) {
		t.Fatalf("fieldStringValue(%+v) = %q, want it to contain the fake token %q "+
			"nested one level deep via AddBinary inside a zap.Object", f, got, fakeToken)
	}
}

// TestFieldStringValue_DetectsReflectLeak is the F4-required zap.Reflect
// coverage: a token nested inside a reflected map value (Type ==
// ReflectType, whose AddTo branch stores the raw value UNCONVERTED via
// AddReflected — see zapcore/memory_encoder.go) must still be found even
// when the token itself is a []byte one level deep inside that reflected
// value, not merely a top-level string.
//
// Mutation-proof: same underlying invariant as
// TestFieldStringValue_DetectsNestedBinaryLeak — reverting the nested
// []byte conversion to the generic %v fallback makes this test fail
// (RED); restoring it makes it pass (GREEN).
func TestFieldStringValue_DetectsReflectLeak(t *testing.T) {
	const fakeToken = "f4-reflect-leak-token-pqr678"
	f := zap.Reflect("payload", map[string]interface{}{
		"token_bytes": []byte(fakeToken),
	})
	got, ok := fieldStringValue(f)
	if !ok {
		t.Fatalf("fieldStringValue returned ok=false for a ReflectType field")
	}
	if !strings.Contains(got, fakeToken) {
		t.Fatalf("fieldStringValue(%+v) = %q, want it to contain the fake token %q "+
			"nested one level deep inside the reflected map value", f, got, fakeToken)
	}
}

// tokenStruct and tokenStructPtr are minimal fixtures used only by the F2
// (round-5) typed-container tests below — they carry a []byte field
// exactly the way a real caller's own domain type might, with no
// zap-specific machinery of their own (unlike fakeNestedBinaryMarshaler
// above, which explicitly calls AddBinary; these instead go through
// zap.Reflect/zap.Any's plain AddReflected path — see
// stringifyReflectValue's doc comment for why that path stores its
// argument's CONCRETE type completely unconverted).
type tokenStruct struct {
	Token []byte
}

// TestFieldStringValue_DetectsTypedByteMapLeak is the F2 (round 5)
// remediation test: a []byte value carried inside a TYPED map
// (map[string][]byte, as opposed to the map[string]interface{} the
// pre-fix type-switch-only walk matched explicitly) must still be found.
// zap.Reflect stores its argument's concrete type completely unconverted
// (AddReflected — see stringifyReflectValue's doc comment), so
// enc.Fields holds a genuine map[string][]byte value, not a
// map[string]interface{} one.
//
// Mutation-proof (verified by hand during remediation, per this file's
// established convention): reverting stringifyEncoded to the pre-F2
// type-switch-only version (matching only map[string]interface{} and
// []interface{}, falling through to fmt.Sprintf("%v", val) for anything
// else) makes this test fail (RED) — a map[string][]byte value matches
// neither switch arm, so it renders as
// "map[k:[<decimal bytes>]]", never the literal token text; restoring
// the reflection-based walk makes it pass (GREEN).
func TestFieldStringValue_DetectsTypedByteMapLeak(t *testing.T) {
	const fakeToken = "f2-typed-byte-map-leak-token-stu901"
	f := zap.Reflect("m", map[string][]byte{"k": []byte(fakeToken)})
	got, ok := fieldStringValue(f)
	if !ok {
		t.Fatalf("fieldStringValue returned ok=false for a map[string][]byte ReflectType field")
	}
	if !strings.Contains(got, fakeToken) {
		t.Fatalf("fieldStringValue(%+v) = %q, want it to contain the fake token %q "+
			"carried inside a map[string][]byte (a TYPED container, not map[string]interface{})", f, got, fakeToken)
	}
}

// TestFieldStringValue_DetectsAnyTypedByteMapLeak is
// TestFieldStringValue_DetectsTypedByteMapLeak's zap.Any() analogue —
// zap.Any dispatches a map[string][]byte argument to zap.Reflect
// internally, so this proves the fix holds for the constructor callers
// are most likely to reach for directly, not only the explicit
// zap.Reflect spelling.
func TestFieldStringValue_DetectsAnyTypedByteMapLeak(t *testing.T) {
	const fakeToken = "f2-any-typed-byte-map-leak-token-vwx234"
	f := zap.Any("m", map[string][]byte{"k": []byte(fakeToken)})
	got, ok := fieldStringValue(f)
	if !ok {
		t.Fatalf("fieldStringValue returned ok=false for a zap.Any(map[string][]byte) field")
	}
	if !strings.Contains(got, fakeToken) {
		t.Fatalf("fieldStringValue(%+v) = %q, want it to contain the fake token %q", f, got, fakeToken)
	}
}

// TestFieldStringValue_DetectsTypedByteSliceOfSliceLeak is the F2
// (round 5) [][]byte coverage — the slice analogue of the typed-map
// case above: [][]byte is a distinct concrete type from []interface{},
// so the pre-fix type switch's `case []interface{}:` arm never matched
// it either.
func TestFieldStringValue_DetectsTypedByteSliceOfSliceLeak(t *testing.T) {
	const fakeToken = "f2-typed-byte-slice-leak-token-yz567"
	f := zap.Reflect("s", [][]byte{[]byte(fakeToken)})
	got, ok := fieldStringValue(f)
	if !ok {
		t.Fatalf("fieldStringValue returned ok=false for a [][]byte ReflectType field")
	}
	if !strings.Contains(got, fakeToken) {
		t.Fatalf("fieldStringValue(%+v) = %q, want it to contain the fake token %q "+
			"carried inside a [][]byte (a TYPED container, not []interface{})", f, got, fakeToken)
	}
}

// TestFieldStringValue_DetectsStructFieldByteLeak is the F2 (round 5)
// struct coverage: a []byte carried in a named EXPORTED struct field
// (struct{ Token []byte }) — neither a map nor a slice at the top level,
// so it matched NEITHER of the pre-fix type switch's two container arms
// and fell straight to the %v fallback.
func TestFieldStringValue_DetectsStructFieldByteLeak(t *testing.T) {
	const fakeToken = "f2-struct-field-byte-leak-token-abc890"
	f := zap.Reflect("st", tokenStruct{Token: []byte(fakeToken)})
	got, ok := fieldStringValue(f)
	if !ok {
		t.Fatalf("fieldStringValue returned ok=false for a struct{Token []byte} ReflectType field")
	}
	if !strings.Contains(got, fakeToken) {
		t.Fatalf("fieldStringValue(%+v) = %q, want it to contain the fake token %q "+
			"carried inside struct{Token []byte}", f, got, fakeToken)
	}
}

// TestFieldStringValue_DetectsPointerToStructFieldByteLeak is
// TestFieldStringValue_DetectsStructFieldByteLeak's pointer-to-struct
// variant — proving the fix follows a *T the same way it walks a bare
// T, since a caller is at least as likely to log a pointer to their
// domain type as the value itself.
func TestFieldStringValue_DetectsPointerToStructFieldByteLeak(t *testing.T) {
	const fakeToken = "f2-ptr-struct-field-byte-leak-token-def123"
	f := zap.Reflect("pst", &tokenStruct{Token: []byte(fakeToken)})
	got, ok := fieldStringValue(f)
	if !ok {
		t.Fatalf("fieldStringValue returned ok=false for a *struct{Token []byte} ReflectType field")
	}
	if !strings.Contains(got, fakeToken) {
		t.Fatalf("fieldStringValue(%+v) = %q, want it to contain the fake token %q "+
			"carried inside a *struct{Token []byte} pointer", f, got, fakeToken)
	}
}

// TestFieldStringValue_TypedContainer_NoFalsePositive is the negative
// control for the F2 (round 5) fix: a typed container that genuinely
// carries NO token must still report found=false — proving the
// reflection-based walk does not turn into an over-eager match-anything
// scanner in the process of closing the fail-open gap above.
func TestFieldStringValue_TypedContainer_NoFalsePositive(t *testing.T) {
	const fakeToken = "f2-negative-control-token-should-not-appear"
	f := zap.Reflect("clean", map[string][]byte{"k": []byte("harmless-value")})
	got, ok := fieldStringValue(f)
	if !ok {
		t.Fatalf("fieldStringValue returned ok=false for a token-free map[string][]byte field")
	}
	if strings.Contains(got, fakeToken) {
		t.Fatalf("fieldStringValue(%+v) = %q, unexpectedly contains a token that was never present", f, got)
	}
}

// ---------------------------------------------------------------------------
// ListTreeRecursive
// ---------------------------------------------------------------------------

func TestListTreeRecursive_Success(t *testing.T) {
	// Fixture JSON shape matches GitHub's documented "get a tree" response
	// (GET /repos/{owner}/{repo}/git/trees/{ref}?recursive=1):
	// https://docs.github.com/en/rest/git/trees#get-a-tree
	const fixture = `{
		"sha": "abc123",
		"tree": [
			{"path": "systematic-debugging/SKILL.md", "mode": "100644", "type": "blob", "sha": "s1", "size": 512},
			{"path": "systematic-debugging/scripts/run.sh", "mode": "100755", "type": "blob", "sha": "s2", "size": 128},
			{"path": "systematic-debugging", "mode": "040000", "type": "tree", "sha": "s3", "size": 0}
		],
		"truncated": false
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// This handler runs on the httptest server's own goroutine, never
		// the test's goroutine — t.Fatalf there would call
		// runtime.Goexit() on the WRONG goroutine (it only terminates the
		// handler's goroutine, not the test) and is unsafe/misleading.
		// t.Errorf + return is the correct pattern here.
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s", r.Method)
			return
		}
		if r.URL.Path != "/repos/anthropics/skills/git/trees/main" {
			t.Errorf("unexpected path %s", r.URL.Path)
			return
		}
		if r.URL.Query().Get("recursive") != "1" {
			t.Errorf("expected recursive=1, got %s", r.URL.RawQuery)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token-123" {
			t.Errorf("expected Authorization header, got %q", r.Header.Get("Authorization"))
			return
		}
		if r.Header.Get("User-Agent") == "" {
			t.Errorf("expected a User-Agent header (GitHub requires one)")
			return
		}
		if r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
			t.Errorf("expected X-GitHub-Api-Version: 2022-11-28, got %q", r.Header.Get("X-GitHub-Api-Version"))
			return
		}
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, fixture)
	}))
	defer srv.Close()

	c, logs := newTestClient(t, srv, "test-token-123")
	result, err := c.ListTreeRecursive(t.Context(), "anthropics", "skills", "main")
	if err != nil {
		t.Fatalf("ListTreeRecursive: %v", err)
	}
	if len(result.Entries) != 3 {
		t.Fatalf("expected 3 tree entries, got %d", len(result.Entries))
	}
	if result.Entries[0].Path != "systematic-debugging/SKILL.md" || result.Entries[0].Type != "blob" {
		t.Errorf("unexpected first entry: %+v", result.Entries[0])
	}
	if result.Truncated {
		t.Errorf("expected Truncated=false for a non-truncated fixture")
	}

	// The configured token must never appear in any captured log line.
	for _, e := range logs.All() {
		if strings.Contains(e.Message, "test-token-123") {
			t.Fatalf("token leaked into log message: %q", e.Message)
		}
		for _, f := range e.Context {
			if s, ok := fieldStringValue(f); ok && strings.Contains(s, "test-token-123") {
				t.Fatalf("token leaked into log field %s: %q", f.Key, s)
			}
		}
	}
}

// TestListTreeRecursive_Truncated_LogsWarning is the W-b remediation
// test: GitHub's "truncated" bit must be surfaced on the RETURNED
// result, not only logged — a caller (e.g. a future scan orchestrator)
// cannot branch its behavior on a log line, only on a value it received.
//
// Mutation-proof: reverting ListTreeRecursive to return a bare
// []TreeEntry (dropping the ListTreeResult.Truncated field entirely)
// makes this test fail to even compile/assert the field, i.e. RED;
// restoring the fix makes result.Truncated observably true, GREEN. The
// warning-log assertion is retained unchanged — the fix ADDS the
// programmatic signal, it does not remove the existing log-based one.
func TestListTreeRecursive_Truncated_LogsWarning(t *testing.T) {
	const fixture = `{"sha":"abc","tree":[],"truncated":true}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, fixture)
	}))
	defer srv.Close()

	c, logs := newTestClient(t, srv, "")
	result, err := c.ListTreeRecursive(t.Context(), "o", "r", "main")
	if err != nil {
		t.Fatalf("ListTreeRecursive: %v", err)
	}
	if !result.Truncated {
		t.Fatalf("expected result.Truncated=true when the API response sets \"truncated\":true, got false " +
			"(a caller has no way to detect a partial listing without this field)")
	}
	found := false
	for _, e := range logs.All() {
		if e.Level == zapcore.WarnLevel && strings.Contains(e.Message, "truncated") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a warning log about a truncated tree listing, got: %+v", logs.All())
	}
}

func TestListTreeRecursive_RejectsEmptyArgs(t *testing.T) {
	c := NewClient("", nil)
	if _, err := c.ListTreeRecursive(t.Context(), "", "repo", "main"); err == nil {
		t.Fatal("expected error for empty owner")
	}
}

func TestListTreeRecursive_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"message":"boom"}`)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, "")
	if _, err := c.ListTreeRecursive(t.Context(), "o", "r", "main"); err == nil {
		t.Fatal("expected error on 500 response")
	} else if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected error to mention status 500, got: %v", err)
	}
}

// TestListTreeRecursive_MalformedJSON_Errors is a W4 missing-coverage
// addition: a 200 response whose body is not valid JSON must error
// cleanly rather than panic or silently return a zero-value tree.
func TestListTreeRecursive_MalformedJSON_Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"sha": "abc", "tree": [not valid json`)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, "")
	if _, err := c.ListTreeRecursive(t.Context(), "o", "r", "main"); err == nil {
		t.Fatal("expected error for malformed JSON tree response")
	} else if !strings.Contains(err.Error(), "unmarshal response") {
		t.Errorf("expected error to mention unmarshaling, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// GetHeadSHA
// ---------------------------------------------------------------------------

func TestGetHeadSHA_Success(t *testing.T) {
	// Fixture matches GitHub's "get a commit" response shape (only the
	// top-level "sha" field is consumed):
	// https://docs.github.com/en/rest/commits/commits#get-a-commit
	const fixture = `{"sha":"deadbeef0123456789","commit":{"message":"fix: something"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/anthropics/skills/commits/main" {
			t.Errorf("unexpected path %s", r.URL.Path)
			return
		}
		fmt.Fprint(w, fixture)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, "")
	sha, err := c.GetHeadSHA(t.Context(), "anthropics", "skills", "main")
	if err != nil {
		t.Fatalf("GetHeadSHA: %v", err)
	}
	if sha != "deadbeef0123456789" {
		t.Errorf("expected sha deadbeef0123456789, got %q", sha)
	}
}

func TestGetHeadSHA_EmptyShaIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"sha":""}`)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, "")
	if _, err := c.GetHeadSHA(t.Context(), "o", "r", "main"); err == nil {
		t.Fatal("expected error for empty sha in response")
	}
}

// TestGetHeadSHA_MalformedJSON_Errors is a W4 missing-coverage addition: a
// 200 response whose body is not valid JSON must error cleanly.
func TestGetHeadSHA_MalformedJSON_Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"sha": not valid json`)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, "")
	if _, err := c.GetHeadSHA(t.Context(), "o", "r", "main"); err == nil {
		t.Fatal("expected error for malformed JSON commit response")
	} else if !strings.Contains(err.Error(), "unmarshal response") {
		t.Errorf("expected error to mention unmarshaling, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// FetchBlob
// ---------------------------------------------------------------------------

func TestFetchBlob_Success(t *testing.T) {
	content := "---\nname: systematic-debugging\ndescription: Debug things.\n---\nBody text.\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	fixture := fmt.Sprintf(`{"sha":"blobsha1","content":%q,"encoding":"base64"}`, encoded)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/anthropics/skills/contents/systematic-debugging/SKILL.md" {
			t.Errorf("unexpected path %s", r.URL.Path)
			return
		}
		if r.URL.Query().Get("ref") != "main" {
			t.Errorf("expected ref=main, got %s", r.URL.RawQuery)
			return
		}
		w.Header().Set("ETag", `"blob-etag-1"`)
		fmt.Fprint(w, fixture)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, "")
	res, err := c.FetchBlob(t.Context(), "anthropics", "skills", "systematic-debugging/SKILL.md", "main", "")
	if err != nil {
		t.Fatalf("FetchBlob: %v", err)
	}
	if res.NotModified {
		t.Fatalf("expected NotModified=false on a fresh fetch")
	}
	if res.SHA != "blobsha1" {
		t.Errorf("expected sha blobsha1, got %q", res.SHA)
	}
	if string(res.Content) != content {
		t.Errorf("decoded content mismatch:\n got: %q\nwant: %q", string(res.Content), content)
	}
	if res.ETag != `"blob-etag-1"` {
		t.Errorf("expected ETag to be captured from the response, got %q", res.ETag)
	}
}

func TestFetchBlob_NotModified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != `"etag-abc"` {
			t.Errorf("expected If-None-Match header, got %q", r.Header.Get("If-None-Match"))
			return
		}
		w.Header().Set("ETag", `"etag-abc"`)
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, "")
	res, err := c.FetchBlob(t.Context(), "o", "r", "path/SKILL.md", "main", `"etag-abc"`)
	if err != nil {
		t.Fatalf("FetchBlob: %v", err)
	}
	if !res.NotModified {
		t.Fatalf("expected NotModified=true on a 304 response")
	}
	if res.Content != nil || res.SHA != "" {
		t.Errorf("expected no content/sha on a 304 response, got content=%v sha=%q", res.Content, res.SHA)
	}
	if res.ETag != `"etag-abc"` {
		t.Errorf("expected the 304 response's ETag to still be captured, got %q", res.ETag)
	}
}

// TestFetchBlob_CachingFlow_TwoCallsReach304 is the F1 remediation test: it
// proves a caller that PERSISTS the ETag from a first FetchBlob call and
// PASSES IT BACK as the etag argument on a second call can actually reach
// a real 304 — the caching path this client exists to enable. Before this
// fix, FetchBlob accepted an etag parameter for If-None-Match but never
// returned the response's ETag, so no caller could ever obtain the value
// needed to trigger this flow; the caching path was dead-by-construction.
func TestFetchBlob_CachingFlow_TwoCallsReach304(t *testing.T) {
	const etag = `"stable-etag-42"`
	content := "---\nname: caching-flow\ndescription: d\n---\nBody.\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	fixture := fmt.Sprintf(`{"sha":"blobsha-cache","content":%q,"encoding":"base64"}`, encoded)

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		fmt.Fprint(w, fixture)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, "")

	// Call 1: no cached ETag yet -> full 200 fetch, caller persists the
	// returned ETag.
	first, err := c.FetchBlob(t.Context(), "o", "r", "path/SKILL.md", "main", "")
	if err != nil {
		t.Fatalf("first FetchBlob: %v", err)
	}
	if first.NotModified {
		t.Fatalf("expected the first call (no prior ETag) to be a real 200 fetch")
	}
	if first.ETag == "" {
		t.Fatalf("expected the first call to return a non-empty ETag to persist")
	}

	// Call 2: pass the persisted ETag back -> the server (and this client)
	// must reach a genuine 304.
	second, err := c.FetchBlob(t.Context(), "o", "r", "path/SKILL.md", "main", first.ETag)
	if err != nil {
		t.Fatalf("second FetchBlob: %v", err)
	}
	if !second.NotModified {
		t.Fatalf("expected the second call (replaying the persisted ETag) to reach a 304, got a full fetch")
	}
	if calls != 2 {
		t.Fatalf("expected exactly 2 requests, got %d", calls)
	}
}

func TestFetchBlob_UnsupportedEncoding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"sha":"x","content":"none","encoding":"none"}`)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, "")
	if _, err := c.FetchBlob(t.Context(), "o", "r", "p", "main", ""); err == nil {
		t.Fatal("expected error for unsupported content encoding")
	}
}

func TestFetchBlob_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Not Found"}`)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, "")
	if _, err := c.FetchBlob(t.Context(), "o", "r", "missing/SKILL.md", "main", ""); err == nil {
		t.Fatal("expected error on 404 response")
	} else if !strings.Contains(err.Error(), "404") {
		t.Errorf("expected error to mention status 404, got: %v", err)
	}
}

// TestFetchBlob_MalformedBase64_Errors is a W4 missing-coverage addition:
// a "base64"-encoded content field that is not actually valid base64 must
// error cleanly (base64.CorruptInputError wrapped), never panic.
func TestFetchBlob_MalformedBase64_Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"sha":"x","content":"!!!not-valid-base64!!!","encoding":"base64"}`)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, "")
	if _, err := c.FetchBlob(t.Context(), "o", "r", "p", "main", ""); err == nil {
		t.Fatal("expected error for malformed base64 content")
	} else if !strings.Contains(err.Error(), "decode base64") {
		t.Errorf("expected error to mention base64 decoding, got: %v", err)
	}
}

// TestFetchBlob_MalformedJSON_Errors is a W4 missing-coverage addition:
// a 200 response whose body is not valid JSON at all must error cleanly.
func TestFetchBlob_MalformedJSON_Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{not valid json`)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, "")
	if _, err := c.FetchBlob(t.Context(), "o", "r", "p", "main", ""); err == nil {
		t.Fatal("expected error for malformed JSON response body")
	} else if !strings.Contains(err.Error(), "unmarshal response") {
		t.Errorf("expected error to mention unmarshaling, got: %v", err)
	}
}

// TestFetchBlob_ErrorBodyTruncated is the W1 remediation test: a non-2xx
// error response body far larger than the truncation cap must appear in
// the returned error message only up to the bound, never verbatim in
// full.
func TestFetchBlob_ErrorBodyTruncated(t *testing.T) {
	hugeBody := strings.Repeat("x", 5000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, hugeBody)
	}))
	defer srv.Close()

	c, _ := newTestClient(t, srv, "")
	_, err := c.FetchBlob(t.Context(), "o", "r", "p", "main", "")
	if err == nil {
		t.Fatal("expected error on 500 response")
	}
	if strings.Contains(err.Error(), hugeBody) {
		t.Fatalf("expected the huge response body to be truncated in the error message, but it appeared in full")
	}
	if len(err.Error()) > 5000 {
		t.Fatalf("expected the error message to be bounded well under the huge body's length, got %d bytes: %.100s...", len(err.Error()), err.Error())
	}
}

// ---------------------------------------------------------------------------
// RateLimitStatus
// ---------------------------------------------------------------------------

func TestRateLimitStatus_Healthy(t *testing.T) {
	c := NewClient("", nil)
	resp := &http.Response{Header: http.Header{
		"X-Ratelimit-Limit":     {"5000"},
		"X-Ratelimit-Remaining": {"4321"},
		"X-Ratelimit-Reset":     {"1750000000"},
	}}
	rl := c.RateLimitStatus(resp)
	if rl.Limit != 5000 || rl.Remaining != 4321 {
		t.Fatalf("unexpected rate limit: %+v", rl)
	}
	if rl.Reset.Unix() != 1750000000 {
		t.Fatalf("unexpected reset time: %v", rl.Reset)
	}
}

func TestRateLimitStatus_Exhausted(t *testing.T) {
	c := NewClient("", nil)
	resp := &http.Response{Header: http.Header{
		"X-Ratelimit-Limit":     {"60"},
		"X-Ratelimit-Remaining": {"0"},
		"X-Ratelimit-Reset":     {"1750003600"},
	}}
	rl := c.RateLimitStatus(resp)
	if rl.Remaining != 0 {
		t.Fatalf("expected Remaining=0 for an exhausted rate limit, got %d", rl.Remaining)
	}
	// This is the exact condition doJSON/FetchBlob key their "rate limit
	// exhausted" warning log on (rl.Limit > 0 && rl.Remaining == 0). A
	// mutation that flips RateLimitStatus to always report a healthy
	// Remaining value (e.g. hardcoding Remaining = rl.Limit) would make
	// this assertion fail — that mutate-and-confirm-RED check was run
	// manually against this exact test during authoring (see the
	// implementer's report).
	if !(rl.Limit > 0 && rl.Remaining == 0) {
		t.Fatalf("exhausted condition not detected: %+v", rl)
	}
}

func TestRateLimitStatus_NilResponse(t *testing.T) {
	c := NewClient("", nil)
	rl := c.RateLimitStatus(nil)
	if rl.Limit != 0 || rl.Remaining != 0 || !rl.Reset.IsZero() {
		t.Fatalf("expected zero-value RateLimit for a nil response, got %+v", rl)
	}
}

// TestDoJSON_LogsWarningOnExhaustedRateLimit is the F3 remediation test.
// Previously this test used an EMPTY token, so the "token never logged"
// invariant it exists to guard was vacuous — a mutation that logged
// req.Header.Get("Authorization") in the warning would have produced an
// empty-string log field and still passed. It now uses a real non-empty
// fake token and asserts the captured warning logs contain NO substring
// of that token, across every log message AND every structured field.
func TestDoJSON_LogsWarningOnExhaustedRateLimit(t *testing.T) {
	const fakeToken = "test-token-warn-do-not-log-456"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "60")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1750003600")
		fmt.Fprint(w, `{"sha":"abc"}`)
	}))
	defer srv.Close()

	c, logs := newTestClient(t, srv, fakeToken)
	if _, err := c.GetHeadSHA(t.Context(), "o", "r", "main"); err != nil {
		t.Fatalf("GetHeadSHA: %v", err)
	}
	found := false
	for _, e := range logs.All() {
		if e.Level == zapcore.WarnLevel && strings.Contains(e.Message, "rate limit exhausted") {
			found = true
		}
		if strings.Contains(e.Message, fakeToken) {
			t.Fatalf("token leaked into log message: %q", e.Message)
		}
		for _, f := range e.Context {
			if s, ok := fieldStringValue(f); ok && strings.Contains(s, fakeToken) {
				t.Fatalf("token leaked into log field %s: %q", f.Key, s)
			}
		}
	}
	if !found {
		t.Fatalf("expected a rate-limit-exhausted warning to be logged, got: %+v", logs.All())
	}
}

// ---------------------------------------------------------------------------
// TokenFromEnv
// ---------------------------------------------------------------------------

func TestTokenFromEnv(t *testing.T) {
	const varName = "HELIX_SOURCE_GITHUB_TEST_TOKEN_PROBE"
	if err := os.Unsetenv(varName); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
	if got := TokenFromEnv(varName); got != "" {
		t.Fatalf("expected empty token when env var unset, got %q", got)
	}

	t.Setenv(varName, "sk-example-not-a-real-token")
	if got := TokenFromEnv(varName); got != "sk-example-not-a-real-token" {
		t.Fatalf("expected token from env var, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Optional live smoke test — SKIPped whenever no token/network opt-in is
// configured, per §11.4.3 (topology-appropriate SKIP, never PASS-by-default)
// and the offline-by-default requirement for this item. Enable by setting
// HELIX_SOURCE_GITHUB_LIVE_TEST_TOKEN to a real (read-only, low-privilege)
// GitHub token; the test then makes ONE real, read-only API call against
// the small, stable, public anthropics/skills repository named in
// docs/source_ingestion/CATALOG.md as the Tier-A pick for this feature.
// ---------------------------------------------------------------------------

func TestLive_GetHeadSHA_RealGitHubAPI(t *testing.T) {
	token := TokenFromEnv("HELIX_SOURCE_GITHUB_LIVE_TEST_TOKEN")
	if token == "" {
		t.Skip("SKIP reason=credentials_absent: HELIX_SOURCE_GITHUB_LIVE_TEST_TOKEN not set; " +
			"this is the ONLY test in this package that touches the network, and it is opt-in only")
	}
	c := NewClient(token, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sha, err := c.GetHeadSHA(ctx, "anthropics", "skills", "main")
	if err != nil {
		t.Fatalf("live GetHeadSHA against anthropics/skills: %v", err)
	}
	if sha == "" {
		t.Fatal("live GetHeadSHA returned an empty sha")
	}
}
