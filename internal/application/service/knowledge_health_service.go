package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// knowledgeHealthService implements interfaces.KnowledgeHealthService.
type knowledgeHealthService struct {
	repo interfaces.KnowledgeHealthRepository
	now  func() time.Time
}

// NewKnowledgeHealthService constructs the knowledge health service.
func NewKnowledgeHealthService(repo interfaces.KnowledgeHealthRepository) interfaces.KnowledgeHealthService {
	return &knowledgeHealthService{repo: repo, now: time.Now}
}

// Compute computes one health snapshot (KnowAxis V3.0 §14.6 六维度 v1) and
// appends it to knowledge_health_snapshots: scopeID == "" → 全租户口径，
// 否则单 KB 口径（KB 不属于该租户时报 not found）。快照只追加不更新——
// 并发首访可能落两行，Latest 按 computed_at 取最新，追加语义无害。
//
// 审计不强制：看板是只读面，Compute 的产物本身就是留档快照。
func (s *knowledgeHealthService) Compute(ctx context.Context, tenantID uint64, scopeID string) (*types.KnowledgeHealthSnapshot, error) {
	scopeID = strings.TrimSpace(scopeID)
	scope := types.KnowledgeHealthScopeTenant
	if scopeID != "" {
		exists, err := s.repo.KnowledgeBaseExists(ctx, tenantID, scopeID)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, apperrors.NewNotFoundError("knowledge base not found")
		}
		scope = types.KnowledgeHealthScopeKnowledgeBase
	}
	raw, err := s.repo.CollectKnowledgeMetrics(ctx, tenantID, scopeID)
	if err != nil {
		return nil, err
	}
	metrics := buildKnowledgeHealthMetrics(raw)
	metrics.Score = types.ComputeKnowledgeHealthScore(
		metrics.Freshness.ValidRate,
		metrics.Completeness.CompletenessScore(),
		metrics.Governance.GovernanceCoverage(),
	)
	snapshot := &types.KnowledgeHealthSnapshot{
		TenantID:   tenantID,
		Scope:      string(scope),
		ScopeID:    scopeID,
		Metrics:    healthMetricsJSON(metrics),
		ComputedAt: s.now().UTC(),
	}
	if err := s.repo.CreateSnapshot(ctx, snapshot); err != nil {
		return nil, err
	}
	return snapshot, nil
}

// Latest returns the newest persisted snapshot; gorm.ErrRecordNotFound when
// none exists yet (the GET handler falls back to Compute then).
func (s *knowledgeHealthService) Latest(
	ctx context.Context, tenantID uint64, scope types.KnowledgeHealthScope, scopeID string,
) (*types.KnowledgeHealthSnapshot, error) {
	return s.repo.LatestSnapshot(ctx, tenantID, scope, scopeID)
}

// CronComputeAll snapshots every live KB of the tenant plus the tenant scope
// itself (供后续定时任务调用). Per-scope failures are logged and skipped:
// one broken KB must not kill the batch, the next cycle recomputes it.
func (s *knowledgeHealthService) CronComputeAll(ctx context.Context, tenantID uint64) ([]*types.KnowledgeHealthSnapshot, error) {
	kbIDs, err := s.repo.ListKnowledgeBaseIDs(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	snapshots := make([]*types.KnowledgeHealthSnapshot, 0, len(kbIDs)+1)
	for _, kbID := range kbIDs {
		snapshot, err := s.Compute(ctx, tenantID, kbID)
		if err != nil {
			logger.Warnf(ctx, "[KnowledgeHealth] compute snapshot for kb %s failed: %v", kbID, err)
			continue
		}
		snapshots = append(snapshots, snapshot)
	}
	// 全租户聚合快照同批落一行，租户级看板直接读最新。
	if snapshot, err := s.Compute(ctx, tenantID, ""); err != nil {
		logger.Warnf(ctx, "[KnowledgeHealth] compute tenant snapshot failed: %v", err)
	} else {
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

// buildKnowledgeHealthMetrics derives the §14.6 rates from the raw counts.
// total=0 → 各占比为 0（空库得 0 分，由看板以 total=0 解释为"暂无知识"）。
func buildKnowledgeHealthMetrics(raw *types.KnowledgeHealthRawStats) *types.KnowledgeHealthMetrics {
	total := raw.Total
	rate := func(count int64) float64 {
		if total <= 0 {
			return 0
		}
		return float64(count) / float64(total)
	}
	counts := raw.LifecycleCounts
	if counts == nil {
		counts = map[string]int64{}
	}
	metrics := &types.KnowledgeHealthMetrics{Total: total}
	metrics.Freshness = types.KnowledgeFreshnessMetrics{
		Counts:            counts,
		ValidRate:         rate(counts[string(types.KnowledgeLifecycleValid)]),
		PendingReviewRate: rate(counts[string(types.KnowledgeLifecyclePendingReview)]),
		ExpiredRate:       rate(counts[string(types.KnowledgeLifecycleExpired)]),
		StaleRate:         rate(counts[string(types.KnowledgeLifecycleStale)]),
	}
	metrics.Completeness = types.KnowledgeCompletenessMetrics{
		CustomMetadataRate: rate(raw.WithCustomMetadata),
		DescriptionRate:    rate(raw.WithDescription),
	}
	metrics.Structure = types.KnowledgeStructureMetrics{
		DuplicateFileCount:  raw.DuplicateFileCount,
		DuplicateGroupCount: raw.DuplicateGroupCount,
		DuplicateRate:       rate(raw.DuplicateFileCount),
	}
	metrics.Governance = types.KnowledgeGovernanceMetrics{
		NoReviewCycleRate:  rate(raw.NoReviewCycle),
		BeyondInternalRate: rate(raw.BeyondInternal),
		SoftDeletedCount:   raw.SoftDeleted,
	}
	// Usage 维度 v1 无信号（KnowledgeUsageMetrics 占位）；TODO 接入浏览统计。
	return metrics
}

// healthMetricsJSON marshals the metrics into the JSONB payload, falling back
// to an empty object on failure so a logging-shaped marshal error never
// blocks a snapshot write. Local to this vertical (keep-verticals-independent
// rule, 同 glossaryDetailsJSON).
func healthMetricsJSON(metrics *types.KnowledgeHealthMetrics) types.JSON {
	data, err := json.Marshal(metrics)
	if err != nil {
		return types.JSON([]byte("{}"))
	}
	return types.JSON(data)
}
