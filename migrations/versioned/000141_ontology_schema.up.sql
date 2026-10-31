DO $$ BEGIN RAISE NOTICE '[Migration 000141] Adding ontology schema tables...'; END $$;

-- Ontology (本体) Schema governance base, Phase B stage 1 (KnowAxis V3.0 §8:
-- 本体 = 类型/属性/关系/约束/别名). Hand-authored schemas only in this stage:
-- manual definition + versioning + publish transition (draft→published,
-- published rows are immutable). The AI-driven modeling surface
-- (schema_proposals) is reserved for the next stage and intentionally NOT
-- created here. Field naming follows 调研报告 §3.2 (ontology_schemas /
-- ontology_types / ontology_properties / ontology_relations), simplified to
-- 4 tables; SKOS/OWL2-aligned columns (display_name/aliases, data_type,
-- cardinality) keep the future standard-ontology export path open.
CREATE TABLE IF NOT EXISTS ontology_schemas (
    id VARCHAR(36) PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id INTEGER NOT NULL,
    name VARCHAR(128) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    version INT NOT NULL DEFAULT 1,
    status VARCHAR(16) NOT NULL DEFAULT 'draft',
    created_by VARCHAR(36) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (tenant_id, name)
);

-- Types are the node vocabulary (Device/SOP/ErrorCode...). aliases mirrors
-- SKOS altLabel (participates in retrieval matching); config carries
-- governance metadata such as the L0-L4 level.
CREATE TABLE IF NOT EXISTS ontology_types (
    id VARCHAR(36) PRIMARY KEY DEFAULT uuid_generate_v4(),
    schema_id VARCHAR(36) NOT NULL,
    name VARCHAR(128) NOT NULL,
    display_name VARCHAR(128) NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    aliases JSONB NOT NULL DEFAULT '[]'::jsonb,
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (schema_id, name)
);

-- Properties are typed slots on a type. data_type aligns with OWL 2 naming:
-- string/number/boolean/date/enum/ref (enum_values and unit live in config).
CREATE TABLE IF NOT EXISTS ontology_properties (
    id VARCHAR(36) PRIMARY KEY DEFAULT uuid_generate_v4(),
    type_id VARCHAR(36) NOT NULL,
    name VARCHAR(128) NOT NULL,
    data_type VARCHAR(24) NOT NULL DEFAULT 'string',
    required BOOLEAN NOT NULL DEFAULT FALSE,
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (type_id, name)
);

-- Relations are the edge vocabulary with both-ends type constraints
-- (KAG 谓词模型: source/target 必须命中本 schema 的类型). The 'many' default
-- is normalized to many_to_many by the application layer.
CREATE TABLE IF NOT EXISTS ontology_relations (
    id VARCHAR(36) PRIMARY KEY DEFAULT uuid_generate_v4(),
    schema_id VARCHAR(36) NOT NULL,
    name VARCHAR(128) NOT NULL,
    source_type_id VARCHAR(36) NOT NULL,
    target_type_id VARCHAR(36) NOT NULL,
    cardinality VARCHAR(16) NOT NULL DEFAULT 'many',
    description TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (schema_id, name)
);

CREATE INDEX IF NOT EXISTS idx_ontology_schemas_tenant_status
    ON ontology_schemas (tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_ontology_types_schema
    ON ontology_types (schema_id);
CREATE INDEX IF NOT EXISTS idx_ontology_properties_type
    ON ontology_properties (type_id);
CREATE INDEX IF NOT EXISTS idx_ontology_relations_schema
    ON ontology_relations (schema_id);
