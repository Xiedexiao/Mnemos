package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
)

// ============================================================================
// AI 预审（KnowAxis V3.0 §6.5）。
//
// 定位：把审批人应该看的地方指出来，不替审批人做决定。因此这里的每一次运行
// 只产出"检查清单 + 风险提示"，既不返回通过/不通过，也不触碰审批状态机。
//
// 两层检查：
//   - 规则类（确定性、零外部依赖、必做）：完整性、术语、重复、敏感、时效、
//     表述，外加引用/冲突的规则线索（断链、书名缺版本、废止类表述）。规则
//     类不依赖模型，因此预审在最保守的部署里也总能给出检查清单。
//   - 模型类（可选）：注入 interfaces.ModelService 且能取到 chat 模型时，
//     对"引用/冲突"做一次结构化输出调用。任何失败（无模型、超时、JSON
//     不合法）都降级为一条 info 提示，绝不让预审阻塞审批（§6.5 边界）。
//
// 可解释性：每条提示带 evidence（依据原文）与 locator（位置），并标注
// source（rules/model），审批人据此判断该复核到什么程度、误报可以标
// dismissed。model_version / prompt_version 落库，便于回溯与评测。
// ============================================================================

// AI 预审业务哨兵错误。handler 按 401/404/400 映射，与 review 系列同风格。
var (
	// ErrAIPreReviewTenantMissing 缺少租户上下文。
	ErrAIPreReviewTenantMissing = errors.New("ai pre-review: tenant context is missing")
	// ErrAIPreReviewTaskNotFound 审批任务不存在（或不在本租户）。
	ErrAIPreReviewTaskNotFound = errors.New("ai pre-review: approval task not found")
	// ErrAIPreReviewKnowledgeNotFound 被预审的知识不存在（或不在本租户）。
	ErrAIPreReviewKnowledgeNotFound = errors.New("ai pre-review: knowledge not found")
	// ErrAIPreReviewKnowledgeMismatch 传入的 knowledge_id 与任务上的知识不一致
	// （防止用任务 A 的 ID 预审知识 B，让预审结论挂错任务）。
	ErrAIPreReviewKnowledgeMismatch = errors.New("ai pre-review: knowledge does not match the approval task")
)

// errAIPreReviewModelNotConfigured 是内部降级信号，不进 HTTP：没有注入模型
// 服务或没有可用 chat 模型时，模型类检查整体缺席。
var errAIPreReviewModelNotConfigured = errors.New("ai pre-review: no chat model is configured")

const (
	// aiPreReviewPromptVersion 是提示词版本。改动提示词必须同步改这个常量，
	// 否则历史运行行无法对应到当时的口径（§6.5 要求记录 Prompt 版本）。
	aiPreReviewPromptVersion = "aipre-prompt-v1"

	// aiPreReviewBodyLimit 是规则类检查读取的正文上限（rune）。
	aiPreReviewBodyLimit = 20000
	// aiPreReviewLLMBodyLimit 是送进模型的正文上限（rune）。取小值是因为
	// 模型只做引用/冲突判断，不需要读完全文，也避免预审把预算吃光。
	aiPreReviewLLMBodyLimit = 6000
	// aiPreReviewRunBudget 是一次预审的总预算（脱离请求生命周期后自管）。
	aiPreReviewRunBudget = 90 * time.Second
	// aiPreReviewLLMBudget 是模型类检查的单独预算：超时即降级为 info。
	aiPreReviewLLMBudget = 45 * time.Second

	// aiPreReviewFreshnessYears 是"时间标记过期"阈值（§6.5：超过 2 年提示）。
	aiPreReviewFreshnessYears = 2
	// aiPreReviewStandardYears 是"标准版本年份过旧"阈值。
	aiPreReviewStandardYears = 5

	// aiPreReviewMaxFindings 是一次运行落库提示数的硬上限：预审是"指出该看
	// 的地方"，不是体检报告；超过 40 条审批人反而会全部忽略。
	aiPreReviewMaxFindings = 40
	// aiPreReviewMaxDuplicateTitles 是重复检查在证据里列出的相似标题上限。
	aiPreReviewMaxDuplicateTitles = 5
	// aiPreReviewDuplicateScanLimit 是重复检查单次扫描的同 KB 知识条数。
	aiPreReviewDuplicateScanLimit = 200
	// aiPreReviewTitleSimilarityThreshold 是判定"高度相似"的标题相似度。
	aiPreReviewTitleSimilarityThreshold = 0.5
	// aiPreReviewTermScanLimit 是术语检查扫描的术语条数上限。
	aiPreReviewTermScanLimit = 500
	// aiPreReviewMaxModelFindings 是模型单次返回可接受的提示条数上限。
	aiPreReviewMaxModelFindings = 10
	// aiPreReviewEvidenceSnippet 是证据片段的最大长度（rune）。
	aiPreReviewEvidenceSnippet = 160
	// aiPreReviewRunListLimit 是运行历史列表的默认条数（仓储侧还有硬上限）。
	aiPreReviewRunListLimit = 20
	// aiPreReviewLinesLimit 是表述/引用检查扫描的行数上限。
	aiPreReviewLinesLimit = 2000
	// aiPreReviewVersionFieldLimit 对齐 model_version 列的 varchar(64)。
	aiPreReviewVersionFieldLimit = 64
	// aiPreReviewErrorMessageLimit 限制 error_message 的长度，避免坏栈刷库。
	aiPreReviewErrorMessageLimit = 500
)

// aiPreReviewService implements interfaces.AIPreReviewService.
type aiPreReviewService struct {
	runs       interfaces.AIPreReviewRepository
	tasks      interfaces.ApprovalTaskRepository
	knowledges interfaces.KnowledgeRepository
	kbs        interfaces.KnowledgeBaseRepository
	chunks     interfaces.ChunkRepository
	audit      interfaces.AuditLogService
	// glossary / dlp / models 是可选的：缺失只让对应检查缺席（或降级），
	// 规则类的基础六项始终可跑。
	glossary interfaces.GlossaryService
	dlp      interfaces.DLPService
	models   interfaces.ModelService
	now      func() time.Time
}

// NewAIPreReviewService constructs the AI pre-review service.
//
// 必需的依赖：运行仓储、审批任务仓储、知识仓储、KB 仓储、chunk 仓储、审计。
// 可选依赖（可传 nil，对应检查缺席/降级）：glossary（术语）、dlp（敏感）、
// models（引用/冲突的模型判断）。
func NewAIPreReviewService(
	runs interfaces.AIPreReviewRepository,
	tasks interfaces.ApprovalTaskRepository,
	knowledges interfaces.KnowledgeRepository,
	kbs interfaces.KnowledgeBaseRepository,
	chunks interfaces.ChunkRepository,
	audit interfaces.AuditLogService,
	glossary interfaces.GlossaryService,
	dlp interfaces.DLPService,
	models interfaces.ModelService,
) interfaces.AIPreReviewService {
	return &aiPreReviewService{
		runs:       runs,
		tasks:      tasks,
		knowledges: knowledges,
		kbs:        kbs,
		chunks:     chunks,
		audit:      audit,
		glossary:   glossary,
		dlp:        dlp,
		models:     models,
		now:        time.Now,
	}
}

