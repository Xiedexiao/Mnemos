package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// 图谱事实层业务哨兵错误。handler 层据此映射 HTTP 语义（400/401/404），
// 与 review 等服务的错误风格一致。
var (
	// ErrGraphTenantMissing 租户上下文缺失（tenantID 为 0）。
	ErrGraphTenantMissing = errors.New("graph fact: tenant context is missing")
	// ErrGraphActorMissing 调用者缺少用户身份。
	ErrGraphActorMissing = errors.New("graph fact: caller identity is missing")
	// ErrGraphEntityNotFound 实体不存在（或不在本租户）。
	ErrGraphEntityNotFound = errors.New("graph fact: entity not found")
	// ErrGraphCommitNotFound 变更集不存在（或不在本租户）。
	ErrGraphCommitNotFound = errors.New("graph fact: commit not found")
	// ErrGraphCacheMiss 增量抽取缓存未命中（无缓存行或 hash/schema_version
	// 不一致——内容改了或本体换版了，需重新抽取）。
	ErrGraphCacheMiss = errors.New("graph fact: extraction cache miss")
	// ErrGraphEmptyBatch 批量为空（entities 与 facts 全空）。
	ErrGraphEmptyBatch = errors.New("graph fact: batch is empty")
	// ErrGraphInvalidQuery 必填查询参数缺失（from/to/entity_id/chunk_id 等）。
	ErrGraphInvalidQuery = errors.New("graph fact: required query parameter is missing")
	// ErrGraphInvalidConfidence confidence 不在三级白名单内。
	ErrGraphInvalidConfidence = errors.New("graph fact: unknown confidence level")
	// ErrGraphInvalidHops 路径跳数超出 1..3（v1 递归 SQL 的有界深度）。
	ErrGraphInvalidHops = errors.New("graph fact: max hops must be between 1 and 3")
	// ErrGraphInvalidEntity 实体行缺 entity_type / name。
	ErrGraphInvalidEntity = errors.New("graph fact: entity_type and name are required")
	// ErrGraphInvalidFact 事实行缺 relation_name / 两端实体。
	ErrGraphInvalidFact = errors.New("graph fact: relation_name, source_entity_id and target_entity_id are required")
	// ErrGraphFactDanglingEndpoint 事实两端实体在本批与库内都不存在
	//（悬空边会让路径/影响查询装配出 nil 实体，写入期拦截）。
	ErrGraphFactDanglingEndpoint = errors.New("graph fact: fact references an unknown entity")
)

const (
	// graphPathMaxHops 是 v1 路径查询的最大跳数（递归 SQL 有界深度）。
	graphPathMaxHops = 3
	// graphPathMaxResults 是单次路径查询返回的路径条数上限。
	graphPathMaxResults = 10
	// graphCommitListDefault / graphCommitListLimit 是 commit 列表的默认与硬上限。
	graphCommitListDefault = 50
	graphCommitListLimit   = 500
)

// graphFactService implements interfaces.GraphFactService（B 期图谱增强
// v1：GraphFact 事实层 + 三级置信度 + 证据绑定 + GraphCommit 版本化 +
// 路径/影响查询）。
//
// 审计取舍：graph_commits 本身就是 append-only 的变更审计锚点（谁、何时、
// 批量统计、golden gate 结果），v1 不再叠加 KB 活动审计——事实层是租户级
// 资产而非单一 KB 的内容，KB 维度的审计待后续版本接 taxonomy。
type graphFactService struct {
	repo interfaces.GraphFactRepository
	now  func() time.Time
}

// NewGraphFactService constructs the production graph fact service.
func NewGraphFactService(repo interfaces.GraphFactRepository) interfaces.GraphFactService {
	return &graphFactService{repo: repo, now: time.Now}
}

