package types

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 黄金问题集 + 评测持久化（KnowAxis V3.0 §12.5/§14.8 的 v1 子集）。
//
// golden_sets 是租户内一份受控的"黄金问题集"：发布门禁的基准问答集。
// golden_questions 是集合内的题目（期望答案 + 期望证据引用 + 风险/时效
// 标注）；golden_set_runs 是一次评测的持久化结果（聚合指标 + 逐题得分），
// 让门禁判定可回溯、可对比。检索指标以 knowledge_id 为命中粒度；答案
// 质量/拒答正确性指标 v1 留空，待接完整问答链路（见
// service.GoldenSetService.RunEvaluation 的 TODO）。
// ============================================================================

// GoldenFreshnessExpectation 描述问题对内容时效的预期。
type GoldenFreshnessExpectation string

const (
	// GoldenFreshnessValid 期望命中的证据是有效的（默认）。
	GoldenFreshnessValid GoldenFreshnessExpectation = "valid"
	// GoldenFreshnessStaleOk 允许命中过期证据（staleness 不算失败）。
	GoldenFreshnessStaleOk GoldenFreshnessExpectation = "stale_ok"
	// GoldenFreshnessExpiredMustRefuse 过期内容必须拒答（供后续拒答评测）。
	GoldenFreshnessExpiredMustRefuse GoldenFreshnessExpectation = "expired_must_refuse"
)

// Valid 报告时效预期是否已知。
func (f GoldenFreshnessExpectation) Valid() bool {
	switch f {
	case GoldenFreshnessValid, GoldenFreshnessStaleOk, GoldenFreshnessExpiredMustRefuse:
		return true
	default:
		return false
	}
}

// String 渲染时效预期用于日志。
func (f GoldenFreshnessExpectation) String() string { return string(f) }

// GoldenRiskLevel 问题的风险等级（内容治理视角：答错的影响面）。
type GoldenRiskLevel string

const (
	// GoldenRiskLow 一般事实类问题。
	GoldenRiskLow GoldenRiskLevel = "low"
	// GoldenRiskMedium 涉及流程/操作类问题。
	GoldenRiskMedium GoldenRiskLevel = "medium"
	// GoldenRiskHigh 合规/安全/资金类问题（答错代价高）。
	GoldenRiskHigh GoldenRiskLevel = "high"
)

// Valid 报告风险等级是否已知。
func (r GoldenRiskLevel) Valid() bool {
	switch r {
	case GoldenRiskLow, GoldenRiskMedium, GoldenRiskHigh:
		return true
	default:
		return false
	}
}

// String 渲染风险等级用于日志。
func (r GoldenRiskLevel) String() string { return string(r) }

// GoldenSetRunStatus 一次评测运行的生命周期状态。
type GoldenSetRunStatus string

const (
	// GoldenSetRunStatusRunning 评测进行中（行已落库）。
	GoldenSetRunStatusRunning GoldenSetRunStatus = "running"
	// GoldenSetRunStatusCompleted 评测跑完（允许逐题失败，见 per_question）。
	GoldenSetRunStatusCompleted GoldenSetRunStatus = "completed"
	// GoldenSetRunStatusFailed 评测整体失败（如读题失败），metrics 里带 error。
	GoldenSetRunStatusFailed GoldenSetRunStatus = "failed"
)

// Valid 报告状态是否已知。
func (s GoldenSetRunStatus) Valid() bool {
	switch s {
	case GoldenSetRunStatusRunning, GoldenSetRunStatusCompleted, GoldenSetRunStatusFailed:
		return true
	default:
		return false
	}
}

// String 渲染状态用于日志。
func (s GoldenSetRunStatus) String() string { return string(s) }

