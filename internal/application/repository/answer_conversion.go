package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ErrAnswerSourceNotFound is returned when the answered turn does not exist.
var ErrAnswerSourceNotFound = errors.New("answered turn not found")

// answerConversionRepository implements interfaces.AnswerConversionRepository
// (KnowAxis V3.0 §13 回答→知识沉淀).
type answerConversionRepository struct {
	db *gorm.DB
}

// NewAnswerConversionRepository constructs the repository.
func NewAnswerConversionRepository(db *gorm.DB) interfaces.AnswerConversionRepository {
	return &answerConversionRepository{db: db}
}

// GetAnsweredTurn returns one Evidence Pack by request id.
//
// 只按 (tenant, request_id) 取：request_id 全表唯一，但带 tenant 条件让
// 跨租户探测即使在唯一性约束将来放宽时也不会生效。
func (r *answerConversionRepository) GetAnsweredTurn(
	ctx context.Context, tenantID uint64, requestID string,
) (*types.EvidencePack, error) {
	var pack types.EvidencePack
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND request_id = ?", tenantID, requestID).
		First(&pack).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrAnswerSourceNotFound
	}
	if err != nil {
		return nil, err
	}
	return &pack, nil
}

// FindCandidateBySource returns the candidate previously created from this
// answered turn.
func (r *answerConversionRepository) FindCandidateBySource(
	ctx context.Context, tenantID uint64, sourceType, sourceID string,
) (*types.KnowledgeCandidate, error) {
	var candidate types.KnowledgeCandidate
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND source_type = ? AND source_id = ?",
			tenantID, sourceType, sourceID).
		Order("created_at DESC").
		First(&candidate).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &candidate, nil
}
