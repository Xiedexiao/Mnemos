package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/application/service/metric"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Golden set audit actions (V3.0 §12.5/§14.8: 发布门禁是内容治理的一等
// 事件——谁建了基准集、哪次评测给出了放行结论都要可回溯)。v1 词表只有
// created/evaluated 两个动作；update/delete/import 暂不落审计（词表后续
// 扩展时再加，避免把既有审计消费方灌爆）。
const (
	auditActionGoldenSetCreated   = "goldenset.created"
	auditActionGoldenSetEvaluated = "goldenset.evaluated"
)

// maxGoldenSetNameLen mirrors the migration's varchar(128).
const maxGoldenSetNameLen = 128

// DefaultGateTop5Hit is the release-gate floor: a run passes when the
// fraction of scorable questions whose top-5 retrieval hits at least one
// expected knowledge_id reaches 90%. v1 keeps it a constant so the gate
// semantics stay reviewable in code; overriding it from config (e.g. a
// cfg.Evaluation.GateTop5Hit field on config.Config, resolved in
// NewGoldenSetService) is the intended v2 extension — the constant is
// exported exactly for that wiring.
const DefaultGateTop5Hit = 0.9

// goldenSearchTopK is the retrieval depth of the v1 evaluation: the gate is
// a top-5 hit gate, so HybridSearch is asked for exactly 5 chunks.
const goldenSearchTopK = 5

// goldenThresholds comments the SearchParams thresholds: both stay at 0 so
// the gate measures rank quality against the full candidate pool, never the
// deployment's threshold tuning (调阈值掩盖召回缺口就失去门禁意义).
const (
	goldenVectorThreshold  = 0.0
	goldenKeywordThreshold = 0.0
)

// goldenSetService implements interfaces.GoldenSetService.
type goldenSetService struct {
	repo        interfaces.GoldenSetRepository
	kbService   interfaces.KnowledgeBaseService
	audit       interfaces.AuditLogService
	now         func() time.Time
	gateTop5Hit float64 // 常量注入点：v1 固定 DefaultGateTop5Hit，v2 可从 config 覆盖
}

// NewGoldenSetService constructs the golden set service. knowledgeBaseService
// backs RunEvaluation's retrieval scoring (set.kb_id is searched via
// HybridSearch); audit is optional (nil disables audit logging, same policy
// as the other verticals).
func NewGoldenSetService(
	repo interfaces.GoldenSetRepository,
	knowledgeBaseService interfaces.KnowledgeBaseService,
	audit interfaces.AuditLogService,
) interfaces.GoldenSetService {
	return &goldenSetService{
		repo:        repo,
		kbService:   knowledgeBaseService,
		audit:       audit,
		now:         time.Now,
		gateTop5Hit: DefaultGateTop5Hit,
	}
}

// ============================================================================
// JSON helpers. 审计/指标 JSON 统一走 json.Marshal + types.JSON；助手名与
// lifecycle/feedback/glossary 的助手刻意不同名，各垂直独立维护。
// ============================================================================

// goldenEvidenceJSON marshals evidence refs into the JSONB payload, falling
// back to an empty array on failure so the default-valued column always
// holds a JSON array.
func goldenEvidenceJSON(refs []types.GoldenEvidenceRef) types.JSON {
	data, err := json.Marshal(refs)
	if err != nil {
		return types.JSON([]byte("[]"))
	}
	return types.JSON(data)
}

// goldenMetricsJSON marshals the aggregate metrics payload, falling back to
// an empty object so a logging-shaped marshal error never blocks a run.
func goldenMetricsJSON(v interface{}) types.JSON {
	data, err := json.Marshal(v)
	if err != nil {
		return types.JSON([]byte("{}"))
	}
	return types.JSON(data)
}

// goldenPerQuestionJSON marshals the per-question result array, falling back
// to an empty array so the default-valued column always holds a JSON array.
func goldenPerQuestionJSON(v interface{}) types.JSON {
	data, err := json.Marshal(v)
	if err != nil {
		return types.JSON([]byte("[]"))
	}
	return types.JSON(data)
}