// RunPreReview 对一次审批任务执行预审。
//
// 流程：校验任务与知识归属 → 落 running 运行行 → 规则类检查 → 模型类检查
// （可选，失败降级）→ 排序/去重/截断 → 落 completed + 审计 → 返回提示。
//
// 预审运行脱离调用方的取消信号（context.WithoutCancel）并自带总预算：这样
// "提交审核后异步触发预审"的 goroutine 不会因为 HTTP 请求结束而被半路掐断，
// 运行行也不会永远停在 running。
func (s *aiPreReviewService) RunPreReview(
	ctx context.Context, tenantID uint64, taskID, knowledgeID string,
) ([]types.PreReviewFinding, error) {
	if tenantID == 0 {
		return nil, ErrAIPreReviewTenantMissing
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, ErrAIPreReviewTaskNotFound
	}

	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), aiPreReviewRunBudget)
	defer cancel()
	// 异步触发时调用方 ctx 可能只有部分值；显式锚定执行租户，避免下游
	// model/knowledge 仓储因缺租户而 panic 或跨租户读取。
	runCtx = types.WithExecutionTenant(runCtx, tenantID)

	task, err := s.tasks.GetByID(runCtx, tenantID, taskID)
	if err != nil {
		if errors.Is(err, repository.ErrApprovalTaskNotFound) {
			return nil, ErrAIPreReviewTaskNotFound
		}
		return nil, err
	}

	knowledgeID = strings.TrimSpace(knowledgeID)
	if knowledgeID == "" {
		knowledgeID = task.KnowledgeID
	}
	if knowledgeID != task.KnowledgeID {
		return nil, ErrAIPreReviewKnowledgeMismatch
	}

	now := s.now()
	run := &types.AIPreReviewRun{
		ID:            uuid.NewString(),
		TenantID:      tenantID,
		TaskID:        task.ID,
		KnowledgeID:   knowledgeID,
		Status:        string(types.PreReviewStatusRunning),
		Findings:      types.JSON(`[]`),
		PromptVersion: aiPreReviewPromptVersion,
		CreatedAt:     now,
	}
	if err := s.runs.CreateRun(runCtx, run); err != nil {
		return nil, fmt.Errorf("ai pre-review: create run: %w", err)
	}

	knowledge, err := s.knowledges.GetKnowledgeByID(runCtx, tenantID, knowledgeID)
	if err != nil {
		if errors.Is(err, repository.ErrKnowledgeNotFound) {
			s.aiPreReviewFailRun(runCtx, run, task.KBID, ErrAIPreReviewKnowledgeNotFound)
			return nil, ErrAIPreReviewKnowledgeNotFound
		}
		s.aiPreReviewFailRun(runCtx, run, task.KBID, err)
		return nil, err
	}

	body := s.aiPreReviewBodyText(runCtx, tenantID, knowledge)

	// ---- 规则类检查（必做，互不依赖，单点失败只让该项缺席）----
	findings := make([]types.PreReviewFinding, 0, 12)
	findings = append(findings, s.aiPreReviewCompleteness(runCtx, tenantID, knowledge, task)...)
	findings = append(findings, s.aiPreReviewTerminology(runCtx, tenantID, body)...)
	findings = append(findings, s.aiPreReviewDuplicates(runCtx, tenantID, knowledge)...)
	findings = append(findings, s.aiPreReviewSensitive(runCtx, tenantID, knowledge, body, run.ID)...)
	findings = append(findings, aiPreReviewFreshness(body, now)...)
	findings = append(findings, aiPreReviewExpression(body)...)
	findings = append(findings, aiPreReviewCitation(body)...)
	findings = append(findings, aiPreReviewConflictSignals(body)...)

	// ---- 模型类检查（可选，失败降级为 info）----
	modelVersion := ""
	modelFindings, version, modelErr := s.aiPreReviewModelChecks(runCtx, knowledge, body)
	modelVersion = version
	if modelErr != nil {
		// 降级：把不可用本身作为一条 info 提示，让审批人知道"引用/冲突两项
		// 这次只有规则线索"，而不是误以为模型已经检查过。
		findings = append(findings, types.PreReviewFinding{
			Check:    types.PreReviewCheckCitation,
			Severity: types.PreReviewSeverityInfo,
			Message:  "模型预审不可用，本次仅包含规则检查结果",
			Evidence: aiPreReviewTruncate(modelErr.Error(), aiPreReviewEvidenceSnippet),
			Source:   types.PreReviewSourceRules,
		})
	} else {
		findings = append(findings, modelFindings...)
	}

	findings = aiPreReviewFinalizeFindings(findings)

	finished := s.now()
	run.Status = string(types.PreReviewStatusCompleted)
	run.Findings = aiPreReviewFindingsJSON(findings)
	run.ModelVersion = aiPreReviewTruncate(modelVersion, aiPreReviewVersionFieldLimit)
	run.FinishedAt = &finished
	if err := s.runs.CompleteRun(runCtx, run); err != nil {
		logger.Warnf(runCtx, "[AIPreReview] persist completed run %s failed: %v", run.ID, err)
	}

	summary := types.SummarizePreReviewFindings(findings)
	recordKBActivity(runCtx, s.audit, tenantID, task.KBID,
		types.AuditActionAIPreReviewCompleted, "ai_pre_review_run", run.ID,
		types.AuditOutcomeSuccess, map[string]any{
			"task_id":        task.ID,
			"knowledge_id":   knowledgeID,
			"total":          summary.Total,
			"info":           summary.BySeverity[string(types.PreReviewSeverityInfo)],
			"warning":        summary.BySeverity[string(types.PreReviewSeverityWarning)],
			"blocker":        summary.BySeverity[string(types.PreReviewSeverityBlocker)],
			"max_severity":   summary.MaxSeverity,
			"model_version":  run.ModelVersion,
			"prompt_version": run.PromptVersion,
		})
	return findings, nil
}

// ListRuns 返回该任务的历史预审运行（最新在前）。
func (s *aiPreReviewService) ListRuns(
	ctx context.Context, tenantID uint64, taskID string, limit int,
) ([]*types.AIPreReviewRun, error) {
	if tenantID == 0 {
		return nil, ErrAIPreReviewTenantMissing
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, ErrAIPreReviewTaskNotFound
	}
	if limit <= 0 {
		limit = aiPreReviewRunListLimit
	}
	return s.runs.ListByTask(ctx, tenantID, taskID, limit)
}

// ============================================================================
// 规则类检查
// ============================================================================

