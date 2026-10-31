package types

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 术语表（KnowAxis V3.0 §4.4 的 v1 子集）。
//
// glossary_terms 是租户内一份受控的"标准术语 ↔ 别名"映射：一个标准名 +
// 一组别名/缩写/英文名 + 定义与反例。它服务两个场景：
//   - 术语治理：领域词表由 Admin 维护，deprecated 保留映射（superseded_by_id
//     指向接替术语），供历史回答重定向；
//   - 检索扩展：查询文本分词命中术语后取回全部别名，扩展召回（GlossaryService.
//     ExpandSynonyms；本垂直不接检索管线，只暴露能力）。
// ============================================================================

// GlossaryTermStatus 术语状态。deprecated 不是删除：行保留、映射保留，
// superseded_by_id 指向接替术语。
type GlossaryTermStatus string

const (
	// GlossaryTermStatusActive 生效中：参与搜索与同义词扩展。
	GlossaryTermStatusActive GlossaryTermStatus = "active"
	// GlossaryTermStatusDeprecated 已废弃：保留映射供重定向，不参与扩展。
	GlossaryTermStatusDeprecated GlossaryTermStatus = "deprecated"
)

// Valid 报告状态是否已知。
func (s GlossaryTermStatus) Valid() bool {
	switch s {
	case GlossaryTermStatusActive, GlossaryTermStatusDeprecated:
		return true
	default:
		return false
	}
}

// String 渲染状态用于日志。
func (s GlossaryTermStatus) String() string { return string(s) }

// GlossaryTerm is one controlled-vocabulary row (V3.0 §4.4).
type GlossaryTerm struct {
	// ID of the term.
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// TenantID owning the term.
	TenantID uint64 `json:"tenant_id" gorm:"not null"`
	// StandardName is the canonical form (tenant-unique, paired with
	// TenantID; the UNIQUE constraint lives in migration 000143).
	StandardName string `json:"standard_name" gorm:"type:varchar(128);not null"`
	// Aliases is a JSONB string array: 别名/缩写/英文名. Marshalled with the
	// local service helper, never inline structs.
	Aliases JSON `json:"aliases" gorm:"type:jsonb;not null;default:'[]'"`
	// Definition is the standard definition (what it IS).
	Definition string `json:"definition" gorm:"not null;default:''"`
	// CounterExamples records what the term does NOT mean (约束模型幻觉).
	CounterExamples string `json:"counter_examples" gorm:"default:''"`
	// Domain groups terms by subject area (e.g. "合规", "风控"); empty means
	// unclassified.
	Domain string `json:"domain" gorm:"type:varchar(64);default:''"`
	// OwnerID is the accountable owner (术语责任人; informational in v1).
	OwnerID string `json:"owner_id" gorm:"type:varchar(36);default:''"`
	// Status: active or deprecated (deprecated rows keep the mapping).
	Status string `json:"status" gorm:"type:varchar(16);not null;default:'active'"`
	// SupersededByID points at the replacing term once deprecated (删除=置
	// deprecated+superseded_by，不物理删).
	SupersededByID string `json:"superseded_by_id" gorm:"type:varchar(36);default:''"`
	// CreatedBy records who created the term (audit identity).
	CreatedBy string `json:"created_by" gorm:"type:varchar(36);not null"`
	// CreatedAt of the term.
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt of the term (touched by GORM on Updates).
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the GORM table name.
func (GlossaryTerm) TableName() string { return "glossary_terms" }

// BeforeCreate assigns an ID before persistence.
func (t *GlossaryTerm) BeforeCreate(_ *gorm.DB) error {
	if t.ID == "" {
		t.ID = uuid.NewString()
	}
	return nil
}

// IsDeprecated reports whether the term has been superseded.
func (t *GlossaryTerm) IsDeprecated() bool {
	return GlossaryTermStatus(t.Status) == GlossaryTermStatusDeprecated
}

// GlossaryTermUpdate carries the mutable fields of a term. Nil fields keep
// the current value; pointer fields distinguish "not provided" from "".
type GlossaryTermUpdate struct {
	// StandardName renames the term (nil keeps).
	StandardName *string `json:"standard_name,omitempty"`
	// Aliases replaces the alias array (nil keeps).
	Aliases *[]string `json:"aliases,omitempty"`
	// Definition replaces the definition (nil keeps).
	Definition *string `json:"definition,omitempty"`
	// CounterExamples replaces the counter examples (nil keeps).
	CounterExamples *string `json:"counter_examples,omitempty"`
	// Domain replaces the domain tag (nil keeps).
	Domain *string `json:"domain,omitempty"`
	// OwnerID replaces the owner (nil keeps).
	OwnerID *string `json:"owner_id,omitempty"`
}
