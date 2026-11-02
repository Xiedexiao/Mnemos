DROP INDEX IF EXISTS idx_approval_tasks_subject;
ALTER TABLE approval_tasks DROP COLUMN IF EXISTS title;
ALTER TABLE approval_tasks DROP COLUMN IF EXISTS subject_id;
ALTER TABLE approval_tasks DROP COLUMN IF EXISTS subject_type;
