package mcp

// ACP (Agent Client Protocol) adapter wire-in tests (NEW-2).
//
// internal/mcp/acp_adapter.go (ACPAdapter, NewACPAdapter, handleMessage, ...)
// was fully implemented but had ZERO callers and no `--mcp acp` transport --
// a §11.4.124 investigation confirmed it was dead code that was never wired,
// not code that had been deliberately retired. These tests exercise the REAL
// adapter behavior end-to-end (never a re-implementation of its logic) to
// prove the ACP JSON-RPC protocol actually works once wired in:
//
//   - agent/initialize returns a real ACPInitializeResult carrying the real
//     registered-tool list (getACPToolList), not a stub.
//   - agent/capabilities (the ACP analogue of MCP's tools/list) returns the
//     same real tool + prompt lists.
//   - an unknown ACP method is rejected with the correct JSON-RPC error code.
//   - agent/invoke round-trips through a REAL registered MCP tool
//     (mcpServer.dispatchTool -> the tool's actual handler) against a live
//     PostgreSQL instance (§11.4.27 -- no fakes beyond unit tests for this
//     class of behavior), proving the ACP adapter is not just parsing JSON
//     but genuinely driving the same tool-execution path as the stdio and
//     HTTP transports.
//
// ACPAdapter.writeStdout/writeResponse/writeError all write directly to the
// package-level os.Stdout (mirroring the stdio transport's own stdout
// discipline -- stdout is reserved for JSON-RPC, see stdio.go). captureStdout
// below temporarily redirects os.Stdout to a pipe so these tests can read the
// adapter's real emitted response bytes without touching the terminal.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/registry"
	"github.com/HelixDevelopment/skills/internal/skill"
	"go.uber.org/zap"
)

// captureStdout redirects os.Stdout to an in-memory pipe for the duration of
// fn, then returns everything written to it. This is the only way to observe
// ACPAdapter's real output: its writeStdout method is hardcoded to
// fmt.Fprintln(os.Stdout, ...) (mirroring stdio.go's own stdout discipline),
// so intercepting os.Stdout itself is the non-bluff way to assert on it --
// re-implementing writeResponse's marshaling here instead would test a copy,
// not the real adapter.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	os.Stdout = old

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	return buf.String()
}

// newTestACPAdapter builds a real *ACPAdapter wrapping a real *MCPServer with
// all 7 tools registered, against the given pool (nil is valid for handlers
// that never touch the database -- agent/initialize and agent/capabilities
// only read the static getACPToolList()/getACPPromptList() slices).
func newTestACPAdapter(pool *db.Pool) *ACPAdapter {
	store := skill.NewStore(pool)
	reg := registry.NewRegistry(pool)
	cfg := &config.Config{}
	cfg.MCP.Transport = "acp"
	mcpServer := NewMCPServer(pool, store, reg, cfg, zap.NewNop())
	mcpServer.RegisterTools()
	return NewACPAdapter(mcpServer)
}

// decodeACPResponse unmarshals a single captured ACP response line into an
// ACPResponse envelope, then re-marshals+re-unmarshals its untyped Result
// field into out (a common, non-bluff Go idiom for decoding a json.RawMessage
// stand-in when the field was declared interface{}).
func decodeACPResponse(t *testing.T, line string, out interface{}) *ACPResponse {
	t.Helper()
	line = strings.TrimSpace(line)
	if line == "" {
		t.Fatal("decodeACPResponse: captured stdout was empty -- adapter wrote nothing")
	}

	var resp ACPResponse
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("json.Unmarshal captured ACP response %q: %v", line, err)
	}

	if out != nil && resp.Result != nil {
		raw, err := json.Marshal(resp.Result)
		if err != nil {
			t.Fatalf("re-marshal ACPResponse.Result: %v", err)
		}
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("re-unmarshal ACPResponse.Result into %T: %v", out, err)
		}
	}

	return &resp
}

