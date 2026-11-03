package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ErrKnowledgeRelationNotFound is returned when a relation lookup misses.
var ErrKnowledgeRelationNotFound = errors.New("knowledge relation not found")

// knowledgeRelationRepository implements interfaces.KnowledgeRelationRepository.
type knowledgeRelationRepository struct {
	db *gorm.DB
}

// NewKnowledgeRelationRepository constructs the relation repository.
func NewKnowledgeRelationRepository(db *gorm.DB) interfaces.KnowledgeRelationRepository {
	return &knowledgeRelationRepository{db: db}
}

// CreatePair inserts an edge and its reverse mirror in one transaction.
//
// 成对写入是双向存储的核心：只写一半会让"谁引用了它"在不同页面上给出不同
// 答案。ON CONFLICT DO NOTHING 让重复关联变成幂等操作而不是报错。
func (r *knowledgeRelationRepository) CreatePair(
	ctx context.Context, forward, reverse *types.KnowledgeRelation,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, relation := range []*types.KnowledgeRelation{forward, reverse} {
			if relation == nil {
				continue
			}
			err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(relation).Error
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// ListForKnowledge returns every edge touching one document.
func (r *knowledgeRelationRepository) ListForKnowledge(
	ctx context.Context, tenantID uint64, knowledgeID string,
) ([]*types.KnowledgeRelation, error) {
	var relations []*types.KnowledgeRelation
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND (from_knowledge_id = ? OR to_knowledge_id = ?)",
			tenantID, knowledgeID, knowledgeID).
		Order("relation_type ASC, created_at ASC").
		Find(&relations).Error
	return relations, err
}

// ListOutgoing returns the edges a document authored.
func (r *knowledgeRelationRepository) ListOutgoing(
	ctx context.Context, tenantID uint64, knowledgeID string, relationType string,
) ([]*types.KnowledgeRelation, error) {
	query := r.db.WithContext(ctx).
		Where("tenant_id = ? AND from_knowledge_id = ?", tenantID, knowledgeID)
	if relationType != "" {
		query = query.Where("relation_type = ?", relationType)
	}
	var relations []*types.KnowledgeRelation
	err := query.Order("created_at ASC").Find(&relations).Error
	return relations, err
}

// ListIncoming returns the edges pointing at a document.
func (r *knowledgeRelationRepository) ListIncoming(
	ctx context.Context, tenantID uint64, knowledgeID string, relationType string,
) ([]*types.KnowledgeRelation, error) {
	query := r.db.WithContext(ctx).
		Where("tenant_id = ? AND to_knowledge_id = ?", tenantID, knowledgeID)
	if relationType != "" {
		query = query.Where("relation_type = ?", relationType)
	}
	var relations []*types.KnowledgeRelation
	err := query.Order("created_at ASC").Find(&relations).Error
	return relations, err
}

// DeletePair removes both directions of one edge.
func (r *knowledgeRelationRepository) DeletePair(
	ctx context.Context, tenantID uint64, fromID, toID, relationType string,
) (int64, error) {
	pairKey := types.KnowledgeRelationPairKey(fromID, toID, relationType)
	result := r.db.WithContext(ctx).
		Where("tenant_id = ? AND pair_key = ?", tenantID, pairKey).
		Delete(&types.KnowledgeRelation{})
	return result.RowsAffected, result.Error
}

// DeleteAllForKnowledge removes every edge touching one document, used when the
// document itself is deleted so no orphan edge survives.
func (r *knowledgeRelationRepository) DeleteAllForKnowledge(
	ctx context.Context, tenantID uint64, knowledgeID string,
) (int64, error) {
	result := r.db.WithContext(ctx).
		Where("tenant_id = ? AND (from_knowledge_id = ? OR to_knowledge_id = ?)",
			tenantID, knowledgeID, knowledgeID).
		Delete(&types.KnowledgeRelation{})
	return result.RowsAffected, result.Error
}

// CountForKnowledge counts edges touching one document, for the delete-impact
// summary.
func (r *knowledgeRelationRepository) CountForKnowledge(
	ctx context.Context, tenantID uint64, knowledgeID string,
) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&types.KnowledgeRelation{}).
		Where("tenant_id = ? AND (from_knowledge_id = ? OR to_knowledge_id = ?)",
			tenantID, knowledgeID, knowledgeID).
		Count(&count).Error
	return count, err
}
