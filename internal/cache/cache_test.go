package cache

import (
	"context"
	"testing"
	"time"

	"github.com/HelixDevelopment/skills/internal/config"
)

// ---------------------------------------------------------------------------
// NoopCache tests
// ---------------------------------------------------------------------------

func TestNoopCache_GetReturnsNil(t *testing.T) {
	c := NewNoopCache()
	val, err := c.Get(context.Background(), "any-key")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if val != nil {
		t.Fatalf("expected nil, got %v", val)
	}
}

func TestNoopCache_SetIsNoop(t *testing.T) {
	c := NewNoopCache()
	err := c.Set(context.Background(), "key", []byte("value"), time.Minute)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	// Verify the value was not stored.
	val, err := c.Get(context.Background(), "key")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if val != nil {
		t.Fatalf("expected nil (noop), got %v", val)
	}
}

func TestNoopCache_DeleteIsNoop(t *testing.T) {
	c := NewNoopCache()
	err := c.Delete(context.Background(), "key")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestNoopCache_InvalidatePatternIsNoop(t *testing.T) {
	c := NewNoopCache()
	err := c.InvalidatePattern(context.Background(), "skill:*")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestNoopCache_CloseIsNoop(t *testing.T) {
	c := NewNoopCache()
	err := c.Close()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Cache key generation tests
// ---------------------------------------------------------------------------

func TestSkillKey(t *testing.T) {
	got := SkillKey("docker.container")
	want := "skill:docker.container"
	if got != want {
		t.Errorf("SkillKey(%q) = %q, want %q", "docker.container", got, want)
	}
}

func TestSkillKey_EmptyName(t *testing.T) {
	got := SkillKey("")
	want := "skill:"
	if got != want {
		t.Errorf("SkillKey(\"\") = %q, want %q", got, want)
	}
}

func TestSearchKey(t *testing.T) {
	k1 := SearchKey("docker networking")
	k2 := SearchKey("docker networking")
	k3 := SearchKey("kubernetes pods")

	// Same query should produce same key.
	if k1 != k2 {
		t.Errorf("same query produced different keys: %q != %q", k1, k2)
	}
	// Different queries should produce different keys.
	if k1 == k3 {
		t.Errorf("different queries produced same key: %q", k1)
	}
	// Key should have the expected prefix.
	if len(k1) < 7 || k1[:7] != "search:" {
		t.Errorf("SearchKey prefix = %q, want %q", k1[:7], "search:")
	}
}

func TestSearchKey_CaseInsensitive(t *testing.T) {
	k1 := SearchKey("Docker Networking")
	k2 := SearchKey("docker networking")
	if k1 != k2 {
		t.Errorf("case-insensitive search keys differ: %q != %q", k1, k2)
	}
}

func TestSearchKey_TrimsWhitespace(t *testing.T) {
	k1 := SearchKey("  docker networking  ")
	k2 := SearchKey("docker networking")
	if k1 != k2 {
		t.Errorf("whitespace-trimmed keys differ: %q != %q", k1, k2)
	}
}

func TestTreeKey(t *testing.T) {
	got := TreeKey("go.language")
	want := "tree:go.language"
	if got != want {
		t.Errorf("TreeKey(%q) = %q, want %q", "go.language", got, want)
	}
}

func TestEmbeddingKey(t *testing.T) {
	got := EmbeddingKey("abc-123")
	want := "emb:abc-123"
	if got != want {
		t.Errorf("EmbeddingKey(%q) = %q, want %q", "abc-123", got, want)
	}
}

// ---------------------------------------------------------------------------
// Factory tests
// ---------------------------------------------------------------------------

func TestNew_CacheDisabled(t *testing.T) {
	c, err := New(config.CacheConfig{
		Enabled:   false,
		SkillTTL:  5 * time.Minute,
		SearchTTL: 1 * time.Minute,
		TreeTTL:   10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if _, ok := c.(*NoopCache); !ok {
		t.Fatalf("expected *NoopCache, got %T", c)
	}
}

func TestNew_EmptyRedisURL(t *testing.T) {
	c, err := New(config.CacheConfig{
		Enabled:   true,
		RedisURL:  "",
		SkillTTL:  5 * time.Minute,
		SearchTTL: 1 * time.Minute,
		TreeTTL:   10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if _, ok := c.(*NoopCache); !ok {
		t.Fatalf("expected *NoopCache when RedisURL is empty, got %T", c)
	}
}

func TestNew_InvalidRedisURL(t *testing.T) {
	c, err := New(config.CacheConfig{
		Enabled:   true,
		RedisURL:  "redis://invalid-host:9999",
		SkillTTL:  5 * time.Minute,
		SearchTTL: 1 * time.Minute,
		TreeTTL:   10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("expected no error (graceful degradation), got %v", err)
	}
	if _, ok := c.(*NoopCache); !ok {
		t.Fatalf("expected *NoopCache on connection failure, got %T", c)
	}
}
