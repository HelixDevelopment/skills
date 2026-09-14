// Tenant context middleware for the HelixKnowledge skill graph system.
//
// Extracts tenant identification from incoming HTTP requests and propagates
// it via context.Context so that every downstream handler, database query,
// and audit log can operate within the correct tenant scope.
//
// Tenant resolution order (first match wins):
//
//  1. X-Tenant-ID header — explicit tenant selection (highest priority).
//  2. API key → tenant mapping — implicit tenant from the authenticated key.
//  3. Default tenant — fallback when cfg.DefaultTenant is set.
//  4. Reject — 403 when cfg.Required is true and no tenant could be resolved.
//
// The middleware stores a TenantContext in both gin.Context and the standard
// request context so that code outside the Gin handler chain (background jobs,
// database hooks) can still call TenantFromContext(ctx).
//
// §11.4.84 Tenant context extraction middleware.
package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/HelixDevelopment/skills/internal/config"
	"github.com/HelixDevelopment/skills/internal/db"
)

// ---------------------------------------------------------------------------
// Context key and type
// ---------------------------------------------------------------------------

// tenantContextKey is the type-safe context key for TenantContext values.
// Reuses the unexported contextKey type declared in response.go.
const tenantContextKey contextKey = "tenant"

// TenantContext carries the resolved tenant identity for the current request.
// Handlers must treat TenantID as authoritative for all data-access scoping;
// TenantName is informational (convenience for logging and audit).
type TenantContext struct {
	// TenantID is the UUID primary key from the tenants table (004_enterprise).
	TenantID uuid.UUID
	// TenantName is the human-readable tenant name, populated when the tenant
	// is loaded from the database. May be empty if only the header UUID was
	// provided without a DB lookup.
	TenantName string
}

// ---------------------------------------------------------------------------
// Context helpers
// ---------------------------------------------------------------------------

// WithTenant returns a child context that carries the given TenantContext.
// Use this to propagate tenant identity into goroutines, background jobs,
// or any code path that runs outside the Gin handler chain.
func WithTenant(ctx context.Context, tc *TenantContext) context.Context {
	return context.WithValue(ctx, tenantContextKey, tc)
}

// TenantFromContext extracts the TenantContext stored in ctx. Returns nil when
// no tenant has been set (e.g. in unauthenticated code paths or tests).
func TenantFromContext(ctx context.Context) *TenantContext {
	tc, _ := ctx.Value(tenantContextKey).(*TenantContext)
	return tc
}

// TenantIDFromContext is a convenience accessor that returns the tenant UUID
// from ctx. Returns uuid.Nil when no tenant is present, allowing callers to
// test with `id == uuid.Nil` instead of a nil-pointer check.
func TenantIDFromContext(ctx context.Context) uuid.UUID {
	if tc := TenantFromContext(ctx); tc != nil {
		return tc.TenantID
	}
	return uuid.Nil
}

// ginTenantKey is the gin.Context key used to store the TenantContext alongside
// (and in addition to) the standard context.Context value. Gin handlers can
// retrieve it with c.Get(ginTenantKey) for symmetry with other middleware keys.
const ginTenantKey = "tenant_context"

// TenantFromGinContext retrieves the TenantContext from a gin.Context.
// Returns nil when no tenant has been resolved for the request.
func TenantFromGinContext(c *gin.Context) *TenantContext {
	v, exists := c.Get(ginTenantKey)
	if !exists {
		return nil
	}
	tc, _ := v.(*TenantContext)
	return tc
}

// ---------------------------------------------------------------------------
// TenantMiddleware
// ---------------------------------------------------------------------------

