package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// ============================================================================
// HTTPTransport - MCP over HTTP with SSE streaming
// ============================================================================
//
// This transport supplies the MCP HTTP handlers and MOUNTS them onto the shared
// hardened API router (see cmd/server buildRouter + MCPServer.RegisterHTTPRoutes).
// It no longer owns its own HTTP listener: a second listener bound to the same
// host:port as the API server raced it for the port, and whichever won the bind
// decided the live security posture — the MCP listener had wildcard CORS and no
// authentication. The single shared listener now serves both surfaces under one
// hardened, fail-closed policy.
//
// Routes supplied (all under the auth-guarded /mcp/v1 group):
//   - POST /mcp/v1/messages         - Send JSON-RPC requests
//   - GET  /mcp/v1/sse              - Server-Sent Events for streaming
//   - GET  /mcp/v1/tools            - List available tools (REST fallback)
//   - POST /mcp/v1/tools/:name/call - Direct tool call (REST fallback)
//   - GET  /mcp/v1/prompts          - List prompts
//   - GET  /mcp/v1/prompts/:name    - Fetch a prompt
//
// /health and / are owned by the host router and are NOT registered here.

// HTTPTransport provides the MCP HTTP handlers mounted on the shared router.
type HTTPTransport struct {
	server *MCPServer
	logger *zap.Logger
}

// NewHTTPTransport creates a new HTTP transport (a route/handler provider; it
// does not start a listener).
func NewHTTPTransport(server *MCPServer) *HTTPTransport {
	return &HTTPTransport{
		server: server,
		logger: server.logger.With(zap.String("transport", "http")),
	}
}

// RegisterRoutes mounts the MCP route group (/mcp/v1/*) onto the shared router.
// The ENTIRE group is guarded by authMW — the same fail-closed API-key guard
// that protects /api/v1 (from api.ResolveAPIKeyAuth). Every JSON-RPC /
// tool-call / SSE / prompt route therefore requires authentication; authMW is
// nil ONLY in the explicit auth-disabled mode. The host router owns /health and
// /, so they are not (re-)registered here (that also avoids a duplicate-route
// panic on the shared engine).
func (t *HTTPTransport) RegisterRoutes(router *gin.Engine, authMW gin.HandlerFunc) {
	mcpGroup := router.Group("/mcp/v1")
	if authMW != nil {
		mcpGroup.Use(authMW)
	}
	mcpGroup.POST("/messages", t.handleJSONRPC)
	mcpGroup.GET("/sse", t.handleSSE)
	mcpGroup.GET("/tools", t.handleToolsListREST)
	mcpGroup.POST("/tools/:name/call", t.handleToolCallREST)
	mcpGroup.GET("/prompts", t.handlePromptsListREST)
	mcpGroup.GET("/prompts/:name", t.handlePromptsGetREST)
}

// handleJSONRPC processes JSON-RPC requests over HTTP.
func (t *HTTPTransport) handleJSONRPC(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, JSONRPCResponse{
			JSONRPC: "2.0",
			Error: &JSONRPCError{
				Code:    ErrCodeParseError,
				Message: "Failed to read request body",
			},
		})
		return
	}

	var req JSONRPCRequest
	if err := json.Unmarshal(body, &req); err != nil {
		c.JSON(http.StatusBadRequest, JSONRPCResponse{
			JSONRPC: "2.0",
			Error: &JSONRPCError{
				Code:    ErrCodeParseError,
				Message: "Invalid JSON",
				Data:    err.Error(),
			},
		})
		return
	}

	result, errResp := t.routeJSONRPC(c.Request.Context(), req)
	if errResp != nil {
		c.JSON(http.StatusOK, JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   errResp,
		})
		return
	}

	c.JSON(http.StatusOK, JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	})
}

