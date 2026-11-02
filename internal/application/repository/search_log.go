package repository

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// searchLogRepository implements interfaces.SearchLogRepository.
type searchLogRepository struct {
	db *gorm.DB
}

// NewSearchLogRepository constructs the search log repository.
func NewSearchLogRepository(db *gorm.DB) interfaces.SearchLogRepository {
	return &searchLogRepository{db: db}
}

// Create appends one query log row.
func (r *searchLogRepository) Create(ctx context.Context, entry *types.SearchLog) error {
	return r.db.WithContext(ctx).Create(entry).Error
}

// MarkClicked flags a logged query as leading to a click.
func (r *searchLogRepository) MarkClicked(ctx context.Context, tenantID uint64, logID string) error {
	return r.db.WithContext(ctx).
		Model(&types.SearchLog{}).
		Where("tenant_id = ? AND id = ?", tenantID, logID).
		Update("clicked", true).Error
}

// Overview aggregates the §12.5 dashboard numbers for a window.
func (r *searchLogRepository) Overview(
	ctx context.Context, tenantID uint64, since time.Time,
) (*types.SearchQualityOverview, error) {
	var row struct {
		TotalQueries            int64
		ZeroResultQueries       int64
		PermissionFilteredTotal int64
		ClickedQueries          int64
		AverageDurationMs       float64
	}
	err := r.db.WithContext(ctx).
		Model(&types.SearchLog{}).
		Select(`COUNT(*) AS total_queries,
			COUNT(*) FILTER (WHERE result_count = 0) AS zero_result_queries,
			COALESCE(SUM(permission_filtered_count), 0) AS permission_filtered_total,
			COUNT(*) FILTER (WHERE clicked) AS clicked_queries,
			COALESCE(AVG(duration_ms), 0) AS average_duration_ms`).
		Where("tenant_id = ? AND created_at >= ?", tenantID, since).
		Scan(&row).Error
	if err != nil {
		return nil, err
	}
	overview := &types.SearchQualityOverview{
		TotalQueries:            row.TotalQueries,
		ZeroResultQueries:       row.ZeroResultQueries,
		PermissionFilteredTotal: row.PermissionFilteredTotal,
		ClickedQueries:          row.ClickedQueries,
		AverageDurationMs:       row.AverageDurationMs,
	}
	if row.TotalQueries > 0 {
		overview.ZeroResultRate = float64(row.ZeroResultQueries) / float64(row.TotalQueries)
	}
	return overview, nil
}

// FrequentQueries returns the most frequent queries in the window.
func (r *searchLogRepository) FrequentQueries(
	ctx context.Context, tenantID uint64, since time.Time, zeroOnly bool, limit int,
) ([]types.FrequentQuery, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	query := r.db.WithContext(ctx).
		Model(&types.SearchLog{}).
		Select("query_normalized AS query, COUNT(*) AS occurrences").
		Where("tenant_id = ? AND created_at >= ? AND query_normalized <> ''", tenantID, since)
	if zeroOnly {
		query = query.Where("result_count = 0")
	}
	var rows []types.FrequentQuery
	err := query.
		Group("query_normalized").
		Order("occurrences DESC").
		Limit(limit).
		Scan(&rows).Error
	return rows, err
}

// knowledgeGapRepository implements interfaces.KnowledgeGapRepository.
type knowledgeGapRepository struct {
	db *gorm.DB
}

// NewKnowledgeGapRepository constructs the gap repository.
func NewKnowledgeGapRepository(db *gorm.DB) interfaces.KnowledgeGapRepository {
	return &knowledgeGapRepository{db: db}
}

// UpsertOccurrence increments the occurrence counter for a normalized query and
// returns the row. The increment happens in SQL so concurrent searches on the
// same term cannot lose counts.
func (r *knowledgeGapRepository) UpsertOccurrence(
	ctx context.Context, gap *types.KnowledgeGap,
) (*types.KnowledgeGap, error) {
	if gap.QueryNormalized == "" {
		return nil, gorm.ErrInvalidData
	}
	err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "tenant_id"}, {Name: "query_normalized"}},
			DoUpdates: clause.Assignments(map[string]interface{}{
				"occurrences":  gorm.Expr("knowledge_gaps.occurrences + 1"),
				"last_seen_at": gap.LastSeenAt,
				"updated_at":   gap.LastSeenAt,
			}),
		}).
		Create(gap).Error
	if err != nil {
		return nil, err
	}
	var stored types.KnowledgeGap
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND query_normalized = ?", gap.TenantID, gap.QueryNormalized).
		First(&stored).Error; err != nil {
		return nil, err
	}
	return &stored, nil
}

// List returns gaps of one tenant, most frequent first.
func (r *knowledgeGapRepository) List(
	ctx context.Context, tenantID uint64, status types.KnowledgeGapStatus, limit int,
) ([]*types.KnowledgeGap, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := r.db.WithContext(ctx).Where("tenant_id = ?", tenantID)
	if status != "" {
		query = query.Where("status = ?", string(status))
	}
	var rows []*types.KnowledgeGap
	err := query.Order("occurrences DESC, last_seen_at DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

// Update applies operator changes (assignee, status, resolution).
func (r *knowledgeGapRepository) Update(
	ctx context.Context, tenantID uint64, gapID string, updates map[string]interface{},
) (*types.KnowledgeGap, error) {
	if len(updates) == 0 {
		return r.Get(ctx, tenantID, gapID)
	}
	if err := r.db.WithContext(ctx).
		Model(&types.KnowledgeGap{}).
		Where("tenant_id = ? AND id = ?", tenantID, gapID).
		Updates(updates).Error; err != nil {
		return nil, err
	}
	return r.Get(ctx, tenantID, gapID)
}

// Get returns one gap.
func (r *knowledgeGapRepository) Get(
	ctx context.Context, tenantID uint64, gapID string,
) (*types.KnowledgeGap, error) {
	var row types.KnowledgeGap
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, gapID).
		First(&row).Error
	if err != nil {
		return nil, err
	}
	return &row, nil
}
