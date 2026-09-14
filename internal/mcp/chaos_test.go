package mcp

import (
	"testing"

	"github.com/HelixDevelopment/skills/internal/config"
	"go.uber.org/zap"
)

// TestChaos_NilDependencies_PanicSafety verifies that NewMCPServer with nil
// pool, store, and registry does not panic.
func TestChaos_NilDependencies_PanicSafety(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NewMCPServer(nil,nil,nil,...) panicked: %v", r)
		}
	}()
	cfg := &config.Config{}
	logger := zap.NewNop()
	s := NewMCPServer(nil, nil, nil, cfg, logger)
	if s == nil {
		t.Error("expected non-nil MCPServer")
	}
}
