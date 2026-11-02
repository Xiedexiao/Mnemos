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

// stubGovernanceTasks is an in-memory approval-task store keyed by subject.
type stubGovernanceTasks struct {
	interfaces.ApprovalTaskRepository
	tasks map[string]*types.ApprovalTask
}

func newStubGovernanceTasks() *stubGovernanceTasks {
	return &stubGovernanceTasks{tasks: map[string]*types.ApprovalTask{}}
}

func (s *stubGovernanceTasks) Create(_ context.Context, task *types.ApprovalTask) error {
	s.tasks[task.ID] = task
	return nil
}

func (s *stubGovernanceTasks) GetByID(
	_ context.Context, _ uint64, taskID string,
) (*types.ApprovalTask, error) {
	task, ok := s.tasks[taskID]
	if !ok {
		return nil, ErrReviewTaskNotFound
	}
	return task, nil
}

func (s *stubGovernanceTasks) FindLatestBySubject(
	_ context.Context, _ uint64, subjectType, subjectID string,
) (*types.ApprovalTask, error) {
	for _, task := range s.tasks {
		if task.SubjectType == subjectType && task.SubjectID == subjectID {
			return task, nil
		}
	}
	return nil, ErrReviewTaskNotFound
}

func (s *stubGovernanceTasks) ListPendingBySubject(
	_ context.Context, _ uint64, subjectType string, _ int,
) ([]*types.ApprovalTask, error) {
	var out []*types.ApprovalTask
	for _, task := range s.tasks {
		if subjectType != "" && task.SubjectType != subjectType {
			continue
		}
		if task.Status == string(types.ApprovalTaskStatusPending) {
			out = append(out, task)
		}
	}
	return out, nil
}

func (s *stubGovernanceTasks) Update(_ context.Context, task *types.ApprovalTask) error {
	s.tasks[task.ID] = task
	return nil
}

// stubGovernanceReceipts records appended receipts.
type stubGovernanceReceipts struct {
	interfaces.ApprovalReceiptRepository
	appended []*types.ApprovalReceipt
}

func (s *stubGovernanceReceipts) Append(_ context.Context, receipt *types.ApprovalReceipt) error {
	if receipt.ReceiptID == "" {
		receipt.ReceiptID = "rcpt_test"
	}
	s.appended = append(s.appended, receipt)
	return nil
}

func newGovernanceService(tasks *stubGovernanceTasks, receipts *stubGovernanceReceipts) *governanceApprovalService {
	return &governanceApprovalService{
		tasks:    tasks,
		receipts: receipts,
		now:      func() time.Time { return time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC) },
	}
}

func governanceRequest(subjectType types.ApprovalSubjectType, subjectID string) *types.GovernanceApprovalRequest {
	return &types.GovernanceApprovalRequest{
		SubjectType: subjectType,
		SubjectID:   subjectID,
		KBID:        "kb-1",
		Title:       "测试治理动作",
		Reason:      "例行治理",
		Snapshot:    types.JSON(`{"items":["a","b"]}`),
	}
}

// TestGovernanceSubmitRejectsKnowledgeSubject keeps the two paths separate:
// knowledge publishing freezes a content baseline and advances a lifecycle, so
// it must not be submitted as a generic governance action.
func TestGovernanceSubmitRejectsKnowledgeSubject(t *testing.T) {
	svc := newGovernanceService(newStubGovernanceTasks(), &stubGovernanceReceipts{})
	_, err := svc.Submit(context.Background(), 7, "user-1",
		governanceRequest(types.ApprovalSubjectKnowledge, "doc-1"))
	require.Error(t, err)
}

