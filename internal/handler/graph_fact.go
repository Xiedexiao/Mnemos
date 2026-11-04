package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/service"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// GraphFactHandler 暴露图谱事实层（B 期图谱增强 v1）：实体批量归一写入、
// GraphCommit 版本化提交与回滚、实体检索、路径/影响查询、增量抽取缓存。
// 租户/用户身份一律从 types.CallerFromContext 取（路由层的 RBAC 守卫保证
// 其可信）；服务层哨兵错误经 writeGraphFactError 映射 HTTP 语义。
type GraphFactHandler struct {
	graphFactService interfaces.GraphFactService
}

// NewGraphFactHandler constructs the graph fact handler.
func NewGraphFactHandler(graphFactService interfaces.GraphFactService) *GraphFactHandler {
	return &GraphFactHandler{graphFactService: graphFactService}
}

// graphEntityRequest 是实体行的请求体。confidence 缺省 EXTRACTED；
// normalized_key 缺省由 name 小写归一（服务层补齐）。
type graphEntityRequest struct {
	SchemaID          string     `json:"schema_id"`
	EntityType        string     `json:"entity_type" binding:"required"`
	Name              string     `json:"name" binding:"required"`
	NormalizedKey     string     `json:"normalized_key"`
	Attributes        types.JSON `json:"attributes"`
	SourceKnowledgeID string     `json:"source_knowledge_id"`
	Confidence        string     `json:"confidence"`
}

// toGraphEntity 转成领域行（不含租户/ID——服务层归一化时补齐）。
func (req graphEntityRequest) toGraphEntity() *types.GraphEntity {
	return &types.GraphEntity{
		SchemaID:          req.SchemaID,
		EntityType:        req.EntityType,
		Name:              req.Name,
		NormalizedKey:     req.NormalizedKey,
		Attributes:        req.Attributes,
		SourceKnowledgeID: req.SourceKnowledgeID,
		Confidence:        types.GraphConfidence(req.Confidence),
	}
}

// graphFactRequest 是事实行的请求体。evidence 为结构化证据数组
// （[{knowledge_id, chunk_id, locator, quote}]），json.Marshal 后落 JSONB。
type graphFactRequest struct {
	SchemaID       string                    `json:"schema_id"`
	RelationName   string                    `json:"relation_name" binding:"required"`
	SourceEntityID string                    `json:"source_entity_id" binding:"required"`
	TargetEntityID string                    `json:"target_entity_id" binding:"required"`
	Confidence     string                    `json:"confidence"`
	Evidence       []types.GraphFactEvidence `json:"evidence"`
}

// toGraphFact 转成领域行；证据序列化失败返回错误（400）。
func (req graphFactRequest) toGraphFact() (*types.GraphFact, error) {
	fact := &types.GraphFact{
		SchemaID:       req.SchemaID,
		RelationName:   req.RelationName,
		SourceEntityID: req.SourceEntityID,
		TargetEntityID: req.TargetEntityID,
		Confidence:     types.GraphConfidence(req.Confidence),
	}
	if len(req.Evidence) > 0 {
		raw, err := json.Marshal(req.Evidence)
		if err != nil {
			return nil, err
		}
		fact.Evidence = types.JSON(raw)
	}
	return fact, nil
}

// graphEntityBatchRequest 是 POST /graph/facts/entities 的请求体。
type graphEntityBatchRequest struct {
	Entities []graphEntityRequest `json:"entities" binding:"required,dive"`
}

// graphCommitRequest 是 POST /graph/facts/commit 的请求体：一批实体/事实
// 原子提交为不可变 GraphCommit（统计计数落库，行回填 commit_id）。
type graphCommitRequest struct {
	Description    string               `json:"description"`
	Entities       []graphEntityRequest `json:"entities"`
	Facts          []graphFactRequest   `json:"facts"`
	GoldenGatePass *bool                `json:"golden_gate_pass"`
}

