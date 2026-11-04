package types

import (
	"fmt"
	"path"
	"strings"
	"time"
)

// ============================================================================
// 批量迁移导入（KnowAxis V3.0 §16.3 "导入导出：批量导入、导出审批、迁移工具"）。
//
// 平台此前只能一篇一篇上传：把一套 Confluence 空间或一个文件服务器搬进来，
// 意味着人工点击上千次，而且无法表达"这个目录对应哪个目录、这篇是内部还是
// 秘密、保留原有版本号"。
//
// 这里定义迁移清单（manifest）与逐条结果。清单是**声明式**的：调用方描述
// 目标状态（路径、标题、密级、标签、来源版本），服务端负责逐步应用并如实
// 报告每条的结果——包括被跳过与失败的条目，而不是整批回滚或整批静默。
//
// 与普通上传的区别：
//   - 支持按源路径做目录映射（源系统树 → 空间目录树）；
//   - 保留源系统版本号与更新时间（迁移不应把所有内容刷成"今天新增"）；
//   - 逐条独立成败：一条坏文件不该让其余 999 条全部失败。
// ============================================================================

// MigrationItem is one entry of a bulk import manifest.
type MigrationItem struct {
	// ExternalID is the source system's identifier, used for idempotent
	// re-runs: an existing row with the same external id is updated in place.
	ExternalID string `json:"external_id"`
	// SourcePath is the path in the source system ("sop/e45/changeover.md").
	SourcePath string `json:"source_path"`
	// Title overrides the derived title (defaults to the file name).
	Title string `json:"title,omitempty"`
	// FolderPath is the explicit target directory. Empty means "derive from
	// SourcePath through the manifest's folder mapping".
	FolderPath string `json:"folder_path,omitempty"`
	// Classification is the target classification; empty keeps the space
	// default.
	Classification string `json:"classification,omitempty"`
	// Tags are the tag names to attach.
	Tags []string `json:"tags,omitempty"`
	// SourceVersion is the source system's version label (e.g. "3.2").
	// Preserved verbatim in the import result and the knowledge metadata.
	SourceVersion string `json:"source_version,omitempty"`
	// SourceUpdatedAt is the source system's last-modified timestamp (RFC3339).
	//
	// 保留它，迁移后的知识才不会被全部显示成"今天更新"——否则整个空间的
	// 新鲜度信号在导入那一刻被抹平。
	SourceUpdatedAt string `json:"source_updated_at,omitempty"`
	// ContentHash lets a re-run skip an item whose content did not change.
	ContentHash string `json:"content_hash,omitempty"`
}

// MigrationManifest is one bulk import request.
type MigrationManifest struct {
	// KnowledgeBaseID is the destination space.
	KnowledgeBaseID string `json:"knowledge_base_id"`
	// FolderMappings rewrite source path prefixes onto target folders.
	// The longest matching prefix wins.
	FolderMappings []FolderMapping `json:"folder_mappings,omitempty"`
	// DefaultClassification applies when an item names none.
	DefaultClassification string `json:"default_classification,omitempty"`
	// DryRun resolves and validates the manifest without writing anything.
	DryRun bool `json:"dry_run"`
	// Items are the entries to import.
	Items []MigrationItem `json:"items"`
}

// FolderMapping rewrites one source prefix onto a target folder.
type FolderMapping struct {
	// From is the source path prefix ("sop/").
	From string `json:"from"`
	// To is the target folder ("工艺/SOP").
	To string `json:"to"`
}

// MigrationItemStatus is the per-item outcome.
type MigrationItemStatus string

const (
	// MigrationItemCreated is a new entry.
	MigrationItemCreated MigrationItemStatus = "created"
	// MigrationItemUpdated is an existing entry whose content changed.
	MigrationItemUpdated MigrationItemStatus = "updated"
	// MigrationItemSkipped is unchanged (same content hash) or invalid.
	MigrationItemSkipped MigrationItemStatus = "skipped"
	// MigrationItemFailed could not be applied.
	MigrationItemFailed MigrationItemStatus = "failed"
)

