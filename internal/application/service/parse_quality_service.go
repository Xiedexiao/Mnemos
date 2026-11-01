package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// ============================================================================
// 解析质量评分（KnowAxis V3.0 §3.5 / §9.4 v1）。
//
// 一条知识解析完之后，从"解析产出"本身能观察到的信号给六个维度各打 0-100，
// 再按 §3.5 的权重表（25/20/15/10/15/15）加权成 total_score。落库一行
// parse_quality_scores（迁移 000156）；重算即覆盖。
//
// 打分口径全部是启发式（文档里只给了维度与权重，没给公式），因此每个维度
// 的阈值都写成具名常量并在注释里说明"为什么是这个数"，方便后续用 golden
// set 校准时只改常量、不改结构。
//
// 硬边界（§3.5 末句）：total < ParseQualityThreshold(60) 的文档进解析异常
// 队列，"可搜但不可作高置信来源"。打分本身不改检索行为——降权由消费方读
// below_threshold 决定。
// ============================================================================

// 打分的启发式阈值表。单位见注释；改动会影响历史分与队列口径。
const (
	// parseQualityCharsPerKBFull 是"文本完整度满分"的字符密度：每 1KB 原始
	// 文件产出 12 个有效字符即视为正文提取完整。
	//
	// 依据：中文 PDF/DOCX 每 KB 约 500-600 字节/字，理论上限 ≈ 2 字符/KB；
	// 英文 ≈ 1000 字符/KB。取 12 是因为解析产物含 Markdown 标记、重复标题
	// 与分块重叠，正常文档的实测密度普遍在 8-15 字符/KB 之间；达到 12 说明
	// 正文基本没丢。低于它按比例线性丢分。
	parseQualityCharsPerKBFull = 12.0
	// parseQualityCharsPerKBMidpoint 是密度曲线的中点（半程满分）：低于它
	// 的文档按"密度/中点 × 50"给分，介于中点与满分之间再线性补到 100。
	// 双段线性让"只提取到零星文字"（密度 1-3）明显掉分，而"丢了少量格式"
	// 不被重罚。
	parseQualityCharsPerKBMidpoint = 4.0
	// parseQualityTextFloor 是文本完整度的下限：一条 chunk 都没有的文档
	// 拿 0；有 chunk 但密度极低时最低给到这个值（部分提取仍有检索价值）。
	parseQualityTextFloor = 15
	// parseQualityNoFileSizeChars 是无文件大小（file_size <= 0，例如 URL 导入
	// 或手工知识）时的字符数基准：每累计 2000 字符视为 1 个"单元"，
	// 40 个单元（8 万字符）拿满分。这类行没有字节数可比，只能按绝对字数算。
	parseQualityNoFileSizeCharsPerUnit = 2000.0
	parseQualityNoFileSizeMaxUnits     = 40.0

	// parseQualityTableHeaderRatio 是表格可用性里"表头命中率"的权重：
	// 表格 chunk 里带了表头（table_summary 的列描述）的比例占 70%，
	// 其余 30% 给"表格 chunk 本身存在"（有结构就比碎成纯文本强）。
	parseQualityTableHeaderWeight   = 70
	parseQualityTablePresenceWeight = 30
	// parseQualityTableTextBonus 是无表格 chunk 但正文里出现 Markdown 表格
	// 语法时的补偿分：说明表格内容以文本形态保留了下来（可用性打折）。
	parseQualityTableTextBonus = 55

	// parseQualityOCRDefault 是 image_ocr chunk 没有置信度信号时的中位分。
	// 现有管线不产出 OCR 置信度（VLM 直接给文本），因此这是常态路径：
	// 给中位分既不让纯扫描件被判死，也不假装它可信。
	parseQualityOCRDefault = 60
	// parseQualityOCRNoImage 是没有 image_ocr chunk 时的分：文档不含图片文字
	// 时 OCR 维度不适用，按"不扣分"处理（100）——否则所有纯文本文档都会被
	// 这一维拉低，与 §3.5 的意图（识别质量差才扣分）相反。
	parseQualityOCRNoImage = 100
	// parseQualityOCRMinTextLen 是"认出来算有效"的最短 OCR 文本（字符）：
	// 短于此的 OCR chunk 视为噪声（页码、水印），不计入有效识别。
	parseQualityOCRMinTextLen = 4

	// parseQualityMetadataRateScale 元数据完整度：每命中一个信号给
	// 100/4 = 25 分（四个信号等权：title / description / custom_metadata /
	// file_hash）。
	parseQualityMetadataSignalCount = 4

	// parseQualityCitableTargetRate 可引用性满分的 SourceLocators 覆盖率：
	// 80% 以上的 chunk 带定位即满分（尾块、目录块常常没有定位，留 20%
	// 宽容度）。低于它按比例给分。
	parseQualityCitableTargetRate = 0.8
	// parseQualityCitableNoLocatorFloor 完全没有任何定位时的下限（0）：
	// 定位信息全缺 = 引用无法跳转，这一维必须实打实归零。
	parseQualityCitablePartialWeight = 0.7 // 有定位但覆盖不足时的基线系数

	// parseQualityListLimit 是解析异常队列的默认与硬上限。
	parseQualityListDefault = 50
	parseQualityListLimit   = 200
)

