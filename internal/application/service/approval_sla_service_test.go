package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubSLATaskRepo serves pending tasks within a lookahead window.
type stubSLATaskRepo struct {
	interfaces.ApprovalTaskRepository
	tasks     []*types.ApprovalTask
	tenantIDs []uint64
}

func (s *stubSLATaskRepo) ListPendingTenantIDs(context.Context) ([]uint64, error) {
	return s.tenantIDs, nil
}

func (s *stubSLATaskRepo) ListPendingBySLA(
	_ context.Context, _ uint64, before time.Time, _ int,
) ([]*types.ApprovalTask, error) {
	out := make([]*types.ApprovalTask, 0, len(s.tasks))
	for _, task := range s.tasks {
		if task != nil && task.SLADue != nil && !task.SLADue.After(before) {
			out = append(out, task)
		}
	}
	return out, nil
}

// stubSLAEventRepo records events and enforces per-kind idempotency.
type stubSLAEventRepo struct {
	interfaces.ApprovalSLAEventRepository
	events []*types.ApprovalSLAEvent
}

func (s *stubSLAEventRepo) Create(_ context.Context, event *types.ApprovalSLAEvent) error {
	s.events = append(s.events, event)
	return nil
}

func (s *stubSLAEventRepo) HasEvent(_ context.Context, _ uint64, taskID, kind string) (bool, error) {
	for _, event := range s.events {
		if event.TaskID == taskID && event.Kind == kind {
			return true, nil
		}
	}
	return false, nil
}

func (s *stubSLAEventRepo) ListByTask(
	_ context.Context, _ uint64, _ string, _ int,
) ([]*types.ApprovalSLAEvent, error) {
	return s.events, nil
}

func newSLAService(now time.Time, tasks []*types.ApprovalTask) (*approvalSLAService, *stubSLAEventRepo) {
	events := &stubSLAEventRepo{}
	return &approvalSLAService{
		tasks:      &stubSLATaskRepo{tasks: tasks, tenantIDs: []uint64{7}},
		events:     events,
		now:        func() time.Time { return now },
		sweepLimit: 10,
	}, events
}

// TestSLASweepRemindsBeforeDeadline covers §6.3's 到期催办: a task approaching
// its deadline gets exactly one reminder.
func TestSLASweepRemindsBeforeDeadline(t *testing.T) {
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	due := now.Add(10 * time.Hour) // inside the default 24h reminder window
	svc, events := newSLAService(now, []*types.ApprovalTask{
		{ID: "task-1", TenantID: 7, KnowledgeID: "k-1", KBID: "kb-1", SLADue: &due},
	})

	reminded, escalated, err := svc.SweepOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, reminded)
	assert.Zero(t, escalated)
	require.Len(t, events.events, 1)
	assert.Equal(t, string(types.ApprovalSLAEventReminded), events.events[0].Kind)

	// A second sweep must not notify again for the same task.
	reminded, escalated, err = svc.SweepOnce(context.Background())
	require.NoError(t, err)
	assert.Zero(t, reminded, "the reminder must be idempotent")
	assert.Zero(t, escalated)
	assert.Len(t, events.events, 1)
}

// TestSLASweepEscalatesAfterDeadline covers 超时升级: a task past its deadline
// is escalated, once.
func TestSLASweepEscalatesAfterDeadline(t *testing.T) {
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	due := now.Add(-48 * time.Hour)
	svc, events := newSLAService(now, []*types.ApprovalTask{
		{ID: "task-2", TenantID: 7, KnowledgeID: "k-2", KBID: "kb-1", SLADue: &due},
	})

	reminded, escalated, err := svc.SweepOnce(context.Background())
	require.NoError(t, err)
	assert.Zero(t, reminded, "an overdue task escalates rather than reminding")
	assert.Equal(t, 1, escalated)
	require.Len(t, events.events, 1)
	assert.Equal(t, string(types.ApprovalSLAEventEscalated), events.events[0].Kind)

	_, escalated, err = svc.SweepOnce(context.Background())
	require.NoError(t, err)
	assert.Zero(t, escalated, "escalation must be idempotent")
}

// TestSLASweepIgnoresTasksOutsideWindow keeps a healthy queue quiet.
func TestSLASweepIgnoresTasksOutsideWindow(t *testing.T) {
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	due := now.Add(30 * 24 * time.Hour)
	svc, events := newSLAService(now, []*types.ApprovalTask{
		{ID: "task-3", TenantID: 7, KBID: "kb-1", SLADue: &due},
	})
	reminded, escalated, err := svc.SweepOnce(context.Background())
	require.NoError(t, err)
	assert.Zero(t, reminded)
	assert.Zero(t, escalated)
	assert.Empty(t, events.events)
}

// TestSLASweepIgnoresTasksWithoutDeadline covers legacy rows: a nil sla_due
// must not panic or be treated as overdue.
func TestSLASweepIgnoresTasksWithoutDeadline(t *testing.T) {
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	svc, events := newSLAService(now, []*types.ApprovalTask{
		{ID: "task-4", TenantID: 7, KBID: "kb-1", SLADue: nil},
	})
	reminded, escalated, err := svc.SweepOnce(context.Background())
	require.NoError(t, err)
	assert.Zero(t, reminded)
	assert.Zero(t, escalated)
	assert.Empty(t, events.events)
}
