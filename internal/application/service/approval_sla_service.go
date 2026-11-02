package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// approvalSLAService implements interfaces.ApprovalSLAService (V3.0 §6.3).
//
// The review flow already computed an sla_due timestamp at submission, but
// nothing ever read it: a task could sit pending forever and the "到期催办 /
// 超时升级 / 积压预警" requirements had no implementation. This service is the
// missing consumer. It runs on a ticker, records what it did in
// approval_sla_events, and is idempotent per (task, kind) so a restart or an
// overlapping tick cannot double-notify.
type approvalSLAService struct {
	tasks      interfaces.ApprovalTaskRepository
	events     interfaces.ApprovalSLAEventRepository
	flows      interfaces.ApprovalFlowRepository
	notifier   *NotificationService
	audit      interfaces.AuditLogService
	now        func() time.Time
	sweepLimit int
}

// NewApprovalSLAService constructs the SLA sweep service.
func NewApprovalSLAService(
	tasks interfaces.ApprovalTaskRepository,
	events interfaces.ApprovalSLAEventRepository,
	flows interfaces.ApprovalFlowRepository,
	notifier *NotificationService,
	audit interfaces.AuditLogService,
) interfaces.ApprovalSLAService {
	return &approvalSLAService{
		tasks: tasks, events: events, flows: flows,
		notifier: notifier, audit: audit,
		now: time.Now, sweepLimit: 200,
	}
}

// SweepOnce runs one reminder/escalation pass.
//
// Two thresholds per task are evaluated:
//
//	reminded  — now >= sla_due - reminder_hours  (a friendly nudge first)
//	escalated — now >= sla_due                   (the deadline passed)
//
// Escalation does not change the task's approvers (the task model has no
// per-node assignee list yet); it records the escalation and notifies the
// configured escalation role, which is what makes backlog visible instead of
// silent.
func (s *approvalSLAService) SweepOnce(ctx context.Context) (int, int, error) {
	tenantIDs, err := s.tasks.ListPendingTenantIDs(ctx)
	if err != nil {
		return 0, 0, err
	}
	var reminded, escalated int
	for _, tenantID := range tenantIDs {
		tenantCtx := types.WithExecutionTenant(ctx, tenantID)
		tenantReminded, tenantEscalated, sweepErr := s.sweepTenant(tenantCtx, tenantID)
		reminded += tenantReminded
		escalated += tenantEscalated
		if sweepErr != nil {
			// 单个租户失败不阻断其余租户：SLA 扫描是尽力而为的后台任务。
			logger.Warnf(ctx, "approval SLA sweep failed for tenant %d: %v", tenantID, sweepErr)
		}
	}
	return reminded, escalated, nil
}

// sweepTenant evaluates one tenant's pending tasks.
func (s *approvalSLAService) sweepTenant(
	ctx context.Context, tenantID uint64,
) (int, int, error) {
	now := s.now()
	// 用最长提醒窗口一次捞回所有可能相关的任务，再按各自配置判定。
	lookahead := now.Add(approvalSLAEventLookahead)
	tasks, err := s.tasks.ListPendingBySLA(ctx, tenantID, lookahead, s.sweepLimit)
	if err != nil {
		return 0, 0, err
	}
	var reminded, escalated int
	for _, task := range tasks {
		if task == nil || task.SLADue == nil {
			continue
		}
		reminderWindow, escalateRole := s.windowFor(ctx, tenantID, task)
		overdue := !now.Before(*task.SLADue)

		if overdue {
			done, err := s.record(ctx, tenantID, task, types.ApprovalSLAEventEscalated, map[string]interface{}{
				"overdue_hours": int(now.Sub(*task.SLADue).Hours()),
				"escalate_to":   escalateRole,
				"sla_due":       task.SLADue.Format(time.RFC3339),
			})
			if err != nil {
				logger.Warnf(ctx, "failed to record SLA escalation for task %s: %v", task.ID, err)
				continue
			}
			if done {
				escalated++
				s.notify(ctx, tenantID, task, escalateRole, true)
			}
			continue
		}

		// 未到期但已进入提醒窗口：先催办一次。
		if !now.Before(task.SLADue.Add(-reminderWindow)) {
			done, err := s.record(ctx, tenantID, task, types.ApprovalSLAEventReminded, map[string]interface{}{
				"hours_to_due":   int(task.SLADue.Sub(now).Hours()),
				"reminder_hours": int(reminderWindow.Hours()),
			})
			if err != nil {
				logger.Warnf(ctx, "failed to record SLA reminder for task %s: %v", task.ID, err)
				continue
			}
			if done {
				reminded++
				s.notify(ctx, tenantID, task, "", false)
			}
		}
	}
	return reminded, escalated, nil
}

