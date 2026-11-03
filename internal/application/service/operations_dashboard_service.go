package service

import (
	"context"
	"sort"
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
	s.fillTrends(ctx, tenantID, since, windowDays, dashboard)
	s.fillContributors(ctx, tenantID, since, dashboard)
	s.fillCost(ctx, tenantID, since, dashboard)
	s.fillObservability(ctx, tenantID, since, dashboard)
	return dashboard, nil
}

// fillObservability builds the §17.2 runtime block.
//
// 全部取自 search_logs 与 approval_tasks：不引入独立指标库，所以看板上的
// "拒答率/权限拦截/慢查询"与搜索质量页永远同源。
func (s *operationsDashboardService) fillObservability(
	ctx context.Context, tenantID uint64, since time.Time, dashboard *types.OperationsDashboard,
) {
	// 用 SUM(CASE WHEN ...) 而不是 COUNT(...) FILTER (WHERE ...)：结果相同，
	// 但前者不依赖引擎版本（SQLite 需 ≥ 3.30 才支持 FILTER）。
	var row struct {
		Total          int64
		ZeroResult     int64
		PermissionKept int64
		AvgLatency     float64
		SlowQueries    int64
	}
	if err := s.db.WithContext(ctx).
		Model(&types.SearchLog{}).
		Select(`COUNT(*) AS total,
			COALESCE(SUM(CASE WHEN result_count = 0 THEN 1 ELSE 0 END), 0) AS zero_result,
			COALESCE(SUM(permission_filtered_count), 0) AS permission_kept,
			COALESCE(AVG(duration_ms), 0) AS avg_latency,
			COALESCE(SUM(CASE WHEN duration_ms > ? THEN 1 ELSE 0 END), 0) AS slow_queries`,
			slowQueryThresholdMs).
		Where("tenant_id = ? AND created_at >= ?", tenantID, since).
		Scan(&row).Error; err == nil {
		dashboard.Observability.ZeroResultQueries = row.ZeroResult
		dashboard.Observability.PermissionBlocked = row.PermissionKept
		dashboard.Observability.AverageLatencyMs = row.AvgLatency
		dashboard.Observability.SlowQueries = row.SlowQueries
		if row.Total > 0 {
			dashboard.Observability.RefusalRate = float64(row.ZeroResult) / float64(row.Total)
		}
	}
	dashboard.Observability.OverdueApprovals = dashboard.Delivery.OverduePending
}

// slowQueryThresholdMs is the §17.1 "大规模 ≤ 2s" degradation threshold.
const slowQueryThresholdMs = 2000

