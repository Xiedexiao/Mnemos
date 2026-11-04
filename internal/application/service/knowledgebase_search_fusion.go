package service

import (
	"context"
	"math"
	"slices"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// classifyRetrievalResults separates retrieval results by retriever type
// (vector vs keyword). Each RetrieveResult stays its own list: they come from
// different engines, store groups and parameter sets (document vs FAQ), are
// appended in goroutine completion order, and their positions are only
// meaningful within the list.
func classifyRetrievalResults(ctx context.Context, retrieveResults []*types.RetrieveResult) (
	vectorLists, keywordLists [][]*types.IndexWithScore,
) {
	for _, retrieveResult := range retrieveResults {
		logger.Infof(ctx, "Retrieval results, engine: %v, retriever: %v, count: %v",
			retrieveResult.RetrieverEngineType,
			retrieveResult.RetrieverType,
			len(retrieveResult.Results),
		)
		if len(retrieveResult.Results) == 0 {
			continue
		}
		if retrieveResult.RetrieverType == types.VectorRetrieverType {
			vectorLists = append(vectorLists, retrieveResult.Results)
		} else {
			keywordLists = append(keywordLists, retrieveResult.Results)
		}
	}
	return
}

// flattenLists concatenates retrieval lists.
func flattenLists(lists [][]*types.IndexWithScore) []*types.IndexWithScore {
	var out []*types.IndexWithScore
	for _, list := range lists {
		out = append(out, list...)
	}
	return out
}

// fuseOrDeduplicate fuses the retrieval channels via RRF, or deduplicates when
// only a single channel produced results.
//
// KnowAxis V3.0 §12.2 "图谱增强"：图谱召回与向量/关键词**在同一个融合里**
// 排序。此前图谱是一个独立的后置阶段（chat_pipeline/search_entity.go），它的
// 结果被 append 到已排序列表的末尾——这意味着图谱命中的名次完全由"不在向量
// 结果里"决定，即使它是唯一真正相关的那条。并入 RRF 之后，一条同时被向量与
// 图谱命中的 chunk 会累加两路名次贡献，这正是融合要表达的信号。
//
// Every path returns scores in [0, 1] so results of separate searches
// (per-document targets, embedding-model groups, FAQ vs document calls) can be
// ranked together downstream:
//   - vector only: the engines' cosine similarity, unchanged (FAQ thresholds
//     depend on it);
//   - keyword only: BM25 divided by the best score of its own list;
//   - graph only: 1.0 for every graph hit — a graph match is binary (the entity
//     either links this chunk or it does not), so inventing a gradient would be
//     false precision;
//   - multi-channel: weighted RRF divided by its maximum.
//
// retrievalCfg may be nil — defaults are then used for RRF parameters.
func fuseOrDeduplicate(
	ctx context.Context,
	vectorLists, keywordLists, graphLists [][]*types.IndexWithScore,
	retrievalCfg *types.RetrievalConfig,
) []*types.IndexWithScore {
	channelCount := 0
	for _, lists := range [][][]*types.IndexWithScore{vectorLists, keywordLists, graphLists} {
		if len(lists) > 0 {
			channelCount++
		}
	}
	if channelCount == 0 {
		return nil
	}
	if channelCount > 1 {
		result := fuseWithRRF(ctx, vectorLists, keywordLists, graphLists, retrievalCfg)
		logger.Infof(ctx, "Result count after RRF fusion: %d", len(result))
		return result
	}
	// Single channel: keep its native scores, normalized where they are unbounded.
	switch {
	case len(vectorLists) > 0:
		result := deduplicateByScore(flattenLists(vectorLists))
		logger.Infof(ctx, "Result count after deduplication: %d", len(result))
		return result
	case len(keywordLists) > 0:
		for _, list := range keywordLists {
			rescaleUnboundedScores(list)
		}
		result := deduplicateByScore(flattenLists(keywordLists))
		logger.Infof(ctx, "Result count after deduplication: %d", len(result))
		return result
	default:
		// Graph-only: every hit is a full match (see the doc comment).
		for _, list := range graphLists {
			for _, row := range list {
				if row != nil {
					row.Score = 1.0
				}
			}
		}
		result := deduplicateByScore(flattenLists(graphLists))
		logger.Infof(ctx, "Result count after graph deduplication: %d", len(result))
		return result
	}
}

// sortByScoreDesc is a reusable sort comparator for IndexWithScore slices (descending by Score).
func sortByScoreDesc(a, b *types.IndexWithScore) int {
	if a.Score > b.Score {
		return -1
	} else if a.Score < b.Score {
		return 1
	}
	return 0
}

// deduplicateByScore deduplicates retrieval results by chunk ID, keeping the highest score
// for each chunk. Returns the results sorted by score descending.
// Used when only a single retriever (e.g. vector-only for FAQ) is active.
func deduplicateByScore(results []*types.IndexWithScore) []*types.IndexWithScore {
	chunkInfoMap := make(map[string]*types.IndexWithScore, len(results))
	for _, r := range results {
		if existing, exists := chunkInfoMap[r.ChunkID]; !exists || r.Score > existing.Score {
			chunkInfoMap[r.ChunkID] = r
		}
	}
	deduped := make([]*types.IndexWithScore, 0, len(chunkInfoMap))
	for _, info := range chunkInfoMap {
		deduped = append(deduped, info)
	}
	slices.SortFunc(deduped, sortByScoreDesc)
	return deduped
}

// rescaleUnboundedScores maps a single-retriever candidate set onto [0, 1]
// by dividing through the maximum finite score. Keyword (BM25) scores are
// unbounded; leaving them in place saturates compositeScore (0.3*base with
// a [0, 1] clamp) so every candidate ties at 1.0 and MMR degenerates to
// diversity-only ordering.
//
// Scores already in [0, 1] are left unchanged so engines that stamp
// keyword hits at 1.0 (Qdrant, Doris) and already-normalized vector
// scores keep their current magnitude. Relative order is preserved
// either way. Retrieve/Langfuse sampling happens before fusion, so
// raw BM25 remains visible in the retrieve span.
func rescaleUnboundedScores(results []*types.IndexWithScore) {
	maxScore := 0.0
	for _, r := range results {
		if r == nil {
			continue
		}
		if math.IsNaN(r.Score) || math.IsInf(r.Score, 0) {
			continue
		}
		if r.Score > maxScore {
			maxScore = r.Score
		}
	}
	if maxScore <= 1 {
		return
	}
	for _, r := range results {
		if r == nil {
			continue
		}
		switch {
		case math.IsNaN(r.Score), math.IsInf(r.Score, -1), r.Score <= 0:
			r.Score = 0
		case math.IsInf(r.Score, 1):
			r.Score = 1
		default:
			r.Score = r.Score / maxScore
		}
	}
}

// fuseWithRRF merges vector and keyword retrieval results using Reciprocal Rank Fusion.
// RRF score = vectorWeight/(k+vectorRank) + keywordWeight/(k+keywordRank),
// divided by its maximum (vectorWeight+keywordWeight)/(k+1) so it lands in
// [0, 1] like the single-retriever paths. Raw RRF tops out near 0.016, which
// made hybrid hits lose to any vector-only or keyword-only result they were
// ranked against and left rerank's base-score term and MMR's relevance term
// with nothing to work with.
//
// A chunk's rank for a retriever is its best position in any one list of
// that retriever, each list ordered by its own score. Positions in the
// concatenation of several lists are arbitrary: the second list's best hit
// would otherwise rank behind every hit of the first.
// k, vectorWeight and keywordWeight are sourced from retrievalCfg (with defaults).
// The merged results are sorted by RRF score descending.
func fuseWithRRF(
	ctx context.Context,
	vectorLists, keywordLists, graphLists [][]*types.IndexWithScore,
	retrievalCfg *types.RetrievalConfig,
) []*types.IndexWithScore {
	rrfK := retrievalCfg.GetEffectiveRRFK()
	vectorWeight, keywordWeight, graphWeight := retrievalCfg.GetEffectiveRRFWeights()

	vectorRanks := bestRanks(vectorLists)
	keywordRanks := bestRanks(keywordLists)
	graphRanks := bestRanks(graphLists)

	// Collect all unique chunks — prefer the first channel that saw a chunk for
	// its metadata, in the order vector → keyword → graph (vector carries the
	// richest payload: VectorScore, image info).
	chunkInfoMap := make(map[string]*types.IndexWithScore)
	for _, r := range flattenLists(vectorLists) {
		if existing, exists := chunkInfoMap[r.ChunkID]; !exists || r.Score > existing.Score {
			chunkInfoMap[r.ChunkID] = r
		}
	}
	for _, lists := range [][][]*types.IndexWithScore{keywordLists, graphLists} {
		for _, r := range flattenLists(lists) {
			if _, exists := chunkInfoMap[r.ChunkID]; !exists {
				chunkInfoMap[r.ChunkID] = r
			}
		}
	}

	// The normalization constant is the score a chunk ranked first by every
	// **participating** channel would receive. Dividing by it keeps the output
	// in [0, 1]; a chunk that only one channel saw therefore scores below 1
	// even at rank 1, which is the intended signal that agreement is worth
	// more than a single match.
	//
	// 只计入实际产出结果的通道，而不是配置里的全部权重：图谱通道在多数空间
	// 里没有命中（没抽取过实体），把它的 0.2 也计入分母会让所有分数被永久
	// 压低 1.2 倍，既与既往行为不兼容，也把"图谱这一路没参与"错误地表达成
	// "图谱参与了但没帮上忙"。
	participatingWeight := 0.0
	if len(vectorRanks) > 0 {
		participatingWeight += vectorWeight
	}
	if len(keywordRanks) > 0 {
		participatingWeight += keywordWeight
	}
	if len(graphRanks) > 0 {
		participatingWeight += graphWeight
	}
	maxRRF := participatingWeight / float64(rrfK+1)
	if maxRRF <= 0 {
		maxRRF = 1
	}

	result := make([]*types.IndexWithScore, 0, len(chunkInfoMap))
	for chunkID, info := range chunkInfoMap {
		rrfScore := 0.0
		if rank, ok := vectorRanks[chunkID]; ok {
			rrfScore += vectorWeight / float64(rrfK+rank)
		}
		if rank, ok := keywordRanks[chunkID]; ok {
			rrfScore += keywordWeight / float64(rrfK+rank)
		}
		if rank, ok := graphRanks[chunkID]; ok {
			rrfScore += graphWeight / float64(rrfK+rank)
		}
		info.Score = rrfScore / maxRRF
		// MatchType 保持各通道的原始值：一条既被向量又被图谱命中的 chunk
		// 仍然显示为向量命中（向量通道排在收集顺序的第一位），图谱只是给它
		// 加了票。把 MatchType 改成 graph 会丢失"向量也命中了"这个更强的证据。
		result = append(result, info)
	}
	slices.SortFunc(result, sortByScoreDesc)

	for i, chunk := range result {
		if i >= 15 {
			break
		}
		vRank, vOk := vectorRanks[chunk.ChunkID]
		kRank, kOk := keywordRanks[chunk.ChunkID]
		gRank, gOk := graphRanks[chunk.ChunkID]
		logger.Debugf(ctx,
			"RRF rank %d: chunk_id=%s, rrf_score=%.6f, vector_rank=%v(%v), keyword_rank=%v(%v), graph_rank=%v(%v)",
			i, chunk.ChunkID, chunk.Score, vRank, vOk, kRank, kOk, gRank, gOk)
	}
	return result
}

// bestRanks returns each chunk's best 1-based rank across lists, ranking every
// list on its own by score (a chunk repeated in a list keeps its best score).
func bestRanks(lists [][]*types.IndexWithScore) map[string]int {
	ranks := make(map[string]int)
	for _, list := range lists {
		for i, r := range deduplicateByScore(list) {
			if current, ok := ranks[r.ChunkID]; !ok || i+1 < current {
				ranks[r.ChunkID] = i + 1
			}
		}
	}
	return ranks
}