// Parse quality service sentinel errors. The handler maps them onto HTTP
// status codes (400 / 401 / 404), matching the other verticals' style.
var (
	// ErrParseQualityTenantMissing 租户上下文缺失（tenantID 为 0）。
	ErrParseQualityTenantMissing = errors.New("parse quality: tenant context is missing")
	// ErrParseQualityKnowledgeMissing 知识 ID 缺失。
	ErrParseQualityKnowledgeMissing = errors.New("parse quality: knowledge id is required")
	// ErrParseQualityKnowledgeNotFound 知识不存在（或不在本租户）。
	ErrParseQualityKnowledgeNotFound = errors.New("parse quality: knowledge not found")
	// ErrParseQualityNotFound 该知识尚无评分行（尚未计算过）。
	ErrParseQualityNotFound = errors.New("parse quality: score not found")
)

// parseQualityService implements interfaces.ParseQualityService.
//
// 依赖取舍：
//   - knowledgeRepo 读 knowledges 行（标题/描述/自定义元数据/文件哈希/大小）；
//   - chunkRepo 读该知识的 chunks（文本、表格、OCR 三类原语）；
//   - audit 可选（nil 时跳过审计，不阻塞评分——同 feedback/golden set 的
//     降级策略）。
type parseQualityService struct {
	repo          interfaces.ParseQualityRepository
	knowledgeRepo interfaces.KnowledgeRepository
	chunkRepo     interfaces.ChunkRepository
	audit         interfaces.AuditLogService
	now           func() time.Time
}

// NewParseQualityService constructs the parse quality service.
func NewParseQualityService(
	repo interfaces.ParseQualityRepository,
	knowledgeRepo interfaces.KnowledgeRepository,
	chunkRepo interfaces.ChunkRepository,
	audit interfaces.AuditLogService,
) interfaces.ParseQualityService {
	return &parseQualityService{
		repo:          repo,
		knowledgeRepo: knowledgeRepo,
		chunkRepo:     chunkRepo,
		audit:         audit,
		now:           time.Now,
	}
}

