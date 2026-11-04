package types

import (
	"strings"
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
	// GovernanceLevel is the §8.2 L0–L4 maturity of this structure. It is
	// stored per schema, not per type: the level describes how much the whole
	// structure can be trusted, so two types in one schema cannot disagree.
	GovernanceLevel string `json:"governance_level" gorm:"type:varchar(4);not null;default:'L0'"`
	// Domain scopes the level floor (price/contract → L2, drawing/HR → L3).
	Domain    string    `json:"domain" gorm:"type:varchar(32);not null;default:''"`
	CreatedBy string    `json:"created_by" gorm:"type:varchar(36);not null;default:''"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the GORM table name.
func (OntologySchema) TableName() string { return "ontology_schemas" }

// OntologyGovernanceLevel 是本体的治理分级（KnowAxis V3.0 §8.2 L0–L4）。
//
// 级别描述"以这套本体产出的知识能被信任到什么程度"，决定三类强制动作：
//   - 是否必须双人审核（L2 以上）；
//   - 结构变更是否需要迁移预演（L3 以上）；
//   - 变更是否需要黄金集回归（L4）。
//
// 级别存在 schema 上而不是类型上：治理强度描述的是"整套结构"的成熟度，
// 同一套结构里不同类型不该有不同的审核门槛。
type OntologyGovernanceLevel string

const (
	// OntologyGovernanceL0 草案：允许自由试验，产出的知识不进入正式问答。
	OntologyGovernanceL0 OntologyGovernanceLevel = "L0"
	// OntologyGovernanceL1 内部试用：单审即可发布，产出知识标记为试点。
	OntologyGovernanceL1 OntologyGovernanceLevel = "L1"
	// OntologyGovernanceL2 受控：双人审核；价格/合同类抽取必须在此级或以上。
	OntologyGovernanceL2 OntologyGovernanceLevel = "L2"
	// OntologyGovernanceL3 生产：双审 + 结构变更需迁移预演（图纸/HR 类）。
	OntologyGovernanceL3 OntologyGovernanceLevel = "L3"
	// OntologyGovernanceL4 关键：双审 + 迁移预演 + 黄金集回归门禁。
	OntologyGovernanceL4 OntologyGovernanceLevel = "L4"
)

// allOntologyGovernanceLevels is the ordering ladder.
var allOntologyGovernanceLevels = []OntologyGovernanceLevel{
	OntologyGovernanceL0, OntologyGovernanceL1, OntologyGovernanceL2,
	OntologyGovernanceL3, OntologyGovernanceL4,
}

// Valid reports whether the level is known.
func (l OntologyGovernanceLevel) Valid() bool {
	for _, candidate := range allOntologyGovernanceLevels {
		if candidate == l {
			return true
		}
	}
	return false
}

// Rank returns the numeric position (L0 = 0).
func (l OntologyGovernanceLevel) Rank() int {
	for index, candidate := range allOntologyGovernanceLevels {
		if candidate == l {
			return index
		}
	}
	return 0
}

// ParseOntologyGovernanceLevel normalizes the stored value; unknown or empty
// falls back to L0 (the least trusted level — fail-closed for governance).
func ParseOntologyGovernanceLevel(raw string) OntologyGovernanceLevel {
	level := OntologyGovernanceLevel(strings.ToUpper(strings.TrimSpace(raw)))
	if level.Valid() {
		return level
	}
	return OntologyGovernanceL0
}

// RequiresDualApproval reports whether content published under this level must
// pass two approvers (§8.2).
func (l OntologyGovernanceLevel) RequiresDualApproval() bool {
	return l.Rank() >= OntologyGovernanceL2.Rank()
}

// RequiresMigrationRehearsal reports whether a structural change needs a
// migration rehearsal (§8.3).
func (l OntologyGovernanceLevel) RequiresMigrationRehearsal() bool {
	return l.Rank() >= OntologyGovernanceL3.Rank()
}

// RequiresGoldenRegression reports whether a change needs a golden-set gate.
func (l OntologyGovernanceLevel) RequiresGoldenRegression() bool {
	return l.Rank() >= OntologyGovernanceL4.Rank()
}

// MinOntologyGovernanceLevelForDomain returns the floor a business domain may
// not go below (§8.2: 价格/合同/图纸/HR 属于强治理域).
//
// 返回 L0 表示该领域没有额外下限。下限是**只能抬高不能降低**的：把价格类
// 本体降到 L1 会让它的产出数字直接进入问答，而这正是设计要阻止的事。
func MinOntologyGovernanceLevelForDomain(domain string) OntologyGovernanceLevel {
	switch strings.ToLower(strings.TrimSpace(domain)) {
	case "price", "pricing", "contract", "contracts", "legal":
		return OntologyGovernanceL2
	case "drawing", "drawings", "plm", "hr", "payroll":
		return OntologyGovernanceL3
	default:
		return OntologyGovernanceL0
	}
}

// EffectiveOntologyGovernanceLevel raises a requested level to the domain
// floor. It never lowers: a caller asking for L4 on a price ontology keeps L4.
func EffectiveOntologyGovernanceLevel(
	requested OntologyGovernanceLevel, domain string,
) OntologyGovernanceLevel {
	floor := MinOntologyGovernanceLevelForDomain(domain)
	if requested.Rank() >= floor.Rank() {
		return requested
	}
	return floor
}

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
	// GovernanceLevel raises (never lowers) the §8.2 level.
	GovernanceLevel *string `json:"governance_level,omitempty"`
	// Domain narrows the level floor.
	Domain *string `json:"domain,omitempty"`
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
