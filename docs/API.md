# API Documentation

## Table of Contents

- [Overview](#overview)
- [Base URL](#base-url)
- [Content Negotiation](#content-negotiation)
- [Authentication](#authentication)
- [Endpoints](#endpoints)
- [Request/Response Schemas](#requestresponse-schemas)
- [Error Codes](#error-codes)
- [Rate Limiting](#rate-limiting)

---

## Overview

The Skill Graph System exposes a REST API over HTTP/2 (TCP) and HTTP/3 (QUIC). All endpoints support content negotiation between JSON and TOML.

### API Version

Current version: `v1`

The version is included in the URL path: `/api/v1/...`

---

## Base URL

| Protocol | URL | Notes |
|----------|-----|-------|
| HTTP/2 | `http://localhost:8080` | Default, always available |
| HTTP/3 | `https://localhost:8443` | Requires `ENABLE_HTTP3=true` |

---

## Content Negotiation

The API supports both JSON and TOML for requests and responses.

### Request Format

```bash
# JSON (default)
curl -X POST http://localhost:8080/api/v1/skills \
  -H "Content-Type: application/json" \
  -d '{"name": "Go"}'

# TOML
curl -X POST http://localhost:8080/api/v1/skills \
  -H "Content-Type: application/toml" \
  -d 'name = "Go"'
```

### Response Format

```bash
# JSON (default)
curl http://localhost:8080/api/v1/skills
# Returns: [{"id": "...", "name": "Go", ...}]

# TOML
curl -H "Accept: application/toml" http://localhost:8080/api/v1/skills
# Returns: [[skills]]\nname = "Go"\n...
```

### Response Content Types

| Accept Header | Content-Type | Format |
|---------------|-------------|--------|
| `application/json` or omitted | `application/json` | JSON |
| `application/toml` | `application/toml` | TOML |
| `text/plain` | `text/plain` | Human-readable |

---

## Authentication

The `/api/v1` and `/mcp/v1` surfaces share one API-key scheme, governed by
`api_keys` and `auth_disabled` in `config/config.toml`. The key is read from the
**`X-API-Key`** header only — there is no JWT, OAuth, bearer token, or
`/auth/token` endpoint in the system.

### API Key Authentication

For programmatic access, send the key in the `X-API-Key` header:

```bash
curl -H "X-API-Key: your-api-key" http://localhost:8080/api/v1/skills
```

Configure the accepted keys via the `HELIX_API_KEYS` environment variable
(comma-separated) or the `api_keys` list in `config/config.toml`.

### Fail-closed by default

Authentication is **fail-closed**. With no `api_keys` configured and
`auth_disabled=false` (the default), every `/api/v1` and `/mcp/v1` request is
rejected with **`503 auth_not_configured`** until keys are configured — the API
is *never* silently served without authentication.

To run with no authentication (local development only), set `auth_disabled=true`
in `config/config.toml`; the server then logs a loud warning that `/api/v1` is
publicly accessible.

---

## Endpoints

### Health & Info

#### GET /health

Health check endpoint.

```bash
curl http://localhost:8080/health
```

**Response (200 OK):**

```json
{
  "status": "healthy",
  "version": "1.0.0",
  "commit": "abc1234",
  "database": "connected",
  "timestamp": "2024-01-15T10:30:00Z"
}
```

#### GET /api/v1/docs

API documentation redirect.

### Skills

#### GET /api/v1/skills

List all skills with pagination.

```bash
curl "http://localhost:8080/api/v1/skills?page=1&limit=20&category=backend&status=validated"
```

**Query Parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `page` | int | 1 | Page number |
| `limit` | int | 20 | Items per page (max 100) |
| `category` | string | - | Filter by category |
| `status` | string | - | Filter by status |
| `parent_id` | UUID | - | Filter by parent skill |
| `sort` | string | `name` | Sort field |
| `order` | string | `asc` | Sort order (`asc`, `desc`) |

**Response (200 OK):**

```json
{
  "skills": [
    {
      "id": "550e8400-e29b-41d4-a716-446655440000",
      "name": "Go",
      "description": "Go programming language",
      "category": "backend",
      "status": "validated",
      "confidence": 0.95,
      "created_at": "2024-01-01T00:00:00Z",
      "updated_at": "2024-01-10T00:00:00Z"
    }
  ],
  "pagination": {
    "page": 1,
    "limit": 20,
    "total": 47,
    "total_pages": 3
  }
}
```

#### POST /api/v1/skills

Create a new skill.

```bash
curl -X POST http://localhost:8080/api/v1/skills \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Rust Ownership",
    "description": "Memory ownership model in Rust",
    "category": "systems",
    "parent_skill_id": "550e8400-e29b-41d4-a716-446655440001"
  }'
```

**Request Body:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Unique skill name |
| `description` | string | No | Skill description |
| `category` | string | Yes | Category (e.g., `backend`, `frontend`, `systems`) |
| `parent_skill_id` | UUID | No | Parent skill reference |
| `metadata` | object | No | Additional key-value data |

**Response (201 Created):**

```json
{
  "id": "550e8400-e29b-41d4-a716-446655440002",
  "name": "Rust Ownership",
  "description": "Memory ownership model in Rust",
  "category": "systems",
  "status": "proposed",
  "confidence": 0.0,
  "created_at": "2024-01-15T10:30:00Z",
  "updated_at": "2024-01-15T10:30:00Z"
}
```

#### GET /api/v1/skills/:id

Get a specific skill by ID.

```bash
curl http://localhost:8080/api/v1/skills/550e8400-e29b-41d4-a716-446655440000
```

**Response (200 OK):**

```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "name": "Go",
  "description": "Go programming language",
  "category": "backend",
  "status": "validated",
  "confidence": 0.95,
  "parent_skill": {
    "id": "...",
    "name": "Programming Languages"
  },
  "relationships": {
    "requires": [
      {"id": "...", "name": "Programming Fundamentals"}
    ],
    "enhances": [
      {"id": "...", "name": "Go Concurrency"}
    ],
    "related_to": [
      {"id": "...", "name": "Rust"}
    ]
  },
  "evidence": [
    {
      "id": "...",
      "type": "code_file",
      "source_url": "https://github.com/...",
      "strength": 0.8
    }
  ],
  "created_at": "2024-01-01T00:00:00Z",
  "updated_at": "2024-01-10T00:00:00Z"
}
```

#### PUT /api/v1/skills/:id

Update a skill.

```bash
curl -X PUT http://localhost:8080/api/v1/skills/550e8400-e29b-41d4-a716-446655440000 \
  -H "Content-Type: application/json" \
  -d '{
    "description": "Updated description",
    "category": "backend"
  }'
```

**Response (200 OK):** Updated skill object

#### DELETE /api/v1/skills/:id

Delete a skill.

```bash
curl -X DELETE http://localhost:8080/api/v1/skills/550e8400-e29b-41d4-a716-446655440000
```

**Response (204 No Content)**

#### GET /api/v1/skills/search

Semantic search for skills.

```bash
curl "http://localhost:8080/api/v1/skills/search?q=distributed+systems&limit=10"
```

**Query Parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `q` | string | Required | Search query |
| `limit` | int | 10 | Max results |
| `threshold` | float | 0.5 | Minimum similarity score |
| `category` | string | - | Filter by category |

**Response (200 OK):**

```json
{
  "query": "distributed systems",
  "results": [
    {
      "id": "...",
      "name": "Go Concurrency",
      "description": "...",
      "similarity_score": 0.89,
      "category": "backend"
    },
    {
      "id": "...",
      "name": "Kubernetes",
      "description": "...",
      "similarity_score": 0.82,
      "category": "devops"
    }
  ],
  "total": 5
}
```

#### POST /api/v1/skills/:id/evidence

Add evidence to a skill.

```bash
curl -X POST http://localhost:8080/api/v1/skills/550e8400-e29b-41d4-a716-446655440000/evidence \
  -H "Content-Type: application/json" \
  -d '{
    "type": "git_commit",
    "source_url": "https://github.com/org/repo/commit/abc123",
    "description": "Implemented goroutine pool",
    "strength": 0.8
  }'
```

**Evidence Types:**

| Type | Description |
|------|-------------|
| `git_commit` | Git commit reference |
| `code_file` | Source code file |
| `pr` | Pull request |
| `documentation` | Documentation page |
| `manual` | Manual entry |

#### POST /api/v1/skills/:id/validate

Trigger validation for a skill.

```bash
curl -X POST http://localhost:8080/api/v1/skills/550e8400-e29b-41d4-a716-446655440000/validate
```

**Response (200 OK):**

```json
{
  "skill_id": "550e8400-e29b-41d4-a716-446655440000",
  "score": 0.85,
  "status": "validated",
  "evidence_count": 5,
  "checked_at": "2024-01-15T10:30:00Z"
}
```

### Relationships

#### POST /api/v1/skills/:id/relationships

Create a relationship between skills.

```bash
curl -X POST http://localhost:8080/api/v1/skills/550e8400-e29b-41d4-a716-446655440000/relationships \
  -H "Content-Type: application/json" \
  -d '{
    "target_skill_id": "550e8400-e29b-41d4-a716-446655440003",
    "relationship_type": "requires",
    "strength": 0.9
  }'
```

**Relationship Types:** `requires`, `enhances`, `related_to`

#### DELETE /api/v1/skills/:id/relationships/:relationship_id

Remove a relationship.

```bash
curl -X DELETE http://localhost:8080/api/v1/skills/550e8400-e29b-41d4-a716-446655440000/relationships/550e8400-e29b-41d4-a716-446655440010
```

### Graph

#### GET /api/v1/graph

Export the skill graph.

```bash
curl http://localhost:8080/api/v1/graph
```

**Response (200 OK):**

```json
{
  "nodes": [
    {"id": "...", "name": "Go", "category": "backend"},
    {"id": "...", "name": "Rust", "category": "systems"}
  ],
  "edges": [
    {"source": "...", "target": "...", "type": "related_to", "strength": 0.7}
  ],
  "metadata": {
    "node_count": 47,
    "edge_count": 89,
    "generated_at": "2024-01-15T10:30:00Z"
  }
}
```

#### GET /api/v1/graph/path

Find learning path between two skills.

```bash
curl "http://localhost:8080/api/v1/graph/path?from=go&to=kubernetes"
```

**Response (200 OK):**

```json
{
  "from": "Go",
  "to": "Kubernetes",
  "path": [
    {"id": "...", "name": "Go", "step": 1},
    {"id": "...", "name": "Docker", "step": 2},
    {"id": "...", "name": "Kubernetes", "step": 3}
  ],
  "path_length": 3
}
```

### Metrics

#### GET /metrics

Prometheus metrics endpoint.

```bash
curl http://localhost:8080/metrics
```

**Response:** Prometheus exposition format

### Backup

#### POST /api/v1/admin/backup

Trigger a backup.

```bash
curl -X POST http://localhost:8080/api/v1/admin/backup \
  -H "X-API-Key: your-api-key"
```

---

## Request/Response Schemas

### Skill Schema

```json
{
  "id": "UUID",
  "name": "string (required, unique)",
  "description": "string",
  "category": "string (required)",
  "status": "enum: proposed, validated, deprecated",
  "confidence": "float (0.0-1.0)",
  "parent_skill_id": "UUID or null",
  "embedding": "vector (768-dim)",
  "metadata": "object",
  "created_at": "ISO 8601 timestamp",
  "updated_at": "ISO 8601 timestamp"
}
```

### Evidence Schema

```json
{
  "id": "UUID",
  "skill_id": "UUID",
  "evidence_type": "enum: git_commit, code_file, pr, documentation, manual",
  "source_url": "string (URL)",
  "description": "string",
  "strength": "float (0.0-1.0)",
  "metadata": "object",
  "created_at": "ISO 8601 timestamp"
}
```

### Relationship Schema

```json
{
  "id": "UUID",
  "source_skill_id": "UUID",
  "target_skill_id": "UUID",
  "relationship_type": "enum: requires, enhances, related_to",
  "strength": "float (0.0-1.0)",
  "confidence": "float (0.0-1.0)",
  "created_at": "ISO 8601 timestamp"
}
```

### Validation Result Schema

```json
{
  "skill_id": "UUID",
  "score": "float (0.0-1.0)",
  "status": "enum: validated, partial, needs-evidence",
  "evidence_count": "integer",
  "details": "object",
  "checked_at": "ISO 8601 timestamp"
}
```

### Error Response Schema

```json
{
  "error": {
    "code": "ERROR_CODE",
    "message": "Human-readable error message",
    "details": "Additional context (optional)",
    "timestamp": "2024-01-15T10:30:00Z"
  }
}
```

---

## Error Codes

| Status | Code | Description |
|--------|------|-------------|
| 400 | `BAD_REQUEST` | Invalid request body or parameters |
| 400 | `VALIDATION_ERROR` | Schema validation failed |
| 401 | `UNAUTHORIZED` | Missing or invalid authentication |
| 403 | `FORBIDDEN` | Insufficient permissions |
| 404 | `NOT_FOUND` | Resource not found |
| 409 | `CONFLICT` | Resource already exists |
| 422 | `UNPROCESSABLE` | Business logic error |
| 429 | `RATE_LIMITED` | Too many requests |
| 500 | `INTERNAL_ERROR` | Server error |
| 503 | `SERVICE_UNAVAILABLE` | Dependency unavailable |

### Error Examples

```bash
# 404 Not Found
curl http://localhost:8080/api/v1/skills/non-existent-id
# Response:
# {"error": {"code": "NOT_FOUND", "message": "Skill not found", "timestamp": "..."}}

# 400 Bad Request
curl -X POST http://localhost:8080/api/v1/skills \
  -d '{"name": ""}'
# Response:
# {"error": {"code": "VALIDATION_ERROR", "message": "Name is required", "timestamp": "..."}}

# 409 Conflict
curl -X POST http://localhost:8080/api/v1/skills \
  -d '{"name": "Go", "category": "test"}'
# Response:
# {"error": {"code": "CONFLICT", "message": "Skill 'Go' already exists", "timestamp": "..."}}
```

---

## Rate Limiting

Rate limiting is applied per API key / IP address.

### Limits

| Scope | Limit | Window |
|-------|-------|--------|
| General API | 100 requests | 1 minute |
| Search | 30 requests | 1 minute |
| Create/Update | 20 requests | 1 minute |

### Headers

| Header | Description |
|--------|-------------|
| `X-RateLimit-Limit` | Maximum requests allowed |
| `X-RateLimit-Remaining` | Remaining requests in window |
| `X-RateLimit-Reset` | Unix timestamp when limit resets |

### Rate Limit Response

```json
{
  "error": {
    "code": "RATE_LIMITED",
    "message": "Rate limit exceeded. Retry after 45 seconds.",
    "retry_after": 45
  }
}
```
