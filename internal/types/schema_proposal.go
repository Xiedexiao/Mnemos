package types

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
)

// ============================================================================
// SchemaProposal：文档驱动 AI 自动建模 + 提案治理（KnowAxis V3.0 §8.2/§8.4）。
//
// 闭环：知识文档 →（LLM 结构化抽取 | 人工提交）→ pending 提案 → 兼容性
// 检查（新增类型可自动通过 / 同名类型属性变更 warning / 关系端点缺失
// blocker）→ Admin 四眼审批（approve|reject）→ Apply 落成正式本体（
// ontology_schemas 的 draft 版本，或追加到同名 draft）。
//
// 提案不是本体：AI 的输出必须经人批准并落库后才会约束后续抽取。这一层
// 隔离正是调研报告 §3.2 schema_proposals 表建议的核心目的（"changes 由
// 评审确认，而不是直接改 schema"）。
// ============================================================================

// SchemaProposalStatus 提案治理状态。pending 待审；approved 已批准待落地；
// rejected 驳回终态；applied 已落成本体终态。
type SchemaProposalStatus string

const (
	// SchemaProposalStatusPending 待审批：AI/人工生成后即处于该状态。
	SchemaProposalStatusPending SchemaProposalStatus = "pending"
	// SchemaProposalStatusApproved 已批准：等待 Apply 落成本体 Schema。
	SchemaProposalStatusApproved SchemaProposalStatus = "approved"
	// SchemaProposalStatusRejected 已驳回：终态，不再可落地。
	SchemaProposalStatusRejected SchemaProposalStatus = "rejected"
	// SchemaProposalStatusApplied 已落地：终态，applied_schema_id 指向产物。
	SchemaProposalStatusApplied SchemaProposalStatus = "applied"
)

// Valid reports whether the value is a known proposal status.
func (s SchemaProposalStatus) Valid() bool {
	switch s {
	case SchemaProposalStatusPending, SchemaProposalStatusApproved,
		SchemaProposalStatusRejected, SchemaProposalStatusApplied:
		return true
	default:
		return false
	}
}

// CanTransitionTo reports whether the status move is legal. pending is the
// only entry state; approved→applied is the only path that touches a schema.
func (s SchemaProposalStatus) CanTransitionTo(next SchemaProposalStatus) bool {
	if s == next {
		return false
	}
	switch s {
	case SchemaProposalStatusPending:
		return next == SchemaProposalStatusApproved || next == SchemaProposalStatusRejected
	case SchemaProposalStatusApproved:
		return next == SchemaProposalStatusApplied
	default:
		return false
	}
}

// Terminal reports whether no further transition is possible.
func (s SchemaProposalStatus) Terminal() bool {
	return s == SchemaProposalStatusRejected || s == SchemaProposalStatusApplied
}

// String renders the status for logs.
func (s SchemaProposalStatus) String() string { return string(s) }

// ProposalProperty 提案中的一个属性槽位。Data 与 ontology_properties.data_type
// 同一词表（string/number/boolean/date/enum/ref）。
type ProposalProperty struct {
	Name     string `json:"name"`
	DataType string `json:"data_type"`
	Required bool   `json:"required"`
}

// ProposalType 提案中的一个类型。Aliases 对齐 SKOS altLabel（参与检索匹配，
// 也用于兼容性检查的"疑似改名"识别）。
type ProposalType struct {
	Name        string             `json:"name"`
	DisplayName string             `json:"display_name"`
	Description string             `json:"description"`
	Aliases     []string           `json:"aliases"`
	Properties  []ProposalProperty `json:"properties"`
}

// ProposalRelation 提案中的一条关系。SourceType/TargetType 是类型机器名
// （不是 ID）：提案在被 Apply 之前不知道目标 schema 的 ID。
type ProposalRelation struct {
	Name        string `json:"name"`
	SourceType  string `json:"source_type"`
	TargetType  string `json:"target_type"`
	Cardinality string `json:"cardinality"`
	Description string `json:"description"`
}

// ProposalPayload 提案的结构化内容（存 schema_proposals.proposal，JSON 同构）。
type ProposalPayload struct {
	Types     []ProposalType     `json:"types"`
	Relations []ProposalRelation `json:"relations"`
}

