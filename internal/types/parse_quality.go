package types

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 解析质量评分（KnowAxis V3.0 §3.5 / §9.4）。
//
// 解析完成（或用户手动触发重算）后给每条知识条目打一个 0-100 的解析质量分：
// 六个维度各自 0-100，按 §3.5 权重表加权求和。分数落 parse_quality_scores
// （迁移 000156），每个 knowledge 一行当前快照——重算即覆盖。
//
// 取分原则（§3.5 末句）：低于阈值的文档"可搜但不可作高置信来源"。本文件
// 只定义打分口径与落库行；检索侧是否降权由消费方读 BelowThreshold 决定。
//
// 权重表（§3.5）：
//
//	文本完整度 25 / 结构完整度 20 / 表格可用性 15 / OCR 质量 10 /
//	元数据完整度 15 / 可引用性 15          —— 合计 100
// ============================================================================

// Parse quality dimension weights (KnowAxis V3.0 §3.5). They sum to 100 so
// the weighted sum lands directly on the 0-100 scale the grade bands use.
const (
	// ParseQualityWeightTextCompleteness 文本完整度 25：是否提取出主要正文。
	ParseQualityWeightTextCompleteness = 25
	// ParseQualityWeightStructureCompleteness 结构完整度 20：标题、段落、表格
	// 是否保留。
	ParseQualityWeightStructureCompleteness = 20
	// ParseQualityWeightTableUsability 表格可用性 15：表头和单元格是否可理解。
	ParseQualityWeightTableUsability = 15
	// ParseQualityWeightOCRQuality OCR 质量 10：图片文字识别可信度。
	ParseQualityWeightOCRQuality = 10
	// ParseQualityWeightMetadataCompleteness 元数据完整度 15：标题/描述/自定义
	// 元数据/文件哈希的完整程度（v1 取 knowledges 表可得信号）。
	ParseQualityWeightMetadataCompleteness = 15
	// ParseQualityWeightCitable 可引用性 15：能否定位到页码、段落或表格。
	ParseQualityWeightCitable = 15
)

// 档位与队列阈值（§3.5）：≥85 good / ≥60 fair / <60 poor，<60 进解析
// 异常队列。改 ParseQualityThreshold 等于改 below_threshold 的落库口径，
// 历史行需整体重算。
const (
	// ParseQualityGoodThreshold good 档下限。
	ParseQualityGoodThreshold = 85
	// ParseQualityFairThreshold fair 档下限（同时是解析异常队列的分界）。
	ParseQualityFairThreshold = 60
	// ParseQualityThreshold 是"解析异常"的分界：total_score < 60 的文档进
	// 解析异常队列，不可作高置信来源。
	ParseQualityThreshold = ParseQualityFairThreshold
)

// ParseQualityGradeLevel 是解析质量档位（good/fair/poor/unknown）。
type ParseQualityGradeLevel string

const (
	// ParseQualityGradeGood 好（≥85）：解析完整，可作高置信来源。
	ParseQualityGradeGood ParseQualityGradeLevel = "good"
	// ParseQualityGradeFair 中（≥60 且 <85）：可用，引用时宜提示来源质量。
	ParseQualityGradeFair ParseQualityGradeLevel = "fair"
	// ParseQualityGradePoor 差（<60）：进解析异常队列，可搜但不可作高置信来源。
	ParseQualityGradePoor ParseQualityGradeLevel = "poor"
	// ParseQualityGradeUnknown 未评分（迁移默认值；尚无评分行时的占位档）。
	ParseQualityGradeUnknown ParseQualityGradeLevel = "unknown"
)

// ParseQualityGrade 按总分给出档位（§3.5）：≥85 good / ≥60 fair / <60 poor。
// 纯函数：不读时钟、不落库、不依赖全局状态。
func ParseQualityGrade(total int) ParseQualityGradeLevel {
	switch {
	case total >= ParseQualityGoodThreshold:
		return ParseQualityGradeGood
	case total >= ParseQualityFairThreshold:
		return ParseQualityGradeFair
	default:
		return ParseQualityGradePoor
	}
}

// Valid 报告档位是否在白名单内。
func (g ParseQualityGradeLevel) Valid() bool {
	switch g {
	case ParseQualityGradeGood, ParseQualityGradeFair, ParseQualityGradePoor, ParseQualityGradeUnknown:
		return true
	default:
		return false
	}
}

