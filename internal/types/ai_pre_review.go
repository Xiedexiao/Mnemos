package types

import (
	"encoding/json"
	"time"
)

// ============================================================================
// AI 预审（KnowAxis V3.0 §6.5）。
//
// AI 预审的定位是"把审批人应该看的地方指出来"，不是替审批人做决定。这套
// 模型因此刻意不带 verdict 字段：一次运行只产出 findings（检查清单 + 风险
// 提示），审批结论永远在 approval_tasks 上由人落。
//
// 八项检查（§6.5 表格）：
//
//	completeness 完整性 —— 必填字段/适用范围/责任人/有效期是否齐全
//	citation     引用   —— 是否存在、是否指向当前版本、是否缺少依据
//	conflict     冲突   —— 与现有制度/SOP/官方答案是否矛盾
//	sensitive    敏感   —— 涉密、个人信息、客户信息、价格、合同
//	expression   表述   —— 绝对化用语、缺少条件、责任不清
//	terminology  术语   —— 与术语表不一致、同义词混用
//	freshness    时效   —— 引用的法规/标准/产品版本是否已过期
//	duplicate    重复   —— 与已有知识高度相似
//
// 每条 finding 都必须可解释、可申诉、可关闭（§6.5「AI 预审的边界」）：
// evidence 存依据原文，locator 存位置，severity 只表达"该看多仔细"，
// 从不表达"该不该通过"。误报标记见 PreReviewFinding.Dismissed。
// ============================================================================

// PreReviewCheck 是八项检查的检查项标识。词表与 §6.5 表格一一对应；新增
// 检查项只能追加，因为 findings JSONB 里已有历史行按旧值存储。
type PreReviewCheck string

const (
	// PreReviewCheckCompleteness 完整性：必填字段、适用范围、责任人、有效期。
	PreReviewCheckCompleteness PreReviewCheck = "completeness"
	// PreReviewCheckCitation 引用：断链、旧版引用、无依据。
	PreReviewCheckCitation PreReviewCheck = "citation"
	// PreReviewCheckConflict 冲突：与现有制度/SOP/官方答案矛盾。
	PreReviewCheckConflict PreReviewCheck = "conflict"
	// PreReviewCheckSensitive 敏感：涉密、个人信息、客户信息、价格、合同。
	PreReviewCheckSensitive PreReviewCheck = "sensitive"
	// PreReviewCheckExpression 表述：绝对化用语、缺少条件、责任不清。
	PreReviewCheckExpression PreReviewCheck = "expression"
	// PreReviewCheckTerminology 术语：与术语表不一致、同义词混用。
	PreReviewCheckTerminology PreReviewCheck = "terminology"
	// PreReviewCheckFreshness 时效：引用的法规/标准/产品版本已过期。
	PreReviewCheckFreshness PreReviewCheck = "freshness"
	// PreReviewCheckDuplicate 重复：与已有知识高度相似。
	PreReviewCheckDuplicate PreReviewCheck = "duplicate"
)

// PreReviewChecks 是按 §6.5 表格顺序排列的全部检查项，供前端渲染"八项
// 检查"关卡进度（未命中的检查项也应显示为"已检查、无提示"）。
var PreReviewChecks = []PreReviewCheck{
	PreReviewCheckCompleteness,
	PreReviewCheckCitation,
	PreReviewCheckConflict,
	PreReviewCheckSensitive,
	PreReviewCheckExpression,
	PreReviewCheckTerminology,
	PreReviewCheckFreshness,
	PreReviewCheckDuplicate,
}

// Valid 报告检查项是否已知。
func (c PreReviewCheck) Valid() bool {
	switch c {
	case PreReviewCheckCompleteness, PreReviewCheckCitation, PreReviewCheckConflict,
		PreReviewCheckSensitive, PreReviewCheckExpression, PreReviewCheckTerminology,
		PreReviewCheckFreshness, PreReviewCheckDuplicate:
		return true
	default:
		return false
	}
}

// String 渲染检查项用于日志与提示文案。
func (c PreReviewCheck) String() string { return string(c) }

// PreReviewSeverity 是一条提示的风险档位。它决定"审批人该多花时间"，不
// 决定"过不过"：blocker 表示"这条必须先处理或明确豁免"，而不是"系统驳回"。
// 预审永远不自动推进审批状态机，任何档位都需要审批人显式确认（§6.5）。
type PreReviewSeverity string

const (
	// PreReviewSeverityInfo 信息：仅供参考，通常是改进建议。
	PreReviewSeverityInfo PreReviewSeverity = "info"
	// PreReviewSeverityWarning 提示：建议发布前处理，但不构成硬性障碍。
	PreReviewSeverityWarning PreReviewSeverity = "warning"
	// PreReviewSeverityBlocker 阻断级提示：强烈建议处理或留下豁免理由，
	// 审批人仍可基于专业判断继续裁决。
	PreReviewSeverityBlocker PreReviewSeverity = "blocker"
)

