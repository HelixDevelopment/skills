-- 004_enterprise.up.sql — Multi-tenant foundation and enterprise scalability.
-- Additive, zero-data-loss superset of 001-003. Runs in ONE transaction.
--
-- Adds:
--   (1) tenants table for multi-tenant management
--   (2) tenant_id columns on all core tables (nullable for backward compat)
--   (3) indexes on tenant_id for query performance
--   (4) commented-out row-level security policies (enable when ready)
--   (5) BRIN indexes for timestamp columns on high-volume tables

-- ---------------------------------------------------------------------------
-- (1) Tenants table
-- ---------------------------------------------------------------------------

CREATE TABLE tenants (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL UNIQUE,
    config     JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_tenants_name ON tenants(name);

-- ---------------------------------------------------------------------------
-- (2) tenant_id columns — NULLABLE for backward compatibility with existing
--     single-tenant data. Backfill with a default tenant before enforcing
--     NOT NULL.
-- ---------------------------------------------------------------------------

ALTER TABLE skills ADD COLUMN tenant_id UUID REFERENCES tenants(id);
ALTER TABLE skill_dependencies ADD COLUMN tenant_id UUID REFERENCES tenants(id);
ALTER TABLE evidences ADD COLUMN tenant_id UUID REFERENCES tenants(id);
ALTER TABLE resources ADD COLUMN tenant_id UUID REFERENCES tenants(id);
ALTER TABLE skill_registry ADD COLUMN tenant_id UUID REFERENCES tenants(id);

-- ---------------------------------------------------------------------------
-- (3) Indexes on tenant_id for efficient tenant-scoped queries
-- ---------------------------------------------------------------------------

CREATE INDEX idx_skills_tenant ON skills(tenant_id);
CREATE INDEX idx_deps_tenant ON skill_dependencies(tenant_id);
CREATE INDEX idx_evidences_tenant ON evidences(tenant_id);
CREATE INDEX idx_resources_tenant ON resources(tenant_id);
CREATE INDEX idx_registry_tenant ON skill_registry(tenant_id);

-- Composite index: tenant + name lookup is the most common query pattern.
CREATE INDEX idx_skills_tenant_name ON skills(tenant_id, name);

-- ---------------------------------------------------------------------------
-- (4) Row-level security policies (COMMENTED OUT — enable when multi-tenant
--     isolation is ready. Requires setting app.tenant_id per connection.)
--
-- Uncomment these after:
--   a) All tenant_id columns are backfilled with a real tenant UUID
--   b) tenant_id columns are altered to NOT NULL
--   c) Application code sets app.tenant_id on each connection
-- ---------------------------------------------------------------------------

-- ALTER TABLE skills ENABLE ROW LEVEL SECURITY;
-- CREATE POLICY tenant_isolation_skills ON skills
--     USING (tenant_id = current_setting('app.tenant_id')::uuid);

-- ALTER TABLE skill_dependencies ENABLE ROW LEVEL SECURITY;
-- CREATE POLICY tenant_isolation_deps ON skill_dependencies
--     USING (tenant_id = current_setting('app.tenant_id')::uuid);

-- ALTER TABLE evidences ENABLE ROW LEVEL SECURITY;
-- CREATE POLICY tenant_isolation_evidences ON evidences
--     USING (tenant_id = current_setting('app.tenant_id')::uuid);

-- ALTER TABLE resources ENABLE ROW LEVEL SECURITY;
-- CREATE POLICY tenant_isolation_resources ON resources
--     USING (tenant_id = current_setting('app.tenant_id')::uuid);

-- ALTER TABLE skill_registry ENABLE ROW LEVEL SECURITY;
-- CREATE POLICY tenant_isolation_registry ON skill_registry
--     USING (tenant_id = current_setting('app.tenant_id')::uuid);

-- ---------------------------------------------------------------------------
-- (5) BRIN indexes for timestamp columns on high-volume tables
-- ---------------------------------------------------------------------------

-- BRIN indexes are tiny and efficient for naturally ordered columns like
-- timestamps. They trade point-query speed for massive space savings on
-- large tables (audit_log, evidences).
CREATE INDEX idx_audit_ts_brin ON audit_log USING BRIN(ts);
CREATE INDEX idx_evidences_created_brin ON evidences USING BRIN(created_at);
