package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// newCORSRouter builds a minimal gin engine wired with the CORS middleware
// under test plus a terminal handler so we can observe whether the request
// actually reached application code (as opposed to being aborted by the
// middleware, e.g. on a rejected preflight).
func newCORSRouter(allowedOrigins []string) (*gin.Engine, *bool) {
	reachedHandler := false
	r := gin.New()
	r.Use(CORS(allowedOrigins))
	r.Any("/resource", func(c *gin.Context) {
		reachedHandler = true
		c.Status(http.StatusOK)
	})
	return r, &reachedHandler
}

// TestCORS_AllowlistedOrigin verifies that an Origin present in the allowlist
// is reflected back with credentials enabled, per the CORS contract described
// on the CORS() doc comment: only an exact allowlist match may carry
// Allow-Credentials: true.
func TestCORS_AllowlistedOrigin(t *testing.T) {
	router, reached := newCORSRouter([]string{"https://good.example"})

	req := httptest.NewRequest(http.MethodGet, "/resource", nil)
	req.Header.Set("Origin", "https://good.example")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://good.example" {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, "https://good.example")
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("Access-Control-Allow-Credentials = %q, want %q", got, "true")
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want %q", got, "Origin")
	}
	if !*reached {
		t.Error("handler was not reached for an allowlisted origin")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

// TestCORS_NonAllowlistedOriginIsNeverReflected is the primary security
// regression test: an Origin absent from the allowlist MUST NOT be echoed
// back in Access-Control-Allow-Origin (no reflection) and MUST NOT receive
// Allow-Credentials. If this test is broken by re-introducing "reflect any
// Origin" behavior, it must fail — see the paired-mutation demonstration in
// the task report.
func TestCORS_NonAllowlistedOriginIsNeverReflected(t *testing.T) {
	router, reached := newCORSRouter([]string{"https://good.example"})

	req := httptest.NewRequest(http.MethodGet, "/resource", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty (no reflection of disallowed origin)", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("Access-Control-Allow-Credentials = %q, want empty for a disallowed origin", got)
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want %q (cache-safety must hold even when rejecting)", got, "Origin")
	}
	// A non-OPTIONS request still reaches the handler (CORS only blocks the
	// browser from reading the response; it doesn't turn a GET into an error
	// response by itself unless it's a preflight).
	if !*reached {
		t.Error("non-preflight handler should still run even for a disallowed origin")
	}
}

// TestCORS_WildcardAllowsAnyOriginWithoutCredentials verifies the "*" allowlist
// entry permits any origin but the middleware NEVER combines that with
// Allow-Credentials: true (which the CORS spec forbids as an open redirect /
// credential-leak footgun).
func TestCORS_WildcardAllowsAnyOriginWithoutCredentials(t *testing.T) {
	router, reached := newCORSRouter([]string{"*"})

	req := httptest.NewRequest(http.MethodGet, "/resource", nil)
	req.Header.Set("Origin", "https://anything.example")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, "*")
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("Access-Control-Allow-Credentials = %q, want empty when wildcard-allowed (spec forbids wildcard+credentials)", got)
	}
	if !*reached {
		t.Error("handler was not reached for a wildcard-allowed origin")
	}
}

// TestCORS_EmptyAllowlistFailsClosed verifies that an empty allowlist grants
// no cross-origin access at all: no Access-Control-Allow-Origin header is
// ever emitted, for any Origin.
func TestCORS_EmptyAllowlistFailsClosed(t *testing.T) {
	router, reached := newCORSRouter(nil)

	req := httptest.NewRequest(http.MethodGet, "/resource", nil)
	req.Header.Set("Origin", "https://anything.example")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty (fail-closed with empty allowlist)", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("Access-Control-Allow-Credentials = %q, want empty", got)
	}
	if !*reached {
		t.Error("non-preflight handler should still run even with an empty allowlist")
	}
}

// TestCORS_PreflightRejectedForDisallowedOrigin verifies an OPTIONS preflight
// from a disallowed origin is aborted with 403 rather than a permissive
// no-op success, and never reaches application code.
func TestCORS_PreflightRejectedForDisallowedOrigin(t *testing.T) {
	router, reached := newCORSRouter([]string{"https://good.example"})

	req := httptest.NewRequest(http.MethodOptions, "/resource", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("preflight status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if *reached {
		t.Error("handler must not run for a rejected preflight")
	}
}

// TestCORS_PreflightAcceptedForAllowlistedOrigin verifies an OPTIONS
// preflight from an allowlisted origin returns 204 No Content.
func TestCORS_PreflightAcceptedForAllowlistedOrigin(t *testing.T) {
	router, reached := newCORSRouter([]string{"https://good.example"})

	req := httptest.NewRequest(http.MethodOptions, "/resource", nil)
	req.Header.Set("Origin", "https://good.example")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://good.example" {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, "https://good.example")
	}
	if *reached {
		t.Error("handler must not run for a preflight (OPTIONS is always aborted)")
	}
}

