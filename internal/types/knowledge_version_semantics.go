package types

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// ============================================================================
// 版本差异、回滚与语义化版本号（KnowAxis V3.0 §7.4 版本与生效期）。
//
// §7.4 要求"版本号：主版本.次版本.修订号；文字修订 → 修订号；内容/步骤调整 →
// 次版本；参数/结构/替代 → 主版本"，以及"历史版本永久可查"与"支持 as-of 查询"。
//
// 平台此前只有单调递增的 current_version（1、2、3…）与快照：读者不知道
// "这次改动是大是小"，也无法回退到某个历史版本。
//
// 三个决定：
//  1. 语义化版本是**存下来的**，不是从递增号算出来的：只有提交改动的人知道
//     这次是措辞修正还是参数变更，让系统猜必然猜错。
//  2. 差异按段落（行）算，不给字符级 diff：读者要回答的是"哪一段变了、
//     变了什么"，字符级 diff 在中文上会把一整行标红。
//  3. 回滚不是"删掉新版本"，而是**以旧版内容新建一个版本**：历史必须永久
//     可查（§7.4），把 v5 删掉回退到 v4 会让"v5 存在过"这件事消失。
// ============================================================================

// SemanticVersion is the §7.4 主.次.修订 version.
type SemanticVersion struct {
	Major int `json:"major"`
	Minor int `json:"minor"`
	Patch int `json:"patch"`
}

// String renders "major.minor.patch".
func (v SemanticVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// Valid reports whether every component is non-negative.
func (v SemanticVersion) Valid() bool {
	return v.Major >= 0 && v.Minor >= 0 && v.Patch >= 0
}

// Compare returns -1, 0 or 1.
func (v SemanticVersion) Compare(other SemanticVersion) int {
	if v.Major != other.Major {
		return compareInt(v.Major, other.Major)
	}
	if v.Minor != other.Minor {
		return compareInt(v.Minor, other.Minor)
	}
	return compareInt(v.Patch, other.Patch)
}

// compareInt returns the sign of a-b.
func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// ParseSemanticVersion parses "1.2.3"; a bare "2" becomes "2.0.0" so legacy
// rows that only carry an integer still resolve.
func ParseSemanticVersion(raw string) (SemanticVersion, error) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(raw), "v")
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return SemanticVersion{}, fmt.Errorf("version is required")
	}
	parts := strings.Split(trimmed, ".")
	if len(parts) > 3 {
		return SemanticVersion{}, fmt.Errorf("version %q has too many components", raw)
	}
	values := make([]int, 3)
	for index, part := range parts {
		number, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || number < 0 {
			return SemanticVersion{}, fmt.Errorf("version %q is not numeric", raw)
		}
		values[index] = number
	}
	return SemanticVersion{Major: values[0], Minor: values[1], Patch: values[2]}, nil
}

// Bump returns the next version for the given change size.
func (v SemanticVersion) Bump(size VersionBump) SemanticVersion {
	switch size {
	case VersionBumpMajor:
		return SemanticVersion{Major: v.Major + 1}
	case VersionBumpMinor:
		return SemanticVersion{Major: v.Major, Minor: v.Minor + 1}
	default:
		return SemanticVersion{Major: v.Major, Minor: v.Minor, Patch: v.Patch + 1}
	}
}

// VersionBump is how large a change is (§7.4 版本变更级别).
type VersionBump string

const (
	// VersionBumpPatch 文字修订。
	VersionBumpPatch VersionBump = "patch"
	// VersionBumpMinor 内容/步骤调整。
	VersionBumpMinor VersionBump = "minor"
	// VersionBumpMajor 参数/结构/替代。
	VersionBumpMajor VersionBump = "major"
)

// Valid reports whether the bump size is known.
func (b VersionBump) Valid() bool {
	switch b {
	case VersionBumpPatch, VersionBumpMinor, VersionBumpMajor:
		return true
	default:
		return false
	}
}

