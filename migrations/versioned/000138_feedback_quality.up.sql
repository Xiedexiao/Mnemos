DO $$ BEGIN RAISE NOTICE '[Migration 000138] Adding message feedback and quality task tables...'; END $$;

-- Per-user message ratings (KnowAxis V3.0 §14.3). One row per
-- (message, user): a re-rating overwrites rating / reason_codes / comment
-- in place via the unique constraint (the repository upserts on conflict).
CREATE TABLE IF NOT EXISTS message_feedback (
    id VARCHAR(36) PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id INTEGER NOT NULL,
    message_id VARCHAR(36) NOT NULL,
    session_id VARCHAR(36) NOT NULL DEFAULT '',
    user_id VARCHAR(36) NOT NULL,
    rating VARCHAR(16) NOT NULL,
    reason_codes JSONB NOT NULL DEFAULT '[]'::jsonb,
    comment TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_message_feedback_message_user UNIQUE (message_id, user_id)
);

-- Quality tasks (KnowAxis V3.0 §13.8 / §14.5). Governance work items opened
-- by user reports (correction / stale_report) or by the useless-rating
-- threshold sweep (useless_threshold); low_rating is reserved for V1 follow
-- ups. status flows open -> processing -> done|waived with reopen allowed.
CREATE TABLE IF NOT EXISTS quality_tasks (
    id VARCHAR(36) PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id INTEGER NOT NULL,
    task_type VARCHAR(24) NOT NULL,
    source_type VARCHAR(16) NOT NULL,
    source_id VARCHAR(36) NOT NULL DEFAULT '',
    knowledge_id VARCHAR(36) NOT NULL DEFAULT '',
    chunk_id VARCHAR(36) NOT NULL DEFAULT '',
    detail JSONB NOT NULL DEFAULT '{}'::jsonb,
    status VARCHAR(16) NOT NULL DEFAULT 'open',
    assignee_id VARCHAR(36) NOT NULL DEFAULT '',
    resolution_note TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_quality_tasks_tenant_status ON quality_tasks (tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_quality_tasks_knowledge ON quality_tasks (knowledge_id);
