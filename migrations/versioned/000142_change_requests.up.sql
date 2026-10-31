DO $$ BEGIN RAISE NOTICE '[Migration 000142] Adding change request tables...'; END $$;

-- 变更单（KnowAxis V3.0 §7.2 的 v1 子集）。
--
-- change_requests：一次批量知识变更 = 一张单。CR 编号服务层生成
-- （CR-YYYYMMDD-序号，租户内唯一）。status 是变更单自己的状态机：
--   draft → analyzing → in_approval → scheduled → publishing → verifying
--   → closed | rolled_back
-- baseline 冻结分析时刻的条目清单（item_count + knowledge_ids），关闭门禁
-- 与回溯对照都用它；close_conditions 是关闭前置条件的结构化清单（v1 由
-- 服务层门禁兜底，条件本身只读展示）。
CREATE TABLE IF NOT EXISTS change_requests (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    cr_number VARCHAR(32) NOT NULL,
    title VARCHAR(255) NOT NULL,
    reason VARCHAR(32) NOT NULL,
    change_type VARCHAR(24) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'draft',
    risk_level VARCHAR(16) NOT NULL DEFAULT 'low',
    baseline JSONB DEFAULT '{}'::jsonb,
    effective_at TIMESTAMPTZ,
    announce_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    rollback_plan TEXT DEFAULT '',
    close_conditions JSONB DEFAULT '[]'::jsonb,
    created_by VARCHAR(36) NOT NULL,
    closed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_change_requests_tenant_number UNIQUE (tenant_id, cr_number)
);

CREATE INDEX IF NOT EXISTS idx_change_requests_tenant_status ON change_requests (tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_change_requests_tenant_created ON change_requests (tenant_id, created_at DESC);

-- change_items：变更单作用到的知识条目。item_status 由 Publish 逐项回写：
--   pending（待应用）| applied（已应用）| failed（应用失败，可重试）| skipped
CREATE TABLE IF NOT EXISTS change_items (
    id VARCHAR(36) PRIMARY KEY,
    cr_id VARCHAR(36) NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL,
    item_status VARCHAR(16) DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_change_items_cr_knowledge UNIQUE (cr_id, knowledge_id)
);

CREATE INDEX IF NOT EXISTS idx_change_items_cr ON change_items (cr_id);
CREATE INDEX IF NOT EXISTS idx_change_items_knowledge ON change_items (knowledge_id);

-- impact_tasks：影响分析产出的待办（变更波及面）。v1 的影响分析是"谁引用
-- 了它"的简化：同 KB 共享标签的近邻知识 + 条目自身；training / external_doc /
-- material / system_config 目标为后续版本预留，v1 只产 knowledge 类任务。
CREATE TABLE IF NOT EXISTS impact_tasks (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    cr_id VARCHAR(36) NOT NULL,
    target_type VARCHAR(16) NOT NULL,
    target_id VARCHAR(36) DEFAULT '',
    priority VARCHAR(16) DEFAULT 'normal',
    status VARCHAR(16) DEFAULT 'open',
    assignee_id VARCHAR(36) DEFAULT '',
    note TEXT DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_impact_tasks_tenant_status ON impact_tasks (tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_impact_tasks_cr ON impact_tasks (cr_id);
CREATE INDEX IF NOT EXISTS idx_impact_tasks_target ON impact_tasks (target_type, target_id);
