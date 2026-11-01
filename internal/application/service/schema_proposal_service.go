package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
)

// SchemaProposal 业务哨兵错误。handler 层据此映射 HTTP 语义
// （404/403/409/400/401），与 review / knowledge candidate 等服务的错误风格
// 一致。
var (
	// ErrSchemaProposalTenantMissing 请求上下文缺少租户（中间件层未注入）。
	ErrSchemaProposalTenantMissing = errors.New("schema proposal: tenant context is missing")
	// ErrSchemaProposalActorMissing 调用者缺少用户身份（提案必须可归因）。
	ErrSchemaProposalActorMissing = errors.New("schema proposal: caller identity is missing")
	// ErrSchemaProposalNotFound 提案不存在（或不在本租户）。
	ErrSchemaProposalNotFound = errors.New("schema proposal: proposal not found")
	// ErrSchemaProposalInvalidState 状态机不允许该动作（终态/顺序不符）。
	ErrSchemaProposalInvalidState = errors.New("schema proposal: status does not allow this action")
	// ErrSchemaProposalSelfReview 四眼校验失败：审批人就是提案创建人。
	ErrSchemaProposalSelfReview = errors.New("schema proposal: the creator cannot review their own proposal")
	// ErrSchemaProposalForbidden 审批人不是 Admin（路由层已要求，服务层兜底）。
	ErrSchemaProposalForbidden = errors.New("schema proposal: reviewer must be an admin of this workspace")
	// ErrSchemaProposalNameRequired 提案名必填（同时是 Apply 目标 schema 名）。
	ErrSchemaProposalNameRequired = errors.New("schema proposal: name is required")
	// ErrSchemaProposalInvalidPayload 提案结构非法（缺类型/类型名重复/未知
	// data_type 或 cardinality 等）。
	ErrSchemaProposalInvalidPayload = errors.New("schema proposal: payload is invalid")
	// ErrSchemaProposalNoDocuments 文档驱动建模未提供任何文档。
	ErrSchemaProposalNoDocuments = errors.New("schema proposal: at least one source document is required")
	// ErrSchemaProposalTooManyDocuments 文档数超过上限（≤8 篇）。
	ErrSchemaProposalTooManyDocuments = errors.New("schema proposal: at most 8 source documents are allowed")
	// ErrSchemaProposalDocumentNotFound 输入文档不存在（或不在本租户）。
	ErrSchemaProposalDocumentNotFound = errors.New("schema proposal: source document not found")
	// ErrSchemaProposalDocumentEmpty 输入文档没有任何可用正文。
	ErrSchemaProposalDocumentEmpty = errors.New("schema proposal: source document has no extractable text")
	// ErrSchemaProposalModelMissing 未注入 chat 模型（改用 ProposeManual 手工
	// 提交 payload）。
	ErrSchemaProposalModelMissing = errors.New("schema proposal: no chat model is available for document-driven modeling")
	// ErrSchemaProposalModelOutput 模型输出不是预期的提案 JSON（400）。
	ErrSchemaProposalModelOutput = errors.New("schema proposal: the model output is not a valid proposal JSON")
	// ErrSchemaProposalCompatibilityBlocker 兼容性检查存在 blocker，拒绝应用。
	ErrSchemaProposalCompatibilityBlocker = errors.New("schema proposal: compatibility report has blocking issues")
	// ErrSchemaProposalTargetSchemaImmutable 目标 schema 已发布/已归档，不能
	// 追加（新版本命名冲突时才会出现，正常路径会自动派生新版本名）。
	ErrSchemaProposalTargetSchemaImmutable = errors.New("schema proposal: target schema is not editable")
	// ErrSchemaProposalApplyFailed Apply 过程中本体写入失败（类型/属性/关系）。
	ErrSchemaProposalApplyFailed = errors.New("schema proposal: applying the proposal to the ontology failed")
)

const (
	// schemaProposalAuditScope 提案审计事件的 scope_type。
	schemaProposalAuditScope = "schema_proposal"
	// schemaProposalMaxDocuments 单次文档驱动建模的输入文档上限（§8.2：
	// 送模型前先做文档级截断，避免提案成本失控）。
	schemaProposalMaxDocuments = 8
	// schemaProposalDocContentRunes 单篇文档送入模型的正文字符上限。
	schemaProposalDocContentRunes = 4000
	// schemaProposalPromptVersion 提示词版本（审计详情里记录，便于对比不同
	// 版本产出）。
	schemaProposalPromptVersion = "v1"
	// schemaProposalNameMaxRunes 提案名字符上限（与列宽 varchar(128) 对齐）。
	schemaProposalNameMaxRunes = 128
	// schemaProposalDomainMaxRunes domain 字符上限（varchar(64)）。
	schemaProposalDomainMaxRunes = 64
	// schemaProposalAuditCommentLimit 限制进入审计详情的评语长度。
	schemaProposalAuditCommentLimit = 200
	// schemaProposalModelMaxTokens 提案抽取的 completion 预算：类型/属性/
	// 关系数量大时 JSON 较长。
	schemaProposalModelMaxTokens = 4000
	// schemaProposalModelTemperature 结构化抽取取低温。
	schemaProposalModelTemperature = 0.1
)

// aischemaSystemPrompt 文档驱动建模的系统提示词（内联常量：与解析逻辑同
// 版本演进，改提示词时同时升 schemaProposalPromptVersion）。
const aischemaSystemPrompt = `You are an ontology engineer. You read enterprise documents and propose a typed knowledge schema: the entity types, their attributes, and the relations between them.

Rules:
- Return ONLY a JSON object, no prose, no markdown fences.
- JSON shape:
{"types":[{"name":"Device","display_name":"设备","description":"...","aliases":["设备","机台"],"properties":[{"name":"model","data_type":"string","required":true}]}],
 "relations":[{"name":"belongs_to","source_type":"Device","target_type":"ProductionLine","cardinality":"many_to_one","description":"..."}]}
- "name" values are machine names: ASCII identifiers without spaces (PascalCase for types, snake_case for relations and properties).
- "data_type" MUST be one of: string, number, boolean, date, enum, ref.
- "cardinality" MUST be one of: one_to_one, one_to_many, many_to_one, many_to_many. Omit it to default to many_to_many.
- "source_type" and "target_type" MUST be names of types you define in the same "types" array.
- Only propose concepts that the supplied documents actually discuss; do not invent generic filler types.
- Prefer 3-12 types. Every type needs at least one property when the documents describe attributes.
- Aliases are the surface forms (abbreviations, Chinese synonyms) that appear in the text.`

// aischemaUserPromptTemplate 用户消息模板：文档标题 + 截断正文（≤4000 字/篇）。
const aischemaUserPromptTemplate = `Domain: %s
Proposal name: %s

Documents (%d):
%s

Return the ontology proposal JSON described in the system prompt.`

// aischemaDocumentPromptTemplate 单篇文档在 prompt 中的片段模板。
const aischemaDocumentPromptTemplate = "\n<document index=\"%d\">\n<title>%s</title>\n<file_type>%s</file_type>\n<content>\n%s\n</content>\n</document>\n"

// schemaProposalService implements interfaces.SchemaProposalService：KnowAxis
// V3.0 §8.2/§8.4 的"文档驱动 AI 自动建模 + 提案治理"闭环。
//
// 依赖都是接口，模型可缺省（models == nil 时只支持 ProposeManual）：
//   - ontology：提案落地时唯一的本体写入口（CreateSchema/AddType/
//     AddProperty/AddRelation），保证 ontology_schemas 只有被人批准过的结构；
//   - knowledges/chunks：取输入文档正文（chunk 文本优先，manual 正文兜底）；
//   - models：文档 → 提案的结构化抽取；
//   - audit：每次创建/检查/审批/落地都写 ontology.schema_proposal_* 事件。
type schemaProposalService struct {
	repo       interfaces.SchemaProposalRepository
	ontology   interfaces.OntologyService
	knowledges interfaces.KnowledgeRepository
	chunks     interfaces.ChunkRepository
	models     interfaces.ModelService
	audit      interfaces.AuditLogService
	now        func() time.Time
}

