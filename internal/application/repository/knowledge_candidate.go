package repository

import (
	"context"
	"errors"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// ErrKnowledgeCandidateNotFound 候选卡片不存在（或不在本租户）。
var ErrKnowledgeCandidateNotFound = errors.New("knowledge candidate not found")

const (
	// candidateListDefaultLimit 是候选列表的默认条数。
	candidateListDefaultLimit = 50
	// candidateListMaxLimit 是候选列表的硬上限：审核队列是低频人读接口，
	// 200 条足够一屏，防止无界扫描。
	candidateListMaxLimit = 200
)

// knowledgeCandidateRepository implements interfaces.KnowledgeCandidateRepository
// （KnowAxis V3.0 §10 v1）。
type knowledgeCandidateRepository struct {
	db *gorm.DB
}

// NewKnowledgeCandidateRepository creates a new knowledge candidate repository.
func NewKnowledgeCandidateRepository(db *gorm.DB) interfaces.KnowledgeCandidateRepository {
	return &knowledgeCandidateRepository{db: db}
}

// Create creates a knowledge candidate.
func (r *knowledgeCandidateRepository) Create(ctx context.Context, candidate *types.KnowledgeCandidate) error {
	return r.db.WithContext(ctx).Create(candidate).Error
}

// GetByID gets a knowledge candidate by tenant and id.
func (r *knowledgeCandidateRepository) GetByID(
	ctx context.Context, tenantID uint64, id string,
) (*types.KnowledgeCandidate, error) {
	var candidate types.KnowledgeCandidate
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, id).
		First(&candidate).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrKnowledgeCandidateNotFound
		}
		return nil, err
	}
	return &candidate, nil
}

// List 分页列出候选（created_at 倒序），返回总数。Status/Level 为空不过滤。
func (r *knowledgeCandidateRepository) List(
	ctx context.Context, tenantID uint64, filter types.KnowledgeCandidateListFilter,
) ([]*types.KnowledgeCandidate, int64, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = candidateListDefaultLimit
	}
	if limit > candidateListMaxLimit {
		limit = candidateListMaxLimit
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}

	query := r.db.WithContext(ctx).
		Model(&types.KnowledgeCandidate{}).
		Where("tenant_id = ?", tenantID)
	if status := strings.TrimSpace(filter.Status); status != "" {
		query = query.Where("status = ?", status)
	}
	if level := strings.TrimSpace(filter.Level); level != "" {
		query = query.Where("level = ?", level)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var candidates []*types.KnowledgeCandidate
	if err := query.
		Order("created_at DESC").
		Limit(limit).
		Offset(filter.Offset).
		Find(&candidates).Error; err != nil {
		return nil, 0, err
	}
	return candidates, total, nil
}

// Update saves the whole candidate row (caller loads, mutates, saves back).
func (r *knowledgeCandidateRepository) Update(ctx context.Context, candidate *types.KnowledgeCandidate) error {
	return r.db.WithContext(ctx).Save(candidate).Error
}

// FindExactTitle 返回同租户内标题精确相同的候选（排除 excludeID）。
// v1 查重第一步：精确命中即标记 duplicate。
func (r *knowledgeCandidateRepository) FindExactTitle(
	ctx context.Context, tenantID uint64, title string, excludeID string,
) ([]*types.KnowledgeCandidate, error) {
	query := r.db.WithContext(ctx).
		Where("tenant_id = ? AND title = ?", tenantID, title)
	if excludeID != "" {
		query = query.Where("id <> ?", excludeID)
	}
	var candidates []*types.KnowledgeCandidate
	if err := query.Order("created_at DESC").Limit(candidateListMaxLimit).Find(&candidates).Error; err != nil {
		return nil, err
	}
	return candidates, nil
}

// CountSimilarTitles 统计同租户内标题包含给定关键词（LIKE，转义通配符）
// 的候选数量（排除 excludeID）。v1 查重第二步：相似命中标记 conflict。
func (r *knowledgeCandidateRepository) CountSimilarTitles(
	ctx context.Context, tenantID uint64, titleFragment string, excludeID string,
) (int64, error) {
	fragment := strings.TrimSpace(titleFragment)
	if fragment == "" {
		return 0, nil
	}
	like := "%" + escapeLikeKeyword(fragment) + "%"
	query := r.db.WithContext(ctx).
		Model(&types.KnowledgeCandidate{}).
		Where("tenant_id = ? AND title LIKE ? ESCAPE ?", tenantID, like, likeEscapeChar)
	if excludeID != "" {
		query = query.Where("id <> ?", excludeID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}
