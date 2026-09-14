package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/HelixDevelopment/skills/cmd/cli/commands"
	"github.com/HelixDevelopment/skills/internal/models"
	toml "github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
)

var (
	// Global flags
	apiURL  string
	apiKey  string
	format  string
	verbose bool
)

// APIClient wraps HTTP calls for the CLI
type APIClient struct {
	BaseURL string
	APIKey  string
	Client  *http.Client
	Verbose bool
}

// NewAPIClient creates a new API client from global flags
func NewAPIClient() *APIClient {
	return &APIClient{
		BaseURL: strings.TrimRight(apiURL, "/"),
		APIKey:  apiKey,
		Client:  &http.Client{Timeout: 30 * time.Second},
		Verbose: verbose,
	}
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

	// Content negotiation
	contentType := "application/json"
	if format == "toml" {
		contentType = "application/toml"
	}
	req.Header.Set("Accept", contentType)
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	// Route through the single first-party auth seam (see commands.SetAuthHeader):
	// the key travels in the server-canonical X-API-Key header, never as
	// "Authorization: Bearer" which the server does not read and 401s under
	// enforced auth (G35). This dedupes the config client onto the shared seam.
	commands.SetAuthHeader(req, c.APIKey)

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(bodyBytes))
	}

	return resp, nil
}

// OutputJSON prints data as JSON
func (c *APIClient) OutputJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// OutputTOML prints data as TOML.
func (c *APIClient) OutputTOML(v interface{}) error {
	data, err := toml.Marshal(v)
	if err != nil {
		// TOML encoding requires a struct or map at the top level. For values
		// it cannot represent (e.g. slices or scalars), fall back to JSON so
		// the command still produces usable output instead of failing.
		return c.OutputJSON(v)
	}
	_, err = os.Stdout.Write(data)
	return err
}

// Output prints data in the configured format
func (c *APIClient) Output(v interface{}) error {
	if format == "toml" {
		return c.OutputTOML(v)
	}
	return c.OutputJSON(v)
}

func main() {
	rootCmd := &cobra.Command{
		Use:   "skill-system",
		Short: "HelixKnowledge Skill Graph System CLI",
		Long: `skill-system is the command-line interface for the HelixKnowledge Skill Graph System.

Manage skills, search the knowledge graph, monitor registry health,
trigger expansions, and submit learning jobs - all from your terminal.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       "1.0.0",
	}

	// Global flags
	rootCmd.PersistentFlags().StringVar(&apiURL, "api-url", "http://localhost:8080", "Base URL of the skill system API")
	rootCmd.PersistentFlags().StringVar(&apiKey, "api-key", "", "API key for authentication")
	rootCmd.PersistentFlags().StringVar(&format, "format", "json", "Output format: json|toml")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose output")

	// Validate format flag
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if format != "json" && format != "toml" {
			return fmt.Errorf("invalid format %q: must be 'json' or 'toml'", format)
		}
		return nil
	}

	// Add subcommands
	rootCmd.AddCommand(commands.NewSkillCommand())
	rootCmd.AddCommand(commands.NewSearchCommand())
	rootCmd.AddCommand(commands.NewRegistryCommand())
	rootCmd.AddCommand(commands.NewExpandCommand())
	rootCmd.AddCommand(commands.NewLearnCommand())
	rootCmd.AddCommand(newConfigCommand())
	commands.RegisterSourceCmd(rootCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func newConfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage CLI configuration",
		Long:  "View and update the CLI configuration settings.",
	}

	showCmd := &cobra.Command{
		Use:   "show",
		Short: "Show current configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			config := map[string]interface{}{
				"api_url": apiURL,
				"api_key": maskSecret(apiKey),
				"format":  format,
				"verbose": verbose,
			}
			client := NewAPIClient()
			return client.Output(config)
		},
	}

	testCmd := &cobra.Command{
		Use:   "test",
		Short: "Test API connectivity",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := NewAPIClient()
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()

			// Probe the server's OPEN health route at ROOT /health (see
			// cmd/server/main.go buildRouter "Health check (open)" and
			// docs/API.md → "Health & Info → GET /health"). There is no
			// /api/v1/health route, so probing under /api/v1 would 404 and
			// report a healthy server as unreachable (G52). Health is
			// unauthenticated; SetAuthHeader is a no-op without a key.
			resp, err := client.Request(ctx, http.MethodGet, "/health", nil)
			if err != nil {
				return fmt.Errorf("API connection failed: %w", err)
			}
			defer resp.Body.Close()

			var health models.SkillRegistryEntry
			if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
				// Health endpoint may return different structure
				fmt.Println("API connection: OK")
				return nil
			}

			fmt.Println("API connection: OK")
			return client.Output(health)
		},
	}

	cmd.AddCommand(showCmd, testCmd)
	return cmd
}

func maskSecret(s string) string {
	if s == "" {
		return "(not set)"
	}
	if len(s) <= 4 {
		return "****"
	}
	return s[:2] + "****" + s[len(s)-2:]
}
