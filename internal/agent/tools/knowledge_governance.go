package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// Tool names for the governance-facing Agent/MCP surface (KnowAxis V3.0 §16.2).
//
// 这三个工具把平台上已经存在、但只能通过 REST 访问的治理能力开放给 Agent 与
// MCP 客户端：一次问答的依据（evidence）、一个变更的影响面（impact）、以及
// 待确认的经验候选（candidate）。缺了它们，外部系统只能"检索"，不能"追溯"
// ——而追溯恰恰是这些能力在受控委派场景里的价值。
var knowledgeEvidenceTool = BaseTool{
	name:        ToolKnowledgeEvidence,
	description: "Return the evidence pack of one answered turn: the question, answer, citations with their knowledge ids, the permission snapshot and the refusal marker. Use it to answer \"what was this answer based on\" and to carry citations into your own output. Requires the request_id of the turn.",
	schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "request_id": {
      "type": "string",
      "description": "The answered turn's request id",
      "minLength": 1
    },
    "include_answer": {
      "type": "boolean",
      "description": "Include the answer text (default true). Set false to fetch only citations and provenance."
    }
  },
  "required": ["request_id"]
}`),
}

var knowledgeImpactTool = BaseTool{
	name:        ToolKnowledgeImpact,
	description: "Return what a change to one graph entity would affect: the facts and entities reachable through its outgoing dependency edges, with confidence and relation type. Use it before recommending a change, to see which other knowledge depends on the subject.",
	schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "entity_id": {
      "type": "string",
      "description": "Graph entity id (from query_knowledge_graph)",
      "minLength": 1
    }
  },
  "required": ["entity_id"]
}`),
}

