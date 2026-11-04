-- Token budgets and model fallback chains (KnowAxis V3.0 §16.5 成本控制).
--
-- 预算只告警、不阻断：真正需要硬限制的租户应当用模型配额，那是另一个功能。
-- 降级链是"主力不可用时按顺序尝试"的有序候选，不是随机换一个。
CREATE TABLE IF NOT EXISTS token_budgets (
    id VARCHAR(36) PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id INTEGER NOT NULL,
    -- daily | monthly；窗口边界按 UTC，与 model_usage_daily.day 同口径。
    period VARCHAR(16) NOT NULL DEFAULT 'monthly',
    limit_tokens BIGINT NOT NULL DEFAULT 0,
    warn_percent INTEGER NOT NULL DEFAULT 80,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_token_budgets_tenant
    ON token_budgets (tenant_id);

CREATE TABLE IF NOT EXISTS model_fallback_chains (
    id VARCHAR(36) PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id INTEGER NOT NULL,
    -- 场景（chat/summary/embedding/...），与 model_usage_daily.scene 同口径。
    scene VARCHAR(32) NOT NULL,
    -- 有序候选；第一个是主力，其后是降级候选。
    model_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    description VARCHAR(255) NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_model_fallback_chains_tenant_scene
    ON model_fallback_chains (tenant_id, scene);

-- 通知台账：一行 = (租户, 周期, 窗口起点)。它是"只在跨档时通知"的载体，
-- 没有它，进程重启后每轮检查都会重新通知，重复通知等于没有通知。
CREATE TABLE IF NOT EXISTS token_budget_notices (
    id VARCHAR(36) PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id INTEGER NOT NULL,
    period VARCHAR(16) NOT NULL,
    window_start TIMESTAMPTZ NOT NULL,
    status VARCHAR(16) NOT NULL,
    notified_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_token_budget_notices
    ON token_budget_notices (tenant_id, period, window_start);
