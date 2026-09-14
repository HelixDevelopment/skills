-- 003_pg_trgm.down.sql — inverse of 003_pg_trgm.up.sql.
--
-- Drops the trigram GIN indexes BEFORE the extension that defines the
-- gin_trgm_ops operator class they depend on, so the DROP EXTENSION never
-- needs CASCADE and never risks taking anything else down with it.
DROP INDEX IF EXISTS idx_skills_title_trgm;
DROP INDEX IF EXISTS idx_skills_name_trgm;
DROP EXTENSION IF EXISTS pg_trgm;
