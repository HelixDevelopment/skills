-- Enable required extensions
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- Skills table: core knowledge units
CREATE TABLE skills (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name          TEXT NOT NULL UNIQUE,
    version       TEXT NOT NULL DEFAULT '0.1.0',
    title         TEXT NOT NULL,
    description   TEXT,
    content       TEXT NOT NULL,
    metadata      JSONB NOT NULL DEFAULT '{}',
    embedding     vector(768),
    created_at    TIMESTAMPTZ DEFAULT NOW(),
    updated_at    TIMESTAMPTZ DEFAULT NOW(),
    status        TEXT DEFAULT 'draft' CHECK (status IN ('draft', 'validated', 'active', 'deprecated'))
);

CREATE INDEX idx_skills_name ON skills(name);
CREATE INDEX idx_skills_status ON skills(status);
CREATE INDEX idx_skills_metadata ON skills USING GIN(metadata);

-- Skill dependencies: directed edges forming a DAG
CREATE TABLE skill_dependencies (
    skill_id      UUID REFERENCES skills(id) ON DELETE CASCADE,
    depends_on    UUID REFERENCES skills(id) ON DELETE CASCADE,
    relation_type TEXT DEFAULT 'requires' CHECK (relation_type IN ('requires', 'extends', 'recommends')),
    PRIMARY KEY (skill_id, depends_on)
);

CREATE INDEX idx_deps_skill ON skill_dependencies(skill_id);
CREATE INDEX idx_deps_depends_on ON skill_dependencies(depends_on);

-- Resources: external references (URLs to docs, articles, code)
CREATE TABLE resources (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    skill_id       UUID REFERENCES skills(id) ON DELETE CASCADE,
    url            TEXT NOT NULL,
    title          TEXT,
    resource_type  TEXT DEFAULT 'article' CHECK (resource_type IN ('official-doc', 'article', 'code', 'video', 'tutorial')),
    fetched_hash   TEXT,
    content_cached TEXT,
    last_validated TIMESTAMPTZ,
    created_at     TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX idx_resources_skill ON resources(skill_id);

-- Evidence: learned experiences from real codebases
CREATE TABLE evidences (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    skill_id       UUID REFERENCES skills(id) ON DELETE CASCADE,
    source_project TEXT NOT NULL,
    source_file    TEXT,
    code_snippet   TEXT,
    pattern        TEXT,
    language       TEXT,
    validated      BOOLEAN DEFAULT FALSE,
    embedding      vector(768),
    created_at     TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX idx_evidences_skill ON evidences(skill_id);
CREATE INDEX idx_evidences_project ON evidences(source_project);

-- Skill registry: health and completeness tracking
CREATE TABLE skill_registry (
    skill_id     UUID PRIMARY KEY REFERENCES skills(id),
    skill_name   TEXT NOT NULL,
    missing_deps TEXT[] DEFAULT '{}',
    stale        BOOLEAN DEFAULT FALSE,
    last_review  TIMESTAMPTZ,
    auto_expand  BOOLEAN DEFAULT TRUE,
    coverage     FLOAT DEFAULT 0.0
);

CREATE INDEX idx_registry_stale ON skill_registry(stale);

-- Audit log: all system events
CREATE TABLE audit_log (
    ts        TIMESTAMPTZ DEFAULT NOW(),
    event     TEXT NOT NULL,
    skill_id  UUID REFERENCES skills(id),
    details   JSONB DEFAULT '{}'
);

CREATE INDEX idx_audit_ts ON audit_log(ts);
CREATE INDEX idx_audit_event ON audit_log(event);

-- HNSW index for skill embeddings (pgvector)
-- m=32, ef_construction=128: balanced build speed vs query quality
CREATE INDEX idx_skills_embedding ON skills USING hnsw(embedding vector_cosine_ops)
    WITH (m = 32, ef_construction = 128);

-- HNSW index for evidence embeddings (smaller, faster for inserts)
CREATE INDEX idx_evidences_embedding ON evidences USING hnsw(embedding vector_cosine_ops)
    WITH (m = 16, ef_construction = 64);

-- Trigger: auto-update updated_at on skills
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ language 'plpgsql';

CREATE TRIGGER update_skills_updated_at BEFORE UPDATE ON skills
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- Down migration
-- CREATE MIGRATION 001_initial.down.sql
