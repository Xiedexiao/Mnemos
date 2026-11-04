-- Saved knowledge-base views (KnowAxis V3.0 §12.2 "筛选条件可保存为我的视图").
--
-- 个人资产：作用域是 (tenant_id, user_id)，没有租户共享视图——共享会带来
-- "谁维护、改了影响谁"的治理问题，而这一节要的是个人效率。
-- 名称在 (tenant, user) 内唯一：两个同名视图让人无法判断点哪个。
-- 条件以 JSON 存：筛选维度还会增长，每加一维就迁移一次列不划算，而视图
-- 条件从不需要被 SQL 单独查询。
CREATE TABLE IF NOT EXISTS saved_knowledge_views (
    id VARCHAR(36) PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id INTEGER NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    name VARCHAR(120) NOT NULL,
    -- 空串表示"在任意空间打开都可用"
    knowledge_base_id VARCHAR(36) NOT NULL DEFAULT '',
    filter JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- 同一 (用户, 空间) 至多一个默认；由服务层的"先清后设"保证，
    -- 因为部分唯一索引在两种方言间的支持不一致。
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_saved_view_user_name
    ON saved_knowledge_views (tenant_id, user_id, name);
CREATE INDEX IF NOT EXISTS idx_saved_views_user
    ON saved_knowledge_views (tenant_id, user_id, name);
CREATE INDEX IF NOT EXISTS idx_saved_views_default
    ON saved_knowledge_views (tenant_id, user_id, knowledge_base_id)
    WHERE is_default = TRUE;
