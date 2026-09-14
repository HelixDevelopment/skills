package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/spf13/cobra"
)

// SkillCommand creates the skill management command group
func NewSkillCommand() *cobra.Command {
	var statusFilter, domainFilter, tagFilter string
	var limit int

	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Manage skills in the graph",
		Long:  `Create, read, update, delete, list, import, and export skills.`,
	}

	// skill list
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all skills",
		Long:  `List skills with optional filtering by status, domain, or tag.`,
		Example: `  skill-system skill list
  skill-system skill list --status active --domain backend
  skill-system skill list --tag kubernetes --limit 50`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSkillList(cmd, statusFilter, domainFilter, tagFilter, limit)
		},
	}
	listCmd.Flags().StringVar(&statusFilter, "status", "", "Filter by status: draft|validated|active|deprecated")
	listCmd.Flags().StringVar(&domainFilter, "domain", "", "Filter by domain")
	listCmd.Flags().StringVar(&tagFilter, "tag", "", "Filter by tag")
	listCmd.Flags().IntVar(&limit, "limit", 100, "Maximum number of results")

	// skill get <name>
	var recursive bool
	getCmd := &cobra.Command{
		Use:   "get <name>",
		Short: "Get a skill by name",
		Long:  `Retrieve detailed information about a specific skill.`,
		Example: `  skill-system skill get go-concurrency
  skill-system skill get go-concurrency --recursive`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSkillGet(cmd, args[0], recursive)
		},
	}
	getCmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "Include dependencies recursively")

	// skill create --file skill.toml
	var createFile string
	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new skill",
		Long:  `Create a skill from a TOML or JSON definition file.`,
		Example: `  skill-system skill create --file skill.toml
  skill-system skill create --file skill.json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if createFile == "" {
				return fmt.Errorf("--file flag is required")
			}
			return runSkillCreate(cmd, createFile)
		},
	}
	createCmd.Flags().StringVarP(&createFile, "file", "f", "", "Path to skill definition file (required)")
	createCmd.MarkFlagRequired("file")

	// skill update <name> --file skill.toml
	var updateFile string
	updateCmd := &cobra.Command{
		Use:     "update <name>",
		Short:   "Update an existing skill",
		Long:    `Update a skill's properties from a definition file.`,
		Example: `  skill-system skill update go-concurrency --file skill.toml`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if updateFile == "" {
				return fmt.Errorf("--file flag is required")
			}
			return runSkillUpdate(cmd, args[0], updateFile)
		},
	}
	updateCmd.Flags().StringVarP(&updateFile, "file", "f", "", "Path to skill definition file (required)")
	updateCmd.MarkFlagRequired("file")

	// skill delete <name>
	deleteCmd := &cobra.Command{
		Use:     "delete <name>",
		Short:   "Delete a skill",
		Long:    `Delete a skill from the graph. This operation cannot be undone.`,
		Example: `  skill-system skill delete old-skill`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSkillDelete(cmd, args[0])
		},
	}

	// skill import --file skills.toml
	var importFile string
	importCmd := &cobra.Command{
		Use:     "import",
		Short:   "Import skills from file",
		Long:    `Import multiple skills from a TOML or JSON file.`,
		Example: `  skill-system skill import --file skills.toml`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if importFile == "" {
				return fmt.Errorf("--file flag is required")
			}
			return runSkillImport(cmd, importFile)
		},
	}
	importCmd.Flags().StringVarP(&importFile, "file", "f", "", "Path to skills file (required)")
	importCmd.MarkFlagRequired("file")

	// skill export <name> --output file.toml
	var outputFile string
	exportCmd := &cobra.Command{
		Use:   "export <name>",
		Short: "Export a skill to file",
		Long:  `Export a skill definition to a TOML or JSON file.`,
		Example: `  skill-system skill export go-concurrency --output skill.toml
  skill-system skill export go-concurrency --output skill.json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSkillExport(cmd, args[0], outputFile)
		},
	}
	exportCmd.Flags().StringVarP(&outputFile, "output", "o", "", "Output file path (default: stdout)")

	// skill tree <name> [--depth]
	var treeDepth int
	treeCmd := &cobra.Command{
		Use:   "tree <name>",
		Short: "Show skill dependency tree",
		Long:  `Display the dependency tree for a skill, color-coded by relation type.`,
		Example: `  skill-system skill tree go-concurrency
  skill-system skill tree go-concurrency --depth 3`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSkillTree(cmd, args[0], treeDepth)
		},
	}
	treeCmd.Flags().IntVar(&treeDepth, "depth", 5, "Maximum tree depth")

	cmd.AddCommand(listCmd, getCmd, createCmd, updateCmd, deleteCmd, importCmd, exportCmd, treeCmd)
	return cmd
}

func runSkillList(cmd *cobra.Command, status, domain, tag string, limit int) error {
	client := getAPIClient(cmd)
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()

	// Build query parameters
	query := "/api/v1/skills?limit=" + strconv.Itoa(limit)
	if status != "" {
		query += "&status=" + status
	}
	if domain != "" {
		query += "&domain=" + domain
	}
	if tag != "" {
		query += "&tag=" + tag
	}

	resp, err := client.Request(ctx, http.MethodGet, query, nil)
	if err != nil {
		return fmt.Errorf("list skills: %w", err)
	}
	defer resp.Body.Close()

	var skills []models.Skill
	if err := json.NewDecoder(resp.Body).Decode(&skills); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	if len(skills) == 0 {
		fmt.Println("No skills found.")
		return nil
	}

	// Print as formatted table
	fmt.Printf("%-30s %-20s %-12s %-8s\n", "NAME", "TITLE", "STATUS", "VERSION")
	fmt.Println(strings.Repeat("-", 80))
	for _, s := range skills {
		fmt.Printf("%-30s %-20s %-12s %-8s\n", s.Name, s.Title, s.Status, s.Version)
	}
	fmt.Printf("\nTotal: %d skills\n", len(skills))
	return nil
}

func runSkillGet(cmd *cobra.Command, name string, recursive bool) error {
	client := getAPIClient(cmd)
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()

	query := "/api/v1/skills/" + name
	if recursive {
		query += "?recursive=true"
	}

	resp, err := client.Request(ctx, http.MethodGet, query, nil)
	if err != nil {
		return fmt.Errorf("get skill: %w", err)
	}
	defer resp.Body.Close()

	var skill models.Skill
	if err := json.NewDecoder(resp.Body).Decode(&skill); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	fmt.Printf("Name:        %s\n", skill.Name)
	fmt.Printf("Title:       %s\n", skill.Title)
	fmt.Printf("Version:     %s\n", skill.Version)
	fmt.Printf("Status:      %s\n", skill.Status)
	fmt.Printf("Description: %s\n", skill.Description)
	fmt.Printf("Created:     %s\n", skill.CreatedAt.Format(time.RFC3339))
	fmt.Printf("Updated:     %s\n", skill.UpdatedAt.Format(time.RFC3339))

	if len(skill.Dependencies) > 0 {
		fmt.Printf("\nDependencies (%d):\n", len(skill.Dependencies))
		for _, dep := range skill.Dependencies {
			relationColor := ""
			switch dep.RelationType {
			case models.DepTypeRequires:
				relationColor = "[required]"
			case models.DepTypeExtends:
				relationColor = "[extends]"
			case models.DepTypeRecommends:
				relationColor = "[recommends]"
			}
			fmt.Printf("  %s %s (%s)\n", relationColor, dep.DependsOnName, dep.RelationType)
		}
	}

	if len(skill.Resources) > 0 {
		fmt.Printf("\nResources (%d):\n", len(skill.Resources))
		for _, r := range skill.Resources {
			fmt.Printf("  [%s] %s - %s\n", r.ResourceType, r.Title, r.URL)
		}
	}

	return nil
}

func runSkillCreate(cmd *cobra.Command, filename string) error {
	client := getAPIClient(cmd)
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()

	data, err := os.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	var contentType string
	if strings.HasSuffix(filename, ".toml") {
		contentType = "application/toml"
	} else {
		contentType = "application/json"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, client.BaseURL+"/api/v1/skills", strings.NewReader(string(data)))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	SetAuthHeader(req, client.APIKey)

	resp, err := client.Client.Do(req)
	if err != nil {
		return fmt.Errorf("create skill: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create skill failed (%d): %s", resp.StatusCode, string(body))
	}

	var skill models.Skill
	if err := json.NewDecoder(resp.Body).Decode(&skill); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	fmt.Printf("Created skill: %s (%s)\n", skill.Name, skill.ID)
	return nil
}

func runSkillUpdate(cmd *cobra.Command, name, filename string) error {
	client := getAPIClient(cmd)
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()

	data, err := os.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	var contentType string
	if strings.HasSuffix(filename, ".toml") {
		contentType = "application/toml"
	} else {
		contentType = "application/json"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, client.BaseURL+"/api/v1/skills/"+name, strings.NewReader(string(data)))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	SetAuthHeader(req, client.APIKey)

	resp, err := client.Client.Do(req)
	if err != nil {
		return fmt.Errorf("update skill: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("update skill failed (%d): %s", resp.StatusCode, string(body))
	}

	fmt.Printf("Updated skill: %s\n", name)
	return nil
}

func runSkillDelete(cmd *cobra.Command, name string) error {
	client := getAPIClient(cmd)
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()

	resp, err := client.Request(ctx, http.MethodDelete, "/api/v1/skills/"+name, nil)
	if err != nil {
		return fmt.Errorf("delete skill: %w", err)
	}
	defer resp.Body.Close()

	fmt.Printf("Deleted skill: %s\n", name)
	return nil
}

func runSkillImport(cmd *cobra.Command, filename string) error {
	client := getAPIClient(cmd)
	ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
	defer cancel()

	data, err := os.ReadFile(filename)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, client.BaseURL+"/api/v1/skills/import", strings.NewReader(string(data)))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	if strings.HasSuffix(filename, ".toml") {
		req.Header.Set("Content-Type", "application/toml")
	} else {
		req.Header.Set("Content-Type", "application/json")
	}
	SetAuthHeader(req, client.APIKey)

	resp, err := client.Client.Do(req)
	if err != nil {
		return fmt.Errorf("import skills: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("import failed (%d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		Imported int      `json:"imported"`
		Errors   []string `json:"errors,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		// Gracefully handle non-JSON response
		fmt.Println("Skills imported successfully.")
		return nil
	}

	fmt.Printf("Imported %d skills.\n", result.Imported)
	if len(result.Errors) > 0 {
		fmt.Printf("Warnings (%d):\n", len(result.Errors))
		for _, e := range result.Errors {
			fmt.Printf("  - %s\n", e)
		}
	}
	return nil
}