// graphExtractionCachePutRequest 是 PUT /graph/extraction-cache/:chunk_id
// 的请求体：content_hash + schema_version 组成缓存 key。
type graphExtractionCachePutRequest struct {
	ContentHash   string     `json:"content_hash" binding:"required"`
	SchemaVersion int        `json:"schema_version"`
	Extraction    types.JSON `json:"extraction"`
}

// UpsertEntities godoc
// @Summary      批量归一写入图谱实体
// @Description  按 (entity_type, normalized_key) 归一 upsert：命中则合并 attributes、置信度就高不就低；AMBIGUOUS 直接带置信度入库但默认查询不可见。
// @Tags         图谱事实层
// @Accept       json
// @Produce      json
// @Param        request  body  graphEntityBatchRequest  true  "{entities: [...]}"
// @Success      200  {object}  map[string]interface{}  "{success, data: {added, updated, entities}}"
// @Failure      400  {object}  apperrors.AppError  "批次为空/字段非法/置信度非法"
// @Failure      401  {object}  apperrors.AppError  "缺少租户上下文"
// @Security     Bearer
// @Router       /graph/facts/entities [post]
func (h *GraphFactHandler) UpsertEntities(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}
	var req graphEntityBatchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewValidationError("invalid request body: " + err.Error()))
		return
	}
	entities := make([]*types.GraphEntity, 0, len(req.Entities))
	for _, item := range req.Entities {
		entities = append(entities, item.toGraphEntity())
	}

	result, err := h.graphFactService.UpsertEntities(ctx, caller.TenantID, entities, caller.UserID)
	if err != nil {
		writeGraphFactError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

// CreateCommit godoc
// @Summary      提交图谱变更集（GraphCommit）
// @Description  一批实体/事实原子提交为不可变 commit：单事务 upsert、统计计数落 graph_commits、批次行回填 commit_id；事实端点必须在本批或库内存在。
// @Tags         图谱事实层
// @Accept       json
// @Produce      json
// @Param        request  body  graphCommitRequest  true  "{description, entities: [...], facts: [...], golden_gate_pass?}"
// @Success      200  {object}  map[string]interface{}  "{success, data: commit}"
// @Failure      400  {object}  apperrors.AppError  "批次为空/字段非法/事实端点悬空"
// @Failure      401  {object}  apperrors.AppError  "缺少租户上下文"
// @Security     Bearer
// @Router       /graph/facts/commit [post]
func (h *GraphFactHandler) CreateCommit(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}
	var req graphCommitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewValidationError("invalid request body: " + err.Error()))
		return
	}
	input := &types.GraphCommitInput{
		Description:    req.Description,
		Entities:       make([]*types.GraphEntity, 0, len(req.Entities)),
		Facts:          make([]*types.GraphFact, 0, len(req.Facts)),
		GoldenGatePass: req.GoldenGatePass,
	}
	for _, item := range req.Entities {
		input.Entities = append(input.Entities, item.toGraphEntity())
	}
	for _, item := range req.Facts {
		fact, err := item.toGraphFact()
		if err != nil {
			c.Error(apperrors.NewValidationError("invalid evidence: " + err.Error()))
			return
		}
		input.Facts = append(input.Facts, fact)
	}

	commit, err := h.graphFactService.CreateCommit(ctx, caller.TenantID, input, caller.UserID)
	if err != nil {
		writeGraphFactError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": commit})
}

