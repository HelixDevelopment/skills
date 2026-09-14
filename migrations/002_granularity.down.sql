-- 002_granularity.down.sql — inverse of 002. FAIL-CLOSED: if the granularity
-- feature was used (a >1-edge-per-pair, or a composes/related_to/alternative_to
-- row), the narrowing steps ERROR and the whole tx rolls back — never a silent
-- partial drop. Safe pre-use rollback tool; a down AFTER real feature use is a
-- documented destructive rollback (see research/p1t1_granularity_schema_migration.md §3.4).
--
-- W1 fix (Fable code-review remediation, P1.T1): the ORIGINAL guards below
-- (the relation_type CHECK narrowing + the PK narrowing) only fail closed on
-- TWO of the FOUR ways this feature's data can be incompatible with the 001
-- shape: a bogus relation_type value, and >1 edge per (skill_id, depends_on)
-- pair. They do NOT catch (a) an edge using only a LEGACY relation_type
-- (requires/extends/recommends) -- so it passes the narrowed CHECK -- that
-- ALSO has optional=TRUE or a non-NULL sort_order set, with only ONE edge for
-- its pair -- so it passes the PK-narrowing too -- which the unconditional
-- `DROP COLUMN optional, DROP COLUMN sort_order` below then silently
-- destroys; nor (b) a skills row with kind <> 'atomic' that happens to have
-- ZERO outgoing composes edges (kind is a column on `skills`, entirely
-- independent of skill_dependencies, so no dependency-shaped guard ever sees
-- it), which the unconditional `DROP COLUMN kind` below then silently
-- destroys. Both are real, DB-reachable states (see
-- internal/db/migrations_granularity_test.go's M9-class fail-closed suite,
-- extended with cases exercising exactly these two gaps) that a down BEFORE
-- this fix would narrow successfully, silently losing data instead of
-- refusing. The two DO blocks below close both gaps explicitly, BEFORE any
-- narrowing step runs, so the whole transaction aborts (§9.2 zero-risk) the
-- instant either condition is detected.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM skill_dependencies WHERE optional OR sort_order IS NOT NULL
    ) THEN
        RAISE EXCEPTION 'migration 002 down refused: skill_dependencies has a row with optional=true or sort_order set (edge-attribute data); dropping the optional/sort_order columns would silently lose it. See research/p1t1_granularity_schema_migration.md §3.4.';
    END IF;

    IF EXISTS (
        SELECT 1 FROM skills WHERE kind <> 'atomic'
    ) THEN
        RAISE EXCEPTION 'migration 002 down refused: skills has a row with kind <> ''atomic'' (composite/umbrella); dropping the kind column would silently lose it. See research/p1t1_granularity_schema_migration.md §3.4.';
    END IF;
END $$;

-- inverse (4): narrow PK back to the pair — ERRORS if any pair now has >1 edge.
ALTER TABLE skill_dependencies DROP CONSTRAINT skill_dependencies_pkey;
ALTER TABLE skill_dependencies
    ADD CONSTRAINT skill_dependencies_pkey PRIMARY KEY (skill_id, depends_on);
ALTER TABLE skill_dependencies ALTER COLUMN relation_type DROP NOT NULL;   -- restore 001 nullable-with-default

-- inverse (3): drop edge attributes.
ALTER TABLE skill_dependencies
    DROP COLUMN IF EXISTS sort_order,
    DROP COLUMN IF EXISTS optional;

-- inverse (2): narrow the CHECK back to the 001 3-value set — ERRORS (rolls back
--   the whole tx) if any composes/related_to/alternative_to row exists.
ALTER TABLE skill_dependencies
    DROP CONSTRAINT IF EXISTS skill_dependencies_relation_type_check;
ALTER TABLE skill_dependencies
    ADD CONSTRAINT skill_dependencies_relation_type_check
    CHECK (relation_type IN ('requires', 'extends', 'recommends'));

-- inverse (1): drop skills.kind (+ index).
DROP INDEX IF EXISTS idx_skills_kind;
ALTER TABLE skills DROP COLUMN IF EXISTS kind;