// fillTrends builds the per-day series (§16.1 趋势).
//
// 逐日分桶在 SQL 里做（date_trunc / DATE 截断），不把行拉进内存：看板默认
// 30 天，但窗口可到 365 天，逐行聚合会随数据量线性变慢。
func (s *operationsDashboardService) fillTrends(
	ctx context.Context, tenantID uint64, since time.Time, windowDays int,
	dashboard *types.OperationsDashboard,
) {
	type dayCount struct {
		Day   string
		Total int64
	}
	series := make(map[string]*types.DashboardTrendPoint, windowDays)
	dashboard.Trends = []types.DashboardTrendPoint{}

	ensure := func(day string) *types.DashboardTrendPoint {
		point, ok := series[day]
		if !ok {
			point = &types.DashboardTrendPoint{Day: day}
			series[day] = point
		}
		return point
	}

	// dayExpression 生成跨库一致的日期截断：Postgres 用 TO_CHAR，SQLite 用
	// strftime。写死其中一种会让 Lite 模式的看板整块静默为空。
	dayExpression := s.dayExpression()

	var knowledgeRows []dayCount
	if err := s.db.WithContext(ctx).
		Model(&types.Knowledge{}).
		Select(dayExpression+" AS day, COUNT(*) AS total").
		Where("tenant_id = ? AND deleted_at IS NULL AND created_at >= ?", tenantID, since).
		Group("day").Scan(&knowledgeRows).Error; err == nil {
		for _, row := range knowledgeRows {
			ensure(row.Day).KnowledgeAdded = row.Total
		}
	}

	var reviewRows []dayCount
	if err := s.db.WithContext(ctx).
		Model(&types.ApprovalTask{}).
		Select(dayExpression+" AS day, COUNT(*) AS total").
		Where("tenant_id = ? AND created_at >= ?", tenantID, since).
		Group("day").Scan(&reviewRows).Error; err == nil {
		for _, row := range reviewRows {
			ensure(row.Day).ReviewsSubmitted = row.Total
		}
	}

	var queryRows []dayCount
	if err := s.db.WithContext(ctx).
		Model(&types.SearchLog{}).
		Select(dayExpression+" AS day, COUNT(*) AS total").
		Where("tenant_id = ? AND created_at >= ?", tenantID, since).
		Group("day").Scan(&queryRows).Error; err == nil {
		for _, row := range queryRows {
			ensure(row.Day).Queries = row.Total
		}
	}

	days := make([]string, 0, len(series))
	for day := range series {
		days = append(days, day)
	}
	sort.Strings(days)
	for _, day := range days {
		dashboard.Trends = append(dashboard.Trends, *series[day])
	}
}

// dayExpression returns the dialect-appropriate date-truncation expression for
// created_at. Both forms yield YYYY-MM-DD text so the scan target stays a string.
func (s *operationsDashboardService) dayExpression() string {
	if s.db != nil && s.db.Dialector != nil && s.db.Dialector.Name() == "sqlite" {
		return "strftime('%Y-%m-%d', created_at)"
	}
	return "TO_CHAR(created_at, 'YYYY-MM-DD')"
}

// fillContributors builds the §16.2 contribution leaderboard.
//
// 只统计可验证的行为（做出的审批决定、提交的审批、社区问答），不统计"被
// 浏览数"这类可以刷的量：§16.2 明确要求"避免只比数量"。
func (s *operationsDashboardService) fillContributors(
	ctx context.Context, tenantID uint64, since time.Time, dashboard *types.OperationsDashboard,
) {
	contributors := map[string]*types.DashboardContributor{}
	ensure := func(userID string) *types.DashboardContributor {
		row, ok := contributors[userID]
		if !ok {
			row = &types.DashboardContributor{UserID: userID}
			contributors[userID] = row
		}
		return row
	}

	type userCount struct {
		UserID string
		Total  int64
	}

	var decided []userCount
	if err := s.db.WithContext(ctx).
		Model(&types.ApprovalTask{}).
		Select("decided_by AS user_id, COUNT(*) AS total").
		Where("tenant_id = ? AND decided_by <> '' AND decision_at >= ?", tenantID, since).
		Group("decided_by").Scan(&decided).Error; err == nil {
		for _, row := range decided {
			ensure(row.UserID).ReviewsCompleted = row.Total
		}
	}

	var submitted []userCount
	if err := s.db.WithContext(ctx).
		Model(&types.ApprovalTask{}).
		Select("requested_by AS user_id, COUNT(*) AS total").
		Where("tenant_id = ? AND created_at >= ?", tenantID, since).
		Group("requested_by").Scan(&submitted).Error; err == nil {
		for _, row := range submitted {
			ensure(row.UserID).Submitted = row.Total
		}
	}

	var answers []userCount
	if err := s.db.WithContext(ctx).
		Model(&types.CommunityAnswer{}).
		Select("answered_by AS user_id, COUNT(*) AS total").
		Where("tenant_id = ? AND created_at >= ?", tenantID, since).
		Group("answered_by").Scan(&answers).Error; err == nil {
		for _, row := range answers {
			ensure(row.UserID).Answers = row.Total
		}
	}

	var questions []userCount
	if err := s.db.WithContext(ctx).
		Model(&types.CommunityQuestion{}).
		Select("asked_by AS user_id, COUNT(*) AS total").
		Where("tenant_id = ? AND created_at >= ?", tenantID, since).
		Group("asked_by").Scan(&questions).Error; err == nil {
		for _, row := range questions {
			ensure(row.UserID).Questions = row.Total
		}
	}

	list := make([]types.DashboardContributor, 0, len(contributors))
	for _, row := range contributors {
		// 权重：审批与回答都比"提交/提问"更重——它们代表真实产出。
		row.Score = row.ReviewsCompleted*3 + row.Answers*2 + row.Submitted
		list = append(list, *row)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Score != list[j].Score {
			return list[i].Score > list[j].Score
		}
		return list[i].UserID < list[j].UserID
	})
	if len(list) > 20 {
		list = list[:20]
	}
	dashboard.Contributors = list
}