// ComputeForKnowledge 计算并落库一条知识的解析质量分（§3.5）。
//
// 流程：读 knowledges 行（租户内，不存在报 ErrParseQualityKnowledgeNotFound）
// → 读该知识的全部 chunks（ListAllChunksByKnowledgeID：含 text / parent_text /
// summary / table_summary / table_column / image_ocr，打分需要跨类型看结构）
// → 六维度打分 → 加权 → UpsertScore → 审计。
//
// 空 chunk 的文档仍然落一行 0 分（below_threshold=true）：它在队列里出现的
// 意义正是"这条解析没产出东西"。
func (s *parseQualityService) ComputeForKnowledge(
	ctx context.Context, tenantID uint64, knowledgeID, actorID string,
) (*types.ParseQualityScore, error) {
	if tenantID == 0 {
		return nil, ErrParseQualityTenantMissing
	}
	knowledgeID = strings.TrimSpace(knowledgeID)
	if knowledgeID == "" {
		return nil, ErrParseQualityKnowledgeMissing
	}
	if s.knowledgeRepo == nil || s.chunkRepo == nil {
		return nil, errors.New("parse quality: service dependencies are not ready")
	}

	knowledge, err := s.knowledgeRepo.GetKnowledgeByID(ctx, tenantID, knowledgeID)
	if err != nil {
		if errors.Is(err, repository.ErrKnowledgeNotFound) || errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrParseQualityKnowledgeNotFound
		}
		return nil, err
	}

	chunks, err := s.chunkRepo.ListAllChunksByKnowledgeID(ctx, tenantID, knowledgeID)
	if err != nil {
		return nil, err
	}

	signals := buildParseQualitySignals(knowledge, chunks)
	score := signals.toScore(tenantID, knowledgeID)
	score.ComputedAt = s.now().UTC()

	if err := s.repo.UpsertScore(ctx, score); err != nil {
		return nil, err
	}
	s.logComputed(ctx, score, actorID)
	return score, nil
}

// Get 返回已落库的评分；无评分行时返回 ErrParseQualityNotFound（handler 映射
// 404，前端据此提示"尚未评分，可触发重算"）。
func (s *parseQualityService) Get(
	ctx context.Context, tenantID uint64, knowledgeID string,
) (*types.ParseQualityScore, error) {
	if tenantID == 0 {
		return nil, ErrParseQualityTenantMissing
	}
	knowledgeID = strings.TrimSpace(knowledgeID)
	if knowledgeID == "" {
		return nil, ErrParseQualityKnowledgeMissing
	}
	score, err := s.repo.GetByKnowledge(ctx, tenantID, knowledgeID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrParseQualityNotFound
		}
		return nil, err
	}
	return score, nil
}

// ListBelowThreshold 返回解析异常队列（§3.5）：total_score < 阈值 的文档，
// 最差的在前，行内附知识元信息。limit 由服务层钳制（≤0 → 默认，>上限 →
// 上限），避免把整个租户的低质文档一次拖出来。
func (s *parseQualityService) ListBelowThreshold(
	ctx context.Context, tenantID uint64, limit int,
) ([]*types.ParseQualityQueueItem, error) {
	if tenantID == 0 {
		return nil, ErrParseQualityTenantMissing
	}
	items, err := s.repo.ListQueueItemsBelowThreshold(ctx, tenantID, clampParseQualityLimit(limit))
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []*types.ParseQualityQueueItem{}
	}
	return items, nil
}

// ----------------------------------------------------------------------------
// 打分
// ----------------------------------------------------------------------------

// parseQualitySignals 是打分前的原始观测值：先数清楚"有什么"，再换算成
// 六维度得分。把观测与换算分开，是为了让 details JSONB 能如实记录输入，
// 也便于测试只断言观测。
type parseQualitySignals struct {
	chunkCount             int
	textChunkCount         int
	emptyContentChunks     int
	totalChars             int
	fileSize               int64
	structuredChunkCount   int
	locatorChunkCount      int
	tableChunkCount        int
	tableChunkWithHeader   int
	markdownTableInText    bool
	ocrChunkCount          int
	ocrChunkWithConfidence int
	ocrValidChars          int
	// ocrConfidences 是有置信度信号的 OCR chunk 的置信度（已归一 0-100），
	// 用于算均值；没有信号时为空。
	ocrConfidences []float64
	metadata       types.ParseQualityMetadataSignals
}

