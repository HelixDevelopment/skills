package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/spf13/cobra"
)

// NewSearchCommand creates the search command group
func NewSearchCommand() *cobra.Command {
	var limit int
	var outputFormat string

	cmd := &cobra.Command{
		Use:   "search",
		Short: "Search the skill graph",
		Long:  `Search for skills by keyword, semantic similarity, or code patterns.`,
	}

	// search <query> [--limit] [--format]
	queryCmd := &cobra.Command{
		Use:   "query <search-query>",
		Short: "Search skills by keyword",
		Long:  `Search for skills using keyword matching against names, titles, descriptions, and content.`,
		Example: `  skill-system search query "concurrency"
  skill-system search query "kubernetes deployment" --limit 20
  skill-system search query "go channels" --format json`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := strings.Join(args, " ")
			return runSearchQuery(cmd, query, limit, outputFormat)
		},
	}
	queryCmd.Flags().IntVar(&limit, "limit", 20, "Maximum number of results")
	queryCmd.Flags().StringVar(&outputFormat, "format", "", "Output format: table|json (overrides global --format)")

	// search similar --file code.java
	var similarFile string
	similarCmd := &cobra.Command{
		Use:   "similar",
		Short: "Find semantically similar skills",
		Long:  `Find skills that are semantically similar to a given code file or text snippet using vector embeddings.`,
		Example: `  skill-system search similar --file main.go
  skill-system search similar --file README.md --limit 10`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if similarFile == "" {
				return fmt.Errorf("--file flag is required")
			}
			return runSearchSimilar(cmd, similarFile, limit, outputFormat)
		},
	}
	similarCmd.Flags().StringVarP(&similarFile, "file", "f", "", "Path to code file to analyze (required)")
	similarCmd.Flags().IntVar(&limit, "limit", 20, "Maximum number of results")
	similarCmd.Flags().StringVar(&outputFormat, "format", "", "Output format: table|json")
	similarCmd.MarkFlagRequired("file")

	cmd.AddCommand(queryCmd, similarCmd)
	return cmd
}

func runSearchQuery(cmd *cobra.Command, query string, limit int, outputFormat string) error {
	client := getAPIClient(cmd)
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()

	path := fmt.Sprintf("/api/v1/search?q=%s&limit=%d", urlEncode(query), limit)
	resp, err := client.Request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return fmt.Errorf("search: %w", err)
	}
	defer resp.Body.Close()

	var results []models.SearchResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	if len(results) == 0 {
		fmt.Println("No results found.")
		return nil
	}

	// Check if JSON output is requested
	of := outputFormat
	if of == "" {
		of = getGlobalFormat(cmd)
	}
	if of == "json" {
		return client.Output(results)
	}

	// Print formatted table with relevance scores
	fmt.Printf("%-30s %-25s %-10s %s\n", "NAME", "TITLE", "SCORE", "STATUS")
	fmt.Println(strings.Repeat("-", 95))
	for _, r := range results {
		scoreStr := fmt.Sprintf("%.3f", r.Score)
		fmt.Printf("%-30s %-25s %-10s %s\n", r.Skill.Name, r.Skill.Title, scoreStr, r.Skill.Status)
	}
	fmt.Printf("\nFound %d results for query: %q\n", len(results), query)
	return nil
}

func runSearchSimilar(cmd *cobra.Command, filename string, limit int, outputFormat string) error {
	client := getAPIClient(cmd)
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()

	// Read the file content
	content, err := os.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	// Build the request body
	reqBody := fmt.Sprintf(`{"content":%q,"language":%q,"limit":%d}`,
		string(content), detectLanguage(filename), limit)

	resp, err := client.Request(ctx, http.MethodPost, "/api/v1/search/similar", strings.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("similarity search: %w", err)
	}
	defer resp.Body.Close()

	var results []models.SearchResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	if len(results) == 0 {
		fmt.Println("No similar skills found.")
		return nil
	}

	// Check output format
	of := outputFormat
	if of == "" {
		of = getGlobalFormat(cmd)
	}
	if of == "json" {
		return client.Output(results)
	}

	// Print formatted table
	fmt.Printf("Skills similar to %s:\n\n", filename)
	fmt.Printf("%-30s %-25s %-10s %s\n", "NAME", "TITLE", "SCORE", "STATUS")
	fmt.Println(strings.Repeat("-", 95))
	for _, r := range results {
		scoreStr := fmt.Sprintf("%.3f", r.Score)
		fmt.Printf("%-30s %-25s %-10s %s\n", r.Skill.Name, r.Skill.Title, scoreStr, r.Skill.Status)
	}
	fmt.Printf("\nFound %d similar skills\n", len(results))
	return nil
}

// detectLanguage guesses the programming language from filename extension
func detectLanguage(filename string) string {
	ext := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(ext, ".go"):
		return "go"
	case strings.HasSuffix(ext, ".py"):
		return "python"
	case strings.HasSuffix(ext, ".java"):
		return "java"
	case strings.HasSuffix(ext, ".js") || strings.HasSuffix(ext, ".ts"):
		return "javascript"
	case strings.HasSuffix(ext, ".rs"):
		return "rust"
	case strings.HasSuffix(ext, ".c") || strings.HasSuffix(ext, ".h"):
		return "c"
	case strings.HasSuffix(ext, ".cpp") || strings.HasSuffix(ext, ".hpp"):
		return "cpp"
	case strings.HasSuffix(ext, ".rb"):
		return "ruby"
	default:
		return "unknown"
	}
}

func urlEncode(s string) string {
	return strings.ReplaceAll(s, " ", "+")
}
