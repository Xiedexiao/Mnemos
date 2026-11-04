package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
)

const samplingTestDDL = `
CREATE TABLE knowledges (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL DEFAULT '',
    title VARCHAR(255) NOT NULL DEFAULT '',
    classification VARCHAR(16) NOT NULL DEFAULT 'internal',
    lifecycle_status VARCHAR(32) NOT NULL DEFAULT 'valid',
    deleted_at DATETIME,
    updated_at DATETIME
);
CREATE TABLE sampling_inspections (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL DEFAULT '',
    period VARCHAR(16) NOT NULL,
    risk_level VARCHAR(16) NOT NULL DEFAULT 'mid',
    ratio INTEGER NOT NULL DEFAULT 10,
    title VARCHAR(255) NOT NULL DEFAULT '',
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    reviewed_by VARCHAR(36) NOT NULL DEFAULT '',
    reviewed_at DATETIME,
    note VARCHAR(1024) NOT NULL DEFAULT '',
    follow_up_task_id VARCHAR(36) NOT NULL DEFAULT '',
    created_at DATETIME,
    updated_at DATETIME,
    CONSTRAINT uq_sampling_period_doc UNIQUE (tenant_id, knowledge_id, period)
);
CREATE TABLE quality_tasks (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    task_type VARCHAR(24) NOT NULL,
    source_type VARCHAR(16) NOT NULL,
    source_id VARCHAR(36) NOT NULL DEFAULT '',
    knowledge_id VARCHAR(36) NOT NULL DEFAULT '',
    chunk_id VARCHAR(36) NOT NULL DEFAULT '',
    detail TEXT NOT NULL DEFAULT '{}',
    status VARCHAR(16) NOT NULL DEFAULT 'open',
    assignee_id VARCHAR(36) NOT NULL DEFAULT '',
    resolution_note TEXT NOT NULL DEFAULT '',
    created_at DATETIME,
    updated_at DATETIME,
    resolved_at DATETIME
);
`

// fixtureSeq keeps two fixtures inside one test from sharing an in-memory
// database (they would otherwise both key on t.Name()).
var fixtureSeq int

func newSamplingFixture(t *testing.T) (*samplingService, *gorm.DB) {
	t.Helper()
	fixtureSeq++
	db, err := gorm.Open(sqlite.Open(
		fmt.Sprintf("file:%s-%d?mode=memory&cache=shared", t.Name(), fixtureSeq)), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(samplingTestDDL).Error)
	svc := &samplingService{
		repo:    repository.NewSamplingRepository(db),
		quality: repository.NewQualityTaskRepository(db),
		now:     func() time.Time { return time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC) },
	}
	return svc, db
}

func seedSamplingKnowledge(t *testing.T, db *gorm.DB, id, classification, lifecycle string) {
	t.Helper()
	require.NoError(t, db.Exec(`
		INSERT INTO knowledges (id, tenant_id, knowledge_base_id, title, classification,
			lifecycle_status, updated_at)
		VALUES (?, 7, 'kb-1', ?, ?, ?, ?)`,
		id, id, classification, lifecycle, time.Now().UTC()).Error)
}

// TestDrawSamplesHighRiskFully is the §6.6 core: every high-risk document is
// inspected.
func TestDrawSamplesHighRiskFully(t *testing.T) {
	svc, db := newSamplingFixture(t)
	ctx := context.Background()
	for _, id := range []string{"s1", "s2", "s3"} {
		seedSamplingKnowledge(t, db, id, "secret", "valid")
	}
	seedSamplingKnowledge(t, db, "i1", "internal", "valid")

	result, err := svc.Draw(ctx, 7, "2026-11", 100)
	require.NoError(t, err)
	assert.Equal(t, 4, result.Candidates)
	assert.Equal(t, 3, result.ByRatio[int(types.SamplingRatioFull)],
		"every secret document must be drawn")

	rows, err := svc.List(ctx, 7, "2026-11", "", 50)
	require.NoError(t, err)
	for _, row := range rows {
		if row.KnowledgeID == "i1" {
			assert.Equal(t, int(types.SamplingRatioTen), row.Ratio)
			continue
		}
		assert.Equal(t, int(types.SamplingRatioFull), row.Ratio)
	}
}

