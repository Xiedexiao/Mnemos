DO $$ BEGIN RAISE NOTICE '[Migration 000140] Adding audit log hash chain columns...'; END $$;

-- Audit tamper-evidence (KnowAxis V3.0 §6.7/§2.8): each row carries
-- prev_hash (previous row of the same tenant) and row_hash =
-- sha256(prev_hash + canonical fields). Append-only semantics already
-- enforced by the model (no UpdatedAt, no soft delete).
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS prev_hash VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS row_hash VARCHAR(64) NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS idx_audit_logs_row_hash ON audit_logs (row_hash);
