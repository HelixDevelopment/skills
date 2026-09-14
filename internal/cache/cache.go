// Package cache provides a Redis-backed caching layer for the HelixKnowledge
// skill graph system, with graceful degradation to a NoopCache when Redis is
// unavailable or not configured.
//
// Usage:
//
//	c, err := cache.New(cfg.Cache)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer c.Close()
//
//	// Cache a skill.
//	err = c.Set(ctx, cache.SkillKey("docker.container"), skillJSON, cfg.Cache.SkillTTL)
//
//	// Retrieve a cached skill.
//	data, err := c.Get(ctx, cache.SkillKey("docker.container"))
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/HelixDevelopment/skills/internal/config"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Interface
// ---------------------------------------------------------------------------

// Cache defines the caching contract. All implementations must be safe for
// concurrent use.
type Cache interface {
	// Get retrieves a cached value by key. Returns (nil, nil) on cache miss.
	Get(ctx context.Context, key string) ([]byte, error)

	// Set stores a value with the given TTL. A zero TTL means no expiry.
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error

	// Delete removes a single key from the cache.
	Delete(ctx context.Context, key string) error

	// InvalidatePattern removes all keys matching the given pattern.
	// Pattern uses Redis glob syntax (e.g. "skill:*").
	InvalidatePattern(ctx context.Context, pattern string) error

	// Close releases underlying resources.
	Close() error
}

// ---------------------------------------------------------------------------
// Cache key helpers
// ---------------------------------------------------------------------------

// SkillKey returns the cache key for a skill by name.
func SkillKey(name string) string {
	return "skill:" + name
}

// SearchKey returns the cache key for a search query. The query string is
// hashed to keep key length bounded.
func SearchKey(query string) string {
	h := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(query))))
	return "search:" + hex.EncodeToString(h[:16])
}

// TreeKey returns the cache key for a dependency tree rooted at the given
// skill name.
func TreeKey(name string) string {
	return "tree:" + name
}

// EmbeddingKey returns the cache key for a skill's embedding vector.
func EmbeddingKey(skillID string) string {
	return "emb:" + skillID
}

// ---------------------------------------------------------------------------
// RedisCache
// ---------------------------------------------------------------------------

// RedisCache implements Cache using a Redis backend.
type RedisCache struct {
	client *redis.Client
	logger *zap.Logger
}

// compile-time interface check.
var _ Cache = (*RedisCache)(nil)

// NewRedisCache creates a RedisCache from the given configuration.
// The Redis connection is pinged before returning.
func NewRedisCache(cfg config.CacheConfig) (*RedisCache, error) {
	opts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis URL: %w", err)
	}

	client := redis.NewClient(opts)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	return &RedisCache{
		client: client,
		logger: zap.L().With(zap.String("component", "cache")),
	}, nil
}

// Get retrieves a cached value. Returns (nil, nil) on miss.
func (c *RedisCache) Get(ctx context.Context, key string) ([]byte, error) {
	val, err := c.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil // cache miss
	}
	if err != nil {
		return nil, fmt.Errorf("redis GET %q: %w", key, err)
	}
	return val, nil
}

// Set stores a value with the given TTL.
func (c *RedisCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := c.client.Set(ctx, key, value, ttl).Err(); err != nil {
		return fmt.Errorf("redis SET %q: %w", key, err)
	}
	return nil
}

// Delete removes a single key.
func (c *RedisCache) Delete(ctx context.Context, key string) error {
	if err := c.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("redis DEL %q: %w", key, err)
	}
	return nil
}

// InvalidatePattern removes all keys matching the pattern using SCAN + DEL.
// This is safe for production use — it does not block the Redis server.
func (c *RedisCache) InvalidatePattern(ctx context.Context, pattern string) error {
	var cursor uint64
	var totalDeleted int64

	for {
		keys, nextCursor, err := c.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return fmt.Errorf("redis SCAN %q: %w", pattern, err)
		}

		if len(keys) > 0 {
			deleted, err := c.client.Del(ctx, keys...).Result()
			if err != nil {
				return fmt.Errorf("redis DEL batch: %w", err)
			}
			totalDeleted += deleted
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	c.logger.Debug("invalidated cache pattern",
		zap.String("pattern", pattern),
		zap.Int64("deleted", totalDeleted))

	return nil
}

// Close shuts down the Redis connection.
func (c *RedisCache) Close() error {
	return c.client.Close()
}

// GetJSON is a convenience method that unmarshals a cached JSON value into
// the provided destination. Returns (false, nil) on cache miss.
func (c *RedisCache) GetJSON(ctx context.Context, key string, dest any) (bool, error) {
	data, err := c.Get(ctx, key)
	if err != nil {
		return false, err
	}
	if data == nil {
		return false, nil
	}
	if err := json.Unmarshal(data, dest); err != nil {
		return false, fmt.Errorf("unmarshal cached value for %q: %w", key, err)
	}
	return true, nil
}

// SetJSON is a convenience method that marshals a value to JSON and caches it.
func (c *RedisCache) SetJSON(ctx context.Context, key string, value any, ttl time.Duration) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal value for cache %q: %w", key, err)
	}
	return c.Set(ctx, key, data, ttl)
}

// ---------------------------------------------------------------------------
// NoopCache
// ---------------------------------------------------------------------------

// NoopCache is a cache implementation that does nothing. It is used when
// Redis is unavailable or not configured, ensuring the system degrades
// gracefully without cache.
type NoopCache struct{}

// compile-time interface check.
var _ Cache = (*NoopCache)(nil)

// NewNoopCache returns a NoopCache. It never errors.
func NewNoopCache() *NoopCache {
	return &NoopCache{}
}

// Get always returns a cache miss.
func (c *NoopCache) Get(_ context.Context, _ string) ([]byte, error) {
	return nil, nil
}

// Set is a no-op.
func (c *NoopCache) Set(_ context.Context, _ string, _ []byte, _ time.Duration) error {
	return nil
}

// Delete is a no-op.
func (c *NoopCache) Delete(_ context.Context, _ string) error {
	return nil
}

// InvalidatePattern is a no-op.
func (c *NoopCache) InvalidatePattern(_ context.Context, _ string) error {
	return nil
}

// Close is a no-op.
func (c *NoopCache) Close() error {
	return nil
}

// ---------------------------------------------------------------------------
// Factory
// ---------------------------------------------------------------------------

// New creates the appropriate Cache implementation based on configuration.
// When cfg.Enabled is true and cfg.RedisURL is non-empty, a RedisCache is
// created. Otherwise a NoopCache is returned.
//
// If the Redis connection fails, New logs a warning and falls back to
// NoopCache rather than returning an error — this is deliberate graceful
// degradation.
func New(cfg config.CacheConfig) (Cache, error) {
	if !cfg.Enabled || cfg.RedisURL == "" {
		zap.L().Info("cache disabled (no Redis configured or cache.enabled=false)")
		return NewNoopCache(), nil
	}

	rc, err := NewRedisCache(cfg)
	if err != nil {
		zap.L().Warn("failed to connect to Redis, falling back to no-op cache",
			zap.Error(err))
		return NewNoopCache(), nil
	}

	zap.L().Info("Redis cache enabled",
		zap.Duration("skill_ttl", cfg.SkillTTL),
		zap.Duration("search_ttl", cfg.SearchTTL),
		zap.Duration("tree_ttl", cfg.TreeTTL))

	return rc, nil
}