// TestDrawSkipsNonPublishedContent keeps drafts out of the sample.
func TestDrawSkipsNonPublishedContent(t *testing.T) {
	svc, db := newSamplingFixture(t)
	ctx := context.Background()
	seedSamplingKnowledge(t, db, "draft", "secret", "draft")
	seedSamplingKnowledge(t, db, "live", "secret", "valid")

	result, err := svc.Draw(ctx, 7, "2026-11", 100)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Candidates, "only the published document is a candidate")
	rows, err := svc.List(ctx, 7, "2026-11", "", 50)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "live", rows[0].KnowledgeID)
}

// TestDrawIsIdempotentWithinAPeriod keeps a daily run from duplicating samples.
func TestDrawIsIdempotentWithinAPeriod(t *testing.T) {
	svc, db := newSamplingFixture(t)
	ctx := context.Background()
	for _, id := range []string{"s1", "s2"} {
		seedSamplingKnowledge(t, db, id, "secret", "valid")
	}

	first, err := svc.Draw(ctx, 7, "2026-11", 100)
	require.NoError(t, err)
	assert.Equal(t, 2, first.Sampled)

	second, err := svc.Draw(ctx, 7, "2026-11", 100)
	require.NoError(t, err)
	assert.Zero(t, second.Sampled, "the same period must not re-draw")
	assert.Equal(t, 2, second.SkippedExisting)

	rows, err := svc.List(ctx, 7, "2026-11", "", 50)
	require.NoError(t, err)
	assert.Len(t, rows, 2)
}

// TestDrawUsesDeterministicSampleSoARerunMatches is the reproducibility rule.
func TestDrawUsesDeterministicSampleSoARerunMatches(t *testing.T) {
	svc, db := newSamplingFixture(t)
	ctx := context.Background()
	for i := 0; i < 40; i++ {
		seedSamplingKnowledge(t, db, "d"+itoaSmall(i), "internal", "valid")
	}
	first, err := svc.Draw(ctx, 7, "2026-11", 100)
	require.NoError(t, err)
	firstRows, err := svc.List(ctx, 7, "2026-11", "", 100)
	require.NoError(t, err)

	// A different tenant in a fresh database with the same documents has no
	// state to inherit, so it re-draws from scratch.
	svc2, db2 := newSamplingFixture(t)
	for i := 0; i < 40; i++ {
		seedSamplingKnowledge(t, db2, "d"+itoaSmall(i), "internal", "valid")
	}
	second, err := svc2.Draw(context.Background(), 7, "2026-11", 100)
	require.NoError(t, err)
	assert.Equal(t, first.Sampled, second.Sampled,
		"the same documents and period must draw the same count")
	secondRows, err := svc2.List(context.Background(), 7, "2026-11", "", 100)
	require.NoError(t, err)

	firstIDs := make([]string, 0, len(firstRows))
	for _, row := range firstRows {
		firstIDs = append(firstIDs, row.KnowledgeID)
	}
	secondIDs := make([]string, 0, len(secondRows))
	for _, row := range secondRows {
		secondIDs = append(secondIDs, row.KnowledgeID)
	}
	assert.ElementsMatch(t, firstIDs, secondIDs, "the drawn set must be identical")
}

