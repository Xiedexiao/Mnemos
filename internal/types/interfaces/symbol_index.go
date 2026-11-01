package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// SymbolIndexRepository persists symbol_index rows (KnowAxis V3.0 §12.7
// 精准索引 v1) and answers the exact-match queries behind ExactLookup.
type SymbolIndexRepository interface {
	// UpsertSymbols batch-upserts normalized symbol rows. Conflicts on
	// (tenant_id, symbol, knowledge_id, chunk_id) update symbol_type and
	// locator in place (重复建索引幂等), keeping the original id/created_at.
	UpsertSymbols(ctx context.Context, rows []*types.SymbolIndex) error
	// FindBySymbols exact-matches the given normalized symbols within the
	// tenant; kbIDs (non-empty) narrows the hit surface to those KBs.
	// Results are ordered longest-symbol-first (越具体越优先).
	FindBySymbols(ctx context.Context, tenantID uint64, kbIDs, symbols []string) ([]*types.SymbolIndex, error)
	// ListByKnowledge returns every symbol row of one knowledge document.
	ListByKnowledge(ctx context.Context, tenantID uint64, knowledgeID string) ([]*types.SymbolIndex, error)
	// DeleteByKnowledge removes all symbol rows of one knowledge document
	// (知识删除联动). Idempotent: zero rows affected is not an error.
	DeleteByKnowledge(ctx context.Context, tenantID uint64, knowledgeID string) error
}

// SymbolIndexService is the exact-retrieval channel (精确检索通道)：
// index-time extraction plus query-time exact lookup. Retrieval-pipeline
// wiring is a later phase; this vertical only ships the capability.
type SymbolIndexService interface {
	// IndexSymbols extracts symbol candidates from text (正则：大写字母≥2
	// + 数字组合的 token；E 前缀错误码形放行) and batch-upserts them for
	// (kbID, knowledgeID, chunkID). chunkID=="" indexes document-level
	// rows. Returns the upserted rows; a text with no candidates is an
	// empty result, not an error.
	IndexSymbols(
		ctx context.Context, tenantID uint64, kbID, knowledgeID, chunkID, text string,
	) ([]*types.SymbolIndex, error)
	// ExactLookup extracts symbol candidates from query (same regex) and
	// returns the exact hits in symbol_index as
	// [{knowledge_id, chunk_id, symbol, locator}], longest symbol first
	// (越具体越优先). kbIDs empty means tenant-wide.
	ExactLookup(ctx context.Context, tenantID uint64, kbIDs []string, query string) ([]*types.SymbolHit, error)
	// ListByKnowledge lists the indexed symbols of one knowledge document.
	ListByKnowledge(ctx context.Context, tenantID uint64, knowledgeID string) ([]*types.SymbolIndex, error)
	// DeleteByKnowledge removes all symbol rows of one knowledge document
	// (知识删除联动：knowledge 删除/回收站物理清除处接线；本垂直不改
	// 既有文件，只暴露能力).
	DeleteByKnowledge(ctx context.Context, tenantID uint64, knowledgeID string) error
}