// TenantMiddleware resolves the tenant for each request and stores the result
// in both gin.Context and the standard request context.
//
// Resolution order:
//
//  1. X-Tenant-ID header — an explicit UUID selects the tenant directly.
//  2. API key → tenant mapping — looks up the tenant associated with the
//     authenticated API key (requires APIKeyAuth to have run first).
//  3. cfg.DefaultTenant — when set, used as the fallback tenant UUID.
//  4. If cfg.Required is true and no tenant was resolved, the request is
//     aborted with 403 Forbidden.
//
// When a tenant UUID is resolved (from any source), the middleware queries the
// tenants table to populate TenantName. A missing row is treated as an invalid
// tenant — the request is aborted with 403.
//
// §11.4.84 Tenant context extraction.
func TenantMiddleware(cfg config.TenantConfig) gin.HandlerFunc {
	// Pre-parse the default tenant UUID once at startup.
	var defaultTenantID uuid.UUID
	if cfg.DefaultTenant != "" {
		id, err := uuid.Parse(cfg.DefaultTenant)
		if err != nil {
			zap.L().Fatal("invalid tenant.default_tenant config value",
				zap.String("value", cfg.DefaultTenant),
				zap.Error(err),
			)
		}
		defaultTenantID = id
	}

	// Pre-build the API key → tenant map (populated from config).
	// Keys are API key strings; values are tenant UUIDs.
	var apiKeyTenantMap map[string]uuid.UUID
	if len(cfg.APIKeyTenants) > 0 {
		apiKeyTenantMap = make(map[string]uuid.UUID, len(cfg.APIKeyTenants))
		for apiKey, tenantStr := range cfg.APIKeyTenants {
			tid, err := uuid.Parse(tenantStr)
			if err != nil {
				zap.L().Fatal("invalid tenant.api_key_tenant mapping",
					zap.String("api_key", apiKey),
					zap.String("tenant_id", tenantStr),
					zap.Error(err),
				)
			}
			apiKeyTenantMap[apiKey] = tid
		}
	}

	return func(c *gin.Context) {
		var tenantID uuid.UUID

		// --- Step 1: X-Tenant-ID header (explicit selection) ---
		if hdr := c.GetHeader("X-Tenant-ID"); hdr != "" {
			id, err := uuid.Parse(hdr)
			if err != nil {
				RespondErrorWithCode(c, http.StatusForbidden, "invalid_tenant_id",
					"The X-Tenant-ID header contains an invalid UUID.")
				c.Abort()
				return
			}
			tenantID = id
		}

		// --- Step 2: API key → tenant mapping ---
		if tenantID == uuid.Nil && apiKeyTenantMap != nil {
			apiKey := c.GetHeader("X-API-Key")
			if apiKey != "" {
				if tid, ok := apiKeyTenantMap[apiKey]; ok {
					tenantID = tid
				}
			}
		}

		// --- Step 3: Default tenant fallback ---
		if tenantID == uuid.Nil && defaultTenantID != uuid.Nil {
			tenantID = defaultTenantID
		}

		// --- Step 4: No tenant resolved — reject or pass through ---
		if tenantID == uuid.Nil {
			if cfg.Required {
				RespondErrorWithCode(c, http.StatusForbidden, "tenant_required",
					"No tenant could be resolved for this request. "+
						"Provide X-Tenant-ID header or use a tenant-scoped API key.")
				c.Abort()
				return
			}
			// Tenant not required and not resolved — continue without tenant
			// context (single-tenant or unscoped mode).
			c.Next()
			return
		}

		// --- Resolve tenant from database ---
		pool := PoolFromContext(c.Request.Context())
		if pool == nil {
			// No database pool in context — this is a configuration error if
			// tenant middleware is active. Log and abort.
			zap.L().Error("tenant middleware requires a db.Pool in request context",
				zap.String("tenant_id", tenantID.String()),
			)
			RespondError(c, http.StatusInternalServerError,
				"Internal configuration error: database pool not available.")
			c.Abort()
			return
		}

		tc, err := TenantFromDB(pool, tenantID)
		if err != nil {
			zap.L().Warn("tenant lookup failed",
				zap.String("tenant_id", tenantID.String()),
				zap.String("request_id", requestIDFromContext(c)),
				zap.Error(err),
			)
			RespondErrorWithCode(c, http.StatusForbidden, "tenant_not_found",
				"The specified tenant does not exist or is inactive.")
			c.Abort()
			return
		}

		// Store in both gin.Context and the standard request context.
		c.Set(ginTenantKey, tc)
		c.Request = c.Request.WithContext(WithTenant(c.Request.Context(), tc))

		// Attach tenant ID to response headers for debugging / traceability.
		c.Header("X-Tenant-ID", tc.TenantID.String())

		c.Next()
	}
}

// ---------------------------------------------------------------------------
// Database helper
// ---------------------------------------------------------------------------

// tenantQueryTimeout is the maximum time allowed for a single tenant lookup
// query. The value is deliberately short — tenant resolution is a hot path
// on every request and a slow query should fail fast rather than block the
// entire handler chain.
const tenantQueryTimeout = 3 * time.Second

// TenantFromDB loads a tenant record from the tenants table by UUID.
// Returns a populated TenantContext or an error when the tenant is not found.
//
// §11.4.84 Tenant DB lookup.
func TenantFromDB(pool *db.Pool, tenantID uuid.UUID) (*TenantContext, error) {
	ctx, cancel := context.WithTimeout(context.Background(), tenantQueryTimeout)
	defer cancel()

	var tc TenantContext
	err := pool.QueryRow(ctx,
		`SELECT id, name FROM tenants WHERE id = $1`,
		tenantID,
	).Scan(&tc.TenantID, &tc.TenantName)
	if err != nil {
		return nil, fmt.Errorf("query tenant %s: %w", tenantID, err)
	}

	return &tc, nil
}

// ---------------------------------------------------------------------------
// Pool-in-context bridge (used by TenantMiddleware to access the DB pool)
// ---------------------------------------------------------------------------

// dbPoolContextKey is the context key under which the application stores
// the *db.Pool so that middleware can access it without a package-level
// global. The server setup code must call WithDBPool on the request context
// before the tenant middleware runs.
const dbPoolContextKey contextKey = "db_pool"

// WithDBPool stores a *db.Pool in the context. Called by the server setup
// code when constructing the Gin engine.
func WithDBPool(ctx context.Context, pool *db.Pool) context.Context {
	return context.WithValue(ctx, dbPoolContextKey, pool)
}

// PoolFromContext extracts the *db.Pool stored in ctx. Returns nil when no
// pool has been set.
func PoolFromContext(ctx context.Context) *db.Pool {
	pool, _ := ctx.Value(dbPoolContextKey).(*db.Pool)
	return pool
}

// WithDBPoolMiddleware returns a Gin middleware that injects the *db.Pool into
// the request context. This must run BEFORE TenantMiddleware so that tenant
// resolution can query the tenants table.
//
// §11.4.84 DB pool context injection.
func WithDBPoolMiddleware(pool *db.Pool) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request = c.Request.WithContext(WithDBPool(c.Request.Context(), pool))
		c.Next()
	}
}
