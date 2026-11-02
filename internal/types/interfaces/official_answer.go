package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// OfficialAnswerRepository persists approved standard answers
// (KnowAxis V3.0 §13.7).
type OfficialAnswerRepository interface {
	// Create inserts one official answer.
	Create(ctx context.Context, answer *types.OfficialAnswer) error
	// Update persists the whole row.
	Update(ctx context.Context, answer *types.OfficialAnswer) error
	// GetByID returns one answer within the tenant.
	GetByID(ctx context.Context, tenantID uint64, answerID string) (*types.OfficialAnswer, error)
	// List returns answers of one tenant, newest first.
	List(
		ctx context.Context, tenantID uint64, status types.OfficialAnswerStatus, limit int,
	) ([]*types.OfficialAnswer, error)
	// FindAnswerable looks up the answer for a normalized question in one space.
	FindAnswerable(
		ctx context.Context, tenantID uint64, normalizedQuestion, kbID string,
	) (*types.OfficialAnswer, error)
	// FindActiveByQuestion returns the live answer ignoring the space scope.
	FindActiveByQuestion(
		ctx context.Context, tenantID uint64, normalizedQuestion string,
	) (*types.OfficialAnswer, error)
	// ListExpiredDue returns published answers past their expiry.
	ListExpiredDue(
		ctx context.Context, tenantID uint64, now time.Time, limit int,
	) ([]*types.OfficialAnswer, error)
}

// OfficialAnswerService is the §13.7 official-answer lifecycle: drafting, the
// approval gate, publishing, lookup for the answer path, and the automatic
// demotion when an answer goes stale.
type OfficialAnswerService interface {
	// Save creates or edits a draft. Editing a published answer puts it back
	// into review so an approved text is never changed silently.
	Save(
		ctx context.Context, tenantID uint64, answer *types.OfficialAnswer, actorID string,
	) (*types.OfficialAnswer, error)
	// SubmitReview moves a draft into review.
	SubmitReview(ctx context.Context, tenantID uint64, answerID, actorID string) (*types.OfficialAnswer, error)
	// Approve records the approval. High-risk answers require a receipt ID.
	Approve(
		ctx context.Context, tenantID uint64, answerID, approverID, receiptID string,
	) (*types.OfficialAnswer, error)
	// Publish makes an approved answer live, binding the approval receipt.
	Publish(
		ctx context.Context, tenantID uint64, answerID, actorID string,
	) (*types.OfficialAnswer, error)
	// Retire withdraws an answer (§13.7 替代/下线).
	Retire(ctx context.Context, tenantID uint64, answerID, actorID, supersededByID string) (*types.OfficialAnswer, error)
	// Get returns one answer with its resolved state.
	Get(ctx context.Context, tenantID uint64, answerID string) (*types.OfficialAnswer, error)
	// List returns answers of one tenant.
	List(
		ctx context.Context, tenantID uint64, status types.OfficialAnswerStatus, limit int,
	) ([]*types.OfficialAnswer, error)
	// Lookup answers a question from the official-answer set. A nil match means
	// "no official answer", which is different from an expired one.
	Lookup(
		ctx context.Context, tenantID uint64, question, kbID string,
	) (*types.OfficialAnswerMatch, error)
	// DemoteDue expires published answers whose window ended, returning how many
	// were demoted.
	DemoteDue(ctx context.Context, tenantID uint64) (int, error)
	// CheckEvidence demotes answers whose evidence knowledge is no longer
	// available (§13.7 证据失效时自动降级并通知责任人).
	CheckEvidence(ctx context.Context, tenantID uint64, knowledgeID string) (int, error)
}
