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

// stubCommunityRepo is an in-memory community store.
type stubCommunityRepo struct {
	interfaces.CommunityRepository
	questions []*types.CommunityQuestion
	answers   []*types.CommunityAnswer
	reports   []*types.CommunityReport
}

func newStubCommunityRepo() *stubCommunityRepo {
	return &stubCommunityRepo{}
}

func (s *stubCommunityRepo) CreateQuestion(_ context.Context, question *types.CommunityQuestion) error {
	question.ID = "q-" + itoaSmall(len(s.questions)+1)
	s.questions = append(s.questions, question)
	return nil
}

func (s *stubCommunityRepo) GetQuestion(
	_ context.Context, _ uint64, questionID string,
) (*types.CommunityQuestion, error) {
	for _, question := range s.questions {
		if question.ID == questionID {
			return question, nil
		}
	}
	return nil, ErrCommunityQuestionNotFound
}

func (s *stubCommunityRepo) ListQuestions(
	_ context.Context, _ uint64, status types.CommunityQuestionStatus, _ int,
) ([]*types.CommunityQuestion, error) {
	out := make([]*types.CommunityQuestion, 0, len(s.questions))
	for _, question := range s.questions {
		if status != "" && question.Status != string(status) {
			continue
		}
		out = append(out, question)
	}
	return out, nil
}

func (s *stubCommunityRepo) UpdateQuestion(_ context.Context, question *types.CommunityQuestion) error {
	return nil
}

func (s *stubCommunityRepo) CreateAnswer(_ context.Context, answer *types.CommunityAnswer) error {
	answer.ID = "a-" + itoaSmall(len(s.answers)+1)
	s.answers = append(s.answers, answer)
	return nil
}

func (s *stubCommunityRepo) GetAnswer(
	_ context.Context, _ uint64, answerID string,
) (*types.CommunityAnswer, error) {
	for _, answer := range s.answers {
		if answer.ID == answerID {
			return answer, nil
		}
	}
	return nil, ErrCommunityAnswerNotFound
}

func (s *stubCommunityRepo) ListAnswers(
	_ context.Context, _ uint64, questionID string,
) ([]*types.CommunityAnswer, error) {
	out := make([]*types.CommunityAnswer, 0, len(s.answers))
	for _, answer := range s.answers {
		if answer.QuestionID == questionID {
			out = append(out, answer)
		}
	}
	return out, nil
}

func (s *stubCommunityRepo) VoteAnswer(
	_ context.Context, tenantID uint64, answerID string,
) (*types.CommunityAnswer, error) {
	answer, err := s.GetAnswer(context.Background(), tenantID, answerID)
	if err != nil {
		return nil, err
	}
	answer.Votes++
	return answer, nil
}

func (s *stubCommunityRepo) AcceptAnswer(
	_ context.Context, _ uint64, questionID, answerID string,
) error {
	for _, answer := range s.answers {
		if answer.QuestionID != questionID {
			continue
		}
		answer.Accepted = answer.ID == answerID
	}
	return nil
}

func (s *stubCommunityRepo) CreateReport(_ context.Context, report *types.CommunityReport) error {
	report.ID = "r-" + itoaSmall(len(s.reports)+1)
	s.reports = append(s.reports, report)
	return nil
}

func (s *stubCommunityRepo) ListReports(
	_ context.Context, _ uint64, status types.CommunityReportStatus, _ int,
) ([]*types.CommunityReport, error) {
	out := make([]*types.CommunityReport, 0, len(s.reports))
	for _, report := range s.reports {
		if status != "" && report.Status != string(status) {
			continue
		}
		out = append(out, report)
	}
	return out, nil
}

func (s *stubCommunityRepo) ResolveReport(
	_ context.Context, _ uint64, reportID string,
	status types.CommunityReportStatus, actorID string,
) (*types.CommunityReport, error) {
	for _, report := range s.reports {
		if report.ID == reportID {
			report.Status = string(status)
			report.HandledBy = actorID
			return report, nil
		}
	}
	return nil, assert.AnError
}

func (s *stubCommunityRepo) CountAnswers(
	_ context.Context, _ uint64, questionID string,
) (int64, error) {
	count := int64(0)
	for _, answer := range s.answers {
		if answer.QuestionID == questionID {
			count++
		}
	}
	return count, nil
}

