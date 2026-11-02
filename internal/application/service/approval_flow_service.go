package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// approvalFlowService manages per-space approval routing configuration
// (KnowAxis V3.0 §6.3).
//
// v1 shipped the configuration table read-only: no tenant could ever enable
// dual approval, set an SLA, or express a conditional route. This service is
// the missing write path, plus the evaluator that turns "content risk ×
// impact × audience" into an actual approval chain.
type approvalFlowService struct {
	flows interfaces.ApprovalFlowRepository
	// kbs validates that the target space exists in the caller's tenant.
	kbs interfaces.KnowledgeBaseRepository
	// chain persists the node definitions of a flow (§6.3 逐级/会签/或签).
	chain interfaces.ApprovalChainRepository
}

// NewApprovalFlowService constructs the approval-flow service.
func NewApprovalFlowService(
	flows interfaces.ApprovalFlowRepository,
	kbs interfaces.KnowledgeBaseRepository,
	chain interfaces.ApprovalChainRepository,
) interfaces.ApprovalFlowService {
	return &approvalFlowService{flows: flows, kbs: kbs, chain: chain}
}

// approvalFlowDefaultSLAHours mirrors reviewDefaultSLA for configured flows.
const approvalFlowDefaultSLAHours = 72

// Get returns the configuration of one space, or the single-mode default.
func (s *approvalFlowService) Get(
	ctx context.Context, tenantID uint64, kbID string,
) (*types.ApprovalFlow, error) {
	flow, err := s.flows.GetByKB(ctx, tenantID, kbID)
	if err != nil {
		// 无配置是合法状态：服务层按 single 兜底，而不是报错。
		return defaultApprovalFlow(tenantID, kbID), nil
	}
	flow.RiskRules = nil // 兼容列不回显，避免两个 SLA 来源互相矛盾
	return flow, nil
}

// List returns every configured space in the tenant.
func (s *approvalFlowService) List(
	ctx context.Context, tenantID uint64, limit int,
) ([]*types.ApprovalFlow, error) {
	flows, err := s.flows.ListByTenant(ctx, tenantID, limit)
	if err != nil {
		return nil, err
	}
	for _, flow := range flows {
		if flow != nil {
			flow.RiskRules = nil
		}
	}
	return flows, nil
}

// Save creates or updates one space's configuration, including its node chain.
//
// The node list is replaced wholesale rather than diffed: an approval flow is a
// small, fully-authored definition, and a partial update would let a caller
// produce a chain that cannot be instantiated (e.g. a 会签 node with no
// approvers). In-flight tasks are unaffected because they snapshot their own
// nodes at submission.
func (s *approvalFlowService) Save(
	ctx context.Context, flow *types.ApprovalFlow, nodes []*types.ApprovalFlowNode,
) (*types.ApprovalFlow, error) {
	if flow == nil || flow.TenantID == 0 || flow.KBID == "" {
		return nil, apperrors.NewBadRequestError("kb_id is required")
	}
	if err := validateApprovalFlow(flow); err != nil {
		return nil, err
	}
	if s.kbs != nil {
		if _, err := s.kbs.GetKnowledgeBaseByIDAndTenant(ctx, flow.KBID, flow.TenantID); err != nil {
			return nil, apperrors.NewNotFoundError("knowledge base not found")
		}
	}
	if flow.Mode == "" {
		flow.Mode = string(types.ApprovalModeSingle)
	}
	if flow.SLAHours <= 0 {
		flow.SLAHours = approvalFlowDefaultSLAHours
	}
	if flow.EscalateRole == "" {
		flow.EscalateRole = string(types.TenantRoleAdmin)
	}
	saved, err := s.flows.Upsert(ctx, flow)
	if err != nil {
		return nil, err
	}
	if s.chain != nil {
		if err := s.chain.ReplaceFlowNodes(ctx, flow.TenantID, saved.ID, nodes); err != nil {
			return nil, err
		}
	}
	saved.RiskRules = nil
	return saved, nil
}

// ListNodes returns a flow's node definitions.
func (s *approvalFlowService) ListNodes(
	ctx context.Context, tenantID uint64, kbID string,
) ([]*types.ApprovalFlowNode, error) {
	if s.chain == nil {
		return nil, nil
	}
	flow, err := s.flows.GetByKB(ctx, tenantID, kbID)
	if err != nil || flow == nil {
		return nil, nil
	}
	return s.chain.ListFlowNodes(ctx, tenantID, flow.ID)
}