// Normalize trims and fills defaults in place, deduplicating aliases and
// dropping empty entries. It does not validate cross references; call Validate
// afterwards (Apply re-validates against the live schema anyway).
func (p *ProposalPayload) Normalize() {
	if p == nil {
		return
	}
	types := p.Types[:0]
	for _, t := range p.Types {
		t.Name = strings.TrimSpace(t.Name)
		if t.Name == "" {
			continue
		}
		t.DisplayName = strings.TrimSpace(t.DisplayName)
		if t.DisplayName == "" {
			t.DisplayName = t.Name
		}
		t.Description = strings.TrimSpace(t.Description)
		t.Aliases = normalizeProposalAliases(t.Aliases)
		properties := t.Properties[:0]
		for _, prop := range t.Properties {
			prop.Name = strings.TrimSpace(prop.Name)
			if prop.Name == "" {
				continue
			}
			prop.DataType = strings.TrimSpace(prop.DataType)
			if prop.DataType == "" {
				prop.DataType = string(OntologyPropertyDataTypeString)
			}
			properties = append(properties, prop)
		}
		t.Properties = properties
		types = append(types, t)
	}
	p.Types = types

	relations := p.Relations[:0]
	for _, rel := range p.Relations {
		rel.Name = strings.TrimSpace(rel.Name)
		rel.SourceType = strings.TrimSpace(rel.SourceType)
		rel.TargetType = strings.TrimSpace(rel.TargetType)
		rel.Description = strings.TrimSpace(rel.Description)
		rel.Cardinality = string(OntologyRelationCardinality(rel.Cardinality).Normalize())
		if rel.Name == "" {
			continue
		}
		relations = append(relations, rel)
	}
	p.Relations = relations
}

// Validate checks the structural contract of a proposal payload. Endpoint
// existence is deliberately NOT checked here: a proposal may legally extend a
// type that already exists in the target schema, and only the compatibility
// check (which can see the live schema) can tell the two cases apart.
func (p *ProposalPayload) Validate() error {
	if p == nil {
		return errors.New("proposal payload is required")
	}
	if len(p.Types) == 0 {
		return errors.New("proposal must define at least one type")
	}
	seenTypes := make(map[string]struct{}, len(p.Types))
	for _, t := range p.Types {
		if err := validateProposalName(t.Name, "type"); err != nil {
			return err
		}
		key := strings.ToLower(t.Name)
		if _, dup := seenTypes[key]; dup {
			return errors.New("duplicate type in proposal: " + t.Name)
		}
		seenTypes[key] = struct{}{}
		seenProps := make(map[string]struct{}, len(t.Properties))
		for _, prop := range t.Properties {
			if err := validateProposalName(prop.Name, "property"); err != nil {
				return err
			}
			if !OntologyPropertyDataType(prop.DataType).Valid() {
				return errors.New("unknown property data type " + prop.DataType + " on type " + t.Name)
			}
			key := strings.ToLower(prop.Name)
			if _, dup := seenProps[key]; dup {
				return errors.New("duplicate property " + prop.Name + " on type " + t.Name)
			}
			seenProps[key] = struct{}{}
		}
	}
	seenRelations := make(map[string]struct{}, len(p.Relations))
	for _, rel := range p.Relations {
		if err := validateProposalName(rel.Name, "relation"); err != nil {
			return err
		}
		if rel.SourceType == "" || rel.TargetType == "" {
			return errors.New("relation " + rel.Name + " must declare source_type and target_type")
		}
		if !OntologyRelationCardinality(rel.Cardinality).Valid() {
			return errors.New("unknown relation cardinality " + rel.Cardinality + " on relation " + rel.Name)
		}
		key := strings.ToLower(rel.Name)
		if _, dup := seenRelations[key]; dup {
			return errors.New("duplicate relation in proposal: " + rel.Name)
		}
		seenRelations[key] = struct{}{}
	}
	return nil
}

