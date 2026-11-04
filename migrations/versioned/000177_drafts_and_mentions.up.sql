DO $$ BEGIN RAISE NOTICE '[Migration 000177] Adding content drafts and comment mentions...'; END $$;

-- 草稿自动保存（KnowAxis V3.0 §5.4）与评论 @提及（§5.1）。
--
-- 草稿刻意独立于正式内容：不参与版本号、不进入检索、不触发审批。每个
-- (tenant, user, kind, scope) 只有一行——自动保存是覆盖当前草稿，不是追加
-- 历史；历史由正式版本承担。
--
-- revision 是乐观锁：客户端带上自己读到的 revision，不匹配时服务端返回 409
-- 与当前内容，由前端合并。协同编辑里 last-write-wins 会直接吃掉别人的一段
-- 文字，这个代价远高于一次冲突提示。

CREATE TABLE IF NOT EXISTS content_drafts (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    -- knowledge | comment | answer
    kind VARCHAR(16) NOT NULL,
    -- 目标对象 id；新建知识时为 'new'
    scope_id VARCHAR(64) NOT NULL DEFAULT '',
    title VARCHAR(512) NOT NULL DEFAULT '',
    body TEXT NOT NULL DEFAULT '',
    anchor JSONB,
    mentions JSONB NOT NULL DEFAULT '[]'::jsonb,
    revision INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_drafts_owner UNIQUE (tenant_id, user_id, kind, scope_id)
);

CREATE INDEX IF NOT EXISTS idx_drafts_user
    ON content_drafts (tenant_id, user_id, updated_at DESC);

-- 评论的提及列表。落库的是**校验过**的提及（存在、同工作区、作者可见），
-- 未通过的在服务端就被剔除，不会留在这里。
ALTER TABLE knowledge_comments
    ADD COLUMN IF NOT EXISTS mentions JSONB NOT NULL DEFAULT '[]'::jsonb;
