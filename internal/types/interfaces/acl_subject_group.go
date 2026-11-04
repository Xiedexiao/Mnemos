package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// ACLSubjectGroupRepository persists tenant-scoped subject groups.
type ACLSubjectGroupRepository interface {
	// CreateGroup inserts one group.
	CreateGroup(ctx context.Context, group *types.ACLSubjectGroup) error
	// GetGroup returns one group in the tenant.
	GetGroup(ctx context.Context, tenantID uint64, groupID string) (*types.ACLSubjectGroup, error)
	// ListGroups returns the tenant's groups with member counts.
	ListGroups(ctx context.Context, tenantID uint64) ([]*types.ACLSubjectGroup, error)
	// UpdateGroup renames / re-describes one group.
	UpdateGroup(ctx context.Context, group *types.ACLSubjectGroup) error
	// DeleteGroup removes one group and its memberships.
	DeleteGroup(ctx context.Context, tenantID uint64, groupID string) error
	// AddMembers adds users to a group, ignoring existing rows.
	AddMembers(ctx context.Context, tenantID uint64, groupID string, userIDs []string, addedBy string) (int64, error)
	// RemoveMember removes one user from a group.
	RemoveMember(ctx context.Context, tenantID uint64, groupID, userID string) error
	// ListMembers returns the group's member ids.
	ListMembers(ctx context.Context, tenantID uint64, groupID string) ([]string, error)
	// GroupIDsForUser returns the ids of the groups the user belongs to.
	GroupIDsForUser(ctx context.Context, tenantID uint64, userID string) ([]string, error)
}

// ACLSubjectGroupService manages subject groups and expands a user into the
// subjects they hold.
type ACLSubjectGroupService interface {
	// CreateGroup creates one group (Admin).
	CreateGroup(
		ctx context.Context, tenantID uint64, actorID, name, description string,
	) (*types.ACLSubjectGroup, error)
	// ListGroups returns the tenant's groups.
	ListGroups(ctx context.Context, tenantID uint64) ([]*types.ACLSubjectGroup, error)
	// UpdateGroup renames / re-describes one group (Admin).
	UpdateGroup(
		ctx context.Context, tenantID uint64, groupID, actorID, name, description string,
	) (*types.ACLSubjectGroup, error)
	// DeleteGroup removes one group and its memberships (Admin).
	DeleteGroup(ctx context.Context, tenantID uint64, groupID, actorID string) error
	// AddMembers adds users to a group (Admin).
	AddMembers(
		ctx context.Context, tenantID uint64, groupID, actorID string, userIDs []string,
	) (int64, error)
	// RemoveMember removes one user from a group (Admin).
	RemoveMember(ctx context.Context, tenantID uint64, groupID, userID, actorID string) error
	// ListMembers returns a group's member ids.
	ListMembers(ctx context.Context, tenantID uint64, groupID string) ([]string, error)
	// SubjectsForUser expands one user into every subject they hold: the user
	// themselves, each group they belong to, and their tenant role. The
	// caller's role comes from the authenticated context, not from a lookup,
	// so an authorization decision never depends on a stale membership read.
	SubjectsForUser(
		ctx context.Context, tenantID uint64, userID string, role types.TenantRole,
	) ([]types.ACLSubject, error)
}
