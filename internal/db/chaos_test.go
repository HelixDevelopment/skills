package db

import (
	"testing"

	"github.com/HelixDevelopment/skills/internal/config"
)

// TestChaos_UnknownProvider_ReturnsError verifies that NewEmbedderFromConfig
// returns an error for an unknown provider (fail-closed).
func TestChaos_UnknownProvider_ReturnsError(t *testing.T) {
	_, err := NewEmbedderFromConfig(config.EmbeddingConfig{
		Provider:   "nonexistent-provider",
		Dimensions: 1536,
	})
	if err == nil {
		t.Error("expected error for unknown provider, got nil")
	}
}

// TestChaos_EmptyConfig_ReturnsError verifies that NewEmbedderFromConfig
// returns an error for an empty config.
func TestChaos_EmptyConfig_ReturnsError(t *testing.T) {
	_, err := NewEmbedderFromConfig(config.EmbeddingConfig{})
	if err == nil {
		t.Error("expected error for empty config, got nil")
	}
}

// TestChaos_LocalEmbedder_NoEndpoint_ReturnsError verifies that the local
// embedder provider requires an endpoint.
func TestChaos_LocalEmbedder_NoEndpoint_ReturnsError(t *testing.T) {
	_, err := NewEmbedderFromConfig(config.EmbeddingConfig{
		Provider:   "local",
		Dimensions: 768,
		// No LocalEndpoint.
	})
	if err == nil {
		t.Error("expected error for local embedder without endpoint, got nil")
	}
}
