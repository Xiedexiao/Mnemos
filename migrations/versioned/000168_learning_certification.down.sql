DROP INDEX IF EXISTS idx_learning_certificates_path;
DROP INDEX IF EXISTS idx_learning_certificates_user;
DROP TABLE IF EXISTS learning_certificates;
ALTER TABLE learning_paths DROP COLUMN IF EXISTS stage;