// buildParseQualitySignals 汇总一条知识 + 其 chunks 的可观测信号。
func buildParseQualitySignals(
	knowledge *types.Knowledge, chunks []*types.Chunk,
) *parseQualitySignals {
	signals := &parseQualitySignals{}
	if knowledge != nil {
		signals.fileSize = knowledge.FileSize
		signals.metadata = parseQualityMetadataSignals(*knowledge)
	}

	for _, chunk := range chunks {
		if chunk == nil {
			continue
		}
		signals.chunkCount++
		content := strings.TrimSpace(chunk.Content)
		if content == "" {
			signals.emptyContentChunks++
		}
		switch chunk.ChunkType {
		case types.ChunkTypeTableSummary, types.ChunkTypeTableColumn:
			signals.tableChunkCount++
			if content != "" && (chunk.ChunkType == types.ChunkTypeTableColumn ||
				parseQualityHasTableHeader(content)) {
				signals.tableChunkWithHeader++
			}
			signals.totalChars += utf8.RuneCountInString(content)
		case types.ChunkTypeImageOCR:
			signals.ocrChunkCount++
			if confidence := parseQualityConfidenceOf(chunk); confidence >= 0 {
				signals.ocrChunkWithConfidence++
				signals.ocrConfidences = append(signals.ocrConfidences, confidence)
			}
			if content != "" {
				signals.ocrValidChars += utf8.RuneCountInString(content)
			}
			signals.totalChars += utf8.RuneCountInString(content)
		case types.ChunkTypeParentText:
			// 父块只作上下文，不参与向量索引；计入结构分（说明存在层级
			// 分组），但不重复计入字数（子块已经包含同样内容）。
		default:
			if parseQualityIsPrimaryTextType(chunk.ChunkType) {
				signals.textChunkCount++
				signals.totalChars += utf8.RuneCountInString(content)
				if content != "" && parseQualityHasStructure(content) {
					signals.structuredChunkCount++
				}
				if !signals.markdownTableInText && parseQualityHasMarkdownTable(content) {
					signals.markdownTableInText = true
				}
			}
		}
		if len(chunk.SourceLocators) > 0 {
			signals.locatorChunkCount++
		}
	}
	return signals
}

// parseQualityMetadataSignals 汇总元数据完整度的四项非空信号（§3.5）。
func parseQualityMetadataSignals(knowledge types.Knowledge) types.ParseQualityMetadataSignals {
	return types.ParseQualityMetadataSignals{
		Title:          strings.TrimSpace(knowledge.Title) != "",
		Description:    strings.TrimSpace(knowledge.Description) != "",
		CustomMetadata: parseQualityHasJSONPayload(knowledge.CustomMetadata),
		FileHash:       strings.TrimSpace(knowledge.FileHash) != "",
	}
}

// toScore 把观测信号换算成评分行（grade / below_threshold 由 ApplyGrade 补）。
func (s *parseQualitySignals) toScore(tenantID uint64, knowledgeID string) *types.ParseQualityScore {
	text := s.textCompletenessScore()
	structure := s.structureCompletenessScore()
	table := s.tableUsabilityScore()
	ocr := s.ocrQualityScore()
	metadata := s.metadataCompletenessScore()
	citable := s.citableScore()

	total := types.ParseQualityTotalScore(text, structure, table, ocr, metadata, citable)
	score := &types.ParseQualityScore{
		TenantID:              tenantID,
		KnowledgeID:           knowledgeID,
		TextCompleteness:      text,
		StructureCompleteness: structure,
		TableUsability:        table,
		OCRQuality:            ocr,
		MetadataCompleteness:  metadata,
		Citable:               citable,
		TotalScore:            total,
		Details:               s.detailsJSON(),
	}
	score.ApplyGrade()
	return score
}

