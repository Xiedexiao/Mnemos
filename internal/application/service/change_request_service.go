package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
)

// 变更单业务哨兵错误。handler 层据此映射 HTTP 语义（404/403/409/400），
// 与 review / tenant_member 等服务的错误风格一致。
var (
	// ErrCRTenantMissing 请求上下文缺少租户（中间件层未注入）。
	ErrCRTenantMissing = errors.New("change request: tenant context is missing")
	// ErrCRActorMissing 调用者缺少用户身份（变更单是治理动作，必须可归因）。
	ErrCRActorMissing = errors.New("change request: caller identity is missing")
	// ErrCRNotFound 变更单不存在（或不在本租户）。
	ErrCRNotFound = errors.New("change request: change request not found")
	// ErrCRItemNotFound 变更条目不存在。
	ErrCRItemNotFound = errors.New("change request: change item not found")
	// ErrCRImpactTaskNotFound 影响任务不存在（或不在本租户）。
	ErrCRImpactTaskNotFound = errors.New("change request: impact task not found")
	// ErrCRItemAlreadyAdded 同一知识重复加入同一张变更单。
	ErrCRItemAlreadyAdded = errors.New("change request: knowledge is already in this change request")
	// ErrCRNotEditable 条目只允许在 draft/analyzing 阶段增删。
	ErrCRNotEditable = errors.New("change request: items can only be modified while the change request is draft or analyzing")
	// ErrCRInvalidState 状态机不允许该动作（含 Close 门禁之外的所有非法迁移）。
	ErrCRInvalidState = errors.New("change request: status does not allow this action")
	// ErrCRKnowledgeNotFound 变更条目引用的知识不存在（或不在本租户）。
	ErrCRKnowledgeNotFound = errors.New("change request: knowledge not found")
	// ErrCRNoItems 变更单没有任何条目（分析/发布的前置）。
	ErrCRNoItems = errors.New("change request: no change items")
	// ErrCRNotAnalyzed 未做影响分析就提交审批：审批人看不到条目与影响清单，
	// 等于盲签。
	ErrCRNotAnalyzed = errors.New("change request: run the impact analysis before submitting for approval")
	// ErrCRNotApproved 变更单尚未获批，不能排期或发布（§7.2）。
	ErrCRNotApproved = errors.New("change request: the change request has not been approved")
	// ErrCRVerificationRequired 验证结论为空：关闭门禁条件②要求显式填写。
	ErrCRVerificationRequired = errors.New("change request: a verification result is required")
	// ErrCRTitleRequired 标题必填。
	ErrCRTitleRequired = errors.New("change request: title is required")
	// ErrCRInvalidReason reason 不在白名单。
	ErrCRInvalidReason = errors.New("change request: unknown change reason")
	// ErrCRInvalidChangeType change_type 不在白名单。
	ErrCRInvalidChangeType = errors.New("change request: unknown change type")
	// ErrCRInvalidRiskLevel risk_level 不在白名单。
	ErrCRInvalidRiskLevel = errors.New("change request: unknown risk level")
	// ErrCRInvalidTaskStatus 影响任务状态必须是 open/done/waived。
	ErrCRInvalidTaskStatus = errors.New("change request: impact task status must be open, done or waived")
)

const (
	// changeRequestAuditScope 是变更单审计事件的 scope_type。
	changeRequestAuditScope = "change_request"
	// changeRequestNumberAttempts 单号生成的重试次数：以"当日序号计数 + 1"为
	// 基数，并发创建撞唯一约束时递增重试。
	changeRequestNumberAttempts = 5
	// changeRequestNeighborLimit 单个条目可产出的"同 KB 近邻"任务上限，
	// 防止超大规模同标签簇把影响分析变成任务风暴（仓储侧还有硬上限）。
	changeRequestNeighborLimit = 20
)

// changeRequestService implements interfaces.ChangeRequestService
// （KnowAxis V3.0 §7.2/§7.6 v1）。发布通过 lifecycle.Transition 推进每个
// 条目的生命周期，让批量变更的每一步状态变化都走既有状态机与审计。
type changeRequestService struct {
	crs        interfaces.ChangeRequestRepository
	knowledges interfaces.KnowledgeRepository
	lifecycle  interfaces.KnowledgeLifecycleService
	audit      interfaces.AuditLogService
	// governance routes the change request through the §6.2 approval flow so
	// Schedule can require a receipt (V3.0 §7.2 审批). Optional: nil keeps the
	// pre-existing "Admin drives it by hand" behaviour.
	governance interfaces.GovernanceApprovalService
	// versions supplies the published-version lookup needed by replacement-type
	// publishes and by the close gate's "old version is gone" check.
	versions interfaces.KnowledgeVersionRepository
	// relations is the dependency graph the impact analysis walks (§7.6).
	// Optional: nil disables downstream expansion (only the change items
	// themselves become tasks).
	relations interfaces.KnowledgeRelationService
	now       func() time.Time
}

// NewChangeRequestService constructs the production change request service.
func NewChangeRequestService(
	crs interfaces.ChangeRequestRepository,
	knowledges interfaces.KnowledgeRepository,
	lifecycle interfaces.KnowledgeLifecycleService,
	audit interfaces.AuditLogService,
	governance interfaces.GovernanceApprovalService,
	versions interfaces.KnowledgeVersionRepository,
	relations interfaces.KnowledgeRelationService,
) interfaces.ChangeRequestService {
	return &changeRequestService{
		crs:        crs,
		knowledges: knowledges,
		lifecycle:  lifecycle,
		audit:      audit,
		governance: governance,
		versions:   versions,
		relations:  relations,
		now:        time.Now,
	}
}

