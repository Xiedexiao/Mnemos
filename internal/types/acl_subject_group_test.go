package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSubjectTypeValidation keeps an unusable grant out of the table: a role
// subject naming a non-existent role would sit there forever and never apply.
func TestSubjectTypeValidation(t *testing.T) {
	kind, err := SubjectTypeForACLAction("", "u-1")
	require.NoError(t, err)
	assert.Equal(t, KnowledgeACLSubjectUser, kind, "an omitted type defaults to user")

	kind, err = SubjectTypeForACLAction("group", " g-1 ")
	require.NoError(t, err)
	assert.Equal(t, KnowledgeACLSubjectGroup, kind)

	_, err = SubjectTypeForACLAction("role", "admin")
	require.NoError(t, err)

	_, err = SubjectTypeForACLAction("role", "superuser")
	require.Error(t, err, "an unknown role must be rejected, not stored inert")

	_, err = SubjectTypeForACLAction("team", "x")
	require.Error(t, err)

	_, err = SubjectTypeForACLAction("user", "   ")
	require.Error(t, err, "an empty subject id is not a subject")
}

// TestCheckGrantCeilingSelfGrant covers 不能自授.
func TestCheckGrantCeilingSelfGrant(t *testing.T) {
	err := CheckGrantCeiling(GrantCeilingInput{
		GrantorRole: TenantRoleAdmin, Action: PermissionActionView,
		Effect: KnowledgeACLEffectAllow, SubjectType: KnowledgeACLSubjectUser,
		SubjectID: "admin-1", GrantorUserID: "admin-1",
	})
	require.ErrorIs(t, err, GrantViolationSelf)

	// Same person, but through a group: not a self grant (the group may hold
	// other members), so it must be allowed.
	err = CheckGrantCeiling(GrantCeilingInput{
		GrantorRole: TenantRoleAdmin, Action: PermissionActionView,
		Effect: KnowledgeACLEffectAllow, SubjectType: KnowledgeACLSubjectGroup,
		SubjectID: "g-1", GrantorUserID: "admin-1",
	})
	require.NoError(t, err)
}

// TestCheckGrantCeilingActionCeiling is the privilege-escalation guard: an
// actor may not hand out an action they do not hold.
func TestCheckGrantCeilingActionCeiling(t *testing.T) {
	// A contributor holds download but not export.
	err := CheckGrantCeiling(GrantCeilingInput{
		GrantorRole: TenantRoleContributor, Action: PermissionActionExport,
		Effect: KnowledgeACLEffectAllow, SubjectType: KnowledgeACLSubjectUser,
		SubjectID: "someone-else", GrantorUserID: "contributor-1",
	})
	require.ErrorIs(t, err, GrantViolationActionCeiling)

	require.NoError(t, CheckGrantCeiling(GrantCeilingInput{
		GrantorRole: TenantRoleContributor, Action: PermissionActionDownload,
		Effect: KnowledgeACLEffectAllow, SubjectType: KnowledgeACLSubjectUser,
		SubjectID: "someone-else", GrantorUserID: "contributor-1",
	}))

	// An explicit grantor action set overrides the role default.
	require.NoError(t, CheckGrantCeiling(GrantCeilingInput{
		GrantorRole: TenantRoleViewer, Action: PermissionActionShare,
		GrantorActions: []PermissionAction{PermissionActionView, PermissionActionShare},
		Effect:         KnowledgeACLEffectAllow, SubjectType: KnowledgeACLSubjectUser,
		SubjectID: "someone-else", GrantorUserID: "viewer-1",
	}))
}

// TestCheckGrantCeilingAllowsDeny: tightening is always legal, even for an
// action the grantor does not hold — otherwise only people with the widest
// rights could ever restrict content.
func TestCheckGrantCeilingAllowsDeny(t *testing.T) {
	require.NoError(t, CheckGrantCeiling(GrantCeilingInput{
		GrantorRole: TenantRoleViewer, Action: PermissionActionExport,
		Effect: KnowledgeACLEffectDeny, SubjectType: KnowledgeACLSubjectUser,
		SubjectID: "someone-else", GrantorUserID: "viewer-1",
	}))
}