// NewSchemaProposalService constructs the schema proposal service. models may
// be nil: document-driven modeling then reports ErrSchemaProposalModelMissing
// and callers use ProposeManual.
func NewSchemaProposalService(
	repo interfaces.SchemaProposalRepository,
	ontology interfaces.OntologyService,
	knowledges interfaces.KnowledgeRepository,
	chunks interfaces.ChunkRepository,
	models interfaces.ModelService,
	audit interfaces.AuditLogService,
) interfaces.SchemaProposalService {
	return &schemaProposalService{
		repo:       repo,
		ontology:   ontology,
		knowledges: knowledges,
		chunks:     chunks,
		models:     models,
		audit:      audit,
		now:        time.Now,
	}
}

// ============================================================================
// 提案创建
// ============================================================================

// ProposeFromDocuments 文档驱动自动建模：
//  1. 校验提案名/调用者；输入文档 ≤8 篇（超出直接 400，不静默截断——评审
//     需要知道 AI 实际看了什么）；
//  2. 逐篇取正文：text/image_ocr/image_caption chunk 按位置拼装，无 chunk
//     时回落 manual 正文/描述；每篇截断到 4000 字；
//  3. 取租户默认 chat 模型做一次结构化抽取（提示词内联常量，要求输出提案
//     JSON）；
//  4. 解析 + 规范化 + 校验后落 pending 提案（不碰 ontology_schemas）。
func (s *schemaProposalService) ProposeFromDocuments(
	ctx context.Context, tenantID uint64, name, domain string, knowledgeIDs []string, actorID string,
) (*types.SchemaProposal, error) {
	tenantID, actorID, err := s.resolveIdentity(ctx, tenantID, actorID)
	if err != nil {
		return nil, err
	}
	name, domain, err = aischemaValidateIdentity(name, domain)
	if err != nil {
		return nil, err
	}
	knowledgeIDs = aischemaNormalizeIDs(knowledgeIDs)
	if len(knowledgeIDs) == 0 {
		return nil, ErrSchemaProposalNoDocuments
	}
	if len(knowledgeIDs) > schemaProposalMaxDocuments {
		return nil, fmt.Errorf("%w: got %d", ErrSchemaProposalTooManyDocuments, len(knowledgeIDs))
	}
	if s.models == nil {
		return nil, ErrSchemaProposalModelMissing
	}

	documents, err := s.aischemaLoadDocuments(ctx, tenantID, knowledgeIDs)
	if err != nil {
		return nil, err
	}
	modelID, model, err := s.aischemaPickChatModel(ctx)
	if err != nil {
		return nil, err
	}
	payload, rationale, err := s.aischemaExtractPayload(ctx, model, modelID, name, domain, documents)
	if err != nil {
		return nil, err
	}

	proposal, err := s.aischemaCreateProposal(ctx, tenantID, name, domain, rationale, payload, knowledgeIDs, actorID, map[string]any{
		"source":            "ai",
		"document_count":    len(documents),
		"model_id":          modelID,
		"prompt_version":    schemaProposalPromptVersion,
		"model_max_tokens":  schemaProposalModelMaxTokens,
		"model_temperature": schemaProposalModelTemperature,
	})
	if err != nil {
		return nil, err
	}
	return proposal, nil
}

// ProposeManual 手工提交提案：无模型时的唯一入口，也是在 AI 产出上人工修订
// 后再提交的通道。payload 与文档驱动共享同一结构校验。
func (s *schemaProposalService) ProposeManual(
	ctx context.Context, input types.SchemaProposalManualInput, actorID string,
) (*types.SchemaProposal, error) {
	tenantID, actorID, err := s.resolveIdentity(ctx, 0, actorID)
	if err != nil {
		return nil, err
	}
	name, domain, err := aischemaValidateIdentity(input.Name, input.Domain)
	if err != nil {
		return nil, err
	}
	if input.Payload == nil {
		return nil, fmt.Errorf("%w: proposal payload is required", ErrSchemaProposalInvalidPayload)
	}
	payload := *input.Payload
	payload.Normalize()
	if err := payload.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrSchemaProposalInvalidPayload, err.Error())
	}
	knowledgeIDs := aischemaNormalizeIDs(input.SourceKnowledgeIDs)
	if len(knowledgeIDs) > schemaProposalMaxDocuments {
		return nil, fmt.Errorf("%w: got %d", ErrSchemaProposalTooManyDocuments, len(knowledgeIDs))
	}

	proposal, err := s.aischemaCreateProposal(ctx, tenantID, name, domain,
		strings.TrimSpace(input.Rationale), &payload, knowledgeIDs, actorID, map[string]any{
			"source":         "manual",
			"document_count": len(knowledgeIDs),
			"type_count":     len(payload.Types),
			"relation_count": len(payload.Relations),
		})
	if err != nil {
		return nil, err
	}
	return proposal, nil
}

// aischemaCreateProposal 落一行 pending 提案并写审计。AI 与手工路径共用，
// 保证两条入口的落库形态、审计字段与状态完全一致。
func (s *schemaProposalService) aischemaCreateProposal(
	ctx context.Context,
	tenantID uint64,
	name, domain, rationale string,
	payload *types.ProposalPayload,
	knowledgeIDs []string,
	actorID string,
	auditDetails map[string]any,
) (*types.SchemaProposal, error) {
	payloadRaw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal payload: %s", ErrSchemaProposalInvalidPayload, err.Error())
	}
	if knowledgeIDs == nil {
		knowledgeIDs = []string{}
	}
	idsRaw, err := json.Marshal(knowledgeIDs)
	if err != nil {
		return nil, fmt.Errorf("schema proposal: marshal source_knowledge_ids: %w", err)
	}

	now := s.now()
	proposal := &types.SchemaProposal{
		ID:                  uuid.NewString(),
		TenantID:            tenantID,
		Name:                name,
		Domain:              domain,
		SourceKnowledgeIDs:  types.JSON(idsRaw),
		Proposal:            types.JSON(payloadRaw),
		Rationale:           rationale,
		Status:              types.SchemaProposalStatusPending,
		CompatibilityReport: types.JSON(`{"compatible":false,"issues":[],"summary":{}}`),
		CreatedBy:           actorID,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if err := s.repo.Create(ctx, proposal); err != nil {
		return nil, err
	}

	details := map[string]any{
		"name":           proposal.Name,
		"domain":         proposal.Domain,
		"status":         string(proposal.Status),
		"type_count":     len(payload.Types),
		"relation_count": len(payload.Relations),
	}
	for key, value := range auditDetails {
		details[key] = value
	}
	s.aischemaAudit(ctx, tenantID, actorID, types.OntologyActionSchemaProposalCreated, proposal.ID, details)
	return proposal, nil
}

// ============================================================================
// 兼容性检查
// ============================================================================

