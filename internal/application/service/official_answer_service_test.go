package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubOfficialAnswerRepo is an in-memory answer store.
type stubOfficialAnswerRepo struct {
	interfaces.OfficialAnswerRepository
	rows map[string]*types.OfficialAnswer
}

func newStubOfficialAnswerRepo() *stubOfficialAnswerRepo {
	return &stubOfficialAnswerRepo{rows: map[string]*types.OfficialAnswer{}}
}

func (s *stubOfficialAnswerRepo) Create(_ context.Context, answer *types.OfficialAnswer) error {
	if answer.ID == "" {
		answer.ID = "oa-" + itoaSmall(len(s.rows)+1)
	}
	s.rows[answer.ID] = answer
	return nil
}

func (s *stubOfficialAnswerRepo) Update(_ context.Context, answer *types.OfficialAnswer) error {
	s.rows[answer.ID] = answer
	return nil
}

func (s *stubOfficialAnswerRepo) GetByID(
	_ context.Context, _ uint64, answerID string,
) (*types.OfficialAnswer, error) {
	answer, ok := s.rows[answerID]
	if !ok {
		return nil, repository.ErrOfficialAnswerNotFound
	}
	return answer, nil
}

func (s *stubOfficialAnswerRepo) List(
	_ context.Context, _ uint64, status types.OfficialAnswerStatus, _ int,
) ([]*types.OfficialAnswer, error) {
	out := make([]*types.OfficialAnswer, 0, len(s.rows))
	for _, answer := range s.rows {
		if status != "" && answer.Status != string(status) {
			continue
		}
		out = append(out, answer)
	}
	return out, nil
}

func (s *stubOfficialAnswerRepo) FindAnswerable(
	_ context.Context, _ uint64, normalizedQuestion, _ string,
) (*types.OfficialAnswer, error) {
	for _, answer := range s.rows {
		if answer.QuestionNormalized == normalizedQuestion &&
			types.OfficialAnswerStatus(answer.Status).Answerable() {
			return answer, nil
		}
	}
	return nil, repository.ErrOfficialAnswerNotFound
}

func (s *stubOfficialAnswerRepo) FindActiveByQuestion(
	_ context.Context, _ uint64, normalizedQuestion, excludeAnswerID string,
) (*types.OfficialAnswer, error) {
	for _, answer := range s.rows {
		if answer.ID == excludeAnswerID {
			continue
		}
		if answer.QuestionNormalized == normalizedQuestion &&
			types.OfficialAnswerStatus(answer.Status).Answerable() {
			return answer, nil
		}
	}
	return nil, repository.ErrOfficialAnswerNotFound
}

func (s *stubOfficialAnswerRepo) ListExpiredDue(
	_ context.Context, _ uint64, now time.Time, _ int,
) ([]*types.OfficialAnswer, error) {
	var out []*types.OfficialAnswer
	for _, answer := range s.rows {
		if types.OfficialAnswerStatus(answer.Status).Answerable() &&
			answer.ExpiresAt != nil && now.After(*answer.ExpiresAt) {
			out = append(out, answer)
		}
	}
	return out, nil
}

// stubAnswerReceipts returns a fixed approved receipt.
type stubAnswerReceipts struct {
	interfaces.ApprovalReceiptRepository
	approved bool
}

func (s *stubAnswerReceipts) Get(
	_ context.Context, tenantID uint64, receiptID string,
) (*types.ApprovalReceipt, error) {
	if !s.approved || receiptID == "" {
		return nil, assert.AnError
	}
	return &types.ApprovalReceipt{
		ReceiptID: receiptID, TenantID: tenantID,
		Decision: string(types.ApprovalDecisionApproved),
	}, nil
}

func newOfficialAnswerServiceForTest(receipts *stubAnswerReceipts) (
	*officialAnswerService, *stubOfficialAnswerRepo,
) {
	repo := newStubOfficialAnswerRepo()
	fixed := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	return &officialAnswerService{
		repo:     repo,
		receipts: receipts,
		now:      func() time.Time { return fixed },
	}, repo
}

