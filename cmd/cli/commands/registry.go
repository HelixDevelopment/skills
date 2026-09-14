package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// NewRegistryCommand creates the registry management command group.
// G63 fix: rewired all endpoints to use the live server's canonical routes
// (/api/v1/skills, /api/v1/coverage, /api/v1/missing) instead of the
// non-existent /api/v1/registry/* routes.
func NewRegistryCommand() *cobra.Command {
	var domain string

	cmd := &cobra.Command{
		Use:   "registry",
		Short: "Skill registry management",
		Long:  `Monitor registry health, review skill completeness, and check coverage.`,
	}

	// registry status — aggregates coverage + skills list
	statusCmd := &cobra.Command{
		Use:     "status",
		Short:   "Show registry health status",
		Long:    `Display overall registry health including skill counts, coverage, and issues.`,
		Example: `  skill-system registry status`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRegistryStatus(cmd)
		},
	}

	// registry missing — uses /api/v1/missing
	missingCmd := &cobra.Command{
		Use:     "missing",
		Short:   "List missing dependencies",
		Long:    `Show all skills that have unresolved or missing dependencies.`,
		Example: `  skill-system registry missing`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRegistryMissing(cmd)
		},
	}

	// registry stale — uses /api/v1/skills filtered
	staleCmd := &cobra.Command{
		Use:     "stale",
		Short:   "List stale skills",
		Long:    `Show skills that haven't been reviewed recently and may need attention.`,
		Example: `  skill-system registry stale`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRegistryStale(cmd)
		},
	}

	// registry review <name> — uses /api/v1/skills/:name
	reviewCmd := &cobra.Command{
		Use:     "review <name>",
		Short:   "Review a skill's registry entry",
		Long:    `Show detailed registry information for a specific skill including coverage and issues.`,
		Example: `  skill-system registry review go-concurrency`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRegistryReview(cmd, args[0])
		},
	}

	// registry coverage [--domain] — uses /api/v1/coverage
	coverageCmd := &cobra.Command{
		Use:   "coverage",
		Short: "Show coverage statistics",
		Long:  `Display coverage metrics for skills, optionally filtered by domain.`,
		Example: `  skill-system registry coverage
  skill-system registry coverage --domain backend`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRegistryCoverage(cmd, domain)
		},
	}
	coverageCmd.Flags().StringVar(&domain, "domain", "", "Filter by domain")

	cmd.AddCommand(statusCmd, missingCmd, staleCmd, reviewCmd, coverageCmd)
	return cmd
}

// runRegistryStatus aggregates data from /api/v1/coverage + /api/v1/skills
// to build a registry health overview.
func runRegistryStatus(cmd *cobra.Command) error {
	client := getAPIClient(cmd)
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()

	// Get coverage data from the live /api/v1/coverage endpoint
	covResp, err := client.Request(ctx, http.MethodGet, "/api/v1/coverage", nil)
	if err != nil {
		return fmt.Errorf("get coverage: %w", err)
	}
	defer covResp.Body.Close()

	var coverage struct {
		TotalSkills     int     `json:"total_skills"`
		AverageCoverage float64 `json:"average_coverage"`
		ByDomain        map[string]struct {
			Count    int     `json:"count"`
			Coverage float64 `json:"coverage"`
		} `json:"by_domain"`
	}
	if err := json.NewDecoder(covResp.Body).Decode(&coverage); err != nil {
		return fmt.Errorf("decode coverage: %w", err)
	}

	// Get missing deps from /api/v1/missing
	missResp, err := client.Request(ctx, http.MethodGet, "/api/v1/missing", nil)
	if err != nil {
		// Non-fatal — missing endpoint may not exist yet
		coverage.TotalSkills = 0
	} else {
		defer missResp.Body.Close()
		var missing struct {
			Count int `json:"count"`
		}
		json.NewDecoder(missResp.Body).Decode(&missing)
		_ = missing
	}

	// Print formatted status
	fmt.Println("Registry Health Status")
	fmt.Println(strings.Repeat("=", 50))
	fmt.Printf("Total Skills:   %d\n", coverage.TotalSkills)
	fmt.Printf("Avg Coverage:   %.1f%%\n", coverage.AverageCoverage*100)

	if len(coverage.ByDomain) > 0 {
		fmt.Printf("\nSkills by Domain:\n")
		for d, v := range coverage.ByDomain {
			fmt.Printf("  %-20s %d skills (%.1f%% coverage)\n", d, v.Count, v.Coverage*100)
		}
	}

	return nil
}