// UpsertEntities 批量归一写入实体。归一：normalized_key 命中 → attributes
// 键级合并、置信度就高不就低（AMBIGUOUS 不落有效图，直接带 confidence 存，
// 默认查询过滤 EXTRACTED+INFERRED）；未命中 → 插入。
func (s *graphFactService) UpsertEntities(
	ctx context.Context, tenantID uint64, entities []*types.GraphEntity, actorID string,
) (*types.GraphEntityUpsertResult, error) {
	if tenantID == 0 {
		return nil, ErrGraphTenantMissing
	}
	if actorID == "" {
		return nil, ErrGraphActorMissing
	}
	if len(graphEntitiesNonNil(entities)) == 0 {
		return nil, ErrGraphEmptyBatch
	}
	if err := s.normalizeEntities(tenantID, entities, actorID, ""); err != nil {
		return nil, err
	}
	added, updated, err := s.repo.UpsertEntities(ctx, tenantID, entities)
	if err != nil {
		return nil, err
	}
	return &types.GraphEntityUpsertResult{Added: added, Updated: updated, Entities: entities}, nil
}

// UpsertFacts 批量写入事实。重复陈述合并进既有 active 边（证据去重累积），
// 不产生重复行。这是抽取管线的落库挂接点：extract 管线产出 GraphData 后，
// 由接线方（主会话后续改造 extract.go 时）转成 GraphEntity/GraphFact 批量
// 写入——不直接在 extract.go 内调 LLM 之外的存储原语，保持管线与存储解耦。
func (s *graphFactService) UpsertFacts(
	ctx context.Context, tenantID uint64, facts []*types.GraphFact, actorID string,
) (*types.GraphFactUpsertResult, error) {
	if tenantID == 0 {
		return nil, ErrGraphTenantMissing
	}
	if actorID == "" {
		return nil, ErrGraphActorMissing
	}
	if len(graphFactsNonNil(facts)) == 0 {
		return nil, ErrGraphEmptyBatch
	}
	if err := s.normalizeFacts(tenantID, facts, ""); err != nil {
		return nil, err
	}
	if err := s.validateFactEndpoints(ctx, tenantID, nil, facts); err != nil {
		return nil, err
	}
	added, err := s.repo.UpsertFacts(ctx, tenantID, facts)
	if err != nil {
		return nil, err
	}
	return &types.GraphFactUpsertResult{Added: added, Facts: facts}, nil
}

// CreateCommit 提交一批变更（GraphCommit 版本化 v1）：
//  1. 归一化批次实体/事实；
//  2. 事实端点完整性校验（本批实体 + 库内既有实体）；
//  3. 生成 commit id 并回填本批 entity/fact 行的 commit_id（行归属 =
//     最后一次产出/更新它的 commit，回滚按它反向标记）；
//  4. 单事务内 upsert + 统计计数落 graph_commits（append-only）。
func (s *graphFactService) CreateCommit(
	ctx context.Context, tenantID uint64, input *types.GraphCommitInput, actorID string,
) (*types.GraphCommit, error) {
	if tenantID == 0 {
		return nil, ErrGraphTenantMissing
	}
	if actorID == "" {
		return nil, ErrGraphActorMissing
	}
	if input == nil || len(graphEntitiesNonNil(input.Entities))+len(graphFactsNonNil(input.Facts)) == 0 {
		return nil, ErrGraphEmptyBatch
	}
	if err := s.normalizeEntities(tenantID, input.Entities, actorID, ""); err != nil {
		return nil, err
	}
	if err := s.normalizeFacts(tenantID, input.Facts, ""); err != nil {
		return nil, err
	}
	if err := s.validateFactEndpoints(ctx, tenantID, input.Entities, input.Facts); err != nil {
		return nil, err
	}

	commit := &types.GraphCommit{
		ID:          uuid.NewString(),
		TenantID:    tenantID,
		Description: input.Description,
		// golden gate 缺省通过：v1 断言框架未接入，调用方显式声明失败才落 false。
		GoldenGatePass: input.GoldenGatePass == nil || *input.GoldenGatePass,
		CreatedBy:      actorID,
	}
	// 回填：本批行的 commit_id 指向本 commit（先赋值，事务内与计数同批落库）。
	for _, entity := range input.Entities {
		if entity != nil {
			entity.CommitID = commit.ID
		}
	}
	for _, fact := range input.Facts {
		if fact != nil {
			fact.CommitID = commit.ID
		}
	}
	return s.repo.ApplyCommit(ctx, commit, input.Entities, input.Facts)
}

