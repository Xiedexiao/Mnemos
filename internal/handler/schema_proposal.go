package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/application/service"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// SchemaProposalHandler 暴露本体建模提案（KnowAxis V3.0 §8.2/§8.4）：
// 文档驱动 AI 自动建模（POST /ontology/proposals，Contributor+）→ 列表/详情
// （Viewer+）→ 兼容性检查（Contributor+）→ 批准/驳回（Admin+，四眼）→
// 落地为正式本体 Schema（Admin+）。
//
// 租户/用户身份一律从 types.CallerFromContext 取（路由层 RBAC 守卫保证其
// 可信）；状态机、四眼、blocker 门禁都在 service 层强制，handler 只做参数
// 绑定与错误→HTTP 语义映射。
type SchemaProposalHandler struct {
	proposalService interfaces.SchemaProposalService
}

// NewSchemaProposalHandler constructs the schema proposal handler.
func NewSchemaProposalHandler(proposalService interfaces.SchemaProposalService) *SchemaProposalHandler {
	return &SchemaProposalHandler{proposalService: proposalService}
}

// schemaProposalCreateRequest 是 POST /ontology/proposals 的请求体。两种模式
// 二选一：
//   - 文档驱动（knowledge_ids 非空）：由 chat 模型读文档产出提案；
//   - 手工提交（proposal 非空）：无模型时的直通入口，payload 原样入库待审。
type schemaProposalCreateRequest struct {
	Name         string               `json:"name"          binding:"required,max=128"`
	Domain       string               `json:"domain"        binding:"omitempty,max=64"`
	KnowledgeIDs []string             `json:"knowledge_ids" binding:"max=8,dive,max=36"`
	Proposal     *schemaProposalInput `json:"proposal"`
	Rationale    string               `json:"rationale"     binding:"max=4000"`
}

// schemaProposalInput 是手工提交时的结构化提案（与 ProposalPayload JSON 同构）。
type schemaProposalInput struct {
	Types     []schemaProposalTypeInput     `json:"types"`
	Relations []schemaProposalRelationInput `json:"relations"`
}

// schemaProposalTypeInput 一个提案类型。
type schemaProposalTypeInput struct {
	Name        string                        `json:"name"`
	DisplayName string                        `json:"display_name"`
	Description string                        `json:"description"`
	Aliases     []string                      `json:"aliases"`
	Properties  []schemaProposalPropertyInput `json:"properties"`
}

// schemaProposalPropertyInput 一个提案属性。
type schemaProposalPropertyInput struct {
	Name     string `json:"name"`
	DataType string `json:"data_type"`
	Required bool   `json:"required"`
}

// schemaProposalRelationInput 一条提案关系。
type schemaProposalRelationInput struct {
	Name        string `json:"name"`
	SourceType  string `json:"source_type"`
	TargetType  string `json:"target_type"`
	Cardinality string `json:"cardinality"`
	Description string `json:"description"`
}

// toPayload 转成领域模型（规范化/校验交给 service）。
func (in *schemaProposalInput) toPayload() *types.ProposalPayload {
	if in == nil {
		return nil
	}
	payload := &types.ProposalPayload{
		Types:     make([]types.ProposalType, 0, len(in.Types)),
		Relations: make([]types.ProposalRelation, 0, len(in.Relations)),
	}
	for _, t := range in.Types {
		properties := make([]types.ProposalProperty, 0, len(t.Properties))
		for _, prop := range t.Properties {
			properties = append(properties, types.ProposalProperty{
				Name:     prop.Name,
				DataType: prop.DataType,
				Required: prop.Required,
			})
		}
		payload.Types = append(payload.Types, types.ProposalType{
			Name:        t.Name,
			DisplayName: t.DisplayName,
			Description: t.Description,
			Aliases:     t.Aliases,
			Properties:  properties,
		})
	}
	for _, rel := range in.Relations {
		payload.Relations = append(payload.Relations, types.ProposalRelation{
			Name:        rel.Name,
			SourceType:  rel.SourceType,
			TargetType:  rel.TargetType,
			Cardinality: rel.Cardinality,
			Description: rel.Description,
		})
	}
	return payload
}

// schemaProposalDecideRequest 是 approve/reject 的请求体（意见可选）。
type schemaProposalDecideRequest struct {
	Comment string `json:"comment" binding:"max=4000"`
}