// approvalSLAEventLookahead bounds how far ahead tasks are fetched. A tenant
// with a week-long reminder window still gets fetched because the query is on
// sla_due, and the per-task window decides whether action is due.
const approvalSLAEventLookahead = 30 * 24 * time.Hour

// windowFor resolves the reminder window and escalation role from the task's
// space configuration, falling back to the sensible defaults.
func (s *approvalSLAService) windowFor(
	ctx context.Context, tenantID uint64, task *types.ApprovalTask,
) (time.Duration, string) {
	const defaultReminder = 24 * time.Hour
	role := string(types.TenantRoleAdmin)
	if s.flows == nil || task.KBID == "" {
		return defaultReminder, role
	}
	flow, err := s.flows.GetByKB(ctx, tenantID, task.KBID)
	if err != nil || flow == nil {
		return defaultReminder, role
	}
	if flow.EscalateRole != "" && types.TenantRole(flow.EscalateRole).IsValid() {
		role = flow.EscalateRole
	}
	if flow.ReminderHours > 0 {
		return time.Duration(flow.ReminderHours) * time.Hour, role
	}
	return defaultReminder, role
}

// record appends the event unless the task already has one of this kind.
func (s *approvalSLAService) record(
	ctx context.Context, tenantID uint64, task *types.ApprovalTask,
	kind types.ApprovalSLAEventKind, detail map[string]interface{},
) (bool, error) {
	if s.events == nil {
		return false, nil
	}
	exists, err := s.events.HasEvent(ctx, tenantID, task.ID, string(kind))
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return false, err
	}
	event := &types.ApprovalSLAEvent{
		TenantID:  tenantID,
		TaskID:    task.ID,
		Kind:      string(kind),
		Detail:    types.JSON(raw),
		CreatedAt: s.now(),
	}
	if err := s.events.Create(ctx, event); err != nil {
		return false, err
	}
	if s.audit != nil {
		_ = s.audit.Log(ctx, &types.AuditLog{
			TenantID:    tenantID,
			ActorUserID: "system",
			Action:      types.AuditAction("review.sla_" + string(kind)),
			ScopeType:   "knowledge",
			ScopeID:     task.KnowledgeID,
			TargetType:  "approval_task",
			TargetID:    task.ID,
			Outcome:     types.AuditOutcomeAccepted,
			Details:     types.JSON(raw),
		})
	}
	return true, nil
}

// notify tells the reviewer pool that a task needs attention.
func (s *approvalSLAService) notify(
	ctx context.Context, tenantID uint64, task *types.ApprovalTask, escalateTo string, overdue bool,
) {
	if s.notifier == nil {
		return
	}
	title := "审核待办即将到期"
	body := "有一份知识审核任务即将超过审批时限，请尽快处理。"
	if overdue {
		title = "审核任务已超时并升级"
		body = "有一份知识审核任务已超过审批时限，已升级至 " + escalateTo + "。"
	}
	// 通知按接收者权限渲染：这里只发标题级提示，不携带受限内容（§2.10）。
	// Notify 自身吞掉写入错误（通知不该决定业务成败），所以这里不做错误分支。
	s.notifier.Notify(ctx, &types.Notification{
		TenantID:   tenantID,
		UserID:     task.RequestedBy,
		Kind:       "review.sla",
		Title:      title,
		Body:       body,
		SourceType: "approval_task",
		SourceID:   task.ID,
	})
}

// Events returns the SLA ledger of one task.
func (s *approvalSLAService) Events(
	ctx context.Context, tenantID uint64, taskID string,
) ([]*types.ApprovalSLAEvent, error) {
	if s.events == nil {
		return nil, nil
	}
	return s.events.ListByTask(ctx, tenantID, taskID, 0)
}