func runSkillExport(cmd *cobra.Command, name, outputFile string) error {
	client := getAPIClient(cmd)
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()

	query := "/api/v1/skills/" + name + "/export"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, client.BaseURL+query, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	// Request TOML if output file ends in .toml
	if strings.HasSuffix(outputFile, ".toml") {
		req.Header.Set("Accept", "application/toml")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	SetAuthHeader(req, client.APIKey)

	resp, err := client.Client.Do(req)
	if err != nil {
		return fmt.Errorf("export skill: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("export failed (%d): %s", resp.StatusCode, string(body))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if outputFile != "" {
		if err := os.WriteFile(outputFile, data, 0644); err != nil {
			return fmt.Errorf("write file: %w", err)
		}
		fmt.Printf("Exported skill to: %s\n", outputFile)
	} else {
		fmt.Println(string(data))
	}
	return nil
}

// Terminal color codes are defined once in common.go (colorReset, colorRed,
// colorBlue, colorGreen, colorYellow, colorGray) and shared across this package.

func runSkillTree(cmd *cobra.Command, name string, depth int) error {
	client := getAPIClient(cmd)
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()

	query := fmt.Sprintf("/api/v1/skills/%s/tree?depth=%d", name, depth)
	resp, err := client.Request(ctx, http.MethodGet, query, nil)
	if err != nil {
		return fmt.Errorf("get skill tree: %w", err)
	}
	defer resp.Body.Close()

	var treeNode models.SkillTreeNode
	if err := json.NewDecoder(resp.Body).Decode(&treeNode); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	printTreeNode(&treeNode, "", true)
	return nil
}

func printTreeNode(node *models.SkillTreeNode, prefix string, isLast bool) {
	connector := "├──"
	if isLast {
		connector = "└──"
	}

	color := colorReset
	if node.Depth == 0 {
		color = colorYellow
	}

	fmt.Printf("%s%s %s%s%s (%s)\n", prefix, connector, color, node.Skill.Name, colorReset, node.Skill.Version)

	if len(node.Children) > 0 {
		newPrefix := prefix
		if isLast {
			newPrefix += "    "
		} else {
			newPrefix += "│   "
		}
		for i, child := range node.Children {
			isLastChild := i == len(node.Children)-1
			printTreeNode(&child, newPrefix, isLastChild)
		}
	}
}