// textCompletenessScore 文本完整度（25%）：chunk 总字数 / 文件大小 的密度
// 相对阈值的达成度。
//
// 启发式说明：
//   - file_size > 0（文件上传）：按"每 KB 产出多少有效字符"算密度。密度达
//     到 parseQualityCharsPerKBFull(12) 拿 100；低于中点(4)按
//     density/midpoint × 50 线性给分；中点与满分之间从 50 线性补到 100。
//     这个双段曲线让"只抓到零星文字"快速掉分（0.5 字符/KB → 6 分），
//     而"格式有点损失"不至于重罚。
//   - file_size <= 0（URL 导入 / 手工知识）：没有字节基准，退回绝对字数，
//     每 2000 字符 1 个单元、40 单元（8 万字符）满分。
//   - 没有任何 chunk：0 分（解析没产出）。
//   - 有 chunk 但密度极低：不低于 parseQualityTextFloor，部分提取仍有检索
//     价值，这一维不该直接判死（真正的判死在 below_threshold 上）。
func (s *parseQualitySignals) textCompletenessScore() int {
	if s.chunkCount == 0 || s.totalChars == 0 {
		return 0
	}
	var ratio float64
	if s.fileSize > 0 {
		kb := float64(s.fileSize) / 1024.0
		density := float64(s.totalChars) / math.Max(kb, 0.001)
		switch {
		case density >= parseQualityCharsPerKBFull:
			ratio = 1
		case density >= parseQualityCharsPerKBMidpoint:
			span := parseQualityCharsPerKBFull - parseQualityCharsPerKBMidpoint
			ratio = 0.5 + 0.5*(density-parseQualityCharsPerKBMidpoint)/span
		default:
			ratio = 0.5 * density / parseQualityCharsPerKBMidpoint
		}
	} else {
		units := float64(s.totalChars) / parseQualityNoFileSizeCharsPerUnit
		ratio = math.Min(units/parseQualityNoFileSizeMaxUnits, 1)
	}
	score := int(math.Round(ratio * 100))
	if score < parseQualityTextFloor {
		score = parseQualityTextFloor
	}
	return types.ClampParseQualityScore(score)
}

// structureCompletenessScore 结构完整度（20%）：正文 chunk 里"保留了结构"的
// 占比，再加上 SourceLocators 的结构性加分。
//
// 判定"保留结构"（parseQualityHasStructure）：正文含 Markdown 标题（#）、
// 列表项、表格分隔行，或含换行分段（≥2 个段落）。这些正是 §3.5 说的
// "标题、段落、表格是否保留"。
//
// 加分：chunk 带 SourceLocators 说明解析器知道它在原文的结构位置，
// 每条覆盖最高 +20 分（structureLocatorBonus），因为定位信息本身就是结构
// 证据。最终钳制到 0-100。
func (s *parseQualitySignals) structureCompletenessScore() int {
	if s.textChunkCount == 0 {
		// 没有正文块（纯表格/纯图片文档）：结构分退化为表格块占比。
		if s.chunkCount == 0 {
			return 0
		}
		return int(math.Round(100 * float64(s.tableChunkCount) / float64(s.chunkCount)))
	}
	base := 100 * float64(s.structuredChunkCount) / float64(s.textChunkCount)
	locatorRate := 0.0
	if s.chunkCount > 0 {
		locatorRate = float64(s.locatorChunkCount) / float64(s.chunkCount)
	}
	// 定位覆盖每 100% 加 20 分（上限 20）。
	bonus := locatorRate * parseQualityStructureLocatorBonus
	return types.ClampParseQualityScore(int(math.Round(base + bonus)))
}

// parseQualityStructureLocatorBonus 是结构完整度里"chunk 带原文定位"的加分上限。
const parseQualityStructureLocatorBonus = 20.0

// tableUsabilityScore 表格可用性（15%）：
//   - 有 table_summary / table_column chunk：表头命中率占 70%，表格块存在
//     占 30%（有独立表格结构本身就比碎在正文里可用）；
//   - 没有表格 chunk，但正文里有 Markdown 表格语法：给
//     parseQualityTableTextBonus(55)——内容还在，但少了列语义；
//   - 两者都没有：返回 100（该文件不含表格，这一维不适用，不该扣分）。
func (s *parseQualitySignals) tableUsabilityScore() int {
	if s.tableChunkCount == 0 {
		if s.markdownTableInText {
			return parseQualityTableTextBonus
		}
		return 100
	}
	headerRate := float64(s.tableChunkWithHeader) / float64(s.tableChunkCount)
	score := float64(parseQualityTableHeaderWeight)*headerRate + float64(parseQualityTablePresenceWeight)
	return types.ClampParseQualityScore(int(math.Round(score)))
}

