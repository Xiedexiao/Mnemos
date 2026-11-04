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

// qualityMetricsTestDDL is the minimal schema the aggregate queries touch.
//
// 这些查询跨表聚合（知识 × 阅读 × 证据包 × 抽检 × 反馈 × 搜索日志 × 缺口 ×
// 变更信号），所以 fixture 必须包含全部被引用的表——只用桩无法发现 SQL
// 方言问题（EXTRACT、jsonb 操作符、CASE 聚合）。
const qualityMetricsTestDDL = `
CREATE TABLE knowledges (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL DEFAULT '',
    title VARCHAR(255) NOT NULL DEFAULT '',
    lifecycle_status VARCHAR(32) NOT NULL DEFAULT 'valid',
    deleted_at DATETIME
);
CREATE TABLE knowledge_view_events (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    user_id VARCHAR(36) NOT NULL DEFAULT '',
    knowledge_id VARCHAR(36) NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL DEFAULT '',
    created_at DATETIME
);
CREATE TABLE evidence_packs (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    request_id VARCHAR(64) NOT NULL DEFAULT '',
    session_id VARCHAR(36) NOT NULL DEFAULT '',
    message_id VARCHAR(36) NOT NULL DEFAULT '',
    user_id VARCHAR(36) NOT NULL DEFAULT '',
    question TEXT NOT NULL DEFAULT '',
    candidates TEXT NOT NULL DEFAULT '[]',
    context_snapshot TEXT NOT NULL DEFAULT '{}',
    citations TEXT NOT NULL DEFAULT '[]',
    refusal VARCHAR(16) NOT NULL DEFAULT '',
    created_at DATETIME
);
CREATE TABLE change_signals (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    signal_type VARCHAR(32) NOT NULL DEFAULT '',
    source_id VARCHAR(128) NOT NULL DEFAULT '',
    knowledge_id VARCHAR(36) NOT NULL DEFAULT '',
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    change_request_id VARCHAR(36) NOT NULL DEFAULT '',
    created_at DATETIME
);
CREATE TABLE change_requests (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    title VARCHAR(255) NOT NULL DEFAULT '',
    effective_at DATETIME,
    created_at DATETIME
);
CREATE TABLE approval_tasks (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL DEFAULT '',
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    sla_due DATETIME,
    decision_at DATETIME
);
CREATE TABLE quality_tasks (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    task_type VARCHAR(24) NOT NULL,
    source_type VARCHAR(16) NOT NULL DEFAULT '',
    source_id VARCHAR(36) NOT NULL DEFAULT '',
    knowledge_id VARCHAR(36) NOT NULL DEFAULT '',
    status VARCHAR(16) NOT NULL DEFAULT 'open',
    created_at DATETIME,
    resolved_at DATETIME
);
CREATE TABLE sampling_inspections (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL,
    period VARCHAR(16) NOT NULL DEFAULT '',
    risk_level VARCHAR(16) NOT NULL DEFAULT 'mid',
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    created_at DATETIME
);
CREATE TABLE message_feedback (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    message_id VARCHAR(36) NOT NULL,
    rating VARCHAR(16) NOT NULL,
    created_at DATETIME
);
CREATE TABLE search_logs (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    query VARCHAR(512) NOT NULL,
    query_normalized VARCHAR(512) NOT NULL DEFAULT '',
    result_count INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME
);
CREATE TABLE knowledge_gaps (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    query_normalized VARCHAR(512) NOT NULL DEFAULT '',
    status VARCHAR(16) NOT NULL DEFAULT 'open',
    created_at DATETIME,
    updated_at DATETIME
);
`

func setupQualityMetricsDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(qualityMetricsTestDDL).Error)
	return db
}

// TestCountPublishedKnowledgeExcludesNonUsable covers the denominator.
func TestCountPublishedKnowledgeExcludesNonUsable(t *testing.T) {
	db := setupQualityMetricsDB(t)
	repo := NewQualityMetricsRepository(db)
	ctx := context.Background()

	for _, row := range []struct{ id, status string }{
		{"a", "valid"}, {"b", ""}, {"c", "draft"}, {"d", "archived"},
	} {
		require.NoError(t, db.Exec(`
			INSERT INTO knowledges (id, tenant_id, lifecycle_status) VALUES (?, 7, ?)`,
			row.id, row.status).Error)
	}
	require.NoError(t, db.Exec(`UPDATE knowledges SET deleted_at = CURRENT_TIMESTAMP WHERE id = 'b'`).Error)

	total, err := repo.CountPublishedKnowledge(ctx, 7)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total, "only the usable published row counts")
}

