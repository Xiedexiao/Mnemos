DO $$ BEGIN RAISE NOTICE '[Migration 000165] Wiring change requests to the approval flow...'; END $$;

-- 变更单审批与关闭门禁（KnowAxis V3.0 §7.2 变更单 / §7.6 影响闭环）。
--
-- v1 的 SubmitForApproval 只改状态位：任何 Admin 都能从 analyzing 一路推到
-- 发布，审批形同虚设。关闭门禁的四条条件也只实现两条（影响任务、条目状态），
-- "验证通过"与"通知已送达"没有任何落点。
ALTER TABLE change_requests ADD COLUMN IF NOT EXISTS approval_task_id VARCHAR(36) NOT NULL DEFAULT '';
ALTER TABLE change_requests ADD COLUMN IF NOT EXISTS approval_receipt_id VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE change_requests ADD COLUMN IF NOT EXISTS verification_result TEXT NOT NULL DEFAULT '';
ALTER TABLE change_requests ADD COLUMN IF NOT EXISTS notified_at TIMESTAMPTZ;

-- 替代类变更需要记录接替本条目新知识（§7.2 replacement）。
ALTER TABLE change_items ADD COLUMN IF NOT EXISTS replaced_by_id VARCHAR(36) NOT NULL DEFAULT '';