// CheckCompatibility 把提案与租户现有本体比对并落 compatibility_report：
//   - 新类型（提案独有）→ info，可自动通过；
//   - 同名类型属性变更（data_type / required 变化，或现网必填属性在提案中
//     缺失）→ warning；
//   - 关系端点既不在提案也不在现有 schema → blocker（Apply 硬门禁）；
//   - 提案类型与现网类型名/别名高度相似（疑似改名）→ warning。
func (s *schemaProposalService) CheckCompatibility(
	ctx context.Context, tenantID uint64, proposalID string,
) (*types.CompatibilityReport, error) {
	tenantID, actorID, err := s.resolveIdentity(ctx, tenantID, "")
	if err != nil {
		return nil, err
	}
	proposal, err := s.loadProposal(ctx, tenantID, proposalID)
	if err != nil {
		return nil, err
	}
	payload, err := proposal.PayloadStruct()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrSchemaProposalInvalidPayload, err.Error())
	}

	report, err := s.aischemaBuildReport(ctx, tenantID, proposal, payload)
	if err != nil {
		return nil, err
	}
	reportRaw, err := json.Marshal(report)
	if err != nil {
		return nil, fmt.Errorf("schema proposal: marshal compatibility report: %w", err)
	}
	proposal.CompatibilityReport = types.JSON(reportRaw)
	if err := s.repo.UpdateCompatibilityReport(ctx, proposal); err != nil {
		return nil, err
	}

	s.aischemaAudit(ctx, tenantID, actorID, types.OntologyActionSchemaProposalChecked, proposal.ID, map[string]any{
		"compatible":        report.Compatible,
		"blockers":          report.Summary.Blockers,
		"warnings":          report.Summary.Warnings,
		"new_types":         report.Summary.NewTypes,
		"existing_types":    report.Summary.ExistingTypes,
		"property_changes":  report.Summary.PropertyChanges,
		"missing_endpoints": report.Summary.MissingEndpoints,
		"target_schema_id":  report.TargetSchemaID,
	})
	return report, nil
}

// aischemaBuildReport 是兼容性检查的纯逻辑（不落库），便于 Apply 内部复用
// 与后续单测覆盖。
func (s *schemaProposalService) aischemaBuildReport(
	ctx context.Context, tenantID uint64, proposal *types.SchemaProposal, payload *types.ProposalPayload,
) (*types.CompatibilityReport, error) {
	report := &types.CompatibilityReport{
		ProposalID: proposal.ID,
		CheckedAt:  s.now(),
		SchemaName: proposal.Name,
		Issues:     []types.CompatibilityIssue{},
	}
	if err := payload.Validate(); err != nil {
		report.Compatible = false
		report.Issues = append(report.Issues, types.CompatibilityIssue{
			Severity: types.CompatibilitySeverityBlocker,
			Code:     types.CompatibilityCodeProposalInvalid,
			Message:  "proposal payload is invalid: " + err.Error(),
			Detail:   err.Error(),
		})
		report.Summary.Blockers = 1
		return report, nil
	}

	schemas, err := s.ontology.ListSchemas(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	// 两套索引，语义不同：
	//   targetTypes  同名 schema（Apply 的落点）的类型 —— 属性变更/端点判定
	//                都只跟它比，因为 Apply 只会往这个 schema 里写；
	//   publishedTypes 全部已发布 schema 的类型 —— 只用于"疑似改名"与
	//                "端点其实在别的本体里"这类提示，不参与阻断判定。
	// 这样检查结论与 Apply 的实际行为严格一致：检查说放行，Apply 就能落地。
	var targetSchema *types.OntologySchema
	for _, schema := range schemas {
		if schema == nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(schema.Name), proposal.Name) {
			targetSchema = schema
		}
	}

	publishedTypes := make(map[string]*types.OntologyTypeDetail)
	publishedSchemaOf := make(map[string]string) // 类型名 → 所在已发布 schema 名
	for _, schema := range schemas {
		if schema == nil || schema.Status != types.OntologySchemaStatusPublished {
			continue
		}
		detail, err := s.ontology.GetSchema(ctx, tenantID, schema.ID)
		if err != nil {
			return nil, err
		}
		report.ComparedSchemaIDs = append(report.ComparedSchemaIDs, schema.ID)
		for _, t := range detail.Types {
			if t == nil {
				continue
			}
			key := strings.ToLower(strings.TrimSpace(t.Name))
			if _, ok := publishedTypes[key]; !ok {
				publishedTypes[key] = t
				publishedSchemaOf[key] = schema.Name
			}
		}
	}

	targetTypes := make(map[string]*types.OntologyTypeDetail)
	targetRelations := make(map[string]*types.OntologyRelation)
	if targetSchema != nil {
		detail, err := s.ontology.GetSchema(ctx, tenantID, targetSchema.ID)
		if err != nil {
			return nil, err
		}
		report.TargetSchemaID = targetSchema.ID
		report.TargetSchemaStatus = string(targetSchema.Status)
		report.ComparedSchemaIDs = append(report.ComparedSchemaIDs, targetSchema.ID)
		for _, t := range detail.Types {
			if t == nil {
				continue
			}
			targetTypes[strings.ToLower(strings.TrimSpace(t.Name))] = t
		}
		for _, rel := range detail.Relations {
			if rel == nil {
				continue
			}
			targetRelations[strings.ToLower(rel.Name)] = rel
		}
	}
	// 比对基准：有同名 schema 就只跟它比（Apply 的落点），否则认为这是一个
	// 全新本体，所有类型都是新类型。
	compareTypes := targetTypes
	if targetSchema == nil {
		compareTypes = map[string]*types.OntologyTypeDetail{}
	}

	// ---- 类型与属性 ----
	for _, t := range payload.Types {
		key := strings.ToLower(t.Name)
		current, ok := compareTypes[key]
		if !ok {
			report.Summary.NewTypes++
			report.Issues = append(report.Issues, types.CompatibilityIssue{
				Severity: types.CompatibilitySeverityInfo,
				Code:     types.CompatibilityCodeNewType,
				Message:  "new type " + t.Name + " will be added",
				TypeName: t.Name,
			})
			// 新类型仍可能撞上现网已发布本体的概念（改名而非新增）：给评审
			// 一条 warning，避免同一概念在两个本体里以不同名字重复存在。
			if rename := aischemaDetectRename(t, publishedTypes); rename != nil {
				report.PossibleRenames = append(report.PossibleRenames, *rename)
				report.Summary.Warnings++
				report.Issues = append(report.Issues, types.CompatibilityIssue{
					Severity: types.CompatibilitySeverityWarning,
					Code:     types.CompatibilityCodePossibleRename,
					Message:  "type " + t.Name + " looks like a rename of the published type " + rename.From,
					TypeName: t.Name,
					Detail:   rename.From + " -> " + rename.To,
				})
			}
			continue
		}

		report.Summary.ExistingTypes++
		report.Issues = append(report.Issues, types.CompatibilityIssue{
			Severity: types.CompatibilitySeverityInfo,
			Code:     types.CompatibilityCodeTypeExists,
			Message:  "type " + t.Name + " already exists in the target schema; only missing properties will be added",
			TypeName: t.Name,
			Detail:   current.Name,
		})
		currentProps := make(map[string]*types.OntologyProperty, len(current.Properties))
		for _, prop := range current.Properties {
			if prop == nil {
				continue
			}
			currentProps[strings.ToLower(prop.Name)] = prop
		}
		proposedProps := make(map[string]struct{}, len(t.Properties))
		for _, prop := range t.Properties {
			proposedProps[strings.ToLower(prop.Name)] = struct{}{}
			existingProp, found := currentProps[strings.ToLower(prop.Name)]
			if !found {
				report.Issues = append(report.Issues, types.CompatibilityIssue{
					Severity:     types.CompatibilitySeverityInfo,
					Code:         types.CompatibilityCodePropertyAdded,
					Message:      "property " + t.Name + "." + prop.Name + " will be added",
					TypeName:     t.Name,
					PropertyName: prop.Name,
				})
				continue
			}
			if renames := aischemaPropertyDelta(existingProp, prop); renames != "" {
				report.Summary.PropertyChanges++
				report.Summary.Warnings++
				report.Issues = append(report.Issues, types.CompatibilityIssue{
					Severity:     types.CompatibilitySeverityWarning,
					Code:         types.CompatibilityCodePropertyChanged,
					Message:      "property " + t.Name + "." + prop.Name + " differs from the existing definition",
					TypeName:     t.Name,
					PropertyName: prop.Name,
					Detail:       renames,
				})
			}
		}
		for name, existingProp := range currentProps {
			if _, ok := proposedProps[name]; ok || !existingProp.Required {
				continue
			}
			report.Summary.Warnings++
			report.Issues = append(report.Issues, types.CompatibilityIssue{
				Severity:     types.CompatibilitySeverityWarning,
				Code:         types.CompatibilityCodePropertyMissing,
				Message:      "required property " + t.Name + "." + existingProp.Name + " is absent from the proposal",
				TypeName:     t.Name,
				PropertyName: existingProp.Name,
			})
		}
	}

	// ---- 关系 ----
	// 端点必须能在提案内或目标 schema 内解析：OntologyService.AddRelation 要求
	// 两端是同一 schema 的类型，别处已有的同名类型救不了这条关系。
	proposalTypeNames := make(map[string]struct{}, len(payload.Types))
	for _, t := range payload.Types {
		proposalTypeNames[strings.ToLower(t.Name)] = struct{}{}
	}
	for _, rel := range payload.Relations {
		for _, endpoint := range []string{rel.SourceType, rel.TargetType} {
			key := strings.ToLower(endpoint)
			if _, inProposal := proposalTypeNames[key]; inProposal {
				continue
			}
			if _, inTarget := compareTypes[key]; inTarget {
				continue
			}
			hint := "endpoint " + endpoint + " is neither proposed nor present in the target schema"
			if schemaName, elsewhere := publishedSchemaOf[key]; elsewhere {
				hint = fmt.Sprintf(
					"endpoint %s exists only in the published schema %q; rename the proposal to that schema name to extend it",
					endpoint, schemaName)
			} else if targetSchema == nil {
				hint = "endpoint " + endpoint + " is not proposed; a new schema has no existing types to reuse"
			}
			report.Summary.MissingEndpoints++
			report.Summary.Blockers++
			report.Issues = append(report.Issues, types.CompatibilityIssue{
				Severity:     types.CompatibilitySeverityBlocker,
				Code:         types.CompatibilityCodeRelationEndpointMissing,
				Message:      "relation " + rel.Name + " references unknown type " + endpoint,
				RelationName: rel.Name,
				Detail:       hint,
			})
		}
		if _, exists := targetRelations[strings.ToLower(rel.Name)]; exists {
			report.Issues = append(report.Issues, types.CompatibilityIssue{
				Severity:     types.CompatibilitySeverityInfo,
				Code:         types.CompatibilityCodeRelationExists,
				Message:      "relation " + rel.Name + " already exists in the target schema and will be skipped",
				RelationName: rel.Name,
			})
		}
	}

	report.Compatible = report.Summary.Blockers == 0
	report.ComparedSchemaIDs = aischemaUniqueStrings(report.ComparedSchemaIDs)
	return report, nil
}

