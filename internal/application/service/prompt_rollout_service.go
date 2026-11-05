package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// promptRolloutService implements interfaces.PromptRolloutService
// (KnowAxis V3.0 §16.5 灰度与回滚).
//
// 与 ModelGatewayService 的关系：那边管**版本历史**（append-only），这里管
// **现在用哪一版**。分开是因为两者的不变性要求不同——历史必须不可变，指针
// 必须可变，混在一张表里就必然有一方被破坏。
type promptRolloutService struct {
	repo  interfaces.PromptRolloutRepository
	audit interfaces.AuditLogService
	now   func() time.Time
}

// NewPromptRolloutService constructs the service.
func NewPromptRolloutService(
	repo interfaces.PromptRolloutRepository,
	audit interfaces.AuditLogService,
) interfaces.PromptRolloutService {
	return &promptRolloutService{repo: repo, audit: audit, now: time.Now}
}

// 审计动作。
const (
	auditActionPromptRolloutConfigured = "model_gateway.prompt_rollout_configured"
	auditActionPromptRollback          = "model_gateway.prompt_rolled_back"
)

// Configure sets the stable version and optional canary split.
func (s *promptRolloutService) Configure(
	ctx context.Context, tenantID uint64, actorID string,
	pointer types.PromptVersionPointer,
) (*types.PromptVersionPointer, error) {
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	pointer.TenantID = tenantID
	pointer.TemplateKey = strings.TrimSpace(pointer.TemplateKey)
	pointer.UpdatedBy = actorID
	if err := types.ValidatePromptVersionPointer(pointer); err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	// 两个版本都必须真实存在：指向一个不存在的版本会让取数时静默回退到最新，
	// 而运营以为自己配置了一个灰度。
	if err := s.assertVersionExists(ctx, tenantID, pointer.TemplateKey, pointer.StableVersion); err != nil {
		return nil, err
	}
	if pointer.CanaryVersion > 0 {
		if err := s.assertVersionExists(ctx, tenantID, pointer.TemplateKey, pointer.CanaryVersion); err != nil {
			return nil, err
		}
	}
	if err := s.repo.SavePointer(ctx, &pointer); err != nil {
		return nil, err
	}
	s.logAudit(ctx, tenantID, actorID, auditActionPromptRolloutConfigured,
		pointer.TemplateKey, map[string]interface{}{
			"stable_version": pointer.StableVersion,
			"canary_version": pointer.CanaryVersion,
			"canary_percent": pointer.CanaryPercent,
		})
	return &pointer, nil
}

// Rollback points the stable version back at an existing version.
//
// 回滚是**改指针**，不是删版本、也不是把旧内容复制成新版本：后者会掩盖
// "曾经上线过一个坏版本"这个事实，而复盘需要的正是它。
func (s *promptRolloutService) Rollback(
	ctx context.Context, tenantID uint64, actorID string,
	request types.PromptRollbackRequest,
) (*types.PromptVersionPointer, error) {
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	request.TemplateKey = strings.TrimSpace(request.TemplateKey)
	if err := types.ValidatePromptRollback(request); err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	if err := s.assertVersionExists(ctx, tenantID, request.TemplateKey, request.ToVersion); err != nil {
		return nil, err
	}
	current, err := s.repo.GetPointer(ctx, tenantID, request.TemplateKey)
	if err != nil {
		return nil, err
	}
	previous := 0
	pointer := &types.PromptVersionPointer{
		TenantID: tenantID, TemplateKey: request.TemplateKey,
		StableVersion: request.ToVersion, UpdatedBy: actorID,
	}
	if current != nil {
		previous = current.StableVersion
	}
	// 回滚同时清掉灰度：把流量继续分给"刚被判定有问题"的灰度版是自相矛盾的。
	pointer.CanaryVersion = 0
	pointer.CanaryPercent = 0
	if err := s.repo.SavePointer(ctx, pointer); err != nil {
		return nil, err
	}
	message := types.DescribePromptRollback(request.TemplateKey, previous, request.ToVersion, request.Note)
	s.logAudit(ctx, tenantID, actorID, auditActionPromptRollback,
		request.TemplateKey, map[string]interface{}{
			"from_version": previous,
			"to_version":   request.ToVersion,
			"note":         request.Note,
			"summary":      message,
		})
	return pointer, nil
}

// Select resolves which version one subject sees.
//
// 无指针时返回零版本 + 说明"用最新版本"：调用方据此走原有路径，行为与
// 改动前完全一致。
func (s *promptRolloutService) Select(
	ctx context.Context, tenantID uint64, templateKey, subjectKey string,
) (*types.PromptVersionSelection, error) {
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	pointer, err := s.repo.GetPointer(ctx, tenantID, strings.TrimSpace(templateKey))
	if err != nil {
		return nil, err
	}
	if pointer == nil {
		return &types.PromptVersionSelection{Reason: "no pointer: use the latest version"}, nil
	}
	selection := types.SelectPromptVersion(*pointer, subjectKey)
	return &selection, nil
}

// Get returns the pointer, zero-valued when unset.
func (s *promptRolloutService) Get(
	ctx context.Context, tenantID uint64, templateKey string,
) (*types.PromptVersionPointer, error) {
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	pointer, err := s.repo.GetPointer(ctx, tenantID, strings.TrimSpace(templateKey))
	if err != nil {
		return nil, err
	}
	if pointer == nil {
		return &types.PromptVersionPointer{TenantID: tenantID, TemplateKey: templateKey}, nil
	}
	return pointer, nil
}

// assertVersionExists refuses a pointer to a version that is not there.
func (s *promptRolloutService) assertVersionExists(
	ctx context.Context, tenantID uint64, templateKey string, version int,
) error {
	row, err := s.repo.GetVersion(ctx, tenantID, templateKey, version)
	if err != nil {
		return err
	}
	if row == nil {
		return apperrors.NewNotFoundError(
			"prompt version " + templateKey + "@v" + itoaSafe(version) + " does not exist")
	}
	return nil
}

// itoaSafe renders a non-negative int without importing strconv here.
func itoaSafe(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	pos := len(digits)
	for value > 0 {
		pos--
		digits[pos] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[pos:])
}

// logAudit writes one rollout audit event.
func (s *promptRolloutService) logAudit(
	ctx context.Context, tenantID uint64, actorID, action, targetID string,
	details map[string]interface{},
) {
	if s.audit == nil {
		return
	}
	payload, err := json.Marshal(details)
	if err != nil {
		payload = []byte("{}")
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: actorID,
		Action:      types.AuditAction(action),
		ScopeType:   "model_gateway",
		TargetType:  "prompt_template",
		TargetID:    targetID,
		Outcome:     "success",
		Details:     types.JSON(payload),
	})
}

// compile-time guard.
var _ interfaces.PromptRolloutService = (*promptRolloutService)(nil)
