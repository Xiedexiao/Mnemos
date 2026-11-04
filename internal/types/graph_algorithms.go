package types

import (
	"math"
	"sort"
	"strings"
)

// ============================================================================
// 图计算与关联（KnowAxis V3.0 §11.1 "六类核心能力"）。
//
// 平台此前只有"直接关联"与"关联路径"（关系表 + 遍历）：能回答"这两篇有
// 什么关系"，但回答不了"哪些知识是核心资产""这个领域怎么划分""谁最懂"。
//
// 这里补上剩下三项，全部是**纯计算**，不落库：
//   - 中心度：被依赖最多的知识排行（重点维护清单）；
//   - 社区检测：按关联图划分领域聚类，识别孤岛与桥接节点；
//   - 专家发现：按贡献、被采纳、引用聚合出"这个领域谁最懂"。
//
// 两个决定：
//  1. 不引第三方图库做这些计算。企业级单租户的关联图规模是千级到万级节点，
//     内存里的 O(V+E) 算法毫秒级完成；引入 Neo4j GDS 会让"质量看板"依赖
//     一个额外服务，收益不抵运维成本。
//  2. §11.3 的权限规则在这些算法上同样成立：输入必须是**已按权限过滤后的
//     子图**，且输出不能泄露"还有多少节点你看不到"。函数签名因此只接受
//     nodes/edges，不知道也不返回任何被过滤的信息。
// ============================================================================

// AnalysisNode is one vertex handed to the algorithms.
type AnalysisNode struct {
	// ID is the node identity (knowledge id).
	ID string
	// Weight is a caller-supplied authority hint. It scales how much THIS
	// node's outgoing dependency votes count: a document depended on by a
	// core regulation is worth more than one depended on by a stray note.
	Weight float64
}

// AnalysisEdge is one directed dependency edge: From depends on To.
//
// 方向语义统一为"From 依赖 To"：中心度算的是**入度**（被依赖），社区检测
// 忽略方向，专家发现用关系类型过滤。把方向固定下来，算法之间才不会各写
// 一套。
type AnalysisEdge struct {
	From string
	To   string
	// RelationType is the semantic kind ("based_on", "prerequisite", ...).
	RelationType string
}

// AnalysisCentralityEntry is one node's centrality.
type AnalysisCentralityEntry struct {
	// KnowledgeID the score belongs to.
	KnowledgeID string `json:"knowledge_id"`
	// Degree is the number of dependency edges touching the node.
	Degree int `json:"degree"`
	// InDegree counts documents that depend on this one (the maintenance
	// priority signal).
	InDegree int `json:"in_degree"`
	// OutDegree counts documents this one depends on.
	OutDegree int `json:"out_degree"`
	// Score is the weighted PageRank (see ComputeCentrality).
	Score float64 `json:"score"`
	// Rank is 1-based by score.
	Rank int `json:"rank"`
}

// ComputeCentrality ranks nodes by weighted PageRank.
//
// 为什么用 PageRank 而不是纯入度：入度把"被一篇没人引用的文档依赖"和
// "被核心制度依赖"算成同一分。PageRank 让来自高权重节点的边贡献更大，
// 这正是"核心资产"的定义。
//
// 阻尼固定 0.85、迭代 30 次：图规模在万级以内，30 次已足够收敛，且让它
// 在测试里行为完全确定（不依赖收敛阈值）。
func ComputeCentrality(nodes []AnalysisNode, edges []AnalysisEdge) []AnalysisCentralityEntry {
	if len(nodes) == 0 {
		return []AnalysisCentralityEntry{}
	}
	const (
		damping    = 0.85
		iterations = 30
	)
	index := make(map[string]int, len(nodes))
	weights := make([]float64, len(nodes))
	for i, node := range nodes {
		index[node.ID] = i
		weights[i] = 1.0
		if node.Weight > 0 {
			weights[i] = node.Weight
		}
	}

	outDegree := make([]int, len(nodes))
	inDegree := make([]int, len(nodes))
	degree := make([]int, len(nodes))
	// 按"依赖入边"聚合：from 依赖 to，权重流向 to。
	inbound := make([][]int, len(nodes))
	for _, edge := range edges {
		from, fromOK := index[edge.From]
		to, toOK := index[edge.To]
		if !fromOK || !toOK || from == to {
			continue
		}
		outDegree[from]++
		inDegree[to]++
		degree[from]++
		degree[to]++
		inbound[to] = append(inbound[to], from)
	}

	total := float64(len(nodes))
	rank := make([]float64, len(nodes))
	for i := range rank {
		rank[i] = 1 / total
	}
	next := make([]float64, len(nodes))
	for iteration := 0; iteration < iterations; iteration++ {
		for i := range next {
			next[i] = (1 - damping) / total
		}
		for to := range nodes {
			if len(inbound[to]) == 0 {
				continue
			}
			var sum float64
			for _, from := range inbound[to] {
				if outDegree[from] == 0 {
					continue
				}
				sum += rank[from] * weights[from] / float64(outDegree[from])
			}
			next[to] += damping * sum
		}
		// 悬空节点（出度为 0）的分数按 §"随机跳转"分摊回去，避免总量衰减。
		var dangling float64
		for i := range nodes {
			if outDegree[i] == 0 {
				dangling += rank[i]
			}
		}
		if dangling > 0 {
			share := damping * dangling / total
			for i := range next {
				next[i] += share
			}
		}
		rank, next = next, rank
	}

	entries := make([]AnalysisCentralityEntry, len(nodes))
	for i, node := range nodes {
		entries[i] = AnalysisCentralityEntry{
			KnowledgeID: node.ID,
			Degree:      degree[i],
			InDegree:    inDegree[i],
			OutDegree:   outDegree[i],
			Score:       math.Round(rank[i]*1e6) / 1e6,
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Score != entries[j].Score {
			return entries[i].Score > entries[j].Score
		}
		return entries[i].KnowledgeID < entries[j].KnowledgeID
	})
	for i := range entries {
		entries[i].Rank = i + 1
	}
	return entries
}

