package main

import "github.com/HelixDevelopment/skills/internal/mcp"

// mcpRunner is the subset of *mcp.MCPServer behavior the blocking transport
// dispatch (runBlockingTransport below) depends on. Extracting it as an
// interface makes the "stdio"/"acp" case in main()'s transport switch
// mechanically unit-testable: a test can inject a fake mcpRunner and assert
// exactly which method is invoked for a given --mcp value, without a live
// database, a blocking stdin read, or a network listener.
type mcpRunner interface {
	// RunStdio starts the blocking MCP-over-stdio transport for CLI agents.
	RunStdio() error
	// RunACP starts the blocking ACP (Agent Client Protocol) adapter.
	RunACP() error
}

// var _ mcpRunner = (*mcp.MCPServer)(nil) is a compile-time proof that the
// REAL *mcp.MCPServer satisfies mcpRunner -- if RunACP (or RunStdio) is ever
// renamed or its signature changes, the package fails to BUILD, not merely a
// test to pass.
var _ mcpRunner = (*mcp.MCPServer)(nil)

const (
	// transportStdio is the blocking MCP-over-stdio transport mode.
	transportStdio = "stdio"
	// transportACP is the blocking ACP (Agent Client Protocol) transport mode
	// (NEW-2): wires the previously-dead internal/mcp/acp_adapter.go in as a
	// selectable `--mcp acp` / `mcp.transport = "acp"` transport.
	transportACP = "acp"
)

// runBlockingTransport dispatches a blocking, non-HTTP transport mode
// ("stdio" or "acp") to its corresponding *mcp.MCPServer runner method,
// returning that call's error. It reports handled=false for any other mode
// ("http", "both", empty, or an unrecognized value) so the caller's switch
// falls through to the shared-HTTP-listener branch (setupAPI) -- main()'s
// EXACT prior behavior for those modes is unchanged; this function only
// isolates the two purely-blocking, non-HTTP cases into a directly
// unit-testable seam.
func runBlockingTransport(mode string, r mcpRunner) (handled bool, err error) {
	switch mode {
	case transportStdio:
		return true, r.RunStdio()
	case transportACP:
		return true, r.RunACP()
	default:
		return false, nil
	}
}
