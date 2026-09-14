package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// APIClient wraps HTTP calls for CLI commands
type APIClient struct {
	BaseURL string
	APIKey  string
	Client  *http.Client
	Verbose bool
}

// getAPIClient creates an API client from the global flag values in the cobra command
func getAPIClient(cmd *cobra.Command) *APIClient {
	apiURL, _ := cmd.Root().PersistentFlags().GetString("api-url")
	apiKey, _ := cmd.Root().PersistentFlags().GetString("api-key")
	verbose, _ := cmd.Root().PersistentFlags().GetBool("verbose")

	return &APIClient{
		BaseURL: strings.TrimRight(apiURL, "/"),
		APIKey:  apiKey,
		Client:  &http.Client{Timeout: 30 * time.Second},
		Verbose: verbose,
	}
}

// getGlobalFormat returns the global --format flag value from the root command.
func getGlobalFormat(cmd *cobra.Command) string {
	f, _ := cmd.Root().PersistentFlags().GetString("format")
	return f
}

// Request makes an HTTP request with proper headers and error handling
func (c *APIClient) Request(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	url := c.BaseURL + path
	if c.Verbose {
		fmt.Fprintf(os.Stderr, "[http] %s %s\n", method, url)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	SetAuthHeader(req, c.APIKey)

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(bodyBytes))
	}

	return resp, nil
}

// SetAuthHeader is the single seam every first-party request to THIS project's
// server routes through to apply the API key. It sends the key in the
// server-canonical X-API-Key header (see internal/api/middleware.go APIKeyAuth /
// ResolveAPIKeyAuth, which reads X-API-Key ONLY). Sending "Authorization: Bearer"
// here would 401 the moment auth is enforced (G35) — so every CLI sender
// (commands.APIClient.Request, the raw-HTTP skill create/update/import/export
// requests in skill.go, and the duplicate config client in cmd/cli/main.go) uses
// THIS one function, giving a single guarded place that can never drift back to
// Bearer. An empty key sets no auth header at all.
//
// This is deliberately NOT used by the external OpenAI-compatible clients
// (internal/autoexpand/llm.go, internal/db/embedding.go), which correctly send
// "Authorization: Bearer" to their upstream provider APIs.
func SetAuthHeader(req *http.Request, apiKey string) {
	if apiKey == "" {
		return
	}
	req.Header.Set("X-API-Key", apiKey)
}

// Output prints data in JSON format
func (c *APIClient) Output(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// Terminal color codes (low-saturation professional palette)
var (
	colorReset  = "\033[0m"
	colorRed    = "\033[38;5;167m" // Soft red for errors/issues
	colorGreen  = "\033[38;5;114m" // Soft green for success
	colorBlue   = "\033[38;5;110m" // Soft blue for info/running
	colorYellow = "\033[38;5;180m" // Soft yellow for warnings
	colorGray   = "\033[38;5;245m" // Gray for secondary text
)
