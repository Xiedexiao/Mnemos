DO $$ BEGIN RAISE NOTICE '[Migration 000164] Generalizing approval tasks to all governance objects...'; END $$;

-- 审核对象泛化（KnowAxis V3.0 §6.2 十四类治理动作）。
--
-- v1 的 approval_tasks 只服务"知识发布"：knowledge_id NOT NULL，其余十二类
-- （官方答案、规则政策、权限提权、外部分享、批量导出、删除下架、合并拆分、
-- 术语分类模板变更、模型 Prompt 上线、Agent 工具授权、Schema 变更、AI 沉淀
-- 候选）各走各的、互不相通的状态机，没有回执链也没有四眼校验。
--
-- 这里把主语泛化：subject_type + subject_id 描述"在审什么"，knowledge_id
-- 保留为知识类对象的快捷列（历史数据与既有查询不变）。
ALTER TABLE approval_tasks ADD COLUMN IF NOT EXISTS subject_type VARCHAR(32) NOT NULL DEFAULT 'knowledge';
ALTER TABLE approval_tasks ADD COLUMN IF NOT EXISTS subject_id VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE approval_tasks ADD COLUMN IF NOT EXISTS title VARCHAR(255) NOT NULL DEFAULT '';

-- 历史行回填：知识类任务的主语就是它自己。
UPDATE approval_tasks
SET subject_type = 'knowledge', subject_id = knowledge_id
WHERE subject_type = 'knowledge' AND subject_id = '';

CREATE INDEX IF NOT EXISTS idx_approval_tasks_subject
    ON approval_tasks (tenant_id, subject_type, subject_id, status);