// aiPreReviewCompleteness 检查必填字段（§6.5：完整性）。责任人取"用户元数据里
// 的 owner 类字段"或"KB 创建者"，两者都缺才算缺失 —— 提交人本身不作为责任人
// 兜底，否则这项检查永远命中不到真问题。
func (s *aiPreReviewService) aiPreReviewCompleteness(
	ctx context.Context, tenantID uint64, knowledge *types.Knowledge, task *types.ApprovalTask,
) []types.PreReviewFinding {
	findings := make([]types.PreReviewFinding, 0, 4)

	if strings.TrimSpace(knowledge.Title) == "" {
		findings = append(findings, types.PreReviewFinding{
			Check:      types.PreReviewCheckCompleteness,
			Severity:   types.PreReviewSeverityBlocker,
			Message:    "缺少标题：审批人无法从待办列表识别内容主题",
			Locator:    "title",
			Suggestion: "补充一个能说明对象与结论的标题（避免「文档1」这类占位名）",
			Source:     types.PreReviewSourceRules,
		})
	}

	if strings.TrimSpace(knowledge.Description) == "" {
		findings = append(findings, types.PreReviewFinding{
			Check:      types.PreReviewCheckCompleteness,
			Severity:   types.PreReviewSeverityWarning,
			Message:    "缺少描述/适用范围说明",
			Locator:    "description",
			Suggestion: "补充适用范围、生效对象与前提条件，便于读者判断是否适用于自己",
			Source:     types.PreReviewSourceRules,
		})
	}

	classification := types.ParseKnowledgeClassification(knowledge.Classification)
	if strings.TrimSpace(knowledge.Classification) == "" {
		findings = append(findings, types.PreReviewFinding{
			Check:      types.PreReviewCheckCompleteness,
			Severity:   types.PreReviewSeverityWarning,
			Message:    "缺少密级标注：默认按 internal 处理，可能低估数据敏感度",
			Locator:    "classification",
			Suggestion: "按 public/internal/secret/confidential 明确标注密级",
			Source:     types.PreReviewSourceRules,
		})
	} else if !classification.Valid() {
		findings = append(findings, types.PreReviewFinding{
			Check:      types.PreReviewCheckCompleteness,
			Severity:   types.PreReviewSeverityWarning,
			Message:    "密级取值不在白名单内：" + knowledge.Classification,
			Evidence:   knowledge.Classification,
			Locator:    "classification",
			Suggestion: "改为 public/internal/secret/confidential 之一",
			Source:     types.PreReviewSourceRules,
		})
	}

	if owner := s.aiPreReviewResolveOwner(ctx, tenantID, knowledge); owner == "" {
		finding := types.PreReviewFinding{
			Check:      types.PreReviewCheckCompleteness,
			Severity:   types.PreReviewSeverityWarning,
			Message:    "未找到内容责任人（owner/负责人）",
			Locator:    "owner",
			Suggestion: "在自定义元数据中补充 owner/责任人字段，或为知识库设置创建者",
			Source:     types.PreReviewSourceRules,
		}
		if task != nil && task.RequestedBy != "" {
			finding.Suggestion = "可把提交人 " + task.RequestedBy + " 指定为责任人，" +
				"或在自定义元数据中补充 owner/责任人字段"
		}
		findings = append(findings, finding)
	}

	// 有效期/复审周期：§6.5 把有效期列为完整性的一部分，缺失不阻断，但值得
	// 在审批时确认"这条什么时候该被复审"。
	if knowledge.ExpiresAt == nil && knowledge.ReviewCycleDays <= 0 {
		findings = append(findings, types.PreReviewFinding{
			Check:      types.PreReviewCheckCompleteness,
			Severity:   types.PreReviewSeverityInfo,
			Message:    "未设置有效期或复审周期",
			Locator:    "expires_at",
			Suggestion: "为易过期的内容设置 expires_at 或 review_cycle_days，纳入复审清单",
			Source:     types.PreReviewSourceRules,
		})
	}

	return findings
}

// aiPreReviewResolveOwner 依次取元数据 owner 类字段与 KB 创建者。
func (s *aiPreReviewService) aiPreReviewResolveOwner(
	ctx context.Context, tenantID uint64, knowledge *types.Knowledge,
) string {
	if knowledge == nil {
		return ""
	}
	if owner := aiPreReviewOwnerFromMetadata(knowledge.CustomMetadata); owner != "" {
		return owner
	}
	if s.kbs == nil || knowledge.KnowledgeBaseID == "" {
		return ""
	}
	kb, err := s.kbs.GetKnowledgeBaseByIDAndTenant(ctx, knowledge.KnowledgeBaseID, tenantID)
	if err != nil || kb == nil {
		return ""
	}
	return strings.TrimSpace(kb.CreatorID)
}

// aiPreReviewOwnerFromMetadata 从自定义元数据里找 owner/责任人。
func aiPreReviewOwnerFromMetadata(metadata types.JSON) string {
	if len(metadata) == 0 {
		return ""
	}
	var values map[string]interface{}
	if err := json.Unmarshal([]byte(metadata), &values); err != nil {
		return ""
	}
	for key, value := range values {
		if value == nil {
			continue
		}
		lower := strings.ToLower(strings.TrimSpace(key))
		if !strings.Contains(lower, "owner") &&
			lower != "责任人" && lower != "负责人" && lower != "主责人" && lower != "作者" {
			continue
		}
		if text := strings.TrimSpace(fmt.Sprint(value)); text != "" {
			return text
		}
	}
	return ""
}

// aiPreReviewTerminology 对比术语表：命中非标准别名、或同义词与标准名混用。
// 术语表读取走 GlossaryService.SearchTerms（空关键词即全量列表，v1 的术语表
// 体量很小，扫描上限 500 条）。
func (s *aiPreReviewService) aiPreReviewTerminology(
	ctx context.Context, tenantID uint64, body string,
) []types.PreReviewFinding {
	if s.glossary == nil || strings.TrimSpace(body) == "" {
		return nil
	}
	terms, err := s.glossary.SearchTerms(ctx, tenantID, "", "")
	if err != nil {
		logger.Warnf(ctx, "[AIPreReview] list glossary terms failed: %v", err)
		return nil
	}
	findings := make([]types.PreReviewFinding, 0, 4)
	scanned := 0
	for _, term := range terms {
		if term == nil || term.IsDeprecated() {
			continue
		}
		if scanned >= aiPreReviewTermScanLimit {
			break
		}
		scanned++
		standard := strings.TrimSpace(term.StandardName)
		if standard == "" {
			continue
		}
		aliases := aiPreReviewDecodeAliases(term.Aliases)
		hasStandard := strings.Contains(body, standard)
		for _, alias := range aliases {
			alias = strings.TrimSpace(alias)
			if alias == "" || alias == standard || !strings.Contains(body, alias) {
				continue
			}
			if hasStandard {
				// 同义词混用：标准名与别名同时出现，读者难以判断是否为同一概念。
				findings = append(findings, types.PreReviewFinding{
					Check:      types.PreReviewCheckTerminology,
					Severity:   types.PreReviewSeverityInfo,
					Message:    "同一概念混用了「" + standard + "」与「" + alias + "」",
					Evidence:   aiPreReviewSnippetAround(body, alias, standard),
					Locator:    "term:" + term.ID,
					Suggestion: "统一使用术语表标准名「" + standard + "」，别名只在检索层面扩展",
					Source:     types.PreReviewSourceRules,
				})
				break
			}
			findings = append(findings, types.PreReviewFinding{
				Check:      types.PreReviewCheckTerminology,
				Severity:   types.PreReviewSeverityWarning,
				Message:    "使用了非标准表述「" + alias + "」，术语表标准名为「" + standard + "」",
				Evidence:   aiPreReviewSnippetAround(body, alias, ""),
				Locator:    "term:" + term.ID,
				Suggestion: "替换为「" + standard + "」" + aiPreReviewDefinitionHint(term.Definition),
				Source:     types.PreReviewSourceRules,
			})
			break
		}
		if len(findings) >= aiPreReviewMaxModelFindings {
			break
		}
	}
	return findings
}

// aiPreReviewDecodeAliases 解析术语别名数组；损坏的单行只跳过。
func aiPreReviewDecodeAliases(raw types.JSON) []string {
	if len(raw) == 0 {
		return nil
	}
	var aliases []string
	if err := json.Unmarshal([]byte(raw), &aliases); err != nil {
		return nil
	}
	return aliases
}

// aiPreReviewDefinitionHint 把术语定义压成一句可读的补充提示。
func aiPreReviewDefinitionHint(definition string) string {
	definition = strings.TrimSpace(definition)
	if definition == "" {
		return ""
	}
	return "（术语表定义：" + aiPreReviewTruncate(definition, 60) + "）"
}

