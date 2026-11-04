package types

import (
	"encoding/json"
	"fmt"
	"time"
)

// ============================================================================
// 过程经验自动沉淀 v1（KnowAxis V3.0 §10）：
// 候选卡片 + 审核发布闭环。
//
// 九类业务来源（§10.1）共用一张 knowledge_candidates 表：
//   - manual             手工创建（v1 主路径；连接器后续接 datasource 框架）
//   - work_order         工单
//   - project_close      项目结项
//   - quality_event      质量事件
//   - production_event   生产事件
//   - plm_change         PLM 变更
//   - customer_complaint 客户投诉
//   - meeting_review     会议评审
//   - process_change     流程变更
//
// 状态机（§10.2）：candidate → in_review → confirmed → published；
//   candidate|in_review → withdrawn（提交人撤回），in_review → rejected（驳回）。
//   candidate→in_review 由提交人触发；in_review→confirmed 由审核人触发
//   （v1 审核人 = Admin+）；confirmed→published 生成 manual 正式知识并回填
//   knowledge_id。level 按 §10.4 分层：L0 过程档案 / L1 候选 / L2 已确认 /
//   L3 权威；L3 发布有门禁（Admin 且 confidence != AMBIGUOUS）。
// ============================================================================

// KnowledgeCandidateSourceType 候选卡片的业务来源类型（九类，§10.1）。
type KnowledgeCandidateSourceType string

const (
	// KnowledgeCandidateSourceManual 手工创建。
	KnowledgeCandidateSourceManual KnowledgeCandidateSourceType = "manual"
	// KnowledgeCandidateSourceWorkOrder 工单。
	KnowledgeCandidateSourceWorkOrder KnowledgeCandidateSourceType = "work_order"
	// KnowledgeCandidateSourceProjectClose 项目结项。
	KnowledgeCandidateSourceProjectClose KnowledgeCandidateSourceType = "project_close"
	// KnowledgeCandidateSourceQualityEvent 质量事件。
	KnowledgeCandidateSourceQualityEvent KnowledgeCandidateSourceType = "quality_event"
	// KnowledgeCandidateSourceProductionEvent 生产事件。
	KnowledgeCandidateSourceProductionEvent KnowledgeCandidateSourceType = "production_event"
	// KnowledgeCandidateSourcePLMChange PLM 变更。
	KnowledgeCandidateSourcePLMChange KnowledgeCandidateSourceType = "plm_change"
	// KnowledgeCandidateSourceCustomerComplaint 客户投诉。
	KnowledgeCandidateSourceCustomerComplaint KnowledgeCandidateSourceType = "customer_complaint"
	// KnowledgeCandidateSourceMeetingReview 会议评审。
	KnowledgeCandidateSourceMeetingReview KnowledgeCandidateSourceType = "meeting_review"
	// KnowledgeCandidateSourceProcessChange 流程变更。
	KnowledgeCandidateSourceProcessChange KnowledgeCandidateSourceType = "process_change"
	// KnowledgeCandidateSourceQAAnswer 问答答案沉淀（§13 回答→知识）。
	//
	// 九类业务来源之外单独列一类，是因为它的**证据形态**不同：其余来源的
	// 证据是一张单据或一次评审，而这里是一条已经由模型生成、带引用清单的
	// 回答。混进 manual 会让"这条经验是怎么来的"在候选列表里看不出来。
	KnowledgeCandidateSourceQAAnswer KnowledgeCandidateSourceType = "qa_answer"
)

// Valid 报告来源类型是否在九类白名单内。
func (t KnowledgeCandidateSourceType) Valid() bool {
	switch t {
	case KnowledgeCandidateSourceManual, KnowledgeCandidateSourceWorkOrder,
		KnowledgeCandidateSourceProjectClose, KnowledgeCandidateSourceQualityEvent,
		KnowledgeCandidateSourceProductionEvent, KnowledgeCandidateSourcePLMChange,
		KnowledgeCandidateSourceCustomerComplaint, KnowledgeCandidateSourceMeetingReview,
		KnowledgeCandidateSourceProcessChange, KnowledgeCandidateSourceQAAnswer:
		return true
	default:
		return false
	}
}

