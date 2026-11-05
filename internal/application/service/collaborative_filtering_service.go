package service

import (
	"context"
	"strings"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// collaborativeFilteringService implements
// interfaces.CollaborativeFilteringService (KnowAxis V3.0 §15.3 协同过滤).
//
// 与规则型推荐的边界：那边的每一条推荐都能追溯到一条关系边或一个标签，
// 因此天然可解释；这里的推荐来自"看过 A 的人也看了 B"，可解释性必须由
// 返回结构自带（FromItemIDs + Similarity），否则用户看到一条无法归因的
// 推荐，运营也无法在推荐不准时定位。
type collaborativeFilteringService struct {
	repo interfaces.CollaborativeFilteringRepository
	// acl applies the same deny-wins gate as every other read surface.
	// 推荐位是最容易泄露"存在什么"的通道（§2.10），因此候选必须过滤。
	acl      interfaces.KnowledgeACLRepository
	subjects aclSubjectResolver
	weights  types.CFInteractionWeights
	window   time.Duration
	now      func() time.Time
}

// NewCollaborativeFilteringService constructs the service.
func NewCollaborativeFilteringService(
	repo interfaces.CollaborativeFilteringRepository,
	acl interfaces.KnowledgeACLRepository,
	subjectGroups interfaces.ACLSubjectGroupService,
) interfaces.CollaborativeFilteringService {
	return &collaborativeFilteringService{
		repo:     repo,
		acl:      acl,
		subjects: newACLSubjectResolver(subjectGroups),
		weights:  types.DefaultCFInteractionWeights(),
		// 90 天：与兴趣窗口同量级。更长会把"去年读过一次"当成长期偏好，
		// 更短则在稀疏交互下几乎算不出相似度。
		window: 90 * 24 * time.Hour,
		now:    time.Now,
	}
}

// RecommendForUser suggests items from the caller's own history.
//
// 失败策略：仓库读不到交互时返回空列表而不是错误。协同过滤是推荐的**补充**
// 通道，取不到信号不该让推荐位整体失败——规则型推荐仍然可用。
func (s *collaborativeFilteringService) RecommendForUser(
	ctx context.Context, tenantID uint64, userID string, exclude []string, limit int,
) ([]types.CFRecommendation, error) {
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	if strings.TrimSpace(userID) == "" {
		// 没有身份就没有历史，也就没有协同过滤可算。
		return []types.CFRecommendation{}, nil
	}
	since := s.now().UTC().Add(-s.window)
	history, err := s.repo.UserHistory(ctx, tenantID, userID, since, 200)
	if err != nil {
		logger.Warnf(ctx, "[cf] user history unavailable: %v", err)
		return []types.CFRecommendation{}, nil
	}
	if len(history) == 0 {
		return []types.CFRecommendation{}, nil
	}
	interactions, err := s.repo.Interactions(ctx, tenantID, since, 20000, s.weights)
	if err != nil {
		logger.Warnf(ctx, "[cf] interactions unavailable: %v", err)
		return []types.CFRecommendation{}, nil
	}
	model := types.BuildCFModel(interactions)
	recommendations := model.Recommend(history, exclude, limit, types.DefaultCFMinSimilarity)
	return s.filterVisible(ctx, tenantID, model, recommendations), nil
}

// SimilarItems returns items co-read with the given one.
func (s *collaborativeFilteringService) SimilarItems(
	ctx context.Context, tenantID uint64, itemID string, limit int,
) ([]types.CFRecommendation, error) {
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return nil, apperrors.NewBadRequestError("item id is required")
	}
	since := s.now().UTC().Add(-s.window)
	interactions, err := s.repo.Interactions(ctx, tenantID, since, 20000, s.weights)
	if err != nil {
		logger.Warnf(ctx, "[cf] interactions unavailable: %v", err)
		return []types.CFRecommendation{}, nil
	}
	model := types.BuildCFModel(interactions)
	neighbours := model.Neighbours(itemID, limit, types.DefaultCFMinSimilarity)
	return s.filterVisible(ctx, tenantID, model, neighbours), nil
}

// filterVisible drops recommendations the caller may not open.
//
// fail-closed：ACL 查询失败时返回空列表。推荐位漏掉 ACL 就等于把无权标题
// 推到人眼前，这与列表页、地图、检索的要求一致。
//
// 协同过滤的候选**跨空间**（相似度不区分知识库），因此不能像列表页那样传
// 单一 kb_id；这里按候选各自的 kb 集合做一次 deny-wins 判定。
func (s *collaborativeFilteringService) filterVisible(
	ctx context.Context, tenantID uint64,
	model *types.CFModel, recommendations []types.CFRecommendation,
) []types.CFRecommendation {
	if len(recommendations) == 0 {
		return []types.CFRecommendation{}
	}
	caller := types.CallerFromContext(ctx)
	if caller.UserID == "" {
		return []types.CFRecommendation{}
	}
	if s.acl == nil || model == nil {
		return recommendations
	}
	kbSet := make(map[string]struct{}, 4)
	for _, recommendation := range recommendations {
		if kbID := model.ItemKnowledgeBase[recommendation.ItemID]; kbID != "" {
			kbSet[kbID] = struct{}{}
		}
	}
	if len(kbSet) == 0 {
		// 候选都不带 kb（历史数据缺列）：无法判定可见性，fail-closed。
		logger.Warnf(ctx, "[cf] candidates carry no knowledge base, recommendations withheld")
		return []types.CFRecommendation{}
	}
	kbIDs := make([]string, 0, len(kbSet))
	for kbID := range kbSet {
		kbIDs = append(kbIDs, kbID)
	}
	subjects, err := s.subjects.ForCaller(ctx, caller)
	if err != nil {
		logger.Warnf(ctx, "[cf] subject expansion failed, recommendations withheld: %v", err)
		return []types.CFRecommendation{}
	}
	denied, err := s.acl.DeniedKnowledgeIDsForSubjects(
		ctx, kbIDs, subjects, types.PermissionActionView, s.now())
	if err != nil {
		logger.Warnf(ctx, "[cf] acl lookup failed, recommendations withheld: %v", err)
		return []types.CFRecommendation{}
	}
	deniedSet := make(map[string]struct{}, len(denied))
	for _, id := range denied {
		deniedSet[id] = struct{}{}
	}
	out := make([]types.CFRecommendation, 0, len(recommendations))
	for _, recommendation := range recommendations {
		if _, isDenied := deniedSet[recommendation.ItemID]; isDenied {
			continue
		}
		out = append(out, recommendation)
	}
	return out
}

// compile-time guard.
var _ interfaces.CollaborativeFilteringService = (*collaborativeFilteringService)(nil)