// ============================================================================
// 审批
// ============================================================================

// Approve 批准提案：Admin + 四眼（reviewer != created_by）+ 状态必须 pending。
func (s *schemaProposalService) Approve(
	ctx context.Context, tenantID uint64, proposalID, reviewerID, comment string,
) (*types.SchemaProposal, error) {
	return s.aischemaDecide(ctx, tenantID, proposalID, reviewerID, comment, true)
}

// Reject 驳回提案：与批准同一套身份门槛，终态。
func (s *schemaProposalService) Reject(
	ctx context.Context, tenantID uint64, proposalID, reviewerID, comment string,
) (*types.SchemaProposal, error) {
	return s.aischemaDecide(ctx, tenantID, proposalID, reviewerID, comment, false)
}

// aischemaDecide 是批准/驳回的共用实现：身份门槛与状态机完全一致，只有目标
// 状态与审计 action 不同。
func (s *schemaProposalService) aischemaDecide(
	ctx context.Context, tenantID uint64, proposalID, reviewerID, comment string, approve bool,
) (*types.SchemaProposal, error) {
	tenantID, reviewerID, err := s.resolveIdentity(ctx, tenantID, reviewerID)
	if err != nil {
		return nil, err
	}
	// 路由层用 g.Admin() 守卫，服务层再兜一次：提案审批是治理动作，任何调用
	// 路径（含内部服务调用）都不得绕过。
	if !types.CallerFromContext(ctx).Role.HasPermission(types.TenantRoleAdmin) {
		return nil, ErrSchemaProposalForbidden
	}
	proposal, err := s.loadProposal(ctx, tenantID, proposalID)
	if err != nil {
		return nil, err
	}
	if !proposal.Status.CanTransitionTo(s.aischemaTargetStatus(approve)) {
		return nil, fmt.Errorf("%w: %s -> %s", ErrSchemaProposalInvalidState, proposal.Status, s.aischemaTargetStatus(approve))
	}
	// 四眼原则：提案创建人不能审批自己的提案。
	if reviewerID == proposal.CreatedBy {
		return nil, ErrSchemaProposalSelfReview
	}

	from := proposal.Status
	if approve {
		proposal.Status = types.SchemaProposalStatusApproved
	} else {
		proposal.Status = types.SchemaProposalStatusRejected
	}
	proposal.ReviewedBy = reviewerID
	proposal.ReviewComment = strings.TrimSpace(comment)
	if err := s.repo.Update(ctx, proposal); err != nil {
		return nil, err
	}

	action := types.OntologyActionSchemaProposalApproved
	if !approve {
		action = types.OntologyActionSchemaProposalRejected
	}
	s.aischemaAudit(ctx, tenantID, reviewerID, action, proposal.ID, map[string]any{
		"from":     string(from),
		"to":       string(proposal.Status),
		"reviewer": reviewerID,
		"creator":  proposal.CreatedBy,
		"comment":  truncateSchemaProposalNote(proposal.ReviewComment),
	})
	return proposal, nil
}

// aischemaTargetStatus 审批方向 → 目标状态（批准 approved / 驳回 rejected）。
func (s *schemaProposalService) aischemaTargetStatus(approve bool) types.SchemaProposalStatus {
	if approve {
		return types.SchemaProposalStatusApproved
	}
	return types.SchemaProposalStatusRejected
}

// ============================================================================
// 落地（Apply）
// ============================================================================