func draftAnswer() *types.OfficialAnswer {
	answer := &types.OfficialAnswer{
		Question:   "E45 报警怎么处理？",
		Answer:     "先检查 BMS 通信线束，确认无松动后复位。",
		Conditions: "STACK-01 V6.2 及以上",
		Exceptions: "若伴随过温报警，先停机降温",
		Scope:      "STACK-01 / 现场",
		RiskLevel:  string(types.ApprovalRiskHigh),
		OwnerID:    "owner-1",
	}
	answer.SetEvidenceIDs([]string{"doc-1"})
	return answer
}

// TestOfficialAnswerLifecycle covers the §13.7 path from draft to published.
func TestOfficialAnswerLifecycle(t *testing.T) {
	svc, _ := newOfficialAnswerServiceForTest(&stubAnswerReceipts{approved: true})
	ctx := context.Background()

	answer, err := svc.Save(ctx, 7, draftAnswer(), "author-1")
	require.NoError(t, err)
	assert.Equal(t, string(types.OfficialAnswerDraft), answer.Status)

	answer, err = svc.SubmitReview(ctx, 7, answer.ID, "author-1")
	require.NoError(t, err)
	assert.Equal(t, string(types.OfficialAnswerInReview), answer.Status)

	answer, err = svc.Approve(ctx, 7, answer.ID, "reviewer-1", "rcpt_ok")
	require.NoError(t, err)
	assert.Equal(t, string(types.OfficialAnswerApproved), answer.Status)

	answer, err = svc.Publish(ctx, 7, answer.ID, "admin-1")
	require.NoError(t, err)
	assert.Equal(t, string(types.OfficialAnswerPublished), answer.Status)
	assert.Equal(t, 1, answer.Version)
}

// TestOfficialAnswerRequiresEvidence is the §13.7 hard rule: a standard answer
// with no source cannot be audited or withdrawn.
func TestOfficialAnswerRequiresEvidence(t *testing.T) {
	svc, _ := newOfficialAnswerServiceForTest(&stubAnswerReceipts{approved: true})
	ctx := context.Background()
	answer := draftAnswer()
	answer.SetEvidenceIDs(nil)

	saved, err := svc.Save(ctx, 7, answer, "author-1")
	require.NoError(t, err)
	_, err = svc.SubmitReview(ctx, 7, saved.ID, "author-1")
	require.ErrorIs(t, err, ErrOfficialAnswerEvidenceRequired)
}

// TestHighRiskAnswerRequiresReceipt covers the §13.7 gate for high-risk content.
func TestHighRiskAnswerRequiresReceipt(t *testing.T) {
	svc, _ := newOfficialAnswerServiceForTest(&stubAnswerReceipts{approved: true})
	ctx := context.Background()
	saved, err := svc.Save(ctx, 7, draftAnswer(), "author-1")
	require.NoError(t, err)
	_, err = svc.SubmitReview(ctx, 7, saved.ID, "author-1")
	require.NoError(t, err)

	_, err = svc.Approve(ctx, 7, saved.ID, "reviewer-1", "")
	require.ErrorIs(t, err, ErrOfficialAnswerReceiptRequired)

	// A rejected receipt does not count either.
	rejectedSvc, _ := newOfficialAnswerServiceForTest(&stubAnswerReceipts{approved: false})
	saved2, err := rejectedSvc.Save(ctx, 7, draftAnswer(), "author-1")
	require.NoError(t, err)
	_, err = rejectedSvc.SubmitReview(ctx, 7, saved2.ID, "author-1")
	require.NoError(t, err)
	_, err = rejectedSvc.Approve(ctx, 7, saved2.ID, "reviewer-1", "rcpt_bad")
	require.ErrorIs(t, err, ErrOfficialAnswerReceiptRequired)
}

