DO $$ BEGIN RAISE NOTICE '[Migration 000137] Adding knowledge ACL table...'; END $$;

-- Per-document ACL (KnowAxis V3.0 §2.1-§2.4). Final visibility =
-- classification allowance ∩ ACL. Deny wins over allow; subjects v1 are
-- users (group/role subject types are reserved columns).
CREATE TABLE IF NOT EXISTS knowledge_acl (
    id VARCHAR(36) PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id INTEGER NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL,
    subject_type VARCHAR(16) NOT NULL DEFAULT 'user',
    subject_id VARCHAR(64) NOT NULL,
    action VARCHAR(24) NOT NULL DEFAULT 'view',
    effect VARCHAR(8) NOT NULL DEFAULT 'allow',
    granted_by VARCHAR(36) NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (knowledge_id, subject_type, subject_id, action)
);

CREATE INDEX IF NOT EXISTS idx_knowledge_acl_subject
    ON knowledge_acl (tenant_id, subject_type, subject_id, action);
CREATE INDEX IF NOT EXISTS idx_knowledge_acl_knowledge
    ON knowledge_acl (knowledge_id);
