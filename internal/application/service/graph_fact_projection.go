package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// graphFactProjection turns one chunk's LLM extraction into the versioned graph
// fact layer (KnowAxis V3.0 §8.5/§9.4).
//
// Why this exists: `graph_fact_service` (entities, facts, GraphCommit) was fully
// implemented but had no producer — extraction only wrote to the legacy graph
// store, so none of §8's versioning, path/impact queries or evidence binding
// applied to actually-ingested content. This projection is that missing
// producer.
//
// It writes one commit per chunk rather than per document: a GraphCommit is the
// audit anchor for a batch, and a per-chunk commit keeps the blast radius of a
// bad extraction to the chunk that produced it, so rollback stays surgical.
// graphProjectionEntityType is the placeholder ontology type for entities that
// come straight out of extraction. The extractor emits names without a type, so
// typing them properly is ontology alignment's job (§9.4 本体对齐), not this
// projection's.
const graphProjectionEntityType = "extracted"

type graphFactProjection struct {
	facts interfaces.GraphFactService
}

// projectChunkEntities converts an extraction result into entities and facts
// bound to the source chunk, and commits them.
//
// Every fact carries the chunk as evidence. That is the §9.4 hard rule: a fact
// without a source cannot be verified or withdrawn, so the projection refuses to
// invent provenance it does not have.
func (p *graphFactProjection) projectChunk(
	ctx context.Context, tenantID uint64, chunk *types.Chunk, graph *types.GraphData, actorID string,
) (*types.GraphCommit, error) {
	if p == nil || p.facts == nil || chunk == nil || graph == nil {
		return nil, nil
	}
	if len(graph.Node) == 0 && len(graph.Relation) == 0 {
		return nil, nil
	}

	evidence, err := json.Marshal([]types.GraphFactEvidence{{
		KnowledgeID: chunk.KnowledgeID,
		ChunkID:     chunk.ID,
		Quote:       excerptForEvidence(chunk.Content),
	}})
	if err != nil {
		return nil, fmt.Errorf("graph projection: marshal evidence: %w", err)
	}

	// 抽取结果只给出实体名（GraphNode 没有类型字段），所以投影统一落在
	// 一个通用类型下，由后续的本体对齐/人工审核再细分。端点解析用归一化
	// 名字作批内句柄。
	entities := make([]*types.GraphEntity, 0, len(graph.Node))
	handles := make(map[string]string, len(graph.Node))
	for _, node := range graph.Node {
		if node == nil || strings.TrimSpace(node.Name) == "" {
			continue
		}
		normalized := strings.ToLower(strings.TrimSpace(node.Name))
		if _, seen := handles[normalized]; seen {
			continue
		}
		handles[normalized] = ""
		entity := &types.GraphEntity{
			TenantID:          tenantID,
			EntityType:        graphProjectionEntityType,
			Name:              strings.TrimSpace(node.Name),
			NormalizedKey:     normalized,
			SourceKnowledgeID: chunk.KnowledgeID,
			Confidence:        types.GraphConfidenceExtracted,
			Status:            types.GraphEntityStatusActive,
			CreatedBy:         actorID,
		}
		if len(node.Attributes) > 0 {
			if attributes, marshalErr := json.Marshal(node.Attributes); marshalErr == nil {
				entity.Attributes = types.JSON(attributes)
			}
		}
		entities = append(entities, entity)
	}

	// 先落实体：仓储按归一键匹配既有行并保留原 ID，只有落库之后才能拿到
	// 可引用的实体 ID（新插入的行才有 ID，命中的行是库里的旧 ID）。
	if len(entities) == 0 {
		return nil, nil
	}
	if _, err := p.facts.UpsertEntities(ctx, tenantID, entities, actorID); err != nil {
		logger.Warnf(ctx, "graph projection: upsert entities for chunk %s failed: %v", chunk.ID, err)
		return nil, err
	}
	for _, entity := range entities {
		handles[entity.NormalizedKey] = entity.ID
	}

	facts := make([]*types.GraphFact, 0, len(graph.Relation))
	seen := make(map[string]bool, len(graph.Relation))
	for _, relation := range graph.Relation {
		if relation == nil || strings.TrimSpace(relation.Type) == "" {
			continue
		}
		sourceID, okSource := resolveProjectionEndpoint(handles, entities, relation.Node1)
		targetID, okTarget := resolveProjectionEndpoint(handles, entities, relation.Node2)
		if !okSource || !okTarget || sourceID == "" || targetID == "" {
			// 端点不在本 chunk 的实体集内：跳过而不是凭空建端点。跨 chunk 的
			// 关系需要对方实体先存在，那属于实体归一的职责，不是这里的。
			logger.Warnf(ctx,
				"graph projection: relation %s(%s,%s) skipped, endpoint not extracted in chunk %s",
				relation.Type, relation.Node1, relation.Node2, chunk.ID)
			continue
		}
		key := sourceID + "\x00" + relation.Type + "\x00" + targetID
		if seen[key] {
			continue
		}
		seen[key] = true
		facts = append(facts, &types.GraphFact{
			TenantID:       tenantID,
			RelationName:   strings.TrimSpace(relation.Type),
			SourceEntityID: sourceID,
			TargetEntityID: targetID,
			Confidence:     types.GraphConfidenceExtracted,
			Evidence:       types.JSON(evidence),
			Status:         types.GraphFactStatusActive,
		})
	}
	if len(facts) == 0 {
		// 只有实体没有关系：实体已入库，不需要一个空 commit 占位。
		return nil, nil
	}

	commit, err := p.facts.CreateCommit(ctx, tenantID, &types.GraphCommitInput{
		Description: fmt.Sprintf("chunk extraction: %s", chunk.ID),
		Facts:       facts,
	}, actorID)
	if err != nil {
		// 图谱投影失败不应回滚已完成的解析：正文与向量索引仍有价值，下一次
		// 重解析会重新投影。
		logger.Warnf(ctx, "graph projection failed for chunk %s: %v", chunk.ID, err)
		return nil, err
	}
	return commit, nil
}

// resolveProjectionEndpoint maps an extracted node name back to the entity ID it
// landed under. Names are matched case-insensitively because the model is not
// consistent about casing between the node list and the relation endpoints.
func resolveProjectionEndpoint(
	handles map[string]string, entities []*types.GraphEntity, name string,
) (string, bool) {
	trimmed := strings.ToLower(strings.TrimSpace(name))
	if trimmed == "" {
		return "", false
	}
	if id, ok := handles[trimmed]; ok {
		return id, true
	}
	for _, entity := range entities {
		if entity.NormalizedKey == trimmed {
			return handles[entity.NormalizedKey], true
		}
	}
	return "", false
}

// excerptForEvidence keeps a short, quotable slice of the source so a reviewer
// can tell what the fact was extracted from without opening the document.
func excerptForEvidence(content string) string {
	const maxRunes = 200
	runes := []rune(content)
	if len(runes) <= maxRunes {
		return content
	}
	return string(runes[:maxRunes])
}