// TestAskRequiresTitleAndBody covers input validation.
func TestAskRequiresTitleAndBody(t *testing.T) {
	svc := NewCommunityService(newStubCommunityRepo())
	_, err := svc.Ask(context.Background(), 7, "user-1", &types.CommunityQuestion{Body: "x"})
	require.Error(t, err)
	_, err = svc.Ask(context.Background(), 7, "user-1", &types.CommunityQuestion{Title: "t"})
	require.Error(t, err)
}

// TestAskRejectsOutOfRangeBounty covers the bounty bounds.
func TestAskRejectsOutOfRangeBounty(t *testing.T) {
	svc := NewCommunityService(newStubCommunityRepo())
	_, err := svc.Ask(context.Background(), 7, "user-1", &types.CommunityQuestion{
		Title: "怎么处理 E45", Body: "现场报警", Bounty: -1,
	})
	require.Error(t, err)
	_, err = svc.Ask(context.Background(), 7, "user-1", &types.CommunityQuestion{
		Title: "怎么处理 E45", Body: "现场报警", Bounty: 999999,
	})
	require.Error(t, err)
}

// TestAnswerMovesQuestionToAnswered covers the status transition: once someone
// answers, the question is no longer "unanswered" for listing and filtering.
func TestAnswerMovesQuestionToAnswered(t *testing.T) {
	repo := newStubCommunityRepo()
	svc := NewCommunityService(repo)
	ctx := context.Background()

	question, err := svc.Ask(ctx, 7, "asker", &types.CommunityQuestion{Title: "t", Body: "b"})
	require.NoError(t, err)
	assert.Equal(t, string(types.CommunityQuestionOpen), question.Status)

	_, err = svc.Answer(ctx, 7, question.ID, "helper", "先检查线束")
	require.NoError(t, err)
	assert.Equal(t, string(types.CommunityQuestionAnswered), repo.questions[0].Status)
}

// TestOnlyAskerCanAccept covers the adoption rule.
func TestOnlyAskerCanAccept(t *testing.T) {
	repo := newStubCommunityRepo()
	svc := NewCommunityService(repo)
	ctx := context.Background()
	question, err := svc.Ask(ctx, 7, "asker", &types.CommunityQuestion{Title: "t", Body: "b"})
	require.NoError(t, err)
	answer, err := svc.Answer(ctx, 7, question.ID, "helper", "答案")
	require.NoError(t, err)

	_, err = svc.Accept(ctx, 7, question.ID, answer.ID, "someone-else")
	require.Error(t, err, "only the asker may adopt an answer")

	updated, err := svc.Accept(ctx, 7, question.ID, answer.ID, "asker")
	require.NoError(t, err)
	assert.Equal(t, string(types.CommunityQuestionResolved), updated.Status)
	assert.Equal(t, answer.ID, updated.AcceptedAnswerID)
}

// TestAcceptRejectsAnswerFromAnotherQuestion keeps the adoption bound to the
// question it was asked under.
func TestAcceptRejectsAnswerFromAnotherQuestion(t *testing.T) {
	repo := newStubCommunityRepo()
	svc := NewCommunityService(repo)
	ctx := context.Background()
	first, err := svc.Ask(ctx, 7, "asker", &types.CommunityQuestion{Title: "t1", Body: "b"})
	require.NoError(t, err)
	second, err := svc.Ask(ctx, 7, "asker", &types.CommunityQuestion{Title: "t2", Body: "b"})
	require.NoError(t, err)
	answer, err := svc.Answer(ctx, 7, second.ID, "helper", "答案")
	require.NoError(t, err)

	_, err = svc.Accept(ctx, 7, first.ID, answer.ID, "asker")
	require.Error(t, err)
}

// TestVoteIncrementsHelpfulCount covers the quality signal.
func TestVoteIncrementsHelpfulCount(t *testing.T) {
	repo := newStubCommunityRepo()
	svc := NewCommunityService(repo)
	ctx := context.Background()
	question, err := svc.Ask(ctx, 7, "asker", &types.CommunityQuestion{Title: "t", Body: "b"})
	require.NoError(t, err)
	answer, err := svc.Answer(ctx, 7, question.ID, "helper", "答案")
	require.NoError(t, err)

	voted, err := svc.Vote(ctx, 7, answer.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, voted.Votes)
}

