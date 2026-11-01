package chatpipeline

import (
	"context"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// materializeSymbolHits turns exact symbol-index hits into SearchResults.
// Score is pinned to 1.0 (an exact symbol hit outranks fused retrieval) and
// the chunk body is fetched through the chunk service so downstream citation
// handling sees a normal result. Document-level rows (empty chunk_id) are
// skipped — the fusion path will re-surface their content on its own.
func (p *PluginSearch) materializeSymbolHits(
	ctx context.Context, tenantID uint64, hits []*types.SymbolHit,
) []*types.SearchResult {
	if len(hits) == 0 || p.chunkService == nil {
		return nil
	}
	results := make([]*types.SearchResult, 0, len(hits))
	for _, hit := range hits {
		if hit == nil || hit.KnowledgeID == "" || hit.ChunkID == "" {
			continue
		}
		chunk, err := p.chunkService.GetChunkByID(ctx, hit.ChunkID)
		if err != nil || chunk == nil {
			logger.Warnf(ctx, "symbol hit chunk %s missing: %v", hit.ChunkID, err)
			continue
		}
		locator := types.SourceLocators{}
		if decoded := hit.DecodeLocator(); decoded != nil {
			locator = types.SourceLocators{*decoded}
		}
		results = append(results, &types.SearchResult{
			ID:              chunk.ID,
			KnowledgeID:     hit.KnowledgeID,
			KnowledgeBaseID: chunk.KnowledgeBaseID,
			Content:         chunk.Content,
			Score:           1.0,
			MatchType:       types.MatchTypeKeywords,
			SourceLocators:  locator,
		})
	}
	return results
}