// CreateCR 创建变更单（draft 状态）：校验白名单字段、生成租户内唯一单号
// CR-YYYYMMDD-序号，冻结初始 baseline 与 close_conditions。
func (s *changeRequestService) CreateCR(
	ctx context.Context, input types.ChangeRequestCreateInput,
) (*types.ChangeRequest, error) {
	tenantID, err := changeRequestTenantFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if input.CreatedBy == "" {
		input.CreatedBy = types.CallerFromContext(ctx).UserID
	}
	if input.CreatedBy == "" {
		return nil, ErrCRActorMissing
	}
	if strings.TrimSpace(input.Title) == "" {
		return nil, ErrCRTitleRequired
	}
	if !input.Reason.Valid() {
		return nil, ErrCRInvalidReason
	}
	if !input.ChangeType.Valid() {
		return nil, ErrCRInvalidChangeType
	}
	if input.RiskLevel == "" {
		input.RiskLevel = types.ChangeRiskLow
	}
	if !input.RiskLevel.Valid() {
		return nil, ErrCRInvalidRiskLevel
	}

	now := s.now()
	cr := &types.ChangeRequest{
		ID:           uuid.NewString(),
		TenantID:     tenantID,
		Title:        strings.TrimSpace(input.Title),
		Reason:       string(input.Reason),
		ChangeType:   string(input.ChangeType),
		Status:       string(types.ChangeRequestStatusDraft),
		RiskLevel:    string(input.RiskLevel),
		RollbackPlan: input.RollbackPlan,
		EffectiveAt:  input.EffectiveAt,
		AnnounceAt:   input.AnnounceAt,
		ExpiresAt:    input.ExpiresAt,
		CreatedBy:    input.CreatedBy,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	baselineRaw, err := json.Marshal(types.ChangeRequestBaseline{
		KnowledgeIDs: []string{},
		Note:         input.Note,
	})
	if err != nil {
		return nil, fmt.Errorf("change request: marshal baseline: %w", err)
	}
	cr.Baseline = types.JSON(baselineRaw)
	if input.CloseConditions == nil {
		input.CloseConditions = []types.ChangeCloseCondition{}
	}
	conditionsRaw, err := json.Marshal(input.CloseConditions)
	if err != nil {
		return nil, fmt.Errorf("change request: marshal close_conditions: %w", err)
	}
	cr.CloseConditions = types.JSON(conditionsRaw)

	// 单号：CR-YYYYMMDD-序号。基数取"本租户当日已有单数 + 1"；并发创建撞
	// UNIQUE(tenant_id, cr_number) 时递增重试（序号空洞无害，唯一性才是硬约束）。
	for attempt := 0; attempt < changeRequestNumberAttempts; attempt++ {
		count, err := s.crs.CountByNumberPrefix(ctx, tenantID, changeRequestNumberPrefix(now))
		if err != nil {
			return nil, err
		}
		cr.CRNumber = fmt.Sprintf("%s-%04d", changeRequestNumberPrefix(now), count+int64(attempt)+1)
		err = s.crs.Create(ctx, cr)
		switch {
		case err == nil:
			s.crAuditEvent(ctx, cr, types.AuditActionCRCreated, types.AuditOutcomeSuccess, map[string]any{
				"cr_number":   cr.CRNumber,
				"title":       cr.Title,
				"reason":      cr.Reason,
				"change_type": cr.ChangeType,
				"risk_level":  cr.RiskLevel,
				"created_by":  cr.CreatedBy,
			})
			return cr, nil
		case repository.IsDuplicateKeyError(err):
			continue
		default:
			return nil, err
		}
	}
	return nil, fmt.Errorf(
		"change request: failed to allocate a unique cr_number after %d attempts", changeRequestNumberAttempts,
	)
}

// AddItem 追加变更条目。仅 draft/analyzing 可改；知识必须存在且属于本租户；
// (cr_id, knowledge_id) 幂等防重。
func (s *changeRequestService) AddItem(ctx context.Context, crID string, knowledgeID string) (*types.ChangeRequest, error) {
	tenantID, err := changeRequestTenantFromContext(ctx)
	if err != nil {
		return nil, err
	}
	cr, err := s.loadCR(ctx, tenantID, crID)
	if err != nil {
		return nil, err
	}
	if !changeRequestItemsEditable(cr.Status) {
		return nil, ErrCRNotEditable
	}
	if knowledgeID == "" {
		return nil, ErrCRKnowledgeNotFound
	}
	knowledge, err := s.knowledges.GetKnowledgeByID(ctx, tenantID, knowledgeID)
	if err != nil {
		if errors.Is(err, repository.ErrKnowledgeNotFound) {
			return nil, ErrCRKnowledgeNotFound
		}
		return nil, err
	}
	// 幂等预检（并发窗口由 CreateItem 的唯一约束兜底 → ErrChangeItemDuplicate）。
	if _, err := s.crs.GetItemByKnowledge(ctx, cr.ID, knowledgeID); err == nil {
		return nil, ErrCRItemAlreadyAdded
	} else if !errors.Is(err, repository.ErrChangeItemNotFound) {
		return nil, err
	}

	now := s.now()
	item := &types.ChangeItem{
		ID:          uuid.NewString(),
		CRID:        cr.ID,
		KnowledgeID: knowledge.ID,
		ItemStatus:  string(types.ChangeItemStatusPending),
		CreatedAt:   now,
	}
	if err := s.crs.CreateItem(ctx, item); err != nil {
		if errors.Is(err, repository.ErrChangeItemDuplicate) {
			return nil, ErrCRItemAlreadyAdded
		}
		return nil, err
	}

	details := map[string]any{
		"knowledge_id": knowledge.ID,
		"item_id":      item.ID,
		"cr_number":    cr.CRNumber,
	}
	s.crAuditEvent(ctx, cr, types.AuditActionCRItemAdded, types.AuditOutcomeAccepted, details)
	// 同步到知识所在 KB 的活动流，让 KB 维度的运维也能看到波及登记。
	s.recordKnowledgeActivity(ctx, tenantID, knowledge.KnowledgeBaseID,
		types.AuditActionCRItemAdded, cr, types.AuditOutcomeAccepted, details)
	return s.Get(ctx, cr.ID)
}

// RemoveItem 移除变更条目。仅 draft/analyzing 可改；已产出的影响任务保留
// （分析是快照语义，重新 Analyze 会按剩余条目收敛）。
func (s *changeRequestService) RemoveItem(ctx context.Context, crID string, knowledgeID string) (*types.ChangeRequest, error) {
	tenantID, err := changeRequestTenantFromContext(ctx)
	if err != nil {
		return nil, err
	}
	cr, err := s.loadCR(ctx, tenantID, crID)
	if err != nil {
		return nil, err
	}
	if !changeRequestItemsEditable(cr.Status) {
		return nil, ErrCRNotEditable
	}
	if err := s.crs.DeleteItem(ctx, cr.ID, knowledgeID); err != nil {
		if errors.Is(err, repository.ErrChangeItemNotFound) {
			return nil, ErrCRItemNotFound
		}
		return nil, err
	}

	details := map[string]any{
		"knowledge_id": knowledgeID,
		"cr_number":    cr.CRNumber,
	}
	s.crAuditEvent(ctx, cr, types.AuditActionCRItemRemoved, types.AuditOutcomeSuccess, details)
	if knowledge, err := s.knowledges.GetKnowledgeByID(ctx, tenantID, knowledgeID); err == nil {
		s.recordKnowledgeActivity(ctx, tenantID, knowledge.KnowledgeBaseID,
			types.AuditActionCRItemRemoved, cr, types.AuditOutcomeSuccess, details)
	}
	return s.Get(ctx, cr.ID)
}

// Analyze 影响分析（draft→analyzing）。v1 的"谁引用了它"是简化实现：
//  1. 每个变更条目自身建 1 条 knowledge 类影响任务（priority 按 risk_level
//     路由：high→must_change、mid→suggested、low→fyi）；
//  2. 对每个条目查同 KB 内共享标签的近邻知识（knowledge_tag_relations 联查，
//     TODO(v3.1) 换成 chunk 引文/引用图），为近邻生成波及任务。
//
// 分析幂等：已存在的任务不重复建；条目清单冻结进 baseline。
func (s *changeRequestService) Analyze(ctx context.Context, crID string) (*types.ChangeRequest, error) {
	tenantID, err := changeRequestTenantFromContext(ctx)
	if err != nil {
		return nil, err
	}
	cr, err := s.loadCR(ctx, tenantID, crID)
	if err != nil {
		return nil, err
	}
	if !types.ChangeRequestStatus(cr.Status).CanTransitionTo(types.ChangeRequestStatusAnalyzing) {
		return nil, ErrCRInvalidState
	}
	items, err := s.crs.ListItemsByCR(ctx, []string{cr.ID})
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, ErrCRNoItems
	}

	// 既有任务去重集合：重复 Analyze 时不重建（快照语义，幂等）。
	existingTasks, err := s.crs.ListTasksByCR(ctx, []string{cr.ID})
	if err != nil {
		return nil, err
	}
	tasked := make(map[string]struct{}, len(existingTasks))
	for _, task := range existingTasks {
		if task.TargetType == string(types.ImpactTargetKnowledge) {
			tasked[task.TargetID] = struct{}{}
		}
	}

	priority := string(changeImpactPriorityForRisk(types.ChangeRiskLevel(cr.RiskLevel)))
	created := 0
	knowledgeIDs := make([]string, 0, len(items))
	for _, item := range items {
		knowledge, err := s.knowledges.GetKnowledgeByID(ctx, tenantID, item.KnowledgeID)
		if err != nil {
			if errors.Is(err, repository.ErrKnowledgeNotFound) {
				return nil, ErrCRKnowledgeNotFound
			}
			return nil, err
		}
		knowledgeIDs = append(knowledgeIDs, knowledge.ID)

		// 条目自身的任务。
		if _, seen := tasked[knowledge.ID]; !seen {
			ok, err := s.ensureImpactTask(ctx, cr, knowledge.ID, priority,
				fmt.Sprintf("变更条目（%s / %s）：按变更单应用 lifecycle 动作", cr.CRNumber, cr.ChangeType))
			if err != nil {
				return nil, err
			}
			if ok {
				created++
				tasked[knowledge.ID] = struct{}{}
			}
		}

		// 影响扩散（§7.6）：沿知识关联图遍历真实依赖边，而不是"同标签近邻"
		// 近似。关联表记录了"依据/替代/前置/责任"这类方向明确的依赖，正是
		// 影响分析要沿的边；标签共现既会高估（同标签的兄弟条目）也会低估
		// （引用了正文却没打标签的条目）。
		impacts, err := s.collectRelationImpacts(ctx, tenantID, knowledge)
		if err != nil {
			return nil, err
		}
		for _, impact := range impacts {
			if _, seen := tasked[impact.knowledgeID]; seen {
				continue
			}
			ok, err := s.ensureImpactTask(ctx, cr, impact.knowledgeID, impact.priority,
				impact.note)
			if err != nil {
				return nil, err
			}
			if ok {
				created++
				tasked[impact.knowledgeID] = struct{}{}
			}
		}
	}

	// 冻结条目清单进 baseline（保留创建时的 Note）。
	var baseline types.ChangeRequestBaseline
	if len(cr.Baseline) > 0 {
		_ = json.Unmarshal(cr.Baseline, &baseline)
	}
	baseline.ItemCount = len(items)
	baseline.KnowledgeIDs = knowledgeIDs
	baselineRaw, err := json.Marshal(baseline)
	if err != nil {
		return nil, fmt.Errorf("change request: marshal baseline: %w", err)
	}
	cr.Baseline = types.JSON(baselineRaw)
	cr.Status = string(types.ChangeRequestStatusAnalyzing)
	cr.UpdatedAt = s.now()
	if err := s.crs.Update(ctx, cr); err != nil {
		return nil, err
	}

	s.crAuditEvent(ctx, cr, types.AuditActionCRAnalyzed, types.AuditOutcomeSuccess, map[string]any{
		"cr_number":     cr.CRNumber,
		"items":         len(items),
		"tasks_created": created,
		"tasks_total":   len(tasked),
		"risk_level":    cr.RiskLevel,
	})
	return s.Get(ctx, cr.ID)
}

