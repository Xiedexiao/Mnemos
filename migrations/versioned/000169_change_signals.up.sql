DO $$ BEGIN RAISE NOTICE '[Migration 000169] Adding the change signal catalogue...'; END $$;

-- 变更信号目录（KnowAxis V3.0 §7.3）。
--
-- 更新不能只靠"到期提醒"。§7.3 要求平台持续采集三类信号并为每类定义来源、
-- 置信度与默认动作：
--   1. 内部事件（工单关闭、ECO 发布、组织调整）
--   2. 源系统状态（PLM 图纸作废、制度修订、QMS 异常关闭）
--   3. 外部订阅（法规、国标、行业标准更新）
--   另外还有依赖链、用户反馈、冲突检测与连接器健康四类。
--
-- 平台此前完全没有信号采集：变更单只能由人手工发起，源系统的变更不会自己
-- 变成变更单草稿。本表是信号的统一落地，采集端只负责写入，升级路径（建单/
-- 复审/通知）由服务层决定。
CREATE TABLE IF NOT EXISTS change_signals (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    -- 信号类型（§7.3 七类）
    -- internal_event | source_system | external_subscription | dependency
    -- | user_feedback | conflict | connector_health
    signal_type VARCHAR(32) NOT NULL,
    -- 具体来源标识：连接器 ID、工单号、法规编号、知识 ID 等
    source_id VARCHAR(128) NOT NULL DEFAULT '',
    -- 受影响的知识（可为空：外部法规信号先落库，再按领域匹配知识）
    knowledge_id VARCHAR(36) NOT NULL DEFAULT '',
    knowledge_base_id VARCHAR(36) NOT NULL DEFAULT '',
    -- 信号摘要（人类可读，进入变更单草稿标题）
    summary VARCHAR(512) NOT NULL DEFAULT '',
    -- 置信度：high | mid | low（§7.3 "每类定义置信度"）
    confidence VARCHAR(16) NOT NULL DEFAULT 'mid',
    -- 默认动作：create_cr | review_task | notify | investigate
    default_action VARCHAR(24) NOT NULL DEFAULT 'review_task',
    -- 原始载荷（事件体、法规条目、连接器指标）
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- pending | converted | dismissed
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    -- 转换/驳回后指向的变更单
    change_request_id VARCHAR(36) NOT NULL DEFAULT '',
    handled_by VARCHAR(36) NOT NULL DEFAULT '',
    -- 供去重：同一来源同类信号在窗口内只应产生一条待处理
    dedupe_key VARCHAR(191) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_change_signals_tenant_status
    ON change_signals (tenant_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_change_signals_knowledge
    ON change_signals (knowledge_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_change_signals_pending_dedupe
    ON change_signals (tenant_id, dedupe_key)
    WHERE status = 'pending';