// TestDecideFailedOpensCorrectiveTask is the point of sampling: a failure has
// to produce work, not just a status change.
func TestDecideFailedOpensCorrectiveTask(t *testing.T) {
	svc, db := newSamplingFixture(t)
	ctx := context.Background()
	seedSamplingKnowledge(t, db, "s1", "secret", "valid")
	_, err := svc.Draw(ctx, 7, "2026-11", 100)
	require.NoError(t, err)
	rows, err := svc.List(ctx, 7, "2026-11", "", 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	decided, err := svc.Decide(ctx, 7, "reviewer-1", types.SamplingDecisionInput{
		InspectionID: rows[0].ID,
		Status:       "failed",
		Note:         "步骤 3 与设备手册不符",
	})
	require.NoError(t, err)
	assert.Equal(t, string(types.SamplingFailed), decided.Status)
	assert.NotEmpty(t, decided.FollowUpTaskID, "a failure opens a corrective task")

	var task struct {
		TaskType    string
		KnowledgeID string
		Status      string
	}
	require.NoError(t, db.Raw(`SELECT task_type, knowledge_id, status FROM quality_tasks`).
		Scan(&task).Error)
	assert.Equal(t, string(types.QualityTaskTypeSamplingFailed), task.TaskType)
	assert.Equal(t, "s1", task.KnowledgeID)
	assert.Equal(t, string(types.QualityTaskStatusOpen), task.Status)
}

// TestDecidePassedDoesNotOpenTask covers the passing path.
func TestDecidePassedDoesNotOpenTask(t *testing.T) {
	svc, db := newSamplingFixture(t)
	ctx := context.Background()
	seedSamplingKnowledge(t, db, "s1", "secret", "valid")
	_, err := svc.Draw(ctx, 7, "2026-11", 100)
	require.NoError(t, err)
	rows, err := svc.List(ctx, 7, "2026-11", "", 10)
	require.NoError(t, err)

	decided, err := svc.Decide(ctx, 7, "reviewer-1", types.SamplingDecisionInput{
		InspectionID: rows[0].ID, Status: "passed",
	})
	require.NoError(t, err)
	assert.Equal(t, string(types.SamplingPassed), decided.Status)
	assert.Empty(t, decided.FollowUpTaskID)
	var count int64
	require.NoError(t, db.Table("quality_tasks").Count(&count).Error)
	assert.Zero(t, count)
}

// TestDecideRejectsSecondVerdict keeps a decided inspection immutable.
func TestDecideRejectsSecondVerdict(t *testing.T) {
	svc, db := newSamplingFixture(t)
	ctx := context.Background()
	seedSamplingKnowledge(t, db, "s1", "secret", "valid")
	_, err := svc.Draw(ctx, 7, "2026-11", 100)
	require.NoError(t, err)
	rows, err := svc.List(ctx, 7, "2026-11", "", 10)
	require.NoError(t, err)

	_, err = svc.Decide(ctx, 7, "r1", types.SamplingDecisionInput{
		InspectionID: rows[0].ID, Status: "passed",
	})
	require.NoError(t, err)
	_, err = svc.Decide(ctx, 7, "r2", types.SamplingDecisionInput{
		InspectionID: rows[0].ID, Status: "failed", Note: "改主意了",
	})
	require.Error(t, err, "a decided inspection must not be re-decided")
}

// TestStatsExcludesPendingAndWaivedFromPassRate is the metric definition.
func TestStatsExcludesPendingAndWaivedFromPassRate(t *testing.T) {
	svc, db := newSamplingFixture(t)
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c", "d"} {
		seedSamplingKnowledge(t, db, id, "secret", "valid")
	}
	_, err := svc.Draw(ctx, 7, "2026-11", 100)
	require.NoError(t, err)
	rows, err := svc.List(ctx, 7, "2026-11", "", 10)
	require.NoError(t, err)
	require.Len(t, rows, 4)

	require.NoError(t, decideSampling(t, svc, rows[0].ID, "passed", ""))
	require.NoError(t, decideSampling(t, svc, rows[1].ID, "failed", "内容有误"))
	require.NoError(t, decideSampling(t, svc, rows[2].ID, "waived", "该文档即将下架"))
	// rows[3] stays pending.

	stats, err := svc.Stats(ctx, 7, "2026-11")
	require.NoError(t, err)
	assert.EqualValues(t, 4, stats.Total)
	assert.EqualValues(t, 1, stats.Passed)
	assert.EqualValues(t, 1, stats.Failed)
	assert.EqualValues(t, 1, stats.Waived)
	assert.EqualValues(t, 1, stats.Pending)
	assert.InDelta(t, 0.5, stats.PassRate, 0.001,
		"1 passed / (1 passed + 1 failed); pending and waived are not in the denominator")
}

// decideSampling is a helper that fails the test on error.
func decideSampling(
	t *testing.T, svc *samplingService, id, status, note string,
) error {
	t.Helper()
	_, err := svc.Decide(context.Background(), 7, "reviewer", types.SamplingDecisionInput{
		InspectionID: id, Status: status, Note: note,
	})
	return err
}