// TestCountUsedKnowledgeCountsReadsAndCitations covers the numerator.
func TestCountUsedKnowledgeCountsReadsAndCitations(t *testing.T) {
	db := setupQualityMetricsDB(t)
	repo := NewQualityMetricsRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()

	for _, id := range []string{"k1", "k2", "k3"} {
		require.NoError(t, db.Exec(`
			INSERT INTO knowledges (id, tenant_id, lifecycle_status) VALUES (?, 7, 'valid')`, id).Error)
	}
	// k1 was read; k2 was cited; k3 untouched.
	require.NoError(t, db.Exec(`
		INSERT INTO knowledge_view_events (id, tenant_id, knowledge_id, created_at)
		VALUES ('v1', 7, 'k1', ?)`, now).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO evidence_packs (id, tenant_id, citations, created_at)
		VALUES ('e1', 7, ?, ?)`, `[{"knowledge_id":"k2"}]`, now).Error)

	used, err := repo.CountUsedKnowledge(ctx, 7, now.Add(-time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 2, used, "a read and a citation both count as use")

	// Outside the window nothing was used.
	used, err = repo.CountUsedKnowledge(ctx, 7, now.Add(time.Hour))
	require.NoError(t, err)
	assert.Zero(t, used)
}

// TestSignalToUpdatePairsSkipsImpossibleOrdering covers the latency source.
func TestSignalToUpdatePairsSkipsImpossibleOrdering(t *testing.T) {
	db := setupQualityMetricsDB(t)
	repo := NewQualityMetricsRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()

	// A valid pair: signal collected, then the change request took effect.
	require.NoError(t, db.Exec(`
		INSERT INTO change_signals (id, tenant_id, status, change_request_id, created_at)
		VALUES ('s1', 7, 'converted', 'c1', ?)`, now.Add(-4*time.Hour)).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO change_requests (id, tenant_id, effective_at) VALUES ('c1', 7, ?)`, now).Error)

	// A pending signal without a change request must not appear.
	require.NoError(t, db.Exec(`
		INSERT INTO change_signals (id, tenant_id, status, created_at)
		VALUES ('s2', 7, 'pending', ?)`, now).Error)

	pairs, err := repo.SignalToUpdatePairs(ctx, 7, now.Add(-24*time.Hour), 100)
	require.NoError(t, err)
	require.Len(t, pairs, 1)
	assert.InDelta(t, 4.0, pairs[0].To.Sub(pairs[0].From).Hours(), 0.01)
}