// runRegistryMissing uses /api/v1/missing to list skills with missing deps.
func runRegistryMissing(cmd *cobra.Command) error {
	client := getAPIClient(cmd)
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()

	resp, err := client.Request(ctx, http.MethodGet, "/api/v1/missing", nil)
	if err != nil {
		return fmt.Errorf("get missing: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		MissingSkills []struct {
			SkillName   string   `json:"skill_name"`
			MissingDeps []string `json:"missing_deps"`
		} `json:"missing_skills"`
		Count int `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode missing: %w", err)
	}

	if result.Count == 0 {
		fmt.Println("No missing dependencies found.")
		return nil
	}

	fmt.Printf("Missing Dependencies (%d skills affected)\n", result.Count)
	fmt.Println(strings.Repeat("=", 50))
	for _, s := range result.MissingSkills {
		fmt.Printf("  %s: %s\n", s.SkillName, strings.Join(s.MissingDeps, ", "))
	}

	return nil
}

// runRegistryStale lists skills that may need attention.
// Since the live server doesn't have a dedicated stale endpoint,
// this queries skills and filters client-side.
func runRegistryStale(cmd *cobra.Command) error {
	client := getAPIClient(cmd)
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()

	// Get all skills and filter for stale ones
	resp, err := client.Request(ctx, http.MethodGet, "/api/v1/skills?limit=1000", nil)
	if err != nil {
		return fmt.Errorf("get skills: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Skills []struct {
			Name      string `json:"name"`
			Status    string `json:"status"`
			UpdatedAt string `json:"updated_at"`
		} `json:"skills"`
		Count int `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode skills: %w", err)
	}

	// Filter for deprecated or draft skills (proxy for "stale")
	stale := 0
	for _, s := range result.Skills {
		if s.Status == "deprecated" || s.Status == "draft" {
			fmt.Printf("  %-30s [%s] last updated: %s\n", s.Name, s.Status, s.UpdatedAt)
			stale++
		}
	}

	if stale == 0 {
		fmt.Println("No stale skills found.")
	} else {
		fmt.Printf("\n%d potentially stale skills found.\n", stale)
	}

	return nil
}

// runRegistryReview uses /api/v1/skills/:name to show skill details.
func runRegistryReview(cmd *cobra.Command, name string) error {
	client := getAPIClient(cmd)
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()

	resp, err := client.Request(ctx, http.MethodGet, "/api/v1/skills/"+name, nil)
	if err != nil {
		return fmt.Errorf("get skill %q: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("skill %q not found", name)
	}

	var skill struct {
		Name        string `json:"name"`
		Title       string `json:"title"`
		Status      string `json:"status"`
		Kind        string `json:"kind"`
		Version     string `json:"version"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&skill); err != nil {
		return fmt.Errorf("decode skill: %w", err)
	}

	fmt.Printf("Skill: %s\n", skill.Name)
	fmt.Println(strings.Repeat("=", 50))
	fmt.Printf("Title:       %s\n", skill.Title)
	fmt.Printf("Status:      %s\n", skill.Status)
	fmt.Printf("Kind:        %s\n", skill.Kind)
	fmt.Printf("Version:     %s\n", skill.Version)
	if skill.Description != "" {
		fmt.Printf("Description: %s\n", skill.Description)
	}

	return nil
}

// runRegistryCoverage uses /api/v1/coverage to show coverage metrics.
func runRegistryCoverage(cmd *cobra.Command, domain string) error {
	client := getAPIClient(cmd)
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()

	url := "/api/v1/coverage"
	if domain != "" {
		url += "?domain=" + domain
	}

	resp, err := client.Request(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("get coverage: %w", err)
	}
	defer resp.Body.Close()

	var report struct {
		TotalSkills     int     `json:"total_skills"`
		AverageCoverage float64 `json:"average_coverage"`
		ByDomain        map[string]struct {
			Count    int     `json:"count"`
			Coverage float64 `json:"coverage"`
		} `json:"by_domain"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		return fmt.Errorf("decode coverage: %w", err)
	}

	fmt.Println("Coverage Report")
	fmt.Println(strings.Repeat("=", 50))
	fmt.Printf("Total Skills:   %d\n", report.TotalSkills)
	fmt.Printf("Avg Coverage:   %.1f%%\n", report.AverageCoverage*100)

	if len(report.ByDomain) > 0 {
		fmt.Printf("\nBy Domain:\n")
		for d, v := range report.ByDomain {
			fmt.Printf("  %-20s %d skills (%.1f%%)\n", d, v.Count, v.Coverage*100)
		}
	}

	return nil
}
