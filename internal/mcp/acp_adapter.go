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
// ACPAdapter - Agent Client Protocol adapter
// ============================================================================
//
// ACP (Agent Client Protocol) is a JSON-RPC 2.0 over stdio protocol used by
// some AI agents. It is structurally similar to MCP but uses different method
// names and message formats. This adapter translates between ACP and MCP.
//
// ACP Methods (translated to MCP equivalents):
//   - agent/initialize      -> initialize
//   - agent/capabilities    -> tools/list
//   - agent/invoke          -> tools/call
//   - agent/ping            -> ping
//   - agent/shutdown        -> shutdown

// ACPRequest represents an incoming ACP request.
type ACPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// ACPResponse represents an outgoing ACP response.
type ACPResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id,omitempty"`
	Result  interface{} `json:"result,omitempty"`
	Error   *ACPError   `json:"error,omitempty"`
}

// ACPError represents an ACP error.
type ACPError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// ACPInvokeParams contains parameters for agent/invoke.
type ACPInvokeParams struct {
	Tool   string                 `json:"tool"`
	Params map[string]interface{} `json:"params"`
}

// ACPInitializeParams contains ACP initialization parameters.
type ACPInitializeParams struct {
	ProtocolVersion string                 `json:"protocolVersion"`
	Agent           ACPAgentInfo           `json:"agent"`
	Capabilities    map[string]interface{} `json:"capabilities"`
}

// ACPAgentInfo describes the client agent.
type ACPAgentInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ACPInitializeResult is the ACP initialize response.
type ACPInitializeResult struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Server          ACPServerInfo   `json:"server"`
	Capabilities    ACPCapabilities `json:"capabilities"`
}

// ACPServerInfo describes this server to ACP clients.
type ACPServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ACPCapabilities describes server capabilities in ACP format.
type ACPCapabilities struct {
	Tools   []ACPClientToolInfo `json:"tools,omitempty"`
	Prompts bool                `json:"prompts,omitempty"`
}

// ACPClientToolInfo describes a tool for ACP clients.
type ACPClientToolInfo struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// ACPInvokeResult is the result of an ACP tool invocation.
type ACPInvokeResult struct {
	Success bool        `json:"success"`
	Result  interface{} `json:"result,omitempty"`
	Error   string      `json:"error,omitempty"`
}

// ACPAdapter translates between ACP and MCP protocols.
type ACPAdapter struct {
	mcpServer   *MCPServer
	reader      *bufio.Reader
	mu          sync.Mutex
	logger      *zap.Logger
	stopCh      chan struct{}
	stopOnce    sync.Once
	wg          sync.WaitGroup
	initialized bool
}

// NewACPAdapter creates a new ACP adapter wrapping an MCP server.
func NewACPAdapter(mcpServer *MCPServer) *ACPAdapter {
	return &ACPAdapter{
		mcpServer: mcpServer,
		reader:    bufio.NewReader(os.Stdin),
		logger:    mcpServer.logger.With(zap.String("adapter", "acp")),
		stopCh:    make(chan struct{}),
	}
}

// Run starts the blocking read loop for ACP messages.
func (a *ACPAdapter) Run() error {
	a.logger.Info("ACP adapter started - reading ACP JSON-RPC from stdin")

	a.wg.Add(1)
	defer a.wg.Done()

	for {
		select {
		case <-a.stopCh:
			a.logger.Info("ACP adapter stopping")
			return nil
		default:
		}

		line, err := a.reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				a.logger.Info("ACP adapter: stdin closed (EOF)")
				return nil
			}
			a.logger.Error("ACP read error", zap.Error(err))
			return fmt.Errorf("acp read: %w", err)
		}

		line = trimLine(line)
		if line == "" {
			continue
		}

		if err := a.handleMessage(line); err != nil {
			a.logger.Error("ACP message handling error", zap.Error(err))
		}
	}
}

