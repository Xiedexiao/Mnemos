DO $$ BEGIN RAISE NOTICE '[Migration 000136] Adding review approval tables...'; END $$;

-- 知识审核流（KnowAxis V3.0 §6 的 v1 子集）。
--
-- approval_flows：KB 级审批流配置（single|dual）。无配置时服务层默认 single；
-- enabled=false 视同无配置。risk_rules 预留风险规则与 SLA 参数
-- （v1 只读 {"sla_hours": n}）。
CREATE TABLE IF NOT EXISTS approval_flows (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    kb_id VARCHAR(36) NOT NULL,
    mode VARCHAR(16) NOT NULL DEFAULT 'single',
    risk_rules JSONB NOT NULL DEFAULT '{}'::jsonb,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_approval_flows_tenant_kb UNIQUE (tenant_id, kb_id)
);

-- approval_tasks：一次审核提交 = 一个任务。
-- baseline 冻结提交时刻的内容指纹（content hash + chunk 数 + 标题快照 +
-- custom_metadata 摘要），决策时用它对照"内容是否中途变过"。
-- decided_history 记录每一票 [{by,decision,at}]，支撑 dual 模式 / 高风险的
-- 多人表决与四眼校验（v1 自加列）。mode 冻结提交时刻的流配置，避免
-- submit→decide 之间 flow 配置漂移导致表决口径不一致（v1 自加列）。
CREATE TABLE IF NOT EXISTS approval_tasks (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL,
    kb_id VARCHAR(36) NOT NULL,
    mode VARCHAR(16) NOT NULL DEFAULT 'single',
    baseline JSONB NOT NULL DEFAULT '{}'::jsonb,
    content_hash VARCHAR(128) NOT NULL,
    risk_level VARCHAR(16) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    assignee_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    requested_by VARCHAR(36) NOT NULL,
    decided_by VARCHAR(36) NOT NULL DEFAULT '',
    decided_history JSONB NOT NULL DEFAULT '[]'::jsonb,
    decision_at TIMESTAMPTZ,
    reason_code VARCHAR(32) NOT NULL DEFAULT '',
    comment TEXT NOT NULL DEFAULT '',
    sla_due TIMESTAMPTZ,
    receipt_id VARCHAR(64) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_approval_tasks_tenant_status ON approval_tasks (tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_approval_tasks_knowledge ON approval_tasks (knowledge_id);

-- approval_receipts：append-only 审批回执，逐行以 prev_hash/row_hash 锚成
-- 哈希链（row_hash = sha256_hex(prev_hash + "\n" + 规范化JSON字段串)），
-- 供审计回放验证"没有任何一笔回执被篡改或抽走"。
-- 硬规则：本表 append-only，业务代码不得出现对它的 UPDATE/DELETE 语句
-- （仓储层只提供 Append/Get/List 三个方法）。
CREATE TABLE IF NOT EXISTS approval_receipts (
    receipt_id VARCHAR(64) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    task_id VARCHAR(36) NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL,
    content_hash VARCHAR(128) NOT NULL,
    baseline_snapshot JSONB NOT NULL,
    approver_id VARCHAR(36) NOT NULL,
    decision VARCHAR(16) NOT NULL,
    reason_code VARCHAR(32) NOT NULL DEFAULT '',
    row_hash VARCHAR(64) NOT NULL,
    prev_hash VARCHAR(64) NOT NULL DEFAULT 'GENESIS',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 链尾查找（Append 事务读当前租户最新 row_hash）与按任务追溯。
CREATE INDEX IF NOT EXISTS idx_approval_receipts_tenant_time ON approval_receipts (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_approval_receipts_task ON approval_receipts (task_id);
