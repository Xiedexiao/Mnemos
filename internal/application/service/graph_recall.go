package service

import (
	"context"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// ============================================================================
// 图谱召回通道（KnowAxis V3.0 §12.2 "图谱扩展" 的融合侧）。
//
// 平台已有的图谱检索分两种：
//   - chat_pipeline/search_entity.go：把命中的实体 chunk **追加**到已排序结果
//     之后，作为独立阶段；
//   - graphSearchService：面向界面的可视化查询，返回实体与关系。
//
// 两者都不参与融合排序，所以图谱命中的名次只由"它在不在向量结果里"决定，
// 与它的实际相关性无关。这里补的是第三种：把图谱命中整理成一路候选，交给
// fuseWithRRF 与向量/关键词一起排序。
//
// 三个约束：
//  1. 图谱只提供**候选 chunk ID + 排名**，不提供分数。图匹配是二元的（实体
//     连到这段 chunk / 没连到），伪造一个梯度只是假精度。
//  2. 生命周期与 ACL 门禁在图谱召回里**不重复实现**：候选仍然走主检索的
//     params.ExcludeKnowledgeIDs（那里已经把生命周期、文档 ACL、目录 ACL
//     全部下推）。图谱召回只负责"哪些 chunk 与被查询实体相连"。
//  3. 图谱检索失败**不阻断**主检索：这一路是增强项。失败时记日志并返回空
//     通道，向量/关键词照常工作。
// ============================================================================

const (
	// graphRecallMaxEntities bounds how many query terms one recall extracts.
	graphRecallMaxEntities = 8
	// graphRecallMaxChunks bounds the chunk IDs the graph channel contributes.
	//
	// 上限存在的原因与检索池一致：图谱的邻居展开在企业图里可能是几百个
	// chunk，全塞进融合会让这一路凭数量压过向量召回。
	graphRecallMaxChunks = 40
	// graphRecallTimeout bounds the graph backend round trips.
	graphRecallTimeout = 5 * time.Second
)

// graphRecallTerms extracts the entity names to look up from a query.
//
// 规则刻意保守：查询本身 + 按空白切出的片段（长度 ≥ 2 rune）。不做实体
// 识别：图谱里存的是抽取出来的实体名，用整串查询去精确/子串匹配召回率高，
// 而错误切词会引入噪声邻居。中文长查询整串匹配不到时，退化为双字片段。
func graphRecallTerms(query string) []string {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return nil
	}
	seen := make(map[string]struct{}, graphRecallMaxEntities)
	terms := make([]string, 0, graphRecallMaxEntities)
	add := func(term string) {
		term = strings.TrimSpace(term)
		if len([]rune(term)) < 2 || len(terms) >= graphRecallMaxEntities {
			return
		}
		if _, duplicate := seen[term]; duplicate {
			return
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
	}

	add(trimmed)
	for _, field := range strings.Fields(trimmed) {
		add(field)
	}
	// 中文查询没有空格：补上**汉字**双字片段，让"E45 报警处理"里的"报警"
	// 也能命中。
	//
	// 只对汉字成对：把拉丁词也切成双字母（"alarm" → al/la/ar/rm）会产生大量
	// 无意义的伪实体名——图谱里的实体是抽取出来的名词短语，不是字母组。
	runes := []rune(trimmed)
	if len(terms) >= graphRecallMaxEntities {
		return terms
	}
	for i := 0; i+1 < len(runes) && len(terms) < graphRecallMaxEntities; i++ {
		if !unicode.Is(unicode.Han, runes[i]) || !unicode.Is(unicode.Han, runes[i+1]) {
			continue
		}
		add(string(runes[i : i+2]))
	}
	return terms
}

// graphRecallCandidates queries the graph for chunks linked to the query terms.
//
// 返回一个列表（单元素）：每一路检索在融合里都是"一个排好序的列表"，图谱
// 只有一路，所以列表长度为 1。这样 fuseWithRRF 不需要知道通道的内部结构。
//
// 返回的 IndexWithScore 只填 ChunkID / KnowledgeID / KnowledgeBaseID：
// 其余字段（内容、标题等）由主检索路径的 chunk 补全，图谱不参与元数据。
func (s *knowledgeBaseService) graphRecallCandidates(
	ctx context.Context, searchKBIDs []string, query string,
) [][]*types.IndexWithScore {
	if s.graphEngine == nil || len(searchKBIDs) == 0 {
		return nil
	}
	terms := graphRecallTerms(query)
	if len(terms) == 0 {
		return nil
	}
	recallCtx, cancel := context.WithTimeout(ctx, graphRecallTimeout)
	defer cancel()

	type namespaceResult struct {
		graph *types.GraphData
	}
	namespaces := make([]types.NameSpace, 0, len(searchKBIDs))
	for _, kbID := range searchKBIDs {
		if kbID != "" {
			namespaces = append(namespaces, types.NameSpace{KnowledgeBase: kbID})
		}
	}
	if len(namespaces) == 0 {
		return nil
	}

	results := make([]*types.GraphData, len(namespaces))
	var group sync.WaitGroup
	// 与 chat_pipeline 同一上限：图谱后端是外部服务，扇出要有限度。
	semaphore := make(chan struct{}, 4)
	for index, namespace := range namespaces {
		group.Add(1)
		go func(index int, namespace types.NameSpace) {
			defer group.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			// 每个 namespace 只查一次：把词表一次性交给 SearchNode，具体是
			// 精确还是子串匹配由后端实现决定。
			graph, err := s.graphEngine.SearchNode(recallCtx, namespace, terms)
			if err != nil {
				// 图谱是增强项：失败只记日志，不影响向量/关键词召回。
				logger.Warnf(recallCtx,
					"[graph-recall] entity search failed for namespace %s: %v",
					namespace.KnowledgeBase, err)
				return
			}
			results[index] = graph
		}(index, namespace)
	}
	group.Wait()

	// 按 entity 名称命中的"好程度"排序：整串查询命中的节点排前，双字片段
	// 命中的排后。这样图谱通道内部的 1..N 名次是有意义的，而不是来自后端
	// 的任意顺序。
	list := make([]*types.IndexWithScore, 0, graphRecallMaxChunks)
	seen := make(map[string]struct{}, graphRecallMaxChunks)
	exactTerm := strings.TrimSpace(query)
	for index, graph := range results {
		if graph == nil {
			continue
		}
		// GraphNode 不携带 knowledge base id（图谱后端按 namespace 查询），
		// 这里按 namespace 的下标补上，chunk 的归属就完整了。
		knowledgeBaseID := namespaces[index].KnowledgeBase
		// 两遍：先整串命中，再其余。
		for pass := 0; pass < 2; pass++ {
			for _, node := range graph.Node {
				if node == nil || len(node.Chunks) == 0 {
					continue
				}
				if pass == 0 && node.Name != exactTerm {
					continue
				}
				if pass == 1 && node.Name == exactTerm {
					continue
				}
				for _, chunkID := range node.Chunks {
					if chunkID == "" || len(list) >= graphRecallMaxChunks {
						continue
					}
					if _, duplicate := seen[chunkID]; duplicate {
						continue
					}
					seen[chunkID] = struct{}{}
					list = append(list, &types.IndexWithScore{
						ChunkID:         chunkID,
						KnowledgeID:     node.KnowledgeID,
						KnowledgeBaseID: knowledgeBaseID,
						MatchType:       types.MatchTypeGraph,
					})
				}
			}
		}
	}
	if len(list) == 0 {
		return nil
	}
	logger.Infof(ctx, "[graph-recall] %d chunks from %d terms across %d namespaces",
		len(list), len(terms), len(namespaces))
	return [][]*types.IndexWithScore{list}
}
