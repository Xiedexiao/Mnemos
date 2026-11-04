package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildDepartmentPath covers the materialized path construction.
func TestBuildDepartmentPath(t *testing.T) {
	assert.Equal(t, "/root", BuildDepartmentPath("", "root"))
	assert.Equal(t, "/root/child", BuildDepartmentPath("/root", "child"))
	assert.Equal(t, "/root/child", BuildDepartmentPath("/root/", "child"),
		"a trailing slash must not produce a double separator")
}

// TestDepartmentDepth covers the depth derivation.
func TestDepartmentDepth(t *testing.T) {
	assert.Equal(t, 0, DepartmentDepth("/root"))
	assert.Equal(t, 1, DepartmentDepth("/root/child"))
	assert.Equal(t, 2, DepartmentDepth("/root/child/grand"))
	assert.Equal(t, 0, DepartmentDepth(""))
}

// TestDepartmentIsDescendantOfIsSeparatorAware is the prefix bug guard:
// "/a/bc" must not be treated as a child of "/a/b".
func TestDepartmentIsDescendantOfIsSeparatorAware(t *testing.T) {
	assert.True(t, DepartmentIsDescendantOf("/a/b", "/a/b"), "self counts")
	assert.True(t, DepartmentIsDescendantOf("/a/b/c", "/a/b"))
	assert.False(t, DepartmentIsDescendantOf("/a/bc", "/a/b"),
		"a longer sibling name is not a descendant")
	assert.False(t, DepartmentIsDescendantOf("/a", "/a/b"),
		"a parent is not a descendant of its child")
	assert.False(t, DepartmentIsDescendantOf("", "/a"))
	assert.False(t, DepartmentIsDescendantOf("/a", ""))
}

// TestDepartmentParentPath covers path peeling.
func TestDepartmentParentPath(t *testing.T) {
	assert.Equal(t, "", DepartmentParentPath("/root"))
	assert.Equal(t, "/root", DepartmentParentPath("/root/child"))
	assert.Equal(t, "/root/child", DepartmentParentPath("/root/child/grand"))
}

// TestNormalizeOrgDepartmentName covers the name rules.
func TestNormalizeOrgDepartmentName(t *testing.T) {
	name, err := NormalizeOrgDepartmentName("  工艺部  ")
	require.NoError(t, err)
	assert.Equal(t, "工艺部", name)

	_, err = NormalizeOrgDepartmentName("  ")
	require.Error(t, err)

	// A slash would break the materialized path; it must be rejected, not
	// silently replaced (two different names could collapse into one).
	_, err = NormalizeOrgDepartmentName("研发/平台")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "/")
}

// TestBuildOrgDepartmentTree keeps parent/child links and stable ordering.
func TestBuildOrgDepartmentTree(t *testing.T) {
	rows := []*OrgDepartment{
		{ID: "b", ParentID: "", Name: "B 部门", Path: "/b"},
		{ID: "a", ParentID: "", Name: "A 部门", Path: "/a"},
		{ID: "a1", ParentID: "a", Name: "A-1", Path: "/a/a1"},
		{ID: "a2", ParentID: "a", Name: "A-2", Path: "/a/a2"},
	}
	tree := BuildOrgDepartmentTree(rows)
	require.Len(t, tree, 2)
	assert.Equal(t, "A 部门", tree[0].Name, "roots sort by name")
	assert.Equal(t, "B 部门", tree[1].Name)
	require.Len(t, tree[0].Children, 2)
	assert.Equal(t, "A-1", tree[0].Children[0].Name)
}

// TestBuildOrgDepartmentTreePromotesOrphans keeps a subtree from vanishing
// when its parent is filtered out.
func TestBuildOrgDepartmentTreePromotesOrphans(t *testing.T) {
	rows := []*OrgDepartment{
		{ID: "orphan", ParentID: "missing", Name: "孤儿", Path: "/missing/orphan"},
	}
	tree := BuildOrgDepartmentTree(rows)
	require.Len(t, tree, 1)
	assert.Equal(t, "orphan", tree[0].ID)
}

