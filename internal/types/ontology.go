package types

import (
	"time"
)

// Ontology schema governance (KnowAxis V3.0 §8: 本体 = 类型/属性/关系/约束/别名).
// Phase B stage 1 delivers the schema governance base: hand-authored schemas,
// versioning on publish and an immutable published state. The AI-driven
// modeling surface (SchemaProposal) is reserved and plugs in on top of these
// types later. Field semantics follow 调研报告 §3.2 with SKOS/OWL2-aligned
// naming (display_name/aliases, data_type, cardinality).

// OntologySchemaStatus 本体 Schema 的治理状态。draft 可编辑；published 发布后
// 不可变（新内容走下一版本）；archived 退役留存。
type OntologySchemaStatus string

const (
	// OntologySchemaStatusDraft 草稿：类型/属性/关系可自由增删改。
	OntologySchemaStatusDraft OntologySchemaStatus = "draft"
	// OntologySchemaStatusPublished 已发布：结构不可变，版本号已递增。
	OntologySchemaStatusPublished OntologySchemaStatus = "published"
	// OntologySchemaStatusArchived 已归档：退役留存，只读。
	OntologySchemaStatusArchived OntologySchemaStatus = "archived"
)

// Valid reports whether the value is a known schema status.
func (s OntologySchemaStatus) Valid() bool {
	switch s {
	case OntologySchemaStatusDraft, OntologySchemaStatusPublished, OntologySchemaStatusArchived:
		return true
	default:
		return false
	}
}

// CanTransitionTo reports whether the status move is legal. Publish is the
// only forward action; archived is terminal.
func (s OntologySchemaStatus) CanTransitionTo(next OntologySchemaStatus) bool {
	if s == next {
		return false
	}
	switch s {
	case OntologySchemaStatusDraft:
		return next == OntologySchemaStatusPublished || next == OntologySchemaStatusArchived
	case OntologySchemaStatusPublished:
		return next == OntologySchemaStatusArchived
	default:
		return false
	}
}

// Editable reports whether the schema structure may be modified. Only drafts
// are editable: a published schema is an immutable historical fact.
func (s OntologySchemaStatus) Editable() bool {
	return s == OntologySchemaStatusDraft
}

// String renders the status for logs.
func (s OntologySchemaStatus) String() string { return string(s) }

// OntologyPropertyDataType 属性数据类型，命名对齐 OWL 2（便于未来导出标准本体）。
type OntologyPropertyDataType string

const (
	// OntologyPropertyDataTypeString 字符串。
	OntologyPropertyDataTypeString OntologyPropertyDataType = "string"
	// OntologyPropertyDataTypeNumber 数值。
	OntologyPropertyDataTypeNumber OntologyPropertyDataType = "number"
	// OntologyPropertyDataTypeBoolean 布尔。
	OntologyPropertyDataTypeBoolean OntologyPropertyDataType = "boolean"
	// OntologyPropertyDataTypeDate 日期/时间。
	OntologyPropertyDataTypeDate OntologyPropertyDataType = "date"
	// OntologyPropertyDataTypeEnum 枚举（枚举值存 config.enum_values）。
	OntologyPropertyDataTypeEnum OntologyPropertyDataType = "enum"
	// OntologyPropertyDataTypeRef 引用（指向其他类型的实体）。
	OntologyPropertyDataTypeRef OntologyPropertyDataType = "ref"
)

// Valid reports whether the value is a known property data type.
func (d OntologyPropertyDataType) Valid() bool {
	switch d {
	case OntologyPropertyDataTypeString, OntologyPropertyDataTypeNumber,
		OntologyPropertyDataTypeBoolean, OntologyPropertyDataTypeDate,
		OntologyPropertyDataTypeEnum, OntologyPropertyDataTypeRef:
		return true
	default:
		return false
	}
}

// OntologyRelationCardinality 关系基数（两端类型约束下的多重性）。
type OntologyRelationCardinality string

const (
	// OntologyCardinalityOneToOne 一对一。
	OntologyCardinalityOneToOne OntologyRelationCardinality = "one_to_one"
	// OntologyCardinalityOneToMany 一对多。
	OntologyCardinalityOneToMany OntologyRelationCardinality = "one_to_many"
	// OntologyCardinalityManyToMany 多对多（迁移默认值 'many' 归一化到该值）。
	OntologyCardinalityManyToMany OntologyRelationCardinality = "many_to_many"
)