// relationImpact is one downstream document affected by changing a source.
type relationImpact struct {
	knowledgeID string
	priority    string
	note        string
}

// changeRequestImpactMaxHops bounds the traversal. Two hops is what the spec's
// examples need (SOP → its policy → the FAQs derived from that policy); a
// deeper walk on a densely linked KB turns one change into the whole corpus.
const changeRequestImpactMaxHops = 2

// relationImpactPriorityFor grades how urgent a downstream edit is, by the
// relation that produced it (§7.6 必须改 / 建议改 / 知会).
//
// 依据与前置是硬依赖：源变了，依赖它的内容就不再成立，属于必须改。替代与
// 补充是软关联：建议改。其余（相关/责任）只知会。
func relationImpactPriorityFor(relationType types.KnowledgeRelationType) types.ImpactTaskPriority {
	switch relationType {
	case types.RelationBasedOn, types.RelationPrerequisite:
		return types.ImpactPriorityMustChange
	case types.RelationReplaces, types.RelationSupplements, types.RelationConflicts:
		return types.ImpactPrioritySuggested
	default:
		return types.ImpactPriorityFYI
	}
}

// collectRelationImpacts walks the knowledge relation graph outward from one
// changed document and returns the downstream documents that depend on it.
//
// 只沿"依赖方向"扩散：当前文档是 reverse 行的 from 时，说明别人依据/前置
// 于它——那才是需要修的对象。当前文档 outbound 依赖别人，不会因为自己变了
// 而让别人失效。
func (s *changeRequestService) collectRelationImpacts(
	ctx context.Context, tenantID uint64, source *types.Knowledge,
) ([]relationImpact, error) {
	if s.relations == nil || source == nil {
		return nil, nil
	}
	type queued struct {
		knowledgeID string
		hops        int
		via         types.KnowledgeRelationType
	}
	seen := map[string]bool{source.ID: true}
	queue := []queued{{knowledgeID: source.ID, hops: 0}}
	var impacts []relationImpact

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current.hops >= changeRequestImpactMaxHops {
			continue
		}
		views, err := s.relations.List(ctx, tenantID, current.knowledgeID)
		if err != nil {
			return nil, err
		}
		for _, view := range views {
			// 只处理"别人依赖我"的方向。
			if view.Outbound {
				continue
			}
			if seen[view.PeerID] {
				continue
			}
			seen[view.PeerID] = true
			relationType := types.KnowledgeRelationType(view.RelationType)
			impacts = append(impacts, relationImpact{
				knowledgeID: view.PeerID,
				priority:    string(relationImpactPriorityFor(relationType)),
				note: fmt.Sprintf("沿关联图谱：%s %s %s，请评估是否需同步修订",
					view.PeerID, relationLabelZH(relationType), current.knowledgeID),
			})
			queue = append(queue, queued{
				knowledgeID: view.PeerID,
				hops:        current.hops + 1,
				via:         relationType,
			})
		}
	}
	return impacts, nil
}

