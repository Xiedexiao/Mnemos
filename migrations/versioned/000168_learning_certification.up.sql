DO $$ BEGIN RAISE NOTICE '[Migration 000168] Adding learning path certification...'; END $$;

-- 学习路径认证（KnowAxis V3.0 §15.5）。
--
-- 平台已有学习路径与逐步完成记录（000153），但没有"完成后获得认证"这一层：
-- 进度到 100% 只是 CompletedAt 被填上，无法回答"这个人是否已被认证可以独立
-- 上岗"。§15.5 把认证与上岗资格关联，因此需要一张可查、可撤销的证书表。
--
-- 路径的阶段（基础 → 进阶 → 熟练）此前只存在于 Audience 自由文本里；这里
-- 给它一个结构化字段，认证可以按阶段发。
ALTER TABLE learning_paths ADD COLUMN IF NOT EXISTS stage VARCHAR(16) NOT NULL DEFAULT 'basic';

CREATE TABLE IF NOT EXISTS learning_certificates (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    -- 证书持有的用户
    user_id VARCHAR(36) NOT NULL,
    -- 对应的学习路径与阶段
    path_id VARCHAR(36) NOT NULL,
    stage VARCHAR(16) NOT NULL DEFAULT 'basic',
    -- 证书编号（租户内唯一，可对外引用）
    serial VARCHAR(48) NOT NULL,
    -- active | revoked
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    issued_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- 到期：上岗资格需要周期性复核，过期自动失效
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    revoked_reason VARCHAR(255) NOT NULL DEFAULT '',
    CONSTRAINT uq_learning_certificates_serial UNIQUE (tenant_id, serial)
);

CREATE INDEX IF NOT EXISTS idx_learning_certificates_user
    ON learning_certificates (tenant_id, user_id, status);
CREATE INDEX IF NOT EXISTS idx_learning_certificates_path
    ON learning_certificates (tenant_id, path_id, status);
