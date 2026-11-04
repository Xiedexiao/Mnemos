DO $$ BEGIN RAISE NOTICE '[Migration 000181] Adding outbound webhooks...'; END $$;

-- 出站 Webhook（KnowAxis V3.0 §16.4 "知识发布、更新、下架、审批、权限事件
-- 推送；签名与重试"）。
--
-- 平台此前只有入站 API：业务系统想知道"知识发布了"只能轮询，既延迟又浪费
-- 配额，也拿不到事件顺序。
--
-- outbox 模式：事件行与业务变更写在同一事务里，投递在事务外做。这样"业务
-- 提交了但事件丢了"不会发生；反过来"事件投了但业务回滚"由事务保证。

CREATE TABLE IF NOT EXISTS webhook_endpoints (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    url VARCHAR(2048) NOT NULL,
    description VARCHAR(255) NOT NULL DEFAULT '',
    -- 订阅的事件类型数组：knowledge.published / knowledge.updated /
    -- knowledge.delisted / approval.decided / permission.changed
    events JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- 签名密钥（AES-GCM 加密），永不通过 API 返回
    secret_enc TEXT NOT NULL DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    -- 连续最终失败次数；到阈值自动停用，避免死接收方持续消耗重试
    failure_streak INTEGER NOT NULL DEFAULT 0,
    last_success_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    created_by VARCHAR(36) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_webhook_endpoints_tenant
    ON webhook_endpoints (tenant_id, enabled);

CREATE TABLE IF NOT EXISTS webhook_deliveries (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    endpoint_id VARCHAR(36) NOT NULL,
    -- 幂等键：至少一次投递下，接收方按这个 id 去重
    event_id VARCHAR(36) NOT NULL,
    event_type VARCHAR(48) NOT NULL,
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- pending | delivered | dead
    state VARCHAR(16) NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_status_code INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    delivered_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 投递扫描的主力索引：(state, 下次尝试时间)。
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_due
    ON webhook_deliveries (tenant_id, endpoint_id, state, next_attempt_at);
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_tenant
    ON webhook_deliveries (tenant_id, created_at DESC);
