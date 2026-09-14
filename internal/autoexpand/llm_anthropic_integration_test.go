//go:build integration

package autoexpand

import (
	"context"
	"os"
	"testing"

	"go.uber.org/zap"
)

// TestIntegration_AnthropicLLM_Generate_LiveKey performs one real, low-token
// Generate call against api.anthropic.com. It SKIPs with a reason (never a
// silent green, §11.4.3) when ANTHROPIC_API_KEY is unset. Live-provider proof
// is operator-scheduled; the unit tests prove the request/response contract.
func TestIntegration_AnthropicLLM_Generate_LiveKey(t *testing.T) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Skip("ANTHROPIC_API_KEY not set; skipping live Anthropic Generate test (§11.4.3 SKIP-with-reason)")
	}
	client := NewAnthropicLLM(key, "", zap.NewNop())
	out, err := client.Generate(context.Background(), "Reply with the single word: ok", 16)
	if err != nil {
		t.Fatalf("live Anthropic Generate failed: %v", err)
	}
	if out == "" {
		t.Fatalf("live Anthropic Generate returned empty text")
	}
	t.Logf("live Anthropic response: %q", out)
}
