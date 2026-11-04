package service

import (
	"context"
	"sort"
	"strconv"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// 图分析的审计动作。
const (
	auditActionGraphAnalysisExpert = "graph_analysis.experts_viewed"
)

// graphAnalysisService implements interfaces.GraphAnalysisService
// (KnowAxis V3.0 §11.1 六类核心能力中的中心度 / 社区检测 / 专家发现).
//
// 平台此前只有"直接关联"与"关联路径"：能回答"这两篇有什么关系"，但回答
// 不了"哪些是核心资产""领域怎么划分""谁最懂"。三者共用同一条取数路径，
// 差别只在算完之后怎么聚合，因此放在一个 vertical 里。
type graphAnalysisService struct {
	repo     interfaces.GraphAnalysisRepository
	acl      interfaces.KnowledgeACLRepository
	subjects aclSubjectResolver
	users    interfaces.UserService
	audit    interfaces.AuditLogService
	now      func() time.Time
}

// NewGraphAnalysisService constructs the service.
func NewGraphAnalysisService(
	repo interfaces.GraphAnalysisRepository,
	acl interfaces.KnowledgeACLRepository,
	subjectGroups interfaces.ACLSubjectGroupService,
	users interfaces.UserService,
	audit interfaces.AuditLogService,
) interfaces.GraphAnalysisService {
	return &graphAnalysisService{
		repo:     repo,
		acl:      acl,
		subjects: newACLSubjectResolver(subjectGroups),
		users:    users,
		audit:    audit,
		now:      time.Now,
	}
}

// subgraph is the permission-filtered input shared by all three analyses.
type subgraph struct {
	nodes     []types.GraphAnalysisNode
	edges     []types.KnowledgeMapEdge
	titles    map[string]string
	truncated bool
}

// load builds the visible subgraph of one space.
//
// 顺序与知识地图一致且不可颠倒：先取节点 → 取边 → 交给调用方算。边在仓库
// 层就要求两端都在节点集合内，所以权限过滤后的子图里不会残留"指向看不见的
// 文档"的边（§11.3 端点过滤：路径中任一节点无权限，整段不展示）。
//
// 取节点时多要一倍：权限会砍掉一部分，只取 limit 条会让一张大空间算出个
// 空图。截断标记只看节点上限，与权限过滤无关——后者不能对外暴露。
func (s *graphAnalysisService) load(
	ctx context.Context, tenantID uint64, knowledgeBaseID string, limit int,
) (*subgraph, error) {
	limit = types.NormalizeGraphAnalysisLimit(limit)
	fetch := limit * 2
	if fetch > types.MaxGraphAnalysisNodes {
		fetch = types.MaxGraphAnalysisNodes
	}
	nodes, err := s.repo.AnalysisNodes(ctx, tenantID, knowledgeBaseID, fetch)
	if err != nil {
		return nil, err
	}
	visible, err := s.filterNodes(ctx, tenantID, nodes)
	if err != nil {
		return nil, err
	}
	truncated := len(visible) > limit
	if truncated {
		visible = visible[:limit]
	}

	ids := make([]string, 0, len(visible))
	titles := make(map[string]string, len(visible))
	for _, node := range visible {
		ids = append(ids, node.KnowledgeID)
		if node.Title != "" {
			titles[node.KnowledgeID] = node.Title
		}
	}
	edges, err := s.repo.AnalysisEdges(ctx, tenantID, ids)
	if err != nil {
		return nil, err
	}
	return &subgraph{
		nodes: visible, edges: edges, titles: titles, truncated: truncated,
	}, nil
}

// filterNodes applies the ACL gate. Fail-closed: an error yields an error, not
// an unfiltered graph.
func (s *graphAnalysisService) filterNodes(
	ctx context.Context, tenantID uint64, nodes []types.GraphAnalysisNode,
) ([]types.GraphAnalysisNode, error) {
	if len(nodes) == 0 {
		return []types.GraphAnalysisNode{}, nil
	}
	caller := types.CallerFromContext(ctx)
	if caller.UserID == "" {
		// 无身份不返回任何图：图分析是最方便的存在性列举通道。
		return nil, apperrors.NewUnauthorizedError("caller identity is required")
	}
	if s.acl == nil {
		return nodes, nil
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
		return nil, err
	}
	denied, err := s.acl.DeniedKnowledgeIDsForSubjects(
		ctx, kbIDs, subjects, types.PermissionActionView, s.now())
	if err != nil {
		return nil, err
	}
	deniedSet := make(map[string]struct{}, len(denied))
	for _, id := range denied {
		deniedSet[id] = struct{}{}
	}
	visible := make([]types.GraphAnalysisNode, 0, len(nodes))
	for _, node := range nodes {
		if _, isDenied := deniedSet[node.KnowledgeID]; isDenied {
			continue
		}
		visible = append(visible, node)
	}
	return visible, nil
}

// Centrality ranks the space's documents by weighted PageRank (§11.1 中心度).
func (s *graphAnalysisService) Centrality(
	ctx context.Context, tenantID uint64, knowledgeBaseID string, limit int,
) (*types.CentralityReport, error) {
	if err := types.ValidateKnowledgeMapTarget(knowledgeBaseID); err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	graph, err := s.load(ctx, tenantID, knowledgeBaseID, limit)
	if err != nil {
		return nil, err
	}
	analysisNodes := make([]types.AnalysisNode, 0, len(graph.nodes))
	for _, node := range graph.nodes {
		analysisNodes = append(analysisNodes, types.AnalysisNode{
			ID:     node.KnowledgeID,
			Weight: types.AnalysisAuthorityWeight(node.LifecycleStatus),
		})
	}
	analysisEdges := make([]types.AnalysisEdge, 0, len(graph.edges))
	for _, edge := range graph.edges {
		analysisEdges = append(analysisEdges, types.AnalysisEdge{
			From: edge.From, To: edge.To, RelationType: edge.RelationType,
		})
	}
	return &types.CentralityReport{
		Nodes:       types.ComputeCentrality(analysisNodes, analysisEdges),
		Titles:      graph.titles,
		NodeCount:   len(analysisNodes),
		EdgeCount:   len(analysisEdges),
		Truncated:   graph.truncated,
		GeneratedAt: s.now().UTC(),
	}, nil
}

// Communities clusters the space's documents (§11.1 社区检测).
func (s *graphAnalysisService) Communities(
	ctx context.Context, tenantID uint64, knowledgeBaseID string, limit int,
) (*types.AnalysisCommunityReport, error) {
	if err := types.ValidateKnowledgeMapTarget(knowledgeBaseID); err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	graph, err := s.load(ctx, tenantID, knowledgeBaseID, limit)
	if err != nil {
		return nil, err
	}
	analysisNodes := make([]types.AnalysisNode, 0, len(graph.nodes))
	for _, node := range graph.nodes {
		analysisNodes = append(analysisNodes, types.AnalysisNode{ID: node.KnowledgeID})
	}
	analysisEdges := make([]types.AnalysisEdge, 0, len(graph.edges))
	for _, edge := range graph.edges {
		analysisEdges = append(analysisEdges, types.AnalysisEdge{
			From: edge.From, To: edge.To, RelationType: edge.RelationType,
		})
	}
	detected := types.DetectCommunities(analysisNodes, analysisEdges)
	// 社区 ID 直接用最小节点 id：它稳定、可复现，而且不需要额外的"领域名"
	// 概念。前端可以拿它反查该节点的标题作为这个簇的代表。
	return &types.AnalysisCommunityReport{
		Communities:     detected.Communities,
		IsolatedNodeIDs: detected.IsolatedNodeIDs,
		Titles:          graph.titles,
		NodeCount:       detected.NodeCount,
		EdgeCount:       detected.EdgeCount,
		Truncated:       graph.truncated,
		GeneratedAt:     s.now().UTC(),
	}, nil
}

// Experts ranks the space's domain experts (§11.1 专家发现).
//
// 三个信号各自有明确的取数口径，混在一起会得到一个无法解释的分数：
//   - Authored：本人作为作者/提交人的文档数（见 ExpertAuthorship 的优先级）；
//   - AcceptedAnswers / UsefulRatings：挂在这些文档下的社区问答；
//   - Citations：这些文档在窗口内被回答引用的次数。
//
// §11.3 的两条约束在这里落地：
//  1. 只统计有权查看的文档（filterNodes 已过滤）；
//  2. 未达最小证据量的人**不出现**在结果里，而不是出现并标注"证据不足"——
//     列出来本身就已经泄露了"这个人参与过这个领域"。
//
// 专家榜是管理面的信息（谁是专家会直接影响任务分派与考核），因此要求
// Admin+，并写审计日志：谁在什么时候看了这个领域的专家榜。
func (s *graphAnalysisService) Experts(
	ctx context.Context, tenantID uint64, knowledgeBaseID string,
	limit, windowDays int,
) (*types.ExpertReport, error) {
	if err := types.ValidateKnowledgeMapTarget(knowledgeBaseID); err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	windowDays = types.NormalizeExpertWindowDays(windowDays)
	now := s.now().UTC()
	since := now.AddDate(0, 0, -windowDays)

	graph, err := s.load(ctx, tenantID, knowledgeBaseID, limit)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(graph.nodes))
	authorOf := make(map[string]string, len(graph.nodes))
	for _, node := range graph.nodes {
		ids = append(ids, node.KnowledgeID)
	}
	authorship, err := s.repo.ExpertAuthorship(ctx, tenantID, ids)
	if err != nil {
		return nil, err
	}
	authorOf = authorship

	counters, err := s.repo.ExpertAnswerCounters(ctx, tenantID, ids, since)
	if err != nil {
		return nil, err
	}
	citations, err := s.repo.ExpertCitationCounts(ctx, tenantID, ids, since, 0)
	if err != nil {
		// 引用统计失败不阻断专家榜：少一个加权项比整份报告打不开要好，
		// 但必须留下日志，否则"分数为什么偏低"会变成谜。
		logger.Warnf(ctx, "[graph-analysis] citation counts unavailable: %v", err)
		citations = map[string]int{}
	}

	signals := s.collectExpertSignals(graph.nodes, authorOf, counters, citations)
	ranked := types.RankExperts(signals, types.DefaultExpertRankingWeights(), 0)
	eligible := make([]types.AnalysisExpertEntry, 0, len(ranked))
	for _, entry := range ranked {
		if !entry.Eligible {
			continue
		}
		eligible = append(eligible, entry)
	}
	// 剔除不合格者后重排：否则榜首位置会出现 Rank=7 这种空号。
	for index := range eligible {
		eligible[index].Rank = index + 1
	}

	// 名字只回填到最终入选的人身上，且是"能看见的"用户。用户服务不可用
	// 时留空 id，不阻断榜单。
	if s.users != nil && len(eligible) > 0 {
		userIDs := make([]string, 0, len(eligible))
		for _, entry := range eligible {
			userIDs = append(userIDs, entry.UserID)
		}
		if users, userErr := s.users.GetUsersByIDs(ctx, userIDs); userErr == nil {
			for index := range eligible {
				if user, ok := users[eligible[index].UserID]; ok && user != nil {
					eligible[index].DisplayName = user.Username
				}
			}
		} else {
			logger.Warnf(ctx, "[graph-analysis] user lookup unavailable: %v", userErr)
		}
	}

	s.logAudit(ctx, tenantID, knowledgeBaseID, len(eligible))
	return &types.ExpertReport{
		Experts:         eligible,
		Weights:         types.DefaultExpertRankingWeights(),
		MinEvidence:     types.MinExpertEvidence,
		WindowDays:      windowDays,
		Since:           since,
		KnowledgeBaseID: knowledgeBaseID,
		GeneratedAt:     now,
	}, nil
}

