DO $$ BEGIN RAISE NOTICE '[Migration 000174] Adding embedded contextual push...'; END $$;

-- 业务系统嵌入式上下文推送（KnowAxis V3.0 §15.5 "嵌入式学习" + §16.4 嵌入式组件）。
--
-- §15.5 要求员工在业务系统里处理工单/填表/看设备时按上下文推送相关知识，
-- 受权限与密级约束且可关闭。这里的三张表分别承载：规则、用户开关、交互
-- 事件（曝光/点击/忽略）。

CREATE TABLE IF NOT EXISTS embed_push_rules (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    name VARCHAR(128) NOT NULL,
    -- 空串 = 匹配任意；其余为大小写不敏感的精确匹配
    system VARCHAR(32) NOT NULL DEFAULT '',
    scene VARCHAR(64) NOT NULL DEFAULT '',
    entity_type VARCHAR(64) NOT NULL DEFAULT '',
    entity_value VARCHAR(128) NOT NULL DEFAULT '',
    -- 额外的键值匹配（全部满足才算命中）
    attributes JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- 知识来源二选一或并用：标签集合 / 指定知识 ID
    tag_names JSONB NOT NULL DEFAULT '[]'::jsonb,
    knowledge_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    reason VARCHAR(255) NOT NULL DEFAULT '',
    priority INTEGER NOT NULL DEFAULT 0,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_by VARCHAR(36) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_embed_push_rules_tenant
    ON embed_push_rules (tenant_id, enabled, priority DESC);

CREATE TABLE IF NOT EXISTS embed_push_preferences (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_embed_push_pref_user UNIQUE (tenant_id, user_id)
);

CREATE TABLE IF NOT EXISTS embed_push_events (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    rule_id VARCHAR(36) NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL DEFAULT '',
    -- impression | click | dismiss
    event_type VARCHAR(16) NOT NULL,
    -- 上报时的业务上下文，用于规则调优
    context JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_embed_push_events_user
    ON embed_push_events (tenant_id, user_id, event_type, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_embed_push_events_rule
    ON embed_push_events (tenant_id, rule_id, event_type, created_at DESC);