// Valid reports whether the status is known.
func (s MigrationItemStatus) Valid() bool {
	switch s {
	case MigrationItemCreated, MigrationItemUpdated, MigrationItemSkipped, MigrationItemFailed:
		return true
	default:
		return false
	}
}

// MigrationItemResult is one item's outcome.
type MigrationItemResult struct {
	// ExternalID / SourcePath echo the manifest entry.
	ExternalID string `json:"external_id"`
	SourcePath string `json:"source_path"`
	// Status is the outcome.
	Status MigrationItemStatus `json:"status"`
	// KnowledgeID is the written row (empty for skipped/failed).
	KnowledgeID string `json:"knowledge_id,omitempty"`
	// FolderPath is the resolved target directory.
	FolderPath string `json:"folder_path,omitempty"`
	// Reason explains a skip or failure.
	Reason string `json:"reason,omitempty"`
}

// MigrationReport is the whole run's outcome.
type MigrationReport struct {
	// KnowledgeBaseID the import targeted.
	KnowledgeBaseID string `json:"knowledge_base_id"`
	// DryRun echoes the request.
	DryRun bool `json:"dry_run"`
	// Results are the per-item outcomes in manifest order.
	Results []MigrationItemResult `json:"results"`
	// Counts summarise the run.
	Created int `json:"created"`
	Updated int `json:"updated"`
	Skipped int `json:"skipped"`
	Failed  int `json:"failed"`
	// Total is len(Items).
	Total int `json:"total"`
}

// NormalizeMigrationManifest validates the manifest shape.
//
// 全部条目在写入前先校验一遍：迁移通常是一次性的大批量操作，让它在第 800
// 条才发现路径非法，等于让人重来一遍。校验失败直接拒绝整份清单。
func NormalizeMigrationManifest(manifest MigrationManifest) (MigrationManifest, error) {
	if strings.TrimSpace(manifest.KnowledgeBaseID) == "" {
		return manifest, fmt.Errorf("knowledge_base_id is required")
	}
	if len(manifest.Items) == 0 {
		return manifest, fmt.Errorf("at least one item is required")
	}
	if len(manifest.Items) > MaxMigrationItems {
		return manifest, fmt.Errorf("at most %d items are allowed per manifest", MaxMigrationItems)
	}
	// 用原值直接校验，不走 Parse（它会把未知值静默降级成 internal）：
	// 迁移清单里的拼写错误必须被指出，而不是把一批秘密文件导成内部级。
	if manifest.DefaultClassification != "" &&
		!KnowledgeClassification(manifest.DefaultClassification).Valid() {
		return manifest, fmt.Errorf("unknown default classification %q", manifest.DefaultClassification)
	}
	for index, mapping := range manifest.FolderMappings {
		mapping.From = NormalizeKnowledgeFolderPath(mapping.From)
		mapping.To = NormalizeKnowledgeFolderPath(mapping.To)
		if mapping.From == "" {
			return manifest, fmt.Errorf("folder_mappings[%d]: from is required", index)
		}
		manifest.FolderMappings[index] = mapping
	}
	// 长前缀优先：否则 "sop/" 会抢走 "sop/e45/" 的条目。
	sortFolderMappingsBySpecificity(manifest.FolderMappings)

	seen := make(map[string]struct{}, len(manifest.Items))
	for index := range manifest.Items {
		item := &manifest.Items[index]
		item.SourcePath = NormalizeKnowledgeFolderPath(item.SourcePath)
		if item.SourcePath == "" && item.ExternalID == "" {
			return manifest, fmt.Errorf("items[%d]: source_path or external_id is required", index)
		}
		if item.ExternalID != "" {
			if _, dup := seen[item.ExternalID]; dup {
				return manifest, fmt.Errorf("items[%d]: duplicate external_id %q", index, item.ExternalID)
			}
			seen[item.ExternalID] = struct{}{}
		}
		if item.Classification != "" &&
			!KnowledgeClassification(item.Classification).Valid() {
			return manifest, fmt.Errorf("items[%d]: unknown classification %q", index, item.Classification)
		}
		if strings.TrimSpace(item.SourceUpdatedAt) != "" {
			if _, ok := parseMigrationTime(item.SourceUpdatedAt); !ok {
				return manifest, fmt.Errorf("items[%d]: source_updated_at must be RFC3339", index)
			}
		}
		item.Tags = NormalizeTagNames(item.Tags)
		if item.FolderPath != "" {
			item.FolderPath = NormalizeKnowledgeFolderPath(item.FolderPath)
		}
	}
	return manifest, nil
}

