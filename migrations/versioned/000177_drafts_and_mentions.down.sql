ALTER TABLE knowledge_comments DROP COLUMN IF EXISTS mentions;
DROP INDEX IF EXISTS idx_drafts_user;
DROP TABLE IF EXISTS content_drafts;
