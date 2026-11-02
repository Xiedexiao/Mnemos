package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// KnowledgeACLRepository persists per-document ACL rows (V3.0 §2.1) and
// answers the deny-wins authorization queries used before recall and after it.
type KnowledgeACLRepository interface {
	// UpsertGrant creates or updates one (knowledge, subject, action) grant.
	UpsertGrant(ctx context.Context, grant *types.KnowledgeACL) error
	// ListByKnowledge returns all ACL rows of one knowledge entry.
	ListByKnowledge(ctx context.Context, knowledgeID string) ([]*types.KnowledgeACL, error)
	// DeleteByKnowledge removes every ACL row of one knowledge entry and
	// returns the number of rows removed. Used by "恢复继承"/cross-space move,
	// where the document must fall back to the target space's policy instead
	// of carrying its old grants along (V3.0 §2.3 移动/复制不得静默获得更宽权限).
	DeleteByKnowledge(ctx context.Context, knowledgeID string) (int64, error)
	// Revoke deletes one grant row by ID (within tenant).
	Revoke(ctx context.Context, tenantID uint64, grantID string) error
	// GetByID returns one grant within the tenant. Revocation reads it first so
	// the propagation ledger can name the affected document and subject after
	// the row is deleted.
	GetByID(ctx context.Context, tenantID uint64, grantID string) (*types.KnowledgeACL, error)
	// DeniedKnowledgeIDs returns the IDs of entries in the given knowledge
	// bases that the subject must NOT access for the action:
	//   - any deny row for (user, action) wins, and
	//   - classification secret/confidential without an explicit allow row is
	//     denied (fail-closed, V3.0 §2.4 密级与派生权限).
	// Public/internal entries are allowed by KB-level permission upstream and
	// only excluded when a deny row hits.
	DeniedKnowledgeIDs(
		ctx context.Context,
		knowledgeBaseIDs []string,
		subjectType types.KnowledgeACLSubjectType,
		subjectID string,
		action types.PermissionAction,
		now time.Time,
	) ([]string, error)
}

// KnowledgeACLService manages document-level grants and exposes the
// authorization verdicts used by retrieval and the reading page.
type KnowledgeACLService interface {
	// Grant creates or updates one ACL row (Admin+ routes enforce 不能自授
	// and the authorize action ceiling).
	Grant(ctx context.Context, grant *types.KnowledgeACL) (*types.KnowledgeACL, error)
	// ListByKnowledge returns all grants of one knowledge entry.
	ListByKnowledge(ctx context.Context, knowledgeID string) ([]*types.KnowledgeACL, error)
	// Revoke deletes one grant (within tenant); actorID feeds the audit event.
	Revoke(ctx context.Context, tenantID uint64, grantID, actorID string) error
	// DeniedKnowledgeIDs answers the deny-wins query for retrieval filtering.
	DeniedKnowledgeIDs(
		ctx context.Context,
		knowledgeBaseIDs []string,
		subjectID string,
		action types.PermissionAction,
	) ([]string, error)
	// MovePreview reports who gains and who loses access when knowledge moves
	// from one space to another (V3.0 §2.3 移动前展示"谁将获得/失去访问").
	// It is a read-only projection: nothing is written.
	MovePreview(
		ctx context.Context,
		knowledgeID, sourceKBID, targetKBID string,
	) (*types.KnowledgeMoveAccessPreview, error)
}
