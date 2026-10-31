DO $$ BEGIN RAISE NOTICE '[Migration 000135] Adding knowledge lifecycle columns...'; END $$;

-- Knowledge lifecycle state machine (KnowAxis V3.0 §7.1).
-- Existing rows keep working unchanged: they start as 'valid'.
ALTER TABLE knowledges ADD COLUMN IF NOT EXISTS lifecycle_status VARCHAR(32) NOT NULL DEFAULT 'valid';
ALTER TABLE knowledges ADD COLUMN IF NOT EXISTS classification VARCHAR(16) NOT NULL DEFAULT 'internal';
ALTER TABLE knowledges ADD COLUMN IF NOT EXISTS effective_at TIMESTAMPTZ;
ALTER TABLE knowledges ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;
ALTER TABLE knowledges ADD COLUMN IF NOT EXISTS review_cycle_days INT NOT NULL DEFAULT 0;
ALTER TABLE knowledges ADD COLUMN IF NOT EXISTS last_reviewed_at TIMESTAMPTZ;
ALTER TABLE knowledges ADD COLUMN IF NOT EXISTS last_reviewed_by VARCHAR(36);
ALTER TABLE knowledges ADD COLUMN IF NOT EXISTS replaced_by_id VARCHAR(36);
ALTER TABLE knowledges ADD COLUMN IF NOT EXISTS current_version INT NOT NULL DEFAULT 1;

CREATE INDEX IF NOT EXISTS idx_knowledges_lifecycle ON knowledges (tenant_id, knowledge_base_id, lifecycle_status);

-- Immutable published-version snapshots (KnowAxis V3.0 §7.4). One row per
-- published version; receipt_id links to the approval receipt (migration 136).
CREATE TABLE IF NOT EXISTS knowledge_versions (
    id VARCHAR(36) PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id INTEGER NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL,
    version INT NOT NULL,
    content_hash VARCHAR(128) NOT NULL,
    title VARCHAR(255) NOT NULL DEFAULT '',
    change_note TEXT NOT NULL DEFAULT '',
    snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    published_by VARCHAR(36) NOT NULL DEFAULT '',
    receipt_id VARCHAR(64) NOT NULL DEFAULT '',
    effective_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_knowledge_versions_knowledge ON knowledge_versions (knowledge_id, version DESC);