// RollbackCommit godoc
// @Summary      回滚图谱变更集
// @Description  把该 commit 产出的 active 实体/事实置 superseded（反向标记而非删除，v1）；commit 行不可变，幂等。
// @Tags         图谱事实层
// @Produce      json
// @Param        id  path  string  true  "commit ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: {commit_id, entities_superseded, facts_superseded}}"
// @Failure      401  {object}  apperrors.AppError  "缺少租户上下文"
// @Failure      404  {object}  apperrors.AppError  "commit 不存在"
// @Security     Bearer
// @Router       /graph/commits/{id}/rollback [post]
func (h *GraphFactHandler) RollbackCommit(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}
	commitID := c.Param("id")
	if commitID == "" {
		c.Error(apperrors.NewBadRequestError("commit id is required"))
		return
	}

	result, err := h.graphFactService.RollbackCommit(ctx, caller.TenantID, commitID, caller.UserID)
	if err != nil {
		writeGraphFactError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

// SearchEntities godoc
// @Summary      图谱实体检索
// @Description  按 name/normalized_key 模糊匹配（只返回 active 行）。confidence 传逗号分隔白名单（默认 EXTRACTED,INFERRED；传 AMBIGUOUS 即人工审核队列视图）。
// @Tags         图谱事实层
// @Produce      json
// @Param        keyword     query  string  false  "关键词（模糊匹配 name/normalized_key）"
// @Param        schema_id   query  string  false  "限定本体 Schema（空=全部）"
// @Param        confidence  query  string  false  "逗号分隔置信度白名单"  default(EXTRACTED,INFERRED)
// @Success      200  {object}  map[string]interface{}  "{success, data: [实体]}"
// @Failure      400  {object}  apperrors.AppError  "置信度非法"
// @Failure      401  {object}  apperrors.AppError  "缺少租户上下文"
// @Security     Bearer
// @Router       /graph/entities [get]
func (h *GraphFactHandler) SearchEntities(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}
	confidenceFilter, err := parseGraphConfidenceFilter(c.Query("confidence"))
	if err != nil {
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}

	entities, err := h.graphFactService.SearchEntities(
		ctx, caller.TenantID, c.Query("keyword"), c.Query("schema_id"), confidenceFilter,
	)
	if err != nil {
		writeGraphFactError(c, ctx, err)
		return
	}
	if entities == nil {
		entities = []*types.GraphEntity{}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": entities})
}

// PathQuery godoc
// @Summary      图谱路径查询
// @Description  递归 SQL 走 graph_facts 的 active 边（只走 EXTRACTED/INFERRED），返回 from→to 路径（实体+关系+置信度）。hops 钳制 1..3，缺省 3。
// @Tags         图谱事实层
// @Produce      json
// @Param        from  query  string  true  "起点实体ID"
// @Param        to    query  string  true  "终点实体ID"
// @Param        hops  query  int     false  "最大跳数 1-3"  default(3)
// @Success      200  {object}  map[string]interface{}  "{success, data: [{hops, steps:[{fact, source, target}]}]}"
// @Failure      400  {object}  apperrors.AppError  "缺参/跳数非法"
// @Failure      401  {object}  apperrors.AppError  "缺少租户上下文"
// @Failure      404  {object}  apperrors.AppError  "端点实体不存在"
// @Security     Bearer
// @Router       /graph/path [get]
func (h *GraphFactHandler) PathQuery(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}
	maxHops := 0
	if raw := c.Query("hops"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			c.Error(apperrors.NewBadRequestError("hops must be an integer between 1 and 3"))
			return
		}
		maxHops = parsed
	}

	paths, err := h.graphFactService.PathQuery(ctx, caller.TenantID, c.Query("from"), c.Query("to"), maxHops)
	if err != nil {
		writeGraphFactError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": paths})
}

