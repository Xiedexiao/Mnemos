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
)

// aclSubjectTestDDL is the minimal schema for the multi-subject deny query.
const aclSubjectTestDDL = `
CREATE TABLE knowledges (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    title VARCHAR(255) NOT NULL DEFAULT '',
    classification VARCHAR(16) NOT NULL DEFAULT 'internal',
    deleted_at DATETIME
);
CREATE TABLE knowledge_acl (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL,
    subject_type VARCHAR(16) NOT NULL DEFAULT 'user',
    subject_id VARCHAR(64) NOT NULL,
    action VARCHAR(24) NOT NULL DEFAULT 'view',
    effect VARCHAR(8) NOT NULL DEFAULT 'allow',
    granted_by VARCHAR(36) NOT NULL DEFAULT '',
    expires_at DATETIME,
    created_at DATETIME,
    UNIQUE (knowledge_id, subject_type, subject_id, action)
);
`

func setupACLSubjectDB(t *testing.T) (*gorm.DB, *knowledgeACLRepository) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(aclSubjectTestDDL).Error)
	return db, NewKnowledgeACLRepository(db).(*knowledgeACLRepository)
}

func seedACLDoc(t *testing.T, db *gorm.DB, id, classification string) {
	t.Helper()
	require.NoError(t, db.Exec(`
		INSERT INTO knowledges (id, tenant_id, knowledge_base_id, title, classification)
		VALUES (?, 7, 'kb-1', ?, ?)`, id, id, classification).Error)
}

func seedACLGrant(t *testing.T, db *gorm.DB, knowledgeID, subjectType, subjectID, effect string) {
	t.Helper()
	require.NoError(t, db.Exec(`
		INSERT INTO knowledge_acl (id, tenant_id, knowledge_id, subject_type, subject_id, action, effect, created_at)
		VALUES (?, 7, ?, ?, ?, 'view', ?, ?)`,
		knowledgeID+"-"+subjectType+"-"+subjectID, knowledgeID,
		subjectType, subjectID, effect, time.Now().UTC()).Error)
}

// TestGroupDenyAppliesToMember is the point of group subjects: a deny written
// once for a group must protect the document for every member.
func TestGroupDenyAppliesToMember(t *testing.T) {
	db, repo := setupACLSubjectDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedACLDoc(t, db, "doc-open", "internal")
	seedACLDoc(t, db, "doc-group-denied", "internal")
	seedACLGrant(t, db, "doc-group-denied", "group", "g-suppliers", "deny")

	// Before the group is considered, the document is visible.
	denied, err := repo.DeniedKnowledgeIDsForSubjects(ctx, []string{"kb-1"},
		[]types.ACLSubject{{Type: types.KnowledgeACLSubjectUser, ID: "u-1"}},
		types.PermissionActionView, now)
	require.NoError(t, err)
	assert.Empty(t, denied)

	// With the group subject, the deny applies.
	denied, err = repo.DeniedKnowledgeIDsForSubjects(ctx, []string{"kb-1"},
		[]types.ACLSubject{
			{Type: types.KnowledgeACLSubjectUser, ID: "u-1"},
			{Type: types.KnowledgeACLSubjectGroup, ID: "g-suppliers"},
		}, types.PermissionActionView, now)
	require.NoError(t, err)
	assert.Equal(t, []string{"doc-group-denied"}, denied)

	// A different group is unaffected.
	denied, err = repo.DeniedKnowledgeIDsForSubjects(ctx, []string{"kb-1"},
		[]types.ACLSubject{
			{Type: types.KnowledgeACLSubjectUser, ID: "u-1"},
			{Type: types.KnowledgeACLSubjectGroup, ID: "g-engineering"},
		}, types.PermissionActionView, now)
	require.NoError(t, err)
	assert.Empty(t, denied)
}

// TestRoleAllowSatisfiesClassificationFloor covers the role subject on the
// allow side: a secret document stays invisible until any held subject grants
// it, and a role grant counts.
func TestRoleAllowSatisfiesClassificationFloor(t *testing.T) {
	db, repo := setupACLSubjectDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedACLDoc(t, db, "doc-secret", "secret")
	seedACLDoc(t, db, "doc-internal", "internal")

	// No allow: the secret document is denied by the classification floor.
	denied, err := repo.DeniedKnowledgeIDsForSubjects(ctx, []string{"kb-1"},
		[]types.ACLSubject{{Type: types.KnowledgeACLSubjectUser, ID: "u-1"}},
		types.PermissionActionView, now)
	require.NoError(t, err)
	assert.Equal(t, []string{"doc-secret"}, denied)

	// A role grant lifts it.
	seedACLGrant(t, db, "doc-secret", "role", "admin", "allow")
	denied, err = repo.DeniedKnowledgeIDsForSubjects(ctx, []string{"kb-1"},
		[]types.ACLSubject{
			{Type: types.KnowledgeACLSubjectUser, ID: "u-1"},
			{Type: types.KnowledgeACLSubjectRole, ID: "admin"},
		}, types.PermissionActionView, now)
	require.NoError(t, err)
	assert.Empty(t, denied, "an allow on any held subject satisfies the floor")
}

