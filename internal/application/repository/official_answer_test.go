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

// TestFindActiveByQuestionExcludesCandidate is the regression guard for the
// duplicate-answer check: the candidate itself has the highest version, so a
// query that does not exclude it matches the answer being published and the
// "one active answer per question" rule silently never fires.
func TestFindActiveByQuestionExcludesCandidate(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.OfficialAnswer{}))
	ctx := context.Background()
	now := time.Now().UTC()

	published := &types.OfficialAnswer{
		ID: "answer-1", TenantID: 7, Question: "E45 报警怎么处理？",
		QuestionNormalized: "e45 报警怎么处理", Status: string(types.OfficialAnswerPublished),
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	candidate := &types.OfficialAnswer{
		ID: "answer-2", TenantID: 7, Question: "E45 报警怎么处理？",
		QuestionNormalized: "e45 报警怎么处理", Status: string(types.OfficialAnswerApproved),
		Version: 2, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, db.Create(published).Error)
	require.NoError(t, db.Create(candidate).Error)

	repo := NewOfficialAnswerRepository(db)
	found, err := repo.FindActiveByQuestion(ctx, 7, "e45 报警怎么处理", candidate.ID)
	require.NoError(t, err)
	assert.Equal(t, published.ID, found.ID, "the other active answer must be found")

	// Excluding one row must still see the other.
	other, err := repo.FindActiveByQuestion(ctx, 7, "e45 报警怎么处理", published.ID)
	require.NoError(t, err)
	assert.Equal(t, candidate.ID, other.ID)

	// With only one active row left and that row excluded, the lookup misses:
	// that is the "no duplicate" answer, not an error the caller swallows.
	require.NoError(t, db.Delete(candidate).Error)
	_, err = repo.FindActiveByQuestion(ctx, 7, "e45 报警怎么处理", published.ID)
	require.ErrorIs(t, err, ErrOfficialAnswerNotFound)
}