// RollbackCommit 回滚一个 commit：把其产出的 active 实体/事实置 superseded
// （v1 反向标记而非删除；commit 行不可变，历史可回放）。幂等：重复回滚
// 返回 0 计数。
func (s *graphFactService) RollbackCommit(
	ctx context.Context, tenantID uint64, commitID, actorID string,
) (*types.GraphRollbackResult, error) {
	if tenantID == 0 {
		return nil, ErrGraphTenantMissing
	}
	if actorID == "" {
		return nil, ErrGraphActorMissing
	}
	if commitID == "" {
		return nil, ErrGraphCommitNotFound
	}
	if _, err := s.repo.GetCommit(ctx, tenantID, commitID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrGraphCommitNotFound
		}
		return nil, err
	}
	entitiesSuperseded, factsSuperseded, err := s.repo.RollbackCommit(ctx, tenantID, commitID)
	if err != nil {
		return nil, err
	}
	return &types.GraphRollbackResult{
		CommitID:           commitID,
		EntitiesSuperseded: entitiesSuperseded,
		FactsSuperseded:    factsSuperseded,
	}, nil
}

// ListCommits 按创建时间倒序返回 commit（append-only 审计面）。
func (s *graphFactService) ListCommits(
	ctx context.Context, tenantID uint64, limit int,
) ([]*types.GraphCommit, error) {
	if tenantID == 0 {
		return nil, ErrGraphTenantMissing
	}
	if limit <= 0 {
		limit = graphCommitListDefault
	}
	if limit > graphCommitListLimit {
		limit = graphCommitListLimit
	}
	return s.repo.ListCommits(ctx, tenantID, limit)
}

// SearchEntities 实体检索：name/normalized_key 模糊。confidenceFilter 为空
// 时默认 EXTRACTED+INFERRED（AMBIGUOUS 待人工审核，默认不可见；显式传入
// [AMBIGUOUS] 即审核队列视图）。superseded（回滚废止）不进检索面。
func (s *graphFactService) SearchEntities(
	ctx context.Context, tenantID uint64, keyword, schemaID string, confidenceFilter []types.GraphConfidence,
) ([]*types.GraphEntity, error) {
	if tenantID == 0 {
		return nil, ErrGraphTenantMissing
	}
	for _, confidence := range confidenceFilter {
		if !confidence.Valid() {
			return nil, ErrGraphInvalidConfidence
		}
	}
	if len(confidenceFilter) == 0 {
		confidenceFilter = types.GraphConfidenceDefaultFilter
	}
	return s.repo.SearchEntities(ctx, tenantID, keyword, schemaID, confidenceFilter)
}

// PathQuery 路径查询 v1：递归 SQL（Postgres WITH RECURSIVE，非 Postgres
// 方言走等价 BFS 回退）走 graph_facts 的 active 边，maxHops 钳制 1..3
// （0/缺省=3），只返回 EXTRACTED+INFERRED 的边。路径逐步装配实体 + 关系 +
// 置信度，调用方可按需降权展示 INFERRED 边。
func (s *graphFactService) PathQuery(
	ctx context.Context, tenantID uint64, fromEntityID, toEntityID string, maxHops int,
) ([]*types.GraphFactPath, error) {
	if tenantID == 0 {
		return nil, ErrGraphTenantMissing
	}
	if strings.TrimSpace(fromEntityID) == "" || strings.TrimSpace(toEntityID) == "" {
		return nil, ErrGraphInvalidQuery
	}
	if maxHops == 0 {
		maxHops = graphPathMaxHops
	}
	if maxHops < 1 || maxHops > graphPathMaxHops {
		return nil, ErrGraphInvalidHops
	}

	// 端点存在性：任一端不存在直接 404（顺带取回实体供装配）。
	endpoints, err := s.repo.GetEntitiesByIDs(ctx, tenantID, []string{fromEntityID, toEntityID})
	if err != nil {
		return nil, err
	}
	if len(endpoints) < 2 {
		return nil, ErrGraphEntityNotFound
	}

	factIDPaths, err := s.repo.FindFactIDPaths(ctx, tenantID, fromEntityID, toEntityID, maxHops, graphPathMaxResults)
	if err != nil {
		return nil, err
	}
	if len(factIDPaths) == 0 {
		return []*types.GraphFactPath{}, nil
	}

	// 汇总路径涉及的实体（含事实两端）批量装载。
	entityIDSet := make(map[string]struct{})
	factIDSet := make(map[string]struct{})
	for _, chain := range factIDPaths {
		for _, factID := range chain {
			factIDSet[factID] = struct{}{}
		}
	}
	factIDs := make([]string, 0, len(factIDSet))
	for factID := range factIDSet {
		factIDs = append(factIDs, factID)
	}
	facts, err := s.repo.GetFactsByIDs(ctx, tenantID, factIDs)
	if err != nil {
		return nil, err
	}
	factByID := make(map[string]*types.GraphFact, len(facts))
	for _, fact := range facts {
		factByID[fact.ID] = fact
		entityIDSet[fact.SourceEntityID] = struct{}{}
		entityIDSet[fact.TargetEntityID] = struct{}{}
	}
	entityIDs := make([]string, 0, len(entityIDSet))
	for entityID := range entityIDSet {
		entityIDs = append(entityIDs, entityID)
	}
	entities, err := s.repo.GetEntitiesByIDs(ctx, tenantID, entityIDs)
	if err != nil {
		return nil, err
	}
	entityByID := make(map[string]*types.GraphEntity, len(entities))
	for _, entity := range entities {
		entityByID[entity.ID] = entity
	}

	paths := make([]*types.GraphFactPath, 0, len(factIDPaths))
	for _, chain := range factIDPaths {
		steps := make([]*types.GraphPathStep, 0, len(chain))
		for _, factID := range chain {
			fact := factByID[factID]
			if fact == nil {
				continue // 装载间隙被删除的边跳过，不让整条路径 500
			}
			steps = append(steps, &types.GraphPathStep{
				Fact:   fact,
				Source: entityByID[fact.SourceEntityID],
				Target: entityByID[fact.TargetEntityID],
			})
		}
		if len(steps) == 0 {
			continue
		}
		paths = append(paths, &types.GraphFactPath{Hops: len(steps), Steps: steps})
	}
	return paths, nil
}

