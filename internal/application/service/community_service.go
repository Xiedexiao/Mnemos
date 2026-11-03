package service

import (
	"context"
	"errors"
	"strings"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// 问答社区的业务哨兵错误。
var (
	// ErrCommunityQuestionNotFound 问题不存在（或不在本租户）。
	ErrCommunityQuestionNotFound = errors.New("community: question not found")
	// ErrCommunityAnswerNotFound 回答不存在。
	ErrCommunityAnswerNotFound = errors.New("community: answer not found")
	// ErrCommunityTitleRequired 标题必填。
	ErrCommunityTitleRequired = errors.New("community: question title is required")
	// ErrCommunityBodyRequired 正文必填。
	ErrCommunityBodyRequired = errors.New("community: body is required")
	// ErrCommunityNotAsker 只有提问者可以采纳答案。
	ErrCommunityNotAsker = errors.New("community: only the asker can accept an answer")
	// ErrCommunityInvalidBounty 悬赏积分越界。
	ErrCommunityInvalidBounty = errors.New("community: bounty must be between 0 and 10000")
)

// communityService implements interfaces.CommunityService (KnowAxis V3.0 §15.6).
type communityService struct {
	repo interfaces.CommunityRepository
}

// NewCommunityService constructs the community service.
func NewCommunityService(repo interfaces.CommunityRepository) interfaces.CommunityService {
	return &communityService{repo: repo}
}

// Ask posts a question.
func (s *communityService) Ask(
	ctx context.Context, tenantID uint64, askedBy string, question *types.CommunityQuestion,
) (*types.CommunityQuestion, error) {
	if question == nil {
		return nil, apperrors.NewBadRequestError("question is required")
	}
	question.Title = types.NormalizeCommunityTitle(question.Title)
	if question.Title == "" {
		return nil, apperrors.NewBadRequestError("question title is required")
	}
	if strings.TrimSpace(question.Body) == "" {
		return nil, apperrors.NewBadRequestError("question body is required")
	}
	if !types.ValidateCommunityBounty(question.Bounty) {
		return nil, apperrors.NewBadRequestError("bounty must be between 0 and 10000")
	}
	question.TenantID = tenantID
	question.AskedBy = askedBy
	question.Status = string(types.CommunityQuestionOpen)
	if err := s.repo.CreateQuestion(ctx, question); err != nil {
		return nil, err
	}
	return question, nil
}

// Answer posts an answer and flips the question to answered.
//
// 状态推进放在 Answer 而不是 Accept：一旦有人回答，问题就不再是"无人应答"，
// 列表排序与筛选都依赖这个状态，等到采纳才更新会让"已回答"的判断失真。
func (s *communityService) Answer(
	ctx context.Context, tenantID uint64, questionID, answeredBy, body string,
) (*types.CommunityAnswer, error) {
	if strings.TrimSpace(body) == "" {
		return nil, apperrors.NewBadRequestError("answer body is required")
	}
	question, err := s.repo.GetQuestion(ctx, tenantID, questionID)
	if err != nil {
		return nil, s.mapQuestionError(err)
	}
	answer := &types.CommunityAnswer{
		TenantID:   tenantID,
		QuestionID: questionID,
		Body:       strings.TrimSpace(body),
		AnsweredBy: answeredBy,
	}
	if err := s.repo.CreateAnswer(ctx, answer); err != nil {
		return nil, err
	}
	if types.CommunityQuestionStatus(question.Status) == types.CommunityQuestionOpen {
		question.Status = string(types.CommunityQuestionAnswered)
		if uerr := s.repo.UpdateQuestion(ctx, question); uerr != nil {
			// 状态没推进不影响答案本身：答案已经落库，下一次读取会重新计算。
			return answer, nil
		}
	}
	return answer, nil
}

// Vote marks an answer helpful.
func (s *communityService) Vote(
	ctx context.Context, tenantID uint64, answerID string,
) (*types.CommunityAnswer, error) {
	answer, err := s.repo.VoteAnswer(ctx, tenantID, answerID)
	if err != nil {
		return nil, s.mapAnswerError(err)
	}
	return answer, nil
}

// Accept adopts an answer. Only the asker may accept.
func (s *communityService) Accept(
	ctx context.Context, tenantID uint64, questionID, answerID, actorID string,
) (*types.CommunityQuestion, error) {
	question, err := s.repo.GetQuestion(ctx, tenantID, questionID)
	if err != nil {
		return nil, s.mapQuestionError(err)
	}
	// 采纳权属于提问者：别人采纳会把"哪个答案被认可"变成第三方判断。
	if actorID != question.AskedBy {
		return nil, apperrors.NewForbiddenError("only the asker can accept an answer")
	}
	answer, err := s.repo.GetAnswer(ctx, tenantID, answerID)
	if err != nil {
		return nil, s.mapAnswerError(err)
	}
	if answer.QuestionID != questionID {
		return nil, apperrors.NewBadRequestError("the answer does not belong to this question")
	}
	if err := s.repo.AcceptAnswer(ctx, tenantID, questionID, answerID); err != nil {
		return nil, err
	}
	question.AcceptedAnswerID = answerID
	question.Status = string(types.CommunityQuestionResolved)
	if err := s.repo.UpdateQuestion(ctx, question); err != nil {
		return nil, err
	}
	question.Answers, _ = s.repo.ListAnswers(ctx, tenantID, questionID)
	return question, nil
}

// Get returns one question with its answers.
func (s *communityService) Get(
	ctx context.Context, tenantID uint64, questionID string,
) (*types.CommunityQuestion, error) {
	question, err := s.repo.GetQuestion(ctx, tenantID, questionID)
	if err != nil {
		return nil, s.mapQuestionError(err)
	}
	answers, err := s.repo.ListAnswers(ctx, tenantID, questionID)
	if err != nil {
		return nil, err
	}
	question.Answers = answers
	return question, nil
}

// List returns questions.
func (s *communityService) List(
	ctx context.Context, tenantID uint64, status types.CommunityQuestionStatus, limit int,
) ([]*types.CommunityQuestion, error) {
	if status != "" && !status.Valid() {
		return nil, apperrors.NewBadRequestError("unknown question status")
	}
	return s.repo.ListQuestions(ctx, tenantID, status, limit)
}

// Report files a moderation report.
func (s *communityService) Report(
	ctx context.Context, tenantID uint64, targetType, targetID, reason, reportedBy string,
) (*types.CommunityReport, error) {
	targetType = strings.TrimSpace(targetType)
	if targetType != "question" && targetType != "answer" {
		return nil, apperrors.NewBadRequestError("target_type must be question or answer")
	}
	if strings.TrimSpace(targetID) == "" {
		return nil, apperrors.NewBadRequestError("target_id is required")
	}
	report := &types.CommunityReport{
		TenantID:   tenantID,
		TargetType: targetType,
		TargetID:   targetID,
		Reason:     types.NormalizeRelationNote(reason),
		ReportedBy: reportedBy,
		Status:     string(types.CommunityReportOpen),
	}
	if err := s.repo.CreateReport(ctx, report); err != nil {
		return nil, err
	}
	return report, nil
}

// ListReports returns the moderation queue.
func (s *communityService) ListReports(
	ctx context.Context, tenantID uint64, status types.CommunityReportStatus, limit int,
) ([]*types.CommunityReport, error) {
	if status != "" && !status.Valid() {
		return nil, apperrors.NewBadRequestError("unknown report status")
	}
	return s.repo.ListReports(ctx, tenantID, status, limit)
}

// ResolveReport records a moderator decision and closes removed questions.
func (s *communityService) ResolveReport(
	ctx context.Context, tenantID uint64, reportID string,
	status types.CommunityReportStatus, actorID string,
) (*types.CommunityReport, error) {
	if status != types.CommunityReportDismissed && status != types.CommunityReportRemoved {
		return nil, apperrors.NewBadRequestError("status must be dismissed or removed")
	}
	report, err := s.repo.ResolveReport(ctx, tenantID, reportID, status, actorID)
	if err != nil {
		return nil, err
	}
	// 判定移除时把被举报的问题关掉：只标记举报状态会让违规内容继续可见。
	if status == types.CommunityReportRemoved && report.TargetType == "question" {
		if question, qerr := s.repo.GetQuestion(ctx, tenantID, report.TargetID); qerr == nil && question != nil {
			question.Status = string(types.CommunityQuestionClosed)
			_ = s.repo.UpdateQuestion(ctx, question)
		}
	}
	return report, nil
}

// MarkConverted records that the Q&A became formal knowledge.
func (s *communityService) MarkConverted(
	ctx context.Context, tenantID uint64, questionID, knowledgeID, actorID string,
) (*types.CommunityQuestion, error) {
	question, err := s.repo.GetQuestion(ctx, tenantID, questionID)
	if err != nil {
		return nil, s.mapQuestionError(err)
	}
	question.ConvertedKnowledgeID = knowledgeID
	question.Status = string(types.CommunityQuestionClosed)
	if err := s.repo.UpdateQuestion(ctx, question); err != nil {
		return nil, err
	}
	return question, nil
}

// mapQuestionError normalises a repository miss.
func (s *communityService) mapQuestionError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "not found") {
		return ErrCommunityQuestionNotFound
	}
	return err
}

// mapAnswerError normalises a repository miss.
func (s *communityService) mapAnswerError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "not found") {
		return ErrCommunityAnswerNotFound
	}
	return err
}