// String 渲染档位用于日志。
func (g ParseQualityGradeLevel) String() string { return string(g) }

// ParseQualityDimension 是六个维度的枚举名（详情 JSONB 与 API 输出的键）。
type ParseQualityDimension string

const (
	// ParseQualityDimensionTextCompleteness 文本完整度。
	ParseQualityDimensionTextCompleteness ParseQualityDimension = "text_completeness"
	// ParseQualityDimensionStructureCompleteness 结构完整度。
	ParseQualityDimensionStructureCompleteness ParseQualityDimension = "structure_completeness"
	// ParseQualityDimensionTableUsability 表格可用性。
	ParseQualityDimensionTableUsability ParseQualityDimension = "table_usability"
	// ParseQualityDimensionOCRQuality OCR 质量。
	ParseQualityDimensionOCRQuality ParseQualityDimension = "ocr_quality"
	// ParseQualityDimensionMetadataCompleteness 元数据完整度。
	ParseQualityDimensionMetadataCompleteness ParseQualityDimension = "metadata_completeness"
	// ParseQualityDimensionCitable 可引用性。
	ParseQualityDimensionCitable ParseQualityDimension = "citable"
)

// ParseQualityDimensionWeight 是权重表的一行。
type ParseQualityDimensionWeight struct {
	Dimension ParseQualityDimension `json:"dimension"`
	Weight    int                   `json:"weight"`
}

// ParseQualityWeights 是 §3.5 权重表的有序快照（渲染与测试共用，避免下游
// 各自拼一份常量列表而漂移）。
var ParseQualityWeights = []ParseQualityDimensionWeight{
	{Dimension: ParseQualityDimensionTextCompleteness, Weight: ParseQualityWeightTextCompleteness},
	{Dimension: ParseQualityDimensionStructureCompleteness, Weight: ParseQualityWeightStructureCompleteness},
	{Dimension: ParseQualityDimensionTableUsability, Weight: ParseQualityWeightTableUsability},
	{Dimension: ParseQualityDimensionOCRQuality, Weight: ParseQualityWeightOCRQuality},
	{Dimension: ParseQualityDimensionMetadataCompleteness, Weight: ParseQualityWeightMetadataCompleteness},
	{Dimension: ParseQualityDimensionCitable, Weight: ParseQualityWeightCitable},
}

// ParseQualityDimensionScore 是"维度 → 权重 + 得分"的一行，用于响应与
// 详情里解释总分的构成。
type ParseQualityDimensionScore struct {
	Dimension ParseQualityDimension `json:"dimension"`
	Weight    int                   `json:"weight"`
	// Score 该维度得分（0-100）。
	Score int `json:"score"`
	// Weighted 该维度对总分的贡献（Score * Weight / 100 四舍五入）。
	Weighted int `json:"weighted"`
}

// ParseQualityScore 是 parse_quality_scores 的一行（KnowAxis V3.0 §3.5）。
// 六维度各 0-100；TotalScore 是加权和（0-100）；Grade 由 TotalScore 派生。
type ParseQualityScore struct {
	// ID of the row.
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// TenantID owning the row.
	TenantID uint64 `json:"tenant_id" gorm:"type:integer;not null"`
	// KnowledgeID is the scored knowledge entry (row identity, UNIQUE).
	KnowledgeID string `json:"knowledge_id" gorm:"type:varchar(36);not null;uniqueIndex:uq_parse_quality_scores_knowledge"`
	// TextCompleteness 文本完整度 0-100（权重 25）。
	TextCompleteness int `json:"text_completeness" gorm:"not null;default:0"`
	// StructureCompleteness 结构完整度 0-100（权重 20）。
	StructureCompleteness int `json:"structure_completeness" gorm:"not null;default:0"`
	// TableUsability 表格可用性 0-100（权重 15）。
	TableUsability int `json:"table_usability" gorm:"not null;default:0"`
	// OCRQuality OCR 质量 0-100（权重 10）。
	OCRQuality int `json:"ocr_quality" gorm:"not null;default:0"`
	// MetadataCompleteness 元数据完整度 0-100（权重 15）。
	MetadataCompleteness int `json:"metadata_completeness" gorm:"not null;default:0"`
	// Citable 可引用性 0-100（权重 15）。
	Citable int `json:"citable" gorm:"not null;default:0"`
	// TotalScore 加权和 0-100。
	TotalScore int `json:"total_score" gorm:"not null;default:0"`
	// Grade good / fair / poor（未评分行是 unknown）。
	Grade string `json:"grade" gorm:"type:varchar(16);not null;default:'unknown'"`
	// BelowThreshold = TotalScore < ParseQualityThreshold（落库冗余，让解析
	// 异常队列走单列索引，不必在 SQL 里重复阈值常量）。
	BelowThreshold bool `json:"below_threshold" gorm:"not null;default:false"`
	// Details 打分输入信号（chunk 计数、字数、命中项），JSONB，便于回放
	// "为什么是这个分"。
	Details JSON `json:"details" gorm:"type:jsonb;not null;default:'{}'"`
	// ComputedAt 本次评分的计算时刻（DDL 侧 DEFAULT NOW()，由服务层显式赋值）。
	ComputedAt time.Time `json:"computed_at" gorm:"not null"`
}