// KnowledgeCandidateLevel 候选卡片的知识层级（§10.4）。层级刻画内容在沉淀
// 阶梯上的位置：确认（L1→L2）与发布门禁（L3）都围绕它流转。
type KnowledgeCandidateLevel string

const (
	// KnowledgeCandidateLevelL0 过程档案：原始记录留存，不进检索。
	KnowledgeCandidateLevelL0 KnowledgeCandidateLevel = "L0"
	// KnowledgeCandidateLevelL1 候选：默认层级，待审核确认。
	KnowledgeCandidateLevelL1 KnowledgeCandidateLevel = "L1"
	// KnowledgeCandidateLevelL2 已确认：审核通过的经验卡片。
	KnowledgeCandidateLevelL2 KnowledgeCandidateLevel = "L2"
	// KnowledgeCandidateLevelL3 权威：可作权威答案的高置信经验（发布门禁）。
	KnowledgeCandidateLevelL3 KnowledgeCandidateLevel = "L3"
)

// Valid 报告层级是否已知（L0/L1/L2/L3）。
func (l KnowledgeCandidateLevel) Valid() bool {
	switch l {
	case KnowledgeCandidateLevelL0, KnowledgeCandidateLevelL1,
		KnowledgeCandidateLevelL2, KnowledgeCandidateLevelL3:
		return true
	default:
		return false
	}
}

// KnowledgeCandidateConfidence 证据置信标注（§10.3）。EXTRACTED 表示逐字
// 取自证据；INFERRED 表示由证据推理得出；AMBIGUOUS 表示证据不足/存在歧义。
type KnowledgeCandidateConfidence string

const (
	// KnowledgeCandidateConfidenceExtracted 逐字提取自证据。
	KnowledgeCandidateConfidenceExtracted KnowledgeCandidateConfidence = "EXTRACTED"
	// KnowledgeCandidateConfidenceInferred 由证据推理得出。
	KnowledgeCandidateConfidenceInferred KnowledgeCandidateConfidence = "INFERRED"
	// KnowledgeCandidateConfidenceAmbiguous 证据不足或存在歧义。
	KnowledgeCandidateConfidenceAmbiguous KnowledgeCandidateConfidence = "AMBIGUOUS"
)

// Valid 报告置信标注是否在白名单内。
func (c KnowledgeCandidateConfidence) Valid() bool {
	switch c {
	case KnowledgeCandidateConfidenceExtracted, KnowledgeCandidateConfidenceInferred,
		KnowledgeCandidateConfidenceAmbiguous:
		return true
	default:
		return false
	}
}

// KnowledgeCandidateDedupStatus 查重结论（v1：同租户同标题精确匹配 +
// 标题 LIKE 相似启发）。pending 尚未查重；unique 无重复；duplicate 存在
// 高度相似候选（duplicate_of 指向它）；conflict 与既有候选/知识疑似矛盾。
type KnowledgeCandidateDedupStatus string

const (
	// KnowledgeCandidateDedupPending 未查重（新卡默认值）。
	KnowledgeCandidateDedupPending KnowledgeCandidateDedupStatus = "pending"
	// KnowledgeCandidateDedupUnique 未发现重复。
	KnowledgeCandidateDedupUnique KnowledgeCandidateDedupStatus = "unique"
	// KnowledgeCandidateDedupDuplicate 与既有候选标题精确重复。
	KnowledgeCandidateDedupDuplicate KnowledgeCandidateDedupStatus = "duplicate"
	// KnowledgeCandidateDedupConflict 与既有候选标题相似（可能矛盾/变体）。
	KnowledgeCandidateDedupConflict KnowledgeCandidateDedupStatus = "conflict"
)

// Valid 报告查重结论是否在白名单内。
func (d KnowledgeCandidateDedupStatus) Valid() bool {
	switch d {
	case KnowledgeCandidateDedupPending, KnowledgeCandidateDedupUnique,
		KnowledgeCandidateDedupDuplicate, KnowledgeCandidateDedupConflict:
		return true
	default:
		return false
	}
}

