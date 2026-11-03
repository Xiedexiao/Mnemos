ALTER TABLE change_requests DROP COLUMN IF EXISTS notified_at;
ALTER TABLE change_requests DROP COLUMN IF EXISTS verification_result;
ALTER TABLE change_requests DROP COLUMN IF EXISTS approval_receipt_id;
ALTER TABLE change_requests DROP COLUMN IF EXISTS approval_task_id;
ALTER TABLE change_items DROP COLUMN IF EXISTS replaced_by_id;
