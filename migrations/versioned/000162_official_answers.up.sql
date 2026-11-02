DO $$ BEGIN RAISE NOTICE '[Migration 000162] Adding official answers...'; END $$;

-- 官方答案（KnowAxis V3.0 §13.7）。
--
-- 高频、高风险问题需要一份"经过审批的标准答案"：带条件、例外、适用范围、
-- 有效期、Owner 与证据。平台此前完全没有这个对象——问答只能从文档片段
-- 现拼答案，同一个高风险问题每次措辞都可能不同，也无法回答"这条口径是谁
-- 批的、什么时候到期"。
--
-- 生命周期（§13.7）：draft → in_review → approved → published → 到期/替代。
-- 发布必须绑定审批回执（approval_receipts），与知识发布的门槛一致。
CREATE TABLE IF NOT EXISTS official_answers (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    -- 归一化问题：同一问题只应有一份生效的官方答案
    question_normalized VARCHAR(512) NOT NULL,
    question VARCHAR(512) NOT NULL,
    -- 标准答案正文
    answer TEXT NOT NULL DEFAULT '',
    -- 适用条件：什么情况下这条答案成立
    conditions TEXT NOT NULL DEFAULT '',
    -- 例外：什么情况下不适用
    exceptions TEXT NOT NULL DEFAULT '',
    -- 适用范围（机型/地区/客户类型/版本…）
    scope VARCHAR(512) NOT NULL DEFAULT '',
    -- 风险级：high 的官方答案失效时必须有替代或下线
    risk_level VARCHAR(16) NOT NULL DEFAULT 'mid',
    -- draft | in_review | approved | published | expired | superseded | retired
    status VARCHAR(16) NOT NULL DEFAULT 'draft',
    -- 生效/失效期
    effective_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    -- Owner：谁对这条口径负责
    owner_id VARCHAR(36) NOT NULL DEFAULT '',
    -- 绑定证据（knowledge_id 列表）
    evidence JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- 发布时绑定的审批回执
    receipt_id VARCHAR(64) NOT NULL DEFAULT '',
    -- 被哪条官方答案替代
    superseded_by_id VARCHAR(36) NOT NULL DEFAULT '',
    -- 来源知识库（可选，用于权限收敛：官方答案不比源知识更可见）
    knowledge_base_id VARCHAR(36) NOT NULL DEFAULT '',
    -- 版本号：每次发布 +1
    version INTEGER NOT NULL DEFAULT 0,
    created_by VARCHAR(36) NOT NULL DEFAULT '',
    reviewed_by VARCHAR(36) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_official_answers_tenant_status
    ON official_answers (tenant_id, status);
-- 同一问题在"待审/已发布"区间只能有一份生效答案。
CREATE UNIQUE INDEX IF NOT EXISTS uq_official_answers_active
    ON official_answers (tenant_id, question_normalized)
    WHERE status IN ('approved', 'published');
