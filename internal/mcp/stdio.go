package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"

	mcp_go "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"
)

// ============================================================================
// StdioTransport - JSON-RPC over stdin/stdout for CLI agents
// ============================================================================
//
// This transport reads JSON-RPC 2.0 requests from stdin and writes responses
// to stdout. CRITICAL: stdout must ONLY contain valid JSON-RPC messages.
// All logging and diagnostics go to stderr.

// JSONRPCRequest represents an incoming JSON-RPC 2.0 request.
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCResponse represents an outgoing JSON-RPC 2.0 response.
type JSONRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      interface{}   `json:"id,omitempty"`
	Result  interface{}   `json:"result,omitempty"`
	Error   *JSONRPCError `json:"error,omitempty"`
}

// JSONRPCError represents a JSON-RPC 2.0 error object.
type JSONRPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// Error codes per JSON-RPC 2.0 spec.
const (
	ErrCodeParseError     = -32700
	ErrCodeInvalidRequest = -32600
	ErrCodeMethodNotFound = -32601
	ErrCodeInvalidParams  = -32602
	ErrCodeInternalError  = -32603
	ErrCodeServerError    = -32000
)

// InitializeParams contains the client's initialization parameters.
type InitializeParams struct {
	ProtocolVersion string                 `json:"protocolVersion"`
	Capabilities    map[string]interface{} `json:"capabilities"`
	ClientInfo      ClientInfo             `json:"clientInfo"`
}

// ClientInfo describes the client application.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeResult is the server response to initialize.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      ServerInfo         `json:"serverInfo"`
}

// ServerCapabilities describes what this server supports.
type ServerCapabilities struct {
	Tools   *ToolsCapability   `json:"tools,omitempty"`
	Prompts *PromptsCapability `json:"prompts,omitempty"`
	Logging interface{}        `json:"logging,omitempty"`
}

// ToolsCapability describes tool support.
type ToolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// PromptsCapability describes prompt support.
type PromptsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ServerInfo describes this server.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// CallToolParams contains parameters for tools/call.
type CallToolParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
	Meta      map[string]interface{} `json:"_meta,omitempty"`
}

// CallToolResult is the result of a tool call.
type CallToolResult struct {
	Content []ToolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// ToolContent represents a single content item in a tool result.
type ToolContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ToolInfo describes a registered tool for listing.
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// ToolsListResult is the response for tools/list.
type ToolsListResult struct {
	Tools []ToolInfo `json:"tools"`
}

// StdioTransport implements JSON-RPC 2.0 over stdin/stdout.
type StdioTransport struct {
	server      *MCPServer
	reader      *bufio.Reader
	mu          sync.Mutex // protects stdout writes
	initialized bool
	logger      *zap.Logger
	stopCh      chan struct{}
	stopOnce    sync.Once
	wg          sync.WaitGroup
}

// NewStdioTransport creates a new stdio transport.
func NewStdioTransport(server *MCPServer) *StdioTransport {
	return &StdioTransport{
		server: server,
		reader: bufio.NewReader(os.Stdin),
		logger: server.logger.With(zap.String("transport", "stdio")),
		stopCh: make(chan struct{}),
	}
}

// Run starts the blocking read loop. It reads JSON-RPC requests from stdin
// and writes responses to stdout until Stop() is called or stdin closes.
func (t *StdioTransport) Run() error {
	t.logger.Info("Stdio transport started - reading JSON-RPC from stdin")

	t.wg.Add(1)
	defer t.wg.Done()

	for {
		select {
		case <-t.stopCh:
			t.logger.Info("Stdio transport stopping")
			return nil
		default:
		}

		// Read one line (JSON-RPC message)
		line, err := t.reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				t.logger.Info("Stdio transport: stdin closed (EOF)")
				return nil
			}
			if os.IsTimeout(err) {
				continue
			}
			t.logger.Error("Stdio read error", zap.Error(err))
			return fmt.Errorf("stdio read: %w", err)
		}

		// Trim whitespace
		line = trimLine(line)
		if line == "" {
			continue
		}

		// Handle the request
		if err := t.handleMessage(line); err != nil {
			t.logger.Error("Message handling error", zap.Error(err))
		}
	}
}

// Stop signals the transport to stop reading and exit. It is idempotent and
// safe to call concurrently (and from multiple lifecycle paths): handleShutdown
// fires `go t.Stop()` for every JSON-RPC shutdown request while
// MCPServer.Shutdown also calls Stop() directly, so in `both` mode a client
// shutdown followed by graceful shutdown would double-close t.stopCh and panic
// with "close of closed channel" (O1). The sync.Once guards the single close;
// every caller still waits for the Run loop to finish.
func (t *StdioTransport) Stop() {
	t.stopOnce.Do(func() {
		close(t.stopCh)
	})
	t.wg.Wait()
}

