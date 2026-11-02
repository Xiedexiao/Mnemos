package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// searchQualityService implements interfaces.SearchQualityService (§12.5).
//
// Logging is deliberately fire-and-forget: telemetry must never turn a working
// search into an error, and it must not add latency to the response either, so
// every write runs on a detached context.
type searchQualityService struct {
	logs interfaces.SearchLogRepository
	now  func() time.Time
}

// NewSearchQualityService constructs the search-quality service.
func NewSearchQualityService(logs interfaces.SearchLogRepository) interfaces.SearchQualityService {
	return &searchQualityService{logs: logs, now: time.Now}
}

// searchLogWindowDefaultDays is the default dashboard window.
const searchLogWindowDefaultDays = 30

// Record appends one query log row (best-effort).
func (s *searchQualityService) Record(ctx context.Context, entry *types.SearchLog) {
	if s == nil || s.logs == nil || entry == nil || entry.TenantID == 0 {
		return
	}
	ctx = context.WithoutCancel(ctx)
	if err := s.logs.Create(ctx, entry); err != nil {
		logger.Warnf(ctx, "failed to record search log: %v", err)
	}
}

// RecordResult logs one executed query with its outcome counts.
func (s *searchQualityService) RecordResult(
	ctx context.Context, channel types.SearchChannel, query string,
	kbIDs []string, resultCount, permissionFiltered int, topScore float64,
	duration time.Duration,
) {
	if s == nil || s.logs == nil {
		return
	}
	tenantID, _ := types.TenantIDFromContext(ctx)
	userID, _ := types.UserIDFromContext(ctx)
	raw, err := json.Marshal(kbIDs)
	if err != nil {
		raw = []byte("[]")
	}
	s.Record(ctx, &types.SearchLog{
		TenantID:                tenantID,
		UserID:                  userID,
		Query:                   types.TruncateSearchQuery(query),
		QueryNormalized:         types.NormalizeSearchQuery(query),
		Channel:                 string(channel),
		KnowledgeBaseIDs:        types.JSON(raw),
		ResultCount:             resultCount,
		PermissionFilteredCount: permissionFiltered,
		TopScore:                topScore,
		DurationMs:              int(duration.Milliseconds()),
		CreatedAt:               s.now(),
	})
}

// MarkClicked flags a query as producing a click.
func (s *searchQualityService) MarkClicked(ctx context.Context, tenantID uint64, logID string) error {
	if s == nil || s.logs == nil || logID == "" {
		return nil
	}
	return s.logs.MarkClicked(ctx, tenantID, logID)
}

// Overview returns the §12.5 dashboard numbers.
func (s *searchQualityService) Overview(
	ctx context.Context, tenantID uint64, windowDays int,
) (*types.SearchQualityOverview, error) {
	if s == nil || s.logs == nil {
		return &types.SearchQualityOverview{}, nil
	}
	if windowDays <= 0 || windowDays > 365 {
		windowDays = searchLogWindowDefaultDays
	}
	since := s.now().AddDate(0, 0, -windowDays)
	overview, err := s.logs.Overview(ctx, tenantID, since)
	if err != nil {
		return nil, err
	}
	overview.WindowDays = windowDays
	return overview, nil
}

// FrequentQueries returns the hot queries, optionally only zero-result ones.
func (s *searchQualityService) FrequentQueries(
	ctx context.Context, tenantID uint64, windowDays int, zeroOnly bool, limit int,
) ([]types.FrequentQuery, error) {
	if s == nil || s.logs == nil {
		return nil, nil
	}
	if windowDays <= 0 || windowDays > 365 {
		windowDays = searchLogWindowDefaultDays
	}
	return s.logs.FrequentQueries(ctx, tenantID, s.now().AddDate(0, 0, -windowDays), zeroOnly, limit)
}

// knowledgeGapService implements interfaces.KnowledgeGapService (§14.4).
type knowledgeGapService struct {
	gaps interfaces.KnowledgeGapRepository
	// threshold is how many times a question must be missed before it becomes a
	// gap. 1 would flood the list with one-off typos; the spec's "高频零结果"
	// implies repetition.
	threshold int
	now       func() time.Time
}

