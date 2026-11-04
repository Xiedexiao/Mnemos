package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// readingRepository implements interfaces.ReadingRepository
// (KnowAxis V3.0 §15.2 阅读进度).
type readingRepository struct {
	db *gorm.DB
}

// NewReadingRepository constructs the repository.
func NewReadingRepository(db *gorm.DB) interfaces.ReadingRepository {
	return &readingRepository{db: db}
}

// GetProgress returns one user's position in one document, or nil.
func (r *readingRepository) GetProgress(
	ctx context.Context, tenantID uint64, userID, knowledgeID string,
) (*types.ReadingProgress, error) {
	var row types.ReadingProgress
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ? AND knowledge_id = ?", tenantID, userID, knowledgeID).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// SaveProgress upserts the position.
func (r *readingRepository) SaveProgress(
	ctx context.Context, progress *types.ReadingProgress,
) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "tenant_id"}, {Name: "user_id"}, {Name: "knowledge_id"},
		},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"chunk_id":    progress.ChunkID,
			"chunk_index": progress.ChunkIndex,
			"percent":     progress.Percent,
			"chunk_count": progress.ChunkCount,
			"completed":   progress.Completed,
			"updated_at":  progress.UpdatedAt,
		}),
	}).Create(progress).Error
}

// ListProgress returns the user's positions, newest first.
func (r *readingRepository) ListProgress(
	ctx context.Context, tenantID uint64, userID string, limit int,
) ([]*types.ReadingProgress, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var rows []*types.ReadingProgress
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ?", tenantID, userID).
		Order("updated_at DESC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ChunkContents returns the document's chunk texts in order.
//
// 只取启用的文本块：父子分块里的父块与图片块不是正文，混进来会让目录出现
// 重复标题。
func (r *readingRepository) ChunkContents(
	ctx context.Context, tenantID uint64, knowledgeID string,
) ([]string, error) {
	type row struct {
		Content string
	}
	var rows []row
	if err := r.db.WithContext(ctx).
		Model(&types.Chunk{}).
		Select("content").
		Where("tenant_id = ? AND knowledge_id = ?", tenantID, knowledgeID).
		Where("is_enabled = ?", true).
		Where("chunk_type IN ?", []string{types.ChunkTypeText, types.ChunkTypeFAQ}).
		Order("chunk_index ASC").
		Limit(2000).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, entry := range rows {
		out = append(out, entry.Content)
	}
	return out, nil
}
