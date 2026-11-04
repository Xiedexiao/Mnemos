package types

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildReadingTOCFromMarkdown covers the Markdown heading path.
func TestBuildReadingTOCFromMarkdown(t *testing.T) {
	toc := BuildReadingTOC("k-1", []string{
		"# 换型准备",
		"正文段落",
		"## 工具清单\n- 扳手",
		"### 安全须知",
	})
	require.True(t, toc.HasTOC)
	assert.Equal(t, 4, toc.ChunkCount)
	require.Len(t, toc.Entries, 3)
	assert.Equal(t, "换型准备", toc.Entries[0].Title)
	assert.Equal(t, 1, toc.Entries[0].Level)
	assert.Equal(t, 0, toc.Entries[0].ChunkIndex)
	assert.Equal(t, "工具清单", toc.Entries[1].Title)
	assert.Equal(t, 2, toc.Entries[1].Level)
	assert.Equal(t, 2, toc.Entries[1].ChunkIndex, "the chunk ordinal is preserved")
	assert.Equal(t, 3, toc.Entries[2].Level)
}

// TestBuildReadingTOCFromChineseHeadings covers documents that use 第X章 / 一、
// numbering instead of Markdown.
func TestBuildReadingTOCFromChineseHeadings(t *testing.T) {
	toc := BuildReadingTOC("k-2", []string{
		"第一章 总则",
		"一、适用范围",
		"正文",
		"第二十三条 处罚",
	})
	require.True(t, toc.HasTOC)
	require.Len(t, toc.Entries, 3)
	assert.Equal(t, "第一章 总则", toc.Entries[0].Title)
	assert.Equal(t, 2, toc.Entries[0].Level, "Chinese numbering has no depth, so it is level 2")
	assert.Equal(t, "一、适用范围", toc.Entries[1].Title)
	assert.Equal(t, "第二十三条 处罚", toc.Entries[2].Title)
}

// TestBuildReadingTOCIgnoresBodyHashes is the "only the first line" rule: a
// '#' inside a paragraph is content, not structure.
func TestBuildReadingTOCIgnoresBodyHashes(t *testing.T) {
	toc := BuildReadingTOC("k-3", []string{
		"这是正文\n# 这不是标题",
	})
	assert.False(t, toc.HasTOC)
	assert.Empty(t, toc.Entries)
}

// TestBuildReadingTOCEmpty covers a flat document.
func TestBuildReadingTOCEmpty(t *testing.T) {
	toc := BuildReadingTOC("k-4", nil)
	assert.False(t, toc.HasTOC)
	assert.Equal(t, 0, toc.ChunkCount)
	assert.Empty(t, toc.Entries)
}

// TestNormalizeReadingProgress clamps the position.
func TestNormalizeReadingProgress(t *testing.T) {
	normalized, err := NormalizeReadingProgress(ReadingProgressInput{
		KnowledgeID: " k-1 ", Percent: 140, ChunkCount: -3,
	})
	require.NoError(t, err)
	assert.Equal(t, "k-1", normalized.KnowledgeID)
	assert.Equal(t, 100.0, normalized.Percent)
	assert.Equal(t, 0, normalized.ChunkCount)

	_, err = NormalizeReadingProgress(ReadingProgressInput{Percent: 10})
	require.Error(t, err, "knowledge_id is required")
}

// TestBuildReadingProgressListDedupesAndOrders covers the continue-reading list.
func TestBuildReadingProgressListDedupesAndOrders(t *testing.T) {
	now := time.Now()
	items := BuildReadingProgressList([]*ReadingProgress{
		{KnowledgeID: "old", Percent: 20, UpdatedAt: now.Add(-time.Hour)},
		{KnowledgeID: "new", Percent: 70, UpdatedAt: now},
		{KnowledgeID: "old", Percent: 30, UpdatedAt: now.Add(-2 * time.Hour)},
	})
	require.Len(t, items, 2)
	assert.Equal(t, "new", items[0].KnowledgeID)
	assert.Equal(t, "old", items[1].KnowledgeID)
	assert.Equal(t, 20.0, items[1].Percent, "the newest row per document wins")
}
