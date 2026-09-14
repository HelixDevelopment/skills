-- Rollback initial migration
DROP TRIGGER IF EXISTS update_skills_updated_at ON skills;
DROP FUNCTION IF EXISTS update_updated_at_column();

DROP INDEX IF EXISTS idx_evidences_embedding;
DROP INDEX IF EXISTS idx_skills_embedding;
DROP INDEX IF EXISTS idx_audit_event;
DROP INDEX IF EXISTS idx_audit_ts;
DROP INDEX IF EXISTS idx_registry_stale;
DROP INDEX IF EXISTS idx_evidences_project;
DROP INDEX IF EXISTS idx_evidences_skill;
DROP INDEX IF EXISTS idx_resources_skill;
DROP INDEX IF EXISTS idx_deps_depends_on;
DROP INDEX IF EXISTS idx_deps_skill;
DROP INDEX IF EXISTS idx_skills_metadata;
DROP INDEX IF EXISTS idx_skills_status;
DROP INDEX IF EXISTS idx_skills_name;

DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS skill_registry;
DROP TABLE IF EXISTS evidences;
DROP TABLE IF EXISTS resources;
DROP TABLE IF EXISTS skill_dependencies;
DROP TABLE IF EXISTS skills;