// Stop signals the adapter to exit. It is idempotent and safe to call
// concurrently (and from multiple lifecycle paths): handleShutdown fires
// `go a.Stop()` for every agent/shutdown request while MCPServer.Shutdown also
// calls Stop() directly, so a bare close(a.stopCh) would panic with "close of
// closed channel" on the second call (F1). The sync.Once guards the single
// close; every caller still waits for the Run loop to finish.
func (a *ACPAdapter) Stop() {
	a.stopOnce.Do(func() {
		close(a.stopCh)
	})
	a.wg.Wait()
}

// handleMessage parses and dispatches an ACP message.
func (a *ACPAdapter) handleMessage(line string) error {
	var req ACPRequest
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		a.writeError(nil, ErrCodeParseError, "Parse error", err.Error())
		return nil
	}

	if req.JSONRPC != "2.0" {
		a.writeError(req.ID, ErrCodeInvalidRequest, "Invalid Request", "jsonrpc must be '2.0'")
		return nil
	}

	a.logger.Debug("ACP request", zap.String("method", req.Method), zap.Any("id", req.ID))

	switch req.Method {
	case "agent/initialize":
		return a.handleInitialize(req.ID, req.Params)
	case "agent/initialized":
		return a.handleInitialized(req.ID, req.Params)
	case "agent/capabilities":
		return a.handleCapabilities(req.ID, req.Params)
	case "agent/invoke":
		return a.handleInvoke(req.ID, req.Params)
	case "agent/ping":
		return a.handlePing(req.ID)
	case "agent/shutdown":
		return a.handleShutdown(req.ID)
	default:
		a.writeError(req.ID, ErrCodeMethodNotFound, fmt.Sprintf("Unknown ACP method: %s", req.Method), nil)
		return nil
	}
}

func (a *ACPAdapter) handleInitialize(id interface{}, params json.RawMessage) error {
	var initParams ACPInitializeParams
	if err := json.Unmarshal(params, &initParams); err != nil {
		a.writeError(id, ErrCodeInvalidParams, "Invalid initialize params", err.Error())
		return nil
	}

	a.logger.Info("ACP client initializing",
		zap.String("agent_name", initParams.Agent.Name),
		zap.String("agent_version", initParams.Agent.Version),
	)

	result := ACPInitializeResult{
		ProtocolVersion: "1.0",
		Server: ACPServerInfo{
			Name:    "helix-knowledge-skill-system",
			Version: "1.0.0",
		},
		Capabilities: ACPCapabilities{
			Tools:   a.getACPToolList(),
			Prompts: true,
		},
	}

	a.writeResponse(id, result)
	return nil
}

func (a *ACPAdapter) handleInitialized(id interface{}, params json.RawMessage) error {
	a.initialized = true
	a.logger.Info("ACP client initialization complete")
	return nil
}

func (a *ACPAdapter) handleCapabilities(id interface{}, params json.RawMessage) error {
	tools := a.getACPToolList()

	result := map[string]interface{}{
		"tools":   tools,
		"prompts": a.getACPPromptList(),
	}

	a.writeResponse(id, result)
	return nil
}

func (a *ACPAdapter) handleInvoke(id interface{}, params json.RawMessage) error {
	var invokeParams ACPInvokeParams
	if err := json.Unmarshal(params, &invokeParams); err != nil {
		a.writeError(id, ErrCodeInvalidParams, "Invalid invoke params", err.Error())
		return nil
	}

	if invokeParams.Tool == "" {
		a.writeError(id, ErrCodeInvalidParams, "Tool name is required", nil)
		return nil
	}

	a.logger.Debug("ACP invoke",
		zap.String("tool", invokeParams.Tool),
		zap.Any("params", invokeParams.Params),
	)

	ctx := context.Background()

	// Execute via the registered mcp-go tool handler.
	toolResult, err := a.mcpServer.dispatchTool(ctx, invokeParams.Tool, invokeParams.Params)
	if err != nil {
		a.writeResponse(id, ACPInvokeResult{
			Success: false,
			Error:   fmt.Sprintf("Tool '%s' failed: %v", invokeParams.Tool, err),
		})
		return nil
	}

	// Extract text content
	var resultText string
	for _, c := range toolResult.Content {
		if tc, ok := c.(mcp_go.TextContent); ok {
			resultText = tc.Text
			break
		}
	}

	// Try to parse result as JSON
	var resultData interface{}
	if err := json.Unmarshal([]byte(resultText), &resultData); err != nil {
		resultData = resultText
	}

	a.writeResponse(id, ACPInvokeResult{
		Success: !toolResult.IsError,
		Result:  resultData,
	})
	return nil
}

