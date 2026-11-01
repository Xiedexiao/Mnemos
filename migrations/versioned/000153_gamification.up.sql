DO $$ BEGIN RAISE NOTICE '[Migration 000153] Adding gamification (points/levels + learning paths)...'; END $$;

-- 积分流水（KnowAxis V3.0 §15.4 的 v1 子集）。
--
-- user_points_ledger：每一次加分的只追加账本（append-only）。
--   - action 的词表见 internal/types/gamification.go（knowledge_created/
--     view_given/like_given/review_done/answer_adopted/daily_login，
--     另有 path_completed 由学习路径完成时写入）。
--   - source_type/source_id 记录积分来源（如 knowledge/<id>），
--     服务层按 (user_id, action, source_type, source_id) 查重实现幂等：
--     同一来源同一动作只记一次分。
--   - 日上限防刷在服务层按 (tenant_id, user_id, action, created_at) 聚合，
--     因此本索引同时服务流水查询与防刷聚合。
CREATE TABLE IF NOT EXISTS user_points_ledger (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    action VARCHAR(32) NOT NULL,
    points INT NOT NULL,
    source_type VARCHAR(24) DEFAULT '',
    source_id VARCHAR(36) DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_user_points_ledger_tenant_user_created
    ON user_points_ledger (tenant_id, user_id, created_at);

-- 积分余额 + 等级（KnowAxis V3.0 §15.4）。
--
-- user_point_balance：每人一行（UNIQUE(tenant_id, user_id)），余额只增
-- （v1 无扣分），等级随余额自动晋升：bronze 0 / silver 50 / gold 200 /
-- diamond 500 / expert 1000（阈值表在 internal/types/gamification.go）。
CREATE TABLE IF NOT EXISTS user_point_balance (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    balance INT NOT NULL DEFAULT 0,
    level VARCHAR(16) DEFAULT 'bronze',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_user_point_balance_tenant_user UNIQUE (tenant_id, user_id)
);

-- 学习路径（KnowAxis V3.0 §15.5 的 v1 子集）。
--
-- learning_paths：Admin 维护的"岗位/部门 → 有序知识清单"。
--   - steps 是 [{order, title, knowledge_id, description}] 的 JSONB 数组，
--     order 从 1 起且在服务层保证唯一（完成进度按 order 记账）。
--   - knowledge_id 允许为空串：v1 允许"纯文字步骤"（如线下培训说明）。
CREATE TABLE IF NOT EXISTS learning_paths (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    name VARCHAR(128) NOT NULL,
    audience VARCHAR(64) DEFAULT '',
    steps JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_by VARCHAR(36) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_learning_paths_tenant_created
    ON learning_paths (tenant_id, created_at);

-- 学习进度（KnowAxis V3.0 §15.5）。
--
-- learning_progress：用户在一条路径上的完成账。
--   - completed_steps 是已完成 step order 的整数数组 JSONB。
--   - progress_pct = 完成步数 / 总步数（服务层计算，0-100）。
--   - 100% 时 completed_at 落库并触发 path_completed 加分。
--   - UNIQUE(path_id, user_id)：同一用户在同一路径至多一条进度。
CREATE TABLE IF NOT EXISTS learning_progress (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    path_id VARCHAR(36) NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    completed_steps JSONB DEFAULT '[]'::jsonb,
    progress_pct INT DEFAULT 0,
    completed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_learning_progress_path_user UNIQUE (path_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_learning_progress_tenant_user
    ON learning_progress (tenant_id, user_id);