// collectExpertSignals folds the three data sources into one signal per user.
func (s *graphAnalysisService) collectExpertSignals(
	nodes []types.GraphAnalysisNode,
	authorOf map[string]string,
	counters []types.ExpertAnswerCounters,
	citations map[string]int,
) []types.ExpertSignal {
	byUser := make(map[string]*types.ExpertSignal, len(authorOf)+len(counters))
	ensure := func(userID string) *types.ExpertSignal {
		signal, ok := byUser[userID]
		if !ok {
			signal = &types.ExpertSignal{UserID: userID}
			byUser[userID] = signal
		}
		return signal
	}

	// 只有"有权查看的文档"才计入：filterNodes 已经把不可见的节点去掉，
	// 所以 authorOf 里不会被引用的条目自然也不参与。
	visible := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		visible[node.KnowledgeID] = struct{}{}
	}
	for knowledgeID, userID := range authorOf {
		if userID == "" {
			continue
		}
		if _, ok := visible[knowledgeID]; !ok {
			continue
		}
		ensure(userID).Authored++
	}
	for knowledgeID, count := range citations {
		if count <= 0 {
			continue
		}
		userID := authorOf[knowledgeID]
		if userID == "" {
			continue
		}
		if _, ok := visible[knowledgeID]; !ok {
			continue
		}
		ensure(userID).Citations += count
	}
	for _, counter := range counters {
		if counter.UserID == "" {
			continue
		}
		signal := ensure(counter.UserID)
		signal.AcceptedAnswers += counter.AcceptedAnswers
		signal.UsefulRatings += counter.UsefulRatings
	}

	out := make([]types.ExpertSignal, 0, len(byUser))
	for _, signal := range byUser {
		out = append(out, *signal)
	}
	// 排序交给 RankExperts，这里只需保证输入顺序与 map 迭代无关。
	sort.SliceStable(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out
}

// logAudit records who inspected the expert ranking.
func (s *graphAnalysisService) logAudit(
	ctx context.Context, tenantID uint64, knowledgeBaseID string, experts int,
) {
	if s.audit == nil {
		return
	}
	caller := types.CallerFromContext(ctx)
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: caller.UserID,
		Action:      types.AuditAction(auditActionGraphAnalysisExpert),
		ScopeType:   "graph_analysis",
		TargetType:  "knowledge_base",
		TargetID:    knowledgeBaseID,
		Outcome:     "success",
		Details:     types.JSON(`{"experts":` + strconv.Itoa(experts) + `}`),
	})
}

// compile-time guard.
var _ interfaces.GraphAnalysisService = (*graphAnalysisService)(nil)
