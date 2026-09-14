package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestConfigClientRequestSendsXAPIKeyNotBearer is the G35 recurrence guard
// (§11.4.135) for the duplicate main.APIClient used by `skill-system config test`
// and `config show`. It drives a server that authenticates the way the backend
// middleware does — X-API-Key only (see internal/api/middleware.go APIKeyAuth) —
// and asserts the outgoing request carries the key in that header and never as
// "Authorization: Bearer".
//
// RED on the pre-fix duplicate client (which set Bearer, so the server sees an
// empty X-API-Key and 401s); GREEN once it routes through the shared
// commands.SetAuthHeader seam.
func TestConfigClientRequestSendsXAPIKeyNotBearer(t *testing.T) {
	const wantKey = "g35-secret-key"

	var gotAPIKey, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("X-API-Key")
		gotAuth = r.Header.Get("Authorization")
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

	resp, err := c.Request(context.Background(), http.MethodGet, "/api/v1/health", nil)
	if err != nil {
		t.Fatalf("config client failed against an X-API-Key server: %v "+
			"(server saw X-API-Key=%q, Authorization=%q)", err, gotAPIKey, gotAuth)
	}
	defer resp.Body.Close()

	if gotAPIKey != wantKey {
		t.Fatalf("server-canonical header X-API-Key=%q, want %q "+
			"(client sent Authorization=%q instead)", gotAPIKey, wantKey, gotAuth)
	}
	if gotAuth != "" {
		t.Fatalf("config client set Authorization=%q; first-party senders must use X-API-Key only", gotAuth)
	}
}
