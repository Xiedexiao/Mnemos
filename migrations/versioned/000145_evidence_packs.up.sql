DO $$ BEGIN RAISE NOTICE '[Migration 000145] Adding evidence packs...'; END $$;

-- Evidence Pack 持久化（KnowAxis V3.0 §13.4 的 v1 子集）。
--
-- evidence_packs：一次问答（按 request_id 唯一）的整条链路快照，供事后
-- 按 request_id 回放"当时检索到什么、上下文如何编译、以什么权限基线、
-- 模型答了什么、引用了哪里"。回答正常时 refusal 为空串；命中拒绝路径
-- （refused=策略拒绝 / conflict=与权威知识冲突 / no_evidence=无依据）时
-- answer 可为空、candidates/citations 仍保留现场。
--
-- 保留策略：retention_until 由创建时按租户配置填写；v1 固定
-- created_at + 90 天（常量 EvidencePackRetentionDays，见
-- internal/types/evidence_pack.go）。清理任务后续按 audit_logs 的
-- retention sweep 模式接入（audit_log_retention.go），扫
-- retention_until < NOW() 的行删除；retention_until IS NULL 的历史行
-- 不清（视同永久保留）。
CREATE TABLE IF NOT EXISTS evidence_packs (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    request_id VARCHAR(64) NOT NULL,
    session_id VARCHAR(36) NOT NULL DEFAULT '',
    message_id VARCHAR(36) NOT NULL DEFAULT '',
    user_id VARCHAR(36) NOT NULL DEFAULT '',
    question TEXT NOT NULL DEFAULT '',
    -- 检索候选快照（脱敏后）：[{knowledge_id, chunk_id, score, snippet}]。
    -- snippet 只保留片段摘要，不落全量 chunk 正文。
    candidates JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- 上下文编译结果摘要：{rendered_chars, candidates_shown, candidates_total,
    -- truncation_stage, has_conflict, ...}。版本/冲突标记/预算的落点。
    context_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    model_version VARCHAR(128) NOT NULL DEFAULT '',
    prompt_version VARCHAR(64) NOT NULL DEFAULT '',
    answer TEXT NOT NULL DEFAULT '',
    -- 引用清单（与 assistant message 的 knowledge_references 对齐的稳定子集）：
    -- [{knowledge_id, chunk_id, index, title}]。
    citations JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- 当时权限基线摘要：{lifecycle_status, classification, acl_versions, ...}。
    permission_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- '' = 正常回答；refused | conflict | no_evidence。
    refusal VARCHAR(16) NOT NULL DEFAULT '',
    -- 哈希链锚点：row = sha256_hex(prev + "\n" + 规范化JSON载荷)，链首 GENESIS。
    hash_chain_prev VARCHAR(64) NOT NULL DEFAULT '',
    hash_chain_row VARCHAR(64) NOT NULL,
    retention_until TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_evidence_packs_request_id UNIQUE (request_id)
);

-- 租户维度的时间线扫描（按租户审计回放）与会话维度回放（问答详情页）。
CREATE INDEX IF NOT EXISTS idx_evidence_packs_tenant_time ON evidence_packs (tenant_id, created_at);
CREATE INDEX IF NOT EXISTS idx_evidence_packs_session ON evidence_packs (session_id);
-- 保留策略清理扫描：retention sweep 按 retention_until 找到期行。
CREATE INDEX IF NOT EXISTS idx_evidence_packs_retention ON evidence_packs (retention_until);