// aiPreReviewDuplicates 查同 KB 内的相似知识（§6.5：重复）。先用标题片段做
// title LIKE 召回，再用字符二元组相似度打分，命中 ≥2 条才算 duplicate。
func (s *aiPreReviewService) aiPreReviewDuplicates(
	ctx context.Context, tenantID uint64, knowledge *types.Knowledge,
) []types.PreReviewFinding {
	title := strings.TrimSpace(knowledge.Title)
	if title == "" || knowledge.KnowledgeBaseID == "" {
		return nil
	}
	fragment := aiPreReviewLikeFragment(title)
	if fragment == "" {
		return nil
	}
	page := &types.Pagination{Page: 1, PageSize: aiPreReviewDuplicateScanLimit}
	rows, _, err := s.knowledges.ListPagedKnowledgeByKnowledgeBaseID(
		ctx, tenantID, knowledge.KnowledgeBaseID, page,
		types.KnowledgeListFilter{Keyword: fragment},
	)
	if err != nil {
		logger.Warnf(ctx, "[AIPreReview] scan similar knowledge failed: %v", err)
		return nil
	}

	type scored struct {
		title string
		score float64
	}
	similar := make([]scored, 0, len(rows))
	for _, row := range rows {
		if row == nil || row.ID == knowledge.ID {
			continue
		}
		score := aiPreReviewTitleSimilarity(title, row.Title)
		if score < aiPreReviewTitleSimilarityThreshold {
			continue
		}
		similar = append(similar, scored{title: strings.TrimSpace(row.Title), score: score})
	}
	if len(similar) < 2 {
		return nil
	}
	sort.SliceStable(similar, func(i, j int) bool { return similar[i].score > similar[j].score })
	if len(similar) > aiPreReviewMaxDuplicateTitles {
		similar = similar[:aiPreReviewMaxDuplicateTitles]
	}
	titles := make([]string, 0, len(similar))
	for _, item := range similar {
		if item.title == "" {
			continue
		}
		titles = append(titles, item.title)
	}
	if len(titles) < 2 {
		return nil
	}
	return []types.PreReviewFinding{{
		Check:    types.PreReviewCheckDuplicate,
		Severity: types.PreReviewSeverityWarning,
		Message:  fmt.Sprintf("同知识库内有 %d 条标题高度相似的知识", len(titles)),
		Evidence: strings.Join(titles, "；"),
		Locator:  "kb:" + knowledge.KnowledgeBaseID,
		Suggestion: "确认是否为重复知识：能合并则合并，需要并存则说明差异（适用范围/版本/对象），" +
			"或标注替代关系",
		Source: types.PreReviewSourceRules,
	}}
}

// aiPreReviewSensitive 探测敏感内容（§6.5：敏感）。两条线索：
//   - DLPService.ScanText 的内置/自定义规则（手机号/身份证/银行卡/邮箱等）；
//   - 涉密标记与敏感业务字段关键词。
//
// 另附密级建议：密级越高，审批口径越严（与 review 的高风险路由一致）。
func (s *aiPreReviewService) aiPreReviewSensitive(
	ctx context.Context, tenantID uint64, knowledge *types.Knowledge, body, runID string,
) []types.PreReviewFinding {
	findings := make([]types.PreReviewFinding, 0, 3)
	classification := types.ParseKnowledgeClassification(knowledge.Classification)

	if s.dlp != nil && strings.TrimSpace(body) != "" {
		// ScanText 自带掩码与命中落库（dlp_findings），这里只取命中数做提示。
		// requestID 用运行 ID 前缀，便于把 DLP 命中反查回预审运行。
		_, hits, err := s.dlp.ScanText(
			ctx, tenantID,
			aiPreReviewTruncate("aipre-"+runID, 64),
			knowledge.ID,
			aiPreReviewTruncate(body, aiPreReviewBodyLimit),
		)
		if err != nil {
			logger.Warnf(ctx, "[AIPreReview] DLP scan failed for knowledge %s: %v", knowledge.ID, err)
		} else if hits > 0 {
			findings = append(findings, types.PreReviewFinding{
				Check:    types.PreReviewCheckSensitive,
				Severity: types.PreReviewSeverityWarning,
				Message: fmt.Sprintf(
					"正文疑似包含 %d 处个人信息/敏感信息（DLP 内置与自定义规则命中）", hits),
				Evidence:   "命中已按 DLP 规则掩码并记录在 dlp_findings",
				Locator:    "dlp",
				Suggestion: "确认是否必须留存：能去掉就脱敏，不能去掉则提升密级并限制可见范围",
				Source:     types.PreReviewSourceRules,
			})
		}
	}

	markers := aiPreReviewSensitiveMarkers(body)
	if len(markers) > 0 {
		findings = append(findings, types.PreReviewFinding{
			Check:    types.PreReviewCheckSensitive,
			Severity: types.PreReviewSeverityWarning,
			Message:  "正文出现涉密/敏感标记：" + strings.Join(markers, "、"),
			Evidence: aiPreReviewSnippetAround(body, markers[0], ""),
			Locator:  "marker",
			Suggestion: "核对密级标注与实际内容是否一致；涉密内容应按对应密级限制可见范围，" +
				"个人信息建议脱敏后用「见附件」引用",
			Source: types.PreReviewSourceRules,
		})
		if classification.Rank() <= types.KnowledgeClassificationInternal.Rank() {
			findings = append(findings, types.PreReviewFinding{
				Check:    types.PreReviewCheckSensitive,
				Severity: types.PreReviewSeverityBlocker,
				Message: "正文有涉密/敏感标记，但密级仅为「" + string(classification) + "」",
				Evidence: "涉密标记与 classification 不一致",
				Locator:  "classification",
				Suggestion: "把密级提升到 secret/confidential，或移除涉密内容后再提交；" +
					"不一致时审批人应要求作者说明",
				Source: types.PreReviewSourceRules,
			})
		}
	}

	if classification.AtLeast(types.KnowledgeClassificationSecret) {
		findings = append(findings, types.PreReviewFinding{
			Check:    types.PreReviewCheckSensitive,
			Severity: types.PreReviewSeverityInfo,
			Message:  "本文密级为「" + string(classification) + "」，属于高风险审批口径",
			Locator:  "classification",
			Suggestion: "高风险内容需要双人审批，请确认密级与授权范围（部门/项目组）已明确，" +
				"并确认没有越级外发的内容",
			Source: types.PreReviewSourceRules,
		})
	}
	return findings
}

// aiPreReviewSensitiveMarkers 返回正文命中的涉密/敏感标记词（去重，封顶 3 个）。
func aiPreReviewSensitiveMarkers(body string) []string {
	if strings.TrimSpace(body) == "" {
		return nil
	}
	var hits []string
	for _, marker := range aiPreReviewSensitiveMarkerWords {
		if strings.Contains(body, marker) {
			hits = append(hits, marker)
			if len(hits) >= 3 {
				break
			}
		}
	}
	return hits
}

// aiPreReviewSensitiveMarkerWords 是涉密与敏感业务字段的标记词表。
var aiPreReviewSensitiveMarkerWords = []string{
	"绝密", "机密", "涉密", "内部资料", "禁止外传", "仅限内部",
	"客户名单", "合同金额", "报价单", "身份证", "银行卡", "个人隐私", "薪酬",
}

// aiPreReviewDateRef 是正文里解析出的一个时间标记。
type aiPreReviewDateRef struct {
	Raw string
	At  time.Time
}