func (a *ACPAdapter) handlePing(id interface{}) error {
	a.writeResponse(id, map[string]interface{}{"pong": true})
	return nil
}

func (a *ACPAdapter) handleShutdown(id interface{}) error {
	a.writeResponse(id, map[string]interface{}{"status": "shutting_down"})
	a.logger.Info("ACP shutdown requested")
	go a.Stop()
	return nil
}

func (a *ACPAdapter) getACPToolList() []ACPClientToolInfo {
	return []ACPClientToolInfo{
		{
			Name:        "skill_search",
			Description: "Search the skill graph using text or vector similarity",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]string{"type": "string", "description": "Search query"},
					"limit": map[string]interface{}{"type": "integer", "description": "Max results", "default": "5"},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "skill_get",
			Description: "Retrieve complete skill record by name",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name": map[string]string{"type": "string", "description": "Exact skill name"},
				},
				"required": []string{"name"},
			},
		},
		{
			Name:        "skill_tree",
			Description: "Get dependency tree for a skill",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"name":  map[string]string{"type": "string", "description": "Root skill name"},
					"depth": map[string]interface{}{"type": "integer", "description": "Max depth", "default": "5"},
				},
				"required": []string{"name"},
			},
		},
		{
			Name:        "skill_create",
			Description: "Create or update a skill using TOML format",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"toml": map[string]string{"type": "string", "description": "TOML skill definition"},
				},
				"required": []string{"toml"},
			},
		},
		{
			Name:        "learn_from_project",
			Description: "Submit a project path for skill extraction",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"project_path": map[string]string{"type": "string", "description": "Project directory path"},
					"languages":    map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}},
				},
				"required": []string{"project_path"},
			},
		},
		{
			Name:        "missing_skills",
			Description: "Find gaps in the knowledge graph",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"domain": map[string]string{"type": "string", "description": "Filter by domain (optional)"},
				},
			},
		},
		{
			Name:        "get_coverage",
			Description: "Get coverage report for a domain",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"domain": map[string]string{"type": "string", "description": "Domain (optional)"},
				},
			},
		},
	}
}

func (a *ACPAdapter) getACPPromptList() []map[string]string {
	return []map[string]string{
		{"name": "system-prompt", "description": "System prompt for HelixKnowledge agents"},
		{"name": "skill-format", "description": "TOML skill format guide"},
	}
}

// writeResponse writes an ACP response to stdout.
func (a *ACPAdapter) writeResponse(id interface{}, result interface{}) {
	resp := ACPResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		a.logger.Error("Failed to marshal ACP response", zap.Error(err))
		return
	}

	a.writeStdout(data)
}

// writeError writes an ACP error response to stdout.
func (a *ACPAdapter) writeError(id interface{}, code int, message string, data interface{}) {
	resp := ACPResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &ACPError{
			Code:    code,
			Message: message,
			Data:    data,
		},
	}

	jsonData, err := json.Marshal(resp)
	if err != nil {
		a.logger.Error("Failed to marshal ACP error", zap.Error(err))
		return
	}

	a.writeStdout(jsonData)
}

// writeStdout writes data to stdout with a trailing newline.
func (a *ACPAdapter) writeStdout(data []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()

	fmt.Fprintln(os.Stdout, string(data))
	os.Stdout.Sync()
}
