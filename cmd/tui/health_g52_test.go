package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHealthCheckProbesServerRealHealthRoute is the G52 contract + recurrence
// guard (§11.4.115 RED-polarity + §11.4.135 permanent guard).
//
// The live server registers the OPEN health endpoint at ROOT /health with no
// auth (cmd/server/main.go buildRouter, "Health check (open)"), and advertises
// exactly "GET  /health" in its server-info route; the design contract
// (docs/API.md → "Health & Info → GET /health", curl http://.../health) says
// the same. There is NO /api/v1/health route on the server, so a probe of
// /api/v1/health 404s and the TUI reports the API unreachable while it is
// actually healthy.
//
// This test drives a real httptest server that mirrors the live contract —
// serves /health (200 healthy) and 404s everything else, including
// /api/v1/health — and asserts the TUI HealthCheck probes the route the server
// actually serves. It is RED on the pre-fix code (which probes /api/v1/health →
// 404 → HealthCheck returns false, and the recorded path is /api/v1/health) and
// GREEN once the probe repoints to /health.
//
// Health is an OPEN route: this test's server requires no API key, proving the
// probe does not wrongly demand auth.
func TestHealthCheckProbesServerRealHealthRoute(t *testing.T) {
	var probedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probedPath = r.URL.Path
		// Mirror the live server: the open health route is ROOT /health only.
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"healthy","server":"helix-knowledge-skill-system"}`))
			return
		}
		// Any other path — notably /api/v1/health — does not exist.
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := NewAPIClient(srv.URL, "")
	c.client = srv.Client()

	if !c.HealthCheck(context.Background()) {
		t.Fatalf("HealthCheck reported the API unreachable against a server serving the real "+
			"open /health route (it probed %q instead of /health)", probedPath)
	}

	if probedPath != "/health" {
		t.Fatalf("HealthCheck probed %q, want /health (the server's real open health route; "+
			"there is no /api/v1/health route)", probedPath)
	}
}