// aiPreReviewFreshness 查引用的时效（§6.5：时效）：正文里 YYYY-MM-DD /
// YYYY年(MM月(DD日)?) 形式的时间标记超过 2 年即提示；同时检查标准版本年份
// （GB/ISO/JGJ 等 -YYYY 后缀）是否过于陈旧。
func aiPreReviewFreshness(body string, now time.Time) []types.PreReviewFinding {
	if strings.TrimSpace(body) == "" {
		return nil
	}
	cutoff := now.AddDate(-aiPreReviewFreshnessYears, 0, 0)
	findings := make([]types.PreReviewFinding, 0, 2)

	var stale []aiPreReviewDateRef
	staleContextual := false
	var standardHits []string
	for _, line := range aiPreReviewLines(body) {
		for _, ref := range aiPreReviewDatesInLine(line) {
			if ref.At.After(cutoff) {
				continue
			}
			stale = append(stale, ref)
			if aiPreReviewDateContextPattern.MatchString(line) {
				staleContextual = true
			}
			if len(stale) >= 20 {
				break
			}
		}
		for _, match := range aiPreReviewStandardPattern.FindAllStringSubmatch(line, -1) {
			year, err := strconv.Atoi(match[3])
			if err != nil {
				continue
			}
			if year <= now.Year()-aiPreReviewStandardYears {
				standardHits = append(standardHits, strings.TrimSpace(match[0]))
				if len(standardHits) >= 3 {
					break
				}
			}
		}
	}

	if len(stale) > 0 {
		severity := types.PreReviewSeverityInfo
		if staleContextual {
			// 时间标记出现在"依据/参照/截至/版本"等语境里，过期风险实质更高。
			severity = types.PreReviewSeverityWarning
		}
		earliest := stale[0]
		for _, ref := range stale[1:] {
			if ref.At.Before(earliest.At) {
				earliest = ref
			}
		}
		evidence := make([]string, 0, 3)
		for i, ref := range stale {
			if i >= 3 {
				break
			}
			evidence = append(evidence, ref.Raw)
		}
		findings = append(findings, types.PreReviewFinding{
			Check:    types.PreReviewCheckFreshness,
			Severity: severity,
			Message: fmt.Sprintf(
				"正文有 %d 处超过 %d 年的时间标记（最早 %s）",
				len(stale), aiPreReviewFreshnessYears, earliest.At.Format("2006-01-02")),
			Evidence: strings.Join(evidence, "、"),
			Locator:  "date",
			Suggestion: "核对引用的法规/标准/产品版本是否仍然有效；已失效的请标注替代来源，" +
				"或改为「截至 YYYY 年」这类明确口径",
			Source: types.PreReviewSourceRules,
		})
	}

	if len(standardHits) > 0 {
		findings = append(findings, types.PreReviewFinding{
			Check:    types.PreReviewCheckFreshness,
			Severity: types.PreReviewSeverityWarning,
			Message: fmt.Sprintf(
				"引用的标准版本年份较早（早于 %d 年前）：%s",
				aiPreReviewStandardYears, strings.Join(standardHits, "、")),
			Evidence: strings.Join(standardHits, "、"),
			Locator:  "standard",
			Suggestion: "确认该标准是否已换版或被替代；未换版请在正文注明「现行有效」及查询来源",
			Source: types.PreReviewSourceRules,
		})
	}
	return findings
}

// aiPreReviewDatesInLine 解析单行里的日期标记（两种常见写法）。
func aiPreReviewDatesInLine(line string) []aiPreReviewDateRef {
	if line == "" {
		return nil
	}
	refs := make([]aiPreReviewDateRef, 0, 2)
	appendRef := func(raw, year, month, day string) {
		y, err := strconv.Atoi(year)
		if err != nil || y < 1900 || y > 2100 {
			return
		}
		m, d := 1, 1
		if month != "" {
			if parsed, err := strconv.Atoi(month); err == nil && parsed >= 1 && parsed <= 12 {
				m = parsed
			}
		}
		if day != "" {
			if parsed, err := strconv.Atoi(day); err == nil && parsed >= 1 && parsed <= 31 {
				d = parsed
			}
		}
		refs = append(refs, aiPreReviewDateRef{
			Raw: strings.TrimSpace(raw),
			At:  time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC),
		})
	}
	for _, match := range aiPreReviewDateSeparatedPattern.FindAllStringSubmatch(line, -1) {
		appendRef(match[0], match[1], match[2], match[3])
	}
	for _, match := range aiPreReviewDateChinesePattern.FindAllStringSubmatch(line, -1) {
		year, month, day := match[1], "", ""
		if len(match) > 2 {
			month = match[2]
		}
		if len(match) > 3 {
			day = match[3]
		}
		appendRef(match[0], year, month, day)
	}
	return refs
}

// aiPreReviewExpression 匹配绝对化用语（§6.5：表述），逐句给出修改建议。
// 只报第一处命中词/行，并封顶 6 条，避免整篇"必须"把清单刷满。
func aiPreReviewExpression(body string) []types.PreReviewFinding {
	if strings.TrimSpace(body) == "" {
		return nil
	}
	const maxExpressionFindings = 6
	findings := make([]types.PreReviewFinding, 0, maxExpressionFindings)
	extra := 0
	for index, line := range aiPreReviewLines(body) {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		word := ""
		for _, candidate := range aiPreReviewAbsoluteWords {
			if strings.Contains(trimmed, candidate) {
				word = candidate
				break
			}
		}
		if word == "" {
			continue
		}
		if len(findings) >= maxExpressionFindings {
			extra++
			continue
		}
		findings = append(findings, types.PreReviewFinding{
			Check:    types.PreReviewCheckExpression,
			Severity: types.PreReviewSeverityInfo,
			Message:  "绝对化表述「" + word + "」，缺少适用条件或例外",
			Evidence: aiPreReviewSnippet(trimmed, aiPreReviewEvidenceSnippet),
			Locator:  fmt.Sprintf("line:%d", index+1),
			Suggestion: "补充条件与例外（例如改为「在 X 情况下应当…；Y 情形除外」），" +
				"避免「" + word + "」这类不可执行的责任口径",
			Source: types.PreReviewSourceRules,
		})
	}
	if extra > 0 {
		findings = append(findings, types.PreReviewFinding{
			Check:      types.PreReviewCheckExpression,
			Severity:   types.PreReviewSeverityInfo,
			Message:    fmt.Sprintf("另有 %d 处绝对化表述未逐条列出", extra),
			Locator:    "line",
			Suggestion: "统一排查「必须/绝对/所有/永远」类表述，改为带条件的可执行口径",
			Source:     types.PreReviewSourceRules,
		})
	}
	return findings
}

