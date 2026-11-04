package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// GraphAnalysisRepository feeds the §11.1 graph algorithms.
//
// 它只负责"把一张已经限定在某个知识空间内的子图原样取出来"，不做任何权限
// 判断：ACL 过滤需要调用者的主体展开结果，属于服务层。方法签名里没有
// "隐藏了多少"这类返回值，是刻意的（§11.3 计数保护）——仓库层根本不知道
// 哪些行被过滤掉了。
type GraphAnalysisRepository interface {
	// AnalysisNodes returns up to limit searchable documents of one space.
	// 生命周期口径与知识地图一致：草稿/审批中/过期/下架不参与图计算。
	AnalysisNodes(
		ctx context.Context, tenantID uint64, knowledgeBaseID string, limit int,
	) ([]types.GraphAnalysisNode, error)
	// AnalysisEdges returns the relation edges whose BOTH ends are in the given
	// set. An edge to a document outside the set is dropped here rather than
	// filtered later: its existence would leak that the other end exists.
	AnalysisEdges(
		ctx context.Context, tenantID uint64, knowledgeIDs []string,
	) ([]types.KnowledgeMapEdge, error)
	// ExpertAuthorship maps knowledge id → author user id for the given
	// documents. Precedence: the candidate card's submitter when the document
	// came from §10 经验沉淀, otherwise the first version's publisher.
	//
	// 平台没有独立的"文档作者"列：上传类文档确实没有记录作者，这类文档
	// 只贡献引用计数，不贡献作者计数。宁可少算，也不要靠猜测（比如把
	// 租户 Owner 当作者）伪造出一个专家榜。
	ExpertAuthorship(
		ctx context.Context, tenantID uint64, knowledgeIDs []string,
	) (map[string]string, error)
	// ExpertAnswerCounters aggregates community answer signals per answerer,
	// restricted to questions attached to the given documents.
	ExpertAnswerCounters(
		ctx context.Context, tenantID uint64, knowledgeIDs []string, since time.Time,
	) ([]types.ExpertAnswerCounters, error)
	// ExpertCitationCounts counts, per document, how many distinct answered
	// turns cited it inside the window. Citation JSON is decoded in Go
	// because neither dialect offers a portable "count array elements" query.
	ExpertCitationCounts(
		ctx context.Context, tenantID uint64, knowledgeIDs []string,
		since time.Time, limit int,
	) (map[string]int, error)
}
