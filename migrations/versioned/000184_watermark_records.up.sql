DO $$ BEGIN RAISE NOTICE '[Migration 000184] Adding watermark download records...'; END $$;

-- 水印下载台账（KnowAxis V3.0 §16.3 "密级与安全：... 水印、下载、导出 ..."）。
--
-- 平台此前只把水印文本交给前端渲染，一旦文件被下载走就毫无痕迹。台账是
-- 唯一在文件被复制、裁剪、转存之后仍然成立的证据：它记的是"谁在什么时候
-- 取走了哪一版"。
--
-- 只记 internal 及以上的下载。给每一次公开文件下载都写一行会让台账淹没
-- 真正需要追溯的受控下载。

CREATE TABLE IF NOT EXISTS watermark_records (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL DEFAULT '',
    knowledge_base_id VARCHAR(36) NOT NULL DEFAULT '',
    file_name VARCHAR(512) NOT NULL DEFAULT '',
    -- 下载当时解析出的密级：之后升密不应改写历史
    classification VARCHAR(16) NOT NULL DEFAULT 'internal',
    -- none | footer | tile | full
    strength VARCHAR(16) NOT NULL DEFAULT 'footer',
    -- injected（已写入正文）| sidecar（随包说明）
    mode VARCHAR(16) NOT NULL DEFAULT 'sidecar',
    watermark_text VARCHAR(255) NOT NULL DEFAULT '',
    -- download | export
    source VARCHAR(16) NOT NULL DEFAULT 'download',
    request_id VARCHAR(36) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_watermark_records_tenant
    ON watermark_records (tenant_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_watermark_records_knowledge
    ON watermark_records (tenant_id, knowledge_id, created_at DESC);