// handleMessage parses and dispatches a single JSON-RPC message.
func (t *StdioTransport) handleMessage(line string) error {
	var req JSONRPCRequest
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		t.writeError(nil, ErrCodeParseError, "Parse error", err.Error())
		return nil
	}

	if req.JSONRPC != "2.0" {
		t.writeError(req.ID, ErrCodeInvalidRequest, "Invalid Request", "jsonrpc must be '2.0'")
		return nil
	}

	t.logger.Debug("JSON-RPC request",
		zap.String("method", req.Method),
		zap.Any("id", req.ID),
	)

	switch req.Method {
	case "initialize":
		return t.handleInitialize(req.ID, req.Params)
	case "notifications/initialized":
		return t.handleInitialized(req.ID, req.Params)
	case "tools/list":
		return t.handleToolsList(req.ID, req.Params)
	case "tools/call":
		return t.handleToolsCall(req.ID, req.Params)
	case "prompts/list":
		return t.handlePromptsList(req.ID, req.Params)
	case "prompts/get":
		return t.handlePromptsGet(req.ID, req.Params)
	case "ping":
		return t.handlePing(req.ID)
	case "shutdown":
		return t.handleShutdown(req.ID)
	default:
		t.writeError(req.ID, ErrCodeMethodNotFound, fmt.Sprintf("Method not found: %s", req.Method), nil)
		return nil
	}
}

// handleInitialize responds to the MCP initialize handshake.
func (t *StdioTransport) handleInitialize(id interface{}, params json.RawMessage) error {
	var initParams InitializeParams
	if err := json.Unmarshal(params, &initParams); err != nil {
		t.writeError(id, ErrCodeInvalidParams, "Invalid initialize params", err.Error())
		return nil
	}

	t.logger.Info("Client initializing",
		zap.String("client_name", initParams.ClientInfo.Name),
		zap.String("client_version", initParams.ClientInfo.Version),
		zap.String("protocol_version", initParams.ProtocolVersion),
	)

	result := InitializeResult{
		ProtocolVersion: "2024-11-05", // MCP protocol version
		Capabilities: ServerCapabilities{
			Tools: &ToolsCapability{
				ListChanged: true,
			},
			Prompts: &PromptsCapability{
				ListChanged: false,
			},
		},
		ServerInfo: ServerInfo{
			Name:    "helix-knowledge-skill-system",
			Version: "1.0.0",
		},
	}

	t.writeResponse(id, result)
	return nil
}

// handleInitialized acknowledges client initialization completion.
func (t *StdioTransport) handleInitialized(id interface{}, params json.RawMessage) error {
	t.initialized = true
	t.logger.Info("Client initialization complete")
	return nil
}

