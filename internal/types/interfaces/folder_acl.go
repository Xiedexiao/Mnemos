package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// FolderACLRepository persists directory-level grants and the governance
// findings produced by ceiling checks (KnowAxis V3.0 §2.3 / §4.1).
type FolderACLRepository interface {
	// UpsertGrant creates or updates one folder grant.
	UpsertGrant(ctx context.Context, grant *types.FolderACL) error
	// ListByKnowledgeBase returns every folder grant of one space.
	ListByKnowledgeBase(ctx context.Context, tenantID uint64, kbID string) ([]*types.FolderACL, error)
	// ListForFolder returns the grants applying to one folder (its own + ancestors).
	ListForFolder(
		ctx context.Context, tenantID uint64, kbID, folderPath string,
	) ([]*types.FolderACL, error)
	// Revoke deletes one folder grant.
	Revoke(ctx context.Context, tenantID uint64, grantID string) error
	// DeleteByFolder removes every grant attached to one folder path.
	DeleteByFolder(ctx context.Context, tenantID uint64, kbID, folderPath string) (int64, error)
	// RecordCeilingViolation appends one governance finding.
	RecordCeilingViolation(ctx context.Context, violation *types.ACLCeilingViolation) error
	// ListCeilingViolations returns the governance findings of one tenant.
	ListCeilingViolations(ctx context.Context, tenantID uint64, limit int) ([]*types.ACLCeilingViolation, error)
}

// FolderACLService is the directory-permission surface: grants, the folded
// verdict for a path, and the ceiling checks that keep child grants from
// exceeding their parents.
type FolderACLService interface {
	// Grant creates or updates a folder grant, rejecting one that breaches the
	// parent ceiling (the rejection is recorded as a governance finding).
	Grant(ctx context.Context, grant *types.FolderACL) (*types.FolderACL, error)
	// List returns every folder grant of a space.
	List(ctx context.Context, tenantID uint64, kbID string) ([]*types.FolderACL, error)
	// Revoke deletes one grant.
	Revoke(ctx context.Context, tenantID uint64, grantID, actorID string) error
	// ResolveForFolder folds the grants that apply to one folder path.
	ResolveForFolder(
		ctx context.Context, tenantID uint64, kbID, folderPath string,
	) (*types.FolderACLDecision, error)
	// OnFolderMoved resets descendant overrides when a folder is moved, because
	// an inherited grant from the old parent must not follow it (§2.3 目录移动
	// 触发权限重算).
	OnFolderMoved(ctx context.Context, tenantID uint64, kbID, fromPath, toPath string) (int64, error)
	// ListCeilingViolations returns the governance findings.
	ListCeilingViolations(
		ctx context.Context, tenantID uint64, limit int,
	) ([]*types.ACLCeilingViolation, error)
}
