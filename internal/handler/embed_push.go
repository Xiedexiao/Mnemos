package handler

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// EmbedPushHandler exposes the §15.5 embedded contextual push.
type EmbedPushHandler struct {
	pushes interfaces.EmbedPushService
}

// NewEmbedPushHandler constructs the handler.
func NewEmbedPushHandler(pushes interfaces.EmbedPushService) *EmbedPushHandler {
	return &EmbedPushHandler{pushes: pushes}
}

// resolvePushRequest is the widget's context report.
type resolvePushRequest struct {
	System     string            `json:"system"`
	Scene      string            `json:"scene"`
	EntityType string            `json:"entity_type"`
	EntityID   string            `json:"entity_id"`
	Attributes map[string]string `json:"attributes"`
	Limit      int               `json:"limit"`
}

// pushPreferenceRequest is the opt-out body.
type pushPreferenceRequest struct {
	Enabled *bool `json:"enabled"`
}

// pushEventRequest records a click or dismiss.
type pushEventRequest struct {
	RuleID      string `json:"rule_id"`
	KnowledgeID string `json:"knowledge_id"`
	EventType   string `json:"event_type"`
	System      string `json:"system"`
	Scene       string `json:"scene"`
	EntityType  string `json:"entity_type"`
	EntityID    string `json:"entity_id"`
}

// pushRuleRequest is the Admin rule create/update body.
type pushRuleRequest struct {
	Name         string            `json:"name"`
	System       string            `json:"system"`
	Scene        string            `json:"scene"`
	EntityType   string            `json:"entity_type"`
	EntityValue  string            `json:"entity_value"`
	Attributes   map[string]string `json:"attributes"`
	TagNames     []string          `json:"tag_names"`
	KnowledgeIDs []string          `json:"knowledge_ids"`
	Reason       string            `json:"reason"`
	Priority     int               `json:"priority"`
	Enabled      *bool             `json:"enabled"`
}

// ResolveEmbedPush godoc
// @Summary      上下文推送
// @Description  业务系统（MES/ERP/OA/CRM…）在处理工单、填表、查看设备时上报上下文，平台按推送规则返回相关知识卡片（KnowAxis V3.0 §15.5）。结果已按生命周期、文档 ACL、目录 ACL 过滤；用户关闭推送后返回 enabled=false。
// @Tags         嵌入式推送
// @Accept       json
// @Produce      json
// @Param        request  body  resolvePushRequest  true  "业务上下文"
// @Success      200  {object}  map[string]interface{}  "{success, data: EmbedPushResult}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /embed/push/resolve [post]
func (h *EmbedPushHandler) ResolveEmbedPush(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req resolvePushRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	result, err := h.pushes.Resolve(ctx, tenantID, caller.UserID, types.EmbedPushContext{
		System:     strings.TrimSpace(req.System),
		Scene:      strings.TrimSpace(req.Scene),
		EntityType: strings.TrimSpace(req.EntityType),
		EntityID:   strings.TrimSpace(req.EntityID),
		Attributes: req.Attributes,
	}, req.Limit)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": result})
}