// ImpactQuery 影响面查询：从实体出发沿 outgoing active 边（EXTRACTED+
// INFERRED）BFS 两层，返回受影响 facts 与被波及实体。v1 固定两层
// （"改了 A，哪些事实/实体受牵连"的一跳邻域 + 二跳扩散）。
func (s *graphFactService) ImpactQuery(
	ctx context.Context, tenantID uint64, entityID string,
) (*types.GraphImpactResult, error) {
	if tenantID == 0 {
		return nil, ErrGraphTenantMissing
	}
	if strings.TrimSpace(entityID) == "" {
		return nil, ErrGraphInvalidQuery
	}
	root, err := s.repo.GetEntityByID(ctx, tenantID, entityID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrGraphEntityNotFound
		}
		return nil, err
	}

	result := &types.GraphImpactResult{Root: root, Facts: []*types.GraphFact{}, AffectedEntities: []*types.GraphEntity{}}
	factSeen := make(map[string]struct{})
	affectedSet := make(map[string]struct{})

	layer1, err := s.repo.ListActiveFactsFromSources(ctx, tenantID, []string{entityID})
	if err != nil {
		return nil, err
	}
	targets := make([]string, 0, len(layer1))
	for _, fact := range layer1 {
		if _, ok := factSeen[fact.ID]; ok {
			continue
		}
		factSeen[fact.ID] = struct{}{}
		result.Facts = append(result.Facts, fact)
		if fact.TargetEntityID != entityID {
			targets = append(targets, fact.TargetEntityID)
			affectedSet[fact.TargetEntityID] = struct{}{}
		}
	}
	if len(layer1) > 0 {
		result.Depth = 1
	}

	// 二跳扩散：一跳目标实体的出边。factSeen 去重防自环/回边重复计数。
	if len(targets) > 0 {
		before := len(result.Facts)
		layer2, err := s.repo.ListActiveFactsFromSources(ctx, tenantID, targets)
		if err != nil {
			return nil, err
		}
		for _, fact := range layer2 {
			if _, ok := factSeen[fact.ID]; ok {
				continue
			}
			factSeen[fact.ID] = struct{}{}
			result.Facts = append(result.Facts, fact)
			if fact.TargetEntityID != entityID {
				affectedSet[fact.TargetEntityID] = struct{}{}
			}
		}
		if len(result.Facts) > before {
			result.Depth = 2
		}
	}

	affectedIDs := make([]string, 0, len(affectedSet))
	for id := range affectedSet {
		affectedIDs = append(affectedIDs, id)
	}
	if len(affectedIDs) > 0 {
		affected, err := s.repo.GetEntitiesByIDs(ctx, tenantID, affectedIDs)
		if err != nil {
			return nil, err
		}
		result.AffectedEntities = affected
	}
	return result, nil
}

