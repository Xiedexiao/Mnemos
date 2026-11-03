DO $$ BEGIN RAISE NOTICE '[Migration 000167] Adding the knowledge Q&A community...'; END $$;

-- 知识问答社区（KnowAxis V3.0 §15.6）。
--
-- 平台此前只有"文档评论"：读者可以向文档提问，但没有人回答，也没有悬赏、
-- 采纳、"一键转知识"。一线员工的隐性经验因此仍然停在群聊里。
--
-- 三张表：问题、回答、举报（治理入口）。

CREATE TABLE IF NOT EXISTS community_questions (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    title VARCHAR(255) NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    -- 关联知识（可选）：这个问题是关于哪一篇的
    knowledge_id VARCHAR(36) NOT NULL DEFAULT '',
    -- @专家（可选）
    expert_id VARCHAR(36) NOT NULL DEFAULT '',
    -- 悬赏积分
    bounty INTEGER NOT NULL DEFAULT 0,
    -- open | answered | resolved | closed
    status VARCHAR(16) NOT NULL DEFAULT 'open',
    -- 被采纳的回答
    accepted_answer_id VARCHAR(36) NOT NULL DEFAULT '',
    -- 转成正式知识后的知识 ID（§15.6 一键转知识）
    converted_knowledge_id VARCHAR(36) NOT NULL DEFAULT '',
    asked_by VARCHAR(36) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_community_questions_tenant_status
    ON community_questions (tenant_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_community_questions_knowledge
    ON community_questions (knowledge_id);

CREATE TABLE IF NOT EXISTS community_answers (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    question_id VARCHAR(36) NOT NULL,
    body TEXT NOT NULL,
    answered_by VARCHAR(36) NOT NULL,
    -- 答案质量信号：有用票
    votes INTEGER NOT NULL DEFAULT 0,
    -- 被采纳（同一问题只有一个）
    accepted BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_community_answers_question
    ON community_answers (question_id, accepted DESC, votes DESC, created_at ASC);

-- 举报（§15.6 治理：举报、申诉、违规内容下架）
CREATE TABLE IF NOT EXISTS community_reports (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    -- question | answer
    target_type VARCHAR(16) NOT NULL,
    target_id VARCHAR(36) NOT NULL,
    reason VARCHAR(512) NOT NULL DEFAULT '',
    reported_by VARCHAR(36) NOT NULL,
    -- open | dismissed | removed
    status VARCHAR(16) NOT NULL DEFAULT 'open',
    handled_by VARCHAR(36) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_community_reports_tenant
    ON community_reports (tenant_id, status, created_at DESC);
