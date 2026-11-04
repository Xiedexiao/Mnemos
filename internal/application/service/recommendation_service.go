package service

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// errRecommendationAnonymous marks a board request with no identity. It is not
// an API error: the caller renders an empty list.
var errRecommendationAnonymous = errors.New("recommendation: caller identity is required")

// recommendationService implements interfaces.RecommendationService
// (KnowAxis V3.0 §15.3 发现路径 / §16.1 热门).
type recommendationService struct {
	repo       interfaces.RecommendationRepository
	knowledges interfaces.KnowledgeRepository
	acl        interfaces.KnowledgeACLRepository
	subjects   aclSubjectResolver
	weights    types.RecommendationWeights
	now        func() time.Time
}

// NewRecommendationService constructs the service.
func NewRecommendationService(
	repo interfaces.RecommendationRepository,
	knowledges interfaces.KnowledgeRepository,
	acl interfaces.KnowledgeACLRepository,
	subjectGroups interfaces.ACLSubjectGroupService,
) interfaces.RecommendationService {
	return &recommendationService{
		repo:       repo,
		knowledges: knowledges,
		acl:        acl,
		subjects:   newACLSubjectResolver(subjectGroups),
		weights:    types.DefaultRecommendationWeights(),
		now:        time.Now,
	}
}

// Board returns the four lists for one document.
//
// 排序在服务层做而不是各查询里各写一遍 ORDER BY：四路候选的分数含义不同
// （关系边、共享标签数、新鲜度、阅读量），先取候选再统一打分才能让"相关
// 知识"里既有强关系文档也有高热度文档，而不是被某一路占满。
func (s *recommendationService) Board(
	ctx context.Context, tenantID uint64, knowledgeID string, limit int,
) (*types.RecommendationBoard, error) {
	if strings.TrimSpace(knowledgeID) == "" {
		return nil, apperrors.NewBadRequestError("knowledge_id is required")
	}
	limit = types.NormalizeRecommendationLimit(limit)
	board := &types.RecommendationBoard{
		KnowledgeID: knowledgeID,
		Related:     []types.RecommendationItem{},
		ForYou:      []types.RecommendationItem{},
		Latest:      []types.RecommendationItem{},
		Hot:         []types.RecommendationItem{},
	}
	caller := types.CallerFromContext(ctx)
	tenantID = resolveRecommendationTenant(ctx, tenantID)

	// 锚点文档决定"同空间"与"同标签"的范围。
	var anchor *types.Knowledge
	if s.knowledges != nil {
		if knowledge, err := s.knowledges.GetKnowledgeByID(ctx, tenantID, knowledgeID); err == nil {
			anchor = knowledge
		}
	}

	related, err := s.repo.RelatedKnowledge(ctx, tenantID, knowledgeID, limit*3)
	if err != nil {
		return nil, err
	}
	if len(related) < limit && anchor != nil {
		byTag, tagErr := s.repo.SameTagKnowledge(ctx, tenantID, knowledgeID, limit*3)
		if tagErr != nil {
			return nil, tagErr
		}
		related = append(related, byTag...)
	}
	board.Related = s.rankAndFilter(ctx, tenantID, caller, related, knowledgeID, limit)

	interest := s.interestCandidates(ctx, tenantID, caller, knowledgeID, limit*3)
	board.ForYou = s.rankAndFilter(ctx, tenantID, caller, interest, knowledgeID, limit)

	spaceID := ""
	if anchor != nil {
		spaceID = anchor.KnowledgeBaseID
	}
	latest, err := s.repo.LatestKnowledge(ctx, tenantID, spaceID, limit*3)
	if err != nil {
		return nil, err
	}
	board.Latest = s.rankAndFilter(ctx, tenantID, caller, latest, knowledgeID, limit)

	hot, err := s.repo.HotKnowledge(ctx, tenantID, spaceID,
		s.now().AddDate(0, 0, -types.RecommendationHotWindowDays), limit*3)
	if err != nil {
		return nil, err
	}
	board.Hot = s.rankAndFilter(ctx, tenantID, caller, hot, knowledgeID, limit)
	return board, nil
}