// TestACPAdapter_HandleMessage_InitializeReturnsRealToolListAndServerInfo
// drives a synthetic ACP `agent/initialize` request through the REAL
// handleMessage dispatch and proves the emitted ACPResponse carries the
// real server identity and the real (non-empty, matching the registered
// tool set) capability list -- not a stubbed/empty response.
func TestACPAdapter_HandleMessage_InitializeReturnsRealToolListAndServerInfo(t *testing.T) {
	adapter := newTestACPAdapter(nil)

	const reqLine = `{"jsonrpc":"2.0","id":1,"method":"agent/initialize","params":{"protocolVersion":"1.0","agent":{"name":"test-agent","version":"0.1"},"capabilities":{}}}`

	var result ACPInitializeResult
	out := captureStdout(t, func() {
		if err := adapter.handleMessage(reqLine); err != nil {
			t.Fatalf("handleMessage(agent/initialize): unexpected error: %v", err)
		}
	})
	resp := decodeACPResponse(t, out, &result)

	if resp.Error != nil {
		t.Fatalf("agent/initialize returned an error: %+v", resp.Error)
	}
	if id, ok := resp.ID.(float64); !ok || id != 1 {
		t.Errorf("response ID = %v (%T), want 1", resp.ID, resp.ID)
	}
	if result.ProtocolVersion == "" {
		t.Error("ProtocolVersion is empty, want a real protocol version")
	}
	if result.Server.Name != "helix-knowledge-skill-system" {
		t.Errorf("Server.Name = %q, want %q", result.Server.Name, "helix-knowledge-skill-system")
	}
	if !result.Capabilities.Prompts {
		t.Error("Capabilities.Prompts = false, want true")
	}
	if len(result.Capabilities.Tools) != 7 {
		t.Fatalf("Capabilities.Tools has %d entries, want 7 (the full registered tool set)", len(result.Capabilities.Tools))
	}
	foundSearch := false
	for _, tool := range result.Capabilities.Tools {
		if tool.Name == "skill_search" {
			foundSearch = true
			if tool.Description == "" {
				t.Error(`skill_search tool entry has an empty Description`)
			}
		}
	}
	if !foundSearch {
		t.Error(`Capabilities.Tools does not contain "skill_search"`)
	}
}

// TestACPAdapter_HandleMessage_CapabilitiesListsRegisteredToolsAndPrompts
// drives `agent/capabilities` (ACP's analogue of MCP's tools/list) and
// proves it returns the real tool + prompt catalogues.
func TestACPAdapter_HandleMessage_CapabilitiesListsRegisteredToolsAndPrompts(t *testing.T) {
	adapter := newTestACPAdapter(nil)

	const reqLine = `{"jsonrpc":"2.0","id":2,"method":"agent/capabilities","params":{}}`

	var result struct {
		Tools   []ACPClientToolInfo `json:"tools"`
		Prompts []map[string]string `json:"prompts"`
	}
	out := captureStdout(t, func() {
		if err := adapter.handleMessage(reqLine); err != nil {
			t.Fatalf("handleMessage(agent/capabilities): unexpected error: %v", err)
		}
	})
	resp := decodeACPResponse(t, out, &result)

	if resp.Error != nil {
		t.Fatalf("agent/capabilities returned an error: %+v", resp.Error)
	}
	wantTools := map[string]bool{
		"skill_search": false, "skill_get": false, "skill_tree": false,
		"skill_create": false, "learn_from_project": false,
		"missing_skills": false, "get_coverage": false,
	}
	for _, tool := range result.Tools {
		if _, ok := wantTools[tool.Name]; ok {
			wantTools[tool.Name] = true
		}
	}
	for name, found := range wantTools {
		if !found {
			t.Errorf("agent/capabilities tool list is missing registered tool %q", name)
		}
	}
	if len(result.Prompts) != 2 {
		t.Errorf("agent/capabilities prompt list has %d entries, want 2", len(result.Prompts))
	}
}

// TestACPAdapter_HandleMessage_UnknownMethodReturnsMethodNotFound proves an
// unrecognized ACP method is rejected with the correct JSON-RPC error code,
// never silently dropped and never a fake success.
func TestACPAdapter_HandleMessage_UnknownMethodReturnsMethodNotFound(t *testing.T) {
	adapter := newTestACPAdapter(nil)

	const reqLine = `{"jsonrpc":"2.0","id":3,"method":"agent/does-not-exist","params":{}}`

	out := captureStdout(t, func() {
		if err := adapter.handleMessage(reqLine); err != nil {
			t.Fatalf("handleMessage(unknown method): unexpected error: %v", err)
		}
	})
	resp := decodeACPResponse(t, out, nil)

	if resp.Error == nil {
		t.Fatal("unknown ACP method: expected an error response, got none")
	}
	if resp.Error.Code != ErrCodeMethodNotFound {
		t.Errorf("error code = %d, want %d (ErrCodeMethodNotFound)", resp.Error.Code, ErrCodeMethodNotFound)
	}
}