// TestCheckGrantCeilingRoleGrant covers the role-scope restriction.
func TestCheckGrantCeilingRoleGrant(t *testing.T) {
	err := CheckGrantCeiling(GrantCeilingInput{
		GrantorRole: TenantRoleContributor, Action: PermissionActionView,
		Effect: KnowledgeACLEffectAllow, SubjectType: KnowledgeACLSubjectRole,
		SubjectID: string(TenantRoleViewer), GrantorUserID: "contributor-1",
	})
	require.ErrorIs(t, err, GrantViolationRoleCeiling)

	require.NoError(t, CheckGrantCeiling(GrantCeilingInput{
		GrantorRole: TenantRoleAdmin, Action: PermissionActionView,
		Effect: KnowledgeACLEffectAllow, SubjectType: KnowledgeACLSubjectRole,
		SubjectID: string(TenantRoleViewer), GrantorUserID: "admin-1",
	}))
	require.NoError(t, CheckGrantCeiling(GrantCeilingInput{
		GrantorRole: TenantRoleOwner, Action: PermissionActionView,
		Effect: KnowledgeACLEffectAllow, SubjectType: KnowledgeACLSubjectRole,
		SubjectID: string(TenantRoleViewer), GrantorUserID: "owner-1",
	}))
}

// TestCheckGrantCeilingRejectsUnknownAction keeps a typo from writing a row
// that can never be matched.
func TestCheckGrantCeilingRejectsUnknownAction(t *testing.T) {
	require.ErrorIs(t, CheckGrantCeiling(GrantCeilingInput{
		GrantorRole: TenantRoleOwner, Action: PermissionAction("teleport"),
		Effect: KnowledgeACLEffectAllow, SubjectType: KnowledgeACLSubjectUser,
		SubjectID: "u-1", GrantorUserID: "owner-1",
	}), GrantViolationUnknownAction)
}

// TestNormalizeACLGroupName covers the group-name contract.
func TestNormalizeACLGroupName(t *testing.T) {
	name, err := NormalizeACLGroupName("  工艺部  ")
	require.NoError(t, err)
	assert.Equal(t, "工艺部", name)

	_, err = NormalizeACLGroupName("   ")
	require.Error(t, err)

	long := make([]rune, MaxACLGroupNameLen+1)
	for i := range long {
		long[i] = 'a'
	}
	_, err = NormalizeACLGroupName(string(long))
	require.Error(t, err)
}

// TestACLSubjectString keeps logs and cache keys unambiguous.
func TestACLSubjectString(t *testing.T) {
	assert.Equal(t, "user:u-1", ACLSubject{Type: KnowledgeACLSubjectUser, ID: "u-1"}.String())
	assert.Equal(t, "group:g-1", ACLSubject{Type: KnowledgeACLSubjectGroup, ID: "g-1"}.String())
	assert.True(t, ACLSubject{Type: KnowledgeACLSubjectRole, ID: "admin"}.Valid())
	assert.False(t, ACLSubject{Type: KnowledgeACLSubjectRole, ID: " "}.Valid())
}

// TestCheckGrantCeilingDepartmentScope is the §2.1 organization-range rule: a
// manager may only write grants for groups inside their own subtree.
func TestCheckGrantCeilingDepartmentScope(t *testing.T) {
	managerScope := &DepartmentScope{
		UserID: "manager-1", ManagedPaths: []string{"/org/gongyi"},
	}
	base := GrantCeilingInput{
		GrantorRole: TenantRoleContributor, Action: PermissionActionView,
		Effect: KnowledgeACLEffectAllow, SubjectType: KnowledgeACLSubjectGroup,
		SubjectID: "g-1", GrantorUserID: "manager-1", GrantorScope: managerScope,
	}

	// Inside the subtree: allowed.
	inScope := base
	inScope.SubjectDepartmentPath = "/org/gongyi/sop"
	require.NoError(t, CheckGrantCeiling(inScope))

	// Outside it: refused, for allow and deny alike.
	outOfScope := base
	outOfScope.SubjectDepartmentPath = "/org/caigou"
	require.ErrorIs(t, CheckGrantCeiling(outOfScope), GrantViolationOutOfScope)
	outOfScope.Effect = KnowledgeACLEffectDeny
	require.ErrorIs(t, CheckGrantCeiling(outOfScope), GrantViolationOutOfScope)

	// A tenant-wide admin passes.
	adminScope := &DepartmentScope{UserID: "admin-1", TenantWide: true}
	admin := base
	admin.GrantorRole = TenantRoleAdmin
	admin.GrantorUserID = "admin-1"
	admin.GrantorScope = adminScope
	admin.SubjectDepartmentPath = "/org/caigou"
	require.NoError(t, CheckGrantCeiling(admin))

	// A hand-made group has no department position, so the rule does not fire.
	handmade := base
	handmade.GrantorScope = managerScope
	require.NoError(t, CheckGrantCeiling(handmade))

	// Without a scope (no organization tree) the rule is skipped entirely.
	noTree := base
	noTree.SubjectDepartmentPath = "/org/caigou"
	noTree.GrantorScope = nil
	require.NoError(t, CheckGrantCeiling(noTree))
}
