package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
)

// setupKnowledgeListPermissionTestDB extends the tag fixture with the
// classification column added by the lifecycle migration (000135). The shared
// fixture predates it, and this file is the only place that needs it.
func setupKnowledgeListPermissionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupKnowledgeTagTestDB(t)
	require.NoError(t, db.Exec(`ALTER TABLE knowledges ADD COLUMN classification TEXT`).Error)
	return db
}

// TestApplyKnowledgeListFilter_HidesDeniedEntries covers V3.0 §2.10: the
// document list is an existence channel, so a denied entry must not appear as a
// title-only row — and because the predicate is pushed into the query, the page
// total does not reveal how many entries are hidden either.
func TestApplyKnowledgeListFilter_HidesDeniedEntries(t *testing.T) {
	db := setupKnowledgeListPermissionTestDB(t)
	ctx := context.Background()

	kbID := uuid.New().String()
	visible := uuid.New().String()
	denied := uuid.New().String()

	require.NoError(t, db.Exec(`
		INSERT INTO knowledges (id, tenant_id, knowledge_base_id, type, title, parse_status, classification)
		VALUES (?, 1, ?, 'file', 'visible', 'completed', 'internal'),
		       (?, 1, ?, 'file', 'denied',  'completed', 'internal')
	`, visible, kbID, denied, kbID).Error)

	base := func() *gorm.DB {
		return db.WithContext(ctx).Model(&types.Knowledge{}).
			Where("tenant_id = ? AND knowledge_base_id = ?", uint64(1), kbID)
	}

	var allIDs []string
	require.NoError(t, applyKnowledgeListFilter(base(), types.KnowledgeListFilter{}).Pluck("id", &allIDs).Error)
	assert.ElementsMatch(t, []string{visible, denied}, allIDs, "no filter must list both entries")

	var filteredIDs []string
	require.NoError(t, applyKnowledgeListFilter(base(), types.KnowledgeListFilter{
		ExcludeKnowledgeIDs: []string{denied},
	}).Pluck("id", &filteredIDs).Error)
	assert.Equal(t, []string{visible}, filteredIDs)

	// The count must follow the same predicate, otherwise "1 条" would still
	// tell the reader that a second entry exists.
	var total int64
	require.NoError(t, applyKnowledgeListFilter(base(), types.KnowledgeListFilter{
		ExcludeKnowledgeIDs: []string{denied},
	}).Count(&total).Error)
	assert.Equal(t, int64(1), total)
}

// TestApplyKnowledgeListFilter_ClassificationCeiling keeps entries above the
// caller's ceiling out of both the page and the total, including legacy rows
// whose classification column is empty (treated as internal).
func TestApplyKnowledgeListFilter_ClassificationCeiling(t *testing.T) {
	db := setupKnowledgeListPermissionTestDB(t)
	ctx := context.Background()

	kbID := uuid.New().String()
	public := uuid.New().String()
	internal := uuid.New().String()
	secret := uuid.New().String()
	legacy := uuid.New().String()

	require.NoError(t, db.Exec(`
		INSERT INTO knowledges (id, tenant_id, knowledge_base_id, type, title, parse_status, classification)
		VALUES (?, 1, ?, 'file', 'pub',   'completed', 'public'),
		       (?, 1, ?, 'file', 'int',   'completed', 'internal'),
		       (?, 1, ?, 'file', 'sec',   'completed', 'secret'),
		       (?, 1, ?, 'file', 'legacy','completed', '')
	`, public, kbID, internal, kbID, secret, kbID, legacy, kbID).Error)

	query := applyKnowledgeListFilter(
		db.WithContext(ctx).Model(&types.Knowledge{}).
			Where("tenant_id = ? AND knowledge_base_id = ?", uint64(1), kbID),
		types.KnowledgeListFilter{MaxClassification: types.KnowledgeClassificationInternal},
	)
	var ids []string
	require.NoError(t, query.Pluck("id", &ids).Error)
	assert.ElementsMatch(t, []string{public, internal, legacy}, ids,
		"secret must be hidden and legacy empty classifications treated as internal")
}
