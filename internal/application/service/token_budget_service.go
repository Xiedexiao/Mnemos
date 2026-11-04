package service

import (
	"context"
	"encoding/json"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// tokenBudgetService implements interfaces.TokenBudgetService
// (KnowAxis V3.0 §16.5 成本控制 / 模型降级链).
//
// 两条线共用这个服务是因为它们回答同一个运维问题：**花得起吗**。预算回答
// "这个月还剩多少"，降级链回答"主力不可用时用哪个更便宜的替代"。
type tokenBudgetService struct {
	repo interfaces.TokenBudgetRepository
	// audit records budget/chain changes: they decide both spend and which
	// model answers users, so a silent edit is a governance gap.
	audit interfaces.AuditLogService
	now   func() time.Time
}

// NewTokenBudgetService constructs the service.
func NewTokenBudgetService(
	repo interfaces.TokenBudgetRepository,
	audit interfaces.AuditLogService,
) interfaces.TokenBudgetService {
	return &tokenBudgetService{repo: repo, audit: audit, now: time.Now}
}

// 审计动作。
const (
	auditActionTokenBudgetSaved   = "token_budget.saved"
	auditActionFallbackChainSaved = "token_budget.fallback_chain_saved"
)

// GetBudget returns the tenant's budget; an unset budget is a zero value with
// the default period rather than an error.
func (s *tokenBudgetService) GetBudget(
	ctx context.Context, tenantID uint64,
) (*types.TokenBudget, error) {
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	budget, err := s.repo.GetBudget(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if budget == nil {
		// 未配置不是错误：调用方拿到的是一份"没有上限"的默认值，
		// 与 AssessTokenBudget 对 0 上限的处理保持一致。
		return &types.TokenBudget{
			TenantID:    tenantID,
			Period:      types.TokenBudgetMonthly,
			WarnPercent: types.DefaultTokenBudgetWarnPercent,
		}, nil
	}
	return budget, nil
}

// SaveBudget validates and stores the budget.
func (s *tokenBudgetService) SaveBudget(
	ctx context.Context, tenantID uint64, budget types.TokenBudget,
) (*types.TokenBudget, error) {
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	budget.TenantID = tenantID
	normalized, err := types.NormalizeTokenBudget(budget)
	if err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	if err := s.repo.SaveBudget(ctx, &normalized); err != nil {
		return nil, err
	}
	s.logAudit(ctx, tenantID, auditActionTokenBudgetSaved, "", map[string]interface{}{
		"period":       string(normalized.Period),
		"limit_tokens": normalized.LimitTokens,
		"warn_percent": normalized.WarnPercent,
	})
	return &normalized, nil
}

// Check assesses the current window's spend against the budget.
//
// 跨档时先把新档位写进通知台账再返回 should_notify：反过来的话，通知发出
// 而写库失败会导致下一轮重复通知。
func (s *tokenBudgetService) Check(
	ctx context.Context, tenantID uint64,
) (*types.TokenBudgetAssessment, error) {
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	budget, err := s.GetBudget(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	windowStart := types.TokenBudgetWindowStart(budget.Period, s.now())
	used, err := s.repo.SumUsageTokens(ctx, tenantID, windowStart)
	if err != nil {
		return nil, err
	}
	previous, err := s.repo.LoadNotifiedStatus(ctx, tenantID, budget.Period, windowStart)
	if err != nil {
		// 读不到上次档位时按"没报过"处理：宁可多报一次，也不要因为台账
		// 不可读而漏掉超预算这件事。
		previous = ""
	}
	assessment := types.AssessTokenBudget(*budget, used, previous)
	if assessment.ShouldNotify {
		if saveErr := s.repo.SaveNotifiedStatus(
			ctx, tenantID, budget.Period, windowStart, assessment.Status,
		); saveErr != nil {
			return nil, saveErr
		}
	}
	return &assessment, nil
}

// ListFallbackChains returns every configured chain.
func (s *tokenBudgetService) ListFallbackChains(
	ctx context.Context, tenantID uint64,
) ([]*types.ModelFallbackChain, error) {
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	chains, err := s.repo.ListFallbackChains(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if chains == nil {
		chains = []*types.ModelFallbackChain{}
	}
	return chains, nil
}

// SaveFallbackChain validates and stores one scene's chain.
func (s *tokenBudgetService) SaveFallbackChain(
	ctx context.Context, tenantID uint64, chain types.ModelFallbackChain,
) (*types.ModelFallbackChain, error) {
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	chain.TenantID = tenantID
	normalized, err := types.NormalizeModelFallbackChain(chain)
	if err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	if err := s.repo.SaveFallbackChain(ctx, &normalized); err != nil {
		return nil, err
	}
	s.logAudit(ctx, tenantID, auditActionFallbackChainSaved, normalized.Scene,
		map[string]interface{}{
			"scene":     normalized.Scene,
			"model_ids": normalized.ModelIDs,
			"primary":   normalized.PrimaryModelID(),
		})
	return &normalized, nil
}

// DeleteFallbackChain removes one scene's chain.
func (s *tokenBudgetService) DeleteFallbackChain(
	ctx context.Context, tenantID uint64, scene string,
) error {
	if tenantID == 0 {
		return apperrors.NewUnauthorizedError("tenant context is required")
	}
	if scene == "" {
		return apperrors.NewBadRequestError("scene is required")
	}
	return s.repo.DeleteFallbackChain(ctx, tenantID, scene)
}

// Candidates returns the models to try for one scene.
//
// 查询失败时回退到"只用主模型"：降级链是可用性增强项，配置读不到不该让
// 一次原本能成功的调用失败。
func (s *tokenBudgetService) Candidates(
	ctx context.Context, tenantID uint64, scene, primaryModelID string,
) []string {
	if tenantID == 0 {
		return types.ModelFallbackChain{}.CandidatesFor(primaryModelID)
	}
	chain, err := s.repo.GetFallbackChain(ctx, tenantID, scene)
	if err != nil || chain == nil {
		return types.ModelFallbackChain{}.CandidatesFor(primaryModelID)
	}
	return chain.CandidatesFor(primaryModelID)
}

// logAudit writes one budget/chain audit event.
func (s *tokenBudgetService) logAudit(
	ctx context.Context, tenantID uint64, action, targetID string,
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
		ActorUserID: types.CallerFromContext(ctx).UserID,
		Action:      types.AuditAction(action),
		ScopeType:   "token_budget",
		TargetType:  "token_budget",
		TargetID:    targetID,
		Outcome:     "success",
		Details:     types.JSON(payload),
	})
}

// compile-time guard.
var _ interfaces.TokenBudgetService = (*tokenBudgetService)(nil)