// GoldenEvidenceRef 是期望证据的一条引用。检索指标以 KnowledgeID 为
// 命中粒度；ChunkID/Locator 是定位信息（chunk 内偏移、页码、锚点等），
// 供人工复核与后续的引用级评测。
type GoldenEvidenceRef struct {
	// KnowledgeID is the knowledge entry expected to be recalled.
	KnowledgeID string `json:"knowledge_id"`
	// ChunkID optionally pins the expected chunk.
	ChunkID string `json:"chunk_id,omitempty"`
	// Locator is a human-readable location hint (页码/标题/锚点).
	Locator string `json:"locator,omitempty"`
}

// GoldenSet is one reusable golden question set (V3.0 §12.5).
type GoldenSet struct {
	// ID of the set.
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// TenantID owning the set.
	TenantID uint64 `json:"tenant_id" gorm:"not null"`
	// Name is the tenant-unique display name (UNIQUE lives in migration 000147).
	Name string `json:"name" gorm:"type:varchar(128);not null"`
	// Domain groups sets by subject area (对齐术语表/知识条目的领域口径).
	Domain string `json:"domain" gorm:"type:varchar(64);default:''"`
	// KBID is the evaluation knowledge base the questions are searched
	// against (空表示尚未绑定，无法运行评测).
	KBID string `json:"kb_id" gorm:"type:varchar(36);default:''"`
	// Description explains what the set covers.
	Description string `json:"description" gorm:"default:''"`
	// CreatedBy records who created the set (audit identity).
	CreatedBy string `json:"created_by" gorm:"type:varchar(36);not null"`
	// CreatedAt of the set.
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt of the set (touched by GORM on Updates).
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the GORM table name.
func (GoldenSet) TableName() string { return "golden_sets" }

// BeforeCreate assigns an ID before persistence.
func (s *GoldenSet) BeforeCreate(_ *gorm.DB) error {
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	return nil
}

// GoldenQuestion is one golden question inside a set (V3.0 §12.5).
type GoldenQuestion struct {
	// ID of the question.
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// SetID owning the question (questions are scoped through their set;
	// the table carries no tenant_id of its own — 000147).
	SetID string `json:"set_id" gorm:"type:varchar(36);not null;index:idx_golden_questions_set"`
	// Question is the golden question text (集合内唯一，服务层按文本 upsert).
	Question string `json:"question" gorm:"not null"`
	// ExpectedAnswer is the reference answer (答案质量评测的基准; v1 只存).
	ExpectedAnswer string `json:"expected_answer" gorm:"default:''"`
	// ExpectedEvidence is a JSONB array of GoldenEvidenceRef: 检索指标的
	// ground truth. Marshalled with the local service helper, never inline
	// structs.
	ExpectedEvidence JSON `json:"expected_evidence" gorm:"type:jsonb;default:'[]'"`
	// SimulatedUserRole is the role of the simulated asker (权限边界评测用;
	// 取租户角色值, 默认 viewer).
	SimulatedUserRole string `json:"simulated_user_role" gorm:"type:varchar(16);default:'viewer'"`
	// RiskLevel marks how costly a wrong answer is.
	RiskLevel string `json:"risk_level" gorm:"type:varchar(16);default:'low'"`
	// FreshnessExpectation describes the freshness contract of the evidence.
	FreshnessExpectation string `json:"freshness_expectation" gorm:"type:varchar(16);default:'valid'"`
	// Enabled questions participate in RunEvaluation; disabled ones stay in
	// the set for reference.
	Enabled bool `json:"enabled" gorm:"not null;default:true"`
	// CreatedAt of the question.
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt of the question.
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the GORM table name.
func (GoldenQuestion) TableName() string { return "golden_questions" }

// BeforeCreate assigns an ID before persistence.
func (q *GoldenQuestion) BeforeCreate(_ *gorm.DB) error {
	if q.ID == "" {
		q.ID = uuid.NewString()
	}
	return nil
}

// GoldenSetRun is one persisted evaluation of a set (V3.0 §14.8).
type GoldenSetRun struct {
	// ID of the run.
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// SetID of the evaluated set.
	SetID string `json:"set_id" gorm:"type:varchar(36);not null"`
	// TenantID owning the run (runs are tenant-scoped for listing).
	TenantID uint64 `json:"tenant_id" gorm:"not null"`
	// TriggeredBy records who started the run (audit identity).
	TriggeredBy string `json:"triggered_by" gorm:"type:varchar(36);not null"`
	// Status: running | completed | failed.
	Status string `json:"status" gorm:"type:varchar(16);not null;default:'running'"`
	// Metrics is a JSONB GoldenRunMetrics payload (聚合指标).
	Metrics JSON `json:"metrics" gorm:"type:jsonb;default:'{}'"`
	// PerQuestion is a JSONB array of GoldenQuestionResult (逐题得分与缺口).
	PerQuestion JSON `json:"per_question" gorm:"type:jsonb;default:'[]'"`
	// StartedAt is when the run row was created.
	StartedAt time.Time `json:"started_at"`
	// FinishedAt is when the run reached completed/failed (null while running).
	FinishedAt *time.Time `json:"finished_at"`
	// CreatedAt of the run.
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt of the run.
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the GORM table name.
func (GoldenSetRun) TableName() string { return "golden_set_runs" }

// BeforeCreate assigns an ID before persistence.
func (r *GoldenSetRun) BeforeCreate(_ *gorm.DB) error {
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	return nil
}

// GoldenSetUpdate carries the mutable fields of a set. Nil fields keep the
// current value; pointer fields distinguish "not provided" from "".
type GoldenSetUpdate struct {
	// Name renames the set (nil keeps; renames re-check tenant uniqueness).
	Name *string `json:"name,omitempty"`
	// Domain replaces the domain tag (nil keeps).
	Domain *string `json:"domain,omitempty"`
	// KBID re-binds the evaluation knowledge base (nil keeps).
	KBID *string `json:"kb_id,omitempty"`
	// Description replaces the description (nil keeps).
	Description *string `json:"description,omitempty"`
}

// GoldenQuestionUpdate carries the mutable fields of a question. The question
// text itself is immutable — 文本即题目身份（批量导入按文本 upsert），改题
// 走"删旧 + 导入新"。
type GoldenQuestionUpdate struct {
	// ExpectedAnswer replaces the reference answer (nil keeps).
	ExpectedAnswer *string `json:"expected_answer,omitempty"`
	// ExpectedEvidence replaces the evidence refs (nil keeps).
	ExpectedEvidence *[]GoldenEvidenceRef `json:"expected_evidence,omitempty"`
	// SimulatedUserRole replaces the simulated asker role (nil keeps).
	SimulatedUserRole *string `json:"simulated_user_role,omitempty"`
	// RiskLevel replaces the risk level (nil keeps).
	RiskLevel *string `json:"risk_level,omitempty"`
	// FreshnessExpectation replaces the freshness expectation (nil keeps).
	FreshnessExpectation *string `json:"freshness_expectation,omitempty"`
	// Enabled flips question participation in runs (nil keeps).
	Enabled *bool `json:"enabled,omitempty"`
}

// GoldenQuestionImportResult reports what one batch import did (upsert by
// question text: 命中文本即更新，未命中即插入).
type GoldenQuestionImportResult struct {
	// Total is the number of questions in the batch.
	Total int `json:"total"`
	// Inserted is how many new questions were created.
	Inserted int `json:"inserted"`
	// Updated is how many existing questions were overwritten.
	Updated int `json:"updated"`
}

// GoldenRunMetrics is the JSONB payload of GoldenSetRun.Metrics.
type GoldenRunMetrics struct {
	// Top5Hit is the fraction of scorable questions whose top-5 retrieval
	// contains at least one expected knowledge_id — the v1 gate metric.
	Top5Hit float64 `json:"top5_hit"`
	// RecallAt5 averages per-question |expected ∩ top5| / |expected|.
	RecallAt5 float64 `json:"recall_at_5"`
	// NDCG is the average per-question NDCG@5 with binary relevance.
	NDCG float64 `json:"ndcg"`
	// MRR is the average per-question reciprocal rank of the first hit.
	MRR float64 `json:"mrr"`
	// AnswerAccuracy 是答案质量指标：对照 expected_answer 的 token 级
	// F1（见 types.AnswerSimilarity）。nil = 本次没有可判分的题
	// （所有题都没写期望答案），与"评测了但得 0 分"区分开。
	AnswerAccuracy *float64 `json:"answer_accuracy"`
	// RefusalCorrectness 是拒答正确率：freshness_expectation=
	// expired_must_refuse 的题是否确实拒答，加上其余题是否没有误拒。
	// 两者都是预期，因此只要存在可判分的题就有分母；nil 仅表示本次一条
	// 都判不了（全部检索失败）。
	RefusalCorrectness *float64 `json:"refusal_correctness"`
	// OverPermissionRecall 是越权召回率：以 viewer 身份检索时，命中集合里
	// 出现 secret/confidential 文档的比例。越高越危险，0 是唯一合格值。
	OverPermissionRecall float64 `json:"over_permission_recall"`
	// ExpiredMisuseRate 是过期误用率：期望时效为 valid 的题里，命中集合
	// 包含过期/已替换文档的题数占比。
	ExpiredMisuseRate float64 `json:"expired_misuse_rate"`
	// ScorableQuestions / ScorableAnswers / ScorableRefusals break the
	// denominators down so a reported rate can be audited.
	ScorableQuestions int `json:"scorable_questions"`
	ScorableAnswers   int `json:"scorable_answers"`
	ScorableRefusals  int `json:"scorable_refusals"`
	// Pass is the release-gate verdict: Top5Hit ≥ threshold, no
	// over-permission recall, and — when the sets carry the expectations —
	// the answer/refusal rates above their thresholds.
	Pass bool `json:"pass"`
	// Error carries the failure reason on status=failed runs.
	Error string `json:"error,omitempty"`
}

// GoldenQuestionResult is one entry of GoldenSetRun.PerQuestion.
type GoldenQuestionResult struct {
	// QuestionID of the scored question.
	QuestionID string `json:"question_id"`
	// Question text (快照，便于离线阅读报告).
	Question string `json:"question"`
	// ExpectedKnowledgeIDs is the distinct knowledge_id ground truth.
	ExpectedKnowledgeIDs []string `json:"expected_knowledge_ids"`
	// Top5Hit reports whether any expected knowledge_id appeared in top-5.
	Top5Hit bool `json:"top5_hit"`
	// FirstHitRank is the 1-based rank of the first hit (0 = 无命中).
	FirstHitRank int `json:"first_hit_rank"`
	// RecallAt5 is |expected ∩ top5| / |expected| for this question.
	RecallAt5 float64 `json:"recall_at_5"`
	// MRR is the reciprocal rank of the first hit for this question.
	MRR float64 `json:"mrr"`
	// NDCG@5 with binary relevance for this question.
	NDCG float64 `json:"ndcg"`
	// RefusalExpected reports whether the question must be refused.
	RefusalExpected bool `json:"refusal_expected"`
	// Refused reports whether the §13.5 matrix refused this question.
	Refused bool `json:"refused"`
	// AnswerSimilarity is the token-level F1 against expected_answer; nil when
	// the question carries no reference answer.
	AnswerSimilarity *float64 `json:"answer_similarity"`
	// MissingKnowledgeIDs lists expected knowledge that did NOT appear in
	// top-5 (缺口，供补语料/改召回).
	MissingKnowledgeIDs []string `json:"missing_knowledge_ids"`
	// Skipped marks questions without expected evidence (无法判检索, 不计入
	// 聚合分母).
	Skipped bool `json:"skipped"`
	// Error carries a per-question search failure (单题失败不拖垮整场跑批).
	Error string `json:"error,omitempty"`
}