// SchemaProposal 本体建模提案（表 schema_proposals）。提案是治理对象，不是
// 生效结构；只有 Apply 通过后才由 OntologyService 落成本体 Schema。
type SchemaProposal struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"type:integer;not null"`
	// Name 是提案名，同时也是 Apply 时目标 schema 的名字（同名 schema 已
	// 存在则追加到其 draft 版本）。
	Name   string `json:"name" gorm:"type:varchar(128);not null"`
	Domain string `json:"domain" gorm:"type:varchar(64);not null;default:''"`
	// SourceKnowledgeIDs 输入文档 ID 数组（服务层限制 ≤8 篇）。
	SourceKnowledgeIDs JSON `json:"source_knowledge_ids" gorm:"column:source_knowledge_ids;type:jsonb;not null;default:'[]'"`
	// Proposal 结构化的类型/关系提案（ProposalPayload 的 JSONB 形态）。
	Proposal JSON `json:"proposal" gorm:"type:jsonb;not null;default:'{}'"`
	// Rationale 提案动机（AI 生成时说明"哪些文档出现了当前 schema 无法表达
	// 的概念"；人工提交时由提交人填写）。
	Rationale string               `json:"rationale" gorm:"type:text;not null;default:''"`
	Status    SchemaProposalStatus `json:"status" gorm:"type:varchar(16);not null;default:'pending'"`
	// ReviewedBy 审批人（四眼：必须 != CreatedBy，服务层强制）。
	ReviewedBy    string `json:"reviewed_by" gorm:"type:varchar(36);not null;default:''"`
	ReviewComment string `json:"review_comment" gorm:"type:text;not null;default:''"`
	// AppliedSchemaID Apply 成功后回填的 ontology_schemas.id。
	AppliedSchemaID string `json:"applied_schema_id" gorm:"type:varchar(36);not null;default:''"`
	// CompatibilityReport CheckCompatibility 的结果（CompatibilityReport
	// 的 JSONB 形态）；尚未检查时为空对象，Apply 要求 compatible。
	CompatibilityReport JSON      `json:"compatibility_report" gorm:"type:jsonb;not null;default:'{}'"`
	CreatedBy           string    `json:"created_by" gorm:"type:varchar(36);not null"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// TableName pins the GORM table name.
func (SchemaProposal) TableName() string { return "schema_proposals" }

// PayloadStruct decodes the stored proposal JSON into a typed payload.
func (p *SchemaProposal) PayloadStruct() (*ProposalPayload, error) {
	if p == nil {
		return nil, errors.New("schema proposal is required")
	}
	raw := strings.TrimSpace(string(p.Proposal))
	if raw == "" || raw == "null" || raw == "{}" {
		return &ProposalPayload{}, nil
	}
	var payload ProposalPayload
	if err := json.Unmarshal(p.Proposal, &payload); err != nil {
		return nil, errors.New("schema proposal payload is not valid JSON: " + err.Error())
	}
	return &payload, nil
}

// KnowledgeIDList decodes source_knowledge_ids into a string slice.
func (p *SchemaProposal) KnowledgeIDList() ([]string, error) {
	if p == nil {
		return nil, nil
	}
	raw := strings.TrimSpace(string(p.SourceKnowledgeIDs))
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var ids []string
	if err := json.Unmarshal(p.SourceKnowledgeIDs, &ids); err != nil {
		return nil, errors.New("schema proposal source_knowledge_ids is not a JSON array: " + err.Error())
	}
	return ids, nil
}

// CompatibilityReportStruct decodes the stored compatibility report. A nil
// report (never checked) decodes to an empty report rather than an error.
func (p *SchemaProposal) CompatibilityReportStruct() (*CompatibilityReport, error) {
	if p == nil {
		return nil, errors.New("schema proposal is required")
	}
	raw := strings.TrimSpace(string(p.CompatibilityReport))
	if raw == "" || raw == "null" || raw == "{}" {
		return &CompatibilityReport{}, nil
	}
	var report CompatibilityReport
	if err := json.Unmarshal(p.CompatibilityReport, &report); err != nil {
		return nil, errors.New("schema proposal compatibility report is not valid JSON: " + err.Error())
	}
	return &report, nil
}

// CompatibilitySeverity 兼容性检查结论的严重级别。blocker 阻断 Apply；
// warning 提示但放行；info 记录"可自动通过"的增量（新增类型等）。
type CompatibilitySeverity string

const (
	// CompatibilitySeverityBlocker 阻断性冲突（如关系端点在任何 schema 都
	// 不存在）：Apply 必须拒绝。
	CompatibilitySeverityBlocker CompatibilitySeverity = "blocker"
	// CompatibilitySeverityWarning 需要评审关注但不阻断（如同名类型的属性
	// 类型变更、疑似改名）。
	CompatibilitySeverityWarning CompatibilitySeverity = "warning"
	// CompatibilitySeverityInfo 纯增量（新类型/新属性/新关系），可自动通过。
	CompatibilitySeverityInfo CompatibilitySeverity = "info"
)