// ImpactQuery godoc
// @Summary      图谱影响面查询
// @Description  从实体出发沿 outgoing active 边（EXTRACTED/INFERRED）BFS 两层，返回受影响 facts 与被波及实体。
// @Tags         图谱事实层
// @Produce      json
// @Param        entity_id  query  string  true  "根实体ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: {root, facts, affected_entities, depth}}"
// @Failure      400  {object}  apperrors.AppError  "缺 entity_id"
// @Failure      401  {object}  apperrors.AppError  "缺少租户上下文"
// @Failure      404  {object}  apperrors.AppError  "实体不存在"
// @Security     Bearer
// @Router       /graph/impact [get]
func (h *GraphFactHandler) ImpactQuery(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}

	result, err := h.graphFactService.ImpactQuery(ctx, caller.TenantID, c.Query("entity_id"))
	if err != nil {
		writeGraphFactError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

// GetExtractionCache godoc
// @Summary      查看增量抽取缓存
// @Description  按 chunk_id 读取缓存行。带 content_hash+schema_version 查询参数时做命中判断（不一致返回 404 miss）；不带则原样返回缓存行（管理面查看）。
// @Tags         图谱事实层
// @Produce      json
// @Param        chunk_id        path   string  true  "chunk ID"
// @Param        content_hash    query  string  false "命中判断用内容指纹"
// @Param        schema_version  query  int     false "命中判断用本体版本"
// @Success      200  {object}  map[string]interface{}  "{success, data: 缓存行}"
// @Failure      400  {object}  apperrors.AppError  "缺参"
// @Failure      401  {object}  apperrors.AppError  "缺少租户上下文"
// @Failure      404  {object}  apperrors.AppError  "缓存未命中"
// @Security     Bearer
// @Router       /graph/extraction-cache/{chunk_id} [get]
func (h *GraphFactHandler) GetExtractionCache(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}
	chunkID := c.Param("chunk_id")
	if chunkID == "" {
		c.Error(apperrors.NewBadRequestError("chunk id is required"))
		return
	}

	// 带 content_hash 判命中（服务内部用），否则按主键查看（管理面）。
	contentHash := c.Query("content_hash")
	schemaVersion := 0
	if raw := c.Query("schema_version"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			schemaVersion = parsed
		}
	}
	var (
		cache *types.ChunkExtractionCache
		err   error
	)
	if contentHash != "" {
		cache, err = h.graphFactService.GetExtractionCache(ctx, caller.TenantID, chunkID, contentHash, schemaVersion)
	} else {
		cache, err = h.graphFactService.PeekExtractionCache(ctx, caller.TenantID, chunkID)
	}
	if err != nil {
		writeGraphFactError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": cache})
}

// LetExtractionCache godoc
// @Summary      写入增量抽取缓存
// @Description  写入/覆盖 chunk 的抽取缓存（UNIQUE(chunk_id)，重复写入即覆盖）。缓存 key = content_hash + schema_version，命中判断见 GET。
// @Tags         图谱事实层
// @Accept       json
// @Produce      json
// @Param        chunk_id  path  string                            true  "chunk ID"
// @Param        request   body  graphExtractionCachePutRequest    true  "{content_hash, schema_version, extraction}"
// @Success      200  {object}  map[string]interface{}  "{success, data: 缓存行}"
// @Failure      400  {object}  apperrors.AppError  "缺 content_hash"
// @Failure      401  {object}  apperrors.AppError  "缺少租户上下文"
// @Security     Bearer
// @Router       /graph/extraction-cache/{chunk_id} [put]
func (h *GraphFactHandler) LetExtractionCache(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}
	chunkID := c.Param("chunk_id")
	if chunkID == "" {
		c.Error(apperrors.NewBadRequestError("chunk id is required"))
		return
	}
	var req graphExtractionCachePutRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewValidationError("invalid request body: " + err.Error()))
		return
	}

	cache, err := h.graphFactService.LetExtractionCache(
		ctx, caller.TenantID, chunkID, req.ContentHash, req.SchemaVersion, req.Extraction,
	)
	if err != nil {
		writeGraphFactError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": cache})
}

// parseGraphConfidenceFilter 解析逗号分隔的置信度白名单；空串返回 nil
// （服务层默认 EXTRACTED+INFERRED），非法值报 400。
func parseGraphConfidenceFilter(raw string) ([]types.GraphConfidence, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	filter := make([]types.GraphConfidence, 0, len(parts))
	for _, part := range parts {
		confidence := types.GraphConfidence(strings.ToUpper(strings.TrimSpace(part)))
		if !confidence.Valid() {
			return nil, errors.New("unknown confidence level: " + part)
		}
		filter = append(filter, confidence)
	}
	return filter, nil
}

