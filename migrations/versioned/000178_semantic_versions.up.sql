DO $$ BEGIN RAISE NOTICE '[Migration 000178] Adding semantic version columns...'; END $$;

-- 语义化版本号（KnowAxis V3.0 §7.4）。
--
-- 此前只有单调递增的 version（1、2、3…）：读者不知道这次改动是措辞修正
-- 还是参数变更，也无法按"主.次.修订"理解版本强度。
--
-- 存量行回填为 "<version>.0.0"：它们没有原始 bump 语义，用递增号当主版本
-- 既保持单调、也不会与之后真正的主版本冲突（之后的主版本从该值继续递增）。

ALTER TABLE knowledge_versions
    ADD COLUMN IF NOT EXISTS semantic_version VARCHAR(24) NOT NULL DEFAULT '';
ALTER TABLE knowledge_versions
    ADD COLUMN IF NOT EXISTS bump_size VARCHAR(8) NOT NULL DEFAULT '';
-- 回滚产生的新版本记录"还原自哪一版"，否则历史上突然出现的旧内容无法解释。
ALTER TABLE knowledge_versions
    ADD COLUMN IF NOT EXISTS rolled_back_from VARCHAR(24) NOT NULL DEFAULT '';

UPDATE knowledge_versions
SET semantic_version = CAST(version AS VARCHAR) || '.0.0'
WHERE semantic_version = '';
