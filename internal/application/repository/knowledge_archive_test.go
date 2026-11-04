package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
)

// TestArchiveListingFiltersByLifecycle is the §7.8 archive-library query:
// archived content is excluded from search but must remain findable on demand.
func TestArchiveListingFiltersByLifecycle(t *testing.T) {
	db := setupKnowledgeTestDB(t)
	repo := NewKnowledgeRepository(db)
	ctx := context.Background()

	for id, status := range map[string]string{
		"live":     "valid",
		"archived": "archived",
		"expired":  "expired",
		"empty":    "",
	} {
		require.NoError(t, db.Exec(`
			INSERT INTO knowledges (id, tenant_id, knowledge_base_id, type, title, parse_status, lifecycle_status)
			VALUES (?, 7, 'kb-1', 'file', ?, 'completed', ?)`,
			id, id, status).Error)
	}

	// No filter: everything in the space.
	all, total, err := repo.ListPagedKnowledgeByKnowledgeBaseID(ctx, 7, "kb-1",
		&types.Pagination{Page: 1, PageSize: 20}, types.KnowledgeListFilter{})
	require.NoError(t, err)
	assert.EqualValues(t, 4, total)
	assert.Len(t, all, 4)

	// Archive library: only archived rows.
	archived, total, err := repo.ListPagedKnowledgeByKnowledgeBaseID(ctx, 7, "kb-1",
		&types.Pagination{Page: 1, PageSize: 20},
		types.KnowledgeListFilter{LifecycleStatuses: []string{"archived"}})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	require.Len(t, archived, 1)
	assert.Equal(t, "archived", archived[0].ID)

	// An empty status means "valid", matching how reads resolve a blank column.
	// The row with an empty column must be found by that filter, and the
	// paginated total must agree with the rows.
	live, total, err := repo.ListPagedKnowledgeByKnowledgeBaseID(ctx, 7, "kb-1",
		&types.Pagination{Page: 1, PageSize: 20},
		types.KnowledgeListFilter{LifecycleStatuses: []string{""}})
	require.NoError(t, err)
	assert.EqualValues(t, 2, total, "the blank-status row counts as valid")
	assert.Len(t, live, 2)

	// Several statuses at once.
	gone, total, err := repo.ListPagedKnowledgeByKnowledgeBaseID(ctx, 7, "kb-1",
		&types.Pagination{Page: 1, PageSize: 20},
		types.KnowledgeListFilter{LifecycleStatuses: []string{"archived", "expired"}})
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	assert.Len(t, gone, 2)
}

// TestArchiveListingPaginates keeps a large archive paginating correctly with
// the filter applied (the count must not include filtered-out rows).
func TestArchiveListingPaginates(t *testing.T) {
	db := setupKnowledgeTestDB(t)
	repo := NewKnowledgeRepository(db)
	ctx := context.Background()

	for _, row := range []struct {
		id     string
		status string
	}{
		{"a1", "archived"}, {"a2", "archived"}, {"a3", "archived"},
		{"v1", "valid"}, {"d1", "draft"},
	} {
		require.NoError(t, db.Exec(`
			INSERT INTO knowledges (id, tenant_id, knowledge_base_id, type, title, parse_status,
				lifecycle_status, created_at)
			VALUES (?, 7, 'kb-1', 'file', ?, 'completed', ?, ?)`,
			row.id, row.id, row.status, time.Now().UTC()).Error)
	}

	page, total, err := repo.ListPagedKnowledgeByKnowledgeBaseID(ctx, 7, "kb-1",
		&types.Pagination{Page: 1, PageSize: 2},
		types.KnowledgeListFilter{LifecycleStatuses: []string{"archived"}})
	require.NoError(t, err)
	assert.EqualValues(t, 3, total, "the total counts only archived rows")
	assert.Len(t, page, 2)
}
