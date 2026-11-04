ALTER TABLE knowledge_versions DROP COLUMN IF EXISTS rolled_back_from;
ALTER TABLE knowledge_versions DROP COLUMN IF EXISTS bump_size;
ALTER TABLE knowledge_versions DROP COLUMN IF EXISTS semantic_version;
