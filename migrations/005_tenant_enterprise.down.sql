-- 005_tenant_enterprise.down.sql — inverse of 005_tenant_enterprise.up.sql.
--
-- Drops the tenant metrics and audit log tables in reverse dependency order
-- so that foreign key constraints are satisfied at every step.

-- (2) Drop tenant_metrics first (no other table depends on it).
DROP INDEX IF EXISTS idx_metrics_tenant_date;
DROP TABLE IF EXISTS tenant_metrics;

-- (1) Drop tenant_audit_log.
DROP INDEX IF EXISTS idx_audit_log_created_brin;
DROP INDEX IF EXISTS idx_audit_log_tenant_created;
DROP TABLE IF EXISTS tenant_audit_log;
