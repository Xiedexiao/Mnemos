package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// CommunityRepository persists the Q&A community (KnowAxis V3.0 §15.6).
type CommunityRepository interface {
	// CreateQuestion inserts one question.
	CreateQuestion(ctx context.Context, question *types.CommunityQuestion) error
	// GetQuestion returns one question within the tenant.
	GetQuestion(ctx context.Context, tenantID uint64, questionID string) (*types.CommunityQuestion, error)
	// ListQuestions returns questions, optionally filtered by status.
	ListQuestions(
		ctx context.Context, tenantID uint64, status types.CommunityQuestionStatus, limit int,
	) ([]*types.CommunityQuestion, error)
	// UpdateQuestion persists the whole row.
	UpdateQuestion(ctx context.Context, question *types.CommunityQuestion) error
	// CreateAnswer inserts one answer.
	CreateAnswer(ctx context.Context, answer *types.CommunityAnswer) error
	// GetAnswer returns one answer within the tenant.
	GetAnswer(ctx context.Context, tenantID uint64, answerID string) (*types.CommunityAnswer, error)
	// ListAnswers returns a question's answers, accepted first.
	ListAnswers(ctx context.Context, tenantID uint64, questionID string) ([]*types.CommunityAnswer, error)
	// VoteAnswer increments an answer's helpful count.
	VoteAnswer(ctx context.Context, tenantID uint64, answerID string) (*types.CommunityAnswer, error)
	// AcceptAnswer adopts one answer and clears any previous adoption.
	AcceptAnswer(ctx context.Context, tenantID uint64, questionID, answerID string) error
	// CreateReport inserts one moderation report.
	CreateReport(ctx context.Context, report *types.CommunityReport) error
	// ListReports returns the moderation queue.
	ListReports(
		ctx context.Context, tenantID uint64, status types.CommunityReportStatus, limit int,
	) ([]*types.CommunityReport, error)
	// ResolveReport records a moderator decision.
	ResolveReport(
		ctx context.Context, tenantID uint64, reportID string,
		status types.CommunityReportStatus, actorID string,
	) (*types.CommunityReport, error)
	// CountAnswers counts answers to one question.
	CountAnswers(ctx context.Context, tenantID uint64, questionID string) (int64, error)
}

// CommunityService is the §15.6 surface: ask, answer, vote, adopt, convert to
// knowledge and moderate.
type CommunityService interface {
	// Ask posts a question.
	Ask(ctx context.Context, tenantID uint64, askedBy string, question *types.CommunityQuestion) (*types.CommunityQuestion, error)
	// Answer posts an answer and moves the question to answered.
	Answer(ctx context.Context, tenantID uint64, questionID, answeredBy, body string) (*types.CommunityAnswer, error)
	// Vote marks an answer helpful.
	Vote(ctx context.Context, tenantID uint64, answerID string) (*types.CommunityAnswer, error)
	// Accept adopts an answer; only the asker may do it.
	Accept(ctx context.Context, tenantID uint64, questionID, answerID, actorID string) (*types.CommunityQuestion, error)
	// Get returns one question with its answers.
	Get(ctx context.Context, tenantID uint64, questionID string) (*types.CommunityQuestion, error)
	// List returns questions.
	List(
		ctx context.Context, tenantID uint64, status types.CommunityQuestionStatus, limit int,
	) ([]*types.CommunityQuestion, error)
	// Report files a moderation report.
	Report(
		ctx context.Context, tenantID uint64, targetType, targetID, reason, reportedBy string,
	) (*types.CommunityReport, error)
	// ListReports returns the moderation queue.
	ListReports(
		ctx context.Context, tenantID uint64, status types.CommunityReportStatus, limit int,
	) ([]*types.CommunityReport, error)
	// ResolveReport records a moderator decision and, on removal, closes the
	// offending question.
	ResolveReport(
		ctx context.Context, tenantID uint64, reportID string,
		status types.CommunityReportStatus, actorID string,
	) (*types.CommunityReport, error)
	// MarkConverted records that the Q&A was promoted to formal knowledge
	// (§15.6 一键转知识), closing the question.
	MarkConverted(
		ctx context.Context, tenantID uint64, questionID, knowledgeID, actorID string,
	) (*types.CommunityQuestion, error)
}
