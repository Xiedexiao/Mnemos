DO $$ BEGIN RAISE NOTICE '[Migration 000158] Adding approval flow configuration...'; END $$;

-- 审批流配置（KnowAxis V3.0 §6.3）。v1 的 approval_flows 只有 mode 一列且
-- 没有任何写入入口，租户实际上无法开启双人审批；这里补齐配置面：
--
--   version         流程定义的版本号。§6.3 要求"流程版本化：在途流程按旧版本
--                   走完"，所以任务提交时冻结版本号，改配置不影响在途任务。
--   rules           条件路由规则（JSONB）。按密级/风险/影响范围决定审批强度。
--   sla_hours       审批时限（小时）。到期催办、超时升级的依据。
--   escalate_role   超时升级到的角色（缺省 admin）。
--   reminder_hours  首次催办提前量（小时）。
ALTER TABLE approval_flows ADD COLUMN IF NOT EXISTS version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE approval_flows ADD COLUMN IF NOT EXISTS rules JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE approval_flows ADD COLUMN IF NOT EXISTS sla_hours INTEGER NOT NULL DEFAULT 72;
ALTER TABLE approval_flows ADD COLUMN IF NOT EXISTS reminder_hours INTEGER NOT NULL DEFAULT 24;
ALTER TABLE approval_flows ADD COLUMN IF NOT EXISTS escalate_role VARCHAR(32) NOT NULL DEFAULT 'admin';

-- SLA 事件台账：§6.3 的"到期催办、超时升级、积压预警"需要可追溯的处置记录，
-- 否则无法回答"这张单为什么被升级"。
CREATE TABLE IF NOT EXISTS approval_sla_events (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    task_id VARCHAR(36) NOT NULL,
    -- reminded | escalated
    kind VARCHAR(16) NOT NULL,
    -- 处置详情：催办对象、升级到谁、当时的逾期时长
    detail JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_approval_sla_events_task ON approval_sla_events (task_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_approval_tasks_sla ON approval_tasks (tenant_id, sla_due) WHERE status = 'pending';
