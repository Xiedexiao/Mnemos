package types

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ============================================================================
// 阅读页目录与阅读进度（KnowAxis V3.0 §15.2 阅读页）。
//
// §15.2 要求阅读页有"自动目录、阅读进度、沉浸模式"。平台此前只渲染正文：
// 长文档没有可跳转的目录，读到哪也记不住（换设备即丢）。
//
// 两个决定：
//  1. 目录从**分块结果**推导，而不是重新解析原文件：分块已经是"按标题与
//     段落切开的正文"，再解析一次既慢又会与检索看到的内容不一致。
//  2. 阅读进度按"位置锚点"记录（chunk id 或百分比），不按时间：时间无法
//     换算成"读到哪"，而锚点可以直接定位并跨设备续读。
// ============================================================================

// TOCEntry is one heading of the reading-page table of contents.
type TOCEntry struct {
	// Level is the heading depth (1 = top-level).
	Level int `json:"level"`
	// Title is the heading text.
	Title string `json:"title"`
	// ChunkID is the chunk the heading starts; the reader jumps here.
	ChunkID string `json:"chunk_id"`
	// ChunkIndex is the chunk's ordinal, for ordering and progress math.
	ChunkIndex int `json:"chunk_index"`
	// StartAt is the character offset inside the source document, when known.
	StartAt int `json:"start_at,omitempty"`
}

// ReadingTOC is the reading-page outline.
type ReadingTOC struct {
	// KnowledgeID the outline belongs to.
	KnowledgeID string `json:"knowledge_id"`
	// Entries are the headings in document order.
	Entries []TOCEntry `json:"entries"`
	// ChunkCount is how many chunks the document has (for progress display).
	ChunkCount int `json:"chunk_count"`
	// HasTOC reports whether any heading was found. A flat document gets
	// `false` and the reader renders no outline rather than a one-line one.
	HasTOC bool `json:"has_toc"`
}

// markdownHeadingPattern matches a Markdown ATX heading.
var markdownHeadingPattern = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*#*$`)

// chineseHeadingPattern matches 第X章 / 一、 (二) style headings.
//
// 中文文档大量使用这两种编号而不是 Markdown 标题；不认它们的话，一份标准
// 或制度文件的目录会是空的。
var chineseHeadingPattern = regexp.MustCompile(
	`^\s*(第[一二三四五六七八九十百零〇\d]+[章节条部分]|[一二三四五六七八九十]+、)\s*\S`)

// BuildReadingTOC derives the outline from the document's chunks.
//
// 优先级：Markdown 标题优先；某一块没有 Markdown 标题时再看是否是中文编号
// 标题。两者都不命中就跳过——把每段都当标题的"目录"没有导航价值。
func BuildReadingTOC(knowledgeID string, chunks []string) *ReadingTOC {
	toc := &ReadingTOC{
		KnowledgeID: knowledgeID,
		Entries:     []TOCEntry{},
		ChunkCount:  len(chunks),
	}
	for index, raw := range chunks {
		title, level, ok := extractHeading(raw)
		if !ok {
			continue
		}
		toc.Entries = append(toc.Entries, TOCEntry{
			Level:      level,
			Title:      title,
			ChunkIndex: index,
		})
	}
	toc.HasTOC = len(toc.Entries) > 0
	return toc
}

// extractHeading returns the heading text and depth of one chunk, if any.
//
// 只看块的首行：标题后面紧跟正文时，正文里的 `#` 是内容而不是结构。
func extractHeading(chunk string) (string, int, bool) {
	firstLine := chunk
	if index := strings.IndexAny(chunk, "\r\n"); index >= 0 {
		firstLine = chunk[:index]
	}
	firstLine = strings.TrimSpace(firstLine)
	if firstLine == "" {
		return "", 0, false
	}
	if match := markdownHeadingPattern.FindStringSubmatch(firstLine); match != nil {
		return strings.TrimSpace(match[2]), len(match[1]), true
	}
	if chineseHeadingPattern.MatchString(firstLine) {
		// 中文编号结构上没有层级信息，统一按二级处理：一级留给文档标题。
		return firstLine, 2, true
	}
	return "", 0, false
}

