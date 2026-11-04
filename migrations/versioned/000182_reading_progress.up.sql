DO $$ BEGIN RAISE NOTICE '[Migration 000182] Adding reading progress...'; END $$;

-- 阅读进度（KnowAxis V3.0 §15.2 阅读页）。
--
-- 此前"读到哪"只存在前端：换设备即丢，也无法支撑"继续阅读"这一入口。
--
-- 记住位置而不记时间：时间无法换算成"读到哪"，锚点可以直接定位并跨设备
-- 续读。chunk_count 一并存下——文档编辑后分块数量会变，百分比需要按新总数
-- 重新换算，否则"读到 60%" 会漂移到完全不同的段落。

CREATE TABLE IF NOT EXISTS reading_progress (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL,
    chunk_id VARCHAR(36) NOT NULL DEFAULT '',
    chunk_index INTEGER NOT NULL DEFAULT 0,
    percent DOUBLE PRECISION NOT NULL DEFAULT 0,
    chunk_count INTEGER NOT NULL DEFAULT 0,
    completed BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_reading_progress UNIQUE (tenant_id, user_id, knowledge_id)
);

-- "继续阅读"按 (tenant, user) 倒序取最近若干条。
CREATE INDEX IF NOT EXISTS idx_reading_progress_user
    ON reading_progress (tenant_id, user_id, updated_at DESC);
