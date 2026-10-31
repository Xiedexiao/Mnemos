DO $$ BEGIN RAISE NOTICE '[Migration 000146] Dropping knowledge comments and recycle bin...'; END $$;

DROP INDEX IF EXISTS idx_recycle_bin_tenant_purge;
DROP TABLE IF EXISTS recycle_bin;

DROP INDEX IF EXISTS idx_knowledge_comments_tenant_knowledge_created;
DROP TABLE IF EXISTS knowledge_comments;