// Apply 把已批准的提案落成正式本体：
//  1. 状态必须 approved；兼容性报告必须存在且无 blocker（未检查过就先补跑
//     一次检查——不能靠"没检查"绕过门禁）；
//  2. 目标 schema：同名 draft → 追加（并在结果里提示发布即版本升级）；同名
//     已发布/归档 → 派生 <name> v<version+1> 的新 draft（既有发布件不可变）；
//     无同名 → 新建 draft；
//  3. 通过 OntologyService.CreateSchema/AddType/AddProperty/AddRelation 逐个
//     落地，已存在的类型/属性/关系跳过并记入 Skipped；
//  4. 回填 applied_schema_id、status=applied，写审计并返回版本升级提示。
func (s *schemaProposalService) Apply(
	ctx context.Context, tenantID uint64, proposalID, actorID string,
) (*types.SchemaProposalApplyResult, error) {
	tenantID, actorID, err := s.resolveIdentity(ctx, tenantID, actorID)
	if err != nil {
		return nil, err
	}
	proposal, err := s.loadProposal(ctx, tenantID, proposalID)
	if err != nil {
		return nil, err
	}
	if proposal.Status != types.SchemaProposalStatusApproved {
		return nil, fmt.Errorf("%w: only approved proposals can be applied (current: %s)",
			ErrSchemaProposalInvalidState, proposal.Status)
	}
	payload, err := proposal.PayloadStruct()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrSchemaProposalInvalidPayload, err.Error())
	}
	payload.Normalize()
	if err := payload.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrSchemaProposalInvalidPayload, err.Error())
	}

	// 门禁：compatible 且无 blocker。报告从未跑过（Issues 空且 Compatible
	// false 且 ProposalID 空）时补跑，避免"跳过检查 = 直通"。
	report, err := proposal.CompatibilityReportStruct()
	if err != nil {
		return nil, err
	}
	if report.ProposalID == "" {
		report, err = s.CheckCompatibility(ctx, tenantID, proposal.ID)
		if err != nil {
			return nil, err
		}
		// CheckCompatibility 已把报告写回库，重新加载以拿到最新行。
		if proposal, err = s.loadProposal(ctx, tenantID, proposal.ID); err != nil {
			return nil, err
		}
	}
	if report.HasBlocker() {
		return nil, fmt.Errorf("%w: %s", ErrSchemaProposalCompatibilityBlocker, aischemaBlockerSummary(report))
	}

	target, schemaCreated, versionUpgrade, carryFrom, err := s.aischemaResolveTargetSchema(ctx, tenantID, proposal, actorID)
	if err != nil {
		return nil, err
	}

	result := &types.SchemaProposalApplyResult{
		Proposal:               proposal,
		SchemaID:               target.SchemaID,
		SchemaName:             target.SchemaName,
		SchemaVersion:          target.SchemaVersion,
		SchemaCreated:          schemaCreated,
		VersionUpgradeRequired: versionUpgrade,
		Skipped:                []string{},
	}

	// 派生新版本时先把上一发布版本的结构整体继承过来（types/properties/
	// relations），否则"v2"会只剩本次提案新增的部分，退回成残缺本体。
	if carryFrom != "" {
		carried, carriedRelations, err := s.aischemaCarryOver(ctx, tenantID, target.SchemaID, carryFrom, actorID)
		if err != nil {
			return nil, err
		}
		result.CarriedOverTypes = carried
		result.CarriedOverRelations = carriedRelations
	}

	// 目标 schema 现有结构（用于跳过已存在元素并复用 type ID）。
	detail, err := s.ontology.GetSchema(ctx, tenantID, target.SchemaID)
	if err != nil {
		return nil, err
	}
	typeIDs := make(map[string]string, len(detail.Types))
	existingProps := make(map[string]map[string]struct{}, len(detail.Types))
	for _, t := range detail.Types {
		if t == nil {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(t.Name))
		typeIDs[key] = t.ID
		props := make(map[string]struct{}, len(t.Properties))
		for _, prop := range t.Properties {
			if prop == nil {
				continue
			}
			props[strings.ToLower(prop.Name)] = struct{}{}
		}
		existingProps[key] = props
	}
	existingRelations := make(map[string]struct{}, len(detail.Relations))
	for _, rel := range detail.Relations {
		if rel == nil {
			continue
		}
		existingRelations[strings.ToLower(rel.Name)] = struct{}{}
	}

	// ---- 类型与属性 ----
	for _, t := range payload.Types {
		key := strings.ToLower(t.Name)
		typeID, exists := typeIDs[key]
		if exists {
			result.Skipped = append(result.Skipped, "type:"+t.Name)
		} else {
			aliasesRaw, err := json.Marshal(aischemaAliasList(t.Aliases))
			if err != nil {
				return nil, fmt.Errorf("%w: marshal aliases for %s: %s", ErrSchemaProposalApplyFailed, t.Name, err.Error())
			}
			created, err := s.ontology.AddType(ctx, tenantID, &types.OntologyType{
				SchemaID:    target.SchemaID,
				Name:        t.Name,
				DisplayName: t.DisplayName,
				Description: t.Description,
				Aliases:     types.JSON(aliasesRaw),
			}, actorID)
			if err != nil {
				return nil, fmt.Errorf("%w: add type %s: %s", ErrSchemaProposalApplyFailed, t.Name, err.Error())
			}
			typeIDs[key] = created.ID
			typeID = created.ID
			existingProps[key] = map[string]struct{}{}
			result.TypesAdded++
		}

		for _, prop := range t.Properties {
			propKey := strings.ToLower(prop.Name)
			if _, ok := existingProps[key][propKey]; ok {
				result.Skipped = append(result.Skipped, "property:"+t.Name+"."+prop.Name)
				continue
			}
			if _, err := s.ontology.AddProperty(ctx, tenantID, &types.OntologyProperty{
				TypeID:   typeID,
				Name:     prop.Name,
				DataType: types.OntologyPropertyDataType(prop.DataType),
				Required: prop.Required,
			}, actorID); err != nil {
				return nil, fmt.Errorf("%w: add property %s.%s: %s",
					ErrSchemaProposalApplyFailed, t.Name, prop.Name, err.Error())
			}
			existingProps[key][propKey] = struct{}{}
			result.PropertiesAdded++
		}
	}

	// ---- 关系 ----
	for _, rel := range payload.Relations {
		relKey := strings.ToLower(rel.Name)
		if _, ok := existingRelations[relKey]; ok {
			result.Skipped = append(result.Skipped, "relation:"+rel.Name)
			continue
		}
		sourceID, ok := typeIDs[strings.ToLower(rel.SourceType)]
		if !ok {
			return nil, fmt.Errorf("%w: relation %s source type %s is missing from schema %s",
				ErrSchemaProposalApplyFailed, rel.Name, rel.SourceType, target.SchemaName)
		}
		targetID, ok := typeIDs[strings.ToLower(rel.TargetType)]
		if !ok {
			return nil, fmt.Errorf("%w: relation %s target type %s is missing from schema %s",
				ErrSchemaProposalApplyFailed, rel.Name, rel.TargetType, target.SchemaName)
		}
		if _, err := s.ontology.AddRelation(ctx, tenantID, &types.OntologyRelation{
			SchemaID:     target.SchemaID,
			Name:         rel.Name,
			SourceTypeID: sourceID,
			TargetTypeID: targetID,
			Cardinality:  types.OntologyRelationCardinality(rel.Cardinality).Normalize(),
			Description:  rel.Description,
		}, actorID); err != nil {
			return nil, fmt.Errorf("%w: add relation %s: %s", ErrSchemaProposalApplyFailed, rel.Name, err.Error())
		}
		existingRelations[relKey] = struct{}{}
		result.RelationsAdded++
	}

	proposal.Status = types.SchemaProposalStatusApplied
	proposal.AppliedSchemaID = target.SchemaID
	if err := s.repo.Update(ctx, proposal); err != nil {
		return nil, err
	}
	result.Proposal = proposal

	s.aischemaAudit(ctx, tenantID, actorID, types.OntologyActionSchemaProposalApplied, proposal.ID, map[string]any{
		"applied_schema_id":        target.SchemaID,
		"applied_schema_name":      target.SchemaName,
		"schema_created":           schemaCreated,
		"version_upgrade_required": versionUpgrade,
		"types_added":              result.TypesAdded,
		"properties_added":         result.PropertiesAdded,
		"relations_added":          result.RelationsAdded,
		"skipped":                  len(result.Skipped),
	})
	return result, nil
}

// aischemaTarget 是 Apply 解析出的目标 schema 位置。
type aischemaTarget struct {
	SchemaID      string
	SchemaName    string
	SchemaVersion int
}

