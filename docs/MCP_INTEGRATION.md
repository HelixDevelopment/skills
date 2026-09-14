# MCP Integration Guide

## Table of Contents

- [What is MCP](#what-is-mcp)
- [Why MCP](#why-mcp)
- [Configuration](#configuration)
- [Available Tools](#available-tools)
- [Example Usage](#example-usage)

---

## What is MCP

The **Model Context Protocol (MCP)** is an open protocol that enables AI assistants to interact with external tools and data sources. It uses JSON-RPC 2.0 for communication and allows LLMs to:

- Discover available tools and their schemas
- Call tools with typed parameters
- Receive structured results

MCP replaces ad-hoc integrations with a standardized protocol that works across different AI assistants and tools.

## Why MCP

We chose MCP for the Skill Graph System because:

1. **Universal compatibility** - Works with Claude Code, OpenCode, Continue.dev, and more
2. **Type-safe interactions** - Schema-validated tool calls
3. **Discoverability** - AI assistants auto-discover available capabilities
4. **Composable** - Multiple MCP servers can be chained
5. **Local-first** - Runs locally, keeping your skill data private

---

## Configuration

### Claude Code (.mcp.json)

Create or edit `~/.mcp.json`:

```json
{
  "mcpServers": {
    "skill-system": {
      "command": "docker",
      "args": [
        "exec",
        "-i",
        "skill-api",
        "/app/skillctl",
        "mcp",
        "stdio"
      ],
      "env": {},
      "disabled": false,
      "autoApprove": []
    }
  }
}
```

Or using local binary:

```json
{
  "mcpServers": {
    "skill-system": {
      "command": "/opt/skill-system/bin/skillctl",
      "args": ["mcp", "stdio"],
      "env": {
        "SKILL_API_URL": "http://localhost:8080",
        "SKILL_API_KEY": "your-api-key"
      }
    }
  }
}
```

### OpenCode (opencode.json)

Add to your `~/.opencode/opencode.json`:

```json
{
  "mcpServers": {
    "skill-system": {
      "command": "docker",
      "args": ["exec", "-i", "skill-api", "/app/skillctl", "mcp", "stdio"],
      "env": {},
      "disabled": false
    }
  }
}
```

### Continue.dev (.continue/config.json)

Add to your Continue configuration:

```json
{
  "server": {
    "mcpServers": {
      "skill-system": {
        "command": "docker",
        "args": ["exec", "-i", "skill-api", "/app/skillctl", "mcp", "stdio"],
        "env": {}
      }
    }
  }
}
```

### VS Code with Cline

Add to Cline settings:

```json
{
  "mcpServers": {
    "skill-system": {
      "command": "docker",
      "args": ["exec", "-i", "skill-api", "/app/skillctl", "mcp", "stdio"]
    }
  }
}
```

### Environment Variables

The MCP server supports these environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `SKILL_API_URL` | `http://localhost:8080` | API server URL |
| `SKILL_API_KEY` | - | API key for authentication |
| `MCP_LOG_LEVEL` | `info` | Logging level |

---

## Available Tools

### search_skills

Semantic search across all skills.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `query` | string | Yes | Search query |
| `limit` | integer | No | Max results (default: 10) |
| `category` | string | No | Filter by category |
| `threshold` | float | No | Min similarity (default: 0.5) |

**Example:**

```json
{
  "query": "async programming patterns",
  "limit": 5,
  "category": "backend"
}
```

**Returns:**

```json
{
  "results": [
    {
      "id": "550e8400-e29b-41d4-a716-446655440000",
      "name": "Go Concurrency",
      "description": "Goroutines, channels, and select",
      "similarity_score": 0.92,
      "category": "backend",
      "status": "validated"
    }
  ],
  "total": 3
}
```

### get_skill

Get detailed information about a skill.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `skill_id` | string | Yes | UUID of the skill |
| `include_evidence` | boolean | No | Include evidence data |
| `include_relationships` | boolean | No | Include graph relationships |

**Example:**

```json
{
  "skill_id": "550e8400-e29b-41d4-a716-446655440000",
  "include_evidence": true,
  "include_relationships": true
}
```

**Returns:**

```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "name": "Go",
  "description": "Go programming language",
  "category": "backend",
  "status": "validated",
  "confidence": 0.95,
  "evidence": [
    {
      "type": "code_file",
      "source_url": "https://github.com/org/repo/blob/main/main.go",
      "strength": 0.8
    }
  ],
  "relationships": {
    "requires": [{"id": "...", "name": "Programming Fundamentals"}],
    "enhances": [{"id": "...", "name": "Go Concurrency"}],
    "related_to": [{"id": "...", "name": "Rust"}]
  }
}
```

### add_evidence

Add evidence to a skill.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `skill_id` | string | Yes | Target skill UUID |
| `evidence_type` | string | Yes | `git_commit`, `code_file`, `pr`, `documentation`, `manual` |
| `source_url` | string | Yes | URL or path to evidence |
| `description` | string | No | Description of the evidence |
| `strength` | float | No | Evidence strength (0.0-1.0) |

**Example:**

```json
{
  "skill_id": "550e8400-e29b-41d4-a716-446655440000",
  "evidence_type": "git_commit",
  "source_url": "https://github.com/org/repo/commit/abc123",
  "description": "Implemented worker pool pattern",
  "strength": 0.85
}
```

### validate_skill

Trigger validation for a skill.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `skill_id` | string | Yes | Skill UUID |

**Returns:**

```json
{
  "skill_id": "550e8400-e29b-41d4-a716-446655440000",
  "score": 0.87,
  "status": "validated",
  "evidence_count": 5,
  "details": {
    "code_evidence": 0.9,
    "git_evidence": 0.8,
    "doc_evidence": 0.1
  }
}
```

### get_learning_path

Generate a learning path between skills.

**Parameters:**

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `from_skill` | string | Yes | Starting skill name or ID |
| `to_skill` | string | Yes | Target skill name or ID |

**Example:**

```json
{
  "from_skill": "Go",
  "to_skill": "Kubernetes"
}
```

**Returns:**

```json
{
  "from": "Go",
  "to": "Kubernetes",
  "path": [
    {"step": 1, "name": "Go", "id": "..."},
    {"step": 2, "name": "Docker", "id": "..."},
    {"step": 3, "name": "Kubernetes", "id": "..."}
  ],
  "estimated_hours": 120
}
```

### list_categories

List all skill categories.

**Parameters:** None

**Returns:**

```json
{
  "categories": [
    {"name": "backend", "skill_count": 15},
    {"name": "frontend", "skill_count": 12},
    {"name": "devops", "skill_count": 8},
    {"name": "systems", "skill_count": 5}
  ]
}
```

### get_stats

Get system statistics.

**Parameters:** None

**Returns:**

```json
{
  "total_skills": 47,
  "validated_skills": 35,
  "total_evidence": 128,
  "categories": 6,
  "relationships": 89,
  "validation_rate": 0.74
}
```

---

## Example Usage

### Session 1: Finding Skills

```
User: What async skills do I have?

Claude: I'll search for async-related skills in your graph. 
        [Calls: search_skills with query="async programming"]

Claude: Here are your async-related skills:
        - Go Concurrency (score: 0.92, validated)
        - Tokio Async Runtime (score: 0.85, validated)  
        - JavaScript Promises (score: 0.78, partial)
```

### Session 2: Adding Evidence

```
User: I just committed a goroutine pool implementation

Claude: I'll add that as evidence for your Go Concurrency skill.
        [Calls: add_evidence with skill_id for Go Concurrency,
         evidence_type="git_commit", source_url=commit_url]

Claude: Added! Your Go Concurrency skill now has 6 evidence items.
        Should I run validation to update the score?
```

### Session 3: Learning Path

```
User: How do I learn Kubernetes?

Claude: Let me find the learning path from your current skills.
        [Calls: get_learning_path from_skill="Go" to_skill="Kubernetes"]

Claude: Here's your learning path (estimated 120 hours):
        1. Go (current) -> 2. Docker (12h) -> 3. Kubernetes (108h)
        
        You already know Go well. Next steps:
        - Learn containerization with Docker
        - Study Kubernetes architecture
        - Practice with minikube deployments
```

### Session 4: Gap Analysis

```
User: What skills should I develop for distributed systems?

Claude: Searching your current skills and finding gaps...
        [Calls: search_skills with query="distributed systems"]
        [Calls: get_stats]

Claude: Based on your profile, here are recommended skills:
        - etcd/Raft consensus (new)
        - gRPC (you know REST, this extends it)
        - System design patterns (related to your architecture skills)
```
