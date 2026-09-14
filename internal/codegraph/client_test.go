package codegraph

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/HelixDevelopment/skills/internal/config"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Mock MCP server (HTTP)
// ---------------------------------------------------------------------------

// mockMCPServer is a minimal HTTP server that responds to JSON-RPC calls
// like a real CodeGraph MCP server would.
type mockMCPServer struct {
	mu       sync.Mutex
	calls    []string // record of method names called
	symbols  []Symbol
	deps     []Dependency
	indexRes IndexResult
}

func newMockMCPServer() *mockMCPServer {
	return &mockMCPServer{
		symbols: []Symbol{
			{Name: "HandleRequest", Kind: "function", File: "handler.go", Line: 10, Language: "go", Signature: "func HandleRequest(w http.ResponseWriter, r *http.Request)"},
			{Name: "UserService", Kind: "struct", File: "service.go", Line: 5, Language: "go", Signature: "type UserService struct {}"},
		},
		deps: []Dependency{
			{Source: "handler.go", Target: "net/http", Kind: "import", File: "handler.go", Line: 3},
			{Source: "handler.go", Target: "service.UserService", Kind: "call", File: "handler.go", Line: 15},
		},
		indexRes: IndexResult{
			ProjectPath:  "/tmp/test-project",
			FilesIndexed: 42,
			SymbolsFound: 128,
			Languages:    []string{"go", "python"},
			DurationMs:   1500,
		},
	}
}

func (m *mockMCPServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		var req jsonrpcRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		m.mu.Lock()
		m.calls = append(m.calls, req.Method)
		m.mu.Unlock()

		var result interface{}
		switch req.Method {
		case "initialize":
			result = map[string]interface{}{"protocolVersion": "2024-11-05"}
		case "tools/call":
			// Parse the tool name from params.
			params, _ := json.Marshal(req.Params)
			var toolReq struct {
				Name      string                 `json:"name"`
				Arguments map[string]interface{} `json:"arguments"`
			}
			json.Unmarshal(params, &toolReq)

			switch toolReq.Name {
			case "query_symbols":
				result = m.symbols
			case "get_dependencies":
				result = m.deps
			case "index_project":
				result = m.indexRes
			default:
				result = map[string]interface{}{}
			}
		default:
			result = map[string]interface{}{}
		}

		resp := jsonrpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
		}

		resultBytes, _ := json.Marshal(result)
		resp.Result = resultBytes

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestNewMCPClient(t *testing.T) {
	cfg := config.CodeGraphConfig{
		Enabled:   true,
		Transport: "http",
		Endpoint:  "http://localhost:9999",
	}
	logger := zap.NewNop()

	client := NewMCPClient(cfg, logger)
	if client == nil {
		t.Fatal("NewMCPClient returned nil")
	}
	if client.transport != "http" {
		t.Errorf("transport = %q, want %q", client.transport, "http")
	}
	if client.endpoint != "http://localhost:9999" {
		t.Errorf("endpoint = %q, want %q", client.endpoint, "http://localhost:9999")
	}
	if client.IsAvailable() {
		t.Error("new client should not be available before Connect")
	}
}

func TestMCPClient_ConnectHTTP_Success(t *testing.T) {
	mock := newMockMCPServer()
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()

	cfg := config.CodeGraphConfig{
		Enabled:   true,
		Transport: "http",
		Endpoint:  srv.URL,
	}
	logger := zap.NewNop()

	client := NewMCPClient(cfg, logger)
	defer client.Close()

	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	if !client.IsAvailable() {
		t.Error("client should be available after successful Connect")
	}

	// Verify initialize was called.
	mock.mu.Lock()
	found := false
	for _, c := range mock.calls {
		if c == "initialize" {
			found = true
			break
		}
	}
	mock.mu.Unlock()
	if !found {
		t.Error("initialize was not called during Connect")
	}
}

func TestMCPClient_ConnectHTTP_Unavailable(t *testing.T) {
	cfg := config.CodeGraphConfig{
		Enabled:   true,
		Transport: "http",
		Endpoint:  "http://127.0.0.1:1", // unreachable
	}
	logger := zap.NewNop()

	client := NewMCPClient(cfg, logger)
	defer client.Close()

	// Should not return error — graceful degradation.
	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("Connect should degrade gracefully, got: %v", err)
	}
	if client.IsAvailable() {
		t.Error("client should NOT be available when server is unreachable")
	}
}

func TestMCPClient_ConnectHTTP_EmptyEndpoint(t *testing.T) {
	cfg := config.CodeGraphConfig{
		Enabled:   true,
		Transport: "http",
		Endpoint:  "",
	}
	logger := zap.NewNop()

	client := NewMCPClient(cfg, logger)
	defer client.Close()

	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("Connect should degrade gracefully, got: %v", err)
	}
	if client.IsAvailable() {
		t.Error("client should NOT be available with empty endpoint")
	}
}