// parseGoldenEvidence decodes an evidence JSONB payload tolerantly: a
// malformed row degrades to "no evidence" (the question is then skipped from
// retrieval scoring) instead of failing the run.
func parseGoldenEvidence(payload types.JSON) []types.GoldenEvidenceRef {
	if len(payload) == 0 {
		return nil
	}
	var refs []types.GoldenEvidenceRef
	if err := json.Unmarshal(payload, &refs); err != nil {
		return nil
	}
	return refs
}

// distinctKnowledgeIDs returns the distinct, order-stable knowledge_ids of
// the evidence refs (entries without a knowledge_id are dropped).
func distinctKnowledgeIDs(refs []types.GoldenEvidenceRef) []string {
	seen := make(map[string]struct{}, len(refs))
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		id := strings.TrimSpace(ref.KnowledgeID)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

// ============================================================================
// Set CRUD
// ============================================================================

// CreateSet creates one set. New sets may reference an evaluation KB but do
// not have to (未绑定的集合可以先攒题).
func (s *goldenSetService) CreateSet(ctx context.Context, set *types.GoldenSet) (*types.GoldenSet, error) {
	if set == nil {
		return nil, apperrors.NewBadRequestError("set body is required")
	}
	set.Name = strings.TrimSpace(set.Name)
	if set.Name == "" {
		return nil, apperrors.NewBadRequestError("name is required")
	}
	if utf8.RuneCountInString(set.Name) > maxGoldenSetNameLen {
		return nil, apperrors.NewBadRequestError("name is too long (max 128 chars)")
	}
	if set.ID == "" {
		set.ID = uuid.NewString()
	}
	if existing, err := s.repo.GetSetByName(ctx, set.TenantID, set.Name); err == nil && existing != nil {
		return nil, apperrors.NewBadRequestError("set name already exists in this workspace")
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if err := s.repo.CreateSet(ctx, set); err != nil {
		return nil, err
	}
	s.logAudit(ctx, set.TenantID, set.CreatedBy, auditActionGoldenSetCreated,
		"golden_set", set.ID, map[string]interface{}{
			"name":   set.Name,
			"domain": set.Domain,
			"kb_id":  set.KBID,
		})
	return set, nil
}

// UpdateSet patches the mutable fields of one set. Renames re-check the
// tenant-unique name (excluding self).
func (s *goldenSetService) UpdateSet(
	ctx context.Context, tenantID uint64, setID string,
	input *types.GoldenSetUpdate, actorID string,
) (*types.GoldenSet, error) {
	if input == nil {
		return nil, apperrors.NewBadRequestError("update body is required")
	}
	set, err := s.loadSet(ctx, tenantID, setID)
	if err != nil {
		return nil, err
	}
	if input.Name != nil {
		name := strings.TrimSpace(*input.Name)
		if name == "" {
			return nil, apperrors.NewBadRequestError("name cannot be empty")
		}
		if utf8.RuneCountInString(name) > maxGoldenSetNameLen {
			return nil, apperrors.NewBadRequestError("name is too long (max 128 chars)")
		}
		if name != set.Name {
			if existing, err := s.repo.GetSetByName(ctx, tenantID, name); err == nil && existing != nil && existing.ID != setID {
				return nil, apperrors.NewBadRequestError("set name already exists in this workspace")
			} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, err
			}
		}
		set.Name = name
	}
	if input.Domain != nil {
		set.Domain = strings.TrimSpace(*input.Domain)
	}
	if input.KBID != nil {
		set.KBID = strings.TrimSpace(*input.KBID)
	}
	if input.Description != nil {
		set.Description = *input.Description
	}
	if err := s.repo.UpdateSet(ctx, set); err != nil {
		return nil, err
	}
	return set, nil
}

// DeleteSet removes one set with its questions and runs (级联删除在仓储层
// 事务内完成).
func (s *goldenSetService) DeleteSet(ctx context.Context, tenantID uint64, setID, actorID string) error {
	set, err := s.loadSet(ctx, tenantID, setID)
	if err != nil {
		return err
	}
	return s.repo.DeleteSet(ctx, tenantID, set.ID)
}

// GetSet resolves one set by ID within the tenant.
func (s *goldenSetService) GetSet(ctx context.Context, tenantID uint64, setID string) (*types.GoldenSet, error) {
	return s.loadSet(ctx, tenantID, setID)
}

// ListSets lists the tenant's sets; domain is an optional filter.
func (s *goldenSetService) ListSets(ctx context.Context, tenantID uint64, domain string) ([]*types.GoldenSet, error) {
	return s.repo.ListSets(ctx, tenantID, strings.TrimSpace(domain))
}

// ============================================================================
// Questions
// ============================================================================

// ImportQuestions batch-upserts questions by exact (trimmed) question text:
// hit = overwrite the mutable fields from the payload, miss = insert. v1
// matches per row with FindQuestionByText (导入批量通常在几十题量级，逐条
// 匹配足够；题目表无文本唯一约束，服务层匹配是唯一权威).
func (s *goldenSetService) ImportQuestions(
	ctx context.Context, tenantID uint64, setID string,
	input []*types.GoldenQuestion, actorID string,
) (*types.GoldenQuestionImportResult, error) {
	if len(input) == 0 {
		return nil, apperrors.NewBadRequestError("questions array is required")
	}
	set, err := s.loadSet(ctx, tenantID, setID)
	if err != nil {
		return nil, err
	}

	result := &types.GoldenQuestionImportResult{Total: len(input)}
	inserts := make([]*types.GoldenQuestion, 0, len(input))
	for _, item := range input {
		if item == nil {
			return nil, apperrors.NewBadRequestError("questions array contains a null entry")
		}
		question := strings.TrimSpace(item.Question)
		if question == "" {
			return nil, apperrors.NewBadRequestError("question text is required")
		}
		role, err := normalizeGoldenRole(item.SimulatedUserRole)
		if err != nil {
			return nil, err
		}
		risk := types.GoldenRiskLevel(strings.TrimSpace(item.RiskLevel))
		if risk == "" {
			risk = types.GoldenRiskLow
		}
		if !risk.Valid() {
			return nil, apperrors.NewBadRequestError("risk_level must be low, medium or high")
		}
		freshness := types.GoldenFreshnessExpectation(strings.TrimSpace(item.FreshnessExpectation))
		if freshness == "" {
			freshness = types.GoldenFreshnessValid
		}
		if !freshness.Valid() {
			return nil, apperrors.NewBadRequestError(
				"freshness_expectation must be valid, stale_ok or expired_must_refuse")
		}
		refs := parseGoldenEvidence(item.ExpectedEvidence)
		for _, ref := range refs {
			if strings.TrimSpace(ref.KnowledgeID) == "" {
				return nil, apperrors.NewBadRequestError(
					"expected_evidence entries need a knowledge_id")
			}
		}

		existing, err := s.repo.FindQuestionByText(ctx, set.ID, question)
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		if existing != nil {
			// 命中文本即整行覆盖可变字段（upsert 语义：以最新导入为准）。
			existing.ExpectedAnswer = item.ExpectedAnswer
			existing.ExpectedEvidence = goldenEvidenceJSON(refs)
			existing.SimulatedUserRole = role
			existing.RiskLevel = string(risk)
			existing.FreshnessExpectation = string(freshness)
			// 导入一律重新启用：enabled 是运行开关，需要禁用走
			// UpdateQuestion（避免 bool 零值把"没传 enabled"误判成禁用）。
			existing.Enabled = true
			if err := s.repo.UpdateQuestion(ctx, existing); err != nil {
				return nil, err
			}
			result.Updated++
			continue
		}
		inserts = append(inserts, &types.GoldenQuestion{
			SetID:                set.ID,
			Question:             question,
			ExpectedAnswer:       item.ExpectedAnswer,
			ExpectedEvidence:     goldenEvidenceJSON(refs),
			SimulatedUserRole:    role,
			RiskLevel:            string(risk),
			FreshnessExpectation: string(freshness),
			Enabled:              true,
		})
	}
	if err := s.repo.CreateQuestions(ctx, inserts); err != nil {
		return nil, err
	}
	result.Inserted = len(inserts)
	return result, nil
}

// normalizeGoldenRole defaults and validates the simulated asker role
// (取租户角色值；权限边界评测是后续阶段，v1 只做取值合法化).
func normalizeGoldenRole(role string) (string, error) {
	role = strings.TrimSpace(role)
	if role == "" {
		return string(types.TenantRoleViewer), nil
	}
	if !types.TenantRole(role).IsValid() {
		return "", apperrors.NewBadRequestError(
			"simulated_user_role must be one of the tenant roles (viewer/contributor/admin/owner)")
	}
	return role, nil
}

// ListQuestions lists a set's questions (含 disabled，前端标灰展示).
func (s *goldenSetService) ListQuestions(ctx context.Context, tenantID uint64, setID string) ([]*types.GoldenQuestion, error) {
	set, err := s.loadSet(ctx, tenantID, setID)
	if err != nil {
		return nil, err
	}
	return s.repo.ListQuestions(ctx, set.ID, false)
}

// UpdateQuestion patches one question. The question text is immutable:
// 文本即题目身份（批量导入按文本 upsert），改题走"删旧 + 导入新"。
func (s *goldenSetService) UpdateQuestion(
	ctx context.Context, tenantID uint64, setID, questionID string,
	input *types.GoldenQuestionUpdate, actorID string,
) (*types.GoldenQuestion, error) {
	if input == nil {
		return nil, apperrors.NewBadRequestError("update body is required")
	}
	set, err := s.loadSet(ctx, tenantID, setID)
	if err != nil {
		return nil, err
	}
	question, err := s.loadQuestion(ctx, set.ID, questionID)
	if err != nil {
		return nil, err
	}
	if input.ExpectedAnswer != nil {
		question.ExpectedAnswer = *input.ExpectedAnswer
	}
	if input.ExpectedEvidence != nil {
		for _, ref := range *input.ExpectedEvidence {
			if strings.TrimSpace(ref.KnowledgeID) == "" {
				return nil, apperrors.NewBadRequestError(
					"expected_evidence entries need a knowledge_id")
			}
		}
		question.ExpectedEvidence = goldenEvidenceJSON(*input.ExpectedEvidence)
	}
	if input.SimulatedUserRole != nil {
		role, err := normalizeGoldenRole(*input.SimulatedUserRole)
		if err != nil {
			return nil, err
		}
		question.SimulatedUserRole = role
	}
	if input.RiskLevel != nil {
		risk := types.GoldenRiskLevel(strings.TrimSpace(*input.RiskLevel))
		if !risk.Valid() {
			return nil, apperrors.NewBadRequestError("risk_level must be low, medium or high")
		}
		question.RiskLevel = string(risk)
	}
	if input.FreshnessExpectation != nil {
		freshness := types.GoldenFreshnessExpectation(strings.TrimSpace(*input.FreshnessExpectation))
		if !freshness.Valid() {
			return nil, apperrors.NewBadRequestError(
				"freshness_expectation must be valid, stale_ok or expired_must_refuse")
		}
		question.FreshnessExpectation = string(freshness)
	}
	if input.Enabled != nil {
		question.Enabled = *input.Enabled
	}
	if err := s.repo.UpdateQuestion(ctx, question); err != nil {
		return nil, err
	}
	return question, nil
}

// DeleteQuestion removes one question from a set.
func (s *goldenSetService) DeleteQuestion(ctx context.Context, tenantID uint64, setID, questionID, actorID string) error {
	set, err := s.loadSet(ctx, tenantID, setID)
	if err != nil {
		return err
	}
	if _, err := s.loadQuestion(ctx, set.ID, questionID); err != nil {
		return err
	}
	return s.repo.DeleteQuestion(ctx, tenantID, set.ID, questionID)
}

// ============================================================================
// Runs
// ============================================================================

// RunEvaluation runs the v1 retrieval evaluation synchronously and persists
// the result as one golden_set_run.
//
// v1 范围（刻意收窄）：
//   - 只对 set.kb_id 绑定的评测知识库做 HybridSearch（top-5、阈值置 0），
//     以 expected_evidence.knowledge_id 为命中粒度计算 top5_hit /
//     recall_at_5 / NDCG@5（binary relevance）/ MRR；
//   - answer_accuracy / refusal_correctness 留 TODO：需要跑完整问答链路
//     （sessionService.KnowledgeQAByEvent 之类）再对 expected_answer 与
//     expired_must_refuse 期望打分，v2 接 pipeline 后补；
//   - 单题检索失败只记到 per_question 的 error，不拖垮整场跑批；整体失败
//     （读题失败等）置 run failed，metrics.error 带原因。
//
// 指标复用 internal/application/service/metric（与既有离线评测同一套实现）：
// 其 MetricInput 以 int 下标为文档 ID，这里把本题的 knowledge_id 集合映射
// 成稠密下标后喂给 Recall/MRR/NDCG。
func (s *goldenSetService) RunEvaluation(ctx context.Context, tenantID uint64, setID, userID string) (*types.GoldenSetRun, error) {
	set, err := s.loadSet(ctx, tenantID, setID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(set.KBID) == "" {
		return nil, apperrors.NewBadRequestError(
			"golden set has no bound evaluation knowledge base (kb_id)")
	}
	questions, err := s.repo.ListQuestions(ctx, set.ID, true)
	if err != nil {
		return nil, err
	}

	// 落一行 running：即使进程中途挂掉，运行痕迹也留得住（started_at 起）。
	startedAt := s.now()
	run := &types.GoldenSetRun{
		SetID:       set.ID,
		TenantID:    tenantID,
		TriggeredBy: userID,
		Status:      string(types.GoldenSetRunStatusRunning),
		Metrics:     types.JSON([]byte("{}")),
		PerQuestion: types.JSON([]byte("[]")),
		StartedAt:   startedAt,
	}
	if err := s.repo.CreateRun(ctx, run); err != nil {
		return nil, err
	}

	results, metrics, evalErr := s.evaluateQuestions(ctx, set, questions)
	finishedAt := s.now()
	run.FinishedAt = &finishedAt
	if evalErr != nil {
		run.Status = string(types.GoldenSetRunStatusFailed)
		metrics.Pass = false
		metrics.Error = evalErr.Error()
	} else {
		run.Status = string(types.GoldenSetRunStatusCompleted)
	}
	run.Metrics = goldenMetricsJSON(metrics)
	run.PerQuestion = goldenPerQuestionJSON(results)
	if err := s.repo.UpdateRun(ctx, run); err != nil {
		return nil, err
	}

	// 门禁结论是治理事件：completed/failed 都落 goldenset.evaluated。
	s.logAudit(ctx, tenantID, userID, auditActionGoldenSetEvaluated,
		"golden_set_run", run.ID, map[string]interface{}{
			"set_id":    set.ID,
			"set_name":  set.Name,
			"status":    run.Status,
			"top5_hit":  metrics.Top5Hit,
			"pass":      metrics.Pass,
			"questions": len(questions),
			"kb_id":     set.KBID,
		})

	if evalErr != nil {
		return run, evalErr
	}
	return run, nil
}

// evaluateQuestions runs the per-question retrieval scoring and aggregates
// the run metrics. It never returns an error for per-question search
// failures (they land in the question's result); the error return is for
// run-wide failures only.
func (s *goldenSetService) evaluateQuestions(
	ctx context.Context,
	set *types.GoldenSet,
	questions []*types.GoldenQuestion,
) ([]*types.GoldenQuestionResult, *types.GoldenRunMetrics, error) {
	results := make([]*types.GoldenQuestionResult, 0, len(questions))
	metrics := &types.GoldenRunMetrics{}

	var (
		scorable  int // 有期望证据且未整体失败的题数（聚合分母）
		hitCount  int // 其中 top5 命中的题数
		recallSum float64
		ndcgSum   float64
		mrrSum    float64
	)
	for _, question := range questions {
		result := &types.GoldenQuestionResult{
			QuestionID: question.ID,
			Question:   question.Question,
		}
		expected := distinctKnowledgeIDs(parseGoldenEvidence(question.ExpectedEvidence))
		result.ExpectedKnowledgeIDs = expected
		if len(expected) == 0 {
			// 没有期望证据的题无法判检索（纯答案质量题），跳过且不计分母。
			result.Skipped = true
			results = append(results, result)
			continue
		}

		searchResults, err := s.kbService.HybridSearch(ctx, set.KBID, types.SearchParams{
			QueryText:        question.Question,
			MatchCount:       goldenSearchTopK,
			VectorThreshold:  goldenVectorThreshold,
			KeywordThreshold: goldenKeywordThreshold,
		})
		if err != nil {
			// 单题失败不拖垮整场跑批：缺口照记，得分按 0 计。
			logger.Warnf(ctx, "golden set %s question %s search failed: %v",
				set.ID, question.ID, err)
			result.Error = err.Error()
			result.MissingKnowledgeIDs = expected
			scorable++
			results = append(results, result)
			continue
		}

		// 命中粒度是 knowledge_id：同文档多 chunk 命中先按首现去重，避免
		// NDCG 的 DCG 重复计同一篇文档。
		retrieved := make([]string, 0, len(searchResults))
		for _, r := range searchResults {
			if r == nil || r.KnowledgeID == "" {
				continue
			}
			retrieved = append(retrieved, r.KnowledgeID)
		}
		retrieved = distinctStrings(retrieved)

		firstHitRank, missing := scoreRetrieval(expected, retrieved)
		result.FirstHitRank = firstHitRank
		result.Top5Hit = firstHitRank > 0
		result.MissingKnowledgeIDs = missing

		// metric 包吃 int 下标：本题的 ground truth 与召回映射成稠密下标。
		idToInt := make(map[string]int, len(expected)+len(retrieved))
		gtInts := make([]int, 0, len(expected))
		for _, id := range expected {
			idToInt[id] = len(idToInt)
			gtInts = append(gtInts, idToInt[id])
		}
		retInts := make([]int, 0, len(retrieved))
		for _, id := range retrieved {
			if _, ok := idToInt[id]; !ok {
				idToInt[id] = len(idToInt)
			}
			retInts = append(retInts, idToInt[id])
		}
		input := &types.MetricInput{
			RetrievalGT:  [][]int{gtInts},
			RetrievalIDs: retInts,
		}
		result.RecallAt5 = metric.NewRecallMetric().Compute(input)
		result.NDCG = metric.NewNDCGMetric(goldenSearchTopK).Compute(input)
		result.MRR = metric.NewMRRMetric().Compute(input)

		scorable++
		if result.Top5Hit {
			hitCount++
		}
		recallSum += result.RecallAt5
		ndcgSum += result.NDCG
		mrrSum += result.MRR
		results = append(results, result)
	}

	// 聚合只看 scorable 题（跳过题不稀释分母）。没有可判题时门禁失败闭合
	// （0 分不通过）：一个没有期望证据的集合没有资格给出放行结论。
	if scorable > 0 {
		metrics.Top5Hit = float64(hitCount) / float64(scorable)
		metrics.RecallAt5 = recallSum / float64(scorable)
		metrics.NDCG = ndcgSum / float64(scorable)
		metrics.MRR = mrrSum / float64(scorable)
	}
	metrics.Pass = metrics.Top5Hit >= s.gateTop5Hit
	return results, metrics, nil
}

// scoreRetrieval finds the first hit's 1-based rank and the expected IDs
// missing from the retrieved list. rank 0 = no hit.
func scoreRetrieval(expected, retrieved []string) (int, []string) {
	expectedSet := make(map[string]struct{}, len(expected))
	for _, id := range expected {
		expectedSet[id] = struct{}{}
	}
	retrievedSet := make(map[string]struct{}, len(retrieved))
	for _, id := range retrieved {
		retrievedSet[id] = struct{}{}
	}
	rank := 0
	for i, id := range retrieved {
		if _, ok := expectedSet[id]; ok {
			rank = i + 1
			break
		}
	}
	missing := make([]string, 0, len(expected))
	for _, exp := range expected {
		if _, ok := retrievedSet[exp]; !ok {
			missing = append(missing, exp)
		}
	}
	return rank, missing
}

// distinctStrings dedupes preserving first occurrence (order-stable).
func distinctStrings(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// ListRuns lists a set's runs, newest first.
func (s *goldenSetService) ListRuns(ctx context.Context, tenantID uint64, setID string, limit int) ([]*types.GoldenSetRun, error) {
	if _, err := s.loadSet(ctx, tenantID, setID); err != nil {
		return nil, err
	}
	return s.repo.ListRuns(ctx, tenantID, setID, limit)
}

// GetRun resolves one run by ID within the tenant.
func (s *goldenSetService) GetRun(ctx context.Context, tenantID uint64, runID string) (*types.GoldenSetRun, error) {
	run, err := s.repo.GetRun(ctx, tenantID, runID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperrors.NewNotFoundError("golden set run not found")
		}
		return nil, err
	}
	return run, nil
}

// CheckGate reads the latest completed run of a set and reports the
// release-gate verdict. 没有已完成评测 = 不放行（失败闭合）：发布门禁
// 不接受"没跑过就当通过"。
func (s *goldenSetService) CheckGate(ctx context.Context, tenantID uint64, setID string) (bool, *types.GoldenSetRun, error) {
	if _, err := s.loadSet(ctx, tenantID, setID); err != nil {
		return false, nil, err
	}
	run, err := s.repo.GetLatestRun(ctx, tenantID, setID, types.GoldenSetRunStatusCompleted)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil, nil
		}
		return false, nil, err
	}
	var metrics types.GoldenRunMetrics
	if len(run.Metrics) > 0 {
		if err := json.Unmarshal(run.Metrics, &metrics); err != nil {
			// 指标损坏按不通过处理（门禁失败闭合），错误只进日志。
			logger.Warnf(ctx, "golden set run %s has malformed metrics: %v", run.ID, err)
			return false, run, nil
		}
	}
	return metrics.Pass, run, nil
}

