-- 005_performance_indexes.up.sql — Performance audit remediation.
-- Additive, zero-data-loss superset of 001-004. Runs in ONE transaction.
--
-- Adds indexes identified by the performance audit:
--   (1) Composite index on skill_registry for GetMissingSkills/GetCoverage
--   (2) Partial index on skill_registry for missing_deps queries
--   (3) Expression index on skills.metadata->>'domain' for domain-filtered queries
--   (4) Composite index on skill_dependencies for GetDependents (reverse lookup)

-- (1) Composite covering index for registry queries that filter on skill_id
--     and read missing_deps/coverage. GetMissingSkills filters on
--     array_length(missing_deps) and orders by coverage; GetCoverage joins
--     on skill_id and reads coverage/missing_deps.
CREATE INDEX IF NOT EXISTS idx_registry_skill_coverage
    ON skill_registry (skill_id, coverage);

-- (2) Partial index for the common "has missing deps" filter used by
--     GetMissingSkills and GetCoverage. Avoids scanning rows with no
--     missing dependencies.
CREATE INDEX IF NOT EXISTS idx_registry_has_missing_deps
    ON skill_registry (skill_id)
    WHERE array_length(missing_deps, 1) > 0;

-- (3) Expression index on metadata->>'domain' for domain-filtered queries
--     in GetMissingSkills, GetCoverage, and ListSkills-with-domain-filter.
--     The existing GIN index on metadata (001) supports @> containment but
--     is inefficient for the ->>'domain' text extraction pattern.
CREATE INDEX IF NOT EXISTS idx_skills_domain
    ON skills ((metadata->>'domain'))
    WHERE metadata->>'domain' IS NOT NULL;

-- (4) Composite index on skill_dependencies(depends_on, skill_id) to support
--     GetDependents (reverse edge lookup) with an index-only scan when
--     combined with the skills table join.
CREATE INDEX IF NOT EXISTS idx_deps_reverse
    ON skill_dependencies (depends_on, skill_id);