// ============================================================================
// 增量抽取缓存（§9.4：内容没改不重复提取）
// ============================================================================

// GetExtractionCache 命中判断：chunk 缓存行存在且 content_hash 与
// schema_version 完全相同才返回缓存；未命中报 ErrGraphCacheMiss。
func (s *graphFactService) GetExtractionCache(
	ctx context.Context, tenantID uint64, chunkID, contentHash string, schemaVersion int,
) (*types.ChunkExtractionCache, error) {
	if tenantID == 0 {
		return nil, ErrGraphTenantMissing
	}
	if strings.TrimSpace(chunkID) == "" || contentHash == "" {
		return nil, ErrGraphInvalidQuery
	}
	cache, err := s.repo.GetExtractionCache(ctx, tenantID, chunkID, contentHash, schemaVersion)
	if err != nil {
		if errors.Is(err, repository.ErrExtractionCacheMiss) {
			return nil, ErrGraphCacheMiss
		}
		return nil, err
	}
	return cache, nil
}

// LetExtractionCache 写入/覆盖缓存（抽取完成后调用；schemaVersion ≤0 按 1）。
func (s *graphFactService) LetExtractionCache(
	ctx context.Context, tenantID uint64, chunkID, contentHash string, schemaVersion int, extraction types.JSON,
) (*types.ChunkExtractionCache, error) {
	if tenantID == 0 {
		return nil, ErrGraphTenantMissing
	}
	if strings.TrimSpace(chunkID) == "" || strings.TrimSpace(contentHash) == "" {
		return nil, ErrGraphInvalidQuery
	}
	if schemaVersion < 1 {
		schemaVersion = 1
	}
	cache := &types.ChunkExtractionCache{
		TenantID:      tenantID,
		ChunkID:       chunkID,
		ContentHash:   contentHash,
		SchemaVersion: schemaVersion,
		Extraction:    extraction,
	}
	if err := s.repo.LetExtractionCache(ctx, cache); err != nil {
		return nil, err
	}
	return cache, nil
}

// PeekExtractionCache 按主键读缓存行（管理面查看，不做命中判断）。
func (s *graphFactService) PeekExtractionCache(
	ctx context.Context, tenantID uint64, chunkID string,
) (*types.ChunkExtractionCache, error) {
	if tenantID == 0 {
		return nil, ErrGraphTenantMissing
	}
	if strings.TrimSpace(chunkID) == "" {
		return nil, ErrGraphInvalidQuery
	}
	cache, err := s.repo.PeekExtractionCache(ctx, tenantID, chunkID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrGraphCacheMiss
		}
		return nil, err
	}
	return cache, nil
}

// ============================================================================
// 内部助手
// ============================================================================

// normalizeEntities 补齐实体行默认值（ID/归一键/置信度/归属），并做请求
// 体白名单校验。commitID 非空时回填行归属。
func (s *graphFactService) normalizeEntities(tenantID uint64, entities []*types.GraphEntity, actorID, commitID string) error {
	for _, entity := range entities {
		if entity == nil {
			continue
		}
		entity.EntityType = strings.TrimSpace(entity.EntityType)
		entity.Name = strings.TrimSpace(entity.Name)
		if entity.EntityType == "" || entity.Name == "" {
			return ErrGraphInvalidEntity
		}
		if entity.NormalizedKey == "" {
			entity.NormalizedKey = strings.ToLower(entity.Name)
		}
		entity.NormalizedKey = strings.TrimSpace(entity.NormalizedKey)
		switch {
		case entity.Confidence == "":
			entity.Confidence = types.GraphConfidenceExtracted
		case !entity.Confidence.Valid():
			return ErrGraphInvalidConfidence
		}
		entity.TenantID = tenantID
		if entity.ID == "" {
			entity.ID = uuid.NewString()
		}
		if entity.CreatedBy == "" {
			entity.CreatedBy = actorID
		}
		if commitID != "" {
			entity.CommitID = commitID
		}
	}
	return nil
}