// AnalysisCommunity is one detected cluster.
type AnalysisCommunity struct {
	// ID is a stable identifier: the smallest node id in the community.
	ID string `json:"id"`
	// NodeIDs are the members, sorted.
	NodeIDs []string `json:"node_ids"`
	// Size is len(NodeIDs).
	Size int `json:"size"`
	// InternalEdges counts edges with both ends inside.
	InternalEdges int `json:"internal_edges"`
	// BridgeNodeIDs are members with an edge leading outside (the nodes that
	// connect this domain to another).
	BridgeNodeIDs []string `json:"bridge_node_ids"`
}

// AnalysisCommunityResult is the detection output.
type AnalysisCommunityResult struct {
	// Communities are the clusters, largest first.
	Communities []AnalysisCommunity `json:"communities"`
	// IsolatedNodeIDs have no edges at all (§11.1 盲区).
	IsolatedNodeIDs []string `json:"isolated_node_ids"`
	// NodeCount / EdgeCount echo the input size.
	NodeCount int `json:"node_count"`
	EdgeCount int `json:"edge_count"`
}

// DetectCommunities groups nodes by connected component and reports bridges.
//
// 用连通分量而不是 Louvain：连通分量的结果是**唯一确定**的（同一张图每次
// 得到同样的划分），而 Louvain 依赖随机初始顺序。对一个要进看板、要被讨论
// "为什么这两块被划到一起"的功能，可重复比"更优的模块度"更重要；真实图在
// 关联密度上通常本来就是分块的。
func DetectCommunities(nodes []AnalysisNode, edges []AnalysisEdge) *AnalysisCommunityResult {
	result := &AnalysisCommunityResult{
		Communities:     []AnalysisCommunity{},
		IsolatedNodeIDs: []string{},
		NodeCount:       len(nodes),
		EdgeCount:       len(edges),
	}
	if len(nodes) == 0 {
		return result
	}
	adjacency := make(map[string]map[string]struct{}, len(nodes))
	known := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		known[node.ID] = struct{}{}
		adjacency[node.ID] = map[string]struct{}{}
	}
	for _, edge := range edges {
		if _, ok := known[edge.From]; !ok {
			continue
		}
		if _, ok := known[edge.To]; !ok {
			continue
		}
		if edge.From == edge.To {
			continue
		}
		adjacency[edge.From][edge.To] = struct{}{}
		adjacency[edge.To][edge.From] = struct{}{}
	}

	visited := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		if visited[node.ID] {
			continue
		}
		component := make([]string, 0, 8)
		queue := []string{node.ID}
		visited[node.ID] = true
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			component = append(component, current)
			for peer := range adjacency[current] {
				if visited[peer] {
					continue
				}
				visited[peer] = true
				queue = append(queue, peer)
			}
		}
		sort.Strings(component)
		if len(component) == 1 && len(adjacency[component[0]]) == 0 {
			result.IsolatedNodeIDs = append(result.IsolatedNodeIDs, component[0])
			continue
		}
		members := make(map[string]struct{}, len(component))
		for _, id := range component {
			members[id] = struct{}{}
		}
		internal := 0
		bridgeSet := make(map[string]struct{})
		for _, id := range component {
			for peer := range adjacency[id] {
				if _, inside := members[peer]; inside {
					internal++
					continue
				}
				bridgeSet[id] = struct{}{}
			}
		}
		bridges := make([]string, 0, len(bridgeSet))
		for id := range bridgeSet {
			bridges = append(bridges, id)
		}
		sort.Strings(bridges)
		result.Communities = append(result.Communities, AnalysisCommunity{
			ID:            component[0],
			NodeIDs:       component,
			Size:          len(component),
			InternalEdges: internal / 2,
			BridgeNodeIDs: bridges,
		})
	}
	sort.SliceStable(result.Communities, func(i, j int) bool {
		if result.Communities[i].Size != result.Communities[j].Size {
			return result.Communities[i].Size > result.Communities[j].Size
		}
		return result.Communities[i].ID < result.Communities[j].ID
	})
	sort.Strings(result.IsolatedNodeIDs)
	return result
}

