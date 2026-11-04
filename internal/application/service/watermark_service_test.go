package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
)

const watermarkTestDDL = `
CREATE TABLE watermark_records (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    knowledge_id VARCHAR(36) NOT NULL DEFAULT '',
    knowledge_base_id VARCHAR(36) NOT NULL DEFAULT '',
    file_name VARCHAR(512) NOT NULL DEFAULT '',
    classification VARCHAR(16) NOT NULL DEFAULT 'internal',
    strength VARCHAR(16) NOT NULL DEFAULT 'footer',
    mode VARCHAR(16) NOT NULL DEFAULT 'sidecar',
    watermark_text VARCHAR(255) NOT NULL DEFAULT '',
    source VARCHAR(16) NOT NULL DEFAULT 'download',
    request_id VARCHAR(36) NOT NULL DEFAULT '',
    created_at DATETIME
);
`

func newWatermarkFixture(t *testing.T) (*watermarkService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(watermarkTestDDL).Error)
	svc := &watermarkService{
		repo: repository.NewWatermarkRepository(db),
		now:  func() time.Time { return time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC) },
	}
	return svc, db
}

func secretKnowledge() *types.Knowledge {
	return &types.Knowledge{
		ID: "k-1", KnowledgeBaseID: "kb-1", Title: "E45 工艺卡",
		Classification: string(types.KnowledgeClassificationSecret),
	}
}

// TestRecordWritesLedgerForControlledDownloads is the §16.3 trace property.
func TestRecordWritesLedgerForControlledDownloads(t *testing.T) {
	svc, _ := newWatermarkFixture(t)
	ctx := types.WithCaller(context.Background(), types.Caller{
		TenantID: 7, UserID: "u-1", Role: types.TenantRoleViewer,
	})
	knowledge := secretKnowledge()

	applied := svc.Plan(ctx, 7, "u-1", knowledge, "spec.pdf", "download", "")
	require.NoError(t, svc.Record(ctx, 7, "u-1", knowledge, applied, "download", ""))

	rows, err := svc.List(ctx, 7, "", "", 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	record := rows[0]
	assert.Equal(t, "u-1", record.UserID)
	assert.Equal(t, "k-1", record.KnowledgeID)
	assert.Equal(t, "kb-1", record.KnowledgeBaseID)
	assert.Equal(t, string(types.KnowledgeClassificationSecret), record.Classification)
	assert.Equal(t, string(types.WatermarkStrengthTile), record.Strength)
	assert.Equal(t, string(types.WatermarkSidecar), record.Mode)
	assert.Equal(t, applied.Text, record.WatermarkText)
	assert.Equal(t, "download", record.Source)
}

// TestRecordSkipsPublicDownloads keeps the ledger on controlled content only.
func TestRecordSkipsPublicDownloads(t *testing.T) {
	svc, _ := newWatermarkFixture(t)
	ctx := context.Background()
	knowledge := &types.Knowledge{
		ID: "k-2", KnowledgeBaseID: "kb-1", Title: "公开说明",
		Classification: string(types.KnowledgeClassificationPublic),
	}
	applied := svc.Plan(ctx, 7, "u-1", knowledge, "readme.md", "download", "")
	require.NoError(t, svc.Record(ctx, 7, "u-1", knowledge, applied, "download", ""))

	rows, err := svc.List(ctx, 7, "", "", 10)
	require.NoError(t, err)
	assert.Empty(t, rows, "a public download is not a controlled distribution")
}

// TestPlanUsesTheDocumentClassification keeps the mark tied to the content,
// not to the caller's guess.
func TestPlanUsesTheDocumentClassification(t *testing.T) {
	svc, _ := newWatermarkFixture(t)
	ctx := context.Background()

	internal := svc.Plan(ctx, 7, "u-1", &types.Knowledge{
		ID: "k-a", Title: "A", Classification: string(types.KnowledgeClassificationInternal),
	}, "a.md", "download", "")
	assert.Equal(t, types.WatermarkStrengthFooter, internal.Strength)
	assert.Equal(t, types.WatermarkInjected, internal.Mode)

	confidential := svc.Plan(ctx, 7, "u-1", &types.Knowledge{
		ID: "k-b", Title: "B", Classification: string(types.KnowledgeClassificationConfidential),
	}, "b.docx", "download", "")
	assert.Equal(t, types.WatermarkStrengthFull, confidential.Strength)
	assert.Equal(t, types.WatermarkSidecar, confidential.Mode)
}

// TestPlanHandlesNilKnowledge keeps a lookup miss from panicking.
func TestPlanHandlesNilKnowledge(t *testing.T) {
	svc, _ := newWatermarkFixture(t)
	applied := svc.Plan(context.Background(), 7, "u-1", nil, "x.md", "download", "")
	assert.Equal(t, types.WatermarkStrength(""), applied.Strength)
}

// TestListFiltersByKnowledgeAndUser covers the ledger query.
func TestListFiltersByKnowledgeAndUser(t *testing.T) {
	svc, _ := newWatermarkFixture(t)
	ctx := context.Background()
	first := secretKnowledge()
	second := &types.Knowledge{
		ID: "k-9", KnowledgeBaseID: "kb-1", Title: "另一篇",
		Classification: string(types.KnowledgeClassificationInternal),
	}
	for _, pair := range []struct {
		user string
		doc  *types.Knowledge
	}{{"u-1", first}, {"u-2", first}, {"u-1", second}} {
		applied := svc.Plan(ctx, 7, pair.user, pair.doc, "f.md", "download", "")
		require.NoError(t, svc.Record(ctx, 7, pair.user, pair.doc, applied, "download", ""))
	}

	byDoc, err := svc.List(ctx, 7, "k-1", "", 10)
	require.NoError(t, err)
	assert.Len(t, byDoc, 2)

	byUser, err := svc.List(ctx, 7, "", "u-1", 10)
	require.NoError(t, err)
	assert.Len(t, byUser, 2)

	mine, err := svc.List(ctx, 7, "k-1", "u-1", 10)
	require.NoError(t, err)
	require.Len(t, mine, 1)
	assert.Equal(t, "u-1", mine[0].UserID)
}

// TestRecordTracksExportSource keeps the ledger able to tell a plain download
// from an approved export.
func TestRecordTracksExportSource(t *testing.T) {
	svc, _ := newWatermarkFixture(t)
	ctx := context.Background()
	knowledge := secretKnowledge()
	applied := svc.Plan(ctx, 7, "u-1", knowledge, "spec.pdf", "export", "req-1")
	require.NoError(t, svc.Record(ctx, 7, "u-1", knowledge, applied, "export", "req-1"))

	rows, err := svc.List(ctx, 7, "", "", 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "export", rows[0].Source)
	assert.Equal(t, "req-1", rows[0].RequestID)
}
