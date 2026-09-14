-- 005_tenant_enterprise.up.sql — Enterprise tenant infrastructure.
-- Additive, zero-data-loss superset of 001-004. Runs in ONE transaction.
--
-- Adds:
--   (1) tenant_audit_log table for per-request audit trail
--   (2) tenant_metrics table for daily aggregated tenant usage
--   (3) Indexes for efficient audit and metrics queries

-- ---------------------------------------------------------------------------
-- (1) Tenant audit log — durable per-request audit trail
-- ---------------------------------------------------------------------------

CREATE TABLE tenant_audit_log (
    id           BIGSERIAL PRIMARY KEY,
    tenant_id    UUID NOT NULL REFERENCES tenants(id),
    action       TEXT NOT NULL,       -- CREATE, READ, UPDATE, DELETE, LIST, UNKNOWN
    resource     TEXT NOT NULL,       -- primary resource segment (e.g. "skills")
    method       TEXT NOT NULL,       -- HTTP method (GET, POST, ...)
    path         TEXT NOT NULL,       -- full request path
    status_code  INT  NOT NULL,       -- HTTP response status code
    request_id   TEXT NOT NULL,       -- request correlation ID from RequestID middleware
    duration_ms  BIGINT NOT NULL,     -- handler wall-clock time in milliseconds
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Hot index: tenant_id + created_at is the primary query pattern for audit
-- log retrieval (tenant dashboard, compliance queries, time-range scans).
CREATE INDEX idx_audit_log_tenant_created
    ON tenant_audit_log(tenant_id, created_at DESC);

-- BRIN index for time-range scans across all tenants (lightweight, naturally
-- ordered by created_at).
CREATE INDEX idx_audit_log_created_brin
    ON tenant_audit_log USING BRIN(created_at);

-- ---------------------------------------------------------------------------
-- (2) Tenant metrics — daily aggregated usage per tenant
-- ---------------------------------------------------------------------------

CREATE TABLE tenant_metrics (
    id               BIGSERIAL PRIMARY KEY,
    tenant_id        UUID NOT NULL REFERENCES tenants(id),
    date             DATE NOT NULL,        -- aggregation day (UTC)
    request_count    BIGINT NOT NULL DEFAULT 0,
    error_count      BIGINT NOT NULL DEFAULT 0,  -- status_code >= 500
    avg_duration_ms  DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    p99_duration_ms  DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    UNIQUE(tenant_id, date)
);

-- Index for dashboard queries: tenant metrics over a date range.
CREATE INDEX idx_metrics_tenant_date
    ON tenant_metrics(tenant_id, date DESC);