// TestUserDenyBeatsGroupAllow is deny-wins across subject kinds.
func TestUserDenyBeatsGroupAllow(t *testing.T) {
	db, repo := setupACLSubjectDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedACLDoc(t, db, "doc", "internal")
	seedACLGrant(t, db, "doc", "group", "g-1", "allow")
	seedACLGrant(t, db, "doc", "user", "u-1", "deny")

	denied, err := repo.DeniedKnowledgeIDsForSubjects(ctx, []string{"kb-1"},
		[]types.ACLSubject{
			{Type: types.KnowledgeACLSubjectUser, ID: "u-1"},
			{Type: types.KnowledgeACLSubjectGroup, ID: "g-1"},
		}, types.PermissionActionView, now)
	require.NoError(t, err)
	assert.Equal(t, []string{"doc"}, denied, "a personal deny beats a group allow")
}

// TestSameValueDifferentSubjectTypeDoesNotLeak covers the pair matching: a
// user id that happens to equal a role name must not borrow the other's grant.
func TestSameValueDifferentSubjectTypeDoesNotLeak(t *testing.T) {
	db, repo := setupACLSubjectDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedACLDoc(t, db, "doc", "secret")
	// "admin" is a role name here, not a user id.
	seedACLGrant(t, db, "doc", "role", "admin", "allow")

	// The caller is user "admin" but holds role "viewer": the role grant must
	// not apply, so the secret floor still denies.
	denied, err := repo.DeniedKnowledgeIDsForSubjects(ctx, []string{"kb-1"},
		[]types.ACLSubject{
			{Type: types.KnowledgeACLSubjectUser, ID: "admin"},
			{Type: types.KnowledgeACLSubjectRole, ID: "viewer"},
		}, types.PermissionActionView, now)
	require.NoError(t, err)
	assert.Equal(t, []string{"doc"}, denied,
		"(user,admin) must not match a (role,admin) grant")

	// With the admin role held, it applies.
	denied, err = repo.DeniedKnowledgeIDsForSubjects(ctx, []string{"kb-1"},
		[]types.ACLSubject{
			{Type: types.KnowledgeACLSubjectUser, ID: "admin"},
			{Type: types.KnowledgeACLSubjectRole, ID: "admin"},
		}, types.PermissionActionView, now)
	require.NoError(t, err)
	assert.Empty(t, denied)
}

// TestExpiredGrantDoesNotApply keeps a temporal grant from outliving its window.
func TestExpiredGrantDoesNotApply(t *testing.T) {
	db, repo := setupACLSubjectDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	past := now.Add(-time.Hour)

	seedACLDoc(t, db, "doc", "internal")
	require.NoError(t, db.Exec(`
		INSERT INTO knowledge_acl (id, tenant_id, knowledge_id, subject_type, subject_id, action, effect, expires_at, created_at)
		VALUES ('g-exp', 7, 'doc', 'group', 'g-1', 'view', 'deny', ?, ?)`, past, past).Error)

	denied, err := repo.DeniedKnowledgeIDsForSubjects(ctx, []string{"kb-1"},
		[]types.ACLSubject{{Type: types.KnowledgeACLSubjectGroup, ID: "g-1"}},
		types.PermissionActionView, now)
	require.NoError(t, err)
	assert.Empty(t, denied, "an expired deny no longer applies")
}

// TestEmptySubjectSetDeniesNothing is the no-op case (no identity).
func TestEmptySubjectSetDeniesNothing(t *testing.T) {
	db, repo := setupACLSubjectDB(t)
	seedACLDoc(t, db, "doc", "secret")
	denied, err := repo.DeniedKnowledgeIDsForSubjects(
		context.Background(), []string{"kb-1"}, nil, types.PermissionActionView, time.Now())
	require.NoError(t, err)
	assert.Nil(t, denied)
}