// KnowledgeCandidateStatus 候选卡片状态机（§10.2 v1）。单向推进，
// published/rejected/withdrawn 为终态。
type KnowledgeCandidateStatus string

const (
	// KnowledgeCandidateStatusCandidate 新建候选（未提交确认）。
	KnowledgeCandidateStatusCandidate KnowledgeCandidateStatus = "candidate"
	// KnowledgeCandidateStatusInReview 提交人已确认，待审核人裁决。
	KnowledgeCandidateStatusInReview KnowledgeCandidateStatus = "in_review"
	// KnowledgeCandidateStatusConfirmed 审核通过（L1→L2），待发布。
	KnowledgeCandidateStatusConfirmed KnowledgeCandidateStatus = "confirmed"
	// KnowledgeCandidateStatusPublished 已发布为正式知识（终态）。
	KnowledgeCandidateStatusPublished KnowledgeCandidateStatus = "published"
	// KnowledgeCandidateStatusRejected 审核驳回（终态，留痕可查）。
	KnowledgeCandidateStatusRejected KnowledgeCandidateStatus = "rejected"
	// KnowledgeCandidateStatusWithdrawn 提交人撤回（终态，留痕可查）。
	KnowledgeCandidateStatusWithdrawn KnowledgeCandidateStatus = "withdrawn"
)

// candidateTransitions 是候选卡片状态机的合法迁移表。
var candidateTransitions = map[KnowledgeCandidateStatus][]KnowledgeCandidateStatus{
	KnowledgeCandidateStatusCandidate: {KnowledgeCandidateStatusInReview, KnowledgeCandidateStatusWithdrawn},
	KnowledgeCandidateStatusInReview:  {KnowledgeCandidateStatusConfirmed, KnowledgeCandidateStatusRejected, KnowledgeCandidateStatusWithdrawn},
	KnowledgeCandidateStatusConfirmed: {KnowledgeCandidateStatusPublished},
	KnowledgeCandidateStatusPublished: {},
	KnowledgeCandidateStatusRejected:  {},
	KnowledgeCandidateStatusWithdrawn: {},
}