// TableName pins the GORM table name.
func (ParseQualityScore) TableName() string { return "parse_quality_scores" }

// BeforeCreate assigns an ID before persistence (upsert paths still get a
// stable primary key when the caller left it empty).
func (s *ParseQualityScore) BeforeCreate(_ *gorm.DB) error {
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	return nil
}

// GradeLevel returns the typed grade.
func (s *ParseQualityScore) GradeLevel() ParseQualityGradeLevel {
	return ParseQualityGradeLevel(s.Grade)
}

// ApplyGrade fills Grade / BelowThreshold from TotalScore, clamping the
// total into 0-100 first, so the three fields can never disagree.
func (s *ParseQualityScore) ApplyGrade() {
	if s == nil {
		return
	}
	s.TotalScore = ClampParseQualityScore(s.TotalScore)
	s.Grade = string(ParseQualityGrade(s.TotalScore))
	s.BelowThreshold = s.TotalScore < ParseQualityThreshold
}

// DimensionScores returns the six dimensions in weight-table order with
// their weighted contributions (报告/详情渲染用).
func (s *ParseQualityScore) DimensionScores() []ParseQualityDimensionScore {
	if s == nil {
		return nil
	}
	out := make([]ParseQualityDimensionScore, 0, len(ParseQualityWeights))
	for _, d := range ParseQualityWeights {
		score := s.scoreOf(d.Dimension)
		out = append(out, ParseQualityDimensionScore{
			Dimension: d.Dimension,
			Weight:    d.Weight,
			Score:     score,
			Weighted:  ParseQualityWeightedContribution(score, d.Weight),
		})
	}
	return out
}

// scoreOf resolves one dimension's raw score.
func (s *ParseQualityScore) scoreOf(d ParseQualityDimension) int {
	switch d {
	case ParseQualityDimensionTextCompleteness:
		return s.TextCompleteness
	case ParseQualityDimensionStructureCompleteness:
		return s.StructureCompleteness
	case ParseQualityDimensionTableUsability:
		return s.TableUsability
	case ParseQualityDimensionOCRQuality:
		return s.OCRQuality
	case ParseQualityDimensionMetadataCompleteness:
		return s.MetadataCompleteness
	case ParseQualityDimensionCitable:
		return s.Citable
	default:
		return 0
	}
}

// ParseQualityWeightedContribution 是单维度的加权贡献：score * weight / 100
// 四舍五入。六个维度贡献之和即 ParseQualityTotalScore 的返回值。
func ParseQualityWeightedContribution(score, weight int) int {
	score = ClampParseQualityScore(score)
	if weight < 0 {
		weight = 0
	}
	total := score * weight
	// 四舍五入（两侧均非负）。
	if remainder := total % 100; remainder >= 50 {
		return total/100 + 1
	}
	return total / 100
}