// ReadingProgress is one user's position in one document.
type ReadingProgress struct {
	ID          string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID    uint64 `json:"tenant_id" gorm:"not null;uniqueIndex:uq_reading_progress,priority:1"`
	UserID      string `json:"user_id" gorm:"type:varchar(36);not null;uniqueIndex:uq_reading_progress,priority:2"`
	KnowledgeID string `json:"knowledge_id" gorm:"type:varchar(36);not null;uniqueIndex:uq_reading_progress,priority:3"`
	// ChunkID is the anchor the reader stopped at ('' when only a percentage
	// is known).
	ChunkID string `json:"chunk_id" gorm:"type:varchar(36);not null;default:''"`
	// ChunkIndex is the anchor's ordinal.
	ChunkIndex int `json:"chunk_index" gorm:"not null;default:0"`
	// Percent is 0-100; it only ever moves forward in one sitting but can be
	// reset by explicit "start over".
	Percent float64 `json:"percent" gorm:"not null;default:0"`
	// ChunkCount is the document's chunk count when the position was saved, so
	// a later edit can be detected (the percentage then needs rescaling).
	ChunkCount int `json:"chunk_count" gorm:"not null;default:0"`
	// Completed marks the reader reaching the end.
	Completed bool `json:"completed" gorm:"not null;default:false"`
	// UpdatedAt is the last position update.
	UpdatedAt time.Time `json:"updated_at"`
	CreatedAt time.Time `json:"created_at"`
}

// TableName pins the GORM table name.
func (ReadingProgress) TableName() string { return "reading_progress" }

// ReadingProgressInput is the save payload.
type ReadingProgressInput struct {
	// KnowledgeID the position belongs to.
	KnowledgeID string `json:"knowledge_id"`
	// ChunkID / ChunkIndex anchor the position.
	ChunkID string `json:"chunk_id"`
	// Percent is the approximate position (0-100).
	Percent float64 `json:"percent"`
	// ChunkCount lets the service detect that the document changed.
	ChunkCount int `json:"chunk_count"`
}

// NormalizeReadingProgress clamps and validates a save payload.
func NormalizeReadingProgress(input ReadingProgressInput) (ReadingProgressInput, error) {
	input.KnowledgeID = strings.TrimSpace(input.KnowledgeID)
	if input.KnowledgeID == "" {
		return input, fmt.Errorf("knowledge_id is required")
	}
	input.ChunkID = strings.TrimSpace(input.ChunkID)
	if input.Percent < 0 {
		input.Percent = 0
	}
	if input.Percent > 100 {
		input.Percent = 100
	}
	if input.ChunkCount < 0 {
		input.ChunkCount = 0
	}
	return input, nil
}

// ReadingProgressList is the "continue reading" projection.
type ReadingProgressList struct {
	Items []ReadingProgressItem `json:"items"`
}

// ReadingProgressItem is one entry of "continue reading".
type ReadingProgressItem struct {
	KnowledgeID     string    `json:"knowledge_id"`
	KnowledgeBaseID string    `json:"knowledge_base_id,omitempty"`
	Title           string    `json:"title,omitempty"`
	Percent         float64   `json:"percent"`
	Completed       bool      `json:"completed"`
	UpdatedAt       time.Time `json:"updated_at"`
	// Visible is false when the document can no longer be opened; the row is
	// kept so the reader understands why it is missing from the list.
	Visible bool `json:"visible"`
}

// BuildReadingProgressList sorts progress newest-first and dedupes.
func BuildReadingProgressList(rows []*ReadingProgress) []ReadingProgressItem {
	items := make([]ReadingProgressItem, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	sorted := make([]*ReadingProgress, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		sorted = append(sorted, row)
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].UpdatedAt.After(sorted[j].UpdatedAt)
	})
	for _, row := range sorted {
		if _, ok := seen[row.KnowledgeID]; ok {
			continue
		}
		seen[row.KnowledgeID] = struct{}{}
		items = append(items, ReadingProgressItem{
			KnowledgeID: row.KnowledgeID,
			Percent:     row.Percent,
			Completed:   row.Completed,
			UpdatedAt:   row.UpdatedAt,
			Visible:     true,
		})
	}
	return items
}
