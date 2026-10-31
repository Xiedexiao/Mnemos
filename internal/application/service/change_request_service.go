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
	now        func() time.Time
}

// NewChangeRequestService constructs the production change request service.
func NewChangeRequestService(
	crs interfaces.ChangeRequestRepository,
	knowledges interfaces.KnowledgeRepository,
	lifecycle interfaces.KnowledgeLifecycleService,
	audit interfaces.AuditLogService,
) interfaces.ChangeRequestService {
	return &changeRequestService{
		crs:        crs,
		knowledges: knowledges,
		lifecycle:  lifecycle,
		audit:      audit,
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

		// TODO(v3.1): 完整影响分析应沿 chunk 引文、wiki 反链与订阅关系展开
		// （V3.0 §7.6）；v1 用"同 KB 共享标签近邻"近似"谁引用了它"。
		neighbors, err := s.crs.ListTagNeighborKnowledgeIDs(
			ctx, tenantID, knowledge.KnowledgeBaseID, knowledge.ID, changeRequestNeighborLimit,
		)
		if err != nil {
			return nil, err
		}
		for _, neighborID := range neighbors {
			if _, seen := tasked[neighborID]; seen {
				continue
			}
			ok, err := s.ensureImpactTask(ctx, cr, neighborID, priority,
				fmt.Sprintf("同 KB 近邻（共享标签）：与变更条目 %s 关联，请评估是否需同步修订", knowledge.ID))
			if err != nil {
				return nil, err
			}
			if ok {
				created++
				tasked[neighborID] = struct{}{}
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

// SubmitForApproval 提交审批（analyzing→in_approval）。
//
// TODO(v3.1): 真实审批对接 —— 复用 A2 ReviewService 的审批任务/回执/哈希链：
// 为变更单建立审批任务（四眼校验基于 CreatedBy vs 审批人，高风险单走 dual），
// 法定人数通过后出回执，再放行 Schedule。v1 占位：直接进入 in_approval，
// 由治理面 Admin 手工驱动排期与发布；每一步都写审计可回溯。
func (s *changeRequestService) SubmitForApproval(ctx context.Context, crID string) (*types.ChangeRequest, error) {
	tenantID, err := changeRequestTenantFromContext(ctx)
	if err != nil {
		return nil, err
	}
	cr, err := s.loadCR(ctx, tenantID, crID)
	if err != nil {
		return nil, err
	}
	if !types.ChangeRequestStatus(cr.Status).CanTransitionTo(types.ChangeRequestStatusInApproval) {
		return nil, ErrCRInvalidState
	}
	cr.Status = string(types.ChangeRequestStatusInApproval)
	cr.UpdatedAt = s.now()
	if err := s.crs.Update(ctx, cr); err != nil {
		return nil, err
	}
	// accepted：审批未决，与 review.submitted 同语义。
	s.crAuditEvent(ctx, cr, types.AuditActionCRSubmitted, types.AuditOutcomeAccepted, map[string]any{
		"cr_number":  cr.CRNumber,
		"created_by": cr.CreatedBy,
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
		// 状态机推进失败只标 failed，不中断本轮：一条坏数据不能挡住其余条目。
		if _, err := s.lifecycle.Transition(ctx, &types.KnowledgeLifecycleTransitionInput{
			KnowledgeID: item.KnowledgeID,
			Action:      types.KnowledgeLifecycleActionMarkStale,
			ActorID:     actor,
			ChangeNote:  changeNote,
		}); err != nil {
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
	if len(openTasks) > 0 || len(pendingItems) > 0 {
		return nil, &types.ChangeCloseBlockedError{OpenTasks: openTasks, PendingItems: pendingItems}
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
