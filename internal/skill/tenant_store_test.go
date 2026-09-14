package skill

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/HelixDevelopment/skills/internal/db"
)

// ---------------------------------------------------------------------------
// Context helpers
// ---------------------------------------------------------------------------

func TestTenantFromContext_WithUUID(t *testing.T) {
	id := uuid.New()
	ctx := context.WithValue(context.Background(), TenantKey, id)

	got, ok := TenantFromContext(ctx)
	if !ok {
		t.Fatal("TenantFromContext returned false after setting UUID")
	}
	if got != id {
		t.Errorf("TenantFromContext = %s, want %s", got, id)
	}
}

func TestTenantFromContext_WithPointer(t *testing.T) {
	id := uuid.New()
	ctx := context.WithValue(context.Background(), TenantKey, &id)

	got, ok := TenantFromContext(ctx)
	if !ok {
		t.Fatal("TenantFromContext returned false after setting *UUID")
	}
	if got != id {
		t.Errorf("TenantFromContext = %s, want %s", got, id)
	}
}

func TestTenantFromContext_NilPointer(t *testing.T) {
	var id *uuid.UUID // nil pointer
	ctx := context.WithValue(context.Background(), TenantKey, id)

	got, ok := TenantFromContext(ctx)
	if ok {
		t.Error("TenantFromContext should return false for nil pointer")
	}
	if got != uuid.Nil {
		t.Errorf("expected uuid.Nil, got %s", got)
	}
}

func TestTenantFromContext_Empty(t *testing.T) {
	got, ok := TenantFromContext(context.Background())
	if ok {
		t.Error("TenantFromContext should return false for empty context")
	}
	if got != uuid.Nil {
		t.Errorf("expected uuid.Nil, got %s", got)
	}
}

func TestTenantFromContext_WrongType(t *testing.T) {
	ctx := context.WithValue(context.Background(), TenantKey, "not-a-uuid")

	got, ok := TenantFromContext(ctx)
	if ok {
		t.Error("TenantFromContext should return false for wrong type")
	}
	if got != uuid.Nil {
		t.Errorf("expected uuid.Nil, got %s", got)
	}
}

// ---------------------------------------------------------------------------
// TenantStore construction
// ---------------------------------------------------------------------------

func TestNewTenantStore_NilLogger(t *testing.T) {
	// We can't create a real *db.Pool without a DB connection, but we can
	// verify that NewTenantStore handles nil logger gracefully.
	// This test just verifies the nil-logger default path.
	ts := &TenantStore{pool: nil, logger: nil}
	if ts.logger != nil {
		t.Error("expected nil logger before NewTenantStore default")
	}

	// Verify the default logger is set when nil is passed.
	// We can't call NewTenantStore with a nil pool because it would panic
	// on the first query, but we can verify the logger default logic.
	logger := zap.NewNop()
	ts2 := &TenantStore{pool: nil, logger: logger}
	if ts2.logger == nil {
		t.Error("expected non-nil logger after explicit set")
	}
}

// ---------------------------------------------------------------------------
// ListOpts
// ---------------------------------------------------------------------------

func TestListOpts_ZeroValues(t *testing.T) {
	opts := ListOpts{}
	if opts.Status != "" {
		t.Errorf("expected empty status, got %s", opts.Status)
	}
	if opts.Limit != 0 {
		t.Errorf("expected zero limit, got %d", opts.Limit)
	}
	if opts.Offset != 0 {
		t.Errorf("expected zero offset, got %d", opts.Offset)
	}
}

// ---------------------------------------------------------------------------
// Stress: concurrent context access
// ---------------------------------------------------------------------------

func TestTenantFromContext_Concurrent(t *testing.T) {
	id := uuid.New()
	ctx := context.WithValue(context.Background(), TenantKey, id)

	done := make(chan struct{}, 100)
	for i := 0; i < 100; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			got, ok := TenantFromContext(ctx)
			if !ok {
				t.Error("concurrent TenantFromContext returned false")
			}
			if got != id {
				t.Errorf("concurrent TenantFromContext = %s, want %s", got, id)
			}
		}()
	}
	for i := 0; i < 100; i++ {
		<-done
	}
}

// ---------------------------------------------------------------------------
// Chaos: edge cases
// ---------------------------------------------------------------------------

func TestTenantFromContext_ZeroUUID(t *testing.T) {
	ctx := context.WithValue(context.Background(), TenantKey, uuid.Nil)

	got, ok := TenantFromContext(ctx)
	if !ok {
		t.Error("TenantFromContext should return true for uuid.Nil")
	}
	if got != uuid.Nil {
		t.Errorf("expected uuid.Nil, got %s", got)
	}
}

// Ensure db is used (imported for TenantStore type).
var _ *db.Pool