// TestCORS_MultipleOriginsInAllowlist is a table-driven pass over several
// origins against a multi-entry allowlist, confirming each origin is
// evaluated independently (exact match only, no substring/prefix matching).
func TestCORS_MultipleOriginsInAllowlist(t *testing.T) {
	allowlist := []string{"https://a.example", "https://b.example"}

	tests := []struct {
		name        string
		origin      string
		wantAllowed bool
	}{
		{"first allowlisted origin", "https://a.example", true},
		{"second allowlisted origin", "https://b.example", true},
		{"similar but not identical origin (subdomain)", "https://evil.a.example", false},
		{"similar but not identical origin (suffix)", "https://a.example.evil.com", false},
		{"completely unrelated origin", "https://c.example", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router, _ := newCORSRouter(allowlist)
			req := httptest.NewRequest(http.MethodGet, "/resource", nil)
			req.Header.Set("Origin", tt.origin)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			got := rec.Header().Get("Access-Control-Allow-Origin")
			if tt.wantAllowed && got != tt.origin {
				t.Errorf("origin %q: Access-Control-Allow-Origin = %q, want %q", tt.origin, got, tt.origin)
			}
			if !tt.wantAllowed && got != "" {
				t.Errorf("origin %q: Access-Control-Allow-Origin = %q, want empty (not allowlisted)", tt.origin, got)
			}
		})
	}
}

// TestCORS_NoOriginHeaderIsTreatedAsSameOriginOrNonBrowser verifies that a
// request without an Origin header (same-origin navigation, curl, server-to-
// server calls) is never blocked and never gets an Allow-Origin header
// (there's nothing to reflect).
func TestCORS_NoOriginHeaderIsTreatedAsSameOriginOrNonBrowser(t *testing.T) {
	router, reached := newCORSRouter([]string{"https://good.example"})

	req := httptest.NewRequest(http.MethodGet, "/resource", nil)
	// No Origin header set.
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty when no Origin header is present", got)
	}
	if !*reached {
		t.Error("handler should run when no Origin header is present")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

// ---------------------------------------------------------------------------
// Logger query-param redaction (redactQuery)
// ---------------------------------------------------------------------------

// TestRedactQuery covers the sensitive-query-parameter redaction helper used
// by Logger() before writing the raw query string to structured logs. This
// guards against secrets (API keys, tokens, passwords) leaking into log
// aggregation systems.
func TestRedactQuery(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "no sensitive params left untouched",
			raw:  "foo=bar&baz=qux",
			want: "foo=bar&baz=qux",
		},
		{
			name: "api_key redacted",
			raw:  "api_key=SECRET123&user=alice",
			want: "api_key=REDACTED&user=alice",
		},
		{
			name: "token redacted",
			raw:  "token=abc123&q=search",
			want: "q=search&token=REDACTED",
		},
		{
			name: "password redacted",
			raw:  "password=hunter2",
			want: "password=REDACTED",
		},
		{
			name: "case-insensitive key match",
			raw:  "Token=ABC123",
			want: "Token=REDACTED",
		},
		{
			name: "multiple values for same sensitive key all redacted",
			raw:  "password=a&password=b",
			want: "password=REDACTED&password=REDACTED",
		},
		{
			name: "multiple distinct sensitive params",
			raw:  "api_key=k1&secret=s1&signature=sig1",
			want: "api_key=REDACTED&secret=REDACTED&signature=REDACTED",
		},
		{
			name: "empty query string",
			raw:  "",
			want: "",
		},
		{
			name: "malformed query redacted wholesale",
			raw:  "%zz",
			want: "REDACTED",
		},
		{
			name: "authorization and access_token redacted",
			raw:  "authorization=Bearer%20xyz&access_token=at1&refresh_token=rt1",
			want: "access_token=REDACTED&authorization=REDACTED&refresh_token=REDACTED",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := redactQuery(tt.raw)
			if got != tt.want {
				t.Errorf("redactQuery(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// TestRedactQuery_NeverLeaksSecretValue is a targeted anti-bluff check: for
// every sensitive key, the ORIGINAL secret value must never appear anywhere
// in the redacted output.
func TestRedactQuery_NeverLeaksSecretValue(t *testing.T) {
	secrets := []string{
		"api_key=super-secret-key-value",
		"password=correct-horse-battery-staple",
		"token=eyJhbGciOiJIUzI1NiJ9.leaked.jwt",
		"secret=do-not-log-me",
		"authorization=Bearer-super-secret-bearer-token",
	}

	for _, raw := range secrets {
		t.Run(raw, func(t *testing.T) {
			got := redactQuery(raw)
			// Extract the value after '=' to check it never survives.
			eq := -1
			for i, c := range raw {
				if c == '=' {
					eq = i
					break
				}
			}
			if eq == -1 {
				t.Fatalf("test data malformed, no '=' in %q", raw)
			}
			secretValue := raw[eq+1:]
			if secretValue != "" && strings.Contains(got, secretValue) {
				t.Errorf("redactQuery(%q) = %q leaked the secret value %q", raw, got, secretValue)
			}
		})
	}
}
