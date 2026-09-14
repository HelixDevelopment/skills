// Package codegraph provides the CodeGraph MCP integration layer for the
// HelixKnowledge Skill Graph System. It wraps an MCP client connection to
// the external CodeGraph server (via stdio or HTTP transport) and exposes
// code-intelligence operations -- symbol indexing, dependency analysis, and
// change watching -- consumed by the index manager and sync automation
// subsystems (§11.4.78/§11.4.79/§11.4.80).
//
// When the CodeGraph server is unavailable the client degrades gracefully:
// every public method returns empty results and logs a warning rather than
// propagating an error that would block the caller.
package codegraph

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/HelixDevelopment/skills/internal/config"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// MCP JSON-RPC types
// ---------------------------------------------------------------------------

// jsonrpcRequest is a JSON-RPC 2.0 request envelope.
type jsonrpcRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// jsonrpcResponse is a JSON-RPC 2.0 response envelope.
type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

// jsonrpcError is a JSON-RPC 2.0 error object.
type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ---------------------------------------------------------------------------
// Result types
// ---------------------------------------------------------------------------

// Symbol represents a code symbol returned by CodeGraph.
type Symbol struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"` // "function", "class", "interface", "variable", etc.
	File       string `json:"file"`
	Line       int    `json:"line"`
	Language   string `json:"language"`
	Signature  string `json:"signature"`
	DocComment string `json:"doc_comment"`
}

// Dependency represents a single dependency edge from CodeGraph.
type Dependency struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Kind   string `json:"kind"` // "import", "call", "inherit", "implement"
	File   string `json:"file"`
	Line   int    `json:"line"`
}

// IndexResult is the response from an index-project call.
type IndexResult struct {
	ProjectPath  string   `json:"project_path"`
	FilesIndexed int      `json:"files_indexed"`
	SymbolsFound int      `json:"symbols_found"`
	Languages    []string `json:"languages"`
	DurationMs   int64    `json:"duration_ms"`
}

// ChangeEvent represents a file-level change detected by CodeGraph.
type ChangeEvent struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"` // "create", "modify", "delete"
	Timestamp int64  `json:"timestamp"`
}

// ---------------------------------------------------------------------------
// MCPClient
// ---------------------------------------------------------------------------

// MCPClient connects to an external CodeGraph MCP server and exposes
// code-intelligence operations. It is safe for concurrent use.
type MCPClient struct {
	cfg       config.CodeGraphConfig
	logger    *zap.Logger
	transport string // "stdio" | "http"
	endpoint  string // HTTP URL when transport=http

	// stdio state
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	// HTTP client
	httpClient *http.Client

	// JSON-RPC bookkeeping
	mu     sync.Mutex
	nextID int
	avail  bool // false when CodeGraph is unreachable
}

// NewMCPClient creates a new CodeGraph MCP client from the application
// configuration. The client does NOT connect at construction time; call
// Connect before invoking any code-intelligence method.
func NewMCPClient(cfg config.CodeGraphConfig, logger *zap.Logger) *MCPClient {
	return &MCPClient{
		cfg:        cfg,
		logger:     logger,
		transport:  cfg.Transport,
		endpoint:   cfg.Endpoint,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		avail:      false,
	}
}

// Connect establishes the transport to the CodeGraph MCP server.
// For stdio transport it spawns the codegraph-server process.
// For HTTP transport it verifies reachability with a health probe.
// Returns nil on success; the client degrades gracefully on error.
func (c *MCPClient) Connect(ctx context.Context) error {
	switch c.transport {
	case "stdio":
		return c.connectStdio(ctx)
	case "http":
		return c.connectHTTP(ctx)
	default:
		return fmt.Errorf("unsupported codegraph transport: %q", c.transport)
	}
}