// ClassifyVersionBump infers the bump size from the change type when the
// author did not name one.
//
// 这是**建议值**，不是判定：作者可以在提交时覆盖它。推断只用于降低填表
// 负担，猜错时以作者的选择为准（§7.4 三级变更强度）。
func ClassifyVersionBump(changeType ChangeType) VersionBump {
	switch changeType {
	case ChangeTypeParamChange, ChangeTypeReplacement, ChangeTypeRestructure, ChangeTypeDelist:
		return VersionBumpMajor
	case ChangeTypeContentRevision:
		return VersionBumpMinor
	default:
		return VersionBumpPatch
	}
}

// ============================================================================
// 段落级 diff。
// ============================================================================

// DiffLineKind is what happened to one paragraph.
type DiffLineKind string

const (
	// DiffLineUnchanged 段落未变。
	DiffLineUnchanged DiffLineKind = "unchanged"
	// DiffLineAdded 新增段落。
	DiffLineAdded DiffLineKind = "added"
	// DiffLineRemoved 删除段落。
	DiffLineRemoved DiffLineKind = "removed"
)

// DiffLine is one paragraph of the comparison.
type DiffLine struct {
	Kind DiffLineKind `json:"kind"`
	// Text is the paragraph content (trailing spaces trimmed).
	Text string `json:"text"`
	// LeftLine / RightLine are the 1-based paragraph numbers in each side, 0
	// when the paragraph does not exist on that side.
	LeftLine  int `json:"left_line,omitempty"`
	RightLine int `json:"right_line,omitempty"`
}

// VersionDiff is the comparison of two versions.
type VersionDiff struct {
	// Left / Right are the compared labels.
	Left  string `json:"left"`
	Right string `json:"right"`
	// Lines are the paragraph-level comparison in document order.
	Lines []DiffLine `json:"lines"`
	// Added / Removed / Unchanged count the paragraphs.
	Added     int `json:"added"`
	Removed   int `json:"removed"`
	Unchanged int `json:"unchanged"`
	// Changed reports whether anything differs at all.
	Changed bool `json:"changed"`
	// ContentHashChanged compares the stored hashes, which is authoritative
	// even when the rendered text happens to look the same.
	ContentHashChanged bool `json:"content_hash_changed"`
	// Truncated reports that the documents were too large for paragraph-level
	// comparison and the result is a whole-document replacement.
	Truncated bool `json:"truncated"`
}

// diffMaxParagraphs bounds one comparison.
const diffMaxParagraphs = 5000

