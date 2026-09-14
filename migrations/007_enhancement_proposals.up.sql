-- Enhancement proposals: tracks proposed improvements to existing skills
-- discovered during source sync (G81). When a synced skill differs from
-- the existing version, a proposal is created rather than overwriting
-- directly — the operator or auto-accept policy decides whether to apply.

CREATE TABLE skill_enhancement_proposals (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    skill_id        UUID NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
    source_id       UUID NOT NULL REFERENCES skill_sources(id) ON DELETE CASCADE,
    proposal_type   TEXT NOT NULL CHECK (proposal_type IN ('update', 'dependency', 'resource', 'deprecation')),
    title           TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    proposed_changes JSONB NOT NULL DEFAULT '{}',
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted', 'rejected', 'applied')),
    reviewed_at     TIMESTAMPTZ,
    reviewed_by     TEXT,
    applied_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_sep_skill_id ON skill_enhancement_proposals(skill_id);
CREATE INDEX idx_sep_source_id ON skill_enhancement_proposals(source_id);
CREATE INDEX idx_sep_status ON skill_enhancement_proposals(status);
CREATE INDEX idx_sep_proposal_type ON skill_enhancement_proposals(proposal_type);