// TestValidateDepartmentMove covers the cycle and depth guards.
func TestValidateDepartmentMove(t *testing.T) {
	// Moving under its own subtree would orphan the whole branch.
	err := ValidateDepartmentMove("/a", "/a/b", 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "own subtree")

	// Moving to itself is also a cycle.
	require.Error(t, ValidateDepartmentMove("/a", "/a", 0))

	// Depth overflow.
	err = ValidateDepartmentMove("/a", "/d1/d2/d3", MaxOrgDepartmentDepth)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "maximum depth")

	// A legal move to a root.
	require.NoError(t, ValidateDepartmentMove("/a", "", 0))
	require.NoError(t, ValidateDepartmentMove("/a", "/other", 1))
}

// TestDepartmentScopeCovers is the authorization core: a manager's scope is
// their subtree, not the whole tenant.
func TestDepartmentScopeCovers(t *testing.T) {
	manager := DepartmentScope{UserID: "m-1", ManagedPaths: []string{"/a/b"}}
	assert.True(t, manager.Covers("/a/b"), "the managed department itself")
	assert.True(t, manager.Covers("/a/b/c"), "a sub-department")
	assert.False(t, manager.Covers("/a"), "the parent is out of scope")
	assert.False(t, manager.Covers("/a/bc"), "a name-prefix sibling is out of scope")
	assert.False(t, manager.Covers("/z"))

	admin := DepartmentScope{UserID: "admin", TenantWide: true}
	assert.True(t, admin.Covers("/anywhere"))
	assert.True(t, admin.Covers(""))
}

// TestSCIMUserValidation covers the provisioning shape rules.
func TestSCIMUserValidation(t *testing.T) {
	require.NoError(t, ValidateSCIMUser(&SCIMUser{
		UserName: "zhangsan",
		Emails:   []SCIMEmail{{Value: "zhangsan@example.com", Primary: true}},
	}))
	require.Error(t, ValidateSCIMUser(nil))
	require.Error(t, ValidateSCIMUser(&SCIMUser{}), "userName is required")
	require.Error(t, ValidateSCIMUser(&SCIMUser{
		UserName: "x",
		Emails:   []SCIMEmail{{Value: "not-an-email"}},
	}))
}

// TestSCIMPrimaryEmailFallsBack covers the email selection.
func TestSCIMPrimaryEmailFallsBack(t *testing.T) {
	assert.Equal(t, "p@example.com", SCIMPrimaryEmail(&SCIMUser{
		Emails: []SCIMEmail{
			{Value: "second@example.com"},
			{Value: "p@example.com", Primary: true},
		},
	}))
	assert.Equal(t, "first@example.com", SCIMPrimaryEmail(&SCIMUser{
		Emails: []SCIMEmail{{Value: "first@example.com"}},
	}))
	assert.Equal(t, "", SCIMPrimaryEmail(nil))
}

// TestSCIMDisplayNameFallbacks covers the name projection chain.
func TestSCIMDisplayNameFallbacks(t *testing.T) {
	assert.Equal(t, "显示名", SCIMDisplayName(&SCIMUser{DisplayName: "显示名", UserName: "u"}))
	assert.Equal(t, "Formatted", SCIMDisplayName(&SCIMUser{
		UserName: "u", Name: &SCIMName{Formatted: "Formatted"},
	}))
	assert.Equal(t, "San Zhang", SCIMDisplayName(&SCIMUser{
		UserName: "u", Name: &SCIMName{GivenName: "San", FamilyName: "Zhang"},
	}))
	assert.Equal(t, "u", SCIMDisplayName(&SCIMUser{UserName: "u"}))
}

// TestSCIMGroupValidation covers the group rules.
func TestSCIMGroupValidation(t *testing.T) {
	require.NoError(t, ValidateSCIMGroup(&SCIMGroup{DisplayName: "工艺部"}))
	require.Error(t, ValidateSCIMGroup(nil))
	require.Error(t, ValidateSCIMGroup(&SCIMGroup{}))
}

// TestSCIMMemberIDsDedupes keeps a repeated member from being counted twice.
func TestSCIMMemberIDsDedupes(t *testing.T) {
	ids := SCIMMemberIDs([]SCIMGroupMember{
		{Value: "u-1"}, {Value: " u-1 "}, {Value: ""}, {Value: "u-2"},
	})
	assert.Equal(t, []string{"u-1", "u-2"}, ids)
}