// normalizeFacts 补齐事实行默认值并校验必填字段（事实行无 created_by 列，
// 归属人走 commit.created_by）。
func (s *graphFactService) normalizeFacts(tenantID uint64, facts []*types.GraphFact, commitID string) error {
	for _, fact := range facts {
		if fact == nil {
			continue
		}
		fact.RelationName = strings.TrimSpace(fact.RelationName)
		if fact.RelationName == "" || fact.SourceEntityID == "" || fact.TargetEntityID == "" {
			return ErrGraphInvalidFact
		}
		switch {
		case fact.Confidence == "":
			fact.Confidence = types.GraphConfidenceExtracted
		case !fact.Confidence.Valid():
			return ErrGraphInvalidConfidence
		}
		fact.TenantID = tenantID
		// 证据绑定是硬约束（§9.4/§8.5）：没有原文位置或业务记录的实体/关系
		// 不得进入正式图谱。此前 Evidence 可空，等于让"无中生有"的抽取直接
		// 落到查询面；这里把无证据的事实降级为 AMBIGUOUS —— 它仍可被人工审核
		// 看到，但不会自动参与检索（AMBIGUOUS 默认排除在查询之外）。
		if !factHasEvidence(fact) {
			if fact.Confidence == types.GraphConfidenceExtracted {
				fact.Confidence = types.GraphConfidenceAmbiguous
			}
		}
		if fact.ID == "" {
			fact.ID = uuid.NewString()
		}
		if commitID != "" {
			fact.CommitID = commitID
		}
	}
	return nil
}

// factHasEvidence reports whether the fact carries at least one usable
// evidence binding: an identifiable source chunk or document. A locator/quote
// alone is not enough — without a document the claim cannot be traced back, so
// it cannot be verified or withdrawn (§2.4 证据可追溯).
func factHasEvidence(fact *types.GraphFact) bool {
	if fact == nil || len(fact.Evidence) == 0 {
		return false
	}
	var evidence []types.GraphFactEvidence
	if err := json.Unmarshal(fact.Evidence, &evidence); err != nil {
		return false
	}
	for _, item := range evidence {
		if item.KnowledgeID != "" || item.ChunkID != "" {
			return true
		}
	}
	return false
}

// validateFactEndpoints 校验事实两端实体存在：本批实体优先，其余查库。
// 悬空边会让路径/影响查询装配出 nil 实体，写入期拦截（遗留事实可引用
// 任意置信度的实体行，这里只验存在性，不验置信度）。
func (s *graphFactService) validateFactEndpoints(
	ctx context.Context, tenantID uint64, entities []*types.GraphEntity, facts []*types.GraphFact,
) error {
	known := make(map[string]struct{})
	for _, entity := range entities {
		if entity != nil {
			known[entity.ID] = struct{}{}
		}
	}
	var missing []string
	for _, fact := range facts {
		if fact == nil {
			continue
		}
		for _, endpoint := range []string{fact.SourceEntityID, fact.TargetEntityID} {
			if _, ok := known[endpoint]; ok {
				continue
			}
			known[endpoint] = struct{}{}
			missing = append(missing, endpoint)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	found, err := s.repo.GetEntitiesByIDs(ctx, tenantID, missing)
	if err != nil {
		return err
	}
	foundSet := make(map[string]struct{}, len(found))
	for _, entity := range found {
		if entity != nil {
			foundSet[entity.ID] = struct{}{}
		}
	}
	for _, id := range missing {
		if _, ok := foundSet[id]; !ok {
			return ErrGraphFactDanglingEndpoint
		}
	}
	return nil
}

// graphEntitiesNonNil / graphFactsNonNil 过滤 nil 行，供空批判断。
func graphEntitiesNonNil(entities []*types.GraphEntity) []*types.GraphEntity {
	out := make([]*types.GraphEntity, 0, len(entities))
	for _, entity := range entities {
		if entity != nil {
			out = append(out, entity)
		}
	}
	return out
}

func graphFactsNonNil(facts []*types.GraphFact) []*types.GraphFact {
	out := make([]*types.GraphFact, 0, len(facts))
	for _, fact := range facts {
		if fact != nil {
			out = append(out, fact)
		}
	}
	return out
}
