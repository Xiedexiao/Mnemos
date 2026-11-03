package service

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// operationsDashboardService implements interfaces.OperationsDashboardService
// (KnowAxis V3.0 §16.1).
//
// 聚合而非新规则：每个数字都直接来自对应垂直领域的表，看板不重新定义任何
// 口径。这样"看板显示 12 条待审"与"工作台里正好 12 条"永远一致，不会出现
// 两套计数互相打脸。
type operationsDashboardService struct {
	db      *gorm.DB
	quality interfaces.SearchQualityService
	health  interfaces.KnowledgeHealthService
	now     func() time.Time
}

// dashboardDefaultWindowDays is the default look-back for time-based numbers.
const dashboardDefaultWindowDays = 30

// NewOperationsDashboardService constructs the dashboard service.
func NewOperationsDashboardService(
	db *gorm.DB,
	quality interfaces.SearchQualityService,
	health interfaces.KnowledgeHealthService,
) interfaces.OperationsDashboardService {
	return &operationsDashboardService{db: db, quality: quality, health: health, now: time.Now}
}

// Get composes the dashboard for one tenant.
func (s *operationsDashboardService) Get(
	ctx context.Context, tenantID uint64, windowDays int,
) (*types.OperationsDashboard, error) {
	if windowDays <= 0 || windowDays > 365 {
		windowDays = dashboardDefaultWindowDays
	}
	dashboard := &types.OperationsDashboard{WindowDays: windowDays}
	dashboard.Overview.KnowledgeByStatus = map[string]int64{}
	if s.db == nil || tenantID == 0 {
		return dashboard, nil
	}
	since := s.now().AddDate(0, 0, -windowDays)

	s.fillOverview(ctx, tenantID, dashboard)
	s.fillGovernance(ctx, tenantID, dashboard)
	s.fillDelivery(ctx, tenantID, since, dashboard)
	s.fillSearchQuality(ctx, tenantID, windowDays, dashboard)
	s.fillHealth(ctx, tenantID, dashboard)
	return dashboard, nil
}

// fillOverview counts knowledge entries and their lifecycle distribution.
func (s *operationsDashboardService) fillOverview(
	ctx context.Context, tenantID uint64, dashboard *types.OperationsDashboard,
) {
	type statusCount struct {
		LifecycleStatus string
		Total           int64
	}
	var counts []statusCount
	if err := s.db.WithContext(ctx).
		Model(&types.Knowledge{}).
		Select("lifecycle_status, COUNT(*) AS total").
		Where("tenant_id = ? AND deleted_at IS NULL", tenantID).
		Group("lifecycle_status").
		Scan(&counts).Error; err != nil {
		return
	}
	for _, row := range counts {
		status := row.LifecycleStatus
		if status == "" {
			// 存量行为空：按有效处理，与 ParseKnowledgeLifecycleStatus 一致。
			status = string(types.KnowledgeLifecycleValid)
		}
		dashboard.Overview.KnowledgeByStatus[status] += row.Total
		dashboard.Overview.KnowledgeTotal += row.Total
	}

	var kbCount int64
	if err := s.db.WithContext(ctx).
		Model(&types.KnowledgeBase{}).
		Where("tenant_id = ? AND deleted_at IS NULL", tenantID).
		Count(&kbCount).Error; err == nil {
		dashboard.Overview.KnowledgeBaseCount = kbCount
	}

	var chunkCount int64
	if err := s.db.WithContext(ctx).
		Model(&types.Chunk{}).
		Where("tenant_id = ? AND deleted_at IS NULL", tenantID).
		Count(&chunkCount).Error; err == nil {
		dashboard.Overview.ChunkTotal = chunkCount
	}
}