// schemaProposalListFilter 解析列表查询参数。
func schemaProposalListFilter(c *gin.Context) types.SchemaProposalListFilter {
	filter := types.SchemaProposalListFilter{Status: strings.TrimSpace(c.Query("status"))}
	if raw := c.Query("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			filter.Limit = v
		}
	}
	if raw := c.Query("offset"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			filter.Offset = v
		}
	}
	return filter
}

// CreateSchemaProposal godoc
// @Summary      创建本体建模提案（文档驱动 AI 自动建模 / 手工提交）
// @Description  两种模式二选一：knowledge_ids 非空时由 chat 模型读文档（≤8 篇，每篇 ≤4000 字）产出结构化提案；proposal 非空时直接采用提交的 payload（无模型时的直通入口）。生成 pending 提案，不直接修改生效本体。
// @Tags         本体建模提案
// @Accept       json
// @Produce      json
// @Param        request body handler.schemaProposalCreateRequest true "{name, domain?, knowledge_ids?, proposal?, rationale?}"
// @Success      200  {object}  map[string]interface{}  "{success, data: 提案}"
// @Failure      400  {object}  errors.AppError  "参数非法/文档缺失/模型输出不是提案 JSON"
// @Failure      401  {object}  errors.AppError  "缺少租户上下文"
// @Security     Bearer
// @x-agent      {"id":"ontology.proposal_create","risk":"write","role":"contributor"}
// @Router       /ontology/proposals [post]
func (h *SchemaProposalHandler) CreateSchemaProposal(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}

	var req schemaProposalCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewValidationError("invalid request body: " + err.Error()))
		return
	}

	var (
		proposal *types.SchemaProposal
		err      error
	)
	switch {
	case len(req.KnowledgeIDs) > 0:
		proposal, err = h.proposalService.ProposeFromDocuments(
			ctx, caller.TenantID, req.Name, req.Domain, req.KnowledgeIDs, caller.UserID)
	case req.Proposal != nil:
		proposal, err = h.proposalService.ProposeManual(ctx, types.SchemaProposalManualInput{
			Name:               req.Name,
			Domain:             req.Domain,
			Rationale:          req.Rationale,
			Payload:            req.Proposal.toPayload(),
			SourceKnowledgeIDs: req.KnowledgeIDs,
		}, caller.UserID)
	default:
		c.Error(apperrors.NewBadRequestError("either knowledge_ids or proposal is required"))
		return
	}
	if err != nil {
		writeSchemaProposalError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": proposal})
}

// ListSchemaProposals godoc
// @Summary      本体建模提案列表
// @Description  分页返回本租户提案（created_at 倒序），可按 status 过滤（pending|approved|rejected|applied）。
// @Tags         本体建模提案
// @Produce      json
// @Param        status  query  string  false  "状态过滤"
// @Param        limit   query  int     false  "条数，默认50，上限200"
// @Param        offset  query  int     false  "偏移量"
// @Success      200  {object}  map[string]interface{}  "{success, data: {items, total}}"
// @Failure      400  {object}  errors.AppError  "未知状态值"
// @Failure      401  {object}  errors.AppError  "缺少租户上下文"
// @Security     Bearer
// @x-agent      {"id":"ontology.proposal_list","risk":"read","role":"viewer"}
// @Router       /ontology/proposals [get]
func (h *SchemaProposalHandler) ListSchemaProposals(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}

	items, total, err := h.proposalService.List(ctx, caller.TenantID, schemaProposalListFilter(c))
	if err != nil {
		writeSchemaProposalError(c, ctx, err)
		return
	}
	if items == nil {
		items = []*types.SchemaProposal{}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"items": items, "total": total}})
}

// GetSchemaProposal godoc
// @Summary      本体建模提案详情
// @Description  按提案 ID 返回提案（租户隔离），含结构化 proposal 与 compatibility_report。
// @Tags         本体建模提案
// @Produce      json
// @Param        id  path  string  true  "提案ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: 提案}"
// @Failure      401  {object}  errors.AppError  "缺少租户上下文"
// @Failure      404  {object}  errors.AppError  "提案不存在"
// @Security     Bearer
// @x-agent      {"id":"ontology.proposal_get","risk":"read","role":"viewer"}
// @Router       /ontology/proposals/{id} [get]
func (h *SchemaProposalHandler) GetSchemaProposal(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}
	proposalID := c.Param("id")
	if proposalID == "" {
		c.Error(apperrors.NewBadRequestError("proposal id is required"))
		return
	}

	proposal, err := h.proposalService.Get(ctx, caller.TenantID, proposalID)
	if err != nil {
		writeSchemaProposalError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": proposal})
}

