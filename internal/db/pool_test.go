package db

import (
	"database/sql"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// PoolStat conversion tests
// ---------------------------------------------------------------------------

func TestConvertDBStats(t *testing.T) {
	stat := convertDBStats(sql.DBStats{
		MaxOpenConnections: 25,
		OpenConnections:    10,
		InUse:              5,
		Idle:               5,
		WaitCount:          42,
		WaitDuration:       3 * time.Second,
		MaxIdleClosed:      7,
		MaxLifetimeClosed:  3,
	})

	if stat.MaxOpenConnections != 25 {
		t.Errorf("MaxOpenConnections = %d, want 25", stat.MaxOpenConnections)
	}
	if stat.OpenConnections != 10 {
		t.Errorf("OpenConnections = %d, want 10", stat.OpenConnections)
	}
	if stat.InUse != 5 {
		t.Errorf("InUse = %d, want 5", stat.InUse)
	}
	if stat.Idle != 5 {
		t.Errorf("Idle = %d, want 5", stat.Idle)
	}
	if stat.WaitCount != 42 {
		t.Errorf("WaitCount = %d, want 42", stat.WaitCount)
	}
	if stat.WaitDuration != 3*time.Second {
		t.Errorf("WaitDuration = %v, want 3s", stat.WaitDuration)
	}
	if stat.MaxIdleClosed != 7 {
		t.Errorf("MaxIdleClosed = %d, want 7", stat.MaxIdleClosed)
	}
	if stat.MaxLifetimeClosed != 3 {
		t.Errorf("MaxLifetimeClosed = %d, want 3", stat.MaxLifetimeClosed)
	}
}

func TestConvertDBStats_ZeroValues(t *testing.T) {
	stat := convertDBStats(sql.DBStats{})

	if stat.MaxOpenConnections != 0 {
		t.Errorf("expected zero MaxOpenConnections, got %d", stat.MaxOpenConnections)
	}
	if stat.OpenConnections != 0 {
		t.Errorf("expected zero OpenConnections, got %d", stat.OpenConnections)
	}
	if stat.InUse != 0 {
		t.Errorf("expected zero InUse, got %d", stat.InUse)
	}
	if stat.Idle != 0 {
		t.Errorf("expected zero Idle, got %d", stat.Idle)
	}
}

// ---------------------------------------------------------------------------
// ReadWritePool routing tests (no real DB — uses nil pool for routing logic)
// ---------------------------------------------------------------------------

func TestReadWritePool_NoReplica_ReadsPrimary(t *testing.T) {
	rwp := &ReadWritePool{
		primary: nil,
		replica: nil,
	}

	// Read() should return primary when replica is nil.
	if got := rwp.Read(); got != rwp.primary {
		t.Errorf("Read() without replica returned %v, want primary %v", got, rwp.primary)
	}

	// Write() always returns primary.
	if got := rwp.Write(); got != rwp.primary {
		t.Errorf("Write() returned %v, want primary %v", got, rwp.primary)
	}
}

func TestReadWritePool_WithHealthyReplica_ReadsReplica(t *testing.T) {
	primary := &sql.DB{}
	replica := &sql.DB{}

	rwp := &ReadWritePool{
		primary: primary,
		replica: replica,
	}
	rwp.replicaHealthy.Store(0) // healthy

	if got := rwp.Read(); got != replica {
		t.Error("Read() with healthy replica should return replica")
	}
	if got := rwp.Write(); got != primary {
		t.Error("Write() should always return primary")
	}
}

func TestReadWritePool_UnhealthyReplica_FallsBackToPrimary(t *testing.T) {
	primary := &sql.DB{}
	replica := &sql.DB{}

	rwp := &ReadWritePool{
		primary: primary,
		replica: replica,
	}
	rwp.replicaHealthy.Store(1) // degraded

	if got := rwp.Read(); got != primary {
		t.Error("Read() with unhealthy replica should fall back to primary")
	}
}

func TestReadWritePool_Stats_NoReplica(t *testing.T) {
	rwp := &ReadWritePool{
		primary: nil,
		replica: nil,
	}

	stats := rwp.Stats()
	if stats.HasReplica {
		t.Error("expected HasReplica=false when no replica configured")
	}
}

func TestReadWritePool_Stats_WithReplica(t *testing.T) {
	rwp := &ReadWritePool{
		primary: nil,
		replica: &sql.DB{},
	}

	stats := rwp.Stats()
	if !stats.HasReplica {
		t.Error("expected HasReplica=true when replica is set")
	}
}
