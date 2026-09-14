// Read/write pool separation for the HelixKnowledge skill graph system.
//
// ReadWritePool wraps two *sql.DB pools — a primary for writes and an
// optional replica for reads. When no replica is configured, all operations
// are routed to the primary, ensuring the system works identically in
// single-node deployments.
//
// Usage:
//
//	pool, err := db.NewReadWritePool(cfg.Database)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer pool.Close()
//
//	// Writes go to primary.
//	_, err = pool.Write().ExecContext(ctx, "INSERT INTO skills ...")
//
//	// Reads go to replica (or primary if no replica).
//	row := pool.Read().QueryRowContext(ctx, "SELECT ...")
package db

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/HelixDevelopment/skills/internal/config"

	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// PoolStats holds connection-pool statistics for observability.
// ---------------------------------------------------------------------------

// PoolStats reports the state of both the primary and replica pools.
type PoolStats struct {
	Primary    PoolStat `json:"primary"`
	Replica    PoolStat `json:"replica"`
	HasReplica bool     `json:"has_replica"`
}

// PoolStat holds the statistics for a single database pool.
type PoolStat struct {
	MaxOpenConnections int           `json:"max_open_connections"`
	OpenConnections    int           `json:"open_connections"`
	InUse              int           `json:"in_use"`
	Idle               int           `json:"idle"`
	WaitCount          int64         `json:"wait_count"`
	WaitDuration       time.Duration `json:"wait_duration"`
	MaxIdleClosed      int64         `json:"max_idle_closed"`
	MaxLifetimeClosed  int64         `json:"max_lifetime_closed"`
}

// ---------------------------------------------------------------------------
// ReadWritePool
// ---------------------------------------------------------------------------

// ReadWritePool separates read and write traffic across two database
// connection pools. The primary pool handles all write operations and
// serves as fallback when no replica is configured or when the replica
// is unhealthy.
type ReadWritePool struct {
	primary *sql.DB
	replica *sql.DB // nil when no replica configured

	replicaDSN       string
	replicaMaxLagSec int

	// replicaHealthy tracks whether the replica is accepting reads.
	// 0 = healthy, 1 = degraded (reads fall back to primary).
	replicaHealthy atomic.Int32

	logger *zap.Logger
}

// NewReadWritePool creates a ReadWritePool from the provided database
// configuration. The primary pool is always created. If cfg.Replica.DSN
// is non-empty, a second pool is opened for the replica.
//
// Both pools are pinged before returning. If the replica ping fails,
// the system logs a warning and falls back to primary-only mode — it
// never fails closed on a missing replica.
func NewReadWritePool(cfg config.DatabaseConfig) (*ReadWritePool, error) {
	log := zap.L().With(zap.String("component", "db_pool"))

	primary, err := openPool(cfg.DSNWithTimeout(), cfg.MaxConnections)
	if err != nil {
		return nil, fmt.Errorf("open primary pool: %w", err)
	}

	rwp := &ReadWritePool{
		primary:          primary,
		replicaDSN:       cfg.Replica.DSN,
		replicaMaxLagSec: cfg.Replica.MaxLagSeconds,
		logger:           log,
	}

	if cfg.Replica.DSN != "" {
		maxConns := cfg.Replica.MaxConnections
		if maxConns <= 0 {
			maxConns = cfg.MaxConnections
		}

		replica, err := openPool(cfg.Replica.DSN, maxConns)
		if err != nil {
			log.Warn("failed to open replica pool, falling back to primary",
				zap.Error(err))
			// Continue without replica — primary handles all traffic.
		} else {
			rwp.replica = replica
			log.Info("read replica pool opened",
				zap.Int("max_connections", maxConns))
		}
	}

	return rwp, nil
}

// openPool opens a *sql.DB with sensible connection-pool settings.
func openPool(dsn string, maxConns int) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("sql.Open: %w", err)
	}

	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(max(maxConns/4, 1))
	db.SetConnMaxLifetime(1 * time.Hour)
	db.SetConnMaxIdleTime(30 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	return db, nil
}

