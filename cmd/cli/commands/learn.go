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

// NewLearnCommand creates the learning command group
func NewLearnCommand() *cobra.Command {
	var languages []string

	cmd := &cobra.Command{
		Use:   "learn",
		Short: "Learn from codebases",
		Long:  `Submit codebases for analysis and extract skill patterns from real-world code.`,
	}

	// learn submit <project-path> [--languages]
	submitCmd := &cobra.Command{
		Use:   "submit <project-path>",
		Short: "Submit a project for learning",
		Long:  `Analyze a codebase to discover patterns, idioms, and skill evidence.`,
		Example: `  skill-system learn submit ./my-project
  skill-system learn submit ./my-project --languages go,typescript
  skill-system learn submit /path/to/repo --languages python`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLearnSubmit(cmd, args[0], languages)
		},
	}
	submitCmd.Flags().StringSliceVar(&languages, "languages", []string{}, "Comma-separated list of languages to analyze")

	// learn status <job-id>
	statusCmd := &cobra.Command{
		Use:     "status <job-id>",
		Short:   "Check learning job status",
		Long:    `Get the current status and progress of a learning job.`,
		Example: `  skill-system learn status 550e8400-e29b-41d4-a716-446655440000`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLearnStatus(cmd, args[0])
		},
	}

	// learn evidences <skill-name>
	evidenceCmd := &cobra.Command{
		Use:     "evidences <skill-name>",
		Short:   "Show evidences for a skill",
		Long:    `Display real-world code evidences that support a specific skill.`,
		Example: `  skill-system learn evidences go-concurrency`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLearnEvidences(cmd, args[0])
		},
	}

	cmd.AddCommand(submitCmd, statusCmd, evidenceCmd)
	return cmd
}

func runLearnSubmit(cmd *cobra.Command, projectPath string, languages []string) error {
	client := getAPIClient(cmd)
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()

	reqBodyMap := map[string]interface{}{
		"project_path": projectPath,
		"languages":    languages,
	}

	reqBodyBytes, err := json.Marshal(reqBodyMap)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	resp, err := client.Request(ctx, http.MethodPost, "/api/v1/learn", strings.NewReader(string(reqBodyBytes)))
	if err != nil {
		return fmt.Errorf("submit learning job: %w", err)
	}
	defer resp.Body.Close()

	var job models.LearningJob
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	fmt.Printf("Learning job submitted\n")
	fmt.Printf("  Job ID:   %s\n", job.ID)
	fmt.Printf("  Project:  %s\n", job.ProjectPath)
	fmt.Printf("  Status:   %s\n", job.Status)
	if len(job.Languages) > 0 {
		fmt.Printf("  Languages: %s\n", strings.Join(job.Languages, ", "))
	}
	fmt.Printf("\nRun 'skill-system learn status %s' to check progress.\n", job.ID)
	return nil
}

func runLearnStatus(cmd *cobra.Command, jobID string) error {
	client := getAPIClient(cmd)
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()

	resp, err := client.Request(ctx, http.MethodGet, "/api/v1/learn/"+jobID, nil)
	if err != nil {
		return fmt.Errorf("get learning status: %w", err)
	}
	defer resp.Body.Close()

	var job models.LearningJob
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
	case "pending", "queued":
		statusColor = colorYellow
	}

	fmt.Printf("Learning Job: %s\n", job.ID)
	fmt.Println(strings.Repeat("-", 40))
	fmt.Printf("Project:        %s\n", job.ProjectPath)
	fmt.Printf("Status:         %s%s%s\n", statusColor, job.Status, colorReset)
	fmt.Printf("Files Processed: %d\n", job.FilesProcessed)
	fmt.Printf("Patterns Found:  %d\n", job.PatternsFound)
	if len(job.Languages) > 0 {
		fmt.Printf("Languages:      %s\n", strings.Join(job.Languages, ", "))
	}
	fmt.Printf("Created:        %s\n", job.CreatedAt.Format(time.RFC3339))
	fmt.Printf("Updated:        %s\n", job.UpdatedAt.Format(time.RFC3339))
	return nil
}

func runLearnEvidences(cmd *cobra.Command, skillName string) error {
	client := getAPIClient(cmd)
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()

	resp, err := client.Request(ctx, http.MethodGet, "/api/v1/learn/evidences/"+skillName, nil)
	if err != nil {
		return fmt.Errorf("get evidences: %w", err)
	}
	defer resp.Body.Close()

	var evidences []models.Evidence
	if err := json.NewDecoder(resp.Body).Decode(&evidences); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	if len(evidences) == 0 {
		fmt.Printf("No evidences found for skill: %s\n", skillName)
		return nil
	}

	fmt.Printf("Evidences for %s%s%s (%d)\n", colorGreen, skillName, colorReset, len(evidences))
	fmt.Println(strings.Repeat("=", 70))

	for i, ev := range evidences {
		fmt.Printf("\n%d. %s\n", i+1, ev.Pattern)
		fmt.Printf("   Project: %s\n", ev.SourceProject)
		fmt.Printf("   File:    %s\n", ev.SourceFile)
		fmt.Printf("   Language: %s\n", ev.Language)
		fmt.Printf("   Validated: %v\n", ev.Validated)
		if ev.CodeSnippet != "" {
			fmt.Printf("   Snippet:\n")
			lines := strings.Split(ev.CodeSnippet, "\n")
			for _, line := range lines {
				fmt.Printf("      %s\n", line)
			}
		}
	}

	return nil
}