// ParseQualityTotalScore 按 §3.5 权重表加权求和六维度得分（0-100）。
// 纯函数：入参各自先钳制到 0-100，越界输入不会把总分推出量程。
func ParseQualityTotalScore(
	textCompleteness, structureCompleteness, tableUsability, ocrQuality,
	metadataCompleteness, citable int,
) int {
	return ClampParseQualityScore(
		ParseQualityWeightedContribution(textCompleteness, ParseQualityWeightTextCompleteness) +
			ParseQualityWeightedContribution(structureCompleteness, ParseQualityWeightStructureCompleteness) +
			ParseQualityWeightedContribution(tableUsability, ParseQualityWeightTableUsability) +
			ParseQualityWeightedContribution(ocrQuality, ParseQualityWeightOCRQuality) +
			ParseQualityWeightedContribution(metadataCompleteness, ParseQualityWeightMetadataCompleteness) +
			ParseQualityWeightedContribution(citable, ParseQualityWeightCitable),
	)
}

// ClampParseQualityScore clamps a raw dimension/total score into 0-100.
func ClampParseQualityScore(v int) int {
	switch {
	case v < 0:
		return 0
	case v > 100:
		return 100
	default:
		return v
	}
}

// ParseQualityQueueItem 是解析异常队列的一行（§3.5 低质量文档）：分数行 +
// 展示所需的知识元信息。仓储层分两次查询后在内存里装配（分数行按
// below_threshold 取，知识行按 ID 批量补），因此不走 gorm 的 join 扫描。
type ParseQualityQueueItem struct {
	*ParseQualityScore
	// Title 知识标题（空时前端回退 FileName）。
	Title string `json:"title"`
	// FileName 原始文件名。
	FileName string `json:"file_name"`
	// FileType 文件类型（扩展名）。
	FileType string `json:"file_type"`
	// ParseStatus 当前解析状态（低质文档可能已完成，也可能失败）。
	ParseStatus string `json:"parse_status"`
	// KnowledgeBaseID 所属知识库。
	KnowledgeBaseID string `json:"knowledge_base_id"`
	// ErrorMessage 来自 knowledges.error_message（解析失败的文档直接看到原因）。
	ErrorMessage string `json:"error_message,omitempty"`
}

// ParseQualityAuditActionComputed 是每次评分落库的审计动作（§3.5 质量分
// 是治理判断，谁在何时重算了哪条文档要可回溯）。定义在本文件而不是
// types/audit_log.go：这是解析质量垂直的私有事件面，后续扩展（如
// parse_quality.threshold_changed）在此追加。
const ParseQualityAuditActionComputed AuditAction = "parse_quality.computed"

// ParseQualityDetailsKeys 是 Details JSONB 里使用的键（服务层写、前端读，
// 集中在此避免两边拼字符串漂移）。
const (
	ParseQualityDetailChunkCount         = "chunk_count"
	ParseQualityDetailTextChunkCount     = "text_chunk_count"
	ParseQualityDetailTotalChars         = "total_chars"
	ParseQualityDetailFileSize           = "file_size"
	ParseQualityDetailCharPerKB          = "chars_per_kb"
	ParseQualityDetailStructuredChunks   = "structured_chunk_count"
	ParseQualityDetailLocatorChunks      = "locator_chunk_count"
	ParseQualityDetailTableChunks        = "table_chunk_count"
	ParseQualityDetailTableWithHeader    = "table_chunk_with_header_count"
	ParseQualityDetailOCRChunks          = "ocr_chunk_count"
	ParseQualityDetailOCRWithConfidence  = "ocr_chunk_with_confidence_count"
	ParseQualityDetailMetadataSignals    = "metadata_signals"
	ParseQualityDetailEmptyContentChunks = "empty_content_chunk_count"
)

// ParseQualityMetadataSignals 是元数据完整度的四项非空计数（§3.5 元数据
// 完整度的 v1 信号集：标题 / 描述 / 自定义元数据 / 文件哈希）。
type ParseQualityMetadataSignals struct {
	Title          bool `json:"title"`
	Description    bool `json:"description"`
	CustomMetadata bool `json:"custom_metadata"`
	FileHash       bool `json:"file_hash"`
}

// Count 返回非空信号数（0-4）。
func (m ParseQualityMetadataSignals) Count() int {
	count := 0
	for _, present := range []bool{m.Title, m.Description, m.CustomMetadata, m.FileHash} {
		if present {
			count++
		}
	}
	return count
}

// TrimParseQualityText 是评分前的文本归一（去首尾空白）。保留为独立函数，
// 服务层与测试共用同一判定。
func TrimParseQualityText(s string) string {
	return strings.TrimSpace(s)
}
