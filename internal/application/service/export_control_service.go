package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 导出管控服务（KnowAxis V3.0 §2.2 export 动作 / §2.4 标识 v1）。
//
// export_requests 状态机的唯一写入口：审批策略、四眼规则与审计都在这里
// 强制，路由层守卫（Admin）只是第一道门。
// ============================================================================

// 导出管控审计动作（与 glossary/review 同风格：domain.event）。
const (
	auditActionExportRequestCreated   = "export.request_created"
	auditActionExportRequestDecided   = "export.request_decided"
	auditActionExportRequestFulfilled = "export.request_fulfilled"
)

// exportWatermarkTimeLayout 水印文本里的时间段格式（精确到秒，租户内一次
// 导出申请一行，秒级 + 随机 4 位足够区分）。
const exportWatermarkTimeLayout = "20060102150405"

// maxExportDecisionNoteLen 限制审批备注长度（与 review 审计评语同口径），
// 防止审批面把审计行撑爆。
const maxExportDecisionNoteLen = 500

// exportControlService implements interfaces.ExportControlService.
type exportControlService struct {
	repo  interfaces.ExportRequestRepository
	audit interfaces.AuditLogService
	now   func() time.Time
}

// NewExportControlService constructs the export control service.
func NewExportControlService(
	repo interfaces.ExportRequestRepository,
	audit interfaces.AuditLogService,
) interfaces.ExportControlService {
	return &exportControlService{repo: repo, audit: audit, now: time.Now}
}

// exportScopeFilterJSON marshals the scope filter into a JSONB payload,
// falling back to an empty object on failure so a logging-shaped marshal
// error never blocks a request write. Kept local to this vertical (and
// deliberately not named like the glossary/lifecycle helpers) so the
// verticals stay independently maintainable.
func exportScopeFilterJSON(filter map[string]interface{}) types.JSON {
	if len(filter) == 0 {
		return types.JSON([]byte("{}"))
	}
	data, err := json.Marshal(filter)
	if err != nil {
		return types.JSON([]byte("{}"))
	}
	return types.JSON(data)
}

// exportDetailsJSON marshals an audit detail payload, falling back to an
// empty object on failure so logging never blocks a business write. Local
// to this vertical (same keep-verticals-independent rationale).
func exportDetailsJSON(v interface{}) types.JSON {
	data, err := json.Marshal(v)
	if err != nil {
		return types.JSON([]byte("{}"))
	}
	return types.JSON(data)
}

// exportWatermarkText generates the default watermark: "用户名-时间-随机4位"
// （V3.0 §2.4 标识：导出物可追溯到"谁、何时、哪一次"）。用户名取申请人的
// user ID（调用方展示层可再映射昵称）；随机 4 位用 crypto/rand，防碰撞也
// 防猜测。
func exportWatermarkText(requestedBy string, now time.Time) string {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 失败几乎不可能；退化为纳秒尾数，仍保证非空。
		b[0] = byte(now.Nanosecond() >> 8)
		b[1] = byte(now.Nanosecond())
	}
	return fmt.Sprintf("%s-%s-%04X", requestedBy, now.Format(exportWatermarkTimeLayout), b)
}

// Create registers one export request.
//
// 审批策略 v1：max_classification=secret/confidential 或 scope=audit 必须
// 人工审批（保持 pending，等 Admin Decide）；public/internal 且非审计导出
// 可自动 approve —— Create 内直接置 approved（approver_id 留空、
// decision_note 标注 auto-approved，四眼原则只约束高危导出，低密级导出
// 若也走人工审批会把审批面淹没成橡皮图章）。
func (s *exportControlService) Create(ctx context.Context, req *types.ExportRequest) (*types.ExportRequest, error) {
	if req == nil {
		return nil, apperrors.NewBadRequestError("request body is required")
	}
	if req.TenantID == 0 {
		return nil, apperrors.NewBadRequestError("tenant context is missing")
	}
	if req.RequestedBy == "" {
		return nil, apperrors.NewBadRequestError("requested_by is required")
	}
	scope := types.ExportScope(strings.TrimSpace(req.Scope))
	if !scope.Valid() {
		return nil, apperrors.NewBadRequestError("scope must be one of: knowledge, evidence_pack, audit")
	}
	req.Scope = string(scope)

	classification := types.ParseKnowledgeClassification(strings.TrimSpace(req.MaxClassification))
	req.MaxClassification = string(classification)

	if len(req.ScopeFilter) == 0 {
		req.ScopeFilter = exportScopeFilterJSON(nil)
	}
	if req.ID == "" {
		req.ID = uuid.NewString()
	}
	if req.WatermarkText == "" {
		req.WatermarkText = exportWatermarkText(req.RequestedBy, s.now())
	}
	// 调用方不能替状态机定起点：新申请一律 pending（自动放行在下方统一
	// 处理），避免伪造 approved/fulfilled 直接绕过审批。
	if req.Status != "" && req.Status != string(types.ExportRequestStatusPending) {
		return nil, apperrors.NewBadRequestError("a new export request must start as pending")
	}
	req.Status = string(types.ExportRequestStatusPending)

	// 审批策略 v1（见函数注释）：低危导出自动放行。
	if !req.RequiresApproval() {
		now := s.now()
		req.Status = string(types.ExportRequestStatusApproved)
		req.DecisionNote = "auto-approved (public/internal, non-audit)"
		req.DecidedAt = &now
		// approver_id 留空：这不是一次人工审批，审计里以 decision_note 区分。
	}

	if err := s.repo.CreateRequest(ctx, req); err != nil {
		return nil, err
	}
	s.logAudit(ctx, req.TenantID, req.RequestedBy, auditActionExportRequestCreated,
		req.ID, map[string]interface{}{
			"scope":              req.Scope,
			"max_classification": req.MaxClassification,
			"status":             req.Status,
			"requires_approval":  req.RequiresApproval(),
		})
	return req, nil
}