// CheckSchemaProposalCompatibility godoc
// @Summary      提案兼容性检查
// @Description  与现有已发布本体比对：新类型（可自动通过）/ 同名类型属性变更（warning）/ 关系端点缺失（blocker），结果写入 compatibility_report。
// @Tags         本体建模提案
// @Produce      json
// @Param        id  path  string  true  "提案ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: 兼容性报告}"
// @Failure      400  {object}  errors.AppError  "提案结构非法"
// @Failure      401  {object}  errors.AppError  "缺少租户上下文"
// @Failure      404  {object}  errors.AppError  "提案不存在"
// @Security     Bearer
// @x-agent      {"id":"ontology.proposal_compatibility_check","risk":"write","role":"contributor"}
// @Router       /ontology/proposals/{id}/compatibility-check [post]
func (h *SchemaProposalHandler) CheckSchemaProposalCompatibility(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}
	proposalID := c.Param("id")
	if proposalID == "" {
		c.Error(apperrors.NewBadRequestError("proposal id is required"))
		return
	}

	report, err := h.proposalService.CheckCompatibility(ctx, caller.TenantID, proposalID)
	if err != nil {
		writeSchemaProposalError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": report})
}

// ApproveSchemaProposal godoc
// @Summary      批准本体建模提案
// @Description  pending→approved（Admin+，且审批人 != 提案创建人：四眼）。批准只表示同意落地，仍需 apply 才会写入正式本体。
// @Tags         本体建模提案
// @Accept       json
// @Produce      json
// @Param        id       path  string                            true  "提案ID"
// @Param        request  body  handler.schemaProposalDecideRequest false "{comment?}"
// @Success      200  {object}  map[string]interface{}  "{success, data: 提案}"
// @Failure      400  {object}  errors.AppError  "缺少提案ID"
// @Failure      401  {object}  errors.AppError  "缺少租户上下文"
// @Failure      403  {object}  errors.AppError  "非 Admin 或审批人即创建人"
// @Failure      404  {object}  errors.AppError  "提案不存在"
// @Failure      409  {object}  errors.AppError  "状态机不允许批准"
// @Security     Bearer
// @x-agent      {"id":"ontology.proposal_approve","risk":"write","role":"admin"}
// @Router       /ontology/proposals/{id}/approve [post]
func (h *SchemaProposalHandler) ApproveSchemaProposal(c *gin.Context) {
	h.decideSchemaProposal(c, true)
}

// RejectSchemaProposal godoc
// @Summary      驳回本体建模提案
// @Description  pending→rejected（终态，Admin+，四眼），记录审核人与意见。
// @Tags         本体建模提案
// @Accept       json
// @Produce      json
// @Param        id       path  string                            true  "提案ID"
// @Param        request  body  handler.schemaProposalDecideRequest false "{comment?}"
// @Success      200  {object}  map[string]interface{}  "{success, data: 提案}"
// @Failure      401  {object}  errors.AppError  "缺少租户上下文"
// @Failure      403  {object}  errors.AppError  "非 Admin 或审批人即创建人"
// @Failure      404  {object}  errors.AppError  "提案不存在"
// @Failure      409  {object}  errors.AppError  "状态机不允许驳回"
// @Security     Bearer
// @x-agent      {"id":"ontology.proposal_reject","risk":"write","role":"admin"}
// @Router       /ontology/proposals/{id}/reject [post]
func (h *SchemaProposalHandler) RejectSchemaProposal(c *gin.Context) {
	h.decideSchemaProposal(c, false)
}

