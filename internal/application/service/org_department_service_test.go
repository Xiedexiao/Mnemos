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

const orgDepartmentTestDDL = `
CREATE TABLE org_departments (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    parent_id VARCHAR(36) NOT NULL DEFAULT '',
    name VARCHAR(128) NOT NULL,
    path VARCHAR(1024) NOT NULL,
    depth INTEGER NOT NULL DEFAULT 0,
    manager_user_id VARCHAR(36) NOT NULL DEFAULT '',
    external_id VARCHAR(191) NOT NULL DEFAULT '',
    subject_group_id VARCHAR(36) NOT NULL DEFAULT '',
    created_at DATETIME,
    updated_at DATETIME
);
CREATE UNIQUE INDEX uq_org_department_path ON org_departments (tenant_id, path);
CREATE TABLE org_department_members (
    department_id VARCHAR(36) NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    tenant_id INTEGER NOT NULL,
    is_manager BOOLEAN NOT NULL DEFAULT FALSE,
    added_by VARCHAR(36) NOT NULL DEFAULT '',
    created_at DATETIME,
    PRIMARY KEY (department_id, user_id)
);
`

func setupOrgDepartmentDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(orgDepartmentTestDDL).Error)
	return db
}

func newOrgDepartmentService(t *testing.T) (*orgDepartmentService, *gorm.DB) {
	t.Helper()
	db := setupOrgDepartmentDB(t)
	svc := &orgDepartmentService{repo: repository.NewOrgDepartmentRepository(db)}
	return svc, db
}

