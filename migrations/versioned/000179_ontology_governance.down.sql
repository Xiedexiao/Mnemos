DROP INDEX IF EXISTS idx_ontology_schemas_governance;
ALTER TABLE ontology_schemas DROP COLUMN IF EXISTS domain;
ALTER TABLE ontology_schemas DROP COLUMN IF EXISTS governance_level;