// relationLabelZH renders a relation type for operator-facing task notes.
func relationLabelZH(relationType types.KnowledgeRelationType) string {
	switch relationType {
	case types.RelationBasedOn:
		return "依据"
	case types.RelationReplaces:
		return "替代"
	case types.RelationSupplements:
		return "补充"
	case types.RelationConflicts:
		return "与"
	case types.RelationPrerequisite:
		return "前置"
	case types.RelationResponsible:
		return "责任关联"
	default:
		return "关联"
	}
}

// SubmitForApproval 提交审批（analyzing→in_approval）。
//
// TODO(v3.1): 真实审批对接 —— 复用 A2 ReviewService 的审批任务/回执/哈希链：
// 为变更单建立审批任务（四眼校验基于 CreatedBy vs 审批人，高风险单走 dual），
// 法定人数通过后出回执，再放行 Schedule。v1 占位：直接进入 in_approval，
// SubmitForApproval 提交审批（analyzing→in_approval）。
//
// 这里接的是真实的 §6.2 治理审批：变更单作为一个 subject 进入审批链，
// 四眼与多级链完全复用。此前只改状态位——任何 Admin 都能从 analyzing 一路
// 推到发布，审批形同虚设。
func (s *changeRequestService) SubmitForApproval(ctx context.Context, crID string) (*types.ChangeRequest, error) {
	tenantID, err := changeRequestTenantFromContext(ctx)
	if err != nil {
		return nil, err
	}
	actor := types.CallerFromContext(ctx).UserID
	if actor == "" {
		return nil, ErrCRActorMissing
	}
	cr, err := s.loadCR(ctx, tenantID, crID)
	if err != nil {
		return nil, err
	}
	if !types.ChangeRequestStatus(cr.Status).CanTransitionTo(types.ChangeRequestStatusInApproval) {
		return nil, ErrCRInvalidState
	}
	// 没有影响分析就提交审批等于让审批人盲签：Analyze 之后才有条目清单与
	// 影响任务可供判断。
	if types.ChangeRequestStatus(cr.Status) == types.ChangeRequestStatusDraft {
		return nil, ErrCRNotAnalyzed
	}

	if s.governance != nil {
		snapshot, marshalErr := json.Marshal(map[string]interface{}{
			"cr_number":     cr.CRNumber,
			"change_type":   cr.ChangeType,
			"change_reason": cr.Reason,
			"risk_level":    cr.RiskLevel,
			"effective_at":  cr.EffectiveAt,
		})
		if marshalErr != nil {
			return nil, fmt.Errorf("change request: marshal approval snapshot: %w", marshalErr)
		}
		task, submitErr := s.governance.Submit(ctx, tenantID, actor, &types.GovernanceApprovalRequest{
			SubjectType: types.ApprovalSubjectChangeRequest,
			SubjectID:   cr.ID,
			Title:       fmt.Sprintf("%s · %s", cr.CRNumber, cr.Title),
			Reason:      cr.Reason,
			RiskLevel:   types.ApprovalRiskLevel(cr.RiskLevel),
			Snapshot:    types.JSON(snapshot),
		})
		if submitErr != nil && !errors.Is(submitErr, ErrGovernancePending) {
			return nil, submitErr
		}
		if task != nil {
			cr.ApprovalTaskID = task.ID
		}
	}

	cr.Status = string(types.ChangeRequestStatusInApproval)
	cr.UpdatedAt = s.now()
	if err := s.crs.Update(ctx, cr); err != nil {
		return nil, err
	}
	// accepted：审批未决，与 review.submitted 同语义。
	s.crAuditEvent(ctx, cr, types.AuditActionCRSubmitted, types.AuditOutcomeAccepted, map[string]any{
		"cr_number":   cr.CRNumber,
		"created_by":  cr.CreatedBy,
		"approval_id": cr.ApprovalTaskID,
	})
	return s.Get(ctx, cr.ID)
}

