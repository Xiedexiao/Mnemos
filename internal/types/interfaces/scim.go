package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// SCIMService is the SCIM 2.0 provisioning surface (KnowAxis V3.0 §16.4).
//
// 方法名刻意贴近 SCIM 资源动作而不是 DAO 命名：调用方是 IdP 的
// provisioning 客户端，语义必须与 RFC 7644 对齐。
type SCIMService interface {
	// ListUsers returns the tenant's users with SCIM pagination.
	ListUsers(
		ctx context.Context, tenantID uint64, startIndex, count int, filter string,
	) (*types.SCIMListResponse, error)
	// GetUser returns one user by id.
	GetUser(ctx context.Context, tenantID uint64, userID string) (*types.SCIMUser, error)
	// CreateUser provisions one user; an existing userName is a 409.
	CreateUser(
		ctx context.Context, tenantID uint64, actorID string, user *types.SCIMUser,
	) (*types.SCIMUser, error)
	// ReplaceUser overwrites the mutable attributes.
	ReplaceUser(
		ctx context.Context, tenantID uint64, userID, actorID string, user *types.SCIMUser,
	) (*types.SCIMUser, error)
	// PatchUser applies the supported PATCH operations (displayName, active).
	PatchUser(
		ctx context.Context, tenantID uint64, userID, actorID string,
		patch *types.SCIMPatchRequest,
	) (*types.SCIMUser, error)
	// DeleteUser DEACTIVATES the account. Users are referenced by audit rows,
	// approval receipts and versions, so they are never physically removed.
	DeleteUser(ctx context.Context, tenantID uint64, userID, actorID string) error

	// ListGroups returns the tenant's ACL subject groups.
	ListGroups(
		ctx context.Context, tenantID uint64, startIndex, count int, filter string,
	) (*types.SCIMListResponse, error)
	// GetGroup returns one group with its members.
	GetGroup(ctx context.Context, tenantID uint64, groupID string) (*types.SCIMGroup, error)
	// CreateGroup provisions one group (backed by an ACL subject group).
	CreateGroup(
		ctx context.Context, tenantID uint64, actorID string, group *types.SCIMGroup,
	) (*types.SCIMGroup, error)
	// ReplaceGroup overwrites displayName and the full member list.
	ReplaceGroup(
		ctx context.Context, tenantID uint64, groupID, actorID string, group *types.SCIMGroup,
	) (*types.SCIMGroup, error)
	// PatchGroup applies the supported PATCH operations (displayName, members).
	PatchGroup(
		ctx context.Context, tenantID uint64, groupID, actorID string,
		patch *types.SCIMPatchRequest,
	) (*types.SCIMGroup, error)
	// DeleteGroup removes one group and its memberships.
	DeleteGroup(ctx context.Context, tenantID uint64, groupID, actorID string) error

	// VerifyToken validates the provisioning bearer token for a tenant.
	VerifyToken(ctx context.Context, tenantID uint64, token string) error
}
