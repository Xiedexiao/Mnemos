package repository

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// maxSymbolHitsPerLookup caps one FindBySymbols call: a hot symbol
// (e.g. a company-wide standard number) can be indexed in hundreds of
// chunks, and the exact channel only needs the top slice of the most
// specific symbols, not the full posting list.
const maxSymbolHitsPerLookup = 200

// symbolIndexRepository implements interfaces.SymbolIndexRepository.
// All queries are exact equality on the normalized symbol — no ILIKE —
// so the same statements run on Postgres and SQLite.
type symbolIndexRepository struct {
	db *gorm.DB
}

// NewSymbolIndexRepository constructs the symbol index repository.
func NewSymbolIndexRepository(db *gorm.DB) interfaces.SymbolIndexRepository {
	return &symbolIndexRepository{db: db}
}

// UpsertSymbols batch-upserts normalized symbol rows. The conflict target
// is the migration's UNIQUE(tenant_id, symbol, knowledge_id, chunk_id);
// re-indexing the same scope refreshes symbol_type/locator in place and
// keeps the original id/created_at (重复建索引幂等).
func (r *symbolIndexRepository) UpsertSymbols(ctx context.Context, rows []*types.SymbolIndex) error {
	if len(rows) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "tenant_id"},
				{Name: "symbol"},
				{Name: "knowledge_id"},
				{Name: "chunk_id"},
			},
			DoUpdates: clause.AssignmentColumns([]string{"symbol_type", "locator"}),
		}).
		Create(&rows).Error
}

// FindBySymbols exact-matches the given normalized symbols within the
// tenant; kbIDs (non-empty) narrows the hit surface. Ordered
// longest-symbol-first (越具体越优先) with a symbol tiebreak so the
// result order is deterministic.
func (r *symbolIndexRepository) FindBySymbols(
	ctx context.Context, tenantID uint64, kbIDs, symbols []string,
) ([]*types.SymbolIndex, error) {
	if len(symbols) == 0 {
		return nil, nil
	}
	query := r.db.WithContext(ctx).
		Model(&types.SymbolIndex{}).
		Where("tenant_id = ? AND symbol IN ?", tenantID, symbols)
	if len(kbIDs) > 0 {
		query = query.Where("kb_id IN ?", kbIDs)
	}
	var rows []*types.SymbolIndex
	err := query.
		Order("LENGTH(symbol) DESC, symbol ASC").
		Limit(maxSymbolHitsPerLookup).
		Find(&rows).Error
	return rows, err
}

// ListByKnowledge returns every symbol row of one knowledge document.
func (r *symbolIndexRepository) ListByKnowledge(
	ctx context.Context, tenantID uint64, knowledgeID string,
) ([]*types.SymbolIndex, error) {
	var rows []*types.SymbolIndex
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_id = ?", tenantID, knowledgeID).
		Order("symbol ASC").
		Find(&rows).Error
	return rows, err
}

// DeleteByKnowledge removes all symbol rows of one knowledge document
// (知识删除联动). Idempotent: deleting an unknown/knowledge-less scope
// affects zero rows and is not an error.
func (r *symbolIndexRepository) DeleteByKnowledge(
	ctx context.Context, tenantID uint64, knowledgeID string,
) error {
	return r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_id = ?", tenantID, knowledgeID).
		Delete(&types.SymbolIndex{}).Error
}
