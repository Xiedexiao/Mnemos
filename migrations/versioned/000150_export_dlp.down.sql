DO $$ BEGIN RAISE NOTICE '[Migration 000150] Dropping export control and DLP findings...'; END $$;

DROP INDEX IF EXISTS idx_dlp_findings_tenant_time;
DROP TABLE IF EXISTS dlp_findings;

DROP INDEX IF EXISTS idx_export_requests_tenant_status;
DROP TABLE IF EXISTS export_requests;
