package mcp

import (
	"sync"
	"testing"

	"github.com/HelixDevelopment/skills/internal/config"
	"go.uber.org/zap"
)

// TestStress_ConcurrentMCPServerConstruction exercises concurrent MCPServer
// construction with nil pool/store/registry. N=100 goroutines, no races.
func TestStress_ConcurrentMCPServerConstruction(t *testing.T) {
	cfg := &config.Config{}
	logger := zap.NewNop()

	const n = 100
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// NewMCPServer with nil pool/store/registry should not panic.
			s := NewMCPServer(nil, nil, nil, cfg, logger)
			if s == nil {
				t.Error("NewMCPServer returned nil")
			}
		}()
	}
	wg.Wait()
}
