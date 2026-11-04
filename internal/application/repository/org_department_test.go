package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const orgRepoTestDDL = `
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
CREATE UNIQUE INDEX uq_org_department_external
    ON org_departments (tenant_id, external_id) WHERE external_id <> '';
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

func setupOrgRepoDB(t *testing.T) (*gorm.DB, interfaces.OrgDepartmentRepository) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(orgRepoTestDDL).Error)
	return db, NewOrgDepartmentRepository(db)
}

func seedDepartment(
	t *testing.T, repo interfaces.OrgDepartmentRepository, id, parent, name, path string,
) {
	t.Helper()
	require.NoError(t, repo.CreateDepartment(context.Background(), &types.OrgDepartment{
		ID: id, TenantID: 7, ParentID: parent, Name: name, Path: path,
		Depth: types.DepartmentDepth(path), CreatedAt: time.Now().UTC(),
	}))
}

// TestListSubtreeMatchesWholeSegments is the prefix bug guard at the SQL level:
// LIKE '/a/b/%' must not match '/a/bc/...'.
func TestListSubtreeMatchesWholeSegments(t *testing.T) {
	db, repo := setupOrgRepoDB(t)
	ctx := context.Background()
	seedDepartment(t, repo, "a", "", "A", "/a")
	seedDepartment(t, repo, "ab", "", "AB", "/ab")
	seedDepartment(t, repo, "a1", "a", "A-1", "/a/a1")
	seedDepartment(t, repo, "a2", "a", "A-2", "/a/a2")
	seedDepartment(t, repo, "a11", "a1", "A-1-1", "/a/a1/a11")
	_ = db

	rows, err := repo.ListSubtree(ctx, 7, "/a")
	require.NoError(t, err)
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	assert.ElementsMatch(t, []string{"a", "a1", "a2", "a11"}, ids)
	assert.NotContains(t, ids, "ab", "a name-prefix sibling must not be included")
}

// TestMoveDepartmentRewritesDescendantPaths covers the SQL REPLACE.
func TestMoveDepartmentRewritesDescendantPaths(t *testing.T) {
	_, repo := setupOrgRepoDB(t)
	ctx := context.Background()
	seedDepartment(t, repo, "a", "", "A", "/a")
	seedDepartment(t, repo, "b", "", "B", "/b")
	seedDepartment(t, repo, "a1", "a", "A-1", "/a/a1")
	seedDepartment(t, repo, "a11", "a1", "A-1-1", "/a/a1/a11")

	moved, err := repo.GetDepartment(ctx, 7, "a1")
	require.NoError(t, err)
	oldPath := moved.Path
	moved.ParentID = "b"
	moved.Path = "/b/a1"
	moved.Depth = 1
	require.NoError(t, repo.MoveDepartment(ctx, 7, moved, oldPath))

	child, err := repo.GetDepartment(ctx, 7, "a11")
	require.NoError(t, err)
	assert.Equal(t, "/b/a1/a11", child.Path)
	assert.Equal(t, 2, child.Depth, "depth is derived from the path on read")

	// The old parent still exists and is untouched.
	parent, err := repo.GetDepartment(ctx, 7, "a")
	require.NoError(t, err)
	assert.Equal(t, "/a", parent.Path)
}

// TestDepartmentsOfUser covers the membership join.
func TestDepartmentsOfUser(t *testing.T) {
	_, repo := setupOrgRepoDB(t)
	ctx := context.Background()
	seedDepartment(t, repo, "a", "", "A", "/a")
	seedDepartment(t, repo, "b", "", "B", "/b")
	_, err := repo.AddMembers(ctx, 7, "a", []string{"u-1", "u-2"}, "admin")
	require.NoError(t, err)
	_, err = repo.AddMembers(ctx, 7, "b", []string{"u-1"}, "admin")
	require.NoError(t, err)

	rows, err := repo.DepartmentsOfUser(ctx, 7, "u-1")
	require.NoError(t, err)
	assert.Len(t, rows, 2, "a user may belong to several departments")

	rows, err = repo.DepartmentsOfUser(ctx, 7, "u-2")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "a", rows[0].ID)
}

// TestAddMembersIsIdempotent keeps a re-sync from inflating counts.
func TestAddMembersIsIdempotent(t *testing.T) {
	_, repo := setupOrgRepoDB(t)
	ctx := context.Background()
	seedDepartment(t, repo, "a", "", "A", "/a")

	added, err := repo.AddMembers(ctx, 7, "a", []string{"u-1"}, "admin")
	require.NoError(t, err)
	assert.EqualValues(t, 1, added)

	added, err = repo.AddMembers(ctx, 7, "a", []string{"u-1"}, "admin")
	require.NoError(t, err)
	assert.Zero(t, added, "a repeated SCIM sync must not duplicate the row")

	members, err := repo.ListMembers(ctx, 7, "a")
	require.NoError(t, err)
	assert.Equal(t, []string{"u-1"}, members)
}

// TestFindByExternalID covers the SCIM relink lookup.
func TestFindByExternalID(t *testing.T) {
	_, repo := setupOrgRepoDB(t)
	ctx := context.Background()
	require.NoError(t, repo.CreateDepartment(ctx, &types.OrgDepartment{
		ID: "a", TenantID: 7, Name: "A", Path: "/a", ExternalID: "idp-dept-1",
	}))

	found, err := repo.FindByExternalID(ctx, 7, "idp-dept-1")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, "a", found.ID)

	missing, err := repo.FindByExternalID(ctx, 7, "unknown")
	require.NoError(t, err)
	assert.Nil(t, missing)

	// An empty external id is not a lookup key.
	empty, err := repo.FindByExternalID(ctx, 7, "  ")
	require.NoError(t, err)
	assert.Nil(t, empty)
}

// TestCountMembersByDepartment covers the list projection.
func TestCountMembersByDepartment(t *testing.T) {
	_, repo := setupOrgRepoDB(t)
	ctx := context.Background()
	seedDepartment(t, repo, "a", "", "A", "/a")
	seedDepartment(t, repo, "b", "", "B", "/b")
	_, err := repo.AddMembers(ctx, 7, "a", []string{"u-1", "u-2", "u-3"}, "admin")
	require.NoError(t, err)

	counts, err := repo.CountMembersByDepartment(ctx, 7)
	require.NoError(t, err)
	assert.EqualValues(t, 3, counts["a"])
	assert.NotContains(t, counts, "b", "an empty department has no row")
}
