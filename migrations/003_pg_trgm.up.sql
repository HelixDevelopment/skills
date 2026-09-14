-- 003_pg_trgm.up.sql — P1.T1 N6 remediation: pg_trgm extension for Store.Search.
--
-- Store.Search (internal/skill/store.go) has always depended on the pg_trgm
-- trigram similarity operator (`s.name % $1`, `s.title % $1`) and the
-- similarity() function to rank fuzzy skill-name/title matches, but no prior
-- migration ever issued `CREATE EXTENSION pg_trgm` -- so on ANY freshly
-- migrated database, Search's primary query fails with
-- `function similarity(text, unknown) does not exist (SQLSTATE 42883)`
-- before it ever reaches the rest of its SELECT list. Confirmed live (RED
-- baseline, pre-fix) via TestP1T1N6_KindAwareReadPathsWorkLive/Search
-- (internal/skill/kind_read_paths_granularity_test.go) against a clean
-- throwaway database migrated with only 001+002: the exact same error text,
-- exact same SQLSTATE 42883.
--
-- pg_trgm ships in PostgreSQL's contrib set and is present in the
-- pgvector/pgvector:pg16 image used by this project's test container
-- (verified live: `SELECT name FROM pg_available_extensions WHERE
-- name='pg_trgm'` returns one row), so this is a same-transaction,
-- additive, zero-data-loss fix -- no existing table/column/row is touched.
-- Appended as a NEW migration per §11.4.9/§11.4.121: 001 and 002 are already
-- applied in every existing environment and are NOT edited.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- GIN trigram indexes on the two columns Search's `%` operator queries
-- (s.name % $1, s.title % $1) so the similarity search can use an index
-- scan instead of a full sequential scan + per-row trigram computation.
CREATE INDEX IF NOT EXISTS idx_skills_name_trgm ON skills USING GIN (name gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_skills_title_trgm ON skills USING GIN (title gin_trgm_ops);
