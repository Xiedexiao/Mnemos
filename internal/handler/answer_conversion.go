package handler

import (
	"github.com/gin-gonic/gin"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// AnswerConversionHandler exposes the §13 answer→knowledge conversion.
type AnswerConversionHandler struct {
	conversion interfaces.AnswerConversionService
}

// NewAnswerConversionHandler constructs the handler.
func NewAnswerConversionHandler(
	conversion interfaces.AnswerConversionService,
) *AnswerConversionHandler {
	return &AnswerConversionHandler{conversion: conversion}
}

// ConvertAnswerToKnowledge godoc
// @Summary      答案转为知识候选
// @Description  把一次已回答的问答（按 request_id 定位证据包）整理成经验候选（KnowAxis V3.0 §13 回答→知识沉淀）。走既有的候选审核路径而不是直接建知识：答案由模型生成，必须经过 §10.4 的查重与审核门禁。拒答/空答案返回 400；同一 request_id 重复转换返回已有候选（created=false）；层级默认 L1，L3 只能由发布门禁授予。引用的知识 ID 会带进候选证据，供发布时推导派生权限。
// @Tags         经验沉淀
// @Accept       json
// @Produce      json
// @Param        request  body  types.AnswerConversionInput  true  "{request_id, title, level}"
// @Success      200  {object}  map[string]interface{}  "{success, data: AnswerConversionResult}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /knowledge-candidates/from-answer [post]
func (h *AnswerConversionHandler) ConvertAnswerToKnowledge(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(apperrors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var input types.AnswerConversionInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}
	// 提交人恒取调用者：答案可能包含只有提问者有权限看到的内容，让别人
	// 指定 requested_by 会让"谁把这条内容变成了知识"变得不可追溯。
	caller := types.CallerFromContext(ctx)
	result, err := h.conversion.Convert(ctx, tenantID, caller.UserID, input)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": result})
}