// TestAuthorCannotApproveOwnAnswer covers the four-eyes rule on this artifact too.
func TestAuthorCannotApproveOwnAnswer(t *testing.T) {
	svc, _ := newOfficialAnswerServiceForTest(&stubAnswerReceipts{approved: true})
	ctx := context.Background()
	saved, err := svc.Save(ctx, 7, draftAnswer(), "author-1")
	require.NoError(t, err)
	_, err = svc.SubmitReview(ctx, 7, saved.ID, "author-1")
	require.NoError(t, err)
	_, err = svc.Approve(ctx, 7, saved.ID, "author-1", "rcpt_ok")
	require.ErrorIs(t, err, ErrReviewSelfDecision)
}

// TestEditingPublishedAnswerResetsToDraft is the important safety property: an
// approved text must never change under its existing receipt.
func TestEditingPublishedAnswerResetsToDraft(t *testing.T) {
	svc, _ := newOfficialAnswerServiceForTest(&stubAnswerReceipts{approved: true})
	ctx := context.Background()
	saved, err := svc.Save(ctx, 7, draftAnswer(), "author-1")
	require.NoError(t, err)
	_, err = svc.SubmitReview(ctx, 7, saved.ID, "author-1")
	require.NoError(t, err)
	_, err = svc.Approve(ctx, 7, saved.ID, "reviewer-1", "rcpt_ok")
	require.NoError(t, err)
	published, err := svc.Publish(ctx, 7, saved.ID, "admin-1")
	require.NoError(t, err)

	edited := *published
	edited.Answer = "改过的正文"
	updated, err := svc.Save(ctx, 7, &edited, "author-1")
	require.NoError(t, err)
	assert.Equal(t, string(types.OfficialAnswerDraft), updated.Status)
	assert.Empty(t, updated.ReceiptID, "the old receipt no longer certifies this text")
}

// TestDuplicateActiveAnswerRejected keeps two standard answers from coexisting
// for the same question.
func TestDuplicateActiveAnswerRejected(t *testing.T) {
	svc, _ := newOfficialAnswerServiceForTest(&stubAnswerReceipts{approved: true})
	ctx := context.Background()

	first, err := svc.Save(ctx, 7, draftAnswer(), "author-1")
	require.NoError(t, err)
	_, err = svc.SubmitReview(ctx, 7, first.ID, "author-1")
	require.NoError(t, err)
	_, err = svc.Approve(ctx, 7, first.ID, "reviewer-1", "rcpt_ok")
	require.NoError(t, err)
	_, err = svc.Publish(ctx, 7, first.ID, "admin-1")
	require.NoError(t, err)

	second, err := svc.Save(ctx, 7, draftAnswer(), "author-2")
	require.NoError(t, err)
	_, err = svc.SubmitReview(ctx, 7, second.ID, "author-2")
	require.NoError(t, err)
	_, err = svc.Approve(ctx, 7, second.ID, "reviewer-2", "rcpt_ok2")
	require.NoError(t, err)
	_, err = svc.Publish(ctx, 7, second.ID, "admin-1")
	require.ErrorIs(t, err, ErrOfficialAnswerDuplicate)
}

// TestLookupResolvesValidityWindow distinguishes "no answer" from "expired".
func TestLookupResolvesValidityWindow(t *testing.T) {
	svc, repo := newOfficialAnswerServiceForTest(&stubAnswerReceipts{approved: true})
	ctx := context.Background()
	past := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	answer := draftAnswer()
	answer.Status = string(types.OfficialAnswerPublished)
	answer.ExpiresAt = &past
	answer.QuestionNormalized = types.NormalizeOfficialAnswerQuestion(answer.Question)
	require.NoError(t, repo.Create(ctx, answer))

	match, err := svc.Lookup(ctx, 7, "E45 报警怎么处理？", "")
	require.NoError(t, err)
	require.NotNil(t, match, "an expired answer is still reported, not hidden")
	assert.Equal(t, types.OfficialAnswerExpired, match.EffectiveStatus)

	// A question with no answer at all yields nil, not an error.
	missing, err := svc.Lookup(ctx, 7, "完全无关的问题", "")
	require.NoError(t, err)
	assert.Nil(t, missing)
}