// writeGraphFactError 把图谱事实层的业务哨兵错误映射为 HTTP 语义：
// 参数/校验 → 400，缺租户/身份 → 401，资源缺失（含缓存 miss）→ 404，
// 其余记日志后 500（不外泄内部细节）。
func writeGraphFactError(c *gin.Context, ctx context.Context, err error) {
	switch {
	case errors.Is(err, service.ErrGraphInvalidConfidence),
		errors.Is(err, service.ErrGraphInvalidHops),
		errors.Is(err, service.ErrGraphInvalidQuery),
		errors.Is(err, service.ErrGraphInvalidEntity),
		errors.Is(err, service.ErrGraphInvalidFact),
		errors.Is(err, service.ErrGraphFactDanglingEndpoint),
		errors.Is(err, service.ErrGraphEmptyBatch):
		c.Error(apperrors.NewBadRequestError(err.Error()))
	case errors.Is(err, service.ErrGraphTenantMissing),
		errors.Is(err, service.ErrGraphActorMissing):
		c.Error(apperrors.NewUnauthorizedError(err.Error()))
	case errors.Is(err, service.ErrGraphEntityNotFound),
		errors.Is(err, service.ErrGraphCommitNotFound),
		errors.Is(err, service.ErrGraphCacheMiss):
		c.Error(apperrors.NewNotFoundError(err.Error()))
	default:
		logger.ErrorWithFields(ctx, err, map[string]interface{}{"handler": "graph_fact"})
		c.Error(apperrors.NewInternalServerError("graph fact operation failed").WithDetails(err.Error()))
	}
}

// DiffCommit godoc
// @Summary      图谱变更集内容
// @Description  返回一个 GraphCommit 实际改动的实体与事实（KnowAxis V3.0 §11.4 可回归）。计数不是内容——"加了 12 条实体"回答不了"哪 12 条、其中几条覆盖了已有行"。输出按 ID 稳定排序，同一 commit 两次请求得到同样结果。
// @Tags         图谱
// @Produce      json
// @Param        id  path  string  true  "变更集ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: GraphCommitDiff}"
// @Failure      404  {object}  apperrors.AppError
// @Security     Bearer
// @Router       /graph/commits/{id} [get]
func (h *GraphFactHandler) DiffCommit(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	diff, err := h.graphFactService.DiffCommit(ctx, tenantID, c.Param("id"))
	if err != nil {
		writeGraphFactError(c, ctx, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": diff})
}

// ReplayGraph godoc
// @Summary      图谱时间点回放
// @Description  重建指定时刻的图谱快照（KnowAxis V3.0 §11.4 可重放）。只读：不修改任何数据。每行同时带**当前**状态，让读者能区分"当时没有它"与"当时有、后来被撤回"。as_of 为 RFC3339，缺省或非法返回 400。
// @Tags         图谱
// @Produce      json
// @Param        as_of      query  string  true   "回放时刻（RFC3339）"
// @Param        schema_id  query  string  false  "限定本体 schema"
// @Param        limit      query  int     false  "实体/事实上限（默认 2000）"
// @Success      200  {object}  map[string]interface{}  "{success, data: GraphReplayResult}"
// @Failure      400  {object}  apperrors.AppError
// @Security     Bearer
// @Router       /graph/replay [get]
func (h *GraphFactHandler) ReplayGraph(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	raw := strings.TrimSpace(c.Query("as_of"))
	if raw == "" {
		c.Error(apperrors.NewBadRequestError("as_of is required (RFC3339)"))
		return
	}
	asOf, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		c.Error(apperrors.NewBadRequestError("as_of must be RFC3339: " + err.Error()))
		return
	}
	limit := 0
	if rawLimit := strings.TrimSpace(c.Query("limit")); rawLimit != "" {
		parsed, parseErr := strconv.Atoi(rawLimit)
		if parseErr != nil {
			c.Error(apperrors.NewBadRequestError("limit must be a number"))
			return
		}
		limit = parsed
	}
	result, replayErr := h.graphFactService.Replay(ctx, tenantID, types.GraphReplayRequest{
		AsOf:     asOf,
		SchemaID: strings.TrimSpace(c.Query("schema_id")),
		Limit:    limit,
	})
	if replayErr != nil {
		writeGraphFactError(c, ctx, replayErr)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": result})
}