// connectStdio spawns the codegraph-server process and wires stdin/stdout.
func (c *MCPClient) connectStdio(ctx context.Context) error {
	cmdPath := "codegraph-server"
	cmd := exec.CommandContext(ctx, cmdPath)
	cmd.Stderr = os.Stderr // forward server logs to stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("codegraph stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("codegraph stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		c.logger.Warn("codegraph server not available (stdio)",
			zap.String("path", cmdPath),
			zap.Error(err),
		)
		c.avail = false
		return nil // degrade gracefully
	}

	c.cmd = cmd
	c.stdin = stdin
	c.stdout = stdout
	c.avail = true

	c.logger.Info("codegraph MCP client connected (stdio)",
		zap.Int("pid", cmd.Process.Pid),
	)

	// Initialize the MCP session.
	return c.initialize(ctx)
}

// connectHTTP verifies the CodeGraph HTTP endpoint is reachable.
func (c *MCPClient) connectHTTP(ctx context.Context) error {
	if c.endpoint == "" {
		c.logger.Warn("codegraph HTTP endpoint not configured, skipping")
		c.avail = false
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/health", nil)
	if err != nil {
		c.logger.Warn("codegraph health probe request failed", zap.Error(err))
		c.avail = false
		return nil
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.logger.Warn("codegraph server not available (http)",
			zap.String("endpoint", c.endpoint),
			zap.Error(err),
		)
		c.avail = false
		return nil // degrade gracefully
	}
	resp.Body.Close()

	c.avail = true
	c.logger.Info("codegraph MCP client connected (http)",
		zap.String("endpoint", c.endpoint),
	)

	return c.initialize(ctx)
}

// initialize sends the MCP initialize handshake.
func (c *MCPClient) initialize(ctx context.Context) error {
	resp, err := c.call(ctx, "initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]interface{}{
			"name":    "helix-skill-system",
			"version": "1.0.0",
		},
	})
	if err != nil {
		c.logger.Warn("codegraph MCP initialize failed", zap.Error(err))
		c.avail = false
		return nil // degrade gracefully
	}

	_ = resp // initialization succeeded
	c.logger.Debug("codegraph MCP session initialized")
	return nil
}

// IsAvailable reports whether the CodeGraph server is reachable.
func (c *MCPClient) IsAvailable() bool {
	return c.avail
}

// Close shuts down the client connection. For stdio transport it terminates
// the spawned process.
func (c *MCPClient) Close() error {
	if c.stdin != nil {
		c.stdin.Close()
	}
	if c.stdout != nil {
		c.stdout.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}
	c.avail = false
	return nil
}

// ---------------------------------------------------------------------------
// Code intelligence operations
// ---------------------------------------------------------------------------

