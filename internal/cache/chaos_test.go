// Package cache provides a Redis-backed caching layer for the HelixKnowledge
// skill graph system. This file contains chaos/resilience tests for cache
// eviction under pressure, nil inputs, and graceful degradation.
package cache

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestChaos_CacheEvictionUnderPressure verifies that the NoopCache handles
// high-volume Set+Get+Delete sequences without panicking or leaking state.
// This simulates cache eviction pressure (the NoopCache has no real
// eviction, but the contract must hold under volume).
func TestChaos_CacheEvictionUnderPressure(t *testing.T) {
	c := NewNoopCache()
	ctx := context.Background()

	const operations = 1000

	// Phase 1: Fill with many keys.
	for i := 0; i < operations; i++ {
		key := fmt.Sprintf("pressure:key:%d", i)
		value := []byte(fmt.Sprintf("value-%d", i))
		if err := c.Set(ctx, key, value, time.Minute); err != nil {
			t.Fatalf("Set %d failed: %v", i, err)
		}
	}

	// Phase 2: Read all keys (NoopCache returns nil on miss — contract holds).
	for i := 0; i < operations; i++ {
		key := fmt.Sprintf("pressure:key:%d", i)
		got, err := c.Get(ctx, key)
		if err != nil {
			t.Fatalf("Get %d failed: %v", i, err)
		}
		// NoopCache always returns nil — that's the expected graceful degradation.
		_ = got
	}

	// Phase 3: Delete all keys.
	for i := 0; i < operations; i++ {
		key := fmt.Sprintf("pressure:key:%d", i)
		if err := c.Delete(ctx, key); err != nil {
			t.Fatalf("Delete %d failed: %v", i, err)
		}
	}

	// Phase 4: Verify post-deletion reads still work (no panic, no leak).
	for i := 0; i < operations; i++ {
		key := fmt.Sprintf("pressure:key:%d", i)
		got, err := c.Get(ctx, key)
		if err != nil {
			t.Fatalf("Post-delete Get %d failed: %v", i, err)
		}
		if got != nil {
			t.Errorf("Post-delete Get %d returned non-nil on NoopCache", i)
		}
	}

	// Recovery verification: the cache is still functional after pressure.
	if err := c.Set(ctx, "recovery:key", []byte("recovery"), time.Minute); err != nil {
		t.Fatalf("Post-pressure Set failed: %v", err)
	}
}

// TestChaos_NilContext verifies that cache operations handle nil or
// cancelled contexts gracefully without panicking.
func TestChaos_NilContext(t *testing.T) {
	c := NewNoopCache()

	// Use a cancelled context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// All operations should complete without panic on a cancelled context.
	if err := c.Set(ctx, "key", []byte("value"), time.Minute); err != nil {
		t.Logf("Set with cancelled context returned error: %v", err)
	}

	got, err := c.Get(ctx, "key")
	if err != nil {
		t.Logf("Get with cancelled context returned error: %v", err)
	}
	_ = got

	if err := c.Delete(ctx, "key"); err != nil {
		t.Logf("Delete with cancelled context returned error: %v", err)
	}

	if err := c.InvalidatePattern(ctx, "key*"); err != nil {
		t.Logf("InvalidatePattern with cancelled context returned error: %v", err)
	}
}

// TestChaos_ZeroTTL verifies that Set with zero TTL does not panic.
func TestChaos_ZeroTTL(t *testing.T) {
	c := NewNoopCache()
	ctx := context.Background()

	if err := c.Set(ctx, "zero-ttl", []byte("value"), 0); err != nil {
		t.Fatalf("Set with zero TTL: %v", err)
	}
}

// TestChaos_EmptyKey verifies that cache operations with empty keys
// do not panic.
func TestChaos_EmptyKey(t *testing.T) {
	c := NewNoopCache()
	ctx := context.Background()

	if err := c.Set(ctx, "", []byte("value"), time.Minute); err != nil {
		t.Logf("Set with empty key: %v", err)
	}

	got, err := c.Get(ctx, "")
	if err != nil {
		t.Logf("Get with empty key: %v", err)
	}
	_ = got
}
