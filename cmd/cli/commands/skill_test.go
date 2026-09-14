package commands

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

// newTestRoot builds a minimal root command exposing the same persistent flags
// (api-url, api-key, verbose) that getAPIClient() reads, with the skill command
// attached, so the skill subcommands can be driven end-to-end in-process.
func newTestRoot() *cobra.Command {
	root := &cobra.Command{Use: "skill-system", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().String("api-url", "http://localhost:8080", "")
	root.PersistentFlags().String("api-key", "", "")
	root.PersistentFlags().Bool("verbose", false, "")
	root.AddCommand(NewSkillCommand())
	return root
}

// TestSkillSubcommandsSendXAPIKeyNotBearer is a G35 recurrence guard (§11.4.135)
// for the raw-HTTP first-party senders in skill.go (create/update/import/export)
// that build their own *http.Request and previously bypassed the fixed
// APIClient.Request. Each case drives the real cobra subcommand against a server
// that authenticates the way the backend middleware does — X-API-Key only (see
// internal/api/middleware.go APIKeyAuth) — and asserts the subcommand carries the
// key in X-API-Key and NEVER as "Authorization: Bearer".
//
// RED on the pre-fix code: the server sees an empty X-API-Key, 401s, and the
// subcommand returns an error. GREEN once every first-party sender routes through
// the single SetAuthHeader seam. A first-party path that reverts to Bearer trips
// this guard.
func TestSkillSubcommandsSendXAPIKeyNotBearer(t *testing.T) {
	const wantKey = "g35-secret-key"

	tmp := t.TempDir()
	jsonFile := filepath.Join(tmp, "skill.json")
	if err := os.WriteFile(jsonFile, []byte(`{"name":"demo"}`), 0o600); err != nil {
		t.Fatalf("write skill file: %v", err)
	}
	exportOut := filepath.Join(tmp, "out.json")

	cases := []struct {
		name string
		args []string
		body string // server success-path response body
	}{
		{"create", []string{"skill", "create", "--file", jsonFile}, `{"name":"demo"}`},
		{"update", []string{"skill", "update", "demo", "--file", jsonFile}, `{}`},
		{"import", []string{"skill", "import", "--file", jsonFile}, `{"imported":1}`},
		{"export", []string{"skill", "export", "demo", "--output", exportOut}, `name = "demo"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotAPIKey, gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAPIKey = r.Header.Get("X-API-Key")
				gotAuth = r.Header.Get("Authorization")
				// Mirror the real middleware: authenticate off X-API-Key only.
				if gotAPIKey != wantKey {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			root := newTestRoot()
			root.SetArgs(append([]string{"--api-url", srv.URL, "--api-key", wantKey}, tc.args...))

			if err := root.Execute(); err != nil {
				t.Fatalf("skill %s failed against an X-API-Key server: %v "+
					"(server saw X-API-Key=%q, Authorization=%q)", tc.name, err, gotAPIKey, gotAuth)
			}
			if gotAPIKey != wantKey {
				t.Fatalf("skill %s: server-canonical header X-API-Key=%q, want %q "+
					"(client sent Authorization=%q instead)", tc.name, gotAPIKey, wantKey, gotAuth)
			}
			if gotAuth != "" {
				t.Fatalf("skill %s: first-party request set Authorization=%q; first-party "+
					"senders must use X-API-Key only, never Bearer", tc.name, gotAuth)
			}
		})
	}
}

// TestSetAuthHeaderUsesServerCanonicalHeader guards the single auth-header seam
// (§11.4.135) every first-party CLI request routes through. It MUST set the
// server-canonical X-API-Key and MUST NOT set "Authorization: Bearer". Reverting
// the seam to Bearer trips this guard (and, because every first-party sender
// routes through it, the end-to-end guards above).
func TestSetAuthHeaderUsesServerCanonicalHeader(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://example.test/api/v1/ping", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	SetAuthHeader(req, "the-key")
	if got := req.Header.Get("X-API-Key"); got != "the-key" {
		t.Fatalf("X-API-Key = %q, want %q", got, "the-key")
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want empty (never Bearer)", got)
	}

	// An empty key sets no auth header at all.
	req2, err := http.NewRequest(http.MethodGet, "http://example.test/", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	SetAuthHeader(req2, "")
	if got := req2.Header.Get("X-API-Key"); got != "" {
		t.Fatalf("empty key set X-API-Key=%q, want none", got)
	}
}