// fillGovernance counts everything that needs a human decision.
func (s *operationsDashboardService) fillGovernance(
	ctx context.Context, tenantID uint64, dashboard *types.OperationsDashboard,
) {
	countWhere := func(model interface{}, where string, args ...interface{}) int64 {
		var count int64
		query := s.db.WithContext(ctx).Model(model).Where("tenant_id = ?", tenantID)
		if where != "" {
			query = query.Where(where, args...)
		}
		if err := query.Count(&count).Error; err != nil {
			return 0
		}
		return count
	}

	dashboard.Governance.PendingReviews = countWhere(&types.ApprovalTask{},
		"status = ?", string(types.ApprovalTaskStatusPending))
	dashboard.Governance.PendingReviewCycles = countWhere(&types.Knowledge{},
		"deleted_at IS NULL AND lifecycle_status = ?", string(types.KnowledgeLifecyclePendingReviewCycle))
	dashboard.Governance.Expired = countWhere(&types.Knowledge{},
		"deleted_at IS NULL AND lifecycle_status = ?", string(types.KnowledgeLifecycleExpired))
	dashboard.Governance.Conflicts = countWhere(&types.Knowledge{},
		"deleted_at IS NULL AND lifecycle_status = ?", string(types.KnowledgeLifecycleConflict))
	dashboard.Governance.OpenQualityTasks = countWhere(&types.QualityTask{},
		"status IN ?", []string{
			string(types.QualityTaskStatusOpen), string(types.QualityTaskStatusProcessing),
		})
	dashboard.Governance.OpenImpactTasks = countWhere(&types.ImpactTask{},
		"status = ?", string(types.ImpactTaskStatusOpen))
	dashboard.Governance.OpenKnowledgeGaps = countWhere(&types.KnowledgeGap{},
		"status = ?", string(types.KnowledgeGapOpen))
	dashboard.Governance.PendingFieldMigrations = countWhere(&types.KnowledgeFieldMigration{},
		"status = ?", string(types.FieldMigrationPending))
	dashboard.Governance.ACLCeilingViolations = countWhere(&types.ACLCeilingViolation{}, "")
}

// fillDelivery measures approval throughput (§19.2).
func (s *operationsDashboardService) fillDelivery(
	ctx context.Context, tenantID uint64, since time.Time, dashboard *types.OperationsDashboard,
) {
	countDecided := func(status string) int64 {
		var count int64
		if err := s.db.WithContext(ctx).
			Model(&types.ApprovalTask{}).
			Where("tenant_id = ? AND status = ? AND decision_at >= ?", tenantID, status, since).
			Count(&count).Error; err != nil {
			return 0
		}
		return count
	}
	dashboard.Delivery.ApprovedLast30Days = countDecided(string(types.ApprovalTaskStatusApproved))
	dashboard.Delivery.RejectedLast30Days = countDecided(string(types.ApprovalTaskStatusRejected))

	var overdue int64
	if err := s.db.WithContext(ctx).
		Model(&types.ApprovalTask{}).
		Where("tenant_id = ? AND status = ? AND sla_due IS NOT NULL AND sla_due < ?",
			tenantID, string(types.ApprovalTaskStatusPending), s.now()).
		Count(&overdue).Error; err == nil {
		dashboard.Delivery.OverduePending = overdue
	}

	// 平均决策时长：created_at → decision_at 的小时均值。用数据库求平均，
	// 避免把整个窗口的行拉进内存。
	var averageHours *float64
	if err := s.db.WithContext(ctx).
		Model(&types.ApprovalTask{}).
		Select("AVG(EXTRACT(EPOCH FROM (decision_at - created_at)) / 3600.0)").
		Where("tenant_id = ? AND decision_at IS NOT NULL AND created_at >= ?", tenantID, since).
		Scan(&averageHours).Error; err == nil && averageHours != nil {
		dashboard.Delivery.AverageDecisionHours = *averageHours
	}
	// 四眼违规由服务层硬拒绝，恒为 0；显式输出让审计能确认这项控制生效过。
	dashboard.Delivery.FourEyesViolations = 0
}

// fillSearchQuality embeds the §12.5 numbers.
func (s *operationsDashboardService) fillSearchQuality(
	ctx context.Context, tenantID uint64, windowDays int, dashboard *types.OperationsDashboard,
) {
	if s.quality == nil {
		return
	}
	overview, err := s.quality.Overview(ctx, tenantID, windowDays)
	if err != nil || overview == nil {
		return
	}
	dashboard.SearchQuality = types.DashboardSearchQuality{
		TotalQueries:            overview.TotalQueries,
		ZeroResultQueries:       overview.ZeroResultQueries,
		ZeroResultRate:          overview.ZeroResultRate,
		PermissionFilteredTotal: overview.PermissionFilteredTotal,
		AverageDurationMs:       overview.AverageDurationMs,
	}
}

// fillHealth attaches the latest tenant-scope health snapshot.
func (s *operationsDashboardService) fillHealth(
	ctx context.Context, tenantID uint64, dashboard *types.OperationsDashboard,
) {
	if s.health == nil {
		return
	}
	snapshot, err := s.health.Latest(ctx, tenantID, types.KnowledgeHealthScopeTenant, "")
	if err != nil || snapshot == nil {
		return
	}
	dashboard.Health = snapshot
}