// TestACPAdapter_HandleMessage_MalformedJSONReturnsParseError proves
// handleMessage's parse-error path (invalid JSON on the wire) responds with
// the correct JSON-RPC parse-error code rather than panicking or hanging.
func TestACPAdapter_HandleMessage_MalformedJSONReturnsParseError(t *testing.T) {
	adapter := newTestACPAdapter(nil)

	out := captureStdout(t, func() {
		if err := adapter.handleMessage(`{not valid json`); err != nil {
			t.Fatalf("handleMessage(malformed JSON): unexpected error: %v", err)
		}
	})
	resp := decodeACPResponse(t, out, nil)

	if resp.Error == nil {
		t.Fatal("malformed JSON: expected a parse-error response, got none")
	}
	if resp.Error.Code != ErrCodeParseError {
		t.Errorf("error code = %d, want %d (ErrCodeParseError)", resp.Error.Code, ErrCodeParseError)
	}
}

// TestACPAdapter_HandleMessage_InvokeRoundTripsThroughRealTool_RequiresLiveDatabase
// is the strong anti-bluff proof (§11.4.27/§11.4.107): it drives a real
// `agent/invoke` request for a REGISTERED MCP tool ("missing_skills") through
// handleInvoke -> mcpServer.dispatchTool -> the tool's actual mcp-go handler
// -> a real SQL query against a live, freshly migrated PostgreSQL database,
// and asserts the ACP response is the tool's real (not re-implemented, not
// mocked) result. This proves the ACP transport genuinely executes MCP
// tools end-to-end, not merely that it can parse JSON-RPC envelopes.
func TestACPAdapter_HandleMessage_InvokeRoundTripsThroughRealTool_RequiresLiveDatabase(t *testing.T) {
	admin, ok := mcpSkipIfNoTestDB(t)
	if !ok {
		return
	}
	ctx := context.Background()

	dbCfg, cleanup := mcpCreateThrowawayDBConfig(t, admin)
	defer cleanup()

	pool, err := db.New(dbCfg)
	if err != nil {
		t.Fatalf("db.New(dbCfg): %v", err)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool, mcpRealMigrationsDir); err != nil {
		t.Fatalf("db.Migrate (full real migrations dir): %v", err)
	}

	adapter := newTestACPAdapter(pool)

	// A freshly migrated database has zero rows in skill_registry, so
	// missing_skills (domain omitted) must report zero gaps -- proving the
	// REAL query executed against REAL (empty) data, not a canned value.
	const reqLine = `{"jsonrpc":"2.0","id":7,"method":"agent/invoke","params":{"tool":"missing_skills","params":{}}}`

	var invokeResult ACPInvokeResult
	out := captureStdout(t, func() {
		if err := adapter.handleMessage(reqLine); err != nil {
			t.Fatalf("handleMessage(agent/invoke): unexpected error: %v", err)
		}
	})
	resp := decodeACPResponse(t, out, &invokeResult)

	if resp.Error != nil {
		t.Fatalf("agent/invoke(missing_skills) returned a JSON-RPC error: %+v", resp.Error)
	}
	if !invokeResult.Success {
		t.Fatalf("ACPInvokeResult.Success = false, want true (error: %q)", invokeResult.Error)
	}

	resultMap, ok := invokeResult.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("ACPInvokeResult.Result is %T, want a JSON object", invokeResult.Result)
	}
	count, ok := resultMap["count"].(float64)
	if !ok || count != 0 {
		t.Errorf(`result["count"] = %v, want 0 (float64) on a freshly migrated, empty database`, resultMap["count"])
	}
	if _, hasMessage := resultMap["message"]; !hasMessage {
		t.Error(`result is missing the "message" field the real missing_skills handler always sets for a zero-gap result`)
	}
}
