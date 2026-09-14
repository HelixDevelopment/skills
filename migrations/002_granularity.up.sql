-- 002_granularity.up.sql — P1.T1 Granularity & composition schema (R16).
-- Additive, zero-data-loss superset of 001_initial. Runs in ONE transaction
-- (internal/db/migrations.go runMigrationSQL) — all-or-nothing.
--
-- See research/p1t1_granularity_schema_migration.md §1.1 for the full design
-- grounding (file:line citations against 001_initial.up.sql / SPEC.md) and
-- research/skill_granularity_and_composition.md §5.3/§5.4 for the model.

-- (1) skills.kind — aggregation axis (default keeps every existing row valid;
--     backward-compat with the 8 seed TOMLs, which omit kind → 'atomic').
ALTER TABLE skills
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'atomic'
    CHECK (kind IN ('atomic', 'composite', 'umbrella'));
CREATE INDEX idx_skills_kind ON skills(kind);

-- (2) widen the relation_type CHECK to the full typed-edge set (§4.1).
ALTER TABLE skill_dependencies
    DROP CONSTRAINT IF EXISTS skill_dependencies_relation_type_check;   -- Postgres default name for the inline 001 CHECK
ALTER TABLE skill_dependencies
    ADD CONSTRAINT skill_dependencies_relation_type_check
    CHECK (relation_type IN (
        'requires', 'extends', 'recommends',        -- existing (001)
        'composes', 'related_to', 'alternative_to'  -- NEW (R16 §4.1)
    ));

-- (3) edge attributes for umbrella→component ordering/optionality (§5.3).
ALTER TABLE skill_dependencies
    ADD COLUMN optional   BOOLEAN NOT NULL DEFAULT FALSE,   -- default keeps 001 rows valid
    ADD COLUMN sort_order INT;                              -- nullable; NULL = unordered

-- (4) widen the edge PK so a (skill_id, depends_on) pair may carry >1 typed edge (§5.3 :258).
--     relation_type must be NON-NULL to join a PK; 001 left it nullable-with-default.
UPDATE skill_dependencies SET relation_type = 'requires' WHERE relation_type IS NULL;  -- defensive backfill (seed has none)
ALTER TABLE skill_dependencies ALTER COLUMN relation_type SET NOT NULL;
ALTER TABLE skill_dependencies DROP CONSTRAINT skill_dependencies_pkey;                 -- Postgres default PK name
ALTER TABLE skill_dependencies
    ADD CONSTRAINT skill_dependencies_pkey
    PRIMARY KEY (skill_id, depends_on, relation_type);
