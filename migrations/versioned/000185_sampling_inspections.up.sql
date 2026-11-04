DO $$ BEGIN RAISE NOTICE '[Migration 000185] Adding sampling inspections...'; END $$;

-- 抽检（KnowAxis V3.0 §6.6 "抽检与质量：100% / 10% / 2%"）。
--
-- 审批回答"能不能发"，抽检回答"发出去之后是不是真的对"。两者不能互相替代：
-- 平台此前没有任何事后复核已发布内容的机制。
--
-- 比例由风险等级决定：高风险 100%、中风险 10%、低风险 2%。人工挑样本永远
-- 偏向"看起来可疑的"，而抽检的价值恰好在于随机发现"看起来没问题"的问题。

CREATE TABLE IF NOT EXISTS sampling_inspections (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL DEFAULT '',
    -- 抽样窗口标签，如 "2026-11"
    period VARCHAR(16) NOT NULL,
    -- 抽样当时的风险等级（密级推导）
    risk_level VARCHAR(16) NOT NULL DEFAULT 'mid',
    -- 抽样当时应用的比率，留档以便解释"为什么这篇被抽中"
    ratio INTEGER NOT NULL DEFAULT 10,
    title VARCHAR(255) NOT NULL DEFAULT '',
    -- pending | passed | failed | waived
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    reviewed_by VARCHAR(36) NOT NULL DEFAULT '',
    reviewed_at TIMESTAMPTZ,
    note VARCHAR(1024) NOT NULL DEFAULT '',
    -- 不通过时关联的纠错任务
    follow_up_task_id VARCHAR(36) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- 同一期同一篇只抽一次：定时任务每天跑，不能产生重复样本
    CONSTRAINT uq_sampling_period_doc UNIQUE (tenant_id, knowledge_id, period)
);

CREATE INDEX IF NOT EXISTS idx_sampling_inspections_tenant
    ON sampling_inspections (tenant_id, period, status, created_at DESC);
