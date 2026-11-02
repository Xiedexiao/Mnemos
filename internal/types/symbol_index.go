package types

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 符号索引（KnowAxis V3.0 §12.7 精准索引 v1）。
//
// symbol_index 是精确检索通道的存储面：编号/错误码/型号等"符号"归一
// （大写、去空格）后按 (tenant, symbol, knowledge, chunk) 建唯一行，
// ExactLookup 用等值查询命中（普通检索对这类符号召回不稳：分词切碎、
// 向量稀释语义）。本垂直不接检索管线，只暴露能力。
// ============================================================================

// Symbol type constants（symbol_type 的枚举值）。
const (
	// SymbolTypeCode 通用编号/代号（v1 启发式的兜底形状类）。
	SymbolTypeCode = "code"
	// SymbolTypeDocumentNumber 文档编号（SOP-EQ-012 / GB-50316-2016 这类
	// 多段分隔编号）。
	SymbolTypeDocumentNumber = "document_number"
	// SymbolTypeModel 型号（XR-5000 / XR5000 这类"字母段+数字段"形状）。
	SymbolTypeModel = "model"
	// SymbolTypeErrorCode 错误码（E023 / E-23 这类单字母 E 前缀形）。
	SymbolTypeErrorCode = "error_code"
	// SymbolTypePartNumber 零件号 / 物料号（PN-4471、PART-88231 这类带
	// 前缀的形状）。
	SymbolTypePartNumber = "part_number"
	// SymbolTypeDrawingNumber 图号（D-100、DWG-2024-01），§12.7 把它列为
	// 独立的精确通道：图纸版本替代查询依赖它。
	SymbolTypeDrawingNumber = "drawing_number"
	// SymbolTypeCustom 人工/外部指定的自定义类别。
	SymbolTypeCustom = "custom"
)

// IsValidSymbolType reports whether t is a known symbol_type value.
func IsValidSymbolType(t string) bool {
	switch t {
	case SymbolTypeCode, SymbolTypeDocumentNumber, SymbolTypeModel,
		SymbolTypeErrorCode, SymbolTypePartNumber, SymbolTypeDrawingNumber, SymbolTypeCustom:
		return true
	default:
		return false
	}
}

// SymbolIndex is one normalized symbol occurrence (V3.0 §12.7).
type SymbolIndex struct {
	// ID of the index row.
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// TenantID owning the row.
	TenantID uint64 `json:"tenant_id" gorm:"not null"`
	// KnowledgeBaseID the knowledge lives in (column kb_id per migration
	// 000152; kept denormalized so /symbol-lookup can scope by KB without
	// a join).
	KnowledgeBaseID string `json:"kb_id" gorm:"column:kb_id;type:varchar(36);not null"`
	// KnowledgeID of the document the symbol occurs in.
	KnowledgeID string `json:"knowledge_id" gorm:"type:varchar(36);not null"`
	// ChunkID narrows the hit to one chunk; "" means a document-level
	// index row (IndexSymbols called without a chunk).
	ChunkID string `json:"chunk_id" gorm:"type:varchar(36);default:''"`
	// Symbol is the normalized form (大写去空格): SOP-EQ-012 / E023.
	Symbol string `json:"symbol" gorm:"type:varchar(255);not null"`
	// SymbolType classifies the symbol (informational in v1 — matching is
	// by symbol equality only).
	SymbolType string `json:"symbol_type" gorm:"type:varchar(24);default:'code'"`
	// Locator points back into the original file (复用 source locators
	// 语义：单个 SourceLocator 的 JSON)。NULL when only chunk-level
	// position is known. Marshalled with types.JSON, never inline structs.
	Locator JSON `json:"locator" gorm:"type:jsonb"`
	// CreatedAt of the row (upserts keep the original).
	CreatedAt time.Time `json:"created_at"`
}

// TableName pins the GORM table name.
func (SymbolIndex) TableName() string { return "symbol_index" }

// BeforeCreate assigns an ID before persistence.
func (s *SymbolIndex) BeforeCreate(_ *gorm.DB) error {
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	return nil
}

// SymbolHit is one exact-lookup hit: where a queried symbol lives.
type SymbolHit struct {
	// KnowledgeID of the document containing the symbol.
	KnowledgeID string `json:"knowledge_id"`
	// ChunkID of the chunk-level row; "" for document-level rows.
	ChunkID string `json:"chunk_id"`
	// Symbol is the matched (normalized) symbol.
	Symbol string `json:"symbol"`
	// Locator is the stored page/paragraph locator JSON (may be null).
	Locator JSON `json:"locator"`
}

// DecodeLocator unmarshals the stored locator into a SourceLocator
// (复用 source locators 语义). Returns nil when the row has no locator
// or the payload is malformed — callers treat "no locator" the same as
// "chunk-level only".
func (h *SymbolHit) DecodeLocator() *SourceLocator {
	if h == nil || len(h.Locator) == 0 {
		return nil
	}
	var loc SourceLocator
	if err := json.Unmarshal(h.Locator, &loc); err != nil {
		return nil
	}
	return &loc
}
