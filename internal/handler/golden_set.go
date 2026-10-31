package handler

import (
	"encoding/json"
	"strconv"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// GoldenSetHandler serves the golden question set endpoints
// (KnowAxis V3.0 §12.5/§14.8). Writes and run triggers are Admin-only —
// 黄金问题集是发布门禁的基准，改题/跑评测直接决定"能不能发布"的结论;
// reads (含门禁查询) are Viewer+. The role floor lives on the routes.
type GoldenSetHandler struct {
	goldenSetService interfaces.GoldenSetService
}

// NewGoldenSetHandler constructs the handler.
func NewGoldenSetHandler(goldenSetService interfaces.GoldenSetService) *GoldenSetHandler {
	return &GoldenSetHandler{goldenSetService: goldenSetService}
}

// createGoldenSetRequest is the body of POST /golden-sets.
type createGoldenSetRequest struct {
	Name        string `json:"name" binding:"required"`
	Domain      string `json:"domain"`
	KBID        string `json:"kb_id"`
	Description string `json:"description"`
}

// updateGoldenSetRequest is the body of PUT /golden-sets/:id. Pointer fields
// distinguish "not provided" from "".
type updateGoldenSetRequest struct {
	Name        *string `json:"name"`
	Domain      *string `json:"domain"`
	KBID        *string `json:"kb_id"`
	Description *string `json:"description"`
}

// goldenEvidenceRefRequest mirrors types.GoldenEvidenceRef on the wire.
type goldenEvidenceRefRequest struct {
	KnowledgeID string `json:"knowledge_id"`
	ChunkID     string `json:"chunk_id"`
	Locator     string `json:"locator"`
}

// goldenQuestionRequest is one entry of the batch import body (POST
// /golden-sets/:id/questions) and of the single-question update body.
type goldenQuestionRequest struct {
	Question             string                     `json:"question"`
	ExpectedAnswer       string                     `json:"expected_answer"`
	ExpectedEvidence     []goldenEvidenceRefRequest `json:"expected_evidence"`
	SimulatedUserRole    string                     `json:"simulated_user_role"`
	RiskLevel            string                     `json:"risk_level"`
	FreshnessExpectation string                     `json:"freshness_expectation"`
	Enabled              *bool                      `json:"enabled"`
}

// importGoldenQuestionsRequest is the body of POST /golden-sets/:id/questions:
// a JSON array of questions, upserted by question text.
type importGoldenQuestionsRequest struct {
	Questions []goldenQuestionRequest `json:"questions" binding:"required,min=1"`
}

// updateGoldenQuestionRequest is the body of PUT /golden-sets/:id/questions/:qid.
// Pointer fields distinguish "not provided" from "".
type updateGoldenQuestionRequest struct {
	ExpectedAnswer       *string                     `json:"expected_answer"`
	ExpectedEvidence     *[]goldenEvidenceRefRequest `json:"expected_evidence"`
	SimulatedUserRole    *string                     `json:"simulated_user_role"`
	RiskLevel            *string                     `json:"risk_level"`
	FreshnessExpectation *string                     `json:"freshness_expectation"`
	Enabled              *bool                       `json:"enabled"`
}

// goldenEvidencePayload converts the request refs into the JSONB payload,
// falling back to an empty array so the default-valued column always holds a
// JSON array. Kept local to this vertical (same rationale as
// glossaryAliasPayload).
func goldenEvidencePayload(refs []goldenEvidenceRefRequest) types.JSON {
	if refs == nil {
		return types.JSON([]byte("[]"))
	}
	out := make([]types.GoldenEvidenceRef, 0, len(refs))
	for _, ref := range refs {
		out = append(out, types.GoldenEvidenceRef{
			KnowledgeID: ref.KnowledgeID,
			ChunkID:     ref.ChunkID,
			Locator:     ref.Locator,
		})
	}
	data, err := json.Marshal(out)
	if err != nil {
		return types.JSON([]byte("[]"))
	}
	return types.JSON(data)
}

// goldenEvidenceRefs converts the update request's refs into the domain
// slice the service re-validates and re-marshals.
func goldenEvidenceRefs(refs *[]goldenEvidenceRefRequest) *[]types.GoldenEvidenceRef {
	if refs == nil {
		return nil
	}
	out := make([]types.GoldenEvidenceRef, 0, len(*refs))
	for _, ref := range *refs {
		out = append(out, types.GoldenEvidenceRef{
			KnowledgeID: ref.KnowledgeID,
			ChunkID:     ref.ChunkID,
			Locator:     ref.Locator,
		})
	}
	return &out
}

// CreateSet godoc
// @Summary      新建黄金问题集
// @Description  新增一份黄金问题集（租户内 name 唯一；kb_id 绑定评测知识库，可先建集后绑库）。
// @Tags         黄金问题集
// @Accept       json
// @Produce      json
// @Param        request body createGoldenSetRequest true "问题集"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError
// @Security     Bearer
// @Router       /golden-sets [post]
func (h *GoldenSetHandler) CreateSet(c *gin.Context) {
	ctx := c.Request.Context()
	var req createGoldenSetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	set, err := h.goldenSetService.CreateSet(ctx, &types.GoldenSet{
		TenantID:    caller.TenantID,
		Name:        req.Name,
		Domain:      req.Domain,
		KBID:        req.KBID,
		Description: req.Description,
		CreatedBy:   caller.UserID,
	})
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": set})
}

// ListSets godoc
// @Summary      列出黄金问题集
// @Description  列出租户内的问题集；domain 可选过滤领域。
// @Tags         黄金问题集
// @Produce      json
// @Param        domain  query  string  false  "领域"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /golden-sets [get]
func (h *GoldenSetHandler) ListSets(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	sets, err := h.goldenSetService.ListSets(ctx, caller.TenantID, c.Query("domain"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": sets})
}

// GetSet godoc
// @Summary      查询单个问题集
// @Description  按 ID 返回问题集（含绑定的评测知识库）。
// @Tags         黄金问题集
// @Produce      json
// @Param        id  path  string  true  "问题集ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /golden-sets/{id} [get]
func (h *GoldenSetHandler) GetSet(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	set, err := h.goldenSetService.GetSet(ctx, caller.TenantID, c.Param("id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": set})
}

// UpdateSet godoc
// @Summary      更新问题集
// @Description  按字段更新问题集；nil 字段保持不变，改名会重新校验租户内唯一。
// @Tags         黄金问题集
// @Accept       json
// @Produce      json
// @Param        id      path  string                   true  "问题集ID"
// @Param        request body  updateGoldenSetRequest   true  "更新"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError
// @Security     Bearer
// @Router       /golden-sets/{id} [put]
func (h *GoldenSetHandler) UpdateSet(c *gin.Context) {
	ctx := c.Request.Context()
	var req updateGoldenSetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	saved, err := h.goldenSetService.UpdateSet(ctx, caller.TenantID, c.Param("id"),
		&types.GoldenSetUpdate{
			Name:        req.Name,
			Domain:      req.Domain,
			KBID:        req.KBID,
			Description: req.Description,
		}, caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": saved})
}

// DeleteSet godoc
// @Summary      删除问题集
// @Description  物理删除问题集，并级联删除其题目与全部评测记录。
// @Tags         黄金问题集
// @Produce      json
// @Param        id  path  string  true  "问题集ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /golden-sets/{id} [delete]
func (h *GoldenSetHandler) DeleteSet(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if err := h.goldenSetService.DeleteSet(ctx, caller.TenantID, c.Param("id"), caller.UserID); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// ImportQuestions godoc
// @Summary      批量导入题目
// @Description  按 question 文本 upsert：命中即覆盖可变字段（并重新启用），未命中即新增。
// @Tags         黄金问题集
// @Accept       json
// @Produce      json
// @Param        id      path  string                        true  "问题集ID"
// @Param        request body  importGoldenQuestionsRequest  true  "题目数组"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError
// @Security     Bearer
// @Router       /golden-sets/{id}/questions [post]
func (h *GoldenSetHandler) ImportQuestions(c *gin.Context) {
	ctx := c.Request.Context()
	var req importGoldenQuestionsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	input := make([]*types.GoldenQuestion, 0, len(req.Questions))
	for i := range req.Questions {
		item := &req.Questions[i]
		input = append(input, &types.GoldenQuestion{
			Question:             item.Question,
			ExpectedAnswer:       item.ExpectedAnswer,
			ExpectedEvidence:     goldenEvidencePayload(item.ExpectedEvidence),
			SimulatedUserRole:    item.SimulatedUserRole,
			RiskLevel:            item.RiskLevel,
			FreshnessExpectation: item.FreshnessExpectation,
		})
	}
	result, err := h.goldenSetService.ImportQuestions(
		ctx, caller.TenantID, c.Param("id"), input, caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": result})
}

// ListQuestions godoc
// @Summary      列出题目
// @Description  列出问题集内的全部题目（含 disabled，前端标灰展示）。
// @Tags         黄金问题集
// @Produce      json
// @Param        id  path  string  true  "问题集ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /golden-sets/{id}/questions [get]
func (h *GoldenSetHandler) ListQuestions(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	questions, err := h.goldenSetService.ListQuestions(ctx, caller.TenantID, c.Param("id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": questions})
}

// UpdateQuestion godoc
// @Summary      更新题目
// @Description  按字段更新题目；题干不可改（文本即题目身份），改题走"删旧 + 导入新"。
// @Tags         黄金问题集
// @Accept       json
// @Produce      json
// @Param        id      path  string                       true  "问题集ID"
// @Param        qid     path  string                       true  "题目ID"
// @Param        request body  updateGoldenQuestionRequest  true  "更新"
// @Success      200  {object}  map[string]interface{}
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /golden-sets/{id}/questions/{qid} [put]
func (h *GoldenSetHandler) UpdateQuestion(c *gin.Context) {
	ctx := c.Request.Context()
	var req updateGoldenQuestionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	saved, err := h.goldenSetService.UpdateQuestion(
		ctx, caller.TenantID, c.Param("id"), c.Param("qid"),
		&types.GoldenQuestionUpdate{
			ExpectedAnswer:       req.ExpectedAnswer,
			ExpectedEvidence:     goldenEvidenceRefs(req.ExpectedEvidence),
			SimulatedUserRole:    req.SimulatedUserRole,
			RiskLevel:            req.RiskLevel,
			FreshnessExpectation: req.FreshnessExpectation,
			Enabled:              req.Enabled,
		}, caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": saved})
}

// DeleteQuestion godoc
// @Summary      删除题目
// @Description  从问题集中物理删除一道题。
// @Tags         黄金问题集
// @Produce      json
// @Param        id   path  string  true  "问题集ID"
// @Param        qid  path  string  true  "题目ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /golden-sets/{id}/questions/{qid} [delete]
func (h *GoldenSetHandler) DeleteQuestion(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if err := h.goldenSetService.DeleteQuestion(
		ctx, caller.TenantID, c.Param("id"), c.Param("qid"), caller.UserID); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// ListRuns godoc
// @Summary      列出评测记录
// @Description  按时间倒序列出问题集的评测运行；limit 默认 50。
// @Tags         黄金问题集
// @Produce      json
// @Param        id     path  string  false  "问题集ID"
// @Param        limit  query int     false  "条数"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /golden-sets/{id}/runs [get]
func (h *GoldenSetHandler) ListRuns(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	limit := 0
	if v := c.Query("limit"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 0 {
			c.Error(errors.NewBadRequestError("limit must be a non-negative integer"))
			return
		}
		limit = parsed
	}
	runs, err := h.goldenSetService.ListRuns(ctx, caller.TenantID, c.Param("id"), limit)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": runs})
}

// RunEvaluation godoc
// @Summary      运行评测
// @Description  对绑定的评测知识库同步跑一遍检索评测（v1：top5_hit/recall_at_5/NDCG/MRR），结果落库并返回。
// @Tags         黄金问题集
// @Produce      json
// @Param        id  path  string  true  "问题集ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError
// @Security     Bearer
// @Router       /golden-sets/{id}/run [post]
func (h *GoldenSetHandler) RunEvaluation(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	// v1 同步执行：题量小（黄金集是人工维护的基准集）可接受；题量上去后
	// 改成落 running 行 + 异步任务，前端轮询 GET /golden-runs/:run_id。
	run, err := h.goldenSetService.RunEvaluation(ctx, caller.TenantID, c.Param("id"), caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": run})
}

// GetRun godoc
// @Summary      查询单次评测
// @Description  按 ID 返回一次评测运行（聚合指标 + 逐题得分与缺口）。
// @Tags         黄金问题集
// @Produce      json
// @Param        run_id  path  string  true  "评测ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /golden-runs/{run_id} [get]
func (h *GoldenSetHandler) GetRun(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	run, err := h.goldenSetService.GetRun(ctx, caller.TenantID, c.Param("run_id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": run})
}

// CheckGate godoc
// @Summary      发布门禁查询
// @Description  读取最近一次 completed 评测的 pass 结论；没有完成过的评测 = 不放行（失败闭合）。
// @Tags         黄金问题集
// @Produce      json
// @Param        id  path  string  true  "问题集ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /golden-sets/{id}/gate [get]
func (h *GoldenSetHandler) CheckGate(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	pass, lastRun, err := h.goldenSetService.CheckGate(ctx, caller.TenantID, c.Param("id"))
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{
		"pass":     pass,
		"last_run": lastRun,
	}})
}