// Valid reports whether the value is a known severity.
func (s CompatibilitySeverity) Valid() bool {
	switch s {
	case CompatibilitySeverityBlocker, CompatibilitySeverityWarning, CompatibilitySeverityInfo:
		return true
	default:
		return false
	}
}

// 兼容性检查的问题代码（稳定标识，供前端与回归断言使用）。
const (
	// CompatibilityCodeNewType 提案引入了当前本体没有的类型（可自动通过）。
	CompatibilityCodeNewType = "new_type"
	// CompatibilityCodeTypeExists 同名类型已存在（本次只会补充缺失属性）。
	CompatibilityCodeTypeExists = "type_exists"
	// CompatibilityCodePropertyAdded 同名类型新增属性（纯增量）。
	CompatibilityCodePropertyAdded = "property_added"
	// CompatibilityCodePropertyChanged 同名类型的已有属性定义发生变化（warning）。
	CompatibilityCodePropertyChanged = "property_changed"
	// CompatibilityCodePropertyMissing 同名类型的已有属性在提案中缺失（warning：
	// 不会删除线上属性，但说明提案模型与现网不一致）。
	CompatibilityCodePropertyMissing = "property_missing"
	// CompatibilityCodeRelationEndpointMissing 关系端点类型既不在提案中也不在
	// 目标 schema 中（blocker）。
	CompatibilityCodeRelationEndpointMissing = "relation_endpoint_missing"
	// CompatibilityCodeRelationExists 同名关系已存在于目标 schema（Apply 跳过）。
	CompatibilityCodeRelationExists = "relation_exists"
	// CompatibilityCodePossibleRename 提案的新类型与既有类型的名字/别名高度
	// 相似，疑似改名（warning）。
	CompatibilityCodePossibleRename = "possible_rename"
	// CompatibilityCodeProposalInvalid 提案本身结构非法（blocker）。
	CompatibilityCodeProposalInvalid = "proposal_invalid"
)

// CompatibilityIssue 一条兼容性检查结论。
type CompatibilityIssue struct {
	Severity CompatibilitySeverity `json:"severity"`
	Code     string                `json:"code"`
	Message  string                `json:"message"`
	// TypeName / RelationName / PropertyName 定位到具体元素（可为空）。
	TypeName     string `json:"type_name,omitempty"`
	RelationName string `json:"relation_name,omitempty"`
	PropertyName string `json:"property_name,omitempty"`
	// Detail 补充说明（如"string → number"、"endpoint Device not found"）。
	Detail string `json:"detail,omitempty"`
}

// CompatibilitySummary 兼容性结论的计数摘要（前端徽标与回归断言用）。
type CompatibilitySummary struct {
	NewTypes         int `json:"new_types"`
	ExistingTypes    int `json:"existing_types"`
	PropertyChanges  int `json:"property_changes"`
	MissingEndpoints int `json:"missing_endpoints"`
	Blockers         int `json:"blockers"`
	Warnings         int `json:"warnings"`
}

// CompatibilityReport 一次兼容性检查的完整结果（存
// schema_proposals.compatibility_report）。Compatible=false 表示存在 blocker，
// Apply 必须拒绝。
type CompatibilityReport struct {
	ProposalID string    `json:"proposal_id"`
	CheckedAt  time.Time `json:"checked_at"`
	Compatible bool      `json:"compatible"`
	// SchemaName 提案名（Apply 的目标 schema 名）。
	SchemaName string `json:"schema_name"`
	// TargetSchemaID / TargetSchemaStatus 命中的同名 schema（无同名时为空）。
	TargetSchemaID     string `json:"target_schema_id,omitempty"`
	TargetSchemaStatus string `json:"target_schema_status,omitempty"`
	// ComparedSchemaIDs 本次比对过的已发布 schema（审计与调试用）。
	ComparedSchemaIDs []string             `json:"compared_schema_ids,omitempty"`
	Summary           CompatibilitySummary `json:"summary"`
	Issues            []CompatibilityIssue `json:"issues"`
	// PossibleRenames 疑似改名 [{from(现网类型), to(提案类型)}]。
	PossibleRenames []CompatibilityRename `json:"possible_renames,omitempty"`
}

