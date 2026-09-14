package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/spf13/cobra"
)

// NewExpandCommand creates the expansion command group
func NewExpandCommand() *cobra.Command {
	var depth int
	var domain string

	cmd := &cobra.Command{
		Use:   "expand",
		Short: "Skill graph expansion",
		Long:  `Trigger and monitor automatic skill graph expansions to fill knowledge gaps.`,
	}

	// expand trigger <name> [--depth]
	triggerCmd := &cobra.Command{
		Use:   "trigger <name>",
		Short: "Trigger expansion for a skill",
		Long:  `Start an expansion job that analyzes a skill and generates missing sub-skills or dependencies.`,
		Example: `  skill-system expand trigger go-concurrency
  skill-system expand trigger kubernetes --depth 3`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExpandTrigger(cmd, args[0], depth)
		},
	}
	triggerCmd.Flags().IntVar(&depth, "depth", 2, "Maximum expansion depth")

	// expand status <job-id>
	statusCmd := &cobra.Command{
		Use:     "status <job-id>",
		Short:   "Check expansion job status",
		Long:    `Get the current status and results of an expansion job.`,
		Example: `  skill-system expand status 550e8400-e29b-41d4-a716-446655440000`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExpandStatus(cmd, args[0])
		},
	}

	// expand gaps [--domain]
	gapsCmd := &cobra.Command{
		Use:   "gaps",
		Short: "List knowledge gaps",
		Long:  `Identify areas in the skill graph that need expansion, optionally filtered by domain.`,
		Example: `  skill-system expand gaps
  skill-system expand gaps --domain backend`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExpandGaps(cmd, domain)
		},
	}
	gapsCmd.Flags().StringVar(&domain, "domain", "", "Filter by domain")

	cmd.AddCommand(triggerCmd, statusCmd, gapsCmd)
	return cmd
}

func runExpandTrigger(cmd *cobra.Command, name string, depth int) error {
	client := getAPIClient(cmd)
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()

	reqBody := fmt.Sprintf(`{"skill_name":%q,"depth":%d}`, name, depth)
	resp, err := client.Request(ctx, http.MethodPost, "/api/v1/expand", strings.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("trigger expansion: %w", err)
	}
	defer resp.Body.Close()

	var job models.ExpansionJob
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	fmt.Printf("Expansion job started\n")
	fmt.Printf("  Job ID:    %s\n", job.ID)
	fmt.Printf("  Skill:     %s\n", job.SkillName)
	fmt.Printf("  Depth:     %d\n", job.Depth)
	fmt.Printf("  Status:    %s\n", job.Status)
	fmt.Printf("\nRun 'skill-system expand status %s' to check progress.\n", job.ID)
	return nil
}

func runExpandStatus(cmd *cobra.Command, jobID string) error {
	client := getAPIClient(cmd)
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()

	resp, err := client.Request(ctx, http.MethodGet, "/api/v1/expand/"+jobID, nil)
	if err != nil {
		return fmt.Errorf("get expansion status: %w", err)
	}
	defer resp.Body.Close()

	var job models.ExpansionJob
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	statusColor := colorReset
	switch job.Status {
	case "completed":
		statusColor = colorGreen
	case "failed":
		statusColor = colorRed
	case "running":
		statusColor = colorBlue
	case "pending":
		statusColor = colorYellow
	}

	fmt.Printf("Expansion Job: %s\n", job.ID)
	fmt.Println(strings.Repeat("-", 40))
	fmt.Printf("Skill:     %s\n", job.SkillName)
	fmt.Printf("Depth:     %d\n", job.Depth)
	fmt.Printf("Status:    %s%s%s\n", statusColor, job.Status, colorReset)
	fmt.Printf("Created:   %s\n", job.CreatedAt.Format(time.RFC3339))
	fmt.Printf("Updated:   %s\n", job.UpdatedAt.Format(time.RFC3339))
	return nil
}

func runExpandGaps(cmd *cobra.Command, domain string) error {
	client := getAPIClient(cmd)
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()

	query := "/api/v1/expand/gaps"
	if domain != "" {
		query += "?domain=" + domain
	}

	resp, err := client.Request(ctx, http.MethodGet, query, nil)
	if err != nil {
		return fmt.Errorf("get gaps: %w", err)
	}
	defer resp.Body.Close()

	var gaps []struct {
		SkillName   string   `json:"skill_name"`
		MissingDeps []string `json:"missing_deps"`
		Suggested   []string `json:"suggested_skills"`
		Reason      string   `json:"reason"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&gaps); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	if len(gaps) == 0 {
		fmt.Println("No knowledge gaps found. The skill graph looks complete!")
		return nil
	}

	fmt.Printf("Knowledge Gaps (%d)\n", len(gaps))
	if domain != "" {
		fmt.Printf("Domain: %s\n", domain)
	}
	fmt.Println(strings.Repeat("=", 60))

	for i, gap := range gaps {
		fmt.Printf("\n%d. %s%s%s\n", i+1, colorYellow, gap.SkillName, colorReset)
		if gap.Reason != "" {
			fmt.Printf("   Reason: %s\n", gap.Reason)
		}
		if len(gap.MissingDeps) > 0 {
			fmt.Printf("   Missing Dependencies:\n")
			for _, dep := range gap.MissingDeps {
				fmt.Printf("     - %s\n", dep)
			}
		}
		if len(gap.Suggested) > 0 {
			fmt.Printf("   Suggested Skills:\n")
			for _, s := range gap.Suggested {
				fmt.Printf("     - %s\n", s)
			}
		}
	}

	fmt.Printf("\nRun 'skill-system expand trigger <skill-name>' to address a gap.\n")
	return nil
}
