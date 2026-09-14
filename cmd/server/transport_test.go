package main

// Unit tests for the "acp" MCP transport wire-in (NEW-2).
//
// main()'s transport switch has real side effects (binds a DB pool, an HTTP
// listener, blocks reading stdin) that make it impractical to exercise
// end-to-end in a fast unit test. runBlockingTransport (transport.go) is the
// extracted, side-effect-isolated seam that main()'s "stdio"/"acp" cases
// delegate to: it takes only a mode string and an mcpRunner, so a fake
// mcpRunner lets these tests prove EXACTLY which underlying *mcp.MCPServer
// method a given --mcp value dispatches to, without a live database, stdin,
// or network listener.
//
// This is the direct regression guard for the wire-in defect: before this
// change, "acp" fell through to the default branch (a plain HTTP server,
// exactly like an unrecognized or empty transport value) because no case
// for it existed anywhere in the dispatch path.

import (
	"errors"
	"testing"

	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/HelixDevelopment/skills/internal/db"
	"github.com/HelixDevelopment/skills/internal/mcp"
	"github.com/HelixDevelopment/skills/internal/registry"
	"github.com/HelixDevelopment/skills/internal/skill"
	"go.uber.org/zap"
)

// fakeMCPRunner is a minimal test double for mcpRunner. It never touches a
// database, stdin, or a network socket -- it only records which method was
// called, so tests can assert dispatch behavior in isolation.
type fakeMCPRunner struct {
	stdioCalled bool
	acpCalled   bool
	stdioErr    error
	acpErr      error
}

func (f *fakeMCPRunner) RunStdio() error {
	f.stdioCalled = true
	return f.stdioErr
}

func (f *fakeMCPRunner) RunACP() error {
	f.acpCalled = true
	return f.acpErr
}

func TestRunBlockingTransport_ACPDispatchesToRunACPOnly(t *testing.T) {
	f := &fakeMCPRunner{}

	handled, err := runBlockingTransport("acp", f)

	if !handled {
		t.Fatal(`runBlockingTransport("acp", ...): handled = false, want true`)
	}
	if err != nil {
		t.Fatalf(`runBlockingTransport("acp", ...): unexpected error: %v`, err)
	}
	if !f.acpCalled {
		t.Error("RunACP was not invoked for mode \"acp\"")
	}
	if f.stdioCalled {
		t.Error("RunStdio was invoked for mode \"acp\" -- it must not be (no HTTP/stdio mixing)")
	}
}

func TestRunBlockingTransport_StdioDispatchesToRunStdioOnly(t *testing.T) {
	f := &fakeMCPRunner{}

	handled, err := runBlockingTransport("stdio", f)

	if !handled {
		t.Fatal(`runBlockingTransport("stdio", ...): handled = false, want true`)
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !f.stdioCalled {
		t.Error("RunStdio was not invoked for mode \"stdio\"")
	}
	if f.acpCalled {
		t.Error("RunACP was invoked for mode \"stdio\" -- it must not be")
	}
}

// TestRunBlockingTransport_UnrecognizedModeFallsThrough proves "http", "both",
// empty, and any other unrecognized mode are left UNHANDLED here -- main()'s
// switch must fall through to the shared-HTTP-listener branch for those,
// exactly as it did before this change. A regression that swallowed one of
// these into the blocking-transport branch would silently break the "both"
// and default HTTP-serving modes.
func TestRunBlockingTransport_UnrecognizedModeFallsThrough(t *testing.T) {
	for _, mode := range []string{"http", "both", "", "bogus"} {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			f := &fakeMCPRunner{}

			handled, err := runBlockingTransport(mode, f)

			if handled {
				t.Errorf("runBlockingTransport(%q, ...): handled = true, want false (must fall through to the HTTP-serving branch)", mode)
			}
			if err != nil {
				t.Errorf("runBlockingTransport(%q, ...): unexpected error: %v", mode, err)
			}
			if f.stdioCalled || f.acpCalled {
				t.Errorf("runBlockingTransport(%q, ...): a runner method was invoked, want none", mode)
			}
		})
	}
}

// TestRunBlockingTransport_PropagatesRunnerError proves a real runner error
// (e.g. ACPAdapter.Run's read error) is returned to the caller unmodified,
// not swallowed -- main() relies on this to logger.Fatal correctly.
func TestRunBlockingTransport_PropagatesRunnerError(t *testing.T) {
	wantErr := errors.New("boom")
	f := &fakeMCPRunner{acpErr: wantErr}

	_, err := runBlockingTransport("acp", f)

	if !errors.Is(err, wantErr) {
		t.Fatalf("runBlockingTransport error = %v, want %v", err, wantErr)
	}
}

// TestRealMCPServerSatisfiesMcpRunner proves the REAL *mcp.MCPServer (built
// exactly like newTestRouter in security_test.go builds it -- nil *db.Pool,
// no live server needed since RunStdio/RunACP are never actually invoked
// here) satisfies the mcpRunner interface main() depends on. This composes
// with the package-level `var _ mcpRunner = (*mcp.MCPServer)(nil)` compile-time
// assertion in transport.go: that assertion fails the BUILD if RunACP is ever
// renamed or its signature changes; this test additionally proves a real,
// fully-constructed instance can be assigned to the interface at runtime.
func TestRealMCPServerSatisfiesMcpRunner(t *testing.T) {
	var pool *db.Pool // nil: neither RunStdio nor RunACP is invoked in this test
	store := skill.NewStore(pool)
	reg := registry.NewRegistry(pool)
	cfg := &config.Config{}
	mcpServer := mcp.NewMCPServer(pool, store, reg, cfg, zap.NewNop())

	var _ mcpRunner = mcpServer
}