// IndexProject submits a project directory for indexing. Returns the index
// result or an empty result when CodeGraph is unavailable.
func (c *MCPClient) IndexProject(ctx context.Context, projectPath string) (*IndexResult, error) {
	if !c.avail {
		c.logger.Warn("codegraph unavailable, returning empty index result",
			zap.String("project", projectPath),
		)
		return &IndexResult{ProjectPath: projectPath}, nil
	}

	raw, err := c.call(ctx, "tools/call", map[string]interface{}{
		"name": "index_project",
		"arguments": map[string]interface{}{
			"path": projectPath,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("codegraph index_project: %w", err)
	}

	var result IndexResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("decode index result: %w", err)
	}
	return &result, nil
}

// QuerySymbols searches for symbols matching a glob/regex pattern. Returns
// an empty slice when CodeGraph is unavailable.
func (c *MCPClient) QuerySymbols(ctx context.Context, pattern string) ([]Symbol, error) {
	if !c.avail {
		c.logger.Warn("codegraph unavailable, returning empty symbols",
			zap.String("pattern", pattern),
		)
		return nil, nil
	}

	raw, err := c.call(ctx, "tools/call", map[string]interface{}{
		"name": "query_symbols",
		"arguments": map[string]interface{}{
			"pattern": pattern,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("codegraph query_symbols: %w", err)
	}

	var symbols []Symbol
	if err := json.Unmarshal(raw, &symbols); err != nil {
		return nil, fmt.Errorf("decode symbols: %w", err)
	}
	return symbols, nil
}

// GetDependencies returns the dependency graph for a single file. Returns
// an empty slice when CodeGraph is unavailable.
func (c *MCPClient) GetDependencies(ctx context.Context, file string) ([]Dependency, error) {
	if !c.avail {
		c.logger.Warn("codegraph unavailable, returning empty dependencies",
			zap.String("file", file),
		)
		return nil, nil
	}

	raw, err := c.call(ctx, "tools/call", map[string]interface{}{
		"name": "get_dependencies",
		"arguments": map[string]interface{}{
			"file": file,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("codegraph get_dependencies: %w", err)
	}

	var deps []Dependency
	if err := json.Unmarshal(raw, &deps); err != nil {
		return nil, fmt.Errorf("decode dependencies: %w", err)
	}
	return deps, nil
}

// WatchChanges registers a filesystem watcher on the given path. Returns
// a channel of change events. The channel is closed when the context is
// cancelled or CodeGraph is unavailable (the caller should fall back to
// periodic polling).
func (c *MCPClient) WatchChanges(ctx context.Context, path string) (<-chan ChangeEvent, error) {
	ch := make(chan ChangeEvent, 64)

	if !c.avail {
		c.logger.Warn("codegraph unavailable, watch_changes returns closed channel",
			zap.String("path", path),
		)
		close(ch)
		return ch, nil
	}

	go c.watchLoop(ctx, path, ch)
	return ch, nil
}

// watchLoop polls the CodeGraph server for change events and forwards them
// to the channel. It exits when the context is cancelled.
func (c *MCPClient) watchLoop(ctx context.Context, path string, ch chan<- ChangeEvent) {
	defer close(ch)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	var lastTimestamp int64

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			raw, err := c.call(ctx, "tools/call", map[string]interface{}{
				"name": "watch_changes",
				"arguments": map[string]interface{}{
					"path":        path,
					"since":       lastTimestamp,
					"max_results": 100,
				},
			})
			if err != nil {
				c.logger.Debug("codegraph watch_changes poll failed", zap.Error(err))
				continue
			}

			var events []ChangeEvent
			if err := json.Unmarshal(raw, &events); err != nil {
				c.logger.Debug("codegraph watch_changes decode failed", zap.Error(err))
				continue
			}

			for _, ev := range events {
				if ev.Timestamp > lastTimestamp {
					lastTimestamp = ev.Timestamp
				}
				select {
				case ch <- ev:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// JSON-RPC transport
// ---------------------------------------------------------------------------

// call sends a JSON-RPC request and returns the raw result. It selects
// the transport based on the configured mode.
func (c *MCPClient) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()

	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	switch c.transport {
	case "stdio":
		return c.callStdio(ctx, req)
	case "http":
		return c.callHTTP(ctx, req)
	default:
		return nil, fmt.Errorf("unsupported transport: %q", c.transport)
	}
}

// callStdio writes a JSON-RPC request to the server's stdin and reads the
// response from stdout.
func (c *MCPClient) callStdio(ctx context.Context, req jsonrpcRequest) (json.RawMessage, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	payload = append(payload, '\n')

	c.mu.Lock()
	_, writeErr := c.stdin.Write(payload)
	c.mu.Unlock()
	if writeErr != nil {
		return nil, fmt.Errorf("write to codegraph stdin: %w", writeErr)
	}

	// Read response (blocking). Context cancellation is handled by the caller.
	decoder := json.NewDecoder(c.stdout)
	var resp jsonrpcResponse
	if err := decoder.Decode(&resp); err != nil {
		return nil, fmt.Errorf("read codegraph response: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("codegraph rpc error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	return resp.Result, nil
}

// callHTTP sends a JSON-RPC request over HTTP POST.
func (c *MCPClient) callHTTP(ctx context.Context, req jsonrpcRequest) (json.RawMessage, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/mcp", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("codegraph http call: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read http response: %w", err)
	}

	var rpcResp jsonrpcResponse
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return nil, fmt.Errorf("decode http response: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("codegraph rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	return rpcResp.Result, nil
}