// Home returns the personal board.
func (s *recommendationService) Home(
	ctx context.Context, tenantID uint64, userID string, limit int,
) (*types.RecommendationBoard, error) {
	if userID == "" {
		return nil, apperrors.NewUnauthorizedError("caller identity is required")
	}
	limit = types.NormalizeRecommendationLimit(limit)
	caller := types.CallerFromContext(ctx)
	tenantID = resolveRecommendationTenant(ctx, tenantID)
	board := &types.RecommendationBoard{
		Related: []types.RecommendationItem{},
		ForYou:  []types.RecommendationItem{},
		Latest:  []types.RecommendationItem{},
		Hot:     []types.RecommendationItem{},
	}

	interest := s.interestCandidates(ctx, tenantID, caller, "", limit*3)
	board.ForYou = s.rankAndFilter(ctx, tenantID, caller, interest, "", limit)

	latest, err := s.repo.LatestKnowledge(ctx, tenantID, "", limit*3)
	if err != nil {
		return nil, err
	}
	board.Latest = s.rankAndFilter(ctx, tenantID, caller, latest, "", limit)

	hot, err := s.repo.HotKnowledge(ctx, tenantID, "",
		s.now().AddDate(0, 0, -types.RecommendationHotWindowDays), limit*3)
	if err != nil {
		return nil, err
	}
	board.Hot = s.rankAndFilter(ctx, tenantID, caller, hot, "", limit)
	return board, nil
}

// RecordView records one read.
//
// 记录失败只告警：热度与历史是辅助功能，不能因为一次写失败让正文打不开。
func (s *recommendationService) RecordView(
	ctx context.Context, tenantID uint64, userID, knowledgeID string,
) error {
	if userID == "" || knowledgeID == "" {
		return apperrors.NewBadRequestError("user_id and knowledge_id are required")
	}
	tenantID = resolveRecommendationTenant(ctx, tenantID)
	spaceID := ""
	if s.knowledges != nil {
		if knowledge, err := s.knowledges.GetKnowledgeByID(ctx, tenantID, knowledgeID); err == nil {
			spaceID = knowledge.KnowledgeBaseID
		}
	}
	if _, err := s.repo.RecordView(ctx, tenantID, userID, knowledgeID, spaceID, s.now()); err != nil {
		logger.Warnf(ctx, "[recommendation] record view failed: %v", err)
		return nil
	}
	return nil
}

// History returns the caller's browse history.
//
// 历史里保留无权访问的行但把标题抹掉（Visible=false）：直接删掉会让"我昨天
// 看的那篇去哪了"变成困惑；显示标题则是 §2.10 禁止的存在性泄露。
func (s *recommendationService) History(
	ctx context.Context, tenantID uint64, userID string, limit int,
) ([]types.ViewHistoryItem, error) {
	if userID == "" {
		return nil, apperrors.NewUnauthorizedError("caller identity is required")
	}
	tenantID = resolveRecommendationTenant(ctx, tenantID)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	events, err := s.repo.ListHistory(ctx, tenantID, userID, limit)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return []types.ViewHistoryItem{}, nil
	}
	ids := make([]string, 0, len(events))
	for _, event := range events {
		ids = append(ids, event.KnowledgeID)
	}
	summaries, err := s.repo.KnowledgeSummary(ctx, tenantID, ids)
	if err != nil {
		return nil, err
	}

	items := make([]types.ViewHistoryItem, 0, len(events))
	seen := make(map[string]struct{}, len(events))
	for _, event := range events {
		// 同一篇知识多次阅读只保留最近一次。
		if _, ok := seen[event.KnowledgeID]; ok {
			continue
		}
		seen[event.KnowledgeID] = struct{}{}
		item := types.ViewHistoryItem{
			KnowledgeID:     event.KnowledgeID,
			KnowledgeBaseID: event.KnowledgeBaseID,
			ViewedAt:        event.CreatedAt,
		}
		if summary, ok := summaries[event.KnowledgeID]; ok {
			item.Title = summary.Title
			item.KnowledgeBaseID = summary.KnowledgeBaseID
			item.Visible = true
		}
		items = append(items, item)
	}
	return items, nil
}

// interestCandidates gathers documents matching the user's interest tags.
func (s *recommendationService) interestCandidates(
	ctx context.Context, tenantID uint64, caller types.Caller,
	excludeKnowledgeID string, limit int,
) []types.RecommendationItem {
	if caller.UserID == "" {
		return nil
	}
	tags, err := s.repo.InterestTags(ctx, tenantID, caller.UserID,
		s.now().Add(-types.RecommendationInterestWindow), 10)
	if err != nil {
		// 兴趣信号取不到就退回"同空间/最新"，不阻断整个看板。
		logger.Warnf(ctx, "[recommendation] interest tags failed: %v", err)
		return nil
	}
	if len(tags) == 0 {
		return nil
	}
	items, err := s.repo.KnowledgeForTags(ctx, tenantID, tags, excludeKnowledgeID, limit)
	if err != nil {
		logger.Warnf(ctx, "[recommendation] interest candidates failed: %v", err)
		return nil
	}
	return items
}