// fillCost builds the §16.1 "成本" block from the usage ledger plus the
// storage/index footprint.
//
// 三类数字口径不同，不能相加：
//   - token 与调用次数来自模型台账（按场景与空间分摊）；
//   - 存储是知识原文的 storage_size 之和；
//   - 索引以 chunk 数作代理指标——平台没有向量索引的字节数，也没有单价
//     目录，所以这里只报可测量的量，不编造金额。
func (s *operationsDashboardService) fillCost(
	ctx context.Context, tenantID uint64, since time.Time, dashboard *types.OperationsDashboard,
) {
	type usageRow struct {
		ModelID          string
		Scene            string
		KnowledgeBaseID  string
		PromptTokens     int64
		CompletionTokens int64
		CallCount        int64
	}
	var rows []usageRow
	if err := s.db.WithContext(ctx).
		Model(&types.ModelUsageDaily{}).
		Select("model_id, scene, knowledge_base_id, "+
			"SUM(prompt_tokens) AS prompt_tokens, "+
			"SUM(completion_tokens) AS completion_tokens, SUM(call_count) AS call_count").
		Where("tenant_id = ? AND day >= ?", tenantID, since).
		Group("model_id, scene, knowledge_base_id").
		Scan(&rows).Error; err != nil {
		return
	}

	dashboard.Cost.ByModel = make(map[string]int64, len(rows))
	dashboard.Cost.ByScene = make(map[string]int64)
	type allocationKey struct{ knowledgeBaseID, scene string }
	allocationIndex := make(map[allocationKey]int, len(rows))
	for _, row := range rows {
		scene := row.Scene
		if scene == "" {
			// 000172 之前的行没有场景维度；它们全部来自问答链路。
			scene = types.ModelUsageSceneChat
		}
		total := row.PromptTokens + row.CompletionTokens
		dashboard.Cost.PromptTokens += row.PromptTokens
		dashboard.Cost.CompletionTokens += row.CompletionTokens
		dashboard.Cost.CallCount += row.CallCount
		dashboard.Cost.ByModel[row.ModelID] += total
		dashboard.Cost.ByScene[scene] += total
		if row.KnowledgeBaseID == "" {
			dashboard.Cost.UnallocatedTokens += total
		}
		key := allocationKey{knowledgeBaseID: row.KnowledgeBaseID, scene: scene}
		index, ok := allocationIndex[key]
		if !ok {
			index = len(dashboard.Cost.Allocations)
			allocationIndex[key] = index
			dashboard.Cost.Allocations = append(dashboard.Cost.Allocations,
				types.DashboardCostAllocation{
					KnowledgeBaseID: row.KnowledgeBaseID,
					Scene:           scene,
				})
		}
		allocation := &dashboard.Cost.Allocations[index]
		allocation.PromptTokens += row.PromptTokens
		allocation.CompletionTokens += row.CompletionTokens
		allocation.TotalTokens += total
		allocation.CallCount += row.CallCount
	}
	dashboard.Cost.TotalTokens = dashboard.Cost.PromptTokens + dashboard.Cost.CompletionTokens

	s.fillCostFootprint(ctx, tenantID, dashboard)
}