// routeJSONRPC dispatches JSON-RPC requests to the appropriate handler.
func (t *HTTPTransport) routeJSONRPC(ctx context.Context, req JSONRPCRequest) (interface{}, *JSONRPCError) {
	switch req.Method {
	case "initialize":
		return t.handleInitializeHTTP(req.Params)
	case "notifications/initialized":
		return map[string]interface{}{}, nil
	case "tools/list":
		return t.handleToolsListInternal()
	case "tools/call":
		return t.handleToolsCallInternal(ctx, req.Params)
	case "prompts/list":
		return t.handlePromptsListInternal()
	case "prompts/get":
		return t.handlePromptsGetInternal(req.Params)
	case "ping":
		return map[string]interface{}{}, nil
	default:
		return nil, &JSONRPCError{
			Code:    ErrCodeMethodNotFound,
			Message: fmt.Sprintf("Method not found: %s", req.Method),
		}
	}
}

func (t *HTTPTransport) handleInitializeHTTP(params json.RawMessage) (interface{}, *JSONRPCError) {
	result := InitializeResult{
		ProtocolVersion: "2024-11-05",
		Capabilities: ServerCapabilities{
			Tools: &ToolsCapability{ListChanged: true},
		},
		ServerInfo: ServerInfo{
			Name:    "helix-knowledge-skill-system",
			Version: "1.0.0",
		},
	}
	return result, nil
}

func (t *HTTPTransport) handleToolsListInternal() (interface{}, *JSONRPCError) {
	tools := []map[string]interface{}{
		{
			"name":        "skill_search",
			"description": "Search the skill graph using text or vector similarity",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]string{"type": "string", "description": "Search query"},
					"limit": map[string]interface{}{"type": "integer", "description": "Max results", "default": 5},
				},
				"required": []string{"query"},
			},
		},
		{
			"name":        "skill_get",
			"description": "Retrieve complete skill record by name",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]string{"type": "string", "description": "Exact skill name"},
				},
				"required": []string{"name"},
			},
		},
		{
			"name":        "skill_tree",
			"description": "Get dependency tree for a skill",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name":  map[string]string{"type": "string", "description": "Root skill name"},
					"depth": map[string]interface{}{"type": "integer", "description": "Max depth", "default": 5},
				},
				"required": []string{"name"},
			},
		},
		{
			"name":        "skill_create",
			"description": "Create or update a skill using TOML format",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"toml": map[string]string{"type": "string", "description": "TOML skill definition"},
				},
				"required": []string{"toml"},
			},
		},
		{
			"name":        "learn_from_project",
			"description": "Submit a project path for skill extraction",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"project_path": map[string]string{"type": "string", "description": "Project directory path"},
					"languages":    map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}},
				},
				"required": []string{"project_path"},
			},
		},
		{
			"name":        "missing_skills",
			"description": "Find gaps in the knowledge graph",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"domain": map[string]string{"type": "string", "description": "Filter by domain (optional)"},
				},
			},
		},
		{
			"name":        "get_coverage",
			"description": "Get coverage report for a domain",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"domain": map[string]string{"type": "string", "description": "Domain (optional)"},
				},
			},
		},
	}

	return map[string]interface{}{"tools": tools}, nil
}

func (t *HTTPTransport) handleToolsCallInternal(ctx context.Context, params json.RawMessage) (interface{}, *JSONRPCError) {
	var callParams CallToolParams
	if err := json.Unmarshal(params, &callParams); err != nil {
		return nil, &JSONRPCError{
			Code:    ErrCodeInvalidParams,
			Message: "Invalid tools/call params",
			Data:    err.Error(),
		}
	}

	// Execute the tool via the registered mcp-go tool handler.
	toolResult, err := t.server.dispatchTool(ctx, callParams.Name, callParams.Arguments)
	if err != nil {
		return nil, &JSONRPCError{
			Code:    ErrCodeInternalError,
			Message: fmt.Sprintf("Tool '%s' failed", callParams.Name),
			Data:    err.Error(),
		}
	}

	return map[string]interface{}{
		"content": toolResult.Content,
		"isError": toolResult.IsError,
	}, nil
}