// rankAndFilter dedupes, drops denied documents, scores and truncates.
//
// 权限过滤是这条链路上唯一不能省的一步：推荐位如果漏掉 ACL，用户会在
// "猜你想学"里看到自己无权打开的标题。ACL 查询失败时整段返回空列表
// （fail-closed），而不是退化成不过滤。
func (s *recommendationService) rankAndFilter(
	ctx context.Context, tenantID uint64, caller types.Caller,
	candidates []types.RecommendationItem, excludeKnowledgeID string, limit int,
) []types.RecommendationItem {
	if len(candidates) == 0 {
		return []types.RecommendationItem{}
	}
	deduped := make([]types.RecommendationItem, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	kbSet := make(map[string]struct{})
	for _, candidate := range candidates {
		if candidate.KnowledgeID == "" || candidate.KnowledgeID == excludeKnowledgeID {
			continue
		}
		if _, ok := seen[candidate.KnowledgeID]; ok {
			continue
		}
		seen[candidate.KnowledgeID] = struct{}{}
		deduped = append(deduped, candidate)
		kbSet[candidate.KnowledgeBaseID] = struct{}{}
	}
	if len(deduped) == 0 {
		return []types.RecommendationItem{}
	}

	denied, err := s.deniedSet(ctx, tenantID, caller, kbSet)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"fail_closed": true, "surface": "recommendation",
		})
		return []types.RecommendationItem{}
	}
	scored := make([]types.RecommendationItem, 0, len(deduped))
	now := s.now()
	for _, item := range deduped {
		if _, isDenied := denied[item.KnowledgeID]; isDenied {
			continue
		}
		item.Score += s.baseScore(item, now)
		scored = append(scored, item)
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		return scored[i].KnowledgeID < scored[j].KnowledgeID
	})
	if len(scored) > limit {
		scored = scored[:limit]
	}
	return scored
}

// deniedSet returns the ids the caller must not see.
//
// 没有身份时返回错误并由调用方把整段清空：推荐位不做"匿名可看"。ACL 查询
// 或主体展开失败同样返回错误，调用方 fail-closed 清空该段列表，而不是
// 退化成不过滤——推荐位漏掉 ACL 就等于把无权标题推到人眼前。
func (s *recommendationService) deniedSet(
	ctx context.Context, tenantID uint64, caller types.Caller, kbSet map[string]struct{},
) (map[string]struct{}, error) {
	if caller.UserID == "" || s.acl == nil {
		return nil, errRecommendationAnonymous
	}
	kbIDs := make([]string, 0, len(kbSet))
	for kbID := range kbSet {
		if kbID != "" {
			kbIDs = append(kbIDs, kbID)
		}
	}
	if len(kbIDs) == 0 {
		return map[string]struct{}{}, nil
	}
	subjects, err := s.subjects.ForCaller(ctx, caller)
	if err != nil {
		return nil, err
	}
	denied, err := s.acl.DeniedKnowledgeIDsForSubjects(
		ctx, kbIDs, subjects, types.PermissionActionView, s.now(),
	)
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(denied))
	for _, id := range denied {
		out[id] = struct{}{}
	}
	return out, nil
}

// baseScore turns reason + recency into one comparable number.
func (s *recommendationService) baseScore(item types.RecommendationItem, now time.Time) float64 {
	score := item.Score
	switch item.Reason {
	case types.RecommendReasonRelated:
		score += s.weights.RelationEdge
	case types.RecommendReasonSameTag:
		score += s.weights.SharedTag
	case types.RecommendReasonInterest:
		score += s.weights.InterestTag
	case types.RecommendReasonSameSpace:
		score += s.weights.SameSpace
	case types.RecommendReasonHot:
		score += s.weights.HotPerView
	case types.RecommendReasonLatest:
		// 最新榜只按新鲜度排序。
	}
	if s.weights.FreshnessHalfLifeHours > 0 && !item.UpdatedAt.IsZero() {
		hours := now.Sub(item.UpdatedAt).Hours()
		if hours > 0 {
			score += 5 * math.Pow(0.5, hours/s.weights.FreshnessHalfLifeHours)
		} else {
			score += 5
		}
	}
	return score
}

// resolveRecommendationTenant keeps the tenant scope explicit.
func resolveRecommendationTenant(ctx context.Context, tenantID uint64) uint64 {
	if tenantID != 0 {
		return tenantID
	}
	if fromContext, ok := types.TenantIDFromContext(ctx); ok {
		return fromContext
	}
	return 0
}