// DiffParagraphs computes a paragraph-level diff with LCS.
//
// 用 LCS 而不是逐行比对：插入一段会让后面所有行"错位"，逐行比对会把它
// 后面整篇标成改动，读者无法定位真正变了什么。O(n*m) 在这里可以接受——
// 单篇知识最多几百段，且只在人主动查看差异时计算。
//
// 超过 diffMaxParagraphs 的对直接退化为"整体替换"：与其在超大文档上算
// 几分钟，不如诚实说明这次无法逐段比对。
func DiffParagraphs(leftText, rightText string) (lines []DiffLine, added, removed, unchanged int, truncated bool) {
	left := splitParagraphs(leftText)
	right := splitParagraphs(rightText)
	if len(left) > diffMaxParagraphs || len(right) > diffMaxParagraphs {
		out := make([]DiffLine, 0, len(left)+len(right))
		for index, text := range left {
			out = append(out, DiffLine{Kind: DiffLineRemoved, Text: text, LeftLine: index + 1})
		}
		for index, text := range right {
			out = append(out, DiffLine{Kind: DiffLineAdded, Text: text, RightLine: index + 1})
		}
		return out, len(right), len(left), 0, true
	}

	// LCS 表：lcs[i][j] = left[i:] 与 right[j:] 的最长公共子序列长度。
	lcs := make([][]int, len(left)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(right)+1)
	}
	for i := len(left) - 1; i >= 0; i-- {
		for j := len(right) - 1; j >= 0; j-- {
			if left[i] == right[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
				continue
			}
			if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	lines = make([]DiffLine, 0, len(left)+len(right))
	i, j := 0, 0
	for i < len(left) && j < len(right) {
		switch {
		case left[i] == right[j]:
			lines = append(lines, DiffLine{
				Kind: DiffLineUnchanged, Text: left[i], LeftLine: i + 1, RightLine: j + 1,
			})
			unchanged++
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			lines = append(lines, DiffLine{Kind: DiffLineRemoved, Text: left[i], LeftLine: i + 1})
			removed++
			i++
		default:
			lines = append(lines, DiffLine{Kind: DiffLineAdded, Text: right[j], RightLine: j + 1})
			added++
			j++
		}
	}
	for ; i < len(left); i++ {
		lines = append(lines, DiffLine{Kind: DiffLineRemoved, Text: left[i], LeftLine: i + 1})
		removed++
	}
	for ; j < len(right); j++ {
		lines = append(lines, DiffLine{Kind: DiffLineAdded, Text: right[j], RightLine: j + 1})
		added++
	}
	return lines, added, removed, unchanged, false
}

// splitParagraphs splits text into trimmed non-empty paragraphs.
//
// 空行只作分隔符，不进 diff 列表：否则每个空行都会成为一条"未变"记录，
// 差异视图难以阅读。
func splitParagraphs(text string) []string {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	raw := strings.Split(normalized, "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		trimmed := strings.TrimRightFunc(line, unicode.IsSpace)
		if strings.TrimSpace(trimmed) == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

// ============================================================================
// as-of 查询与回滚（§7.4 / §7.8）。
// ============================================================================

// AsOfVersion is the resolved historical state at one instant.
type AsOfVersion struct {
	// KnowledgeID inspected.
	KnowledgeID string `json:"knowledge_id"`
	// At is the requested instant.
	At time.Time `json:"at"`
	// Version is the version that was in force at that instant; nil when the
	// document did not exist yet.
	Version *KnowledgeVersion `json:"version,omitempty"`
	// Title is the snapshot title for convenience.
	Title string `json:"title,omitempty"`
	// EffectiveFrom is when that version started being in force.
	EffectiveFrom *time.Time `json:"effective_from,omitempty"`
	// Note explains an empty result.
	Note string `json:"note,omitempty"`
}

// VersionRollbackInput asks to restore a historical version.
type VersionRollbackInput struct {
	// KnowledgeID to roll back.
	KnowledgeID string `json:"knowledge_id"`
	// TargetVersion is the semantic version to restore.
	TargetVersion string `json:"target_version"`
	// ChangeNote explains why.
	ChangeNote string `json:"change_note"`
}

// VersionRollbackResult reports what a rollback produced.
type VersionRollbackResult struct {
	// NewVersion is the version created from the restored content.
	NewVersion *KnowledgeVersion `json:"new_version"`
	// RestoredFrom is the version whose content was copied.
	RestoredFrom string `json:"restored_from"`
	// PreviousVersion is what was in force before.
	PreviousVersion string `json:"previous_version"`
}

// ValidateVersionRollbackInput guards the request shape.
//
// 回滚必须写清原因：它是少数几个会**撤销一次已批准内容**的动作，审计里
// 没有理由就无法解释为什么旧结论重新生效。
func ValidateVersionRollbackInput(input VersionRollbackInput) error {
	if strings.TrimSpace(input.KnowledgeID) == "" {
		return fmt.Errorf("knowledge_id is required")
	}
	if strings.TrimSpace(input.TargetVersion) == "" {
		return fmt.Errorf("target_version is required")
	}
	if strings.TrimSpace(input.ChangeNote) == "" {
		return fmt.Errorf("change_note is required: a rollback must explain why")
	}
	return nil
}