// aischemaResolveTargetSchema 决定提案落到哪个 schema：
//   - 无同名 → 新建 draft（version 1），无继承来源；
//   - 同名 draft → 直接追加，提示"发布即版本升级"，无继承来源；
//   - 同名已发布/归档 → 派生 "<name> v<version+1>" 的新 draft（发布件不可变，
//     且 name 在租户内唯一，不能同名再建一行），并把原 schema 作为继承来源
//     返回给调用方。
//
// 返回值依次为：目标位置、是否新建了 schema、是否需要在 UI 提示版本升级、
// 继承来源 schema ID（空 = 无需继承）。
func (s *schemaProposalService) aischemaResolveTargetSchema(
	ctx context.Context, tenantID uint64, proposal *types.SchemaProposal, actorID string,
) (*aischemaTarget, bool, bool, string, error) {
	schemas, err := s.ontology.ListSchemas(ctx, tenantID)
	if err != nil {
		return nil, false, false, "", err
	}
	var sameName *types.OntologySchema
	nameSet := make(map[string]struct{}, len(schemas))
	for _, schema := range schemas {
		if schema == nil {
			continue
		}
		nameSet[strings.ToLower(schema.Name)] = struct{}{}
		if strings.EqualFold(schema.Name, proposal.Name) {
			sameName = schema
		}
	}

	description := aischemaSchemaDescription(proposal)
	if sameName == nil {
		created, err := s.ontology.CreateSchema(ctx, &types.OntologySchema{
			TenantID:    tenantID,
			Name:        proposal.Name,
			Description: description,
			CreatedBy:   actorID,
		}, actorID)
		if err != nil {
			return nil, false, false, "", err
		}
		return &aischemaTarget{SchemaID: created.ID, SchemaName: created.Name, SchemaVersion: created.Version}, true, false, "", nil
	}
	if sameName.Status.Editable() {
		return &aischemaTarget{
			SchemaID:      sameName.ID,
			SchemaName:    sameName.Name,
			SchemaVersion: sameName.Version,
		}, false, true, "", nil
	}

	// 已发布/归档：派生下一版本名（name 在租户内唯一，同名不可再建）。
	base := fmt.Sprintf("%s v%d", sameName.Name, sameName.Version+1)
	name := base
	for suffix := 2; ; suffix++ {
		if _, taken := nameSet[strings.ToLower(name)]; !taken {
			break
		}
		name = fmt.Sprintf("%s-%d", base, suffix)
	}
	if len([]rune(name)) > schemaProposalNameMaxRunes {
		return nil, false, false, "", fmt.Errorf("%w: derived schema name %q exceeds %d characters",
			ErrSchemaProposalTargetSchemaImmutable, name, schemaProposalNameMaxRunes)
	}
	created, err := s.ontology.CreateSchema(ctx, &types.OntologySchema{
		TenantID:    tenantID,
		Name:        name,
		Description: description,
		Version:     sameName.Version + 1,
		CreatedBy:   actorID,
	}, actorID)
	if err != nil {
		return nil, false, false, "", err
	}
	return &aischemaTarget{SchemaID: created.ID, SchemaName: created.Name, SchemaVersion: created.Version},
		true, true, sameName.ID, nil
}

// aischemaCarryOver 把来源 schema（上一发布版本）的类型/属性/关系整体复制到
// 新 draft 版本，返回复制的类型与关系数量。关系端点用"按名字重新映射后的
// 新类型 ID"连接，保证两端仍命中同一 schema（KAG 谓词模型）。
func (s *schemaProposalService) aischemaCarryOver(
	ctx context.Context, tenantID uint64, targetSchemaID, sourceSchemaID, actorID string,
) (int, int, error) {
	source, err := s.ontology.GetSchema(ctx, tenantID, sourceSchemaID)
	if err != nil {
		return 0, 0, err
	}
	// 来源类型名 → 新 schema 中的类型 ID。
	typeIDs := make(map[string]string, len(source.Types))
	typesCarried := 0
	for _, t := range source.Types {
		if t == nil {
			continue
		}
		aliases := t.Aliases
		if strings.TrimSpace(string(aliases)) == "" || strings.TrimSpace(string(aliases)) == "null" {
			aliases = types.JSON(`[]`)
		}
		created, err := s.ontology.AddType(ctx, tenantID, &types.OntologyType{
			SchemaID:    targetSchemaID,
			Name:        t.Name,
			DisplayName: t.DisplayName,
			Description: t.Description,
			Aliases:     aliases,
			Config:      t.Config,
		}, actorID)
		if err != nil {
			return 0, 0, fmt.Errorf("%w: carry over type %s: %s", ErrSchemaProposalApplyFailed, t.Name, err.Error())
		}
		typeIDs[strings.ToLower(strings.TrimSpace(t.Name))] = created.ID
		typesCarried++
		for _, prop := range t.Properties {
			if prop == nil {
				continue
			}
			if _, err := s.ontology.AddProperty(ctx, tenantID, &types.OntologyProperty{
				TypeID:   created.ID,
				Name:     prop.Name,
				DataType: prop.DataType,
				Required: prop.Required,
				Config:   prop.Config,
			}, actorID); err != nil {
				return 0, 0, fmt.Errorf("%w: carry over property %s.%s: %s",
					ErrSchemaProposalApplyFailed, t.Name, prop.Name, err.Error())
			}
		}
	}

	relationsCarried := 0
	// 来源关系的端点 ID 属于来源 schema，需要按名字映射到新类型。
	sourceTypeNames := make(map[string]string, len(source.Types))
	for _, t := range source.Types {
		if t != nil {
			sourceTypeNames[t.ID] = strings.ToLower(strings.TrimSpace(t.Name))
		}
	}
	for _, rel := range source.Relations {
		if rel == nil {
			continue
		}
		sourceID, ok := typeIDs[sourceTypeNames[rel.SourceTypeID]]
		if !ok {
			continue
		}
		targetID, ok := typeIDs[sourceTypeNames[rel.TargetTypeID]]
		if !ok {
			continue
		}
		if _, err := s.ontology.AddRelation(ctx, tenantID, &types.OntologyRelation{
			SchemaID:     targetSchemaID,
			Name:         rel.Name,
			SourceTypeID: sourceID,
			TargetTypeID: targetID,
			Cardinality:  rel.Cardinality,
			Description:  rel.Description,
		}, actorID); err != nil {
			return 0, 0, fmt.Errorf("%w: carry over relation %s: %s", ErrSchemaProposalApplyFailed, rel.Name, err.Error())
		}
		relationsCarried++
	}
	return typesCarried, relationsCarried, nil
}

// ============================================================================
// 查询
// ============================================================================

// List 返回租户提案列表（可按 status 过滤）。
func (s *schemaProposalService) List(
	ctx context.Context, tenantID uint64, filter types.SchemaProposalListFilter,
) ([]*types.SchemaProposal, int64, error) {
	tenantID, _, err := s.resolveIdentity(ctx, tenantID, "")
	if err != nil {
		return nil, 0, err
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		if !types.SchemaProposalStatus(status).Valid() {
			return nil, 0, fmt.Errorf("%w: unknown status %q", ErrSchemaProposalInvalidState, status)
		}
		filter.Status = status
	}
	return s.repo.List(ctx, tenantID, filter)
}

// Get 返回单条提案（租户隔离）。
func (s *schemaProposalService) Get(
	ctx context.Context, tenantID uint64, proposalID string,
) (*types.SchemaProposal, error) {
	tenantID, _, err := s.resolveIdentity(ctx, tenantID, "")
	if err != nil {
		return nil, err
	}
	return s.loadProposal(ctx, tenantID, proposalID)
}

// ============================================================================
// 内部助手
// ============================================================================