// TestResolveReportRemovedClosesQuestion covers §15.6 moderation: removing
// reported content must also take it down, not just mark the report.
func TestResolveReportRemovedClosesQuestion(t *testing.T) {
	repo := newStubCommunityRepo()
	svc := NewCommunityService(repo)
	ctx := context.Background()
	question, err := svc.Ask(ctx, 7, "asker", &types.CommunityQuestion{Title: "t", Body: "b"})
	require.NoError(t, err)

	report, err := svc.Report(ctx, 7, "question", question.ID, "广告", "reporter")
	require.NoError(t, err)
	resolved, err := svc.ResolveReport(ctx, 7, report.ID, types.CommunityReportRemoved, "moderator")
	require.NoError(t, err)
	assert.Equal(t, string(types.CommunityReportRemoved), resolved.Status)
	assert.Equal(t, string(types.CommunityQuestionClosed), repo.questions[0].Status,
		"removed content must actually be taken down")
}

// TestResolveReportRejectsPendingStatus keeps `open` from being a resolution.
func TestResolveReportRejectsPendingStatus(t *testing.T) {
	svc := NewCommunityService(newStubCommunityRepo())
	_, err := svc.ResolveReport(context.Background(), 7, "r-1",
		types.CommunityReportOpen, "moderator")
	require.Error(t, err)
}

// TestReportValidatesTargetType covers the whitelist.
func TestReportValidatesTargetType(t *testing.T) {
	svc := NewCommunityService(newStubCommunityRepo())
	_, err := svc.Report(context.Background(), 7, "comment", "x", "理由", "u")
	require.Error(t, err)
	_, err = svc.Report(context.Background(), 7, "question", "", "理由", "u")
	require.Error(t, err)
}

// TestMarkConvertedClosesQuestion covers §15.6 一键转知识.
func TestMarkConvertedClosesQuestion(t *testing.T) {
	repo := newStubCommunityRepo()
	svc := NewCommunityService(repo)
	ctx := context.Background()
	question, err := svc.Ask(ctx, 7, "asker", &types.CommunityQuestion{Title: "t", Body: "b"})
	require.NoError(t, err)

	converted, err := svc.MarkConverted(ctx, 7, question.ID, "doc-1", "admin")
	require.NoError(t, err)
	assert.Equal(t, "doc-1", converted.ConvertedKnowledgeID)
	assert.Equal(t, string(types.CommunityQuestionClosed), converted.Status)
}

// TestLearningStageOrdering covers the 基础→进阶→熟练 ladder.
func TestLearningStageOrdering(t *testing.T) {
	assert.Less(t, types.LearningStageBasic.Rank(), types.LearningStageAdvanced.Rank())
	assert.Less(t, types.LearningStageAdvanced.Rank(), types.LearningStageProficient.Rank())
	assert.Equal(t, types.LearningStageBasic, types.ParseLearningStage(""))
	assert.Equal(t, types.LearningStageBasic, types.ParseLearningStage("bogus"))
	assert.Equal(t, types.LearningStageAdvanced, types.ParseLearningStage("advanced"))
}

// TestCertificateEffectiveStatus covers read-time expiry (§7.1).
func TestCertificateEffectiveStatus(t *testing.T) {
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)

	active := &types.LearningCertificate{Status: string(types.LearningCertificateActive)}
	assert.Equal(t, types.LearningCertificateActive, active.EffectiveStatus(now))
	assert.True(t, active.Active(now))

	expired := &types.LearningCertificate{
		Status: string(types.LearningCertificateActive), ExpiresAt: &past,
	}
	assert.Equal(t, types.LearningCertificateExpired, expired.EffectiveStatus(now))
	assert.False(t, expired.Active(now))

	revoked := &types.LearningCertificate{
		Status: string(types.LearningCertificateRevoked), ExpiresAt: &past,
	}
	assert.Equal(t, types.LearningCertificateRevoked, revoked.EffectiveStatus(now),
		"a revocation outranks expiry")
}