// aiPreReviewCitation 是引用检查的规则部分（§6.5：引用）。断链、书名缺版本、
// 外部链接、含糊指引都在规则层可判，模型只做补充判断。
func aiPreReviewCitation(body string) []types.PreReviewFinding {
	if strings.TrimSpace(body) == "" {
		return nil
	}
	findings := make([]types.PreReviewFinding, 0, 4)

	// 1) Markdown 链接断链（空目标或 "#"）。
	broken := make([]string, 0, 3)
	for _, match := range aiPreReviewMarkdownLinkPattern.FindAllStringSubmatch(body, -1) {
		target := strings.TrimSpace(match[2])
		if target == "" || target == "#" {
			broken = append(broken, aiPreReviewSnippet(match[1], 40))
			if len(broken) >= 3 {
				break
			}
		}
	}
	if len(broken) > 0 {
		findings = append(findings, types.PreReviewFinding{
			Check:      types.PreReviewCheckCitation,
			Severity:   types.PreReviewSeverityWarning,
			Message:    fmt.Sprintf("发现 %d 处断链（链接目标为空或 #）", len(broken)),
			Evidence:   strings.Join(broken, "、"),
			Locator:    "link",
			Suggestion: "补上真实链接，或改为附件的可定位引用（文件名 + 章节号 + 版本）",
			Source:     types.PreReviewSourceRules,
		})
	}

	// 2) 书名号引用缺版本/年份：审批时无法判断依据是否为当前版本。
	missingVersion := make([]string, 0, 3)
	for _, match := range aiPreReviewBookTitlePattern.FindAllStringIndex(body, -1) {
		title := body[match[0]:match[1]]
		windowStart := match[1]
		windowEnd := windowStart + 40
		if windowEnd > len(body) {
			windowEnd = len(body)
		}
		window := body[windowStart:windowEnd]
		if aiPreReviewYearPattern.MatchString(title + window) {
			continue
		}
		missingVersion = append(missingVersion, title)
		if len(missingVersion) >= 3 {
			break
		}
	}
	if len(missingVersion) > 0 {
		findings = append(findings, types.PreReviewFinding{
			Check:    types.PreReviewCheckCitation,
			Severity: types.PreReviewSeverityInfo,
			Message:  fmt.Sprintf("有 %d 处《》引用未标注版本或年份", len(missingVersion)),
			Evidence: strings.Join(missingVersion, "、"),
			Locator:  "citation",
			Suggestion: "为引用补充版本号/发布年份/生效日期，并确认指向的是当前版本而非旧版",
			Source: types.PreReviewSourceRules,
		})
	}

	// 3) 外部链接：提醒留存快照与确认版本（不逐条列，避免刷屏）。
	if links := aiPreReviewExternalLinkPattern.FindAllString(body, 4); len(links) > 0 {
		findings = append(findings, types.PreReviewFinding{
			Check:    types.PreReviewCheckCitation,
			Severity: types.PreReviewSeverityInfo,
			Message: fmt.Sprintf(
				"正文引用了外部链接（至少 %d 个），外部内容可能已变更", len(links)),
			Evidence: strings.Join(links, "、"),
			Locator:  "url",
			Suggestion: "确认链接指向当前版本；条件允许时留存快照或改用内部受控来源，" +
				"避免审批依据随外站变动",
			Source: types.PreReviewSourceRules,
		})
	}

	// 4) 含糊指引：没有可定位依据的"见附件/如上所述"。
	vague := make([]string, 0, 2)
	for _, phrase := range aiPreReviewVagueReferenceWords {
		if strings.Contains(body, phrase) {
			vague = append(vague, phrase)
			if len(vague) >= 2 {
				break
			}
		}
	}
	if len(vague) > 0 {
		findings = append(findings, types.PreReviewFinding{
			Check:    types.PreReviewCheckCitation,
			Severity: types.PreReviewSeverityInfo,
			Message:  "出现缺少可定位依据的指代：" + strings.Join(vague, "、"),
			Locator:  "reference",
			Suggestion: "补上具体文件名、章节号或链接，让读者能直接找到依据",
			Source:     types.PreReviewSourceRules,
		})
	}
	return findings
}

// aiPreReviewConflictSignals 是冲突检查的规则线索（§6.5：冲突）。真正的语义
// 冲突判断交给模型；规则层只提示"这段在讲废止/替代"，提醒审批人对照现行制度。
func aiPreReviewConflictSignals(body string) []types.PreReviewFinding {
	if strings.TrimSpace(body) == "" {
		return nil
	}
	hits := make([]string, 0, 3)
	for _, phrase := range aiPreReviewConflictWords {
		if strings.Contains(body, phrase) {
			hits = append(hits, phrase)
			if len(hits) >= 3 {
				break
			}
		}
	}
	if len(hits) == 0 {
		return nil
	}
	return []types.PreReviewFinding{{
		Check:    types.PreReviewCheckConflict,
		Severity: types.PreReviewSeverityInfo,
		Message:  "正文出现废止/替代类表述：" + strings.Join(hits, "、"),
		Evidence: aiPreReviewSnippetAround(body, hits[0], ""),
		Locator:  "conflict",
		Suggestion: "与现行制度/SOP/官方答案逐条核对：被替代的旧条款是否已下架，" +
			"新旧口径是否会在同一问题上给出不同答案",
		Source: types.PreReviewSourceRules,
	}}
}

// ============================================================================
// 模型类检查（可选，失败降级）
// ============================================================================

// aiPreReviewModelChecks 让 chat 模型对引用/冲突做一次结构化输出调用。
// 返回 (findings, modelVersion, err)；err 非空表示本次模型检查缺席，调用方
// 降级为一条 info 提示。
func (s *aiPreReviewService) aiPreReviewModelChecks(
	ctx context.Context, knowledge *types.Knowledge, body string,
) ([]types.PreReviewFinding, string, error) {
	if s.models == nil {
		return nil, "", errAIPreReviewModelNotConfigured
	}
	// ListModels/GetChatModel 依赖 ctx 里的租户；异步路径可能只带了部分值，
	// 这里先确认，避免下游 MustTenantIDFromContext panic。
	if _, ok := types.TenantIDFromContext(ctx); !ok {
		return nil, "", errAIPreReviewModelNotConfigured
	}
	model, version, err := s.aiPreReviewChatModel(ctx)
	if err != nil {
		return nil, version, err
	}

	userPrompt := fmt.Sprintf(
		aiPreReviewUserPromptTemplate,
		strings.TrimSpace(knowledge.Title),
		strings.TrimSpace(knowledge.Classification),
		aiPreReviewTruncate(body, aiPreReviewLLMBodyLimit),
	)
	llmCtx, cancel := context.WithTimeout(ctx, aiPreReviewLLMBudget)
	defer cancel()
	llmCtx = types.WithLLMCallMetadata(llmCtx, "ai_pre_review", "")

	thinking := false
	response, err := model.Chat(llmCtx, []chat.Message{
		{Role: "system", Content: aiPreReviewSystemPrompt},
		{Role: "user", Content: userPrompt},
	}, &chat.ChatOptions{
		Temperature:         0.1,
		MaxCompletionTokens: 900,
		Thinking:            &thinking,
	})
	if err != nil {
		return nil, version, err
	}
	if response == nil {
		return nil, version, errors.New("model returned an empty response")
	}
	return aiPreReviewParseModelFindings(response.Content), version, nil
}

// aiPreReviewChatModel 选一个可用的 chat 模型：优先 KnowledgeQA 默认模型，
// 否则取第一个活跃的 KnowledgeQA 模型。
func (s *aiPreReviewService) aiPreReviewChatModel(
	ctx context.Context,
) (chat.Chat, string, error) {
	models, err := s.models.ListModels(ctx)
	if err != nil {
		return nil, "", err
	}
	var chosen *types.Model
	for _, model := range models {
		if model == nil || model.Type != types.ModelTypeKnowledgeQA ||
			model.Status != types.ModelStatusActive {
			continue
		}
		if model.IsDefault {
			chosen = model
			break
		}
		if chosen == nil {
			chosen = model
		}
	}
	if chosen == nil {
		return nil, "", errAIPreReviewModelNotConfigured
	}
	chatModel, err := s.models.GetChatModel(ctx, chosen.ID)
	if err != nil {
		return nil, "", err
	}
	return chatModel, aiPreReviewModelVersionLabel(chosen), nil
}

// aiPreReviewModelVersionLabel 生成落库用的模型版本标签（列宽 64）。带上
// 展示名是为了让审批人一眼看出是哪套模型给的判断。
func aiPreReviewModelVersionLabel(model *types.Model) string {
	if model == nil {
		return ""
	}
	label := strings.TrimSpace(model.ID)
	if name := strings.TrimSpace(model.DisplayName); name != "" {
		label += "/" + name
	} else if name := strings.TrimSpace(model.Name); name != "" {
		label += "/" + name
	}
	return aiPreReviewTruncate(label, aiPreReviewVersionFieldLimit)
}

// aiPreReviewModelFinding 是模型结构化输出的单条提示（只接受引用/冲突）。
type aiPreReviewModelFinding struct {
	Check      string `json:"check"`
	Severity   string `json:"severity"`
	Message    string `json:"message"`
	Evidence   string `json:"evidence"`
	Locator    string `json:"locator"`
	Suggestion string `json:"suggestion"`
}

// aiPreReviewModelResponse 是模型输出的外层结构。
type aiPreReviewModelResponse struct {
	Findings []aiPreReviewModelFinding `json:"findings"`
}