// Valid 报告档位是否已知。
func (s PreReviewSeverity) Valid() bool {
	switch s {
	case PreReviewSeverityInfo, PreReviewSeverityWarning, PreReviewSeverityBlocker:
		return true
	default:
		return false
	}
}

// String 渲染档位用于日志。
func (s PreReviewSeverity) String() string { return string(s) }

// Rank 给出档位的排序权重（info=1 < warning=2 < blocker=3），用于汇总时
// 取最高档与前端按严重度排序。
func (s PreReviewSeverity) Rank() int {
	switch s {
	case PreReviewSeverityBlocker:
		return 3
	case PreReviewSeverityWarning:
		return 2
	case PreReviewSeverityInfo:
		return 1
	default:
		return 0
	}
}

// PreReviewStatus 是一次预审运行的状态。running 是写入时的初值，终态为
// completed | failed；失败不是异常路径而是正常降级（模型不可用、素材缺失
// 都只让本次运行留痕，审批照常进行）。
type PreReviewStatus string

const (
	// PreReviewStatusRunning 运行中（CreateRun 落库时的初值）。
	PreReviewStatusRunning PreReviewStatus = "running"
	// PreReviewStatusCompleted 已完成：findings 是本次运行的全部结论。
	PreReviewStatusCompleted PreReviewStatus = "completed"
	// PreReviewStatusFailed 失败：error_message 记录原因，findings 可能为空。
	PreReviewStatusFailed PreReviewStatus = "failed"
)

// Valid 报告运行状态是否已知。
func (s PreReviewStatus) Valid() bool {
	switch s {
	case PreReviewStatusRunning, PreReviewStatusCompleted, PreReviewStatusFailed:
		return true
	default:
		return false
	}
}

// String 渲染运行状态用于日志。
func (s PreReviewStatus) String() string { return string(s) }

// AI 预审审计动作（`<area>.<event>` 点分风格，与本垂直的类型定义同文件，
// 沿用 review/glossary 垂直切片自包含的做法）。
const (
	// AuditActionAIPreReviewCompleted 预审运行完成（含 0 条提示：0 条也是
	// 结论，需要留痕"这八项都看过了"）。
	AuditActionAIPreReviewCompleted AuditAction = "aipre.completed"
	// AuditActionAIPreReviewFailed 预审运行失败（error_message 记录原因）。
	AuditActionAIPreReviewFailed AuditAction = "aipre.failed"
)

// 预审提示的来源标记：规则类检查不依赖模型，LLM 类检查在提示里如实标注，
// 让审批人能判断"这是规则命中还是模型判断"（模型判断更可能需要人工复核）。
const (
	// PreReviewSourceRules 由确定性规则产出（完整性/术语/重复/敏感/时效/表述）。
	PreReviewSourceRules = "rules"
	// PreReviewSourceModel 由 LLM 结构化输出产出（引用/冲突）。
	PreReviewSourceModel = "model"
)

// PreReviewFinding 是一条预审提示。设计目标是"可解释"：任何一条都必须能
// 回答"你为什么提醒我看这里"（evidence + locator）与"我该改什么"
// （suggestion）。三者在 v1 全部是自由文本，前端原样展示。
type PreReviewFinding struct {
	// Check 命中的检查项（八项之一，见 PreReviewCheck）。
	Check PreReviewCheck `json:"check"`
	// Severity 档位：info | warning | blocker。
	Severity PreReviewSeverity `json:"severity"`
	// Message 一句话结论，面向审批人（不含实现细节）。
	Message string `json:"message"`
	// Evidence 依据：命中的原文片段、规则名、相似知识的标题等。为空表示
	// 该提示没有可引用的原文（例如"字段缺失"）。
	Evidence string `json:"evidence,omitempty"`
	// Locator 位置：字段名（"description"）、chunk 序号（"chunk:3"）或行号
	// 区间（"title"），供前端定位高亮。自由格式，前端按前缀渲染。
	Locator string `json:"locator,omitempty"`
	// Suggestion 修改建议（§6.5 要求每条提示给建议）。
	Suggestion string `json:"suggestion,omitempty"`
	// Source rules | model：区分确定性规则与模型判断，便于审批人复核与后续
	// 统计误报率。
	Source string `json:"source,omitempty"`
	// Dismissed 是"误报"标记（§6.5 要求支持）。v1 由前端/后续版本回写，
	// 预审服务只负责按当前值展示，不主动改写。
	Dismissed bool `json:"dismissed,omitempty"`
}

// Validate 报告一条提示是否结构合法（落库前的最后一道防线）。Dismissed 与
// Source 不参与校验：前者是展示态，后者为空时前端按"未标注来源"渲染。
func (f PreReviewFinding) Validate() bool {
	if !f.Check.Valid() || !f.Severity.Valid() {
		return false
	}
	return f.Message != ""
}