// TestGovernanceSubmitAppliesRiskFloor covers §6.2: high-risk subjects are high
// risk by default, and the caller cannot talk them down.
func TestGovernanceSubmitAppliesRiskFloor(t *testing.T) {
	tasks := newStubGovernanceTasks()
	svc := newGovernanceService(tasks, &stubGovernanceReceipts{})
	ctx := context.Background()

	// The caller claims low risk for an external share.
	req := governanceRequest(types.ApprovalSubjectShare, "doc-1")
	req.RiskLevel = types.ApprovalRiskLow
	task, err := svc.Submit(ctx, 7, "user-1", req)
	require.NoError(t, err)
	assert.Equal(t, string(types.ApprovalRiskHigh), task.RiskLevel,
		"an external share is high risk regardless of what the requester labels it")
	assert.Equal(t, string(types.ApprovalModeDual), task.Mode,
		"high risk never runs single-approval")
}

// TestGovernanceSubmitIsIdempotent covers the duplicate guard.
func TestGovernanceSubmitIsIdempotent(t *testing.T) {
	tasks := newStubGovernanceTasks()
	svc := newGovernanceService(tasks, &stubGovernanceReceipts{})
	ctx := context.Background()
	req := governanceRequest(types.ApprovalSubjectExport, "export-1")
	_, err := svc.Submit(ctx, 7, "user-1", req)
	require.NoError(t, err)
	_, err = svc.Submit(ctx, 7, "user-1", governanceRequest(types.ApprovalSubjectExport, "export-1"))
	require.ErrorIs(t, err, ErrGovernancePending)
}

// TestGovernanceRequireApprovalBlocksUntilDecided is the enforcement point: a
// gated action is refused until an approved receipt exists.
func TestGovernanceRequireApprovalBlocksUntilDecided(t *testing.T) {
	tasks := newStubGovernanceTasks()
	receipts := &stubGovernanceReceipts{}
	svc := newGovernanceService(tasks, receipts)
	ctx := context.Background()
	const subjectID = "perm-1"

	// Never submitted: high-risk actions are gated even with no task.
	_, err := svc.RequireApproval(ctx, 7, types.ApprovalSubjectPermission, subjectID)
	require.ErrorIs(t, err, ErrGovernanceNotApproved,
		"'nobody asked' must not read as 'nobody objected'")

	task, err := svc.Submit(ctx, 7, "user-1", governanceRequest(types.ApprovalSubjectPermission, subjectID))
	require.NoError(t, err)
	_, err = svc.RequireApproval(ctx, 7, types.ApprovalSubjectPermission, subjectID)
	require.ErrorIs(t, err, ErrGovernanceNotApproved, "a pending task does not authorise the action")

	task, err = svc.Decide(ctx, 7, task.ID, "approver-1", types.ApprovalDecisionApproved, "同意")
	require.NoError(t, err)
	assert.Equal(t, string(types.ApprovalTaskStatusApproved), task.Status)
	require.Len(t, receipts.appended, 1, "approval must produce a receipt")

	decision, err := svc.RequireApproval(ctx, 7, types.ApprovalSubjectPermission, subjectID)
	require.NoError(t, err)
	assert.True(t, decision.Approved)
	assert.NotEmpty(t, decision.ReceiptID)
}

// TestGovernanceRejectKeepsTheActionBlocked covers the negative path.
func TestGovernanceRejectKeepsTheActionBlocked(t *testing.T) {
	tasks := newStubGovernanceTasks()
	svc := newGovernanceService(tasks, &stubGovernanceReceipts{})
	ctx := context.Background()
	task, err := svc.Submit(ctx, 7, "user-1", governanceRequest(types.ApprovalSubjectDeletion, "doc-1"))
	require.NoError(t, err)
	_, err = svc.Decide(ctx, 7, task.ID, "approver-1", types.ApprovalDecisionRejected, "不合规")
	require.NoError(t, err)
	_, err = svc.RequireApproval(ctx, 7, types.ApprovalSubjectDeletion, "doc-1")
	require.ErrorIs(t, err, ErrGovernanceNotApproved)
}

// TestGovernanceFourEyes covers the self-approval ban.
func TestGovernanceFourEyes(t *testing.T) {
	tasks := newStubGovernanceTasks()
	svc := newGovernanceService(tasks, &stubGovernanceReceipts{})
	ctx := context.Background()
	task, err := svc.Submit(ctx, 7, "user-1", governanceRequest(types.ApprovalSubjectExport, "export-1"))
	require.NoError(t, err)
	_, err = svc.Decide(ctx, 7, task.ID, "user-1", types.ApprovalDecisionApproved, "")
	require.ErrorIs(t, err, ErrReviewSelfDecision)
}