func (t *HTTPTransport) handlePromptsListInternal() (interface{}, *JSONRPCError) {
	prompts := []map[string]interface{}{
		{"name": "system-prompt", "description": "System prompt for HelixKnowledge agents"},
		{"name": "skill-format", "description": "TOML skill format guide"},
	}
	return map[string]interface{}{"prompts": prompts}, nil
}

func (t *HTTPTransport) handlePromptsGetInternal(params json.RawMessage) (interface{}, *JSONRPCError) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &JSONRPCError{Code: ErrCodeInvalidParams, Message: "Invalid params"}
	}

	var text string
	switch req.Name {
	case "system-prompt":
		text = GetSystemPrompt()
	case "skill-format":
		text = GetSkillFormatPrompt()
	default:
		return nil, &JSONRPCError{Code: ErrCodeInvalidParams, Message: fmt.Sprintf("Prompt not found: %s", req.Name)}
	}

	return map[string]interface{}{
		"messages": []map[string]interface{}{
			{
				"role":    "system",
				"content": map[string]string{"type": "text", "text": text},
			},
		},
	}, nil
}

// handleSSE provides Server-Sent Events for streaming updates.
func (t *HTTPTransport) handleSSE(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	// CORS for this stream is governed by the shared api.CORS allowlist applied
	// on the host router — no wildcard Access-Control-Allow-Origin is emitted.

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Streaming not supported"})
		return
	}

	fmt.Fprintf(c.Writer, "event: connected\ndata: %s\n\n", `{"status":"connected","server":"helix-knowledge-skill-system"}`)
	flusher.Flush()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	clientGone := c.Request.Context().Done()

	for {
		select {
		case <-clientGone:
			t.logger.Debug("SSE client disconnected")
			return
		case <-ticker.C:
			fmt.Fprintf(c.Writer, "event: ping\ndata: %s\n\n", `{"time":"`+time.Now().Format(time.RFC3339)+`"}`)
			flusher.Flush()
		}
	}
}

func (t *HTTPTransport) handleToolsListREST(c *gin.Context) {
	result, errResp := t.handleToolsListInternal()
	if errResp != nil {
		c.JSON(http.StatusOK, gin.H{"error": errResp.Message})
		return
	}
	c.JSON(http.StatusOK, result)
}

func (t *HTTPTransport) handleToolCallREST(c *gin.Context) {
	toolName := c.Param("name")

	var args map[string]interface{}
	if err := c.ShouldBindJSON(&args); err != nil && err != io.EOF {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return
	}

	params, _ := json.Marshal(CallToolParams{
		Name:      toolName,
		Arguments: args,
	})

	result, errResp := t.handleToolsCallInternal(c.Request.Context(), params)
	if errResp != nil {
		c.JSON(http.StatusOK, gin.H{"error": errResp.Message, "details": errResp.Data})
		return
	}
	c.JSON(http.StatusOK, result)
}

func (t *HTTPTransport) handlePromptsListREST(c *gin.Context) {
	result, errResp := t.handlePromptsListInternal()
	if errResp != nil {
		c.JSON(http.StatusOK, gin.H{"error": errResp.Message})
		return
	}
	c.JSON(http.StatusOK, result)
}

func (t *HTTPTransport) handlePromptsGetREST(c *gin.Context) {
	promptName := c.Param("name")
	params, _ := json.Marshal(map[string]string{"name": promptName})
	result, errResp := t.handlePromptsGetInternal(params)
	if errResp != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": errResp.Message})
		return
	}
	c.JSON(http.StatusOK, result)
}

// handleHealth and handleRoot were removed: /health and / are now owned by the
// shared host router (cmd/server buildRouter), which registers a single set of
// these endpoints reflecting the real, live, auth-guarded route surface. The
// wildcard corsMiddleware and the standalone loggingMiddleware were likewise
// removed with the deleted standalone HTTP listener — the shared router applies
// the hardened api.CORS allowlist and its own request logging.
