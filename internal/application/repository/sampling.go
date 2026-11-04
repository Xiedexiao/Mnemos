package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ErrSamplingInspectionNotFound is returned when an inspection lookup misses.
var ErrSamplingInspectionNotFound = errors.New("sampling inspection not found")

// samplingRepository implements interfaces.SamplingRepository
// (KnowAxis V3.0 §6.6 抽检).
type samplingRepository struct {
	db *gorm.DB
}

// NewSamplingRepository constructs the repository.
func NewSamplingRepository(db *gorm.DB) interfaces.SamplingRepository {
	return &samplingRepository{db: db}
}

// DrawCandidates returns published knowledge that could be sampled.
//
// 只取 valid 状态：抽检的对象是"读者现在能看到的已发布内容"。草稿与已下架
// 的内容不构成质量风险，抽它们会稀释样本。
func (r *samplingRepository) DrawCandidates(
	ctx context.Context, tenantID uint64, since time.Time, limit int,
) ([]*types.SamplingCandidate, error) {
	if limit <= 0 || limit > 5000 {
		limit = 2000
	}
	type row struct {
		ID              string
		KnowledgeBaseID string
		Title           string
		Classification  string
	}
	var rows []row
	query := r.db.WithContext(ctx).Table("knowledges").
		Select("id, knowledge_base_id, title, classification").
		Where("tenant_id = ? AND deleted_at IS NULL", tenantID).
		Where("COALESCE(NULLIF(lifecycle_status, ''), 'valid') = ?",
			string(types.KnowledgeLifecycleValid))
	if !since.IsZero() {
		query = query.Where("updated_at >= ?", since)
	}
	if err := query.Order("updated_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*types.SamplingCandidate, 0, len(rows))
	for _, entry := range rows {
		out = append(out, &types.SamplingCandidate{
			KnowledgeID:     entry.ID,
			KnowledgeBaseID: entry.KnowledgeBaseID,
			Title:           entry.Title,
			// 风险用密级推导：这是平台里唯一与"内容错了代价多大"相关的
			// 显式字段。秘密级以上的内容出错代价最高，因此全部抽检。
			RiskLevel: riskFromClassification(entry.Classification),
		})
	}
	return out, nil
}

// riskFromClassification maps classification onto inspection risk.
func riskFromClassification(classification string) string {
	switch types.ParseKnowledgeClassification(classification) {
	case types.KnowledgeClassificationConfidential, types.KnowledgeClassificationSecret:
		return string(types.ApprovalRiskHigh)
	case types.KnowledgeClassificationPublic:
		return string(types.ApprovalRiskLow)
	default:
		return string(types.ApprovalRiskMid)
	}
}

// Insert draws one inspection, ignoring an existing (period, document) row.
func (r *samplingRepository) Insert(
	ctx context.Context, inspection *types.SamplingInspection,
) (bool, error) {
	result := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(inspection)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// Get returns one inspection in the tenant.
func (r *samplingRepository) Get(
	ctx context.Context, tenantID uint64, inspectionID string,
) (*types.SamplingInspection, error) {
	var row types.SamplingInspection
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, inspectionID).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrSamplingInspectionNotFound
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// List returns inspections.
func (r *samplingRepository) List(
	ctx context.Context, tenantID uint64, period string,
	status types.SamplingDecisionStatus, limit int,
) ([]*types.SamplingInspection, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := r.db.WithContext(ctx).Where("tenant_id = ?", tenantID)
	if period != "" {
		query = query.Where("period = ?", period)
	}
	if status != "" {
		query = query.Where("status = ?", string(status))
	}
	var rows []*types.SamplingInspection
	if err := query.Order("created_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// Decide records one verdict.
func (r *samplingRepository) Decide(
	ctx context.Context, tenantID uint64, inspectionID string,
	status types.SamplingDecisionStatus, reviewerID, note, followUpTaskID string,
	at time.Time,
) (*types.SamplingInspection, error) {
	row, err := r.Get(ctx, tenantID, inspectionID)
	if err != nil {
		return nil, err
	}
	// 已结案的抽检不能再改：与审批回执同理，事后改结论会让"当初抽检发现
	// 了什么"失去意义。要重新评估应新开一期样本。
	if types.SamplingDecisionStatus(row.Status) != types.SamplingPending {
		return nil, ErrSamplingInspectionNotFound
	}
	if err := r.db.WithContext(ctx).Model(&types.SamplingInspection{}).
		Where("id = ?", row.ID).
		Updates(map[string]interface{}{
			"status":            string(status),
			"reviewed_by":       reviewerID,
			"reviewed_at":       at.UTC(),
			"note":              note,
			"follow_up_task_id": followUpTaskID,
			"updated_at":        at.UTC(),
		}).Error; err != nil {
		return nil, err
	}
	row.Status = string(status)
	row.ReviewedBy = reviewerID
	row.ReviewedAt = &at
	row.Note = note
	row.FollowUpTaskID = followUpTaskID
	return row, nil
}

// Stats aggregates the outcomes.
func (r *samplingRepository) Stats(
	ctx context.Context, tenantID uint64, period string,
) (*types.SamplingStats, error) {
	query := r.db.WithContext(ctx).Model(&types.SamplingInspection{}).
		Where("tenant_id = ?", tenantID)
	if period != "" {
		query = query.Where("period = ?", period)
	}
	type row struct {
		Period    string
		RiskLevel string
		Status    string
		Total     int64
	}
	var rows []row
	if err := query.
		Select("period, risk_level, status, COUNT(*) AS total").
		Group("period, risk_level, status").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	stats := &types.SamplingStats{Period: period, ByRisk: map[string]types.SamplingStatsBreakdown{}}
	for _, entry := range rows {
		stats.Total += entry.Total
		breakdown := stats.ByRisk[entry.RiskLevel]
		breakdown.Total += entry.Total
		switch types.SamplingDecisionStatus(entry.Status) {
		case types.SamplingPending:
			stats.Pending += entry.Total
		case types.SamplingPassed:
			stats.Passed += entry.Total
			breakdown.Passed += entry.Total
		case types.SamplingFailed:
			stats.Failed += entry.Total
			breakdown.Failed += entry.Total
		case types.SamplingWaived:
			stats.Waived += entry.Total
		}
		stats.ByRisk[entry.RiskLevel] = breakdown
	}
	// 通过率只在有结论的样本上算：把 pending/waived 算进分母会让"还没抽完"
	// 与"抽了都通过"看起来一样。
	if decided := stats.Passed + stats.Failed; decided > 0 {
		stats.PassRate = float64(stats.Passed) / float64(decided)
	}
	for risk, breakdown := range stats.ByRisk {
		if decided := breakdown.Passed + breakdown.Failed; decided > 0 {
			breakdown.PassRate = float64(breakdown.Passed) / float64(decided)
			stats.ByRisk[risk] = breakdown
		}
	}
	return stats, nil
}