// MaxMigrationItems bounds one manifest.
const MaxMigrationItems = 5000

// ResolveMigrationFolder maps a source path onto its target folder.
//
// 映射按"最长匹配前缀"：一个空间里可能既有 sop/ 的整体映射，又有
// sop/e45/ 的特例；按短的先匹配会把特例吃掉。
func ResolveMigrationFolder(sourcePath string, mappings []FolderMapping) string {
	sourcePath = NormalizeKnowledgeFolderPath(sourcePath)
	if sourcePath == "" {
		return ""
	}
	// 输入既可能是文件路径也可能是目录路径。判断依据是**最后一段有没有
	// 扩展名**，而不是"永远剥掉最后一段"：清单里的 "sop/通用" 是目录，
	// 剥掉会把它整段丢掉；而 "sop/换型.md" 是文件，不剥会把文件名带进目录名。
	sourceDir := sourcePath
	lastSegment := sourcePath
	if index := strings.LastIndexByte(sourcePath, '/'); index >= 0 {
		lastSegment = sourcePath[index+1:]
	}
	if strings.Contains(lastSegment, ".") {
		if index := strings.LastIndexByte(sourcePath, '/'); index > 0 {
			sourceDir = sourcePath[:index]
		} else {
			sourceDir = ""
		}
	}

	best := ""
	bestLen := -1
	for _, mapping := range mappings {
		from := NormalizeKnowledgeFolderPath(mapping.From)
		if from == "" {
			continue
		}
		if sourceDir != from && !strings.HasPrefix(sourceDir, from+"/") {
			continue
		}
		if len(from) <= bestLen {
			continue
		}
		suffix := strings.TrimPrefix(strings.TrimPrefix(sourceDir, from), "/")
		to := NormalizeKnowledgeFolderPath(mapping.To)
		switch {
		case suffix == "":
			best = to
		case to == "":
			best = suffix
		default:
			best = to + "/" + suffix
		}
		bestLen = len(from)
	}
	if bestLen >= 0 {
		return best
	}
	return sourceDir
}

// MigrationItemTitle derives the display title for one item.
func MigrationItemTitle(item MigrationItem) string {
	if title := strings.TrimSpace(item.Title); title != "" {
		return title
	}
	sourcePath := NormalizeKnowledgeFolderPath(item.SourcePath)
	name := path.Base(sourcePath)
	if name == "." || name == "/" {
		return strings.TrimSpace(item.ExternalID)
	}
	// 去掉扩展名：知识标题不应该带 ".md"。
	if index := strings.LastIndexByte(name, '.'); index > 0 {
		name = name[:index]
	}
	return name
}

// sortFolderMappingsBySpecificity orders mappings longest-first.
func sortFolderMappingsBySpecificity(mappings []FolderMapping) {
	for i := 1; i < len(mappings); i++ {
		for j := i; j > 0 && len(mappings[j].From) > len(mappings[j-1].From); j-- {
			mappings[j], mappings[j-1] = mappings[j-1], mappings[j]
		}
	}
}

// parseMigrationTime accepts the timestamp forms a migration manifest carries.
//
// 接受 RFC3339 与不带时区的 "YYYY-MM-DD HH:MM:SS"：导出工具（Confluence、
// 文件系统、数据库导出）给出的格式不统一，要求一律 RFC3339 会把大部分
// 真实清单挡在门外。
func parseMigrationTime(raw string) (time.Time, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{
		time.RFC3339, time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02",
	} {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

// MigrationSourceUpdatedAt parses the item's source timestamp.
func MigrationSourceUpdatedAt(item MigrationItem) (time.Time, bool) {
	return parseMigrationTime(item.SourceUpdatedAt)
}
