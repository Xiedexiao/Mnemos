DO $$ BEGIN RAISE NOTICE '[Migration 000144] Adding knowledge candidate cards (process experience v1)...'; END $$;

-- 过程经验自动沉淀 v1（KnowAxis V3.0 §10）：
-- 候选卡片 + 审核发布闭环。九类业务来源（手工 / 工单 / 项目结项 / 质量事件 /
-- 生产事件 / PLM 变更 / 客诉 / 会议评审 / 流程变更）共用一张候选表。
--
-- 状态机：candidate → in_review → confirmed → published；
-- candidate|in_review → withdrawn（提交人撤回），in_review → rejected（审核驳回）。
-- level 为 §10.4 知识层级：L0 过程档案 / L1 候选 / L2 已确认 / L3 权威。
-- card 存 §10.3 候选卡片结构（what/scope/phenomenon/cause_candidate/measures/
-- validation/evidence_links）；evidence 存结构化证据引用
-- [{source_type,source_id,locator,quote}]。
CREATE TABLE IF NOT EXISTS knowledge_candidates (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    source_type VARCHAR(24) NOT NULL DEFAULT 'manual',
    source_id VARCHAR(64) NOT NULL DEFAULT '',
    title VARCHAR(255) NOT NULL,
    level VARCHAR(8) NOT NULL DEFAULT 'L1',
    card JSONB NOT NULL DEFAULT '{}'::jsonb,
    evidence JSONB DEFAULT '[]'::jsonb,
    confidence VARCHAR(16) NOT NULL DEFAULT 'AMBIGUOUS',
    dedup_status VARCHAR(16) NOT NULL DEFAULT 'pending',
    duplicate_of VARCHAR(36) NOT NULL DEFAULT '',
    status VARCHAR(16) NOT NULL DEFAULT 'candidate',
    knowledge_id VARCHAR(36) NOT NULL DEFAULT '',
    requested_by VARCHAR(36) NOT NULL,
    reviewed_by VARCHAR(36) NOT NULL DEFAULT '',
    review_comment TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 审核待办按状态过滤；层级视图按 level 过滤（§10.4 门禁看 level+confidence）。
CREATE INDEX IF NOT EXISTS idx_knowledge_candidates_tenant_status ON knowledge_candidates (tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_knowledge_candidates_tenant_level ON knowledge_candidates (tenant_id, level);
