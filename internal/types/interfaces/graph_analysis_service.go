package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// GraphAnalysisService answers the three §11.1 questions the platform could
// not: which documents are core assets, how the domain splits up, and who
// knows it best.
//
// 三个方法都只接受 knowledge_base_id：跨空间聚合会让"这个空间谁最懂"变成
// "全租户谁最懂"，而专家榜的用途是分派某个具体领域的任务。租户级视图由
// 调用方对每个空间各调一次再合并，权限口径才不会被悄悄放宽。
type GraphAnalysisService interface {
	// Centrality ranks the space's documents by weighted PageRank.
	Centrality(
		ctx context.Context, tenantID uint64, knowledgeBaseID string, limit int,
	) (*types.CentralityReport, error)
	// Communities clusters the space's documents by connected component and
	// reports the isolated ones (§11.1 盲区).
	Communities(
		ctx context.Context, tenantID uint64, knowledgeBaseID string, limit int,
	) (*types.AnalysisCommunityReport, error)
	// Experts ranks the space's domain experts under the evidence floor.
	Experts(
		ctx context.Context, tenantID uint64, knowledgeBaseID string,
		limit, windowDays int,
	) (*types.ExpertReport, error)
}
