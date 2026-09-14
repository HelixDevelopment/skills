// Package commands implements the Cobra CLI commands for the skill-system.
// This file provides the 'source' command group for managing skill sources
// (GitHub repos, filesystem paths, etc.) that supply SKILL.md files to the
// ingestion pipeline (G85).
package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// sourceCmd represents the 'source' command group.
var sourceCmd = &cobra.Command{
	Use:   "source",
	Short: "Manage skill sources",
	Long:  "Register, list, sync, and delete skill sources (GitHub repos, filesystem paths, URLs).",
}

// sourceRegisterCmd registers a new skill source.
var sourceRegisterCmd = &cobra.Command{
	Use:   "register",
	Short: "Register a new skill source",
	Long:  "Register a new skill source with a name, type, and configuration.",
	RunE:  runSourceRegister,
}

// sourceListCmd lists all registered sources.
var sourceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List registered skill sources",
	Long:  "List all registered skill sources with their status and last sync time.",
	RunE:  runSourceList,
}

// sourceSyncCmd triggers a sync for a source.
var sourceSyncCmd = &cobra.Command{
	Use:   "sync [source-id]",
	Short: "Sync a skill source",
	Long:  "Trigger a full rescan of a registered skill source's SKILL.md files.",
	Args:  cobra.ExactArgs(1),
	RunE:  runSourceSync,
}

// sourceDeleteCmd deletes a source.
var sourceDeleteCmd = &cobra.Command{
	Use:   "delete [source-id]",
	Short: "Delete a skill source",
	Long:  "Delete a registered skill source. Does not remove already-imported skills.",
	Args:  cobra.ExactArgs(1),
	RunE:  runSourceDelete,
}

var (
	sourceName string
	sourceType string
	sourceURL  string
)

func init() {
	sourceRegisterCmd.Flags().StringVar(&sourceName, "name", "", "Human-readable source name (required)")
	sourceRegisterCmd.Flags().StringVar(&sourceType, "type", "github", "Source type: github, filesystem, url")
	sourceRegisterCmd.Flags().StringVar(&sourceURL, "url", "", "Source URL or path (required)")
	_ = sourceRegisterCmd.MarkFlagRequired("name")
	_ = sourceRegisterCmd.MarkFlagRequired("url")

	sourceCmd.AddCommand(sourceRegisterCmd)
	sourceCmd.AddCommand(sourceListCmd)
	sourceCmd.AddCommand(sourceSyncCmd)
	sourceCmd.AddCommand(sourceDeleteCmd)
}

// RegisterSourceCmd adds the 'source' command group to the root command.
func RegisterSourceCmd(root *cobra.Command) {
	root.AddCommand(sourceCmd)
}

func runSourceRegister(cmd *cobra.Command, args []string) error {
	baseURL := getBaseURL()
	payload := map[string]interface{}{
		"name":        sourceName,
		"source_type": sourceType,
		"config":      map[string]interface{}{"url": sourceURL},
		"enabled":     true,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	resp, err := http.Post(baseURL+"/api/v1/sources", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	fmt.Fprintf(os.Stdout, "Source registered: %v\n", result["id"])
	return nil
}

func runSourceList(cmd *cobra.Command, args []string) error {
	baseURL := getBaseURL()
	resp, err := http.Get(baseURL + "/api/v1/sources")
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Sources []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			SourceType string `json:"source_type"`
			Enabled    bool   `json:"enabled"`
			SyncStatus string `json:"sync_status"`
		} `json:"sources"`
		Count int `json:"count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tTYPE\tENABLED\tSTATUS")
	for _, s := range result.Sources {
		fmt.Fprintf(w, "%s\t%s\t%s\t%v\t%s\n", s.ID, s.Name, s.SourceType, s.Enabled, s.SyncStatus)
	}
	w.Flush()
	fmt.Fprintf(os.Stdout, "\n%d sources total\n", result.Count)
	return nil
}

func runSourceSync(cmd *cobra.Command, args []string) error {
	baseURL := getBaseURL()
	sourceID := args[0]

	resp, err := http.Post(baseURL+"/api/v1/sources/"+sourceID+"/sync", "application/json", nil)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	fmt.Fprintf(os.Stdout, "Sync triggered for source %s\n", sourceID)
	return nil
}

func runSourceDelete(cmd *cobra.Command, args []string) error {
	baseURL := getBaseURL()
	sourceID := args[0]

	req, err := http.NewRequest(http.MethodDelete, baseURL+"/api/v1/sources/"+sourceID, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, string(respBody))
	}

	fmt.Fprintf(os.Stdout, "Source %s deleted\n", sourceID)
	return nil
}

// getBaseURL returns the API base URL from environment or default.
func getBaseURL() string {
	if url := os.Getenv("SKILL_SYSTEM_URL"); url != "" {
		return url
	}
	return "http://localhost:8080"
}