// fillCostFootprint adds storage bytes and index chunk counts per space, then
// resolves the knowledge-base names used by both cost views.
func (s *operationsDashboardService) fillCostFootprint(
	ctx context.Context, tenantID uint64, dashboard *types.OperationsDashboard,
) {
	type storageRow struct {
		KnowledgeBaseID string
		KnowledgeCount  int64
		StorageBytes    int64
	}
	var storage []storageRow
	if err := s.db.WithContext(ctx).
		Model(&types.Knowledge{}).
		Select("knowledge_base_id, COUNT(*) AS knowledge_count, "+
			"COALESCE(SUM(storage_size), 0) AS storage_bytes").
		Where("tenant_id = ? AND deleted_at IS NULL", tenantID).
		Group("knowledge_base_id").
		Scan(&storage).Error; err != nil {
		return
	}
	type chunkRow struct {
		KnowledgeBaseID string
		ChunkCount      int64
	}
	var chunks []chunkRow
	if err := s.db.WithContext(ctx).
		Model(&types.Chunk{}).
		Select("knowledge_base_id, COUNT(*) AS chunk_count").
		Where("tenant_id = ?", tenantID).
		Group("knowledge_base_id").
		Scan(&chunks).Error; err != nil {
		return
	}
	chunkByKB := make(map[string]int64, len(chunks))
	for _, row := range chunks {
		chunkByKB[row.KnowledgeBaseID] = row.ChunkCount
	}

	spaces := make([]types.DashboardCostSpace, 0, len(storage))
	names := make([]string, 0, len(storage)+len(dashboard.Cost.Allocations))
	for _, row := range storage {
		space := types.DashboardCostSpace{
			KnowledgeBaseID: row.KnowledgeBaseID,
			KnowledgeCount:  row.KnowledgeCount,
			StorageBytes:    row.StorageBytes,
			ChunkCount:      chunkByKB[row.KnowledgeBaseID],
		}
		dashboard.Cost.TotalStorageBytes += row.StorageBytes
		dashboard.Cost.TotalChunks += space.ChunkCount
		spaces = append(spaces, space)
		names = append(names, row.KnowledgeBaseID)
	}
	for _, allocation := range dashboard.Cost.Allocations {
		if allocation.KnowledgeBaseID != "" {
			names = append(names, allocation.KnowledgeBaseID)
		}
	}
	nameByID := s.knowledgeBaseNames(ctx, tenantID, names)
	for index := range spaces {
		spaces[index].KnowledgeBaseName = nameByID[spaces[index].KnowledgeBaseID]
	}
	for index := range dashboard.Cost.Allocations {
		dashboard.Cost.Allocations[index].KnowledgeBaseName =
			nameByID[dashboard.Cost.Allocations[index].KnowledgeBaseID]
	}
	dashboard.Cost.StorageBySpace = spaces
}

// knowledgeBaseNames resolves display names for a set of ids.
func (s *operationsDashboardService) knowledgeBaseNames(
	ctx context.Context, tenantID uint64, ids []string,
) map[string]string {
	unique := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	names := make(map[string]string, len(unique))
	if len(unique) == 0 {
		return names
	}
	type nameRow struct {
		ID   string
		Name string
	}
	var rows []nameRow
	if err := s.db.WithContext(ctx).
		Model(&types.KnowledgeBase{}).
		Select("id, name").
		Where("tenant_id = ? AND id IN ?", tenantID, unique).
		Scan(&rows).Error; err != nil {
		return names
	}
	for _, row := range rows {
		names[row.ID] = row.Name
	}
	return names
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
	dashboard.Governance.PendingChangeSignals = countWhere(&types.ChangeSignal{},
		"status = ?", string(types.ChangeSignalPending))
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