// TestGovernanceLowRiskSubjectsAreNotGated keeps routine actions from needing a
// approval every time.
func TestGovernanceLowRiskSubjectsAreNotGated(t *testing.T) {
	svc := newGovernanceService(newStubGovernanceTasks(), &stubGovernanceReceipts{})
	decision, err := svc.RequireApproval(context.Background(),
		7, types.ApprovalSubjectTaxonomy, "glossary-1")
	require.NoError(t, err)
	assert.False(t, decision.RequiresApproval)
	assert.True(t, decision.Allows())
}

// TestGovernanceSubjectRiskTable pins §6.2's risk column.
func TestGovernanceSubjectRiskTable(t *testing.T) {
	highRisk := []types.ApprovalSubjectType{
		types.ApprovalSubjectOfficialAnswer, types.ApprovalSubjectRule,
		types.ApprovalSubjectPermission, types.ApprovalSubjectShare,
		types.ApprovalSubjectExport, types.ApprovalSubjectDeletion,
		types.ApprovalSubjectModelRelease, types.ApprovalSubjectAgentTool,
	}
	for _, subject := range highRisk {
		assert.Equal(t, types.ApprovalRiskHigh, subject.DefaultRisk(), "%s must be high risk", subject)
		assert.True(t, subject.RequiresReceiptBeforeEffect(), "%s must be gated", subject)
	}
	midRisk := []types.ApprovalSubjectType{
		types.ApprovalSubjectKnowledge, types.ApprovalSubjectOntology,
		types.ApprovalSubjectMerge, types.ApprovalSubjectTaxonomy,
		types.ApprovalSubjectCandidate, types.ApprovalSubjectChangeRequest,
	}
	for _, subject := range midRisk {
		assert.Equal(t, types.ApprovalRiskMid, subject.DefaultRisk(), "%s is mid risk", subject)
	}
}

// TestGovernanceSubjectsAreAllValid keeps the enum and the risk table in sync.
func TestGovernanceSubjectsAreAllValid(t *testing.T) {
	all := []types.ApprovalSubjectType{
		types.ApprovalSubjectKnowledge, types.ApprovalSubjectOfficialAnswer,
		types.ApprovalSubjectRule, types.ApprovalSubjectOntology,
		types.ApprovalSubjectPermission, types.ApprovalSubjectShare,
		types.ApprovalSubjectExport, types.ApprovalSubjectDeletion,
		types.ApprovalSubjectMerge, types.ApprovalSubjectTaxonomy,
		types.ApprovalSubjectModelRelease, types.ApprovalSubjectAgentTool,
		types.ApprovalSubjectCandidate, types.ApprovalSubjectChangeRequest,
	}
	assert.Len(t, all, 14, "§6.2 lists fourteen governance objects")
	for _, subject := range all {
		assert.True(t, subject.Valid(), "%s must be a valid subject type", subject)
	}
}

// TestGovernanceStatusReportsLatestSubjectState covers the read model.
func TestGovernanceStatusReportsLatestSubjectState(t *testing.T) {
	tasks := newStubGovernanceTasks()
	svc := newGovernanceService(tasks, &stubGovernanceReceipts{})
	ctx := context.Background()

	// No task yet: an empty status, not an error.
	status, err := svc.Status(ctx, 7, types.ApprovalSubjectModelRelease, "model-1")
	require.NoError(t, err)
	assert.Empty(t, status.TaskID)

	task, err := svc.Submit(ctx, 7, "user-1", governanceRequest(types.ApprovalSubjectModelRelease, "model-1"))
	require.NoError(t, err)
	status, err = svc.Status(ctx, 7, types.ApprovalSubjectModelRelease, "model-1")
	require.NoError(t, err)
	assert.Equal(t, task.ID, status.TaskID)
	assert.Equal(t, string(types.ApprovalTaskStatusPending), status.Status)
}