// Decide applies one approval decision. Route guard is Admin+, and the
// service re-checks the role (defense in depth) plus the four-eyes rule:
// 审批人不得是申请人 —— 同一个人申请 + 审批会让导出管控形同虚设。
func (s *exportControlService) Decide(
	ctx context.Context, tenantID uint64, requestID string, input *types.ExportDecisionInput,
) (*types.ExportRequest, error) {
	if input == nil || !input.Decision.Valid() {
		return nil, apperrors.NewBadRequestError("decision must be approve or reject")
	}
	if input.ApproverRole.Level() < types.TenantRoleAdmin.Level() {
		return nil, apperrors.NewForbiddenError("only Admin+ can decide export requests")
	}
	if input.Decision == types.ExportDecisionReject && strings.TrimSpace(input.Note) == "" {
		return nil, apperrors.NewBadRequestError("a rejection requires a decision note")
	}

	req, err := s.loadRequest(ctx, tenantID, requestID)
	if err != nil {
		return nil, err
	}
	if !req.IsStatus(types.ExportRequestStatusPending) {
		return nil, apperrors.NewConflictError(
			"export request is not pending (current status: " + req.Status + ")")
	}
	// 四眼：审批人 ≠ 申请人。自动 approve 的行不经此路径（无 approver）。
	if input.ApproverID == req.RequestedBy {
		return nil, apperrors.NewForbiddenError(
			"four-eyes violation: the requester cannot approve their own export request")
	}

	now := s.now()
	note := strings.TrimSpace(input.Note)
	if len([]rune(note)) > maxExportDecisionNoteLen {
		note = string([]rune(note)[:maxExportDecisionNoteLen])
	}
	req.ApproverID = input.ApproverID
	req.DecisionNote = note
	req.DecidedAt = &now
	if input.Decision == types.ExportDecisionApprove {
		req.Status = string(types.ExportRequestStatusApproved)
	} else {
		req.Status = string(types.ExportRequestStatusRejected)
	}
	if err := s.repo.SaveRequest(ctx, req); err != nil {
		return nil, err
	}
	s.logAudit(ctx, tenantID, input.ApproverID, auditActionExportRequestDecided,
		req.ID, map[string]interface{}{
			"decision": string(input.Decision),
			"scope":    req.Scope,
			"status":   req.Status,
		})
	return req, nil
}

// Fulfill marks an approved request fulfilled. The caller renders the
// returned row's watermark_text on the exported artifact（文档级水印渲染由
// 前端做，离线 DRM 不在 v1 范围）。Only approved requests can be fulfilled:
// pending 必须先走审批，rejected/fulfilled 是终态。
func (s *exportControlService) Fulfill(
	ctx context.Context, tenantID uint64, requestID, actorID string,
) (*types.ExportRequest, error) {
	req, err := s.loadRequest(ctx, tenantID, requestID)
	if err != nil {
		return nil, err
	}
	if !req.IsStatus(types.ExportRequestStatusApproved) {
		return nil, apperrors.NewConflictError(
			"export request is not approved (current status: " + req.Status + ")")
	}
	now := s.now()
	req.Status = string(types.ExportRequestStatusFulfilled)
	req.FulfilledAt = &now
	if err := s.repo.SaveRequest(ctx, req); err != nil {
		return nil, err
	}
	s.logAudit(ctx, tenantID, actorID, auditActionExportRequestFulfilled,
		req.ID, map[string]interface{}{
			"scope":          req.Scope,
			"watermark_text": req.WatermarkText,
		})
	return req, nil
}

// List lists the tenant's requests, newest first; an empty status lists all.
func (s *exportControlService) List(
	ctx context.Context, tenantID uint64, status string,
) ([]*types.ExportRequest, error) {
	status = strings.TrimSpace(status)
	if status != "" && !types.ExportRequestStatus(status).Valid() {
		return nil, apperrors.NewBadRequestError(
			"invalid status filter (pending|approved|rejected|fulfilled)")
	}
	return s.repo.ListRequests(ctx, tenantID, status)
}

// Get resolves one request by ID within the tenant.
func (s *exportControlService) Get(
	ctx context.Context, tenantID uint64, requestID string,
) (*types.ExportRequest, error) {
	return s.loadRequest(ctx, tenantID, requestID)
}

// loadRequest resolves a tenant-scoped request, translating record-not-found
// into the domain error the handler layer maps to 404.
func (s *exportControlService) loadRequest(
	ctx context.Context, tenantID uint64, requestID string,
) (*types.ExportRequest, error) {
	req, err := s.repo.GetRequest(ctx, tenantID, requestID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperrors.NewNotFoundError("export request not found")
		}
		return nil, err
	}
	return req, nil
}

// logAudit writes one export-control audit event; audit failures never block
// the business write (same policy as the other verticals).
func (s *exportControlService) logAudit(
	ctx context.Context, tenantID uint64, actorID, action, requestID string,
	details map[string]interface{},
) {
	if s.audit == nil {
		return
	}
	if err := s.audit.Log(ctx, &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: actorID,
		Action:      types.AuditAction(action),
		ScopeType:   "export",
		TargetType:  "export_request",
		TargetID:    requestID,
		Outcome:     types.AuditOutcomeSuccess,
		Details:     exportDetailsJSON(details),
	}); err != nil {
		logger.Warnf(ctx, "export audit log failed for request %s: %v", requestID, err)
	}
}
