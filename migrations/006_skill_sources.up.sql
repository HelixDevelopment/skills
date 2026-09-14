-- Skill sources: registry of external skill repositories (G74)
-- Tracks GitHub repos, filesystem paths, and URLs that supply SKILL.md files
-- for the source-ingestion pipeline (internal/skillsource,
-- internal/source/github, internal/source/mapper, internal/source/skillmd).

CREATE TABLE skill_sources (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name          TEXT NOT NULL UNIQUE,
    source_type   TEXT NOT NULL CHECK (source_type IN ('github', 'filesystem', 'url')),
    config        JSONB NOT NULL DEFAULT '{}',
    enabled       BOOLEAN NOT NULL DEFAULT TRUE,
    last_sync     TIMESTAMPTZ,
    sync_status   TEXT NOT NULL DEFAULT 'pending' CHECK (sync_status IN ('pending', 'syncing', 'completed', 'failed')),
    error_message TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_skill_sources_name ON skill_sources(name);
CREATE INDEX idx_skill_sources_enabled ON skill_sources(enabled);
CREATE INDEX idx_skill_sources_source_type ON skill_sources(source_type);