// CanTransitionTo 报告从当前状态迁入 next 是否合法。
func (s KnowledgeCandidateStatus) CanTransitionTo(next KnowledgeCandidateStatus) bool {
	if s == next {
		return false
	}
	for _, allowed := range candidateTransitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// Valid 报告状态是否已知。
func (s KnowledgeCandidateStatus) Valid() bool {
	switch s {
	case KnowledgeCandidateStatusCandidate, KnowledgeCandidateStatusInReview,
		KnowledgeCandidateStatusConfirmed, KnowledgeCandidateStatusPublished,
		KnowledgeCandidateStatusRejected, KnowledgeCandidateStatusWithdrawn:
		return true
	default:
		return false
	}
}

// String 渲染状态用于日志。
func (s KnowledgeCandidateStatus) String() string { return string(s) }

// KnowledgeCandidateCard 是候选卡片本体（§10.3 的七段结构）。序列化进
// knowledge_candidates.card JSONB；发布时按固定顺序拼成 Markdown 正文。
type KnowledgeCandidateCard struct {
	// What 一句话结论（这张卡沉淀了什么经验）。
	What string `json:"what"`
	// Scope 适用范围（机型/工位/工序/工况等边界条件）。
	Scope string `json:"scope"`
	// Phenomenon 现象/触发条件（什么情况下适用）。
	Phenomenon string `json:"phenomenon"`
	// CauseCandidate 疑似原因（候选≠定论，措辞保留不确定性）。
	CauseCandidate string `json:"cause_candidate"`
	// Measures 处置措施（有序步骤）。
	Measures []string `json:"measures"`
	// Validation 验证情况（如何验证有效、由谁验证）。
	Validation string `json:"validation"`
	// EvidenceLinks 补充证据链接/单号（与表级 evidence 结构化引用互补）。
	EvidenceLinks []string `json:"evidence_links"`
}

// KnowledgeCandidateEvidence 是一条结构化证据引用（knowledge_candidates.
// evidence JSONB 数组的元素）。source_type 复用候选九类来源 + knowledge
// /chunk 等系统内来源；locator 指向来源内位置（行号/URL/章节）。
type KnowledgeCandidateEvidence struct {
	SourceType string `json:"source_type"`
	SourceID   string `json:"source_id"`
	Locator    string `json:"locator"`
	Quote      string `json:"quote"`
}

// 候选卡片审计动作（action 命名沿用 `<area>.<event>` 点分风格，与审核流的
// review.* 常量并列）。定义在类型文件内让垂直切片自包含。
const (
	// AuditActionCandidateCreated 候选卡片创建（含查重结论）。
	AuditActionCandidateCreated AuditAction = "candidate.created"
	// AuditActionCandidateConfirmed 状态推进/确认（candidate→in_review 或
	// in_review→confirmed，details 里带 from/to 区分）。
	AuditActionCandidateConfirmed AuditAction = "candidate.confirmed"
	// AuditActionCandidatePublished 发布为正式知识（details 带 knowledge_id）。
	AuditActionCandidatePublished AuditAction = "candidate.published"
	// AuditActionCandidateRejected 审核驳回。
	AuditActionCandidateRejected AuditAction = "candidate.rejected"
)

// KnowledgeCandidateMeta* 是发布时写进 knowledge.CustomMetadata 的键：
// 正式知识反查来源候选、层级与来源类型用。
const (
	// KnowledgeCandidateMetaLevelKey 正式知识承载的层级（L2/L3）。
	KnowledgeCandidateMetaLevelKey = "knowaxis_level"
	// KnowledgeCandidateMetaCandidateIDKey 来源候选卡片 ID。
	KnowledgeCandidateMetaCandidateIDKey = "knowaxis_candidate_id"
	// KnowledgeCandidateMetaSourceTypeKey 来源类型（九类之一）。
	KnowledgeCandidateMetaSourceTypeKey = "knowaxis_candidate_source_type"
)

// KnowledgeCandidate 一张过程经验候选卡片。
type KnowledgeCandidate struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null;index:idx_knowledge_candidates_tenant_status,priority:1;index:idx_knowledge_candidates_tenant_level,priority:1"`
	// SourceType 九类业务来源之一（manual|work_order|...）。
	SourceType string `json:"source_type" gorm:"type:varchar(24);not null;default:'manual'"`
	// SourceID 来源系统内的业务单据号（连接器回填；手工为空）。
	SourceID string `json:"source_id" gorm:"type:varchar(64);not null;default:''"`
	Title    string `json:"title" gorm:"type:varchar(255);not null"`
	// Level 知识层级 L0-L3（§10.4）；确认时 L1→L2，L3 发布有门禁。
	Level string `json:"level" gorm:"type:varchar(8);not null;default:'L1';index:idx_knowledge_candidates_tenant_level,priority:2"`
	// Card 候选卡片结构（what/scope/phenomenon/cause_candidate/measures/
	// validation/evidence_links）。
	Card JSON `json:"card" gorm:"type:json;not null;default:'{}'"`
	// Evidence 结构化证据引用 [{source_type,source_id,locator,quote}]。
	Evidence JSON `json:"evidence" gorm:"type:json;default:'[]'"`
	// Confidence 证据置信标注（EXTRACTED|INFERRED|AMBIGUOUS）。
	Confidence string `json:"confidence" gorm:"type:varchar(16);not null;default:'AMBIGUOUS'"`
	// DedupStatus 查重结论（pending|unique|duplicate|conflict）。
	DedupStatus string `json:"dedup_status" gorm:"type:varchar(16);not null;default:'pending'"`
	// DuplicateOf 查重命中的既有候选 ID（duplicate/conflict 时指向）。
	DuplicateOf string `json:"duplicate_of" gorm:"type:varchar(36);not null;default:''"`
	// Status 状态机：candidate → in_review → confirmed → published。
	Status string `json:"status" gorm:"type:varchar(16);not null;default:'candidate';index:idx_knowledge_candidates_tenant_status,priority:2"`
	// KnowledgeID 发布后生成的正式知识 ID（published 时回填）。
	KnowledgeID string `json:"knowledge_id" gorm:"type:varchar(36);not null;default:''"`
	// RequestedBy 提交人（创建者/连接器身份）。
	RequestedBy string `json:"requested_by" gorm:"type:varchar(36);not null"`
	// ReviewedBy 审核人（确认/驳回时回填）。
	ReviewedBy string `json:"reviewed_by" gorm:"type:varchar(36);not null;default:''"`
	// ReviewComment 审核意见（驳回原因等）。
	ReviewComment string    `json:"review_comment" gorm:"type:text;not null;default:''"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// TableName 固定 GORM 表名。
func (KnowledgeCandidate) TableName() string { return "knowledge_candidates" }

// CardStruct 解出候选卡片结构；坏 JSON/空卡按零值卡片处理（发布时仍可
// 用标题出一份最小 Markdown）。
func (c *KnowledgeCandidate) CardStruct() KnowledgeCandidateCard {
	var card KnowledgeCandidateCard
	if c != nil && len(c.Card) > 0 {
		_ = json.Unmarshal(c.Card, &card)
	}
	return card
}

// EvidenceList 解出结构化证据引用；坏 JSON 按空列表处理。
func (c *KnowledgeCandidate) EvidenceList() []KnowledgeCandidateEvidence {
	if c == nil || len(c.Evidence) == 0 {
		return nil
	}
	var evidence []KnowledgeCandidateEvidence
	if err := json.Unmarshal(c.Evidence, &evidence); err != nil {
		return nil
	}
	return evidence
}

// KnowledgeCandidateCreateInput 是 CreateCandidate 的入参。Card/Evidence 以
// 结构体传入，服务层统一 json.Marshal 落库；Confidence 空默认 AMBIGUOUS。
type KnowledgeCandidateCreateInput struct {
	SourceType KnowledgeCandidateSourceType
	SourceID   string
	Title      string
	Level      KnowledgeCandidateLevel
	Card       *KnowledgeCandidateCard
	Evidence   []KnowledgeCandidateEvidence
	Confidence KnowledgeCandidateConfidence
	// RequestedBy 提交人；空则取 ctx caller（服务层兜底）。
	RequestedBy string
}

// KnowledgeCandidateListFilter 是 List 的过滤参数。Status/Level 空不过滤；
// Limit/Offset 由仓储钳制。
type KnowledgeCandidateListFilter struct {
	Status string
	Level  string
	Limit  int
	Offset int
}

// KnowledgeCandidateMarkdown 把候选卡片拼成发布用的 Markdown 正文
// （§10.3 七段结构 → 固定小节；Measures 渲染为有序列表）。发布路径
// 与审计回放共用，保证"看到的卡片 = 发出的知识"。
func KnowledgeCandidateMarkdown(card KnowledgeCandidateCard) string {
	content := fmt.Sprintf("## 经验结论\n\n%s\n", card.What)
	if card.Scope != "" {
		content += fmt.Sprintf("\n## 适用范围\n\n%s\n", card.Scope)
	}
	if card.Phenomenon != "" {
		content += fmt.Sprintf("\n## 现象/触发条件\n\n%s\n", card.Phenomenon)
	}
	if card.CauseCandidate != "" {
		content += fmt.Sprintf("\n## 疑似原因\n\n%s\n", card.CauseCandidate)
	}
	if len(card.Measures) > 0 {
		content += "\n## 处置措施\n"
		for i, step := range card.Measures {
			content += fmt.Sprintf("\n%d. %s", i+1, step)
		}
		content += "\n"
	}
	if card.Validation != "" {
		content += fmt.Sprintf("\n## 验证情况\n\n%s\n", card.Validation)
	}
	if len(card.EvidenceLinks) > 0 {
		content += "\n## 证据链接\n"
		for _, link := range card.EvidenceLinks {
			content += fmt.Sprintf("\n- %s", link)
		}
		content += "\n"
	}
	return content
}