// Read returns the database connection for read operations. If a replica
// is configured and healthy, reads go to the replica. Otherwise they go
// to the primary.
func (p *ReadWritePool) Read() *sql.DB {
	if p.replica != nil && p.replicaHealthy.Load() == 0 {
		return p.replica
	}
	return p.primary
}

// Write returns the primary database connection for write operations.
// All writes always go to the primary.
func (p *ReadWritePool) Write() *sql.DB {
	return p.primary
}

// Close shuts down both connection pools.
func (p *ReadWritePool) Close() error {
	var firstErr error
	if p.replica != nil {
		if err := p.replica.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close replica pool: %w", err)
		}
	}
	if err := p.primary.Close(); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("close primary pool: %w", err)
	}
	return firstErr
}

// Stats returns connection-pool statistics for both pools. If a pool is
// nil (e.g. in tests), the corresponding stats are zero-valued.
func (p *ReadWritePool) Stats() PoolStats {
	stats := PoolStats{
		HasReplica: p.replica != nil,
	}
	if p.primary != nil {
		stats.Primary = convertDBStats(p.primary.Stats())
	}
	if p.replica != nil {
		stats.Replica = convertDBStats(p.replica.Stats())
	}
	return stats
}

// convertDBStats converts sql.DBStats to the local PoolStat type.
func convertDBStats(s sql.DBStats) PoolStat {
	return PoolStat{
		MaxOpenConnections: s.MaxOpenConnections,
		OpenConnections:    s.OpenConnections,
		InUse:              s.InUse,
		Idle:               s.Idle,
		WaitCount:          s.WaitCount,
		WaitDuration:       s.WaitDuration,
		MaxIdleClosed:      s.MaxIdleClosed,
		MaxLifetimeClosed:  s.MaxLifetimeClosed,
	}
}

// Health checks the primary pool. If a replica is configured, it also
// checks replica health and replication lag. When the replica is
// unhealthy, reads are routed to the primary until the next successful
// health check.
func (p *ReadWritePool) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := p.primary.PingContext(ctx); err != nil {
		return fmt.Errorf("primary health check failed: %w", err)
	}

	if p.replica == nil {
		return nil
	}

	if err := p.replica.PingContext(ctx); err != nil {
		p.replicaHealthy.Store(1)
		p.logger.Warn("replica health check failed, falling back to primary",
			zap.Error(err))
		return nil // not fatal — primary is still up
	}

	// Check replication lag if configured.
	if p.replicaMaxLagSec > 0 {
		lag, err := p.checkReplicaLag(ctx)
		if err != nil {
			p.logger.Warn("replica lag check failed", zap.Error(err))
			p.replicaHealthy.Store(1)
			return nil
		}
		if lag > p.replicaMaxLagSec {
			p.replicaHealthy.Store(1)
			p.logger.Warn("replica lag exceeds threshold, falling back to primary",
				zap.Int("lag_seconds", lag),
				zap.Int("max_lag_seconds", p.replicaMaxLagSec))
			return nil
		}
	}

	p.replicaHealthy.Store(0)
	return nil
}

// checkReplicaLag queries the replica for its replication lag in seconds.
// This uses pg_last_xact_replay_timestamp() which is available on
// streaming-replica standbys.
func (p *ReadWritePool) checkReplicaLag(ctx context.Context) (int, error) {
	var lagSec sql.NullFloat64
	err := p.replica.QueryRowContext(ctx,
		`SELECT EXTRACT(EPOCH FROM (now() - pg_last_xact_replay_timestamp()))::int`,
	).Scan(&lagSec)
	if err != nil {
		return 0, fmt.Errorf("query replica lag: %w", err)
	}
	if !lagSec.Valid {
		// NULL means no replay data — replica may be idle. Treat as zero lag.
		return 0, nil
	}
	return int(lagSec.Float64), nil
}