// ocrQualityScore OCR 质量（10%）：image_ocr chunk 的置信度信号。
//
// 现有链路不产出置信度（VLM 直接给文本），因此：
//   - 没有 image_ocr chunk → 100（文档不含图片文字，该维不适用）；
//   - 有 image_ocr chunk 但全无置信度字段 → parseQualityOCRDefault(60)，
//     即"识别成功但无法验证"，中位分；
//   - 有置信度字段 → 按置信度均值给分（0-1 或 0-100 两种写法都识别），
//     再按"有效识别字符数"微调：OCR 文本短于 parseQualityOCRMinTextLen
//     视为噪声，不计入有效内容。
func (s *parseQualitySignals) ocrQualityScore() int {
	if s.ocrChunkCount == 0 {
		return parseQualityOCRNoImage
	}
	if s.ocrValidChars < parseQualityOCRMinTextLen {
		// 有 OCR 块但啥也没认出来：几乎必然是识别失败。
		return 0
	}
	if s.ocrChunkWithConfidence == 0 {
		return parseQualityOCRDefault
	}
	sum := 0.0
	count := 0.0
	for _, confidence := range s.ocrConfidences {
		sum += confidence
		count++
	}
	if count == 0 {
		return parseQualityOCRDefault
	}
	return types.ClampParseQualityScore(int(math.Round(sum / count)))
}

// metadataCompletenessScore 元数据完整度（15%）：四项等权信号
// (title / description / custom_metadata / file_hash)，每命中一个 25 分。
func (s *parseQualitySignals) metadataCompletenessScore() int {
	hit := s.metadata.Count()
	if hit <= 0 {
		return 0
	}
	if hit > parseQualityMetadataSignalCount {
		hit = parseQualityMetadataSignalCount
	}
	return int(math.Round(100 * float64(hit) / float64(parseQualityMetadataSignalCount)))
}

// citableScore 可引用性（15%）：带 SourceLocators 的 chunk 占比。
//
// 覆盖率达到 parseQualityCitableTargetRate(80%) 即满分（尾块/目录块常常没有
// 定位，留 20% 宽容度）；低于它按 rate/target 线性给分，并在有定位时保底
// 乘上 parseQualityCitablePartialWeight 的基线，避免"少量定位"被算成 0。
// 完全没有定位 → 0：引用无法跳转到页码/段落，这一维必须归零。
func (s *parseQualitySignals) citableScore() int {
	if s.chunkCount == 0 || s.locatorChunkCount == 0 {
		return 0
	}
	rate := float64(s.locatorChunkCount) / float64(s.chunkCount)
	if rate >= parseQualityCitableTargetRate {
		return 100
	}
	ratio := rate / parseQualityCitableTargetRate
	// 有定位时给一条软基线：ratio 很低也不至于被判 0（定位确实存在）。
	ratio = ratio*parseQualityCitablePartialWeight + parseQualityCitablePartialWeight*0.3
	return types.ClampParseQualityScore(int(math.Round(ratio * 100)))
}

// detailsJSON 序列化打分输入（原因可回放）。序列化失败退回 {}，绝不让
// 详情构造阻塞评分落库（同 feedback 的 mustDetailsJSON 契约）。
func (s *parseQualitySignals) detailsJSON() types.JSON {
	details := map[string]interface{}{
		types.ParseQualityDetailChunkCount:         s.chunkCount,
		types.ParseQualityDetailTextChunkCount:     s.textChunkCount,
		types.ParseQualityDetailTotalChars:         s.totalChars,
		types.ParseQualityDetailFileSize:           s.fileSize,
		types.ParseQualityDetailStructuredChunks:   s.structuredChunkCount,
		types.ParseQualityDetailLocatorChunks:      s.locatorChunkCount,
		types.ParseQualityDetailTableChunks:        s.tableChunkCount,
		types.ParseQualityDetailTableWithHeader:    s.tableChunkWithHeader,
		types.ParseQualityDetailOCRChunks:          s.ocrChunkCount,
		types.ParseQualityDetailOCRWithConfidence:  s.ocrChunkWithConfidence,
		types.ParseQualityDetailEmptyContentChunks: s.emptyContentChunks,
		types.ParseQualityDetailMetadataSignals:    s.metadata,
	}
	if s.fileSize > 0 {
		kb := float64(s.fileSize) / 1024.0
		details[types.ParseQualityDetailCharPerKB] = math.Round(float64(s.totalChars)/math.Max(kb, 0.001)*100) / 100
	}
	data, err := json.Marshal(details)
	if err != nil {
		return types.JSON("{}")
	}
	return types.JSON(data)
}

