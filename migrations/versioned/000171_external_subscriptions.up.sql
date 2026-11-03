DO $$ BEGIN RAISE NOTICE '[Migration 000171] Adding external subscription feeds and entries...'; END $$;

-- 外部订阅采集（KnowAxis V3.0 §7.3）。
--
-- §7.3 的"外部订阅"信号（法规、国标、行业标准、监管口径更新）此前没有采集
-- 端：change_signals 表能存这类信号，但只能靠人手工上报。这里补上订阅源与
-- 已见条目两张表，调度器按周期抓取，新条目按领域标签匹配知识并落信号。

-- 订阅源：一个法规/标准站点的一条 Feed。
CREATE TABLE IF NOT EXISTS external_subscription_feeds (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    name VARCHAR(255) NOT NULL,
    feed_url VARCHAR(2048) NOT NULL,
    -- rss | atom | json | auto
    format VARCHAR(16) NOT NULL DEFAULT 'auto',
    -- 领域标签（知识标签名数组），用于把条目匹配到知识
    domain_tags JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- 限定匹配范围；空数组表示租户内全部知识库
    knowledge_base_ids JSONB NOT NULL DEFAULT '[]'::jsonb,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    check_interval_hours INTEGER NOT NULL DEFAULT 24,
    last_checked_at TIMESTAMPTZ,
    last_success_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    -- 可选认证头（"Name: Value"），AES-GCM 加密后存储
    auth_header_enc TEXT NOT NULL DEFAULT '',
    created_by VARCHAR(36) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_external_feeds_tenant
    ON external_subscription_feeds (tenant_id, enabled, last_checked_at);

-- 已见条目：指纹去重，保证同一条标准被源站重发时不会重复建信号。
CREATE TABLE IF NOT EXISTS external_subscription_entries (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    feed_id VARCHAR(36) NOT NULL,
    external_id VARCHAR(255) NOT NULL DEFAULT '',
    title VARCHAR(512) NOT NULL,
    url VARCHAR(2048) NOT NULL DEFAULT '',
    published_at TIMESTAMPTZ,
    fingerprint VARCHAR(64) NOT NULL DEFAULT '',
    matched_knowledge_count INTEGER NOT NULL DEFAULT 0,
    signal_count INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_external_entries_feed_fingerprint
    ON external_subscription_entries (feed_id, fingerprint);
CREATE INDEX IF NOT EXISTS idx_external_entries_feed
    ON external_subscription_entries (tenant_id, feed_id, created_at DESC);