// TestCreateDepartmentBuildsPath covers the materialized path on create.
func TestCreateDepartmentBuildsPath(t *testing.T) {
	svc, _ := newOrgDepartmentService(t)
	ctx := context.Background()

	root, err := svc.CreateDepartment(ctx, 7, "admin", types.OrgDepartmentInput{Name: "集团"})
	require.NoError(t, err)
	assert.Equal(t, 0, root.Depth)
	assert.True(t, len(root.Path) > 0 && root.Path[0] == '/')
	assert.Contains(t, root.Path, root.ID)

	child, err := svc.CreateDepartment(ctx, 7, "admin", types.OrgDepartmentInput{
		Name: "工艺部", ParentID: root.ID,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, child.Depth)
	assert.Contains(t, child.Path, root.ID)
	assert.Contains(t, child.Path, child.ID)
}

// TestCreateDepartmentRejectsMissingParent keeps a node from hanging off a
// nonexistent parent.
func TestCreateDepartmentRejectsMissingParent(t *testing.T) {
	svc, _ := newOrgDepartmentService(t)
	_, err := svc.CreateDepartment(context.Background(), 7, "admin", types.OrgDepartmentInput{
		Name: "孤儿部门", ParentID: "does-not-exist",
	})
	require.Error(t, err)
}

// TestCreateDepartmentRejectsSlashInName covers the path-integrity guard.
func TestCreateDepartmentRejectsSlashInName(t *testing.T) {
	svc, _ := newOrgDepartmentService(t)
	_, err := svc.CreateDepartment(context.Background(), 7, "admin", types.OrgDepartmentInput{
		Name: "研发/平台",
	})
	require.Error(t, err)
}

// TestListTreeNestsChildren covers the tree read.
func TestListTreeNestsChildren(t *testing.T) {
	svc, _ := newOrgDepartmentService(t)
	ctx := context.Background()
	root, err := svc.CreateDepartment(ctx, 7, "admin", types.OrgDepartmentInput{Name: "集团"})
	require.NoError(t, err)
	_, err = svc.CreateDepartment(ctx, 7, "admin", types.OrgDepartmentInput{
		Name: "工艺部", ParentID: root.ID,
	})
	require.NoError(t, err)

	tree, err := svc.ListTree(ctx, 7)
	require.NoError(t, err)
	require.Len(t, tree, 1)
	assert.Equal(t, "集团", tree[0].Name)
	require.Len(t, tree[0].Children, 1)
	assert.Equal(t, "工艺部", tree[0].Children[0].Name)
}

// TestMoveDepartmentRewritesSubtree is the materialized-path invariant: after a
// move every descendant's path must follow.
func TestMoveDepartmentRewritesSubtree(t *testing.T) {
	svc, _ := newOrgDepartmentService(t)
	ctx := context.Background()

	rootA, err := svc.CreateDepartment(ctx, 7, "admin", types.OrgDepartmentInput{Name: "A"})
	require.NoError(t, err)
	rootB, err := svc.CreateDepartment(ctx, 7, "admin", types.OrgDepartmentInput{Name: "B"})
	require.NoError(t, err)
	child, err := svc.CreateDepartment(ctx, 7, "admin", types.OrgDepartmentInput{
		Name: "A-1", ParentID: rootA.ID,
	})
	require.NoError(t, err)
	grand, err := svc.CreateDepartment(ctx, 7, "admin", types.OrgDepartmentInput{
		Name: "A-1-1", ParentID: child.ID,
	})
	require.NoError(t, err)

	_, err = svc.MoveDepartment(ctx, 7, child.ID, "admin", rootB.ID)
	require.NoError(t, err)

	// The grandchild must still sit under the moved child.
	moved, err := svc.GetDepartment(ctx, 7, grand.ID)
	require.NoError(t, err)
	assert.Contains(t, moved.OrgDepartment.Path, rootB.ID,
		"a descendant's path must be rewritten to the new ancestor")
	assert.Contains(t, moved.OrgDepartment.Path, child.ID)

	// The tree now nests the whole branch under B.
	tree, err := svc.ListTree(ctx, 7)
	require.NoError(t, err)
	var bNode *types.OrgDepartmentNode
	for _, node := range tree {
		if node.Name == "B" {
			bNode = node
		}
	}
	require.NotNil(t, bNode)
	require.Len(t, bNode.Children, 1)
	assert.Equal(t, "A-1", bNode.Children[0].Name)
	require.Len(t, bNode.Children[0].Children, 1)
	assert.Equal(t, "A-1-1", bNode.Children[0].Children[0].Name)
}

// TestMoveDepartmentRejectsCycle is the structural guard.
func TestMoveDepartmentRejectsCycle(t *testing.T) {
	svc, _ := newOrgDepartmentService(t)
	ctx := context.Background()
	root, err := svc.CreateDepartment(ctx, 7, "admin", types.OrgDepartmentInput{Name: "A"})
	require.NoError(t, err)
	child, err := svc.CreateDepartment(ctx, 7, "admin", types.OrgDepartmentInput{
		Name: "A-1", ParentID: root.ID,
	})
	require.NoError(t, err)

	_, err = svc.MoveDepartment(ctx, 7, root.ID, "admin", child.ID)
	require.Error(t, err, "moving a parent under its own child would orphan the branch")
}

// TestDeleteDepartmentRefusesWithChildren keeps a subtree from vanishing.
func TestDeleteDepartmentRefusesWithChildren(t *testing.T) {
	svc, _ := newOrgDepartmentService(t)
	ctx := context.Background()
	root, err := svc.CreateDepartment(ctx, 7, "admin", types.OrgDepartmentInput{Name: "A"})
	require.NoError(t, err)
	child, err := svc.CreateDepartment(ctx, 7, "admin", types.OrgDepartmentInput{
		Name: "A-1", ParentID: root.ID,
	})
	require.NoError(t, err)

	require.Error(t, svc.DeleteDepartment(ctx, 7, root.ID, "admin"))
	// Removing the leaf first is the supported path.
	require.NoError(t, svc.DeleteDepartment(ctx, 7, child.ID, "admin"))
	require.NoError(t, svc.DeleteDepartment(ctx, 7, root.ID, "admin"))
}

// TestScopeOfIsSubtreeBounded is the authorization core: a manager's scope is
// their own department and below, never the parent or a name-prefix sibling.
func TestScopeOfIsSubtreeBounded(t *testing.T) {
	svc, db := newOrgDepartmentService(t)
	ctx := context.Background()

	root, err := svc.CreateDepartment(ctx, 7, "admin", types.OrgDepartmentInput{Name: "集团"})
	require.NoError(t, err)
	managed, err := svc.CreateDepartment(ctx, 7, "admin", types.OrgDepartmentInput{
		Name: "工艺部", ParentID: root.ID,
	})
	require.NoError(t, err)
	sibling, err := svc.CreateDepartment(ctx, 7, "admin", types.OrgDepartmentInput{
		Name: "采购部", ParentID: root.ID,
	})
	require.NoError(t, err)

	// Make m-1 the manager of 工艺部 through the service (the row already
	// exists, so a hand-rolled INSERT would collide on the primary key).
	_, err = svc.UpdateDepartment(ctx, 7, managed.ID, "admin", types.OrgDepartmentInput{
		ManagerUserID: "m-1",
	})
	require.NoError(t, err)
	_, err = svc.AddMembers(ctx, 7, managed.ID, "admin", []string{"m-1"})
	require.NoError(t, err)
	_ = db

	scope, err := svc.ScopeOf(ctx, 7, "m-1")
	require.NoError(t, err)
	assert.False(t, scope.TenantWide)
	assert.Equal(t, []string{managed.Path}, scope.ManagedPaths)

	// The managed department and its subtree are in scope.
	inScope, err := svc.CanManageDepartment(ctx, 7, "m-1", managed.ID)
	require.NoError(t, err)
	assert.True(t, inScope)

	// The sibling and the parent are not.
	for _, other := range []string{sibling.ID, root.ID} {
		allowed, err := svc.CanManageDepartment(ctx, 7, "m-1", other)
		require.NoError(t, err)
		assert.False(t, allowed, "a manager must not administer outside the subtree")
	}
}

// TestScopeOfAdminIsTenantWide keeps the role matrix authoritative for admins.
func TestScopeOfAdminIsTenantWide(t *testing.T) {
	svc, _ := newOrgDepartmentService(t)
	adminCtx := types.WithCaller(context.Background(), types.Caller{
		TenantID: 7, UserID: "admin", Role: types.TenantRoleAdmin,
	})
	scope, err := svc.ScopeOf(adminCtx, 7, "admin")
	require.NoError(t, err)
	assert.True(t, scope.TenantWide)

	viewerCtx := types.WithCaller(context.Background(), types.Caller{
		TenantID: 7, UserID: "v", Role: types.TenantRoleViewer,
	})
	scope, err = svc.ScopeOf(viewerCtx, 7, "v")
	require.NoError(t, err)
	assert.False(t, scope.TenantWide)
	assert.Empty(t, scope.ManagedPaths, "a viewer manages nothing")
}

// TestDepartmentMembershipIsPerDepartment covers the many-to-many rule.
func TestDepartmentMembershipIsPerDepartment(t *testing.T) {
	svc, _ := newOrgDepartmentService(t)
	ctx := context.Background()
	first, err := svc.CreateDepartment(ctx, 7, "admin", types.OrgDepartmentInput{Name: "A"})
	require.NoError(t, err)
	second, err := svc.CreateDepartment(ctx, 7, "admin", types.OrgDepartmentInput{Name: "B"})
	require.NoError(t, err)

	_, err = svc.AddMembers(ctx, 7, first.ID, "admin", []string{"u-1"})
	require.NoError(t, err)
	_, err = svc.AddMembers(ctx, 7, second.ID, "admin", []string{"u-1"})
	require.NoError(t, err, "a user may belong to several departments")

	detail, err := svc.GetDepartment(ctx, 7, first.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"u-1"}, detail.Members)

	require.NoError(t, svc.RemoveMember(ctx, 7, first.ID, "u-1", "admin"))
	detail, err = svc.GetDepartment(ctx, 7, first.ID)
	require.NoError(t, err)
	assert.Empty(t, detail.Members)
	detail, err = svc.GetDepartment(ctx, 7, second.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"u-1"}, detail.Members, "the other department is unaffected")
}