// Schedule 排期（in_approval→scheduled）：设定 effective_at（未传则取当前
// 时刻，即"批准即生效"）。发布仍需显式 Publish，不做定时器自动触发。
func (s *changeRequestService) Schedule(ctx context.Context, crID string, effectiveAt *time.Time) (*types.ChangeRequest, error) {
	tenantID, err := changeRequestTenantFromContext(ctx)
	if err != nil {
		return nil, err
	}
	cr, err := s.loadCR(ctx, tenantID, crID)
	if err != nil {
		return nil, err
	}
	if !types.ChangeRequestStatus(cr.Status).CanTransitionTo(types.ChangeRequestStatusScheduled) {
		return nil, ErrCRInvalidState
	}
	// 排期门禁（§7.2）：变更单必须先获批。同时把回执号刻进单据，后续发布
	// 与审计都以它为凭据。
	if s.governance != nil {
		decision, gateErr := s.governance.RequireApproval(ctx, tenantID,
			types.ApprovalSubjectChangeRequest, cr.ID)
		if gateErr != nil {
			return nil, ErrCRNotApproved
		}
		if decision != nil && decision.ReceiptID != "" {
			cr.ApprovalReceiptID = decision.ReceiptID
			if decision.TaskID != "" {
				cr.ApprovalTaskID = decision.TaskID
			}
		}
	}
	if effectiveAt == nil {
		now := s.now()
		effectiveAt = &now
	}
	cr.EffectiveAt = effectiveAt
	cr.Status = string(types.ChangeRequestStatusScheduled)
	cr.UpdatedAt = s.now()
	if err := s.crs.Update(ctx, cr); err != nil {
		return nil, err
	}
	s.crAuditEvent(ctx, cr, types.AuditActionCRScheduled, types.AuditOutcomeSuccess, map[string]any{
		"cr_number":    cr.CRNumber,
		"effective_at": effectiveAt,
	})
	return s.Get(ctx, cr.ID)
}

// Publish 发布（scheduled→publishing→verifying）。对每个 pending/failed 条目
// 调 lifecycle Transition 并回写 item_status：
//   - v1 所有变更类型统一 mark_stale：replacement/delist 语义上就是"旧内容先
//     失效"；text_fix/content_revision/param_change/restructure 在 v1 没有
//     真实的内容改写管线，同样先标 stale 提示"受变更单波及"。
//     TODO(v3.1): 按类型分发 —— replacement 走 replace（指向新版本）、
//     delist 走 delist、其余走"新版本发布 + 旧版本 replaced"。
//   - 任一失败：item_status=failed，CR 留在 publishing，可再次 Publish 重试
//     （重试只处理 pending/failed 项）；全部成功才进 verifying。
func (s *changeRequestService) Publish(ctx context.Context, crID string) (*types.ChangeRequest, error) {
	tenantID, err := changeRequestTenantFromContext(ctx)
	if err != nil {
		return nil, err
	}
	actor := types.CallerFromContext(ctx).UserID
	if actor == "" {
		return nil, ErrCRActorMissing
	}
	cr, err := s.loadCR(ctx, tenantID, crID)
	if err != nil {
		return nil, err
	}
	status := types.ChangeRequestStatus(cr.Status)
	// scheduled 首次发布；publishing 是上一轮存在失败项后的重试入口。
	if status != types.ChangeRequestStatusScheduled && status != types.ChangeRequestStatusPublishing {
		return nil, ErrCRInvalidState
	}
	if status == types.ChangeRequestStatusScheduled {
		cr.Status = string(types.ChangeRequestStatusPublishing)
		cr.UpdatedAt = s.now()
		if err := s.crs.Update(ctx, cr); err != nil {
			return nil, err
		}
		s.crAuditEvent(ctx, cr, types.AuditActionCRPublishStarted, types.AuditOutcomeAccepted, map[string]any{
			"cr_number": cr.CRNumber,
		})
	}

	items, err := s.crs.ListItemsByCR(ctx, []string{cr.ID})
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, ErrCRNoItems
	}

	changeNote := fmt.Sprintf("change request %s (%s)", cr.CRNumber, cr.ChangeType)
	failures := make([]string, 0)
	applied := 0
	for _, item := range items {
		switch types.ChangeItemStatus(item.ItemStatus) {
		case types.ChangeItemStatusApplied, types.ChangeItemStatusSkipped:
			continue // 已应用/已跳过的项不重复处理
		case types.ChangeItemStatusPending, types.ChangeItemStatusFailed:
		}
		// 按变更类型分发 lifecycle 动作（§7.2）。此前所有类型统一 mark_stale，
		// 使"替代"和"下架"在状态上无法区分，也让"旧版已失效"这条关闭门禁
		// 无法判定。
		input := types.KnowledgeLifecycleTransitionInput{
			KnowledgeID: item.KnowledgeID,
			Action:      changeRequestLifecycleAction(types.ChangeType(cr.ChangeType)),
			ActorID:     actor,
			ChangeNote:  changeNote,
		}
		// replacement 需要指向接替的新版本；条目上没记录时退回 stale，避免
		// 一次缺少参数就让整批条目 failed。
		if input.Action == types.KnowledgeLifecycleActionReplace {
			if replacementID := changeRequestReplacementTarget(item); replacementID != "" {
				input.ReplacedByID = replacementID
			} else {
				input.Action = types.KnowledgeLifecycleActionMarkStale
			}
		}
		if _, err := s.lifecycle.Transition(ctx, &input); err != nil {
			item.ItemStatus = string(types.ChangeItemStatusFailed)
			failures = append(failures, item.KnowledgeID)
		} else {
			item.ItemStatus = string(types.ChangeItemStatusApplied)
			applied++
		}
		if err := s.crs.UpdateItem(ctx, item); err != nil {
			return nil, err
		}
	}

	if len(failures) > 0 {
		// 留在 publishing：修复（如条目当前生命周期状态不允许 mark_stale）后重试。
		s.crAuditEvent(ctx, cr, types.AuditActionCRPublishFinished, types.AuditOutcomePartial, map[string]any{
			"cr_number": cr.CRNumber,
			"applied":   applied,
			"failed":    failures,
		})
		return s.Get(ctx, cr.ID)
	}

	cr.Status = string(types.ChangeRequestStatusVerifying)
	cr.UpdatedAt = s.now()
	if err := s.crs.Update(ctx, cr); err != nil {
		return nil, err
	}
	s.crAuditEvent(ctx, cr, types.AuditActionCRPublishFinished, types.AuditOutcomeSuccess, map[string]any{
		"cr_number": cr.CRNumber,
		"applied":   applied,
		"items":     len(items),
	})
	return s.Get(ctx, cr.ID)
}

