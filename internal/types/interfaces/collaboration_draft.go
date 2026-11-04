package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// DraftRepository persists per-user autosaved drafts (§5.4).
type DraftRepository interface {
	// Get returns the draft for one (user, kind, scope), or nil when absent.
	Get(
		ctx context.Context, tenantID uint64, userID string, kind types.DraftKind, scopeID string,
	) (*types.Draft, error)
	// Save writes the draft. When expectedRevision is > 0 and the stored
	// revision differs, it returns the stored row and a conflict flag instead
	// of overwriting — the caller turns that into a 409 with both versions.
	Save(
		ctx context.Context, draft *types.Draft, expectedRevision int,
	) (*types.Draft, bool, error)
	// List returns the user's drafts, newest first.
	List(ctx context.Context, tenantID uint64, userID string, kind string, limit int) ([]*types.Draft, error)
	// Delete removes one draft (after it was committed, or by the user).
	Delete(ctx context.Context, tenantID uint64, userID, draftID string) error
}

// DraftService is the §5.4 autosave surface.
type DraftService interface {
	// Save autosaves one draft; a stale expected_revision returns
	// ErrDraftConflict carrying the current row.
	Save(
		ctx context.Context, tenantID uint64, userID string, input types.DraftSaveInput,
	) (*types.Draft, error)
	// Get returns one draft for the caller.
	Get(
		ctx context.Context, tenantID uint64, userID string, kind types.DraftKind, scopeID string,
	) (*types.Draft, error)
	// List returns the caller's drafts.
	List(ctx context.Context, tenantID uint64, userID string, kind string, limit int) ([]*types.Draft, error)
	// Delete removes one draft.
	Delete(ctx context.Context, tenantID uint64, userID, draftID string) error
}

// MentionResolver validates mentions and delivers them.
//
// 校验放在服务端是因为"这个 id 是不是本工作区的用户""他能不能看这篇知识"
// 只有服务端知道。通知投递失败不影响评论落库，但**未授权**的提及会被剔除
// 而不是静默保留——一条指向无权文档的提及本身就是在传递它的存在。
type MentionResolver interface {
	// Resolve validates a mention list and drops the ones the author may not
	// reference (or that do not exist).
	Resolve(
		ctx context.Context, tenantID uint64, actorID string, mentions []types.Mention,
	) ([]types.Mention, error)
	// Notify delivers the mentions of one comment (best effort).
	Notify(
		ctx context.Context, tenantID uint64, actorID string, knowledgeID, commentID string,
		mentions []types.Mention,
	)
}