// logComputed 写 parse_quality.computed 审计。审计失败不阻塞评分（同
// feedback/golden set 的降级契约）。
func (s *parseQualityService) logComputed(
	ctx context.Context, score *types.ParseQualityScore, actorID string,
) {
	if s.audit == nil || score == nil {
		return
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    score.TenantID,
		ActorUserID: actorID,
		Action:      types.ParseQualityAuditActionComputed,
		ScopeType:   "knowledge",
		ScopeID:     score.KnowledgeID,
		TargetType:  "parse_quality_score",
		TargetID:    score.KnowledgeID,
		Outcome:     types.AuditOutcomeSuccess,
		Details: parseQualityDetailsJSON(map[string]interface{}{
			"knowledge_id":           score.KnowledgeID,
			"total_score":            score.TotalScore,
			"grade":                  score.Grade,
			"below_threshold":        score.BelowThreshold,
			"threshold":              types.ParseQualityThreshold,
			"text_completeness":      score.TextCompleteness,
			"structure_completeness": score.StructureCompleteness,
			"table_usability":        score.TableUsability,
			"ocr_quality":            score.OCRQuality,
			"metadata_completeness":  score.MetadataCompleteness,
			"citable":                score.Citable,
		}),
	})
}

// ----------------------------------------------------------------------------
// 小工具
// ----------------------------------------------------------------------------

// parseQualityConfidenceOf 从 chunk 里读 OCR 置信度（0-100 归一后返回）。
// 现有链路不写这个字段，因此返回 -1 表示"无信号"。读取位置按可能性排序：
//  1. chunk.Metadata["ocr_confidence"]（管线自定字段）；
//  2. chunk.Metadata["confidence"]（通用字段）。
//
// 值可以是 0-1 的小数或 0-100 的整数，两种都归一成 0-100。
func parseQualityConfidenceOf(chunk *types.Chunk) float64 {
	if chunk == nil || len(chunk.Metadata) == 0 {
		return -1
	}
	values, err := chunk.Metadata.Map()
	if err != nil {
		return -1
	}
	for _, key := range []string{"ocr_confidence", "confidence"} {
		raw, ok := values[key]
		if !ok {
			continue
		}
		value, ok := parseQualityFloat(raw)
		if !ok {
			continue
		}
		if value <= 1 {
			value *= 100
		}
		if value < 0 {
			continue
		}
		if value > 100 {
			value = 100
		}
		return value
	}
	return -1
}

// parseQualityFloat 把 JSON 数字/字符串形态的置信度解析成 float64。
func parseQualityFloat(raw interface{}) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return f, true
		}
		return 0, false
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return 0, false
		}
		var f float64
		if _, err := fmtSscan(trimmed, &f); err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

// fmtSscan 是 strconv.ParseFloat 的薄封装（字符串形态的置信度解析）。
func fmtSscan(s string, out *float64) (int, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	*out = f
	return 1, nil
}

