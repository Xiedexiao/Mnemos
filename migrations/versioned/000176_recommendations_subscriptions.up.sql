DO $$ BEGIN RAISE NOTICE '[Migration 000176] Adding view events and subscriptions...'; END $$;

-- 站内发现路径（KnowAxis V3.0 §15.3 阅读 / §16.1 热门 / §5 协作订阅）。
--
-- §15.3 要求"猜你想学、最新动态、热门知识、我的关注"；此前平台只有搜索
-- 一条路径，浏览历史存在前端 localStorage（换设备即丢，也无法统计热度）。
--
-- 推荐本身不建表：它是按需计算的投影，落库会带来"推荐结果过期但仍可见"
-- 与一张随时间膨胀的表。这里只存两个事实源：阅读事件与订阅。

CREATE TABLE IF NOT EXISTS knowledge_view_events (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 热门榜：按 (tenant, 时间窗) 取 top，再按 knowledge_id 聚合。
CREATE INDEX IF NOT EXISTS idx_knowledge_views_hot
    ON knowledge_view_events (tenant_id, knowledge_id, created_at DESC);
-- 浏览历史：按 (tenant, user) 倒序。
CREATE INDEX IF NOT EXISTS idx_knowledge_views_history
    ON knowledge_view_events (tenant_id, user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS subscriptions (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    -- space | folder | knowledge | tag | author
    subject_type VARCHAR(16) NOT NULL,
    -- folder 主体格式：spaceID || '\x00' || path（见 types.NormalizeSubscriptionSubject）
    subject_id VARCHAR(191) NOT NULL,
    space_id VARCHAR(36) NOT NULL DEFAULT '',
    label VARCHAR(191) NOT NULL DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    last_digest_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_subscriptions_user_subject
    ON subscriptions (tenant_id, user_id, subject_type, subject_id);
CREATE INDEX IF NOT EXISTS idx_subscriptions_user
    ON subscriptions (tenant_id, user_id, enabled, created_at DESC);