// changeRequestLifecycleAction maps a change type onto the lifecycle action its
// items take on publish.
//
// 文字勘误/内容改版/参数变更 都是"先让旧内容失效，再由编辑重新提交审核"，
// 所以走 stale；替代与下架在语义上是终态，分别走 replace 与 delist；结构
// 重组同样先 stale（拆分/合并后由新条目接替）。
func changeRequestLifecycleAction(changeType types.ChangeType) types.KnowledgeLifecycleAction {
	switch changeType {
	case types.ChangeTypeDelist:
		return types.KnowledgeLifecycleActionDelist
	case types.ChangeTypeReplacement:
		return types.KnowledgeLifecycleActionReplace
	default:
		return types.KnowledgeLifecycleActionMarkStale
	}
}

// changeRequestReplacementTarget returns the knowledge that supersedes an item.
func changeRequestReplacementTarget(item *types.ChangeItem) string {
	if item == nil {
		return ""
	}
	return strings.TrimSpace(item.ReplacedByID)
}

// Close 关闭（verifying→closed）。门禁（V3.0 §7.2）：所有影响任务为
// done/waived 且所有条目非 pending；不满足返回 *types.ChangeCloseBlockedError
// （handler 映射 400 并透出未闭环清单）。
func (s *changeRequestService) Close(ctx context.Context, crID string) (*types.ChangeRequest, error) {
	tenantID, err := changeRequestTenantFromContext(ctx)
	if err != nil {
		return nil, err
	}
	cr, err := s.loadCR(ctx, tenantID, crID)
	if err != nil {
		return nil, err
	}
	if !types.ChangeRequestStatus(cr.Status).CanTransitionTo(types.ChangeRequestStatusClosed) {
		return nil, ErrCRInvalidState
	}

	tasks, err := s.crs.ListTasksByCR(ctx, []string{cr.ID})
	if err != nil {
		return nil, err
	}
	items, err := s.crs.ListItemsByCR(ctx, []string{cr.ID})
	if err != nil {
		return nil, err
	}
	openTasks := make([]string, 0)
	for _, task := range tasks {
		if types.ImpactTaskStatus(task.Status) == types.ImpactTaskStatusOpen {
			openTasks = append(openTasks, task.ID)
		}
	}
	pendingItems := make([]string, 0)
	for _, item := range items {
		if types.ChangeItemStatus(item.ItemStatus) == types.ChangeItemStatusPending {
			pendingItems = append(pendingItems, item.KnowledgeID)
		}
	}
	// 四条关闭门禁（§7.2）：影响任务闭环、验证通过、旧版已失效、通知已送达。
	// v1 只实现了前两条，所以"变更没验证、也没通知受影响的人"照样能关单。
	verified := strings.TrimSpace(cr.VerificationResult) != ""
	notified := cr.NotifiedAt != nil
	oldVersionGone := s.oldVersionsRetired(ctx, cr, items)

	if len(openTasks) > 0 || len(pendingItems) > 0 || !verified || !notified || !oldVersionGone {
		return nil, &types.ChangeCloseBlockedError{
			OpenTasks:      openTasks,
			PendingItems:   pendingItems,
			Verified:       verified,
			Notified:       notified,
			OldVersionGone: oldVersionGone,
		}
	}

	now := s.now()
	cr.Status = string(types.ChangeRequestStatusClosed)
	cr.ClosedAt = &now
	cr.UpdatedAt = now
	if err := s.crs.Update(ctx, cr); err != nil {
		return nil, err
	}
	s.crAuditEvent(ctx, cr, types.AuditActionCRClosed, types.AuditOutcomeSuccess, map[string]any{
		"cr_number": cr.CRNumber,
		"tasks":     len(tasks),
		"items":     len(items),
	})
	return s.Get(ctx, cr.ID)
}

// oldVersionsRetired reports whether every item this change touched has left the
// "current" states (§7.2 关闭条件③：旧版本已失效).
//
// A change that left its items as valid/stale has not actually taken effect:
// closing it would claim "the old version is gone" while readers still get it.
// Items already delisted/replaced/expired/archived count as retired.
func (s *changeRequestService) oldVersionsRetired(
	ctx context.Context, cr *types.ChangeRequest, items []*types.ChangeItem,
) bool {
	tenantID, _ := types.TenantIDFromContext(ctx)
	for _, item := range items {
		if item == nil || item.KnowledgeID == "" {
			continue
		}
		knowledge, err := s.knowledges.GetKnowledgeByID(ctx, tenantID, item.KnowledgeID)
		if err != nil || knowledge == nil {
			// 读不到就当未失效：门禁宁严不宽。
			return false
		}
		status := types.ParseKnowledgeLifecycleStatus(knowledge.LifecycleStatus)
		switch status {
		case types.KnowledgeLifecycleDelisted, types.KnowledgeLifecycleReplaced,
			types.KnowledgeLifecycleExpired, types.KnowledgeLifecycleArchived:
			continue
		default:
			return false
		}
	}
	return true
}