// aiPreReviewParseModelFindings 解析模型输出并做白名单过滤：只收 citation 与
// conflict，check 不在集合内的一律丢弃（防止模型自创检查项污染八项清单）。
func aiPreReviewParseModelFindings(raw string) []types.PreReviewFinding {
	payload := aiPreReviewExtractJSONObject(raw)
	if payload == "" {
		return nil
	}
	var parsed aiPreReviewModelResponse
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return nil
	}
	findings := make([]types.PreReviewFinding, 0, len(parsed.Findings))
	for _, item := range parsed.Findings {
		check := types.PreReviewCheck(strings.TrimSpace(item.Check))
		if check != types.PreReviewCheckCitation && check != types.PreReviewCheckConflict {
			continue
		}
		message := strings.TrimSpace(item.Message)
		if message == "" {
			continue
		}
		severity := types.PreReviewSeverity(strings.TrimSpace(item.Severity))
		if !severity.Valid() {
			severity = types.PreReviewSeverityInfo
		}
		findings = append(findings, types.PreReviewFinding{
			Check:      check,
			Severity:   severity,
			Message:    aiPreReviewTruncate(message, 200),
			Evidence:   aiPreReviewTruncate(strings.TrimSpace(item.Evidence), aiPreReviewEvidenceSnippet),
			Locator:    aiPreReviewTruncate(strings.TrimSpace(item.Locator), 80),
			Suggestion: aiPreReviewTruncate(strings.TrimSpace(item.Suggestion), 200),
			Source:     types.PreReviewSourceModel,
		})
		if len(findings) >= aiPreReviewMaxModelFindings {
			break
		}
	}
	return findings
}

// aiPreReviewExtractJSONObject 从模型输出里截出最外层 JSON 对象，容忍 ```json
// 围栏与前后说明文字。
func aiPreReviewExtractJSONObject(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start < 0 || end <= start {
		return ""
	}
	return trimmed[start : end+1]
}

// aiPreReviewSystemPrompt 是预审提示词（版本见 aiPreReviewPromptVersion）。
// 语气刻意保守：只指出"该看的地方"，不给通过/不通过，且必须引用原文。
const aiPreReviewSystemPrompt = `你是企业知识审核的预审助手。你的职责是把审批人应该重点检查的地方指出来，
而不是替审批人做决定。

硬性要求：
1. 只做两项检查：
   - citation（引用）：引用是否存在、是否指向当前版本、是否缺少可定位依据、是否断链；
   - conflict（冲突）：与现有制度、SOP、官方口径是否存在矛盾或重复定义。
2. 绝不输出"通过""不通过""可以发布""建议驳回"之类结论，只给风险提示。
3. 每条提示必须有依据：evidence 引用原文片段（可原样截取），locator 给出位置
   （字段名、章节号或行号），suggestion 给出可执行的修改建议。
4. 只依据用户提供的正文判断，不要臆测未提供的信息；没有把握就不要报。
5. 最多 6 条；没有发现就返回空数组。

只输出 JSON，不要任何解释文字，格式：
{"findings":[{"check":"citation|conflict","severity":"info|warning|blocker","message":"一句话结论","evidence":"原文片段","locator":"位置","suggestion":"修改建议"}]}`

// aiPreReviewUserPromptTemplate 是用户消息模板（标题/密级/正文三段）。
const aiPreReviewUserPromptTemplate = `文档标题：%s
密级：%s

正文：
%s

请按 citation / conflict 两项检查给出提示，只输出 JSON。`

// ============================================================================
// 内部助手
// ============================================================================

// aiPreReviewBodyText 拼出预审正文：标题 + 描述 + text/faq chunk 内容。
// chunk 读取失败只告警（规则类检查退化为只查标题/描述），不让整次运行失败。
func (s *aiPreReviewService) aiPreReviewBodyText(
	ctx context.Context, tenantID uint64, knowledge *types.Knowledge,
) string {
	if knowledge == nil {
		return ""
	}
	var builder strings.Builder
	if title := strings.TrimSpace(knowledge.Title); title != "" {
		builder.WriteString(title)
		builder.WriteString("\n")
	}
	if description := strings.TrimSpace(knowledge.Description); description != "" {
		builder.WriteString(description)
		builder.WriteString("\n")
	}
	if s.chunks != nil {
		chunks, err := s.chunks.ListChunksByKnowledgeIDAndTypes(
			ctx, tenantID, knowledge.ID,
			[]types.ChunkType{types.ChunkTypeText, types.ChunkTypeFAQ},
		)
		if err != nil {
			logger.Warnf(ctx, "[AIPreReview] list chunks for knowledge %s failed: %v", knowledge.ID, err)
		} else {
			for _, chunk := range chunks {
				if chunk == nil || strings.TrimSpace(chunk.Content) == "" {
					continue
				}
				builder.WriteString(chunk.Content)
				builder.WriteString("\n")
			}
		}
	}
	return aiPreReviewTruncate(builder.String(), aiPreReviewBodyLimit)
}

// aiPreReviewFailRun 落 failed 终态并写审计（best-effort）。预审失败不改变
// 审批任务状态：审批人照常可以裁决，这正是"AI 不替人决定"的工程化体现。
func (s *aiPreReviewService) aiPreReviewFailRun(
	ctx context.Context, run *types.AIPreReviewRun, kbID string, cause error,
) {
	if run == nil {
		return
	}
	finished := s.now()
	run.Status = string(types.PreReviewStatusFailed)
	run.ErrorMessage = aiPreReviewTruncate(cause.Error(), aiPreReviewErrorMessageLimit)
	run.FinishedAt = &finished
	if err := s.runs.CompleteRun(ctx, run); err != nil {
		logger.Warnf(ctx, "[AIPreReview] persist failed run %s failed: %v", run.ID, err)
	}
	recordKBActivity(ctx, s.audit, run.TenantID, kbID,
		types.AuditActionAIPreReviewFailed, "ai_pre_review_run", run.ID,
		types.AuditOutcomeFailed, map[string]any{
			"task_id":        run.TaskID,
			"knowledge_id":   run.KnowledgeID,
			"error":          run.ErrorMessage,
			"prompt_version": run.PromptVersion,
		})
}

// aiPreReviewFinalizeFindings 过滤非法项、去重、按档位/检查项排序并截断。
func aiPreReviewFinalizeFindings(findings []types.PreReviewFinding) []types.PreReviewFinding {
	valid := make([]types.PreReviewFinding, 0, len(findings))
	for _, finding := range findings {
		if !finding.Validate() {
			continue
		}
		if finding.Source == "" {
			finding.Source = types.PreReviewSourceRules
		}
		valid = append(valid, finding)
	}

	// 去重：同一检查项 + 同一结论 + 同一依据只保留一条（模型常与规则重复）。
	seen := make(map[string]struct{}, len(valid))
	deduped := valid[:0]
	for _, finding := range valid {
		key := string(finding.Check) + "\x00" + finding.Message + "\x00" +
			finding.Evidence + "\x00" + finding.Locator
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, finding)
	}

	order := make(map[string]int, len(types.PreReviewChecks))
	for index, check := range types.PreReviewChecks {
		order[string(check)] = index
	}
	sort.SliceStable(deduped, func(i, j int) bool {
		left, right := deduped[i], deduped[j]
		if left.Severity.Rank() != right.Severity.Rank() {
			return left.Severity.Rank() > right.Severity.Rank()
		}
		return order[string(left.Check)] < order[string(right.Check)]
	})

	if len(deduped) > aiPreReviewMaxFindings {
		deduped = deduped[:aiPreReviewMaxFindings]
	}
	return deduped
}