// knowledgeGapDefaultThreshold is how many misses make a gap.
const knowledgeGapDefaultThreshold = 3

// NewKnowledgeGapService constructs the gap service.
func NewKnowledgeGapService(gaps interfaces.KnowledgeGapRepository) interfaces.KnowledgeGapService {
	return &knowledgeGapService{gaps: gaps, threshold: knowledgeGapDefaultThreshold, now: time.Now}
}

// Observe records one missing-knowledge signal and returns the clustered gap.
//
// The threshold is applied on read rather than on write: every miss is counted,
// so the operator sees how often a question was missed even before it crosses
// the line, and a closed gap keeps its history.
func (s *knowledgeGapService) Observe(
	ctx context.Context, tenantID uint64, query string,
	source types.KnowledgeGapSource,
) (*types.KnowledgeGap, error) {
	if s == nil || s.gaps == nil {
		return nil, nil
	}
	normalized := types.NormalizeSearchQuery(query)
	if normalized == "" || tenantID == 0 {
		return nil, nil
	}
	if source == "" {
		source = types.KnowledgeGapSourceSearchZeroResult
	}
	now := s.now()
	gap, err := s.gaps.UpsertOccurrence(ctx, &types.KnowledgeGap{
		// 显式给 ID：GORM 的 BeforeCreate 只在真正落库时跑，聚合器拿到的
		// 行必须自带主键才能被后续分派/关单引用。
		ID:              uuid.NewString(),
		TenantID:        tenantID,
		QueryNormalized: normalized,
		Query:           types.TruncateSearchQuery(query),
		Source:          string(source),
		Occurrences:     1,
		LastSeenAt:      now,
		Status:          string(types.KnowledgeGapOpen),
		CreatedAt:       now,
		UpdatedAt:       now,
	})
	if err != nil {
		// 缺口聚合失败不该影响用户已经得到的搜索结果。
		logger.Warnf(ctx, "failed to record knowledge gap for %q: %v", normalized, err)
		return nil, err
	}
	return gap, nil
}

// List returns the gap list.
func (s *knowledgeGapService) List(
	ctx context.Context, tenantID uint64, status types.KnowledgeGapStatus, limit int,
) ([]*types.KnowledgeGap, error) {
	if s == nil || s.gaps == nil {
		return nil, nil
	}
	if status != "" && !status.Valid() {
		return nil, apperrors.NewBadRequestError("unknown gap status")
	}
	return s.gaps.List(ctx, tenantID, status, limit)
}

// Assign hands a gap to an owner.
func (s *knowledgeGapService) Assign(
	ctx context.Context, tenantID uint64, gapID, assigneeID string,
) (*types.KnowledgeGap, error) {
	if s == nil || s.gaps == nil {
		return nil, apperrors.NewInternalServerError("knowledge gap service is not configured")
	}
	if gapID == "" {
		return nil, apperrors.NewBadRequestError("gap id is required")
	}
	return s.gaps.Update(ctx, tenantID, gapID, map[string]interface{}{
		"assignee_id": assigneeID,
		"updated_at":  s.now(),
	})
}

// UpdateStatus moves a gap through its lifecycle.
func (s *knowledgeGapService) UpdateStatus(
	ctx context.Context, tenantID uint64, gapID string,
	status types.KnowledgeGapStatus, resolvedKnowledgeID string,
) (*types.KnowledgeGap, error) {
	if s == nil || s.gaps == nil {
		return nil, apperrors.NewInternalServerError("knowledge gap service is not configured")
	}
	if gapID == "" {
		return nil, apperrors.NewBadRequestError("gap id is required")
	}
	if !status.Valid() {
		return nil, apperrors.NewBadRequestError("unknown gap status")
	}
	updates := map[string]interface{}{
		"status":     string(status),
		"updated_at": s.now(),
	}
	if resolvedKnowledgeID != "" {
		updates["resolved_knowledge_id"] = resolvedKnowledgeID
	}
	return s.gaps.Update(ctx, tenantID, gapID, updates)
}
