-- Active prompt version pointer (KnowAxis V3.0 §16.5 灰度与回滚).
--
-- 版本表 prompt_versions 是 append-only 的历史；这张表记的是"现在用哪一版"。
-- 把"当前生效"写进版本行会让历史被改写，而历史不可变正是可审计的前提。
-- 一行 = (租户, 模板键)。缺失时行为与改动前一致：取最新版本。
CREATE TABLE IF NOT EXISTS prompt_version_pointers (
    id VARCHAR(36) PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id INTEGER NOT NULL,
    template_key VARCHAR(64) NOT NULL,
    -- 全量用户看到的版本
    stable_version INTEGER NOT NULL DEFAULT 0,
    -- 按比例抽样看到的版本；0 表示不做灰度
    canary_version INTEGER NOT NULL DEFAULT 0,
    canary_percent INTEGER NOT NULL DEFAULT 0,
    updated_by VARCHAR(36) NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_prompt_version_pointer
    ON prompt_version_pointers (tenant_id, template_key);
