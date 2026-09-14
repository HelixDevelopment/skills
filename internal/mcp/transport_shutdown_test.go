package mcp

// Shutdown-idempotency regression guards for the stdio and ACP transports
// (F1 + O1, review of the NEW-2 ACPAdapter wire-in).
//
// Root cause (identical in both transports):
//   - internal/mcp/acp_adapter.go: Stop() (~:164-167) did a bare, non-idempotent
//     close(a.stopCh); handleShutdown() (~:305-310) fires `go a.Stop()` for EVERY
//     agent/shutdown request. Two agent/shutdown lines on stdin -> two goroutines
//     race close() on one channel -> `panic: close of closed channel` -> process
//     death (F1, reproduced 5/5 by the reviewer).
//   - internal/mcp/stdio.go: Stop() (~:196-199) had the identical bare close;
//     handleShutdown() (~:479-486) fires `go t.Stop()`, and MCPServer.Shutdown()
//     (server.go ~:218-223) ALSO calls stdio.Stop()/acp.Stop() directly. In `both`
//     mode a client `shutdown` request (go Stop) followed by main.go's graceful
//     Shutdown -> Stop() double-closes -> the same panic (O1, reachable today).
//
// A JSON-RPC server MUST NOT be crashable by any input/lifecycle sequence
// (§11.4.194). Each guard below reproduces the EXACT double-close condition
// deterministically (§11.4.199 -- no scheduler race): it drives the REAL wire
// shutdown path (a real `agent/shutdown`/`shutdown` request, whose handler fires
// the first `go Stop`), WAITS on the stopCh close so the first Stop has provably
// run, THEN invokes Stop() a second time (modelling the graceful-shutdown /
// second-request path). On the pre-fix code the second Stop is a bare close() of
// an already-closed channel -> panic; the deferred recover turns that into a
// deterministic test FAILURE (the RED). Post-fix (sync.Once) the second Stop is a
// no-op and the test passes (the GREEN) -- one source, RED-on-broken /
// GREEN-on-fixed (§11.4.115), a permanent regression guard (§11.4.135).

import (
	"testing"
	"time"

	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/HelixDevelopment/skills/internal/registry"
	"github.com/HelixDevelopment/skills/internal/skill"
	"go.uber.org/zap"
)

// newTestStdioTransport builds a real *StdioTransport wrapping a real *MCPServer
// (nil pool is valid: the shutdown path never touches the database), mirroring
// newTestACPAdapter in acp_adapter_test.go.
func newTestStdioTransport() *StdioTransport {
	store := skill.NewStore(nil)
	reg := registry.NewRegistry(nil)
	cfg := &config.Config{}
	cfg.MCP.Transport = "stdio"
	mcpServer := NewMCPServer(nil, store, reg, cfg, zap.NewNop())
	mcpServer.RegisterTools()
	return NewStdioTransport(mcpServer)
}

// TestACPAdapter_Shutdown_IsIdempotent_NoDoubleClosePanic reproduces F1: a real
// agent/shutdown request (first `go a.Stop()`) followed by a second Stop() must
// NOT crash the process.
func TestACPAdapter_Shutdown_IsIdempotent_NoDoubleClosePanic(t *testing.T) {
	adapter := newTestACPAdapter(nil)

	// First shutdown: the REAL wire path. handleShutdown fires `go a.Stop()`,
	// closing stopCh exactly as a client agent/shutdown request does. Its
	// JSON-RPC response goes to os.Stdout, so swallow it via captureStdout
	// (defined in acp_adapter_test.go, same package).
	_ = captureStdout(t, func() {
		if err := adapter.handleMessage(`{"jsonrpc":"2.0","id":1,"method":"agent/shutdown"}`); err != nil {
			t.Fatalf("handleMessage(agent/shutdown): unexpected error: %v", err)
		}
	})

	// Wait until the spawned Stop has actually closed stopCh, so the second
	// Stop below deterministically hits an already-closed channel -- the exact
	// pre-fix double-close condition, with no scheduler race (§11.4.199).
	select {
	case <-adapter.stopCh:
	case <-time.After(2 * time.Second):
		t.Fatal("first (agent/shutdown-driven) Stop did not close stopCh within 2s")
	}

	// Second Stop: models MCPServer.Shutdown -> acp.Stop() arriving after the
	// client already requested shutdown (and the two-agent/shutdown-lines race).
	// Pre-fix: bare close() of an already-closed channel -> panic. Post-fix: no-op.
	var recovered interface{}
	func() {
		defer func() { recovered = recover() }()
		adapter.Stop()
	}()
	if recovered != nil {
		t.Fatalf("ACPAdapter.Stop is not idempotent -- second Stop panicked (F1 reproduced): %v", recovered)
	}
}

// TestStdioTransport_Shutdown_IsIdempotent_NoDoubleClosePanic reproduces O1 (the
// `both`-mode double-close): a real `shutdown` request (first `go t.Stop()`)
// followed by graceful MCPServer.Shutdown -> stdio.Stop() must NOT crash.
func TestStdioTransport_Shutdown_IsIdempotent_NoDoubleClosePanic(t *testing.T) {
	transport := newTestStdioTransport()

	// First shutdown: the REAL wire path. handleShutdown fires `go t.Stop()`.
	_ = captureStdout(t, func() {
		if err := transport.handleMessage(`{"jsonrpc":"2.0","id":1,"method":"shutdown"}`); err != nil {
			t.Fatalf("handleMessage(shutdown): unexpected error: %v", err)
		}
	})

	// Wait for the spawned Stop to close stopCh (deterministic, no race).
	select {
	case <-transport.stopCh:
	case <-time.After(2 * time.Second):
		t.Fatal("first (shutdown-driven) Stop did not close stopCh within 2s")
	}

	// Second Stop: models main.go's gracefulShutdown -> MCPServer.Shutdown ->
	// stdio.Stop() in `both` mode. Pre-fix: double-close panic. Post-fix: no-op.
	var recovered interface{}
	func() {
		defer func() { recovered = recover() }()
		transport.Stop()
	}()
	if recovered != nil {
		t.Fatalf("StdioTransport.Stop is not idempotent -- second Stop panicked (O1 reproduced): %v", recovered)
	}
}
