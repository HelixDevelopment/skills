# Danger Zones

## Table of Contents

- [Overview](#overview)
- [TOML Performance](#toml-performance)
- [ACP Protocol Correction](#acp-protocol-correction)
- [HTTP/3 Complexity](#http3-complexity)
- [Embedding Dimension Trade-offs](#embedding-dimension-trade-offs)
- [Auto-Growth Bounding](#auto-growth-bounding)
- [Hallucination Prevention](#hallucination-prevention)
- [Performance at Scale](#performance-at-scale)
- [Mitigation Summary](#mitigation-summary)

---

## Overview

This document catalogs all identified gaps, weak spots, and potential failure modes in the Skill Graph System. Each section describes the risk, its potential impact, and the mitigations in place or planned.

**Risk Classification:**

| Level | Description |
|-------|-------------|
| **CRITICAL** | System failure or data loss risk |
| **HIGH** | Significant performance or reliability impact |
| **MEDIUM** | Manageable with monitoring |
| **LOW** | Minor inconvenience |

---

## TOML Performance

**Risk Level:** MEDIUM

### Problem

TOML parsing is significantly slower than JSON parsing, especially for large documents. Our system supports content negotiation where clients can request TOML responses.

### Benchmarks

| Parser | Small (1KB) | Medium (100KB) | Large (1MB) |
|--------|-------------|----------------|-------------|
| encoding/json | 50 us | 2 ms | 20 ms |
| BurntSushi/toml | 200 us | 15 ms | 200 ms |
| go-toml/v2 | 150 us | 10 ms | 150 ms |

### Impact

- TOML responses add 5-10x latency compared to JSON
- Large skill lists in TOML format could cause timeouts
- Memory pressure during TOML serialization

### Mitigations

1. **JSON as default** - TOML is opt-in via `Accept: application/toml` header
2. **go-toml/v2** - Using the faster v2 parser (2x faster than v1)
3. **Response limits** - Paginated responses cap at 100 items
4. **Caching** - Serialized responses cached by content type
5. **Streaming** - Large responses use chunked transfer encoding

### Future Improvements

- Consider implementing a custom TOML writer for known schemas
- Add TOML generation benchmarks to CI
- Document performance characteristics for API consumers

---

## ACP Protocol Correction

**Risk Level:** HIGH

### Problem

The Agent Communication Protocol (ACP) was initially designed with gRPC as the transport. However, the wider MCP ecosystem standardized on JSON-RPC 2.0 over stdio/SSE. Using gRPC would isolate our system from compatible tools.

### Decision

**Switch from gRPC to JSON-RPC 2.0** for MCP transport.

### Comparison

| Feature | gRPC | JSON-RPC 2.0 |
|---------|------|--------------|
| Performance | Binary, fast | Text, moderate |
| Ecosystem | Limited tools | Universal support |
| Schema | Proto definitions | JSON Schema |
| Streaming | Native | SSE/polling |
| Debugging | Harder | curl-friendly |

### Impact of Switch

- Loss of binary performance (acceptable for tool calls)
- Gain universal compatibility with MCP clients
- Simpler debugging and development
- No proto compilation step

### Implementation

The MCP server in `internal/mcp/` implements JSON-RPC 2.0:

```go
// Request format
{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "tools/call",
    "params": {
        "name": "search_skills",
        "arguments": {
            "query": "async rust"
        }
    }
}

// Response format
{
    "jsonrpc": "2.0",
    "id": 1,
    "result": {
        "content": [
            {
                "type": "text",
                "text": "{\"results\": [...]}"
            }
        ]
    }
}
```

---

## HTTP/3 Complexity

**Risk Level:** MEDIUM

### Problem

HTTP/3 (QUIC) adds significant complexity:

- **UDP firewall rules** - Many corporate networks block UDP
- **Library maturity** - quic-go is mature but less battle-tested than net/http
- **Debugging difficulty** - QUIC packet capture requires specialized tools
- **Client support** - Not all clients support HTTP/3
- **Certificate handling** - Self-signed certs needed for local development

### Benefit Analysis

| Feature | HTTP/2 | HTTP/3 | Worth it? |
|---------|--------|--------|-----------|
| Latency | Good | Better (0-RTT) | Yes |
| Head-of-line blocking | TCP-level | Solved | Yes |
| Connection migration | No | Yes | For mobile |
| Firewall friendly | Yes | No | No |
| Debugging | Easy | Hard | No |

### Decision

**Keep HTTP/3 as optional** (`ENABLE_HTTP3=true/false`), with HTTP/2 as the primary transport.

### Mitigations

1. **HTTP/2 always available** - Primary API on TCP port 8080
2. **HTTP/3 optional** - UDP port 8443, disabled by default
3. **Auto-negotiation** - Clients can discover HTTP/3 support via Alt-Svc
4. **Graceful fallback** - HTTP/3 failures automatically fall back to HTTP/2
5. **Monitoring** - Separate metrics for HTTP/2 vs HTTP/3 traffic

### Configuration

```env
# Default: HTTP/3 disabled for simplicity
ENABLE_HTTP3=false

# Enable when UDP is available and latency matters
ENABLE_HTTP3=true
HTTP3_PORT=8443
```

---

## Embedding Dimension Trade-offs

**Risk Level:** HIGH

### Problem

The embedding dimension (768) affects:

- **Storage**: Each vector = 768 * 4 bytes = 3KB per skill
- **Index size**: pgvector IVFFlat indexes scale with dimension
- **Query speed**: Higher dimensions = slower similarity search
- **Quality**: Higher dimensions = better semantic capture

### Dimension Comparison

| Provider | Dimension | Quality | Storage/skill | Index Build |
|----------|-----------|---------|---------------|-------------|
| Local (all-MiniLM) | 384 | Good | 1.5 KB | Fast |
| Local (default) | 768 | Better | 3 KB | Medium |
| OpenAI text-embedding-3-small | 1536 | Best | 6 KB | Slow |
| OpenAI text-embedding-3-large | 3072 | Excellent | 12 KB | Slow |

### Current Choice: 768 dimensions

**Rationale:** Balance between quality and resource usage for typical deployments.

### Risks

1. **Storage growth**: 10,000 skills = 30 MB of vector data
2. **Index memory**: IVFFlat index can use 2-3x the raw data size
3. **pgvector limits**: Suboptimal performance beyond 2000 dimensions
4. **Migration complexity**: Changing dimensions requires re-embedding all skills

### Mitigations

1. **Configurable dimension**: `EMBEDDING_DIMENSION` env var
2. **IVFFlat index**: Optimized for 768 dimensions with `lists = 100`
3. **Dimension migration**: Script to re-embed all skills when changing
4. **Monitoring**: Track vector storage size and query latency
5. **Quantization**: Future: support int8 quantized vectors (1/4 size)

### pgvector Index Configuration

```sql
-- For 768 dimensions with ~10,000 skills
CREATE INDEX idx_skills_embedding 
ON skills 
USING ivfflat (embedding vector_cosine_ops)
WITH (lists = 100);

-- For larger datasets (>100k skills)
CREATE INDEX idx_skills_embedding_hnsw
ON skills
USING hnsw (embedding vector_cosine_ops)
WITH (m = 16, ef_construction = 64);
```

---

## Auto-Growth Bounding

**Risk Level:** CRITICAL

### Problem

The auto-expansion pipeline uses LLMs to suggest new skills. Without proper bounds, this can:

1. **Create infinite loops** - A -> B -> C -> A circular suggestions
2. **Generate hallucinated skills** - Skills that don't exist in reality
3. **Explode storage** - Unbounded growth of proposed skills
4. **Waste API costs** - Repeated LLM calls for already-rejected suggestions

### Bounding Mechanisms

| Mechanism | Default | Purpose |
|-----------|---------|---------|
| `AUTO_EXPAND_MAX_DEPTH` | 3 | Max graph traversal depth |
| `AUTO_EXPAND_CONFIDENCE_THRESHOLD` | 0.7 | Min confidence for creation |
| `AUTO_EXPAND_COOLDOWN` | 24h | Min time between expansions |
| `AUTO_EXPAND_MAX_CANDIDATES` | 10 | Max skills proposed per run |
| **Deduplication** | Enabled | Fuzzy match against existing skills |
| **Human gate** | Always | Proposed skills need approval |

### Anti-Patterns Prevented

```
Without bounding:
  User has "Go" skill
  LLM suggests "Go Concurrency" (reasonable)
  LLM suggests "Operating Systems" (too broad)
  LLM suggests "Computer Science" (way too broad)
  LLM suggests "Mathematics" (irrelevant)
  ... infinite regression

With bounding:
  User has "Go" skill (depth=0)
  LLM suggests "Go Concurrency" (depth=1, confidence=0.9) -> proposed
  LLM suggests "Channels" (depth=2, confidence=0.85) -> proposed
  LLM suggests "Operating Systems" (depth=3, confidence=0.4) -> rejected
  Stop: max depth reached
```

### Mitigations

1. **Strict depth limiting**: Hard cap at `AUTO_EXPAND_MAX_DEPTH`
2. **Confidence threshold**: Only auto-create above 0.7 confidence
3. **Cooldown period**: Prevent repeated expansion attempts
4. **Duplicate detection**: Levenshtein distance + semantic similarity check
5. **Category constraint**: Only expand within known categories
6. **Code existence check**: Require matching code patterns
7. **Review queue**: All proposals start as `status=proposed`
8. **Audit log**: Track all expansion decisions

### Future: Learning-Based Bounds

```
Procedural memory learns:
  - Which expansion paths yield validated skills
  - Which categories have highest success rates
  - Optimal confidence thresholds per category
  - User preferences for expansion aggressiveness
```

---

## Hallucination Prevention

**Risk Level:** HIGH

### Problem

LLMs can hallucinate (fabricate) skills that sound plausible but don't exist or aren't relevant. This is particularly dangerous in a skill tracking system where false skills undermine trust.

### Hallucination Vectors

| Source | Risk | Example |
|--------|------|---------|
| Auto-expansion | High | LLM invents "Quantum Go Programming" |
| User input | Medium | User typos create false skills |
| Import analysis | Low | Libraries might not be skills |
| Description generation | Medium | LLM embellishes descriptions |

### Prevention Layers

**Layer 1: Input Validation**
```go
// Normalize skill names
name = normalizeSkillName(name)  // lowercase, trim, slugify

// Check against denylist
denied := []string{"", "n/a", "none", "unknown", "..."}

// Length limits
if len(name) < 2 || len(name) > 100 { reject }
```

**Layer 2: Existence Verification**
```go
// Semantic similarity check against existing skills
existing := searchSimilar(name, threshold=0.9)
if len(existing) > 0 {
    return ErrSkillAlreadyExists
}

// Code pattern check (for auto-expanded skills)
if autoExpanded && !hasCodePattern(name) {
    markForReview()
}
```

**Layer 3: Confidence Scoring**
```go
confidence = weightedScore(
    llmConfidence: 0.6,      // LLM's own confidence
    codeMatch: 0.8,          // Code pattern match strength
    graphRelevance: 0.7,     // Distance to existing skills
    communityValidation: 0.0, // Future: multiple users
)

if confidence < 0.7 {
    status = "needs-review"
}
```

**Layer 4: Human Review Gate**
```sql
-- All auto-proposed skills require review
UPDATE skills SET status = 'proposed' WHERE source = 'auto-expand';
-- Only after manual review:
UPDATE skills SET status = 'validated' WHERE reviewed_by IS NOT NULL;
```

**Layer 5: Continuous Validation**
```
Validation pipeline periodically checks:
  - Is there still evidence for this skill?
  - Has the skill been deprecated in the ecosystem?
  - Are there newer versions/frameworks to prefer?
```

---

## Performance at Scale

**Risk Level:** MEDIUM

### Problem

The system is designed for personal/small-team use (hundreds to thousands of skills). Scaling beyond this requires careful attention.

### Scaling Thresholds

| Metric | Current Target | Scaling Concern |
|--------|---------------|-----------------|
| Skills | < 10,000 | > 10,000 needs optimization |
| Evidence items | < 100,000 | > 100,000 needs partitioning |
| API requests/sec | < 100 | > 100 needs caching layer |
| Workers | 1-4 | > 8 needs queue redesign |
| Concurrent users | < 10 | > 50 needs connection pooling |

### Bottlenecks

1. **Vector search**: pgvector IVFFlat degrades at > 1M vectors
2. **Graph traversal**: Recursive queries are expensive
3. **LLM calls**: Rate limited and slow
4. **Git operations**: Linear scan of commit history
5. **Memory**: Working memory cache has fixed size

### Mitigations

1. **Connection pooling**: `DB_MAX_OPEN_CONNS=25`
2. **Request caching**: In-memory LRU for frequent queries
3. **Pagination**: All list endpoints paginated
4. **Async processing**: Worker queue for heavy operations
5. **Database indexes**: Optimized for query patterns
6. **Resource limits**: Docker memory/CPU constraints

### Future Scaling Path

```
Phase 1 (< 10k skills): Single instance, local PostgreSQL
Phase 2 (< 100k skills): Read replicas, Redis cache
Phase 3 (< 1M skills): Sharded PostgreSQL, dedicated embedding service
Phase 4 (> 1M skills): Kubernetes, managed PostgreSQL, vector DB
```

---

## Mitigation Summary

| Risk | Level | Status | Key Mitigation |
|------|-------|--------|----------------|
| TOML Performance | MEDIUM | Mitigated | JSON default, go-toml/v2 |
| ACP Protocol | HIGH | Resolved | JSON-RPC 2.0 |
| HTTP/3 Complexity | MEDIUM | Mitigated | Optional, HTTP/2 fallback |
| Embedding Dimensions | HIGH | Monitored | Configurable, IVFFlat index |
| Auto-Growth Bounds | CRITICAL | Mitigated | Depth/confidence/cooldown limits |
| Hallucination | HIGH | Mitigated | 5-layer prevention system |
| Performance at Scale | MEDIUM | Planned | Clear scaling thresholds |

### Monitoring Checklist

- [ ] Track TOML vs JSON request ratio and latency
- [ ] Monitor HTTP/3 connection success rate
- [ ] Alert on vector index query time > 100ms
- [ ] Log all auto-expansion decisions
- [ ] Track skill creation source (manual vs auto vs validated)
- [ ] Monitor database storage growth
- [ ] Alert on worker queue depth
- [ ] Track LLM API costs and rate limit usage
