package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// 知识之星（§15.4）的审计动作。
const (
	auditActionKnowledgeStarSelected  = "knowledge_star.selected"
	auditActionKnowledgeStarPublished = "knowledge_star.published"
)

// knowledgeStarMaxWinners 是一期入选人数上限：评选是表彰，不是排行榜。
const knowledgeStarMaxWinners = 20

// knowledgeStarService implements interfaces.KnowledgeStarService.
type knowledgeStarService struct {
	repo  interfaces.KnowledgeStarRepository
	audit interfaces.AuditLogService
	now   func() time.Time
}

// NewKnowledgeStarService constructs the service.
func NewKnowledgeStarService(
	repo interfaces.KnowledgeStarRepository, audit interfaces.AuditLogService,
) interfaces.KnowledgeStarService {
	return &knowledgeStarService{repo: repo, audit: audit, now: time.Now}
}

// Candidates returns the ranked contributors for a period.
func (s *knowledgeStarService) Candidates(
	ctx context.Context, tenantID uint64,
	periodType types.KnowledgeStarPeriodType, period string, limit int,
) ([]*types.KnowledgeStarCandidate, error) {
	start, end, canonical, err := resolveKnowledgeStarPeriod(periodType, period, s.now())
	if err != nil {
		return nil, err
	}
	candidates, err := s.repo.ListCandidates(ctx, tenantID, start, end, limit)
	if err != nil {
		return nil, err
	}
	awards, err := s.repo.ListAwards(ctx, tenantID, string(periodType), canonical, false, 0)
	if err != nil {
		return nil, err
	}
	awarded := make(map[string]struct{}, len(awards))
	for _, award := range awards {
		awarded[award.UserID] = struct{}{}
	}
	for _, candidate := range candidates {
		_, candidate.AlreadyAwarded = awarded[candidate.UserID]
	}
	return candidates, nil
}

// Select finalises the period's winners.
func (s *knowledgeStarService) Select(
	ctx context.Context, tenantID uint64, actorID string,
	selection types.KnowledgeStarSelection,
) ([]*types.KnowledgeStarAward, error) {
	start, end, canonical, err := resolveKnowledgeStarPeriod(
		selection.PeriodType, selection.Period, s.now())
	if err != nil {
		return nil, err
	}
	userIDs := normalizeStarUserIDs(selection.UserIDs)
	if len(userIDs) == 0 {
		return nil, apperrors.NewBadRequestError("at least one winner is required")
	}
	if len(userIDs) > knowledgeStarMaxWinners {
		return nil, apperrors.NewBadRequestError(fmt.Sprintf(
			"at most %d winners may be selected per period", knowledgeStarMaxWinners))
	}

	// 只有在周期内有正贡献的人才能入选：不能凭关系把一个零分用户写进
	// 表彰名单，那会让评选失去意义。
	candidates, err := s.repo.ListCandidates(ctx, tenantID, start, end, 200)
	if err != nil {
		return nil, err
	}
	byUser := make(map[string]*types.KnowledgeStarCandidate, len(candidates))
	for _, candidate := range candidates {
		byUser[candidate.UserID] = candidate
	}
	selected := make([]*types.KnowledgeStarCandidate, 0, len(userIDs))
	for _, userID := range userIDs {
		candidate, ok := byUser[userID]
		if !ok || candidate.Score <= 0 {
			return nil, apperrors.NewBadRequestError(
				"selected user has no contribution in this period: " + userID)
		}
		selected = append(selected, candidate)
	}
	sort.SliceStable(selected, func(i, j int) bool {
		if selected[i].Score != selected[j].Score {
			return selected[i].Score > selected[j].Score
		}
		return selected[i].UserID < selected[j].UserID
	})

	now := s.now().UTC()
	reason := strings.TrimSpace(selection.Reason)
	awards := make([]*types.KnowledgeStarAward, 0, len(selected))
	for index, candidate := range selected {
		awards = append(awards, &types.KnowledgeStarAward{
			TenantID:   tenantID,
			PeriodType: string(selection.PeriodType),
			Period:     canonical,
			UserID:     candidate.UserID,
			Rank:       index + 1,
			Score:      candidate.Score,
			Reason:     reason,
			Published:  false,
			AwardedBy:  actorID,
			AwardedAt:  now,
		})
	}
	if err := s.repo.ReplaceAwards(ctx, tenantID, awards); err != nil {
		return nil, err
	}
	s.logAudit(ctx, tenantID, actorID, auditActionKnowledgeStarSelected, canonical, map[string]interface{}{
		"period_type": string(selection.PeriodType),
		"winners":     len(awards),
	})
	return awards, nil
}