var knowledgeCandidatesTool = BaseTool{
	name:        ToolKnowledgeCandidates,
	description: "List experience candidates awaiting review, newest first. Use it to summarize the governance backlog or to check whether an experience has already been captured before proposing a new one.",
	schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "status": {
      "type": "string",
      "enum": ["candidate", "in_review", "confirmed", "published", "rejected", "withdrawn"],
      "description": "Optional status filter"
    },
    "level": {
      "type": "string",
      "enum": ["L0", "L1", "L2", "L3"],
      "description": "Optional knowledge-level filter"
    },
    "limit": {
      "type": "integer",
      "description": "Maximum rows (default 20, max 50)",
      "minimum": 1,
      "maximum": 50
    }
  }
}`),
}

const (
	knowledgeCandidatesDefaultLimit = 20
	knowledgeCandidatesMaxLimit     = 50
)

// KnowledgeEvidenceInput is the input for knowledge_evidence.
type KnowledgeEvidenceInput struct {
	RequestID string `json:"request_id"`
	// IncludeAnswer defaults to true; a false value suppresses the answer text.
	IncludeAnswer *bool `json:"include_answer,omitempty"`
}

// KnowledgeEvidenceTool returns an answered turn's evidence pack.
type KnowledgeEvidenceTool struct {
	BaseTool
	packs interfaces.EvidencePackRepository
}

// NewKnowledgeEvidenceTool constructs the tool.
func NewKnowledgeEvidenceTool(
	packs interfaces.EvidencePackRepository,
) *KnowledgeEvidenceTool {
	return &KnowledgeEvidenceTool{BaseTool: knowledgeEvidenceTool, packs: packs}
}

// Execute returns the evidence of one turn.
//
// 输出刻意**不包含** permission snapshot 的原始结构与 hash chain：委派调用方
// 需要的是"依据了什么"，不是平台的内部审计字段。citations 保留知识 ID 与
// 标题，这样调用方能自己再去看原文。
func (t *KnowledgeEvidenceTool) Execute(
	ctx context.Context, args json.RawMessage,
) (*types.ToolResult, error) {
	var input KnowledgeEvidenceInput
	if err := json.Unmarshal(args, &input); err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("Failed to parse args: %v", err)}, err
	}
	requestID := strings.TrimSpace(input.RequestID)
	if requestID == "" {
		return &types.ToolResult{Success: false, Error: "request_id is required"},
			fmt.Errorf("request_id is required")
	}
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		msg := "no tenant in scope"
		return &types.ToolResult{Success: false, Error: msg}, fmt.Errorf("%s", msg)
	}
	pack, err := t.packs.GetByRequestID(ctx, tenantID, requestID)
	if err != nil {
		// 找不到就是找不到：不区分"不存在"与"不属于这个租户"，
		// 后者会变成存在性探测。
		msg := "answered turn not found"
		return &types.ToolResult{Success: false, Error: msg}, nil
	}

	includeAnswer := true
	if input.IncludeAnswer != nil {
		includeAnswer = *input.IncludeAnswer
	}
	citations := pack.CitationsList()
	payload := map[string]interface{}{
		"request_id":     pack.RequestID,
		"question":       pack.Question,
		"refusal":        pack.Refusal,
		"citation_count": len(citations),
		"citations":      citations,
		"model_version":  pack.ModelVersion,
		"prompt_version": pack.PromptVersion,
		"answered_at":    pack.CreatedAt,
	}
	if includeAnswer {
		payload["answer"] = pack.Answer
	}
	raw, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		msg := "failed to render the evidence pack"
		return &types.ToolResult{Success: false, Error: msg}, marshalErr
	}
	return &types.ToolResult{Success: true, Output: string(raw)}, nil
}

// KnowledgeImpactInput is the input for knowledge_impact.
type KnowledgeImpactInput struct {
	EntityID string `json:"entity_id"`
}

// KnowledgeImpactTool returns the blast radius of one entity change.
type KnowledgeImpactTool struct {
	BaseTool
	graph interfaces.GraphFactService
}

// NewKnowledgeImpactTool constructs the tool.
func NewKnowledgeImpactTool(graph interfaces.GraphFactService) *KnowledgeImpactTool {
	return &KnowledgeImpactTool{BaseTool: knowledgeImpactTool, graph: graph}
}

// maxImpactFactsPerOutput bounds the rows rendered into the tool output.
//
// 影响面可能是几百条；把它们全部塞进模型上下文既撑爆窗口，也淹没了真正
// 需要先处理的少数几条。截断后明确告知还剩多少条。
const maxImpactFactsPerOutput = 40

// Execute returns the impact of one entity.
func (t *KnowledgeImpactTool) Execute(
	ctx context.Context, args json.RawMessage,
) (*types.ToolResult, error) {
	var input KnowledgeImpactInput
	if err := json.Unmarshal(args, &input); err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("Failed to parse args: %v", err)}, err
	}
	entityID := strings.TrimSpace(input.EntityID)
	if entityID == "" {
		return &types.ToolResult{Success: false, Error: "entity_id is required"},
			fmt.Errorf("entity_id is required")
	}
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		msg := "no tenant in scope"
		return &types.ToolResult{Success: false, Error: msg}, fmt.Errorf("%s", msg)
	}
	result, err := t.graph.ImpactQuery(ctx, tenantID, entityID)
	if err != nil {
		// 实体不存在与无权访问对外同一个答案（同 evidence 的理由）。
		msg := "entity not found or not visible"
		return &types.ToolResult{Success: false, Error: msg}, nil
	}

	type renderedFact struct {
		FactID         string `json:"fact_id"`
		RelationName   string `json:"relation_name"`
		Confidence     string `json:"confidence"`
		SourceEntityID string `json:"source_entity_id"`
		TargetEntityID string `json:"target_entity_id"`
		// EvidenceCount tells the caller how many source chunks back the fact
		// without shipping the quotes themselves.
		EvidenceCount int `json:"evidence_count"`
	}
	rendered := make([]renderedFact, 0, maxImpactFactsPerOutput)
	for _, fact := range result.Facts {
		if fact == nil {
			continue
		}
		if len(rendered) >= maxImpactFactsPerOutput {
			break
		}
		rendered = append(rendered, renderedFact{
			FactID:         fact.ID,
			RelationName:   fact.RelationName,
			Confidence:     string(fact.Confidence),
			SourceEntityID: fact.SourceEntityID,
			TargetEntityID: fact.TargetEntityID,
			EvidenceCount:  countFactEvidence(fact.Evidence),
		})
	}
	payload := map[string]interface{}{
		"entity_id":          entityID,
		"depth":              result.Depth,
		"fact_count":         len(result.Facts),
		"affected_count":     len(result.AffectedEntities),
		"facts":              rendered,
		"truncated":          len(result.Facts) > len(rendered),
		"omitted_fact_count": max(0, len(result.Facts)-len(rendered)),
	}
	raw, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		msg := "failed to render the impact result"
		return &types.ToolResult{Success: false, Error: msg}, marshalErr
	}
	return &types.ToolResult{Success: true, Output: string(raw)}, nil
}

// countFactEvidence counts the entries in a fact's evidence array.
//
// 不能直接用 len()：Evidence 是 JSON 列（[]byte），len 给的是字节数。一个
// 混淆这两者的实现会把"26 条证据"这种数字报到调用方面前。
func countFactEvidence(raw types.JSON) int {
	if len(raw) == 0 {
		return 0
	}
	var refs []types.KnowledgeCandidateEvidence
	if err := json.Unmarshal(raw, &refs); err != nil {
		return 0
	}
	return len(refs)
}

// KnowledgeCandidatesInput is the input for knowledge_candidates.
type KnowledgeCandidatesInput struct {
	Status string `json:"status,omitempty"`
	Level  string `json:"level,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// KnowledgeCandidatesTool lists experience candidates.