// parseQualityHasStructure 判定一段正文是否保留了结构（§3.5 结构完整度）。
// 命中任一即算：Markdown 标题、列表项、表格分隔行、≥2 个换行分段。
func parseQualityHasStructure(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "- ") ||
			strings.HasPrefix(line, "* ") || strings.HasPrefix(line, "> ") {
			return true
		}
		if parseQualityIsTableSeparator(line) {
			return true
		}
		// 有序列表 "1. xxx"
		if len(line) > 2 && line[0] >= '0' && line[0] <= '9' {
			if idx := strings.Index(line, ". "); idx > 0 && idx <= 3 {
				return true
			}
		}
	}
	// 多段落（≥2 个非空段）。
	paragraphs := 0
	for _, part := range strings.Split(trimmed, "\n\n") {
		if strings.TrimSpace(part) != "" {
			paragraphs++
			if paragraphs >= 2 {
				return true
			}
		}
	}
	return false
}

// parseQualityHasMarkdownTable 判定正文里是否有 Markdown 表格（表头行 +
// 分隔行）。只看前若干行——表格通常在块首。
func parseQualityHasMarkdownTable(content string) bool {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	checked := 0
	for i, line := range lines {
		checked++
		if checked > 50 {
			return false
		}
		if !parseQualityIsTableSeparator(strings.TrimSpace(line)) {
			continue
		}
		// 分隔行上一行必须是含 | 的表头行。
		if i == 0 {
			continue
		}
		if strings.Count(strings.TrimSpace(lines[i-1]), "|") >= 2 {
			return true
		}
	}
	return false
}

// parseQualityHasTableHeader 判定表格类 chunk 的文本是否含表头结构
// （Markdown 表格或 "列名: 说明" 的列描述行）。
func parseQualityHasTableHeader(content string) bool {
	if parseQualityHasMarkdownTable(content) {
		return true
	}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		// table_column chunk 的形态是 "**列名** (类型)\n- Field Meaning: ..."
		if strings.HasPrefix(line, "**") && strings.Contains(line, "**") &&
			strings.Contains(strings.ToLower(line), "column") {
			return true
		}
	}
	return false
}

// parseQualityIsTableSeparator 判定一行是否是 Markdown 表格分隔行
// （|---|---| 或 |:--:|--:| 等）。
func parseQualityIsTableSeparator(line string) bool {
	if strings.Count(line, "|") < 2 {
		return false
	}
	hasDash := false
	for _, r := range line {
		switch {
		case r == '-':
			hasDash = true
		case r == '|' || r == ':' || unicode.IsSpace(r):
			// 允许
		default:
			return false
		}
	}
	return hasDash
}

// parseQualityIsPrimaryTextType 报告该 chunk 类型是否计入"正文"统计。
// 摘要/实体/关系/FAQ/wiki 等派生块不算正文：它们由模型生成，不代表解析
// 提取的正文多少。
func parseQualityIsPrimaryTextType(chunkType types.ChunkType) bool {
	switch chunkType {
	case types.ChunkTypeText:
		return true
	default:
		return false
	}
}

// parseQualityHasJSONPayload 判定 JSON 列是否有实质内容（非 NULL / 非 '{}' /
// 非 '[]' / 非 'null'），与知识健康度看板的判定口径一致。
func parseQualityHasJSONPayload(raw types.JSON) bool {
	if len(raw) == 0 {
		return false
	}
	trimmed := strings.TrimSpace(string(raw))
	switch trimmed {
	case "", "null", "{}", "[]":
		return false
	default:
		return true
	}
}

// parseQualityDetailsJSON marshals an audit details payload, falling back to
// an empty object so logging never blocks a score write. Kept local to this
// vertical (同 feedback 的 mustDetailsJSON 取舍：各垂直独立维护).
func parseQualityDetailsJSON(v interface{}) types.JSON {
	data, err := json.Marshal(v)
	if err != nil {
		return types.JSON("{}")
	}
	return types.JSON(data)
}

// clampParseQualityLimit clamps the queue limit into (0, hard cap].
func clampParseQualityLimit(limit int) int {
	if limit <= 0 {
		return parseQualityListDefault
	}
	if limit > parseQualityListLimit {
		return parseQualityListLimit
	}
	return limit
}

// 编译期接口满足性检查。
var _ interfaces.ParseQualityService = (*parseQualityService)(nil)