// resolveIdentity 统一身份解析：租户与用户缺失即报错。tenantID 为 0 时回退
// 到 ctx（handlers 传 0 也能工作），actorID 为空时同样回退到 ctx。
func (s *schemaProposalService) resolveIdentity(
	ctx context.Context, tenantID uint64, actorID string,
) (uint64, string, error) {
	if tenantID == 0 {
		tenantID, _ = types.TenantIDFromContext(ctx)
	}
	if tenantID == 0 {
		return 0, "", ErrSchemaProposalTenantMissing
	}
	if actorID == "" {
		actorID = types.CallerFromContext(ctx).UserID
	}
	return tenantID, actorID, nil
}

// loadProposal 按租户加载提案，not-found 映射为业务哨兵错误。
func (s *schemaProposalService) loadProposal(
	ctx context.Context, tenantID uint64, proposalID string,
) (*types.SchemaProposal, error) {
	if strings.TrimSpace(proposalID) == "" {
		return nil, ErrSchemaProposalNotFound
	}
	proposal, err := s.repo.GetByID(ctx, tenantID, proposalID)
	if err != nil {
		if errors.Is(err, repository.ErrSchemaProposalNotFound) {
			return nil, ErrSchemaProposalNotFound
		}
		return nil, err
	}
	if proposal == nil {
		return nil, ErrSchemaProposalNotFound
	}
	return proposal, nil
}

// aischemaInputDocument 是一篇送入模型的文档（已截断）。
type aischemaInputDocument struct {
	KnowledgeID string
	Title       string
	FileType    string
	Content     string
}

// aischemaLoadDocuments 逐篇取正文：chunk 文本优先（text + OCR + 图注，按
// 位置排序），无 chunk 时回落到 manual 正文/知识描述。任一篇取不到正文即
// 报错——缺文档却"安静地少看一篇"会让提案依据与审计不符。
func (s *schemaProposalService) aischemaLoadDocuments(
	ctx context.Context, tenantID uint64, knowledgeIDs []string,
) ([]aischemaInputDocument, error) {
	documents := make([]aischemaInputDocument, 0, len(knowledgeIDs))
	for _, knowledgeID := range knowledgeIDs {
		var knowledge *types.Knowledge
		if s.knowledges != nil {
			loaded, err := s.knowledges.GetKnowledgeByID(ctx, tenantID, knowledgeID)
			if err != nil {
				if errors.Is(err, repository.ErrKnowledgeNotFound) {
					return nil, fmt.Errorf("%w: %s", ErrSchemaProposalDocumentNotFound, knowledgeID)
				}
				return nil, err
			}
			knowledge = loaded
		}
		if knowledge == nil {
			return nil, fmt.Errorf("%w: %s", ErrSchemaProposalDocumentNotFound, knowledgeID)
		}

		content := s.aischemaKnowledgeText(ctx, tenantID, knowledge)
		if strings.TrimSpace(content) == "" {
			return nil, fmt.Errorf("%w: %s", ErrSchemaProposalDocumentEmpty, knowledgeID)
		}
		documents = append(documents, aischemaInputDocument{
			KnowledgeID: knowledge.ID,
			Title:       aischemaDocumentTitle(knowledge),
			FileType:    knowledge.FileType,
			Content:     aischemaTruncateRunes(content, schemaProposalDocContentRunes),
		})
	}
	return documents, nil
}

// aischemaKnowledgeText 拼接一篇文档的可用正文：chunk 文本优先，其次 manual
// 正文，最后知识描述。
func (s *schemaProposalService) aischemaKnowledgeText(
	ctx context.Context, tenantID uint64, knowledge *types.Knowledge,
) string {
	if s.chunks != nil {
		chunks, err := s.chunks.ListChunksByKnowledgeIDAndTypes(ctx, tenantID, knowledge.ID,
			[]types.ChunkType{types.ChunkTypeText, types.ChunkTypeImageOCR, types.ChunkTypeImageCaption})
		if err != nil {
			logger.Warnf(ctx, "schema proposal: list chunks for knowledge %s failed: %v", knowledge.ID, err)
		}
		if text := aischemaJoinChunks(chunks); strings.TrimSpace(text) != "" {
			return text
		}
	}
	if knowledge.IsManual() {
		if meta, err := knowledge.ManualMetadata(); err == nil && meta != nil {
			if text := strings.TrimSpace(meta.Content); text != "" {
				return text
			}
		}
	}
	return strings.TrimSpace(knowledge.Description)
}

// aischemaPickChatModel 选择结构化抽取用的 chat 模型：租户默认 KnowledgeQA
// 模型（types.PickChatModel）。
func (s *schemaProposalService) aischemaPickChatModel(ctx context.Context) (string, chat.Chat, error) {
	models, err := s.models.ListModels(ctx)
	if err != nil {
		return "", nil, err
	}
	picked := types.PickChatModel(models, "")
	if picked == nil {
		return "", nil, ErrSchemaProposalModelMissing
	}
	model, err := s.models.GetChatModel(ctx, picked.ID)
	if err != nil {
		return "", nil, err
	}
	if model == nil {
		return "", nil, ErrSchemaProposalModelMissing
	}
	return picked.ID, model, nil
}

// aischemaExtractPayload 调一次 chat 模型做结构化抽取，解析失败即 400。
// rationale 取模型返回的 rationale 字段（缺省时由调用方拼一句兜底文案）。
func (s *schemaProposalService) aischemaExtractPayload(
	ctx context.Context,
	model chat.Chat,
	modelID, name, domain string,
	documents []aischemaInputDocument,
) (*types.ProposalPayload, string, error) {
	if domain == "" {
		domain = "general"
	}
	var builder strings.Builder
	for index, doc := range documents {
		fmt.Fprintf(&builder, aischemaDocumentPromptTemplate, index+1, doc.Title, doc.FileType, doc.Content)
	}
	userPrompt := fmt.Sprintf(aischemaUserPromptTemplate, domain, name, len(documents), builder.String())

	thinking := false
	response, err := model.Chat(ctx, []chat.Message{
		{Role: "system", Content: aischemaSystemPrompt},
		{Role: "user", Content: userPrompt},
	}, &chat.ChatOptions{
		Temperature:         schemaProposalModelTemperature,
		MaxCompletionTokens: schemaProposalModelMaxTokens,
		Thinking:            &thinking,
	})
	if err != nil {
		return nil, "", fmt.Errorf("schema proposal: model call failed (model=%s): %w", modelID, err)
	}
	if response == nil {
		return nil, "", fmt.Errorf("%w: the model returned no response", ErrSchemaProposalModelOutput)
	}

	var envelope struct {
		Types     []types.ProposalType     `json:"types"`
		Relations []types.ProposalRelation `json:"relations"`
		Rationale string                   `json:"rationale"`
	}
	raw := aischemaExtractJSONObject(response.Content)
	if raw == "" {
		return nil, "", fmt.Errorf("%w: no JSON object found in the model output", ErrSchemaProposalModelOutput)
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return nil, "", fmt.Errorf("%w: %s", ErrSchemaProposalModelOutput, err.Error())
	}
	payload := &types.ProposalPayload{Types: envelope.Types, Relations: envelope.Relations}
	payload.Normalize()
	if err := payload.Validate(); err != nil {
		return nil, "", fmt.Errorf("%w: %s", ErrSchemaProposalModelOutput, err.Error())
	}

	rationale := strings.TrimSpace(envelope.Rationale)
	if rationale == "" {
		rationale = fmt.Sprintf(
			"Model %s proposed %d types and %d relations from %d document(s).",
			modelID, len(payload.Types), len(payload.Relations), len(documents))
	}
	return payload, rationale, nil
}