// Delete removes a space's configuration so it falls back to single approval.
func (s *approvalFlowService) Delete(ctx context.Context, tenantID uint64, kbID string) error {
	if kbID == "" {
		return apperrors.NewBadRequestError("kb_id is required")
	}
	return s.flows.Delete(ctx, tenantID, kbID)
}

// Resolve decides the approval chain for one submission (§6.3/§6.4).
//
// The rules are applied as a floor: the risk level computed from the content
// can only be raised by configuration, never lowered, because the author must
// not be able to reduce their own approval strength. A dual requirement is
// sticky once any rule asks for it.
func (s *approvalFlowService) Resolve(
	ctx context.Context, tenantID uint64, kbID string, contentRisk types.ApprovalRiskLevel,
) (*interfaces.ApprovalResolution, error) {
	flow, err := s.flows.GetByKB(ctx, tenantID, kbID)
	if err != nil {
		flow = defaultApprovalFlow(tenantID, kbID)
	}
	return ResolveApprovalChain(flow, contentRisk), nil
}

// ResolveApprovalChain is the pure evaluator behind Resolve, exported for the
// review service and for tests.
func ResolveApprovalChain(
	flow *types.ApprovalFlow, contentRisk types.ApprovalRiskLevel,
) *interfaces.ApprovalResolution {
	resolution := &interfaces.ApprovalResolution{
		RiskLevel: contentRisk,
		Mode:      types.ApprovalModeSingle,
	}
	if flow == nil {
		// 无配置不等于无约束：§6.3 的"高风险不得单审"是平台级底线，不依赖
		// 租户是否配过审批流。这里必须把 high 折算成双人，否则没有配置的
		// 租户里所有高风险动作都会走单人审批。
		if contentRisk == types.ApprovalRiskHigh {
			resolution.Mode = types.ApprovalModeDual
			resolution.Reasons = append(resolution.Reasons, "high_risk_requires_dual")
		}
		resolution.Quorum = approvalQuorumFor(resolution.Mode, contentRisk)
		return resolution
	}
	resolution.FlowVersion = flow.Version
	rules := types.ParseApprovalRoutingRules(flow.Rules)

	// 1) 配置只能抬高审批强度。
	if raiseApprovalRisk(resolution.RiskLevel, rules.MinRiskLevel) {
		resolution.RiskLevel = types.ApprovalRiskLevel(rules.MinRiskLevel)
		resolution.Reasons = append(resolution.Reasons, "min_risk_level")
	}

	// 2) 密级路由覆盖（显式配置优先于通用模式）。
	//    ClassificationRouting 的键是密级值，值是要采用的审批模式。
	if len(rules.ClassificationRouting) > 0 {
		// 密级本身已经折算进 contentRisk；这里只处理"这个 KB 里该密级的
		// 内容必须双审"这类空间级约束，按风险级反查最接近的键。
		for classification, mode := range rules.ClassificationRouting {
			if strings.EqualFold(classification, string(resolution.RiskLevel)) ||
				strings.EqualFold(classification, string(contentRisk)) {
				if mode == string(types.ApprovalModeDual) {
					resolution.Mode = types.ApprovalModeDual
					resolution.Reasons = append(resolution.Reasons, "classification_routing")
				}
			}
		}
	}

	// 3) 高风险不得单审（§6.3：高风险不得单审，跨部门需双 Owner 会签）。
	if resolution.RiskLevel == types.ApprovalRiskHigh {
		resolution.Mode = types.ApprovalModeDual
		resolution.Reasons = append(resolution.Reasons, "high_risk_requires_dual")
	}

	// 4) ForceDualAbove：达到该级别强制双人。
	if rules.ForceDualAbove != "" && riskAtLeast(resolution.RiskLevel, rules.ForceDualAbove) {
		resolution.Mode = types.ApprovalModeDual
		resolution.Reasons = append(resolution.Reasons, "force_dual_above")
	}

	// 5) 空间默认模式（最低优先级：只有以上都没要求双审时才生效）。
	if flow.Mode == string(types.ApprovalModeDual) {
		resolution.Mode = types.ApprovalModeDual
		resolution.Reasons = append(resolution.Reasons, "space_default_mode")
	}

	resolution.RequireDistinctReviewerRoles = rules.RequireDistinctReviewerRoles
	resolution.Quorum = approvalQuorumFor(resolution.Mode, resolution.RiskLevel)
	return resolution
}

