DO $$ BEGIN RAISE NOTICE '[Migration 000150] Adding export control and DLP findings...'; END $$;

-- 导出管控 + DLP 输出扫描（KnowAxis V3.0 §2.2 export 动作 / §2.4 标识 /
-- §13 生成内容校验 v1）。
--
-- export_requests：一次导出申请（knowledge 知识库导出 / evidence_pack 证据
-- 包导出 / audit 审计导出）的申请-审批-执行状态机。审批策略 v1：
--   - max_classification = secret/confidential 或 scope = audit：必须人工
--     审批（Admin 且 ≠ 申请人，四眼）才可 fulfill；
--   - public/internal 且非审计导出：Create 内直接置 approved（自动放行，
--     decision_note 标注 auto-approved）。
-- watermark_text 由服务端生成（"用户名-时间-随机4位"），fulfill 时返回给
-- 前端渲染导出物水印；文档级水印渲染与离线 DRM 不在本期范围。
--
-- scope_filter 记录导出范围过滤条件（kb_ids / 条数上限 / 时间窗等），结构
-- 按 scope 各异，v1 不做严格 schema 校验，只保证是 JSON 对象。
CREATE TABLE IF NOT EXISTS export_requests (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    requested_by VARCHAR(36) NOT NULL,
    -- knowledge | evidence_pack | audit。
    scope VARCHAR(16) NOT NULL,
    -- 导出范围过滤：{kb_ids: [...], limit: n, time_from: ..., time_to: ...}。
    scope_filter JSONB DEFAULT '{}'::jsonb,
    -- 本次导出允许的最高密级（public|internal|secret|confidential）。
    max_classification VARCHAR(16) NOT NULL DEFAULT 'internal',
    -- pending | approved | rejected | fulfilled。
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    -- 审批人（自动 approve 时为空串，decision_note 标注 auto-approved）。
    approver_id VARCHAR(36) DEFAULT '',
    decision_note TEXT DEFAULT '',
    watermark_text VARCHAR(255) DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    decided_at TIMESTAMPTZ,
    fulfilled_at TIMESTAMPTZ
);

-- 待办列表（Admin 按 status 扫审批队列）与申请人时间线扫描。
CREATE INDEX IF NOT EXISTS idx_export_requests_tenant_status
    ON export_requests (tenant_id, status);

-- dlp_findings：DLP 输出扫描命中记录（v1 只做 masked 动作；logged/blocked
-- 留给后续动作策略）。问答输出、导出物等内部管线调用 ScanText 时逐命中
-- 落行：finding_type 标类别，matched_sample 是脱敏后的样本（只留前后 2 位，
-- 中间打星），request_id/message_id 指向命中发生的请求与消息。
--
-- 与 evidence_packs 的哈希链不同，findings 是普通事件流水：按租户 + 时间
-- 扫描（Admin 巡检），不需要链式防篡改（源头是服务自身写入）。
CREATE TABLE IF NOT EXISTS dlp_findings (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    request_id VARCHAR(64) NOT NULL,
    message_id VARCHAR(36) DEFAULT '',
    -- phone | id_card | bank_card | email | custom_pattern。
    finding_type VARCHAR(32) NOT NULL,
    -- 脱敏后的命中样本（保留前 2 后 2，中间以 * 占位；最长 64）。
    matched_sample VARCHAR(64) DEFAULT '',
    -- logged | masked | blocked（v1 ScanText 只落 masked）。
    action VARCHAR(16) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Admin 巡检扫描：按租户 + 时间倒序翻页。
CREATE INDEX IF NOT EXISTS idx_dlp_findings_tenant_time
    ON dlp_findings (tenant_id, created_at);