// Valid reports whether the value is a known cardinality. The migration
// default 'many' is accepted as a legacy spelling of many_to_many.
func (c OntologyRelationCardinality) Valid() bool {
	switch c {
	case OntologyCardinalityOneToOne, OntologyCardinalityOneToMany,
		OntologyCardinalityManyToMany, "many":
		return true
	default:
		return false
	}
}

// Normalize maps the migration default 'many' (and empty) to many_to_many so
// stored values and API responses always use the canonical spelling.
func (c OntologyRelationCardinality) Normalize() OntologyRelationCardinality {
	if c == "" || c == "many" {
		return OntologyCardinalityManyToMany
	}
	return c
}

// OntologySchema 本体 Schema（租户内按名称唯一）。version 在每次发布时 +1，
// 表示"第几版已发布结构"；draft 行的 version 始终指向其来源版本。
type OntologySchema struct {
	ID          string               `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID    uint64               `json:"tenant_id" gorm:"type:integer;not null"`
	Name        string               `json:"name" gorm:"type:varchar(128);not null"`
	Description string               `json:"description" gorm:"type:text;not null;default:''"`
	Version     int                  `json:"version" gorm:"not null;default:1"`
	Status      OntologySchemaStatus `json:"status" gorm:"type:varchar(16);not null;default:'draft'"`
	CreatedBy   string               `json:"created_by" gorm:"type:varchar(36);not null;default:''"`
	CreatedAt   time.Time            `json:"created_at"`
	UpdatedAt   time.Time            `json:"updated_at"`
}

// TableName pins the GORM table name.
func (OntologySchema) TableName() string { return "ontology_schemas" }

// OntologyType 本体类型（实体/事件/概念的机器名词汇表，如 Device/SOP/ErrorCode）。
// Name 是 schema 内唯一的机器名；DisplayName 对齐 SKOS prefLabel；Aliases 对齐
// SKOS altLabel（参与检索匹配）。Config 携带 L0-L4 治理级别等扩展元数据。
type OntologyType struct {
	ID          string    `json:"id" gorm:"type:varchar(36);primaryKey"`
	SchemaID    string    `json:"schema_id" gorm:"type:varchar(36);not null;index"`
	Name        string    `json:"name" gorm:"type:varchar(128);not null"`
	DisplayName string    `json:"display_name" gorm:"type:varchar(128);not null;default:''"`
	Description string    `json:"description" gorm:"type:text;not null;default:''"`
	Aliases     JSON      `json:"aliases" gorm:"type:jsonb;default:'[]'"`
	Config      JSON      `json:"config" gorm:"type:jsonb;default:'{}'"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// TableName pins the GORM table name.
func (OntologyType) TableName() string { return "ontology_types" }

// OntologyProperty 类型属性（data_type 对齐 OWL 2；枚举值/单位等约束放 config）。
type OntologyProperty struct {
	ID        string                   `json:"id" gorm:"type:varchar(36);primaryKey"`
	TypeID    string                   `json:"type_id" gorm:"type:varchar(36);not null;index"`
	Name      string                   `json:"name" gorm:"type:varchar(128);not null"`
	DataType  OntologyPropertyDataType `json:"data_type" gorm:"type:varchar(24);not null;default:'string'"`
	Required  bool                     `json:"required" gorm:"not null;default:false"`
	Config    JSON                     `json:"config" gorm:"type:jsonb;default:'{}'"`
	CreatedAt time.Time                `json:"created_at"`
}

// TableName pins the GORM table name.
func (OntologyProperty) TableName() string { return "ontology_properties" }

// OntologyRelation 本体关系（边词汇表，如 belongs_to/depends_on/derived_from）。
// 两端类型必须命中同一 schema 内已定义的类型（KAG 谓词模型）。
type OntologyRelation struct {
	ID           string                      `json:"id" gorm:"type:varchar(36);primaryKey"`
	SchemaID     string                      `json:"schema_id" gorm:"type:varchar(36);not null;index"`
	Name         string                      `json:"name" gorm:"type:varchar(128);not null"`
	SourceTypeID string                      `json:"source_type_id" gorm:"type:varchar(36);not null"`
	TargetTypeID string                      `json:"target_type_id" gorm:"type:varchar(36);not null"`
	Cardinality  OntologyRelationCardinality `json:"cardinality" gorm:"type:varchar(16);not null;default:'many'"`
	Description  string                      `json:"description" gorm:"type:text;not null;default:''"`
	CreatedAt    time.Time                   `json:"created_at"`
}

// TableName pins the GORM table name.
func (OntologyRelation) TableName() string { return "ontology_relations" }

// OntologyTypeDetail is one type with its properties, as returned by the
// aggregated schema read (GetSchema).
type OntologyTypeDetail struct {
	OntologyType
	Properties []*OntologyProperty `json:"properties"`
}

// OntologySchemaDetail is the full structure of one schema: types with their
// properties plus the relation vocabulary.
type OntologySchemaDetail struct {
	Schema    *OntologySchema       `json:"schema"`
	Types     []*OntologyTypeDetail `json:"types"`
	Relations []*OntologyRelation   `json:"relations"`
}

// OntologySchemaUpdate carries the editable fields of a draft schema
// (nil = keep unchanged).
type OntologySchemaUpdate struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
}

