package commands

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRequestSendsServerCanonicalAPIKeyHeader is the G35 contract test.
//
// It drives a minimal server that authenticates the way the real backend
// middleware does — reading the API key from the X-API-Key header only (see
// internal/api/middleware.go APIKeyAuth, which calls c.GetHeader("X-API-Key"))
// — and asserts the CLI client's outgoing request carries the key in that exact
// header. The client-sent header MUST equal the server-read header (§11.4.135
// recurrence guard for G35). This test is RED on the pre-fix code that sends
// "Authorization: Bearer <key>" (the minimal server sees an empty X-API-Key and
// returns 401), and GREEN once the client unifies on X-API-Key.
func TestRequestSendsServerCanonicalAPIKeyHeader(t *testing.T) {
	const wantKey = "g35-secret-key"

	var gotAPIKey, gotAuthorization string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("X-API-Key")
		gotAuthorization = r.Header.Get("Authorization")
		// Mirror the real server middleware: authenticate off X-API-Key only.
		if gotAPIKey != wantKey {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := &APIClient{
		BaseURL: srv.URL,
		APIKey:  wantKey,
		Client:  srv.Client(),
	}

	resp, err := c.Request(context.Background(), http.MethodGet, "/api/v1/ping", nil)
	if err != nil {
		t.Fatalf("CLI client failed to authenticate against a server that reads X-API-Key: %v "+
			"(server saw X-API-Key=%q, Authorization=%q)", err, gotAPIKey, gotAuthorization)
	}
	defer resp.Body.Close()

	if gotAPIKey != wantKey {
		t.Fatalf("server-canonical header X-API-Key = %q, want %q "+
			"(client sent the key as Authorization=%q instead)", gotAPIKey, wantKey, gotAuthorization)
	}
}