func TestMCPClient_Connect_BadTransport(t *testing.T) {
	cfg := config.CodeGraphConfig{
		Enabled:   true,
		Transport: "quic",
	}
	logger := zap.NewNop()

	client := NewMCPClient(cfg, logger)
	defer client.Close()

	err := client.Connect(context.Background())
	if err == nil {
		t.Fatal("Connect with bad transport should return error")
	}
}

func TestMCPClient_QuerySymbols_Available(t *testing.T) {
	mock := newMockMCPServer()
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()

	cfg := config.CodeGraphConfig{
		Enabled:   true,
		Transport: "http",
		Endpoint:  srv.URL,
	}
	logger := zap.NewNop()

	client := NewMCPClient(cfg, logger)
	defer client.Close()

	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	symbols, err := client.QuerySymbols(context.Background(), "kind:function")
	if err != nil {
		t.Fatalf("QuerySymbols failed: %v", err)
	}
	if len(symbols) != 2 {
		t.Errorf("got %d symbols, want 2", len(symbols))
	}
	if symbols[0].Name != "HandleRequest" {
		t.Errorf("first symbol name = %q, want %q", symbols[0].Name, "HandleRequest")
	}
}

func TestMCPClient_QuerySymbols_Unavailable(t *testing.T) {
	cfg := config.CodeGraphConfig{
		Enabled:   true,
		Transport: "http",
		Endpoint:  "http://127.0.0.1:1",
	}
	logger := zap.NewNop()

	client := NewMCPClient(cfg, logger)
	defer client.Close()

	client.Connect(context.Background()) // will degrade

	symbols, err := client.QuerySymbols(context.Background(), "*")
	if err != nil {
		t.Fatalf("QuerySymbols should not error when unavailable, got: %v", err)
	}
	if symbols != nil {
		t.Errorf("expected nil symbols when unavailable, got %d", len(symbols))
	}
}

func TestMCPClient_GetDependencies(t *testing.T) {
	mock := newMockMCPServer()
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()

	cfg := config.CodeGraphConfig{
		Enabled:   true,
		Transport: "http",
		Endpoint:  srv.URL,
	}
	logger := zap.NewNop()

	client := NewMCPClient(cfg, logger)
	defer client.Close()

	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	deps, err := client.GetDependencies(context.Background(), "handler.go")
	if err != nil {
		t.Fatalf("GetDependencies failed: %v", err)
	}
	if len(deps) != 2 {
		t.Errorf("got %d deps, want 2", len(deps))
	}
}

func TestMCPClient_IndexProject(t *testing.T) {
	mock := newMockMCPServer()
	srv := httptest.NewServer(mock.handler())
	defer srv.Close()

	cfg := config.CodeGraphConfig{
		Enabled:   true,
		Transport: "http",
		Endpoint:  srv.URL,
	}
	logger := zap.NewNop()

	client := NewMCPClient(cfg, logger)
	defer client.Close()

	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	result, err := client.IndexProject(context.Background(), "/tmp/test-project")
	if err != nil {
		t.Fatalf("IndexProject failed: %v", err)
	}
	if result.FilesIndexed != 42 {
		t.Errorf("FilesIndexed = %d, want 42", result.FilesIndexed)
	}
	if result.SymbolsFound != 128 {
		t.Errorf("SymbolsFound = %d, want 128", result.SymbolsFound)
	}
}

func TestMCPClient_IndexProject_Unavailable(t *testing.T) {
	cfg := config.CodeGraphConfig{
		Enabled:   true,
		Transport: "http",
		Endpoint:  "http://127.0.0.1:1",
	}
	logger := zap.NewNop()

	client := NewMCPClient(cfg, logger)
	defer client.Close()

	client.Connect(context.Background())

	result, err := client.IndexProject(context.Background(), "/tmp/test")
	if err != nil {
		t.Fatalf("IndexProject should not error when unavailable, got: %v", err)
	}
	if result.FilesIndexed != 0 {
		t.Errorf("expected 0 files when unavailable, got %d", result.FilesIndexed)
	}
}

func TestMCPClient_WatchChanges_Unavailable(t *testing.T) {
	cfg := config.CodeGraphConfig{
		Enabled:   true,
		Transport: "http",
		Endpoint:  "http://127.0.0.1:1",
	}
	logger := zap.NewNop()

	client := NewMCPClient(cfg, logger)
	defer client.Close()

	client.Connect(context.Background())

	ch, err := client.WatchChanges(context.Background(), "/tmp/test")
	if err != nil {
		t.Fatalf("WatchChanges should not error when unavailable, got: %v", err)
	}

	// Channel should be closed immediately.
	_, ok := <-ch
	if ok {
		t.Error("expected closed channel when unavailable")
	}
}
