DROP INDEX IF EXISTS idx_approval_tasks_sla;
DROP INDEX IF EXISTS idx_approval_sla_events_task;
DROP TABLE IF EXISTS approval_sla_events;
ALTER TABLE approval_flows DROP COLUMN IF EXISTS escalate_role;
ALTER TABLE approval_flows DROP COLUMN IF EXISTS reminder_hours;
ALTER TABLE approval_flows DROP COLUMN IF EXISTS sla_hours;
ALTER TABLE approval_flows DROP COLUMN IF EXISTS rules;
ALTER TABLE approval_flows DROP COLUMN IF EXISTS version;