// GetEmbedPushPreference godoc
// @Summary      推送开关
// @Description  读取本人是否接收业务系统内的上下文推送（默认开启）。
// @Tags         嵌入式推送
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "{success, data: EmbedPushPreference}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /embed/push/preferences [get]
func (h *EmbedPushHandler) GetEmbedPushPreference(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	preference, err := h.pushes.GetPreference(ctx, tenantID, caller.UserID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": preference})
}

// SetEmbedPushPreference godoc
// @Summary      关闭/开启推送
// @Description  一键关闭或重新开启业务系统内的上下文推送（§15.5 可关闭）。关闭后不再返回卡片，也不再记录交互。
// @Tags         嵌入式推送
// @Accept       json
// @Produce      json
// @Param        request  body  pushPreferenceRequest  true  "开关"
// @Success      200  {object}  map[string]interface{}  "{success, data: EmbedPushPreference}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /embed/push/preferences [put]
func (h *EmbedPushHandler) SetEmbedPushPreference(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req pushPreferenceRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Enabled == nil {
		c.Error(errors.NewBadRequestError("enabled is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	preference, err := h.pushes.SetPreference(ctx, tenantID, caller.UserID, *req.Enabled)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": preference})
}

// RecordEmbedPushEvent godoc
// @Summary      推送交互
// @Description  记录卡片点击或忽略（忽略后同一张卡片 30 天内不再推送）。
// @Tags         嵌入式推送
// @Accept       json
// @Produce      json
// @Param        request  body  pushEventRequest  true  "交互"
// @Success      200  {object}  map[string]interface{}  "{success: true}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /embed/push/events [post]
func (h *EmbedPushHandler) RecordEmbedPushEvent(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req pushEventRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	if err := h.pushes.RecordEvent(ctx, tenantID, caller.UserID,
		req.RuleID, req.KnowledgeID, types.EmbedPushEventType(strings.TrimSpace(req.EventType)),
		types.EmbedPushContext{
			System: req.System, Scene: req.Scene,
			EntityType: req.EntityType, EntityID: req.EntityID,
		}); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// ListEmbedPushRules godoc
// @Summary      推送规则列表
// @Description  列出业务系统上下文推送规则（管理员）。
// @Tags         嵌入式推送
// @Produce      json
// @Success      200  {object}  map[string]interface{}  "{success, data: [EmbedPushRule]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /embed/push/rules [get]
func (h *EmbedPushHandler) ListEmbedPushRules(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	rules, err := h.pushes.ListRules(ctx, tenantID)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rules})
}

// CreateEmbedPushRule godoc
// @Summary      新增推送规则
// @Description  新建一条"业务上下文 → 知识"的推送规则：按系统/场景/实体/属性匹配，用标签或知识 ID 选出内容。
// @Tags         嵌入式推送
// @Accept       json
// @Produce      json
// @Param        request  body  pushRuleRequest  true  "规则"
// @Success      200  {object}  map[string]interface{}  "{success, data: EmbedPushRule}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /embed/push/rules [post]
func (h *EmbedPushHandler) CreateEmbedPushRule(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req pushRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	rule, err := h.pushes.CreateRule(ctx, tenantID, caller.UserID, req.toInput())
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rule})
}

// UpdateEmbedPushRule godoc
// @Summary      修改推送规则
// @Description  修改推送规则；匹配字段传空表示"匹配任意"。
// @Tags         嵌入式推送
// @Accept       json
// @Produce      json
// @Param        id       path  string           true  "规则 ID"
// @Param        request  body  pushRuleRequest  true  "规则"
// @Success      200  {object}  map[string]interface{}  "{success, data: EmbedPushRule}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /embed/push/rules/{id} [put]
func (h *EmbedPushHandler) UpdateEmbedPushRule(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	var req pushRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}
	caller := types.CallerFromContext(ctx)
	rule, err := h.pushes.UpdateRule(ctx, tenantID, c.Param("id"), caller.UserID, req.toInput())
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": rule})
}

// DeleteEmbedPushRule godoc
// @Summary      删除推送规则
// @Description  删除一条推送规则（历史事件保留）。
// @Tags         嵌入式推送
// @Produce      json
// @Param        id  path  string  true  "规则 ID"
// @Success      200  {object}  map[string]interface{}  "{success: true}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /embed/push/rules/{id} [delete]
func (h *EmbedPushHandler) DeleteEmbedPushRule(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	caller := types.CallerFromContext(ctx)
	if err := h.pushes.DeleteRule(ctx, tenantID, c.Param("id"), caller.UserID); err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}

// EmbedPushRuleStats godoc
// @Summary      推送效果
// @Description  按规则统计曝光/点击/忽略次数（管理员）。
// @Tags         嵌入式推送
// @Produce      json
// @Param        days  query  int  false  "统计窗口天数（默认 30）"
// @Success      200  {object}  map[string]interface{}  "{success, data: [EmbedPushRuleStats]}"
// @Security     Bearer
// @Security     ApiKeyAuth
// @Router       /embed/push/stats [get]
func (h *EmbedPushHandler) EmbedPushRuleStats(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		c.Error(errors.NewUnauthorizedError("tenant context is required"))
		return
	}
	days, err := strconv.Atoi(c.DefaultQuery("days", "30"))
	if err != nil || days <= 0 {
		days = 30
	}
	stats, err := h.pushes.RuleStats(ctx, tenantID, days)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": stats})
}

// toInput converts the request body into the service input.
func (r pushRuleRequest) toInput() types.EmbedPushRuleInput {
	return types.EmbedPushRuleInput{
		Name:         r.Name,
		System:       r.System,
		Scene:        r.Scene,
		EntityType:   r.EntityType,
		EntityValue:  r.EntityValue,
		Attributes:   r.Attributes,
		TagNames:     r.TagNames,
		KnowledgeIDs: r.KnowledgeIDs,
		Reason:       r.Reason,
		Priority:     r.Priority,
		Enabled:      r.Enabled,
	}
}