// OntologyTypeUpdate replaces the mutable fields of a type.
type OntologyTypeUpdate struct {
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Aliases     JSON   `json:"aliases"`
	Config      JSON   `json:"config"`
}

// OntologyPropertyUpdate replaces the mutable fields of a property.
type OntologyPropertyUpdate struct {
	DataType string `json:"data_type"`
	Required bool   `json:"required"`
	Config   JSON   `json:"config"`
}

// OntologyRelationUpdate replaces the mutable fields of a relation.
type OntologyRelationUpdate struct {
	SourceTypeID string `json:"source_type_id"`
	TargetTypeID string `json:"target_type_id"`
	Cardinality  string `json:"cardinality"`
	Description  string `json:"description"`
}

// Ontology audit actions (KnowAxis V3.0 §8: Schema 治理是内容治理的一等事件，
// 每次结构变更都写审计). Typed as AuditAction so entries go straight into the
// hash-chained audit stream.
const (
	// OntologyActionSchemaCreated 创建 Schema。
	OntologyActionSchemaCreated AuditAction = "ontology.schema_created"
	// OntologyActionSchemaUpdated 更新 draft Schema（名称/描述）。
	OntologyActionSchemaUpdated AuditAction = "ontology.schema_updated"
	// OntologyActionSchemaPublished 发布 Schema（draft→published，版本 +1）。
	OntologyActionSchemaPublished AuditAction = "ontology.schema_published"
	// OntologyActionSchemaDeleted 删除 draft Schema。
	OntologyActionSchemaDeleted AuditAction = "ontology.schema_deleted"
	// OntologyActionTypeCreated 新增类型。
	OntologyActionTypeCreated AuditAction = "ontology.type_created"
	// OntologyActionTypeUpdated 更新类型。
	OntologyActionTypeUpdated AuditAction = "ontology.type_updated"
	// OntologyActionTypeDeleted 删除类型（连同其属性）。
	OntologyActionTypeDeleted AuditAction = "ontology.type_deleted"
	// OntologyActionPropertyCreated 新增属性。
	OntologyActionPropertyCreated AuditAction = "ontology.property_created"
	// OntologyActionPropertyUpdated 更新属性。
	OntologyActionPropertyUpdated AuditAction = "ontology.property_updated"
	// OntologyActionPropertyDeleted 删除属性。
	OntologyActionPropertyDeleted AuditAction = "ontology.property_deleted"
	// OntologyActionRelationCreated 新增关系。
	OntologyActionRelationCreated AuditAction = "ontology.relation_created"
	// OntologyActionRelationUpdated 更新关系。
	OntologyActionRelationUpdated AuditAction = "ontology.relation_updated"
	// OntologyActionRelationDeleted 删除关系。
	OntologyActionRelationDeleted AuditAction = "ontology.relation_deleted"
)
