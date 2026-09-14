// Package db provides PostgreSQL connectivity, migration management,
// vector search helpers, audit logging, and embedding providers for the
// HelixKnowledge skill graph system.
//
// All functions accept context.Context as the first parameter and use
// zap for structured logging. Errors are wrapped with descriptive messages.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/HelixDevelopment/skills/internal/config"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	pgvec "github.com/pgvector/pgvector-go/pgx"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Pool wrapper
// ---------------------------------------------------------------------------

// Pool wraps *pgxpool.Pool with pgvector type support and convenience
// methods for the skill graph application.
type Pool struct {
	inner *pgxpool.Pool
	cfg   config.DatabaseConfig
}

// Inner returns the underlying *pgxpool.Pool for advanced use cases.
func (p *Pool) Inner() *pgxpool.Pool { return p.inner }

// ---------------------------------------------------------------------------
// Connection lifecycle
// ---------------------------------------------------------------------------

// New creates a new PostgreSQL connection pool from the provided configuration.
// It registers the pgvector type with pgx and verifies connectivity with a
// ping before returning.
//
// The caller is responsible for calling Close() when the pool is no longer
// needed.
func New(cfg config.DatabaseConfig) (*Pool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.ConnectTimeout)
	defer cancel()

	pgxCfg, err := pgxpool.ParseConfig(cfg.DSNWithTimeout())
	if err != nil {
		return nil, fmt.Errorf("parse database DSN: %w", err)
	}

	pgxCfg.MaxConns = int32(cfg.MaxConnections)
	pgxCfg.MinConns = max(1, int32(cfg.MaxConnections)/4)
	pgxCfg.MaxConnLifetime = time.Hour
	pgxCfg.MaxConnIdleTime = 30 * time.Minute
	pgxCfg.HealthCheckPeriod = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, pgxCfg)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}

	// Verify connectivity.
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	// Register pgvector types.
	if err := registerVectorTypes(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("register pgvector types: %w", err)
	}

	return &Pool{inner: pool, cfg: cfg}, nil
}

// Health performs a connectivity check on the pool.
// Returns nil if the database is reachable, otherwise an error.
func (p *Pool) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := p.inner.Ping(ctx); err != nil {
		return fmt.Errorf("database health check failed: %w", err)
	}
	return nil
}

// Stats returns current pool statistics for observability.
func (p *Pool) Stats() pgxpool.Stat {
	return *p.inner.Stat()
}

// Close gracefully shuts down the connection pool.
func (p *Pool) Close() {
	if p.inner != nil {
		p.inner.Close()
	}
}

// ---------------------------------------------------------------------------
// pgvector type registration (pgx native)
// ---------------------------------------------------------------------------

// registerVectorTypes registers the vector type with the pgx connection
// so that pgvector.Vector values can be sent and received transparently.
func registerVectorTypes(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for type registration: %w", err)
	}
	defer conn.Release()

	// Use pgvector-go's native pgx registration (pgx/v5 subpackage).
	if err := pgvec.RegisterTypes(ctx, conn.Conn()); err != nil {
		return fmt.Errorf("pgvector RegisterTypes: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// database/sql compatibility layer
// ---------------------------------------------------------------------------

// StdlibDB returns a *sql.DB backed by the same pgx pool. This is useful
// when a library requires the standard database/sql interface (e.g.
// golang-migrate).
//
// The returned *sql.DB shares the same underlying pool. Callers should NOT
// close it independently; Close the Pool instead.
func (p *Pool) StdlibDB() *sql.DB {
	return stdlib.OpenDBFromPool(p.inner)
}

// ---------------------------------------------------------------------------
// Transaction helpers
// ---------------------------------------------------------------------------

// TxFn is a function executed inside a database transaction.
type TxFn func(tx pgx.Tx) error

// WithTx acquires a connection, begins a transaction, executes fn, and
// commits on success or rolls back on error.
func (p *Pool) WithTx(ctx context.Context, fn TxFn) error {
	conn, err := p.inner.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for transaction: %w", err)
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			zap.L().Warn("transaction rollback failed", zap.Error(rbErr))
		}
		return err // return original error
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Query helpers
// ---------------------------------------------------------------------------

// QueryRow is a thin wrapper around pgxpool.Pool.QueryRow.
func (p *Pool) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return p.inner.QueryRow(ctx, sql, args...)
}

// Query is a thin wrapper around pgxpool.Pool.Query.
func (p *Pool) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return p.inner.Query(ctx, sql, args...)
}

// Exec is a thin wrapper around pgxpool.Pool.Exec. It returns the command
// tag (which carries the number of rows affected) alongside any error,
// mirroring the pgx-native signature so callers can inspect RowsAffected.
func (p *Pool) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return p.inner.Exec(ctx, sql, args...)
}