// ExpertSignal is one user's contribution evidence in one domain.
type ExpertSignal struct {
	UserID string
	// Authored counts documents this user created in the domain.
	Authored int
	// AcceptedAnswers counts community answers the asker adopted.
	AcceptedAnswers int
	// UsefulRatings counts "useful" feedback on answers.
	UsefulRatings int
	// Citations counts how often this user's documents were cited.
	Citations int
}

// AnalysisExpertEntry is one ranked expert.
type AnalysisExpertEntry struct {
	UserID string `json:"user_id"`
	// DisplayName is the username, filled by the service. Empty when the user
	// row is gone or the lookup was unavailable — the id still identifies them.
	DisplayName     string  `json:"display_name,omitempty"`
	Score           float64 `json:"score"`
	Rank            int     `json:"rank"`
	Authored        int     `json:"authored"`
	AcceptedAnswers int     `json:"accepted_answers"`
	UsefulRatings   int     `json:"useful_ratings"`
	Citations       int     `json:"citations"`
	// Eligible reports whether the minimum evidence bar was met.
	Eligible bool `json:"eligible"`
}

// ExpertRankingWeights tunes the expert score.
type ExpertRankingWeights struct {
	Authored        float64
	AcceptedAnswers float64
	UsefulRatings   float64
	Citations       float64
}

// DefaultExpertRankingWeights is the v1 ranking.
//
// 被采纳的答案权重最高：它同时证明了"懂"和"讲清楚了"，比"写得多"更强。
func DefaultExpertRankingWeights() ExpertRankingWeights {
	return ExpertRankingWeights{
		Authored: 1, AcceptedAnswers: 5, UsefulRatings: 2, Citations: 3,
	}
}

// MinExpertEvidence is the minimum number of signals before someone is listed.
//
// §11.3 要求"设置最小案例数，避免反推敏感项目"：一个人只有 1 篇文档时，
// 把他标成"这个领域的专家"既不可靠，也在泄露他参与了哪个项目。
const MinExpertEvidence = 3

// RankExperts scores and orders domain experts.
func RankExperts(
	signals []ExpertSignal, weights ExpertRankingWeights, limit int,
) []AnalysisExpertEntry {
	entries := make([]AnalysisExpertEntry, 0, len(signals))
	for _, signal := range signals {
		evidence := signal.Authored + signal.AcceptedAnswers +
			signal.UsefulRatings + signal.Citations
		score := weights.Authored*float64(signal.Authored) +
			weights.AcceptedAnswers*float64(signal.AcceptedAnswers) +
			weights.UsefulRatings*float64(signal.UsefulRatings) +
			weights.Citations*float64(signal.Citations)
		entries = append(entries, AnalysisExpertEntry{
			UserID:          strings.TrimSpace(signal.UserID),
			Score:           math.Round(score*100) / 100,
			Authored:        signal.Authored,
			AcceptedAnswers: signal.AcceptedAnswers,
			UsefulRatings:   signal.UsefulRatings,
			Citations:       signal.Citations,
			Eligible:        evidence >= MinExpertEvidence,
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Eligible != entries[j].Eligible {
			return entries[i].Eligible
		}
		if entries[i].Score != entries[j].Score {
			return entries[i].Score > entries[j].Score
		}
		return entries[i].UserID < entries[j].UserID
	})
	for i := range entries {
		entries[i].Rank = i + 1
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return entries
}