// ============================================================================
// Shared helpers
// ============================================================================

// loadSet resolves a tenant-scoped set, translating record-not-found into
// the domain error the handler layer maps to 404.
func (s *goldenSetService) loadSet(ctx context.Context, tenantID uint64, setID string) (*types.GoldenSet, error) {
	set, err := s.repo.GetSet(ctx, tenantID, setID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperrors.NewNotFoundError("golden set not found")
		}
		return nil, err
	}
	return set, nil
}

// loadQuestion resolves one question of a set (set 已经过租户校验).
func (s *goldenSetService) loadQuestion(ctx context.Context, setID, questionID string) (*types.GoldenQuestion, error) {
	question, err := s.repo.GetQuestion(ctx, setID, questionID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperrors.NewNotFoundError("golden question not found")
		}
		return nil, err
	}
	return question, nil
}

// logAudit writes one golden-set audit event; audit failures never block the
// business write (same policy as the other verticals).
func (s *goldenSetService) logAudit(
	ctx context.Context, tenantID uint64, actorID, action, targetType, targetID string,
	details map[string]interface{},
) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: actorID,
		Action:      types.AuditAction(action),
		ScopeType:   "goldenset",
		TargetType:  targetType,
		TargetID:    targetID,
		Outcome:     "success",
		Details:     goldenMetricsJSON(details),
	})
}
