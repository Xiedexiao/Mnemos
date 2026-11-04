package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
)

const aclGroupTestDDL = `
CREATE TABLE acl_subject_groups (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    name VARCHAR(128) NOT NULL,
    description VARCHAR(512) NOT NULL DEFAULT '',
    created_by VARCHAR(36) NOT NULL DEFAULT '',
    created_at DATETIME,
    updated_at DATETIME
);
CREATE UNIQUE INDEX uq_acl_group_name ON acl_subject_groups (tenant_id, name);
CREATE TABLE acl_subject_group_members (
    group_id VARCHAR(36) NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    tenant_id INTEGER NOT NULL,
    added_by VARCHAR(36) NOT NULL DEFAULT '',
    created_at DATETIME,
    PRIMARY KEY (group_id, user_id)
);
`

func setupACLGroupDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(aclGroupTestDDL).Error)
	return db
}

// TestSubjectsForUserIncludesGroupAndRole is the expansion contract: one user
// becomes user + role + every group they belong to.
func TestSubjectsForUserIncludesGroupAndRole(t *testing.T) {
	db := setupACLGroupDB(t)
	ctx := context.Background()
	svc := &aclSubjectGroupService{repo: repository.NewACLSubjectGroupRepository(db)}

	group, err := svc.CreateGroup(ctx, 7, "admin", "工艺部", "")
	require.NoError(t, err)
	other, err := svc.CreateGroup(ctx, 7, "admin", "采购部", "")
	require.NoError(t, err)
	_, err = svc.AddMembers(ctx, 7, group.ID, "admin", []string{"u-1", "u-2"})
	require.NoError(t, err)
	_, err = svc.AddMembers(ctx, 7, other.ID, "admin", []string{"u-3"})
	require.NoError(t, err)

	subjects, err := svc.SubjectsForUser(ctx, 7, "u-1", types.TenantRoleContributor)
	require.NoError(t, err)
	assert.Equal(t, []types.ACLSubject{
		{Type: types.KnowledgeACLSubjectUser, ID: "u-1"},
		{Type: types.KnowledgeACLSubjectRole, ID: string(types.TenantRoleContributor)},
		{Type: types.KnowledgeACLSubjectGroup, ID: group.ID},
	}, subjects)

	// Removing the member removes the group subject.
	require.NoError(t, svc.RemoveMember(ctx, 7, group.ID, "u-1", "admin"))
	subjects, err = svc.SubjectsForUser(ctx, 7, "u-1", types.TenantRoleContributor)
	require.NoError(t, err)
	assert.Len(t, subjects, 2, "leaving the group revokes its grants")
	for _, subject := range subjects {
		assert.NotEqual(t, types.KnowledgeACLSubjectGroup, subject.Type)
	}
}

// TestGroupNameMustBeUnique keeps two groups from being indistinguishable in
// the picker.
func TestGroupNameMustBeUnique(t *testing.T) {
	db := setupACLGroupDB(t)
	ctx := context.Background()
	svc := &aclSubjectGroupService{repo: repository.NewACLSubjectGroupRepository(db)}

	_, err := svc.CreateGroup(ctx, 7, "admin", "工艺部", "")
	require.NoError(t, err)
	_, err = svc.CreateGroup(ctx, 7, "admin", "工艺部", "")
	require.Error(t, err)

	// The same name in another tenant is fine.
	_, err = svc.CreateGroup(ctx, 8, "admin", "工艺部", "")
	require.NoError(t, err)
}

// TestRenameKeepsGrantsTargeted: renames must not silently change scope.
func TestRenameKeepsGrantsTargeted(t *testing.T) {
	db := setupACLGroupDB(t)
	ctx := context.Background()
	svc := &aclSubjectGroupService{repo: repository.NewACLSubjectGroupRepository(db)}
	group, err := svc.CreateGroup(ctx, 7, "admin", "旧名字", "描述")
	require.NoError(t, err)
	_, err = svc.AddMembers(ctx, 7, group.ID, "admin", []string{"u-1"})
	require.NoError(t, err)

	updated, err := svc.UpdateGroup(ctx, 7, group.ID, "admin", "新名字", "")
	require.NoError(t, err)
	assert.Equal(t, "新名字", updated.Name)
	assert.Equal(t, group.ID, updated.ID, "the id — and therefore every grant — is unchanged")

	subjects, err := svc.SubjectsForUser(ctx, 7, "u-1", types.TenantRoleViewer)
	require.NoError(t, err)
	assert.Contains(t, subjects, types.ACLSubject{
		Type: types.KnowledgeACLSubjectGroup, ID: group.ID,
	})
}

