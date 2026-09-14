package mcp

import "fmt"

// ============================================================================
// MCP Prompt Templates for Agent Interaction
// ============================================================================

// GetSystemPrompt returns the system prompt for agents using this MCP.
func GetSystemPrompt() string {
	return `You are an AI agent connected to the HelixKnowledge Skill Graph System via MCP (Model Context Protocol).

## Available Tools

You have access to 7 tools for managing and querying the skill graph:

1. **skill_search** - Search for skills using keywords or natural language.
   Use this FIRST when you need to find relevant knowledge.
   Parameters: query (required), limit (optional, default 5)

2. **skill_get** - Retrieve the full details of a specific skill by exact name.
   Use this after skill_search to get complete information.
   Parameters: name (required)

3. **skill_tree** - Get the dependency tree showing prerequisites and related skills.
   Use this to understand skill relationships and learning paths.
   Parameters: name (required), depth (optional, default 5)

4. **skill_create** - Create or update a skill in the knowledge graph.
   You can contribute new knowledge! Skills are defined in TOML format.
   Parameters: toml (required) - see skill-format prompt for format details

5. **learn_from_project** - Submit a codebase path for automated analysis.
   The system will scan source files and extract patterns as new skills.
   Parameters: project_path (required), languages (optional)

6. **missing_skills** - Find gaps in the knowledge graph.
   Use this to identify areas where the knowledge graph needs expansion.
   Parameters: domain (optional)

7. **get_coverage** - Get a coverage report for a domain.
   Shows statistics on skill completeness, dependencies, and evidence.
   Parameters: domain (optional)

## Best Practices

- Always search existing skills before creating new ones to avoid duplicates.
- Use skill_tree to understand the full context of a skill before using it.
- Create skills when you discover new patterns or knowledge worth preserving.
- Check missing_skills periodically to find areas needing attention.
- Use get_coverage to assess the health of a knowledge domain.

## Skill Graph Concepts

- **Skill**: A knowledge unit with name, title, description, content, and metadata.
- **Dependency**: A directed relationship (requires, extends, recommends) between skills.
- **Evidence**: Real-world code patterns extracted from projects that validate a skill.
- **Coverage**: A score (0.0-1.0) indicating how complete a skill's knowledge is.
- **Domain**: A category tag (e.g., backend, frontend, devops) for organizing skills.`
}

// GetSkillFormatPrompt returns a prompt explaining the TOML skill format.
func GetSkillFormatPrompt() string {
	return `## TOML Skill Format

When creating or updating skills with the skill_create tool, use this TOML format:

` + "```toml" + `
[skill]
name = "go-concurrency-patterns"
version = "0.1.0"
title = "Go Concurrency Patterns"
description = "Common concurrency patterns in Go including channels, goroutines, and synchronization primitives."
content = """
## Go Concurrency Patterns

### Fan-Out/Fan-In
Distribute work across multiple goroutines and collect results.

` + "```go" + `
func fanOut(numWorkers int, jobs <-chan Job) <-chan Result {
    results := make(chan Result)
    var wg sync.WaitGroup
    for i := 0; i < numWorkers; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for job := range jobs {
                results <- process(job)
            }
        }()
    }
    go func() { wg.Wait(); close(results) }()
    return results
}
` + "```" + `
"""

[skill.metadata]
tags = ["go", "concurrency", "patterns", "goroutines"]
domain = "backend"
complexity = "intermediate"

[skill.dependencies]
requires = ["go-channels", "go-goroutines"]
extends = ["go-error-handling"]
recommends = ["go-context-package"]

[[skill.resources]]
url = "https://go.dev/blog/pipelines"
title = "Go Concurrency Patterns: Pipelines"
resource_type = "official-doc"

[[skill.resources]]
url = "https://github.com/golang/go/wiki/ConcurrencyPatterns"
title = "Go Concurrency Patterns Wiki"
resource_type = "article"
` + "```" + `

### Field Reference

**[skill]** (required)
- name: Unique kebab-case identifier (e.g., "go-concurrency-patterns")
- version: Semantic version (default: "0.1.0")
- title: Human-readable title
- description: Short summary (1-2 sentences)
- content: Full markdown content with examples and explanations

**[skill.metadata]** (optional)
- tags: Array of string tags for categorization
- domain: High-level domain (e.g., "backend", "frontend", "devops", "data")
- complexity: "beginner", "intermediate", or "advanced"

**[skill.dependencies]** (optional)
- requires: Skills that must be learned first (hard dependency)
- extends: Skills that this one builds upon (soft dependency)
- recommends: Related skills that complement this one

**[[skill.resources]]** (optional, repeatable)
- url: Resource URL
- title: Human-readable title
- resource_type: "official-doc", "article", "code", "video", or "tutorial"

### Tips

- Use kebab-case for skill names (e.g., "rust-lifetimes", "docker-compose")
- Content should be comprehensive but concise - aim for 200-2000 words
- Include code examples in the content field when relevant
- Always define requires dependencies to build a proper learning path
- Use official documentation URLs as the first resource when available`
}

// GetAgentConfigClaudeCode returns MCP configuration for Claude Code.
func GetAgentConfigClaudeCode() map[string]interface{} {
	return map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"helix-knowledge": map[string]interface{}{
				"command": "go",
				"args":    []string{"run", "./cmd/server", "--mcp", "stdio"},
				"env": map[string]string{
					"HELIX_DB_HOST":   "localhost",
					"HELIX_LOG_LEVEL": "warn",
				},
			},
		},
	}
}

// GetAgentConfigOpenCode returns MCP configuration for OpenCode.
func GetAgentConfigOpenCode() map[string]interface{} {
	return map[string]interface{}{
		"mcp": map[string]interface{}{
			"servers": []map[string]interface{}{
				{
					"name":    "helix-knowledge",
					"command": "go run ./cmd/server --mcp stdio",
					"env": map[string]string{
						"HELIX_DB_HOST":   "localhost",
						"HELIX_LOG_LEVEL": "warn",
					},
				},
			},
		},
	}
}

// GetAgentConfigContinueDev returns MCP configuration for Continue.dev.
func GetAgentConfigContinueDev() map[string]interface{} {
	return map[string]interface{}{
		"servers": []map[string]interface{}{
			{
				"name":        "helix-knowledge",
				"command":     "go run ./cmd/server --mcp stdio",
				"description": "HelixKnowledge Skill Graph System for querying and managing skills",
				"env": map[string]string{
					"HELIX_DB_HOST":   "localhost",
					"HELIX_LOG_LEVEL": "warn",
				},
			},
		},
	}
}

// FormatAgentConfigs returns all agent configuration examples as a formatted string.
func FormatAgentConfigs() string {
	return fmt.Sprintf(`## Agent Configuration Examples

### Claude Code (.mcp.json)
Place in your project root or ~/.mcp.json:
`+"```json"+`
%s
`+"```"+`

### OpenCode (opencode.json)
Add to your opencode.json configuration:
`+"```json"+`
%s
`+"```"+`

### Continue.dev (.continue/mcpServers/helix.yaml)
Create .continue/mcpServers/helix.yaml in your project:
`+"```yaml"+`
servers:
  - name: helix-knowledge
    command: go run ./cmd/server --mcp stdio
    env:
      HELIX_DB_HOST: localhost
      HELIX_LOG_LEVEL: warn
`+"```"+`

For all configurations, ensure the Go binary is in PATH and the database is accessible.
`, "{}", "{}")
}