// AIPreReviewRun 是一次 AI 预审运行。每次调用追加一行，不做原地更新除
// 终态以外的字段（CompleteRun / FailRun 只改 status/findings/error_message/
// finished_at）。历史运行保留：审批人可能想对比"改稿前后各提示了什么"。
type AIPreReviewRun struct {
	// ID of the run.
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// TenantID owning the run.
	TenantID uint64 `json:"tenant_id" gorm:"not null;index:idx_ai_pre_review_runs_tenant_task,priority:1"`
	// TaskID 指向 approval_tasks.id（本垂直不建外键，见迁移注释）。
	TaskID string `json:"task_id" gorm:"type:varchar(36);not null;index:idx_ai_pre_review_runs_tenant_task,priority:2"`
	// KnowledgeID 指向被预审的知识（便于脱离任务直接按知识追溯）。
	KnowledgeID string `json:"knowledge_id" gorm:"type:varchar(36);not null"`
	// Status running | completed | failed.
	Status string `json:"status" gorm:"type:varchar(16);not null;default:'running'"`
	// Findings 本次运行的全部提示（[]PreReviewFinding 的 JSON 数组）。
	// 用 types.JSON 而非结构体切片是为了与其它垂直的 JSONB 列保持同一
	// 存取方式（json.Marshal + types.JSON，见仓储实现）。
	Findings JSON `json:"findings" gorm:"type:jsonb;not null;default:'[]'"`
	// ModelVersion 产出 model 类提示的模型标识（含模型 ID 与名称）；未接
	// 模型时为空串。§6.5 要求记录模型版本以便回溯与评测。
	ModelVersion string `json:"model_version" gorm:"type:varchar(64);default:''"`
	// PromptVersion 预审提示词版本（内联常量 aiPreReviewPromptVersion），
	// 改动提示词必须改这个常量，否则历史行无法对应到当时的口径。
	PromptVersion string `json:"prompt_version" gorm:"type:varchar(64);default:''"`
	// ErrorMessage 失败原因（best-effort：抓不到就留空串，status 已足够）。
	ErrorMessage string `json:"error_message" gorm:"type:text;default:''"`
	// CreatedAt 运行开始时间。
	CreatedAt time.Time `json:"created_at"`
	// FinishedAt 运行结束时间；running 时为空。
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// TableName pins the GORM table name.
func (AIPreReviewRun) TableName() string { return "ai_pre_review_runs" }

// IsTerminal 报告该运行是否已结束（completed/failed）。
func (r *AIPreReviewRun) IsTerminal() bool {
	if r == nil {
		return false
	}
	return PreReviewStatus(r.Status) == PreReviewStatusCompleted ||
		PreReviewStatus(r.Status) == PreReviewStatusFailed
}

// FindingsList 解析 findings 列；空值或损坏时返回空切片而不是错误 —— 一条
// 坏行不该让整个历史列表 500（损坏已在落库前由 Validate 拦截，这里是读取
// 侧的兜底）。
func (r *AIPreReviewRun) FindingsList() []PreReviewFinding {
	if r == nil || len(r.Findings) == 0 {
		return nil
	}
	var findings []PreReviewFinding
	if err := json.Unmarshal([]byte(r.Findings), &findings); err != nil {
		return nil
	}
	return findings
}

// PreReviewSummary 是给审批人看的一次运行摘要：按检查项与档位计数，外加
// 最高档位。它服务"这个任务有没有需要我特别留意的"这一眼判断，永远不含
// 通过/不通过。
type PreReviewSummary struct {
	// Total 提示总数（含 dismissed 的误报，便于对比"提了但被忽略"）。
	Total int `json:"total"`
	// Dismissed 已标记为误报的条数。
	Dismissed int `json:"dismissed"`
	// BySeverity 各档位条数（info/warning/blocker，缺省为 0）。
	BySeverity map[string]int `json:"by_severity"`
	// ByCheck 各检查项条数（八项全列，未命中的项为 0，前端据此渲染完整
	// 检查清单）。
	ByCheck map[string]int `json:"by_check"`
	// MaxSeverity 本次运行的最高档位；无提示时为空串。
	MaxSeverity string `json:"max_severity"`
}

// SummarizePreReviewFindings 汇总提示列表。空列表返回"八项全 0、无最高档"
// 的摘要而不是 nil，调用方无需分支。
func SummarizePreReviewFindings(findings []PreReviewFinding) *PreReviewSummary {
	summary := &PreReviewSummary{
		Total:      len(findings),
		BySeverity: make(map[string]int, 3),
		ByCheck:    make(map[string]int, len(PreReviewChecks)),
	}
	for _, check := range PreReviewChecks {
		summary.ByCheck[string(check)] = 0
	}
	for _, severity := range []PreReviewSeverity{
		PreReviewSeverityInfo, PreReviewSeverityWarning, PreReviewSeverityBlocker,
	} {
		summary.BySeverity[string(severity)] = 0
	}
	maxRank := 0
	for _, finding := range findings {
		if finding.Severity.Valid() {
			summary.BySeverity[string(finding.Severity)]++
		}
		if finding.Check.Valid() {
			summary.ByCheck[string(finding.Check)]++
		}
		if finding.Dismissed {
			summary.Dismissed++
		}
		if rank := finding.Severity.Rank(); rank > maxRank {
			maxRank = rank
			summary.MaxSeverity = string(finding.Severity)
		}
	}
	return summary
}
