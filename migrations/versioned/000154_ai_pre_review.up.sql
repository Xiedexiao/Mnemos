DO $$ BEGIN RAISE NOTICE '[Migration 000154] Adding AI pre-review runs (KnowAxis V3.0 §6.5)...'; END $$;

-- AI 预审运行记录（KnowAxis V3.0 §6.5「AI 预审」）。
--
-- 一件事：把"审批人应该看的地方"落成可回溯的检查清单。每次预审追加一行
-- run，findings 存本次全部提示（见 internal/types/ai_pre_review.go 的
-- PreReviewFinding：check/severity/message/evidence/locator/suggestion）。
--
-- 边界（§6.5「AI 预审的边界」）：
--   - 只给"检查清单 + 风险提示"，不落"通过/不通过"结论 —— 表里没有
--     verdict 列，也从没有把它写成审批状态（审批状态在 approval_tasks）；
--   - 每条提示必须可解释：evidence 存原文依据，locator 存位置；
--   - 记录 model_version / prompt_version，便于回溯与评测；
--   - 预审失败不阻断审批流：status=failed + error_message 留痕，审批人照常
--     可以裁决，这正是"AI 不替人决定"的工程化体现。
--
-- 状态机：running → completed | failed（单次运行内单向，不做重跑更新；重跑
-- 追加新行，GET 按 created_at 倒序返回历史）。
--
-- task_id 指向 approval_tasks.id，但刻意不建外键：预审是审批任务的旁路
-- 观察记录，任务归档/清理后运行记录仍需保留供复盘（与 audit_logs 同一
-- 留档语义）。knowledge_id 同理只作引用。
CREATE TABLE IF NOT EXISTS ai_pre_review_runs (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    task_id VARCHAR(36) NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'running',
    findings JSONB NOT NULL DEFAULT '[]'::jsonb,
    model_version VARCHAR(64) DEFAULT '',
    prompt_version VARCHAR(64) DEFAULT '',
    error_message TEXT DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ
);

-- 查询只有两种：按 (tenant, task) 取某次审核的全部预审运行；以及列表内的
-- created_at 倒序。前缀索引一次覆盖过滤，排序走索引内的顺序扫描。
CREATE INDEX IF NOT EXISTS idx_ai_pre_review_runs_tenant_task
    ON ai_pre_review_runs (tenant_id, task_id, created_at DESC);
