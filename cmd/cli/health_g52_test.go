package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/cobra"
)

// TestConfigTestProbesServerRealHealthRoute is the G52 contract + recurrence
// guard (§11.4.115 RED-polarity + §11.4.135 permanent guard) for the CLI
// `skill-system config test` connectivity probe.
//
// The live server serves the OPEN health route at ROOT /health only
// (cmd/server/main.go buildRouter), and the design contract (docs/API.md →
// "Health & Info → GET /health") agrees; there is NO /api/v1/health route. The
// pre-fix `config test` probes /api/v1/health, which 404s, so `config test`
// reports "API connection failed" against a perfectly healthy server.
//
// This test drives the REAL `config test` command call site (its RunE, so the
// hardcoded probe path is what is under test) against an httptest server that
// mirrors the live contract — serves /health (200) and 404s everything else,
// including /api/v1/health. It is RED on the pre-fix code (probe → 404 → RunE
// returns "API connection failed") and GREEN once the probe repoints to /health.
//
// Health is an OPEN route: the server here requires no API key, proving the
// probe does not wrongly demand auth.
func TestConfigTestProbesServerRealHealthRoute(t *testing.T) {
	var probedPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probedPaths = append(probedPaths, r.URL.Path)
		// Mirror the live server: the open health route is ROOT /health only.
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"healthy","server":"helix-knowledge-skill-system"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	// Point the CLI's global flags at the test server, no API key (open route).
	oldURL, oldKey, oldFormat := apiURL, apiKey, format
	apiURL, apiKey, format = srv.URL, "", "json"
	defer func() { apiURL, apiKey, format = oldURL, oldKey, oldFormat }()

	// Resolve the real `config test` subcommand and drive its RunE so the
	// literal probe path in main.go is exercised, not a path this test supplies.
	cfg := newConfigCommand()
	var testCmd *cobra.Command
	for _, sub := range cfg.Commands() {
		if sub.Name() == "test" {
			testCmd = sub
			break
		}
	}
	if testCmd == nil {
		t.Fatal("`config test` subcommand not found")
	}
	// The RunE derives its timeout from cmd.Context(); give it a real parent so
	// the test fails only for the genuine probe-path defect, never a nil-context
	// panic (§11.4.1 no FAIL-bluff).
	testCmd.SetContext(context.Background())

	if err := testCmd.RunE(testCmd, nil); err != nil {
		t.Fatalf("`config test` failed against a server serving the real open /health route: %v "+
			"(probed paths: %v)", err, probedPaths)
	}

	// The connectivity probe MUST hit the route the server actually serves.
	if len(probedPaths) == 0 || probedPaths[len(probedPaths)-1] != "/health" {
		t.Fatalf("`config test` probed %v, want it to hit /health (the server's real open health "+
			"route; there is no /api/v1/health route)", probedPaths)
	}
}