// Publish makes the period's list public.
func (s *knowledgeStarService) Publish(
	ctx context.Context, tenantID uint64, actorID string,
	periodType types.KnowledgeStarPeriodType, period string,
) (int64, error) {
	_, _, canonical, err := resolveKnowledgeStarPeriod(periodType, period, s.now())
	if err != nil {
		return 0, err
	}
	affected, err := s.repo.PublishAwards(ctx, tenantID, string(periodType), canonical)
	if err != nil {
		return 0, err
	}
	if affected == 0 {
		return 0, apperrors.NewNotFoundError("no awards selected for this period")
	}
	s.logAudit(ctx, tenantID, actorID, auditActionKnowledgeStarPublished, canonical, map[string]interface{}{
		"period_type": string(periodType),
		"awards":      affected,
	})
	return affected, nil
}

// List returns awards.
func (s *knowledgeStarService) List(
	ctx context.Context, tenantID uint64, periodType, period string,
	publishedOnly bool, limit int,
) ([]*types.KnowledgeStarAward, error) {
	if periodType != "" {
		parsed := types.KnowledgeStarPeriodType(periodType)
		if !parsed.Valid() {
			return nil, apperrors.NewBadRequestError("unknown period type")
		}
		if period != "" {
			if _, _, _, err := types.ParseKnowledgeStarPeriod(parsed, period); err != nil {
				return nil, apperrors.NewBadRequestError(err.Error())
			}
		}
	} else if period != "" {
		return nil, apperrors.NewBadRequestError("period_type is required when period is set")
	}
	return s.repo.ListAwards(ctx, tenantID, periodType, period, publishedOnly, limit)
}

// resolveKnowledgeStarPeriod parses the period, defaulting to the current one.
func resolveKnowledgeStarPeriod(
	periodType types.KnowledgeStarPeriodType, period string, now time.Time,
) (time.Time, time.Time, string, error) {
	if periodType == "" {
		periodType = types.KnowledgeStarPeriodMonth
	}
	if !periodType.Valid() {
		return time.Time{}, time.Time{}, "", apperrors.NewBadRequestError("unknown period type")
	}
	if strings.TrimSpace(period) == "" {
		period = types.KnowledgeStarPeriodOf(periodType, now)
	}
	start, end, canonical, err := types.ParseKnowledgeStarPeriod(periodType, period)
	if err != nil {
		return time.Time{}, time.Time{}, "", apperrors.NewBadRequestError(err.Error())
	}
	return start, end, canonical, nil
}

// normalizeStarUserIDs trims, dedupes and preserves order.
func normalizeStarUserIDs(userIDs []string) []string {
	out := make([]string, 0, len(userIDs))
	seen := make(map[string]struct{}, len(userIDs))
	for _, userID := range userIDs {
		trimmed := strings.TrimSpace(userID)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

// logAudit writes one knowledge-star audit event.
func (s *knowledgeStarService) logAudit(
	ctx context.Context, tenantID uint64, actorID, action, period string,
	details map[string]interface{},
) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: actorID,
		Action:      types.AuditAction(action),
		ScopeType:   "knowledge_star",
		TargetType:  "period",
		TargetID:    period,
		Outcome:     "success",
		Details:     gamificationAuditJSON(details),
	})
}