// TestDemoteDueExpiresPublishedAnswers covers the automatic degradation.
func TestDemoteDueExpiresPublishedAnswers(t *testing.T) {
	svc, repo := newOfficialAnswerServiceForTest(&stubAnswerReceipts{approved: true})
	ctx := context.Background()
	past := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	answer := draftAnswer()
	answer.Status = string(types.OfficialAnswerPublished)
	answer.ExpiresAt = &past
	require.NoError(t, repo.Create(ctx, answer))

	demoted, err := svc.DemoteDue(ctx, 7)
	require.NoError(t, err)
	assert.Equal(t, 1, demoted)
	assert.Equal(t, string(types.OfficialAnswerExpired), repo.rows[answer.ID].Status)
}

// TestCheckEvidenceDemotesOnWithdrawal covers "证据撤回后答案自动降级".
func TestCheckEvidenceDemotesOnWithdrawal(t *testing.T) {
	svc, repo := newOfficialAnswerServiceForTest(&stubAnswerReceipts{approved: true})
	ctx := context.Background()
	answer := draftAnswer()
	answer.Status = string(types.OfficialAnswerPublished)
	require.NoError(t, repo.Create(ctx, answer))

	demoted, err := svc.CheckEvidence(ctx, 7, "doc-1")
	require.NoError(t, err)
	assert.Equal(t, 1, demoted)
	assert.Equal(t, string(types.OfficialAnswerExpired), repo.rows[answer.ID].Status)

	// An unrelated knowledge withdrawal leaves the answer alone.
	other := draftAnswer()
	other.Status = string(types.OfficialAnswerPublished)
	require.NoError(t, repo.Create(ctx, other))
	demoted, err = svc.CheckEvidence(ctx, 7, "doc-unrelated")
	require.NoError(t, err)
	assert.Zero(t, demoted)
}

// TestSubmitWithoutReviewStateRejected covers the state machine.
func TestSubmitWithoutReviewStateRejected(t *testing.T) {
	svc, _ := newOfficialAnswerServiceForTest(&stubAnswerReceipts{approved: true})
	ctx := context.Background()
	saved, err := svc.Save(ctx, 7, draftAnswer(), "author-1")
	require.NoError(t, err)
	_, err = svc.Publish(ctx, 7, saved.ID, "admin-1")
	require.ErrorIs(t, err, ErrOfficialAnswerInvalidState,
		"a draft cannot be published without approval")
}

// TestNormalizeOfficialAnswerQuestion covers the lookup key.
func TestNormalizeOfficialAnswerQuestion(t *testing.T) {
	assert.Equal(t, "e45 报警怎么处理",
		types.NormalizeOfficialAnswerQuestion("  E45   报警怎么处理？ "))
	assert.Equal(t,
		types.NormalizeOfficialAnswerQuestion("E45 报警"),
		types.NormalizeOfficialAnswerQuestion("e45 报警?"))
}

// TestOfficialAnswerStateTransitions pins the state machine's boundaries.
func TestOfficialAnswerStateTransitions(t *testing.T) {
	assert.True(t, types.OfficialAnswerDraft.CanTransitionTo(types.OfficialAnswerInReview))
	assert.False(t, types.OfficialAnswerDraft.CanTransitionTo(types.OfficialAnswerPublished),
		"a draft must not skip review")
	assert.True(t, types.OfficialAnswerPublished.CanTransitionTo(types.OfficialAnswerSuperseded))
	assert.False(t, types.OfficialAnswerRetired.CanTransitionTo(types.OfficialAnswerPublished),
		"a retired answer is terminal")
}