// decideSchemaProposal 是 approve/reject 的共用实现：绑定可选意见后转给
// service，两者只在目标状态上不同。
func (h *SchemaProposalHandler) decideSchemaProposal(c *gin.Context, approve bool) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}
	proposalID := c.Param("id")
	if proposalID == "" {
		c.Error(apperrors.NewBadRequestError("proposal id is required"))
		return
	}

	// 意见可省略：空 body（io.EOF）视为不带意见，与 glossary/tag 的处理一致
	// （ContentLength 在 chunked 传输下为 -1，不能作为"有无 body"的判据）。
	var req schemaProposalDecideRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.Error(apperrors.NewValidationError("invalid request body: " + err.Error()))
		return
	}

	var (
		proposal *types.SchemaProposal
		err      error
	)
	if approve {
		proposal, err = h.proposalService.Approve(ctx, caller.TenantID, proposalID, caller.UserID, req.Comment)
	} else {
		proposal, err = h.proposalService.Reject(ctx, caller.TenantID, proposalID, caller.UserID, req.Comment)
	}
	if err != nil {
		writeSchemaProposalError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": proposal})
}

// ApplySchemaProposal godoc
// @Summary      应用提案落地为正式本体
// @Description  approved 且兼容性报告无 blocker 时，通过 OntologyService 落成 draft 本体（同名 draft 追加并提示版本升级；同名已发布则派生下一版本 draft）。回填 applied_schema_id 并置 status=applied。
// @Tags         本体建模提案
// @Produce      json
// @Param        id  path  string  true  "提案ID"
// @Success      200  {object}  map[string]interface{}  "{success, data: 落地结果}"
// @Failure      400  {object}  errors.AppError  "状态非 approved / 存在 blocker / 提案结构非法"
// @Failure      401  {object}  errors.AppError  "缺少租户上下文"
// @Failure      404  {object}  errors.AppError  "提案不存在"
// @Failure      409  {object}  errors.AppError  "状态机不允许落地"
// @Security     Bearer
// @x-agent      {"id":"ontology.proposal_apply","risk":"write","role":"admin"}
// @Router       /ontology/proposals/{id}/apply [post]
func (h *SchemaProposalHandler) ApplySchemaProposal(c *gin.Context) {
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
	if caller.TenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is missing"))
		return
	}
	proposalID := c.Param("id")
	if proposalID == "" {
		c.Error(apperrors.NewBadRequestError("proposal id is required"))
		return
	}

	result, err := h.proposalService.Apply(ctx, caller.TenantID, proposalID, caller.UserID)
	if err != nil {
		writeSchemaProposalError(c, ctx, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

// writeSchemaProposalError 把提案业务哨兵错误映射为 HTTP 语义。映射表：
// 状态机/自审 → 409/403，身份门槛 → 403，参数与模型输出 → 400，资源缺失 →
// 404，缺租户上下文 → 401，其余记日志后 500（不外泄内部细节）。
func writeSchemaProposalError(c *gin.Context, ctx context.Context, err error) {
	switch {
	case errors.Is(err, service.ErrSchemaProposalInvalidState):
		c.Error(apperrors.NewConflictError(err.Error()))
	case errors.Is(err, service.ErrSchemaProposalForbidden),
		errors.Is(err, service.ErrSchemaProposalSelfReview):
		c.Error(apperrors.NewForbiddenError(err.Error()))
	case errors.Is(err, service.ErrSchemaProposalNameRequired),
		errors.Is(err, service.ErrSchemaProposalInvalidPayload),
		errors.Is(err, service.ErrSchemaProposalNoDocuments),
		errors.Is(err, service.ErrSchemaProposalTooManyDocuments),
		errors.Is(err, service.ErrSchemaProposalDocumentEmpty),
		errors.Is(err, service.ErrSchemaProposalModelMissing),
		errors.Is(err, service.ErrSchemaProposalModelOutput),
		errors.Is(err, service.ErrSchemaProposalCompatibilityBlocker),
		errors.Is(err, service.ErrSchemaProposalTargetSchemaImmutable),
		errors.Is(err, service.ErrSchemaProposalActorMissing):
		c.Error(apperrors.NewBadRequestError(err.Error()))
	case errors.Is(err, service.ErrSchemaProposalNotFound),
		errors.Is(err, service.ErrSchemaProposalDocumentNotFound):
		c.Error(apperrors.NewNotFoundError(err.Error()))
	case errors.Is(err, service.ErrSchemaProposalTenantMissing):
		c.Error(apperrors.NewUnauthorizedError(err.Error()))
	default:
		logger.ErrorWithFields(ctx, err, map[string]interface{}{"handler": "schema_proposal"})
		c.Error(apperrors.NewInternalServerError("schema proposal operation failed").WithDetails(err.Error()))
	}
}
