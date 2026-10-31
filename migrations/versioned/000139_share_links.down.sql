DO $$ BEGIN RAISE NOTICE '[Migration 000139] Dropping share_links table...'; END $$;

DROP INDEX IF EXISTS idx_share_links_token;
DROP INDEX IF EXISTS idx_share_links_tenant_knowledge;
DROP TABLE IF EXISTS share_links;
