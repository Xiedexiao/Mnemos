package service

import (
	"context"
	"sort"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// knowledgeMapService implements interfaces.KnowledgeMapService
// (KnowAxis V3.0 §4.6 知识地图 / §15.5 岗位知识地图).
type knowledgeMapService struct {
	repo     interfaces.KnowledgeMapRepository
	acl      interfaces.KnowledgeACLRepository
	subjects aclSubjectResolver
	learning interfaces.LearningTaskService
	now      func() time.Time
}

// NewKnowledgeMapService constructs the service.
func NewKnowledgeMapService(
	repo interfaces.KnowledgeMapRepository,
	acl interfaces.KnowledgeACLRepository,
	subjectGroups interfaces.ACLSubjectGroupService,
	learning interfaces.LearningTaskService,
) interfaces.KnowledgeMapService {
	return &knowledgeMapService{
		repo:     repo,
		acl:      acl,
		subjects: newACLSubjectResolver(subjectGroups),
		learning: learning,
		now:      time.Now,
	}
}

// SpaceMap builds one space's knowledge map.
//
// 顺序是刻意的：先取节点、再取边、最后统一过滤。反过来做（先过滤边）会让
// 被过滤节点的边仍然出现在结果里，等同于告诉读者"某个你看不见的文档与此
// 有关"。边只在**两端都可见**时保留。
//
// 过滤分两层：
//  1. 生命周期——仓库层已排除草稿/审批中/过期/下架（与检索同一集合）；
//  2. 文档 ACL——这里按调用者的全部主体判定，失败即整张图清空（fail-closed）。
//
// 隐藏数量只返回聚合值，不点名任何文档（§2.10）。
func (s *knowledgeMapService) SpaceMap(
	ctx context.Context, tenantID uint64, knowledgeBaseID string,
	view types.KnowledgeMapView, limit int,
) (*types.KnowledgeMap, error) {
	if err := types.ValidateKnowledgeMapTarget(knowledgeBaseID); err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	if !view.Valid() {
		view = types.ParseKnowledgeMapView(string(view))
	}
	limit = types.NormalizeKnowledgeMapLimit(limit)

	// 多取一倍的候选：权限过滤会砍掉一部分，只取 limit 条会让地图莫名其妙
	// 变空。总数仍以 CountSpaceKnowledge 为准确口径。
	candidateLimit := limit * 2
	if candidateLimit > types.MaxKnowledgeMapNodes {
		candidateLimit = types.MaxKnowledgeMapNodes
	}
	nodes, err := s.repo.MapNodes(ctx, tenantID, knowledgeBaseID, candidateLimit)
	if err != nil {
		return nil, err
	}
	total, err := s.repo.CountSpaceKnowledge(ctx, tenantID, knowledgeBaseID)
	if err != nil {
		// 计数失败不阻断地图：截断标记退化为"按已加载数量判断"。
		logger.Warnf(ctx, "[knowledge-map] count failed: %v", err)
		total = int64(len(nodes))
	}

	visible, denied, aclErr := s.filterNodes(ctx, tenantID, nodes)
	if aclErr != nil {
		// fail-closed：没有权限结论时返回空地图，而不是完整地图。
		logger.ErrorWithFields(ctx, aclErr, map[string]interface{}{
			"fail_closed": true, "surface": "knowledge_map",
		})
		return &types.KnowledgeMap{
			KnowledgeBaseID: knowledgeBaseID,
			View:            view,
			Nodes:           []types.KnowledgeMapNode{},
			Edges:           []types.KnowledgeMapEdge{},
			Clusters:        []types.KnowledgeMapCluster{},
			Islands:         []string{},
			Hubs:            []string{},
			HiddenCount:     len(nodes),
		}, nil
	}

	if len(visible) > limit {
		visible = visible[:limit]
	}
	truncated := int64(len(visible)) < total
	// HiddenCount 的口径是"这个空间里有多少篇没有出现在图上"，因此必须在
	// 截断之**后**算：被上限截掉的文档同样不在图上，漏算会让 UI 显示
	// "只隐藏了 1 篇"而实际缺了几百篇。denied 是 ACL 过滤的明细计数，
	// 用它兜底保证不会小于真实被拒数量。
	hidden := int(total) - len(visible)
	if hidden < denied {
		hidden = denied
	}
	if hidden < 0 {
		hidden = 0
	}

	ids := make([]string, 0, len(visible))
	for _, node := range visible {
		ids = append(ids, node.KnowledgeID)
	}
	edges, err := s.repo.MapEdges(ctx, tenantID, ids)
	if err != nil {
		return nil, err
	}

	islands, hubs := types.KnowledgeMapStats(visible, edges)
	degree := make(map[string]int, len(visible))
	for _, edge := range edges {
		degree[edge.From]++
		degree[edge.To]++
	}
	for index := range visible {
		visible[index].Degree = degree[visible[index].KnowledgeID]
	}

	clusters := types.BuildKnowledgeMapClusters(view, visible, edges)
	// 回填簇标识，让每个节点自带归属，前端不必反查。
	clusterOf := make(map[string]string, len(visible))
	for _, cluster := range clusters {
		for _, id := range cluster.KnowledgeIDs {
			clusterOf[id] = cluster.ID
		}
	}
	for index := range visible {
		visible[index].ClusterID = clusterOf[visible[index].KnowledgeID]
	}
	for index := range clusters {
		types.KnowledgeMapClusterPreview(&clusters[index], 50)
	}

	return &types.KnowledgeMap{
		KnowledgeBaseID: knowledgeBaseID,
		View:            view,
		Nodes:           visible,
		Edges:           edges,
		Clusters:        clusters,
		Islands:         islands,
		Hubs:            hubs,
		RootFolders:     types.KnowledgeMapRootFolders(visible),
		Truncated:       truncated,
		HiddenCount:     hidden,
	}, nil
}

// RoleMap builds the job-role map.
//
// 岗位用标签定义：平台没有独立的岗位模型，而标签是它唯一显式的范围声明。
// 覆盖率把"岗位需要什么"（标签选出的文档）与"这个人学到哪"（学习任务）
// 连起来——这正是 §15.5 要求的"岗位知识地图与学习任务联动"。
func (s *knowledgeMapService) RoleMap(
	ctx context.Context, tenantID uint64, userID string, roleTags []string, limit int,
) (*types.RoleKnowledgeMap, error) {
	if userID == "" {
		return nil, apperrors.NewUnauthorizedError("caller identity is required")
	}
	tags := types.NormalizeTagNames(roleTags)
	if len(tags) == 0 {
		return nil, apperrors.NewBadRequestError("at least one role tag is required")
	}
	limit = types.NormalizeKnowledgeMapLimit(limit)
	nodes, err := s.repo.RoleMapNodes(ctx, tenantID, tags, limit*2)
	if err != nil {
		return nil, err
	}

	visible, _, aclErr := s.filterNodes(ctx, tenantID, nodes)
	if aclErr != nil {
		logger.ErrorWithFields(ctx, aclErr, map[string]interface{}{
			"fail_closed": true, "surface": "role_map",
		})
		return nil, aclErr
	}
	truncated := len(visible) > limit
	if truncated {
		visible = visible[:limit]
	}

	ids := make([]string, 0, len(visible))
	for _, node := range visible {
		ids = append(ids, node.KnowledgeID)
	}
	edges, err := s.repo.MapEdges(ctx, tenantID, ids)
	if err != nil {
		return nil, err
	}
	islands, hubs := types.KnowledgeMapStats(visible, edges)
	clusters := types.BuildKnowledgeMapClusters(types.KnowledgeMapViewFolder, visible, edges)
	for index := range clusters {
		types.KnowledgeMapClusterPreview(&clusters[index], 50)
	}

	required, completed := s.learningCoverage(ctx, tenantID, userID)
	// 只统计"与岗位文档相交"的任务：用户在别的岗位上的学习任务不该抬高
	// 或拉低这个岗位的覆盖率。
	visibleSet := make(map[string]struct{}, len(visible))
	for _, id := range ids {
		visibleSet[id] = struct{}{}
	}
	requiredForRole := intersect(required, visibleSet)
	completedForRole := intersect(completed, visibleSet)
	coverage := 0.0
	if len(requiredForRole) > 0 {
		coverage = float64(len(completedForRole)) / float64(len(requiredForRole)) * 100
	}

	return &types.RoleKnowledgeMap{
		RoleTags:              tags,
		Nodes:                 visible,
		Clusters:              clusters,
		Islands:               islands,
		Hubs:                  hubs,
		TotalDocuments:        len(visible),
		RequiredKnowledgeIDs:  requiredForRole,
		CompletedKnowledgeIDs: completedForRole,
		CoveragePct:           coverage,
		Truncated:             truncated,
		GeneratedAt:           s.now().UTC(),
	}, nil
}

// filterNodes applies the ACL gate to a node list.
func (s *knowledgeMapService) filterNodes(
	ctx context.Context, tenantID uint64, nodes []types.KnowledgeMapNode,
) ([]types.KnowledgeMapNode, int, error) {
	if len(nodes) == 0 {
		return []types.KnowledgeMapNode{}, 0, nil
	}
	caller := types.CallerFromContext(ctx)
	if caller.UserID == "" {
		// 无身份不返回地图：地图是最方便的存在性列举通道。
		return []types.KnowledgeMapNode{}, len(nodes), errRecommendationAnonymous
	}
	if s.acl == nil {
		return []types.KnowledgeMapNode{}, len(nodes), nil
	}
	kbSet := make(map[string]struct{}, 4)
	for _, node := range nodes {
		if node.KnowledgeBaseID != "" {
			kbSet[node.KnowledgeBaseID] = struct{}{}
		}
	}
	kbIDs := make([]string, 0, len(kbSet))
	for kbID := range kbSet {
		kbIDs = append(kbIDs, kbID)
	}
	subjects, err := s.subjects.ForCaller(ctx, caller)
	if err != nil {
		return nil, len(nodes), err
	}
	denied, err := s.acl.DeniedKnowledgeIDsForSubjects(
		ctx, kbIDs, subjects, types.PermissionActionView, s.now(),
	)
	if err != nil {
		return nil, len(nodes), err
	}
	deniedSet := make(map[string]struct{}, len(denied))
	for _, id := range denied {
		deniedSet[id] = struct{}{}
	}
	visible := make([]types.KnowledgeMapNode, 0, len(nodes))
	hidden := 0
	for _, node := range nodes {
		if _, isDenied := deniedSet[node.KnowledgeID]; isDenied {
			hidden++
			continue
		}
		visible = append(visible, node)
	}
	return visible, hidden, nil
}

// learningCoverage returns the caller's required and completed knowledge ids.
//
// 学习任务服务不可用时返回空集而不是报错：覆盖率是地图的附加信息，
// 打不开地图比看不到覆盖率严重得多。
func (s *knowledgeMapService) learningCoverage(
	ctx context.Context, tenantID uint64, userID string,
) (required []string, completed []string) {
	if s.learning == nil {
		return nil, nil
	}
	tasks, err := s.learning.List(ctx, tenantID, userID, "")
	if err != nil {
		logger.Warnf(ctx, "[knowledge-map] learning tasks unavailable: %v", err)
		return nil, nil
	}
	requiredSet := make(map[string]struct{})
	completedSet := make(map[string]struct{})
	for _, task := range tasks {
		if task == nil || task.KnowledgeID == "" {
			continue
		}
		switch types.LearningTaskStatus(task.Status) {
		case types.LearningTaskOpen:
			requiredSet[task.KnowledgeID] = struct{}{}
		case types.LearningTaskDone, types.LearningTaskWaived:
			completedSet[task.KnowledgeID] = struct{}{}
			// waived 也算完成（已具备资格）；但它同时也是"需要关注"的
			// 内容，因此仍进入 required，覆盖率不会因为豁免而虚高。
			requiredSet[task.KnowledgeID] = struct{}{}
		}
	}
	return mapSortedKeys(requiredSet), mapSortedKeys(completedSet)
}

// mapSortedKeys returns the map keys in a stable order. Local to this vertical
// so it does not collide with the gallery helper of the same shape.
func mapSortedKeys(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// intersect keeps the ids present in both sets, preserving the first list's order.
func intersect(ids []string, allowed map[string]struct{}) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := allowed[id]; ok {
			out = append(out, id)
		}
	}
	return out
}

// knowledgeMapLearningStatusesAreKnown is a compile-time reminder that the
// coverage switch above must handle every status.
var _ = []types.LearningTaskStatus{
	types.LearningTaskOpen, types.LearningTaskDone, types.LearningTaskWaived,
}