type KnowledgeCandidatesTool struct {
	BaseTool
	candidates interfaces.KnowledgeCandidateService
}

// NewKnowledgeCandidatesTool constructs the tool.
func NewKnowledgeCandidatesTool(
	candidates interfaces.KnowledgeCandidateService,
) *KnowledgeCandidatesTool {
	return &KnowledgeCandidatesTool{BaseTool: knowledgeCandidatesTool, candidates: candidates}
}

// Execute lists candidates under the caller's tenant.
func (t *KnowledgeCandidatesTool) Execute(
	ctx context.Context, args json.RawMessage,
) (*types.ToolResult, error) {
	var input KnowledgeCandidatesInput
	if len(args) > 0 {
		if err := json.Unmarshal(args, &input); err != nil {
			return &types.ToolResult{Success: false, Error: fmt.Sprintf("Failed to parse args: %v", err)},
				err
		}
	}
	if status := strings.TrimSpace(input.Status); status != "" {
		if !types.KnowledgeCandidateStatus(status).Valid() {
			msg := fmt.Sprintf("unknown candidate status %q", status)
			return &types.ToolResult{Success: false, Error: msg}, nil
		}
	}
	if level := strings.TrimSpace(input.Level); level != "" {
		if !types.KnowledgeCandidateLevel(level).Valid() {
			msg := fmt.Sprintf("unknown knowledge level %q", level)
			return &types.ToolResult{Success: false, Error: msg}, nil
		}
	}
	limit := input.Limit
	if limit <= 0 {
		limit = knowledgeCandidatesDefaultLimit
	}
	if limit > knowledgeCandidatesMaxLimit {
		limit = knowledgeCandidatesMaxLimit
	}

	rows, total, err := t.candidates.ListCandidates(ctx, types.KnowledgeCandidateListFilter{
		Status: strings.TrimSpace(input.Status),
		Level:  strings.TrimSpace(input.Level),
		Limit:  limit,
	})
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, err
	}

	type renderedCandidate struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Level       string `json:"level"`
		Status      string `json:"status"`
		SourceType  string `json:"source_type"`
		DedupStatus string `json:"dedup_status"`
		Confidence  string `json:"confidence"`
		UpdatedAt   string `json:"updated_at"`
	}
	rendered := make([]renderedCandidate, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		rendered = append(rendered, renderedCandidate{
			ID: row.ID, Title: row.Title, Level: row.Level, Status: row.Status,
			SourceType: row.SourceType, DedupStatus: row.DedupStatus,
			Confidence: row.Confidence,
			UpdatedAt:  row.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	payload := map[string]interface{}{
		"total": total, "returned": len(rendered), "candidates": rendered,
	}
	raw, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		msg := "failed to render the candidate list"
		return &types.ToolResult{Success: false, Error: msg}, marshalErr
	}
	return &types.ToolResult{Success: true, Output: string(raw)}, nil
}