// approvalQuorumFor returns how many distinct approvers the chain needs.
func approvalQuorumFor(mode types.ApprovalFlowMode, risk types.ApprovalRiskLevel) int {
	if mode == types.ApprovalModeDual || risk == types.ApprovalRiskHigh {
		return 2
	}
	return 1
}

// raiseApprovalRisk reports whether a configured floor is stricter than the
// level computed from the content.
func raiseApprovalRisk(current types.ApprovalRiskLevel, floor string) bool {
	return riskAtLeast(types.ApprovalRiskLevel(floor), string(current)) &&
		riskRank(types.ApprovalRiskLevel(floor)) > riskRank(current)
}

// riskRank orders the three risk levels so floors can be compared.
func riskRank(level types.ApprovalRiskLevel) int {
	switch level {
	case types.ApprovalRiskLow:
		return 1
	case types.ApprovalRiskMid:
		return 2
	case types.ApprovalRiskHigh:
		return 3
	default:
		return 0
	}
}

// riskAtLeast reports whether level is at least floor. An empty or unknown
// floor never raises anything.
func riskAtLeast(level types.ApprovalRiskLevel, floor string) bool {
	if floor == "" {
		return false
	}
	return riskRank(level) >= riskRank(types.ApprovalRiskLevel(floor))
}

// validApprovalRisk reports whether the value is a known risk level.
func validApprovalRisk(level string) bool {
	return riskRank(types.ApprovalRiskLevel(level)) > 0
}

// defaultApprovalFlow is the single-mode configuration used when a space has
// none: single approval, default SLA, escalate to admin.
func defaultApprovalFlow(tenantID uint64, kbID string) *types.ApprovalFlow {
	return &types.ApprovalFlow{
		TenantID:      tenantID,
		KBID:          kbID,
		Mode:          string(types.ApprovalModeSingle),
		Version:       0,
		Rules:         types.JSON("{}"),
		SLAHours:      approvalFlowDefaultSLAHours,
		ReminderHours: 24,
		EscalateRole:  string(types.TenantRoleAdmin),
		Enabled:       true,
	}
}

// validateApprovalFlow rejects configurations that would silently weaken the
// gate: an unknown mode, an SLA of zero, or a rules blob that does not parse.
func validateApprovalFlow(flow *types.ApprovalFlow) error {
	if flow.Mode != "" {
		mode := types.ApprovalFlowMode(flow.Mode)
		if mode != types.ApprovalModeSingle && mode != types.ApprovalModeDual {
			return apperrors.NewBadRequestError(
				fmt.Sprintf("mode must be %s or %s", types.ApprovalModeSingle, types.ApprovalModeDual))
		}
	}
	if flow.SLAHours < 0 {
		return apperrors.NewBadRequestError("sla_hours must not be negative")
	}
	if flow.ReminderHours < 0 {
		return apperrors.NewBadRequestError("reminder_hours must not be negative")
	}
	if flow.EscalateRole != "" {
		if !types.TenantRole(flow.EscalateRole).IsValid() {
			return apperrors.NewBadRequestError("escalate_role is not a known tenant role")
		}
	}
	if len(flow.Rules) > 0 && string(flow.Rules) != "{}" {
		var probe types.ApprovalRoutingRules
		if err := json.Unmarshal(flow.Rules, &probe); err != nil {
			return apperrors.NewBadRequestError("rules is not a valid routing configuration")
		}
		if probe.MinRiskLevel != "" && !validApprovalRisk(probe.MinRiskLevel) {
			return apperrors.NewBadRequestError("min_risk_level must be low, mid or high")
		}
		if probe.ForceDualAbove != "" && !validApprovalRisk(probe.ForceDualAbove) {
			return apperrors.NewBadRequestError("force_dual_above must be low, mid or high")
		}
	}
	return nil
}
