DO $$ BEGIN RAISE NOTICE '[Migration 000139] Adding share_links table (knowledge external sharing v1)...'; END $$;

-- Knowledge external share links (KnowAxis V3.0 §2.5, v1 subset).
-- One row per issued link. The token is the only credential a visitor
-- needs: it is generated server-side from crypto/rand (32 bytes,
-- base64url — 43 chars) and never recycled, so revocation is a row
-- flip (revoked_at) rather than a delete.
CREATE TABLE IF NOT EXISTS share_links (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL,
    token VARCHAR(64) NOT NULL UNIQUE,
    -- org: anyone inside the owning workspace; people: named visitors (v1
    -- issues the token without per-visitor credentials; the type is kept
    -- on the row so the audit trail distinguishes the audiences).
    link_type VARCHAR(16) NOT NULL,
    -- view | download. download additionally exposes a presigned file URL
    -- on the public resolve response.
    permission VARCHAR(16) NOT NULL DEFAULT 'view',
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    created_by VARCHAR(36) NOT NULL,
    -- v1 approves links immediately; 'pending' is reserved for the A2
    -- approval flow and never blocks resolution in v1.
    approval_status VARCHAR(16) NOT NULL DEFAULT 'approved',
    visit_count BIGINT NOT NULL DEFAULT 0,
    -- Most recent visits, newest first, trimmed to 100 entries by the
    -- repository on every append: {at, ip, ua}.
    visit_log JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_share_links_tenant_knowledge ON share_links (tenant_id, knowledge_id);
CREATE INDEX IF NOT EXISTS idx_share_links_token ON share_links (token);
