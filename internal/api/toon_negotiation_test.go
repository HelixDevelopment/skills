package api

// G08 integration tests: application/toon content negotiation on the LIVE
// middleware + response stack (no DB required — a minimal gin router carrying
// the real ContentNegotiation()/DetectContentType() middleware and a terminal
// handler that calls the real NegotiateResponse()/parseRequestBody()).
//
// RED→GREEN discipline (§11.4.115): on the pre-fix code (ContentNegotiation
// unaware of application/toon; NegotiateResponse routing only JSON/TOML;
// parseRequestBody decoding only JSON/TOML) TestContentNegotiation_AcceptTOON,
// _FormatQueryTOON and TestRequestBody_TOONDecoded FAIL (JSON is returned for an
// Accept: application/toon request; a TOON body fails to decode). After the
// wiring they PASS. TestContentNegotiation_UnknownAcceptFallsBackToJSON and
// _AcceptJSON guard that JSON stays the safety net (§11.4.6).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/HelixDevelopment/skills/internal/toon"
	"github.com/gin-gonic/gin"
)

// negotiationPayload is an unambiguous fixture: its TOON encoding
// (`active: true\nid: 1\nname: Alice\ntags[2]: a,b`) is clearly not JSON.
func negotiationPayload() map[string]any {
	return map[string]any{
		"id":     1,
		"name":   "Alice",
		"active": true,
		"tags":   []string{"a", "b"},
	}
}

// jsonModel projects any value onto the generic JSON data model for structural
// comparison (numbers become float64, etc.) — the same model TOON round-trips.
func jsonModel(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal fixture: %v", err)
	}
	var m any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("json.Unmarshal fixture: %v", err)
	}
	return m
}

func newNegotiationRouter() *gin.Engine {
	r := gin.New()
	r.Use(ContentNegotiation())
	r.GET("/resource", func(c *gin.Context) {
		NegotiateResponse(c, http.StatusOK, negotiationPayload())
	})
	return r
}

// TestContentNegotiation_AcceptTOON is the primary G08 guard: an
// `Accept: application/toon` request MUST receive a TOON body with a
// `Content-Type: application/toon` header — NOT a silently-substituted JSON body
// (the danger-zone #2 bluff G08 closes).
func TestContentNegotiation_AcceptTOON(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/resource", nil)
	req.Header.Set("Accept", "application/toon")
	rec := httptest.NewRecorder()

	newNegotiationRouter().ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/toon") {
		t.Fatalf("Content-Type = %q, want application/toon", ct)
	}
	body := strings.TrimSpace(rec.Body.String())
	if strings.HasPrefix(body, "{") {
		t.Fatalf("body looks like JSON, expected TOON:\n%s", body)
	}
	got, err := toon.Decode([]byte(body))
	if err != nil {
		t.Fatalf("response body is not valid TOON: %v\n%s", err, body)
	}
	if want := jsonModel(t, negotiationPayload()); !reflect.DeepEqual(got, want) {
		t.Errorf("decoded TOON mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

// TestContentNegotiation_FormatQueryTOON verifies the ?format=toon override.
func TestContentNegotiation_FormatQueryTOON(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/resource?format=toon", nil)
	rec := httptest.NewRecorder()

	newNegotiationRouter().ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/toon") {
		t.Fatalf("Content-Type = %q, want application/toon", ct)
	}
	if _, err := toon.Decode([]byte(strings.TrimSpace(rec.Body.String()))); err != nil {
		t.Fatalf("?format=toon body is not valid TOON: %v", err)
	}
}

// TestContentNegotiation_AcceptJSON guards that JSON is unaffected.
func TestContentNegotiation_AcceptJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/resource", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()

	newNegotiationRouter().ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("JSON body did not parse: %v\n%s", err, rec.Body.String())
	}
}

// TestContentNegotiation_UnknownAcceptFallsBackToJSON is the register's explicit
// "unknown → JSON fallback with correct Content-Type" requirement: an Accept the
// server does not speak MUST fall back to JSON, never error or emit TOON.
func TestContentNegotiation_UnknownAcceptFallsBackToJSON(t *testing.T) {
	for _, accept := range []string{"application/xml", "text/csv", "*/*", ""} {
		accept := accept
		t.Run("accept="+accept, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/resource", nil)
			if accept != "" {
				req.Header.Set("Accept", accept)
			}
			rec := httptest.NewRecorder()

			newNegotiationRouter().ServeHTTP(rec, req)

			if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
				t.Fatalf("Accept %q: Content-Type = %q, want application/json fallback", accept, ct)
			}
			if got := strings.TrimSpace(rec.Body.String()); !strings.HasPrefix(got, "{") {
				t.Fatalf("Accept %q: fallback body is not JSON:\n%s", accept, got)
			}
		})
	}
}

// TestRequestBody_TOONDecoded verifies the request half: a body sent with
// `Content-Type: application/toon` decodes through parseRequestBody exactly as
// the JSON/TOML paths do, so every handler that already uses parseRequestBody
// accepts TOON by default.
func TestRequestBody_TOONDecoded(t *testing.T) {
	type payload struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}
	toonBody, err := toon.MarshalString(payload{Name: "Kotlin", Tags: []string{"jvm", "android"}})
	if err != nil {
		t.Fatalf("encode TOON body: %v", err)
	}

	r := gin.New()
	r.Use(DetectContentType())
	var decoded payload
	r.POST("/in", func(c *gin.Context) {
		if err := parseRequestBody(c, &decoded); err != nil {
			RespondError(c, http.StatusBadRequest, err.Error())
			return
		}
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/in", strings.NewReader(toonBody))
	req.Header.Set("Content-Type", "application/toon")
	rec := httptest.NewRecorder()

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	want := payload{Name: "Kotlin", Tags: []string{"jvm", "android"}}
	if !reflect.DeepEqual(decoded, want) {
		t.Errorf("decoded TOON request body = %+v, want %+v", decoded, want)
	}
}

// TestRespondError_TOON verifies the error envelope is also emitted as TOON when
// TOON is negotiated (errors must not silently drop to JSON mid-format).
func TestRespondError_TOON(t *testing.T) {
	r := gin.New()
	r.Use(ContentNegotiation())
	r.GET("/boom", func(c *gin.Context) {
		RespondError(c, http.StatusTeapot, "kaboom")
	})

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	req.Header.Set("Accept", "application/toon")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/toon") {
		t.Fatalf("error Content-Type = %q, want application/toon", ct)
	}
	got, err := toon.Decode(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("error body is not valid TOON: %v\n%s", err, rec.Body.String())
	}
	m, ok := got.(map[string]any)
	if !ok || m["error"] != "kaboom" {
		t.Errorf("decoded error envelope = %#v, want error=kaboom", got)
	}
}
