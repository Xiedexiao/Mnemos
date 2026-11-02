DROP INDEX IF EXISTS idx_acl_ceiling_violations_tenant;
DROP TABLE IF EXISTS acl_ceiling_violations;
DROP INDEX IF EXISTS idx_folder_acl_path;
DROP INDEX IF EXISTS idx_folder_acl_lookup;
DROP TABLE IF EXISTS folder_acl;
ALTER TABLE knowledge_bases DROP COLUMN IF EXISTS expires_at;
ALTER TABLE knowledge_bases DROP COLUMN IF EXISTS require_review;
ALTER TABLE knowledge_bases DROP COLUMN IF EXISTS default_classification;
ALTER TABLE knowledge_bases DROP COLUMN IF EXISTS space_kind;