// TestDeleteGroupRemovesMemberships covers cleanup.
func TestDeleteGroupRemovesMemberships(t *testing.T) {
	db := setupACLGroupDB(t)
	ctx := context.Background()
	svc := &aclSubjectGroupService{repo: repository.NewACLSubjectGroupRepository(db)}
	group, err := svc.CreateGroup(ctx, 7, "admin", "临时组", "")
	require.NoError(t, err)
	_, err = svc.AddMembers(ctx, 7, group.ID, "admin", []string{"u-1"})
	require.NoError(t, err)

	require.NoError(t, svc.DeleteGroup(ctx, 7, group.ID, "admin"))
	members, err := svc.ListMembers(ctx, 7, group.ID)
	require.NoError(t, err)
	assert.Empty(t, members)

	subjects, err := svc.SubjectsForUser(ctx, 7, "u-1", types.TenantRoleViewer)
	require.NoError(t, err)
	for _, subject := range subjects {
		assert.NotEqual(t, group.ID, subject.ID)
	}
}

// TestListGroupsReportsMemberCounts covers the picker payload.
func TestListGroupsReportsMemberCounts(t *testing.T) {
	db := setupACLGroupDB(t)
	ctx := context.Background()
	svc := &aclSubjectGroupService{repo: repository.NewACLSubjectGroupRepository(db)}
	first, err := svc.CreateGroup(ctx, 7, "admin", "A 组", "")
	require.NoError(t, err)
	_, err = svc.CreateGroup(ctx, 7, "admin", "B 组", "")
	require.NoError(t, err)
	_, err = svc.AddMembers(ctx, 7, first.ID, "admin", []string{"u-1", "u-2", "u-3"})
	require.NoError(t, err)

	groups, err := svc.ListGroups(ctx, 7)
	require.NoError(t, err)
	require.Len(t, groups, 2)
	assert.Equal(t, "A 组", groups[0].Name)
	assert.EqualValues(t, 3, groups[0].MemberCount)
	assert.EqualValues(t, 0, groups[1].MemberCount)
}

// TestResolverFailsClosedOnGroupLookupError keeps a broken group store from
// silently dropping group denies.
func TestResolverFailsClosedOnGroupLookupError(t *testing.T) {
	resolver := newACLSubjectResolver(&failingGroupService{})
	_, err := resolver.ForCaller(context.Background(), types.Caller{
		TenantID: 7, UserID: "u-1", Role: types.TenantRoleViewer,
	})
	require.Error(t, err, "a missed group deny is an over-permission")

	// Without a group service the user+role subjects still resolve.
	plain := newACLSubjectResolver(nil)
	subjects, err := plain.ForCaller(context.Background(), types.Caller{
		TenantID: 7, UserID: "u-1", Role: types.TenantRoleViewer,
	})
	require.NoError(t, err)
	assert.Len(t, subjects, 2)
}

// failingGroupService always errors, standing in for a broken group table.
type failingGroupService struct{}

func (failingGroupService) CreateGroup(
	context.Context, uint64, string, string, string,
) (*types.ACLSubjectGroup, error) {
	return nil, assert.AnError
}

func (failingGroupService) ListGroups(
	context.Context, uint64,
) ([]*types.ACLSubjectGroup, error) {
	return nil, assert.AnError
}

func (failingGroupService) UpdateGroup(
	context.Context, uint64, string, string, string, string,
) (*types.ACLSubjectGroup, error) {
	return nil, assert.AnError
}

func (failingGroupService) DeleteGroup(context.Context, uint64, string, string) error {
	return assert.AnError
}

func (failingGroupService) AddMembers(
	context.Context, uint64, string, string, []string,
) (int64, error) {
	return 0, assert.AnError
}

func (failingGroupService) RemoveMember(context.Context, uint64, string, string, string) error {
	return assert.AnError
}

func (failingGroupService) ListMembers(context.Context, uint64, string) ([]string, error) {
	return nil, assert.AnError
}

func (failingGroupService) SubjectsForUser(
	context.Context, uint64, string, types.TenantRole,
) ([]types.ACLSubject, error) {
	return nil, assert.AnError
}

// TestUpdateGroupRejectsEmptyName keeps a rename from blanking the label.
func TestUpdateGroupRejectsEmptyName(t *testing.T) {
	db := setupACLGroupDB(t)
	ctx := context.Background()
	svc := &aclSubjectGroupService{repo: repository.NewACLSubjectGroupRepository(db)}
	group, err := svc.CreateGroup(ctx, 7, "admin", "有名字", "")
	require.NoError(t, err)
	updated, err := svc.UpdateGroup(ctx, 7, group.ID, "admin", "   ", "")
	require.NoError(t, err)
	assert.Equal(t, "有名字", updated.Name, "whitespace-only means unchanged")
}
