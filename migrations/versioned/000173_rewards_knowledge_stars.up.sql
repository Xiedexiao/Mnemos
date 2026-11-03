DO $$ BEGIN RAISE NOTICE '[Migration 000173] Adding reward redemption and knowledge stars...'; END $$;

-- 积分兑换 + 知识之星（KnowAxis V3.0 §15.4）。
--
-- §15.4 要求"每月/每季度评选知识之星并公开表彰；积分可兑换礼品/福利"。
-- 平台此前只有积分流水与等级：没有可兑换目录，也没有一次评选的定稿记录。
--
-- 兑换是第一个会扣减积分的动作。扣减、库存与兑换单必须同事务，所以在
-- reward_redemptions 上留存成交快照（item_name / points_cost），目录项之后
-- 改名或删除都不影响历史单据。

CREATE TABLE IF NOT EXISTS reward_catalog_items (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    name VARCHAR(128) NOT NULL,
    description VARCHAR(1024) NOT NULL DEFAULT '',
    cost_points INTEGER NOT NULL,
    -- -1 表示不限量
    stock INTEGER NOT NULL DEFAULT -1,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_by VARCHAR(36) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_reward_items_tenant
    ON reward_catalog_items (tenant_id, enabled, cost_points);

CREATE TABLE IF NOT EXISTS reward_redemptions (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    item_id VARCHAR(36) NOT NULL,
    -- 成交快照：目录改名/删除后历史单据仍可读
    item_name VARCHAR(128) NOT NULL,
    points_cost INTEGER NOT NULL,
    -- pending | fulfilled | rejected | canceled
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    note VARCHAR(512) NOT NULL DEFAULT '',
    handled_by VARCHAR(36) NOT NULL DEFAULT '',
    handled_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_reward_redemptions_user
    ON reward_redemptions (tenant_id, user_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_reward_redemptions_status
    ON reward_redemptions (tenant_id, status, created_at DESC);

CREATE TABLE IF NOT EXISTS knowledge_star_awards (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    -- month | quarter
    period_type VARCHAR(16) NOT NULL,
    -- YYYY-MM 或 YYYY-Qn
    period VARCHAR(16) NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    rank INTEGER NOT NULL DEFAULT 0,
    score INTEGER NOT NULL DEFAULT 0,
    reason VARCHAR(512) NOT NULL DEFAULT '',
    published BOOLEAN NOT NULL DEFAULT FALSE,
    awarded_by VARCHAR(36) NOT NULL DEFAULT '',
    awarded_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 同一期同一人只能有一条获奖记录（重选覆盖，不追加）。
CREATE UNIQUE INDEX IF NOT EXISTS uq_knowledge_star_period_user
    ON knowledge_star_awards (tenant_id, period_type, period, user_id);
CREATE INDEX IF NOT EXISTS idx_knowledge_star_tenant
    ON knowledge_star_awards (tenant_id, published, period DESC);