// MarkVerified records the post-publish verification outcome (关闭条件②).
func (s *changeRequestService) MarkVerified(
	ctx context.Context, crID, result, actorID string,
) (*types.ChangeRequest, error) {
	tenantID, err := changeRequestTenantFromContext(ctx)
	if err != nil {
		return nil, err
	}
	cr, err := s.loadCR(ctx, tenantID, crID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(result) == "" {
		return nil, ErrCRVerificationRequired
	}
	cr.VerificationResult = strings.TrimSpace(result)
	cr.UpdatedAt = s.now()
	if err := s.crs.Update(ctx, cr); err != nil {
		return nil, err
	}
	s.crAuditEvent(ctx, cr, types.AuditActionCRVerified, types.AuditOutcomeSuccess, map[string]any{
		"cr_number": cr.CRNumber,
		"actor":     actorID,
		"result":    truncateChangeText(cr.VerificationResult, 200),
	})
	return s.Get(ctx, cr.ID)
}

// MarkNotified records that the affected owners were told (关闭条件④).
func (s *changeRequestService) MarkNotified(
	ctx context.Context, crID, actorID string,
) (*types.ChangeRequest, error) {
	tenantID, err := changeRequestTenantFromContext(ctx)
	if err != nil {
		return nil, err
	}
	cr, err := s.loadCR(ctx, tenantID, crID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	cr.NotifiedAt = &now
	cr.UpdatedAt = now
	if err := s.crs.Update(ctx, cr); err != nil {
		return nil, err
	}
	s.crAuditEvent(ctx, cr, types.AuditActionCRNotified, types.AuditOutcomeSuccess, map[string]any{
		"cr_number": cr.CRNumber,
		"actor":     actorID,
	})
	return s.Get(ctx, cr.ID)
}

// Rollback 回滚（verifying→rolled_back，终态）。已 applied 的条目保持其
// stale 状态：恢复（restore）属于生命周期逆操作，按 V3.0 §7.1 必须走审批，
// 不在变更单 v1 范围内自动执行；本方法只落单据终态并留痕。
func (s *changeRequestService) Rollback(ctx context.Context, crID string) (*types.ChangeRequest, error) {
	tenantID, err := changeRequestTenantFromContext(ctx)
	if err != nil {
		return nil, err
	}
	cr, err := s.loadCR(ctx, tenantID, crID)
	if err != nil {
		return nil, err
	}
	if !types.ChangeRequestStatus(cr.Status).CanTransitionTo(types.ChangeRequestStatusRolledBack) {
		return nil, ErrCRInvalidState
	}
	cr.Status = string(types.ChangeRequestStatusRolledBack)
	cr.UpdatedAt = s.now()
	if err := s.crs.Update(ctx, cr); err != nil {
		return nil, err
	}
	s.crAuditEvent(ctx, cr, types.AuditActionCRRolledBack, types.AuditOutcomeSuccess, map[string]any{
		"cr_number":     cr.CRNumber,
		"rollback_plan": truncateChangeText(cr.RollbackPlan, 200),
	})
	return s.Get(ctx, cr.ID)
}

// List 分页列出变更单，聚合 items + impact_tasks。
func (s *changeRequestService) List(
	ctx context.Context, filter types.ChangeRequestListFilter,
) ([]*types.ChangeRequest, int64, error) {
	tenantID, err := changeRequestTenantFromContext(ctx)
	if err != nil {
		return nil, 0, err
	}
	crs, total, err := s.crs.List(ctx, tenantID, filter)
	if err != nil {
		return nil, 0, err
	}
	if err := s.loadAggregated(ctx, crs); err != nil {
		return nil, 0, err
	}
	return crs, total, nil
}

// Get 取单张变更单（聚合 items + impact_tasks）。
func (s *changeRequestService) Get(ctx context.Context, crID string) (*types.ChangeRequest, error) {
	tenantID, err := changeRequestTenantFromContext(ctx)
	if err != nil {
		return nil, err
	}
	cr, err := s.loadCR(ctx, tenantID, crID)
	if err != nil {
		return nil, err
	}
	if err := s.loadAggregated(ctx, []*types.ChangeRequest{cr}); err != nil {
		return nil, err
	}
	return cr, nil
}

// UpdateImpactTask 更新影响任务状态（open/done/waived）与备注。任务必须属于
// 指定变更单（防跨单操作）；done/waived 可逆（改回 open 即重开），门禁只在
// Close 时校验。
func (s *changeRequestService) UpdateImpactTask(
	ctx context.Context, crID string, taskID string, status types.ImpactTaskStatus, note string,
) (*types.ImpactTask, error) {
	tenantID, err := changeRequestTenantFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if !status.Valid() {
		return nil, ErrCRInvalidTaskStatus
	}
	cr, err := s.loadCR(ctx, tenantID, crID)
	if err != nil {
		return nil, err
	}
	task, err := s.crs.GetTask(ctx, tenantID, taskID)
	if err != nil {
		if errors.Is(err, repository.ErrImpactTaskNotFound) {
			return nil, ErrCRImpactTaskNotFound
		}
		return nil, err
	}
	if task.CRID != cr.ID {
		return nil, ErrCRImpactTaskNotFound
	}
	task.Status = string(status)
	task.Note = note
	task.UpdatedAt = s.now()
	if err := s.crs.UpdateTask(ctx, task); err != nil {
		return nil, err
	}
	s.crAuditEvent(ctx, cr, types.AuditActionCRImpactTaskUpdated, types.AuditOutcomeSuccess, map[string]any{
		"cr_number":   cr.CRNumber,
		"task_id":     task.ID,
		"target_type": task.TargetType,
		"target_id":   task.TargetID,
		"status":      task.Status,
	})
	return task, nil
}

// ============================================================================
// 内部助手
// ============================================================================

// changeRequestTenantFromContext 提取租户上下文；缺失返回 ErrCRTenantMissing。
func changeRequestTenantFromContext(ctx context.Context) (uint64, error) {
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		return 0, ErrCRTenantMissing
	}
	return tenantID, nil
}

// loadCR 按租户加载变更单，仓储哨兵映射为业务哨兵。
func (s *changeRequestService) loadCR(ctx context.Context, tenantID uint64, crID string) (*types.ChangeRequest, error) {
	if crID == "" {
		return nil, ErrCRNotFound
	}
	cr, err := s.crs.GetByID(ctx, tenantID, crID)
	if err != nil {
		if errors.Is(err, repository.ErrChangeRequestNotFound) {
			return nil, ErrCRNotFound
		}
		return nil, err
	}
	return cr, nil
}

// changeRequestItemsEditable 报告条目是否可增删（仅 draft/analyzing）。
func changeRequestItemsEditable(status string) bool {
	s := types.ChangeRequestStatus(status)
	return s == types.ChangeRequestStatusDraft || s == types.ChangeRequestStatusAnalyzing
}

// changeImpactPriorityForRisk 是影响任务优先级的 v1 路由：变更单风险级 →
// 任务 priority。high 必须处理（不闭环关不了单），mid 建议，low 周知。
func changeImpactPriorityForRisk(risk types.ChangeRiskLevel) types.ImpactTaskPriority {
	switch risk {
	case types.ChangeRiskHigh:
		return types.ImpactPriorityMustChange
	case types.ChangeRiskMid:
		return types.ImpactPrioritySuggested
	default:
		return types.ImpactPriorityFYI
	}
}

// changeRequestNumberPrefix 生成当日单号前缀："CR-YYYYMMDD"。
func changeRequestNumberPrefix(now time.Time) string {
	return "CR-" + now.Format("20060102")
}

// ensureImpactTask 幂等创建一条 knowledge 类影响任务（调用方已做过去重集合
// 判断；这里只负责落库）。返回是否真正创建。
func (s *changeRequestService) ensureImpactTask(
	ctx context.Context, cr *types.ChangeRequest, knowledgeID, priority, note string,
) (bool, error) {
	now := s.now()
	task := &types.ImpactTask{
		ID:         uuid.NewString(),
		TenantID:   cr.TenantID,
		CRID:       cr.ID,
		TargetType: string(types.ImpactTargetKnowledge),
		TargetID:   knowledgeID,
		Priority:   priority,
		Status:     string(types.ImpactTaskStatusOpen),
		Note:       note,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.crs.CreateTask(ctx, task); err != nil {
		return false, err
	}
	return true, nil
}

// loadAggregated 把 items / impact_tasks 批量挂到各变更单上（两次 IN 查询，
// 避免 List 的 N+1）。空切片显式置为非 nil，保证 JSON 输出是 [] 而非 null。
func (s *changeRequestService) loadAggregated(ctx context.Context, crs []*types.ChangeRequest) error {
	if len(crs) == 0 {
		return nil
	}
	ids := make([]string, 0, len(crs))
	for _, cr := range crs {
		ids = append(ids, cr.ID)
	}
	items, err := s.crs.ListItemsByCR(ctx, ids)
	if err != nil {
		return err
	}
	tasks, err := s.crs.ListTasksByCR(ctx, ids)
	if err != nil {
		return err
	}
	itemsByCR := make(map[string][]*types.ChangeItem, len(crs))
	for _, item := range items {
		itemsByCR[item.CRID] = append(itemsByCR[item.CRID], item)
	}
	tasksByCR := make(map[string][]*types.ImpactTask, len(crs))
	for _, task := range tasks {
		tasksByCR[task.CRID] = append(tasksByCR[task.CRID], task)
	}
	for _, cr := range crs {
		cr.Items = itemsByCR[cr.ID]
		if cr.Items == nil {
			cr.Items = []*types.ChangeItem{}
		}
		cr.ImpactTasks = tasksByCR[cr.ID]
		if cr.ImpactTasks == nil {
			cr.ImpactTasks = []*types.ImpactTask{}
		}
	}
	return nil
}

// truncateChangeText 限制进入审计详情的文本长度，避免审计行膨胀。
func truncateChangeText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

// crAuditEvent 把变更单事件写进审计流（scope = change_request，租户治理面）。
// best-effort：审计故障不回滚业务，与 recordKBActivity 的失败语义一致。
// 审计 JSON 按 repo 约定用 json.Marshal + types.JSON 装载。
func (s *changeRequestService) crAuditEvent(
	ctx context.Context,
	cr *types.ChangeRequest,
	action types.AuditAction,
	outcome types.AuditOutcome,
	details map[string]any,
) {
	if s.audit == nil {
		return
	}
	var detailJSON types.JSON
	if len(details) > 0 {
		if payload, err := json.Marshal(details); err == nil {
			detailJSON = types.JSON(payload)
		}
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    cr.TenantID,
		ActorUserID: auditActor(ctx),
		ActorRole:   auditActorRole(ctx),
		Action:      action,
		ScopeType:   changeRequestAuditScope,
		ScopeID:     cr.ID,
		TargetType:  changeRequestAuditScope,
		TargetID:    cr.ID,
		Outcome:     outcome,
		Details:     detailJSON,
	})
}

// recordKnowledgeActivity 把条目级动作同步到知识所在 KB 的活动流（best-effort；
// kbID 为空时跳过，recordKBActivity 内部兜底其余防御）。
func (s *changeRequestService) recordKnowledgeActivity(
	ctx context.Context,
	tenantID uint64,
	kbID string,
	action types.AuditAction,
	cr *types.ChangeRequest,
	outcome types.AuditOutcome,
	details map[string]any,
) {
	if kbID == "" {
		return
	}
	recordKBActivity(ctx, s.audit, tenantID, kbID, action, changeRequestAuditScope, cr.ID, outcome, details)
}
