package types

import "time"

// ============================================================================
// 图分析接口载荷（KnowAxis V3.0 §11.1 中心度 / 社区检测 / 专家发现）。
//
// 纯算法在 graph_algorithms.go，这里只放"接口怎么说"的部分：节点行、请求
// 参数归一、响应包装。分开的理由是算法文件不该知道分页、标题回填、时间戳
// 这些接口层的东西，否则它就无法被其它调用方（离线聚类、导出）直接复用。
//
// 三个 §11.3 的硬约束体现在这里：
//  1. 输入永远是**已按权限过滤后的子图**，所以响应里不带任何"被隐藏了多少"
//     的计数——连聚合计数都不能给，否则等于承认存在看不见的节点。
//  2. 标题只回填可见节点；不可见节点的标题既不出现也不留空占位。
//  3. 专家列表只输出达到最小证据量的人（MinExpertEvidence），不足者直接
//     剔除而不是标记"数据不足"：标出来本身就是"这个人参与过这个领域"的
//     信号，正是要避免的反推。
// ============================================================================

const (
	// DefaultGraphAnalysisNodes is the default node cap for one analysis call.
	DefaultGraphAnalysisNodes = 500
	// MaxGraphAnalysisNodes caps it. PageRank over 2000 nodes and a few
	// thousand edges is still milliseconds, but the response has to stay
	// renderable as a graph, so the ceiling is about the reader, not the CPU.
	MaxGraphAnalysisNodes = 2000
	// DefaultExpertWindowDays is how far back expert signals are counted.
	DefaultExpertWindowDays = 90
)

// GraphAnalysisNode is one row handed from the repository to the service.
//
// LifecycleStatus is carried (not just used for filtering) because it becomes
// the voting weight: a dependency declared by a 待复审 document says less about
// what the organisation relies on than one declared by a valid document.
type GraphAnalysisNode struct {
	KnowledgeID     string
	KnowledgeBaseID string
	Title           string
	LifecycleStatus string
}

// NormalizeGraphAnalysisLimit clamps the requested node cap.
func NormalizeGraphAnalysisLimit(limit int) int {
	if limit <= 0 {
		return DefaultGraphAnalysisNodes
	}
	if limit > MaxGraphAnalysisNodes {
		return MaxGraphAnalysisNodes
	}
	return limit
}

// NormalizeExpertWindowDays clamps the expert look-back window.
func NormalizeExpertWindowDays(days int) int {
	if days <= 0 {
		return DefaultExpertWindowDays
	}
	if days > 365 {
		return 365
	}
	return days
}

// AnalysisAuthorityWeight maps a lifecycle status onto the centrality voting
// weight (§7.1 状态行为表).
//
// valid = 1.0；pending_review_cycle / stale = 0.5：这两类"可以读到但不作为
// 权威依据"，所以它们的依赖声明只算半票。0 表示不参与投票（调用方在取节点
// 时已排除了草稿/过期/下架，这里只是兜底）。
func AnalysisAuthorityWeight(lifecycle string) float64 {
	switch KnowledgeLifecycleStatus(lifecycle) {
	case KnowledgeLifecycleValid:
		return 1.0
	case KnowledgeLifecyclePendingReviewCycle, KnowledgeLifecycleStale:
		return 0.5
	default:
		// 空串按 valid 处理：与检索、地图等处 COALESCE(NULLIF(...),'valid')
		// 的口径保持一致，历史行不会因为没写状态就失去投票权。
		if lifecycle == "" {
			return 1.0
		}
		return 0
	}
}

// ExpertAnswerCounters is one answerer's community aggregates.
//
// 字段名与 ExpertSignal 对齐，服务层可以直接参与打分，不必再做一层映射。
type ExpertAnswerCounters struct {
	UserID string `gorm:"column:user_id"`
	// AcceptedAnswers counts answers the asker adopted.
	AcceptedAnswers int `gorm:"column:accepted_answers"`
	// UsefulRatings counts "this helped" votes across the user's answers.
	UsefulRatings int `gorm:"column:useful_ratings"`
}

// CentralityReport is the §11.1 中心度 response.
type CentralityReport struct {
	// Nodes ranked by weighted PageRank, highest first.
	Nodes []AnalysisCentralityEntry `json:"nodes"`
	// Titles maps knowledge id → title for the listed nodes only.
	Titles map[string]string `json:"titles"`
	// NodeCount / EdgeCount are the analysed subgraph size.
	NodeCount int `json:"node_count"`
	EdgeCount int `json:"edge_count"`
	// Truncated reports whether the node cap cut the graph. It counts the
	// cap, never a permission filter — §11.3 forbids exposing the latter.
	Truncated   bool      `json:"truncated"`
	GeneratedAt time.Time `json:"generated_at"`
}

// AnalysisCommunityReport is the §11.1 社区检测 response.
//
// 名字带 Analysis 前缀是因为 CommunityReport 已被 §15.6 社区的举报模型占用：
// 两者都是"报告"，但一个是图算法的输出，一个是用户提交的违规举报。
type AnalysisCommunityReport struct {
	// Communities clustered largest first.
	Communities []AnalysisCommunity `json:"communities"`
	// IsolatedNodeIDs are documents with no relation at all (§11.1 盲区).
	IsolatedNodeIDs []string `json:"isolated_node_ids"`
	// Titles maps knowledge id → title for the listed nodes only.
	Titles map[string]string `json:"titles"`
	// NodeCount / EdgeCount are the analysed subgraph size.
	NodeCount int `json:"node_count"`
	EdgeCount int `json:"edge_count"`
	// Truncated reports whether the node cap cut the graph.
	Truncated   bool      `json:"truncated"`
	GeneratedAt time.Time `json:"generated_at"`
}

// ExpertReport is the §11.1 专家发现 response.
type ExpertReport struct {
	// Experts ranked by the weighted score; only entries meeting
	// MinExpertEvidence are present.
	Experts []AnalysisExpertEntry `json:"experts"`
	// Weights echoes the scoring so a reader can recompute every score.
	Weights ExpertRankingWeights `json:"weights"`
	// MinEvidence echoes the evidence bar that was applied.
	MinEvidence int `json:"min_evidence"`
	// WindowDays / Since bound the signals that were counted.
	WindowDays int       `json:"window_days"`
	Since      time.Time `json:"since"`
	// KnowledgeBaseID scopes the report; empty means the whole tenant.
	KnowledgeBaseID string    `json:"knowledge_base_id"`
	GeneratedAt     time.Time `json:"generated_at"`
}