// CompatibilityRename 一条疑似改名（warning）的成对记录。
type CompatibilityRename struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// HasBlocker reports whether the report blocks Apply. An unparsable/empty
// report is treated as "not checked" by the caller, never as "compatible".
func (r *CompatibilityReport) HasBlocker() bool {
	if r == nil {
		return false
	}
	if !r.Compatible {
		return true
	}
	for _, issue := range r.Issues {
		if issue.Severity == CompatibilitySeverityBlocker {
			return true
		}
	}
	return false
}

// SchemaProposalListFilter 提案列表过滤条件（Status 为空不过滤）。
type SchemaProposalListFilter struct {
	Status string `json:"status"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}

// SchemaProposalManualInput 手工提交提案的输入（无模型或想直接改 payload 时
// 使用）。Name 必填；Payload 至少含一个类型。
type SchemaProposalManualInput struct {
	Name               string           `json:"name"`
	Domain             string           `json:"domain"`
	Rationale          string           `json:"rationale"`
	Payload            *ProposalPayload `json:"proposal"`
	SourceKnowledgeIDs []string         `json:"source_knowledge_ids"`
}

// SchemaProposalApplyResult Apply 的落地结果：除提案行本身，还带上"落到了
// 哪个 schema、追加了什么、是否需要在 UI 提示版本升级"。
type SchemaProposalApplyResult struct {
	Proposal *SchemaProposal `json:"proposal"`
	// SchemaID / SchemaName / SchemaVersion 产物 schema 的位置与版本。
	SchemaID      string `json:"schema_id"`
	SchemaName    string `json:"schema_name"`
	SchemaVersion int    `json:"schema_version"`
	// SchemaCreated 本次是否新建了 draft schema（false = 追加到既有 draft）。
	SchemaCreated bool `json:"schema_created"`
	// VersionUpgradeRequired 追加到既有 draft 时的提示：draft 已带有内容，
	// 发布前应确认版本升级（发布时 OntologyService 会把 version+1）。
	VersionUpgradeRequired bool `json:"version_upgrade_required"`
	// CarriedOverTypes / CarriedOverRelations 派生新版本 draft 时从上一发布
	// 版本继承过来的类型与关系数量（同名 schema 已发布时非零）。
	CarriedOverTypes     int `json:"carried_over_types"`
	CarriedOverRelations int `json:"carried_over_relations"`
	TypesAdded           int `json:"types_added"`
	PropertiesAdded      int `json:"properties_added"`
	RelationsAdded       int `json:"relations_added"`
	// Skipped 已存在而跳过的元素（类型名/属性名/关系名），便于评审核对。
	Skipped []string `json:"skipped"`
}

// SchemaProposal audit actions. Same namespace as the ontology.* schema
// actions so one audit query renders the whole §8 governance story.
const (
	// OntologyActionSchemaProposalCreated 提案创建（含 AI 自动建模与手工提交）。
	OntologyActionSchemaProposalCreated AuditAction = "ontology.schema_proposal_created"
	// OntologyActionSchemaProposalChecked 兼容性检查执行完成。
	OntologyActionSchemaProposalChecked AuditAction = "ontology.schema_proposal_checked"
	// OntologyActionSchemaProposalApproved 提案批准。
	OntologyActionSchemaProposalApproved AuditAction = "ontology.schema_proposal_approved"
	// OntologyActionSchemaProposalRejected 提案驳回。
	OntologyActionSchemaProposalRejected AuditAction = "ontology.schema_proposal_rejected"
	// OntologyActionSchemaProposalApplied 提案落地为正式本体 Schema。
	OntologyActionSchemaProposalApplied AuditAction = "ontology.schema_proposal_applied"
)

// normalizeProposalAliases trims, drops empties, and dedupes (case-insensitive)
// an alias list while preserving declaration order.
func normalizeProposalAliases(aliases []string) []string {
	if len(aliases) == 0 {
		return []string{}
	}
	seen := make(map[string]struct{}, len(aliases))
	out := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			continue
		}
		key := strings.ToLower(alias)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, alias)
	}
	return out
}

// validateProposalName enforces the machine-name contract: non-empty, at most
// 128 runes (column width), and no whitespace/control characters.
func validateProposalName(name, kind string) error {
	if name == "" {
		return errors.New(kind + " name is required")
	}
	if len([]rune(name)) > 128 {
		return errors.New(kind + " name exceeds 128 characters: " + name)
	}
	for _, r := range name {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return errors.New(kind + " name must not contain whitespace: " + name)
		}
	}
	return nil
}