// TestReviewOnTimeCountsOnlyDeadlinedDecisions covers the rate's denominator.
func TestReviewOnTimeCountsOnlyDeadlinedDecisions(t *testing.T) {
	db := setupQualityMetricsDB(t)
	repo := NewQualityMetricsRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()

	// On time.
	require.NoError(t, db.Exec(`
		INSERT INTO approval_tasks (id, tenant_id, status, sla_due, decision_at)
		VALUES ('t1', 7, 'approved', ?, ?)`, now, now.Add(-time.Hour)).Error)
	// Late.
	require.NoError(t, db.Exec(`
		INSERT INTO approval_tasks (id, tenant_id, status, sla_due, decision_at)
		VALUES ('t2', 7, 'approved', ?, ?)`, now.Add(-5*time.Hour), now).Error)
	// No deadline: excluded from the denominator.
	require.NoError(t, db.Exec(`
		INSERT INTO approval_tasks (id, tenant_id, status, decision_at)
		VALUES ('t3', 7, 'approved', ?)`, now).Error)
	// Still pending: not a completed review.
	require.NoError(t, db.Exec(`
		INSERT INTO approval_tasks (id, tenant_id, status, sla_due)
		VALUES ('t4', 7, 'pending', ?)`, now).Error)

	onTime, completed, err := repo.ReviewOnTime(ctx, 7, now.Add(-24*time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 1, onTime)
	assert.EqualValues(t, 2, completed, "only deadlined decisions form the denominator")
}

// TestSamplingStatsCountsDecidedOnly covers the fail-rate source.
func TestSamplingStatsCountsDecidedOnly(t *testing.T) {
	db := setupQualityMetricsDB(t)
	repo := NewQualityMetricsRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()

	for _, row := range []struct{ id, status string }{
		{"i1", "passed"}, {"i2", "failed"}, {"i3", "pending"}, {"i4", "waived"},
	} {
		require.NoError(t, db.Exec(`
			INSERT INTO sampling_inspections (id, tenant_id, knowledge_id, status, created_at)
			VALUES (?, 7, ?, ?, ?)`, row.id, row.id, row.status, now).Error)
	}
	passed, failed, err := repo.SamplingStats(ctx, 7, now.Add(-time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 1, passed)
	assert.EqualValues(t, 1, failed)
}

// TestAnswerCountsExcludesRefusals covers the citation-coverage denominator.
func TestAnswerCountsExcludesRefusals(t *testing.T) {
	db := setupQualityMetricsDB(t)
	repo := NewQualityMetricsRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, db.Exec(`
		INSERT INTO evidence_packs (id, tenant_id, citations, refusal, created_at)
		VALUES ('e1', 7, ?, '', ?)`, `[{"knowledge_id":"k1"}]`, now).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO evidence_packs (id, tenant_id, citations, refusal, created_at)
		VALUES ('e2', 7, '[]', '', ?)`, now).Error)
	// A refusal has no citations by definition and must not lower the rate.
	require.NoError(t, db.Exec(`
		INSERT INTO evidence_packs (id, tenant_id, citations, refusal, created_at)
		VALUES ('e3', 7, '[]', 'no_evidence', ?)`, now).Error)

	withCitations, total, err := repo.AnswerCounts(ctx, 7, now.Add(-time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 1, withCitations)
	assert.EqualValues(t, 2, total, "the refusal is excluded from both sides")
}

// TestFeedbackResolution covers the resolution-rate source.
func TestFeedbackResolution(t *testing.T) {
	db := setupQualityMetricsDB(t)
	repo := NewQualityMetricsRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()

	for i, rating := range []string{"useful", "useful", "useless"} {
		require.NoError(t, db.Exec(`
			INSERT INTO message_feedback (id, tenant_id, message_id, rating, created_at)
			VALUES (?, 7, ?, ?, ?)`, "f"+itoaQuality(i), "m"+itoaQuality(i), rating, now).Error)
	}
	useful, rated, err := repo.FeedbackResolution(ctx, 7, now.Add(-time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 2, useful)
	assert.EqualValues(t, 3, rated)
}

// TestRepeatedZeroResultQueriesCountsRecurringOnly covers the drop source.
func TestRepeatedZeroResultQueriesCountsRecurringOnly(t *testing.T) {
	db := setupQualityMetricsDB(t)
	repo := NewQualityMetricsRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()

	for i, query := range []string{"e45", "e45", "e45", "unique"} {
		require.NoError(t, db.Exec(`
			INSERT INTO search_logs (id, tenant_id, query, query_normalized, result_count, created_at)
			VALUES (?, 7, ?, ?, 0, ?)`, "l"+itoaQuality(i), query, query, now).Error)
	}

	total, err := repo.RepeatedZeroResultQueries(ctx, 7, now.Add(-time.Hour), now.Add(time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, 3, total, "only the recurring query counts; the one-off does not")
}

// TestGapCountsSeparatesCreatedAndClosed covers the gap-close source.
func TestGapCountsSeparatesCreatedAndClosed(t *testing.T) {
	db := setupQualityMetricsDB(t)
	repo := NewQualityMetricsRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()
	since := now.Add(-24 * time.Hour)
	until := now.Add(time.Hour)

	// One created and not closed.
	require.NoError(t, db.Exec(`
		INSERT INTO knowledge_gaps (id, tenant_id, status, created_at, updated_at)
		VALUES ('g1', 7, 'open', ?, ?)`, now, now).Error)
	// One created earlier, closed in the window.
	require.NoError(t, db.Exec(`
		INSERT INTO knowledge_gaps (id, tenant_id, status, created_at, updated_at)
		VALUES ('g2', 7, 'closed', ?, ?)`, since.Add(-time.Hour), now).Error)

	created, closed, err := repo.GapCounts(ctx, 7, since, until)
	require.NoError(t, err)
	assert.EqualValues(t, 1, created, "only the gap created inside the window")
	assert.EqualValues(t, 1, closed, "the gap closed inside the window")
}

// itoaQuality renders a small int.
func itoaQuality(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 4)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// compile-time guard.
var _ = types.QualityMetricWindow{}