// aiPreReviewFindingsJSON 把提示列表序列化进 findings 列（JSONB）。序列化
// 失败兜底为空数组：结构体不会失败，兜底只为保证列约束不被破坏。
func aiPreReviewFindingsJSON(findings []types.PreReviewFinding) types.JSON {
	if len(findings) == 0 {
		return types.JSON(`[]`)
	}
	data, err := json.Marshal(findings)
	if err != nil {
		return types.JSON(`[]`)
	}
	return types.JSON(data)
}

// aiPreReviewLines 按行切正文并限制行数，用于定位（locator "line:N"）。
func aiPreReviewLines(body string) []string {
	if body == "" {
		return nil
	}
	lines := strings.Split(body, "\n")
	if len(lines) > aiPreReviewLinesLimit {
		lines = lines[:aiPreReviewLinesLimit]
	}
	return lines
}

// aiPreReviewSnippet 归一化空白并截断，作为证据片段。
func aiPreReviewSnippet(text string, limit int) string {
	normalized := strings.Join(strings.Fields(text), " ")
	return aiPreReviewTruncate(normalized, limit)
}

// aiPreReviewSnippetAround 找 needle 在 body 中的位置，截取上下文窗口作为证据；
// 找不到时退回 body 开头的片段（用于标记词这类正文级命中）。
func aiPreReviewSnippetAround(body, needle, extra string) string {
	if needle == "" {
		return aiPreReviewSnippet(body, aiPreReviewEvidenceSnippet)
	}
	index := strings.Index(body, needle)
	if index < 0 {
		return aiPreReviewSnippet(body, aiPreReviewEvidenceSnippet)
	}
	runes := []rune(body)
	runesBefore := len([]rune(body[:index]))
	start := runesBefore - 30
	if start < 0 {
		start = 0
	}
	end := runesBefore + 60
	if end > len(runes) {
		end = len(runes)
	}
	snippet := aiPreReviewSnippet(string(runes[start:end]), aiPreReviewEvidenceSnippet)
	if extra != "" && snippet != "" {
		snippet = snippet + "（含标准名「" + extra + "」）"
	}
	return snippet
}

// aiPreReviewTruncate 按 rune 截断，避免把多字节字符切坏。
func aiPreReviewTruncate(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// aiPreReviewLikeFragment 取标题前 12 个 rune 做 LIKE 召回片段：片段太长会
// 退化成精确匹配，太短会把整库的知识都召回。
func aiPreReviewLikeFragment(title string) string {
	runes := []rune(strings.TrimSpace(title))
	if len(runes) == 0 {
		return ""
	}
	if len(runes) > 12 {
		runes = runes[:12]
	}
	return string(runes)
}

// aiPreReviewTitleSimilarity 计算两个标题的字符二元组 Dice 系数（0..1）。
// 对中文标题比空白分词更稳，也不需要引入分词依赖。
func aiPreReviewTitleSimilarity(left, right string) float64 {
	leftGrams := aiPreReviewTitleGrams(left)
	rightGrams := aiPreReviewTitleGrams(right)
	if len(leftGrams) == 0 || len(rightGrams) == 0 {
		return 0
	}
	overlap := 0
	for gram, leftCount := range leftGrams {
		if rightCount, ok := rightGrams[gram]; ok {
			overlap += min(leftCount, rightCount)
		}
	}
	total := 0
	for _, count := range leftGrams {
		total += count
	}
	for _, count := range rightGrams {
		total += count
	}
	if total == 0 {
		return 0
	}
	return float64(2*overlap) / float64(total)
}

// aiPreReviewTitleGrams 把标题归一化为字符二元组计数（去空白与标点）。
func aiPreReviewTitleGrams(title string) map[string]int {
	normalized := make([]rune, 0, len(title))
	for _, r := range strings.ToLower(title) {
		if r == ' ' || r == '\t' || r == '\n' {
			continue
		}
		if !aiPreReviewTitleRuneOK(r) {
			continue
		}
		normalized = append(normalized, r)
	}
	if len(normalized) == 0 {
		return nil
	}
	if len(normalized) == 1 {
		return map[string]int{string(normalized): 1}
	}
	grams := make(map[string]int, len(normalized)-1)
	for i := 0; i+1 < len(normalized); i++ {
		grams[string(normalized[i:i+2])]++
	}
	return grams
}

// aiPreReviewTitleRuneOK 只保留字母、数字与 CJK，标点不参与相似度。
func aiPreReviewTitleRuneOK(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		return true
	case r >= 0x4E00 && r <= 0x9FFF: // CJK 统一表意文字
		return true
	case r >= 0x3400 && r <= 0x4DBF: // CJK 扩展 A
		return true
	default:
		return false
	}
}

// 预审使用的正则与词表（全部编译期固定，运行期无编译开销）。

var (
	// aiPreReviewDateSeparatedPattern 匹配 2024-03-01 / 2024/3/1 / 2024.03.01。
	aiPreReviewDateSeparatedPattern = regexp.MustCompile(
		`((?:19|20)\d{2})\s*[-/.]\s*(\d{1,2})\s*[-/.]\s*(\d{1,2})`)
	// aiPreReviewDateChinesePattern 匹配 2024年3月1日 / 2024年3月 / 2024年。
	aiPreReviewDateChinesePattern = regexp.MustCompile(
		`((?:19|20)\d{2})\s*年(?:\s*(\d{1,2})\s*月(?:\s*(\d{1,2})\s*日)?)?`)
	// aiPreReviewDateContextPattern 判断日期是否处在"依据/截至/版本"语境里。
	aiPreReviewDateContextPattern = regexp.MustCompile(
		`依据|根据|按照|参照|截至|截止|版本|生效|失效|标准|法规|制度|规范`)
	// aiPreReviewStandardPattern 匹配 GB/T 12345-2019 这类标准版本号。
	aiPreReviewStandardPattern = regexp.MustCompile(
		`(?i)\b(GB/T|GB|ISO/IEC|ISO|IEC|JGJ|HJ|DL/T)\s*([\d.\-/]+)\s*[-—–]\s*((?:19|20)\d{2})\b`)
	// aiPreReviewMarkdownLinkPattern 匹配 [文本](目标)。
	aiPreReviewMarkdownLinkPattern = regexp.MustCompile(`\[([^\]]{0,120})\]\(\s*([^)]{0,500}?)\s*\)`)
	// aiPreReviewBookTitlePattern 匹配《制度名》。
	aiPreReviewBookTitlePattern = regexp.MustCompile(`《[^》]{1,60}》`)
	// aiPreReviewYearPattern 用于判断引用附近是否已有年份/版本。
	aiPreReviewYearPattern = regexp.MustCompile(`(19|20)\d{2}|v\d+(\.\d+)+|第\s*\d+\s*版`)
	// aiPreReviewExternalLinkPattern 匹配裸外链。
	aiPreReviewExternalLinkPattern = regexp.MustCompile(`https?://[^\s()<>（）【】"]{1,300}`)
)

// aiPreReviewAbsoluteWords 是绝对化用语表（§6.5 表述检查）。
var aiPreReviewAbsoluteWords = []string{
	"必须", "绝对", "所有", "永远", "一定", "唯一", "百分之百", "100%",
	"绝不", "从不", "无条件", "一律", "零风险", "万无一失",
}

// aiPreReviewVagueReferenceWords 是缺少可定位依据的含糊指代词。
var aiPreReviewVagueReferenceWords = []string{
	"见附件", "见附表", "详见文档", "参见相关制度", "如上所述", "见上表",
}

// aiPreReviewConflictWords 是废止/替代类表述（冲突检查的规则线索）。
var aiPreReviewConflictWords = []string{
	"废止", "停止执行", "不再适用", "已取消", "已被替代", "取代", "冲突",
}