// handleToolsList returns the list of available tools.
func (t *StdioTransport) handleToolsList(id interface{}, params json.RawMessage) error {
	toolDefs := []ToolInfo{
		{
			Name: "skill_search",
			Description: "Search the skill graph using text or vector similarity. Returns skills matching the query with relevance scores. " +
				"Score scale note (§G29): pg_trgm similarity in [0, 1] when no matching skill has an embedding; a much smaller fused " +
				"Reciprocal Rank Fusion value (typically ~0.01-0.03) once semantic recall is active for the query. Compare scores only " +
				"within one response, never against a fixed absolute threshold.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"query": {"type": "string", "description": "Search query text"},
					"limit": {"type": "integer", "description": "Maximum results (default: 5)", "default": 5}
				},
				"required": ["query"]
			}`),
		},
		{
			Name:        "skill_get",
			Description: "Retrieve the complete skill record including dependencies and resources by exact name.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"name": {"type": "string", "description": "Exact skill name"}
				},
				"required": ["name"]
			}`),
		},
		{
			Name:        "skill_tree",
			Description: "Get the dependency tree for a skill showing requires/extends/recommends relationships.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"name": {"type": "string", "description": "Root skill name"},
					"depth": {"type": "integer", "description": "Max depth (default: 5)", "default": 5}
				},
				"required": ["name"]
			}`),
		},
		{
			Name:        "skill_create",
			Description: "Create or update a skill in the knowledge graph using TOML format.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"toml": {"type": "string", "description": "Complete TOML-formatted skill definition"}
				},
				"required": ["toml"]
			}`),
		},
		{
			Name:        "learn_from_project",
			Description: "Submit a codebase path for automated skill extraction and learning.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"project_path": {"type": "string", "description": "Path to project directory"},
					"languages": {"type": "array", "items": {"type": "string"}, "description": "Languages to analyze"}
				},
				"required": ["project_path"]
			}`),
		},
		{
			Name:        "missing_skills",
			Description: "Find gaps in the knowledge graph - skills missing dependencies or with incomplete coverage.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"domain": {"type": "string", "description": "Filter by domain (optional)"}
				}
			}`),
		},
		{
			Name:        "get_coverage",
			Description: "Get a comprehensive coverage report for a domain or the entire skill graph.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"domain": {"type": "string", "description": "Domain to report on (optional)"}
				}
			}`),
		},
	}

	t.writeResponse(id, ToolsListResult{Tools: toolDefs})
	return nil
}

// handleToolsCall executes a tool call via the underlying mcp-go server.
func (t *StdioTransport) handleToolsCall(id interface{}, params json.RawMessage) error {
	var callParams CallToolParams
	if err := json.Unmarshal(params, &callParams); err != nil {
		t.writeError(id, ErrCodeInvalidParams, "Invalid tools/call params", err.Error())
		return nil
	}

	t.logger.Debug("Tool call",
		zap.String("tool", callParams.Name),
		zap.Any("arguments", callParams.Arguments),
	)

	// Use the underlying mcp-go server's tool handlers directly
	result := t.executeToolCall(callParams)
	t.writeResponse(id, result)
	return nil
}

// executeToolCall routes tool execution to the mcp-go server handlers.
func (t *StdioTransport) executeToolCall(callParams CallToolParams) CallToolResult {
	// We call the tool via the MCPServer which has the tool handlers registered.
	// The mcp-go server handles the actual dispatch.
	ctx := context.Background()

	// Route the call through the registered mcp-go tool handler.
	toolResult, err := t.server.dispatchTool(ctx, callParams.Name, callParams.Arguments)
	if err != nil {
		return CallToolResult{
			Content: []ToolContent{
				{Type: "text", Text: fmt.Sprintf(`{"error": "Tool '%s' failed: %v"}`, callParams.Name, err)},
			},
			IsError: true,
		}
	}

	// Convert result
	contents := make([]ToolContent, 0, len(toolResult.Content))
	for _, c := range toolResult.Content {
		if tc, ok := c.(mcp_go.TextContent); ok {
			contents = append(contents, ToolContent{
				Type: "text",
				Text: tc.Text,
			})
		}
	}

	return CallToolResult{
		Content: contents,
		IsError: toolResult.IsError,
	}
}

// handlePromptsList returns available prompts.
func (t *StdioTransport) handlePromptsList(id interface{}, params json.RawMessage) error {
	prompts := []map[string]interface{}{
		{
			"name":        "system-prompt",
			"description": "System prompt for agents using the HelixKnowledge skill system",
		},
		{
			"name":        "skill-format",
			"description": "Prompt explaining the TOML skill format for creating new skills",
		},
	}
	t.writeResponse(id, map[string]interface{}{"prompts": prompts})
	return nil
}

// handlePromptsGet returns a specific prompt.
func (t *StdioTransport) handlePromptsGet(id interface{}, params json.RawMessage) error {
	var promptReq struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(params, &promptReq); err != nil {
		t.writeError(id, ErrCodeInvalidParams, "Invalid prompts/get params", err.Error())
		return nil
	}

	var text string
	switch promptReq.Name {
	case "system-prompt":
		text = GetSystemPrompt()
	case "skill-format":
		text = GetSkillFormatPrompt()
	default:
		t.writeError(id, ErrCodeInvalidParams, fmt.Sprintf("Prompt not found: %s", promptReq.Name), nil)
		return nil
	}

	t.writeResponse(id, map[string]interface{}{
		"description": fmt.Sprintf("Prompt: %s", promptReq.Name),
		"messages": []map[string]interface{}{
			{
				"role":    "system",
				"content": map[string]string{"type": "text", "text": text},
			},
		},
	})
	return nil
}

// handlePing responds to a ping request.
func (t *StdioTransport) handlePing(id interface{}) error {
	t.writeResponse(id, map[string]interface{}{})
	return nil
}

// handleShutdown responds to a shutdown request and initiates cleanup.
func (t *StdioTransport) handleShutdown(id interface{}) error {
	t.writeResponse(id, map[string]interface{}{"status": "shutting_down"})
	t.logger.Info("Shutdown requested via JSON-RPC")

	go t.Stop()

	return nil
}

// writeResponse writes a JSON-RPC response to stdout (thread-safe).
func (t *StdioTransport) writeResponse(id interface{}, result interface{}) {
	resp := JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.logger.Error("Failed to marshal response", zap.Error(err))
		return
	}

	t.writeStdout(data)
}

// writeError writes a JSON-RPC error to stdout (thread-safe).
func (t *StdioTransport) writeError(id interface{}, code int, message string, data interface{}) {
	resp := JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &JSONRPCError{
			Code:    code,
			Message: message,
			Data:    data,
		},
	}

	jsonData, err := json.Marshal(resp)
	if err != nil {
		t.logger.Error("Failed to marshal error response", zap.Error(err))
		return
	}

	t.writeStdout(jsonData)
}

// writeStdout writes data to stdout with a trailing newline.
// This method is the ONLY place that writes to stdout.
func (t *StdioTransport) writeStdout(data []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()

	fmt.Fprintln(os.Stdout, string(data))
	os.Stdout.Sync()
}

// trimLine removes whitespace and trailing newline from a line.
func trimLine(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