// aischemaAudit 写一条提案审计事件（best-effort：审计故障不回滚业务，与
// ontology/review 服务的失败语义一致）。
func (s *schemaProposalService) aischemaAudit(
	ctx context.Context, tenantID uint64, actorID string,
	action types.AuditAction, proposalID string, details map[string]any,
) {
	if s.audit == nil {
		return
	}
	if err := s.audit.Log(ctx, &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: actorID,
		Action:      action,
		ScopeType:   "ontology",
		ScopeID:     proposalID,
		TargetType:  schemaProposalAuditScope,
		TargetID:    proposalID,
		Outcome:     "success",
		Details:     lifecycleJSON(details),
	}); err != nil {
		logger.Warnf(ctx, "schema proposal audit log failed (action=%s): %v", action, err)
	}
}

// ============================================================================
// 包内纯函数助手（aischema 前缀）
// ============================================================================

// aischemaValidateIdentity 校验并规范化提案名/domain。
func aischemaValidateIdentity(name, domain string) (string, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", ErrSchemaProposalNameRequired
	}
	if len([]rune(name)) > schemaProposalNameMaxRunes {
		return "", "", fmt.Errorf("%w: name exceeds %d characters", ErrSchemaProposalNameRequired, schemaProposalNameMaxRunes)
	}
	domain = strings.TrimSpace(domain)
	if len([]rune(domain)) > schemaProposalDomainMaxRunes {
		domain = string([]rune(domain)[:schemaProposalDomainMaxRunes])
	}
	return name, domain, nil
}

// aischemaNormalizeIDs 去空白、去空串并保持顺序去重。
func aischemaNormalizeIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// aischemaJoinChunks 按文档位置拼接 chunk 正文（仅三种文本类 chunk）。
func aischemaJoinChunks(chunks []*types.Chunk) string {
	ordered := make([]*types.Chunk, 0, len(chunks))
	for _, chunk := range chunks {
		if chunk == nil {
			continue
		}
		switch chunk.ChunkType {
		case types.ChunkTypeText, types.ChunkTypeImageOCR, types.ChunkTypeImageCaption:
			ordered = append(ordered, chunk)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].StartAt != ordered[j].StartAt {
			return ordered[i].StartAt < ordered[j].StartAt
		}
		return ordered[i].ChunkIndex < ordered[j].ChunkIndex
	})
	parts := make([]string, 0, len(ordered))
	for _, chunk := range ordered {
		if text := strings.TrimSpace(chunk.Content); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// aischemaDocumentTitle 文档标题（回落到文件名/ID）。
func aischemaDocumentTitle(knowledge *types.Knowledge) string {
	for _, candidate := range []string{knowledge.Title, knowledge.FileName, knowledge.ID} {
		if title := strings.TrimSpace(candidate); title != "" {
			return title
		}
	}
	return "untitled"
}

// aischemaTruncateRunes 按字符（rune）截断，避免 UTF-8 截半。
func aischemaTruncateRunes(text string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit])
}

// aischemaExtractJSONObject 从模型输出中取出第一个完整 JSON 对象：容忍
// markdown 代码围栏与前后解释性文字，按括号深度扫描并以字符串状态跳过
// 引号内的花括号。
func aischemaExtractJSONObject(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	start := strings.Index(text, "{")
	if start < 0 {
		return ""
	}
	depth := 0
	inString := false
	escaped := false
	for index := start; index < len(text); index++ {
		ch := text[index]
		if inString {
			switch {
			case escaped:
				escaped = false
			case ch == '\\':
				escaped = true
			case ch == '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start : index+1]
			}
		}
	}
	return ""
}

// aischemaAliasList 保证别名 JSON 列至少是空数组（不写 null）。
func aischemaAliasList(aliases []string) []string {
	if len(aliases) == 0 {
		return []string{}
	}
	return aliases
}

// aischemaSchemaDescription 由提案生成目标 schema 的描述（保留动机，便于
// 后续在 schema 列表里追溯"这条本体是怎么来的"）。
func aischemaSchemaDescription(proposal *types.SchemaProposal) string {
	rationale := strings.TrimSpace(proposal.Rationale)
	if rationale == "" {
		return "Created from schema proposal " + proposal.ID
	}
	return aischemaTruncateRunes(rationale, 2000)
}

// aischemaPropertyDelta 描述同名属性的定义差异（空串 = 无差异）。
func aischemaPropertyDelta(existing *types.OntologyProperty, proposed types.ProposalProperty) string {
	diffs := make([]string, 0, 2)
	if string(existing.DataType) != proposed.DataType {
		diffs = append(diffs, fmt.Sprintf("data_type %s -> %s", existing.DataType, proposed.DataType))
	}
	if existing.Required != proposed.Required {
		diffs = append(diffs, fmt.Sprintf("required %t -> %t", existing.Required, proposed.Required))
	}
	return strings.Join(diffs, "; ")
}

// aischemaDetectRename 判断提案类型是否疑似现网某类型的改名：名字归一化
// （小写、去下划线/连字符/空格）后相同但原写法不同，或别名彼此命中。
func aischemaDetectRename(proposed types.ProposalType, existing map[string]*types.OntologyTypeDetail) *types.CompatibilityRename {
	normalized := aischemaNormalizeName(proposed.Name)
	proposedAliases := make(map[string]struct{}, len(proposed.Aliases))
	for _, alias := range proposed.Aliases {
		proposedAliases[aischemaNormalizeName(alias)] = struct{}{}
	}
	for key, current := range existing {
		if current == nil {
			continue
		}
		if strings.EqualFold(current.Name, proposed.Name) {
			continue
		}
		if aischemaNormalizeName(current.Name) == normalized {
			return &types.CompatibilityRename{From: current.Name, To: proposed.Name}
		}
		if _, hit := proposedAliases[key]; hit {
			return &types.CompatibilityRename{From: current.Name, To: proposed.Name}
		}
		for _, alias := range aischemaAliasesOf(current) {
			alias = aischemaNormalizeName(alias)
			if alias == "" {
				continue
			}
			if alias == normalized {
				return &types.CompatibilityRename{From: current.Name, To: proposed.Name}
			}
			if _, hit := proposedAliases[alias]; hit {
				return &types.CompatibilityRename{From: current.Name, To: proposed.Name}
			}
		}
	}
	return nil
}

// aischemaAliasesOf 解析既有类型的 aliases JSONB（非法/空返回 nil）。
func aischemaAliasesOf(t *types.OntologyTypeDetail) []string {
	raw := strings.TrimSpace(string(t.Aliases))
	if raw == "" || raw == "null" {
		return nil
	}
	var aliases []string
	if err := json.Unmarshal(t.Aliases, &aliases); err != nil {
		return nil
	}
	return aliases
}

// aischemaNormalizeName 名字归一：小写 + 去掉下划线/连字符/空格/点。
func aischemaNormalizeName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	replacer := strings.NewReplacer("_", "", "-", "", " ", "", ".", "")
	return replacer.Replace(name)
}

// aischemaBlockerSummary 把 blocker 压成一句可读的错误明细。
func aischemaBlockerSummary(report *types.CompatibilityReport) string {
	if report == nil {
		return "compatibility report is missing"
	}
	parts := make([]string, 0, 4)
	for _, issue := range report.Issues {
		if issue.Severity != types.CompatibilitySeverityBlocker {
			continue
		}
		parts = append(parts, issue.Message)
		if len(parts) == 3 {
			break
		}
	}
	if len(parts) == 0 {
		return "compatibility report is not compatible"
	}
	return strings.Join(parts, "; ")
}

// aischemaUniqueStrings 保序去重。
func aischemaUniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, dup := seen[value]; dup {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// truncateSchemaProposalNote 限制进入审计详情的评语长度。
func truncateSchemaProposalNote(note string) string {
	runes := []rune(note)
	if len(runes) <= schemaProposalAuditCommentLimit {
		return note
	}
	return string(runes[:schemaProposalAuditCommentLimit]) + "…"
}
