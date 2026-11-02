package types

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// ============================================================================
// Evidence Pack 持久化（KnowAxis V3.0 §13.4 的 v1 子集）。
//
// evidence_packs 按 request_id 唯一，冻结一次问答的整条链路：检索候选
// 快照、上下文编译摘要、模型/提示词版本、答案与引用、当时权限基线。
// 事后可按 request_id 回放"当时检索到什么、以什么权限、答了什么"。
//
// 行与行之间以 hash_chain_prev → hash_chain_row 首尾相接成租户内单链
// （链首 GENESIS，与审批回执链同一套锚定方式）。链只保证"没有一行被
// 静默篡改"：Save 的重复投递（同一载荷）幂等跳过；同一 request_id 带
// 不同载荷重存会重算行哈希，若该行已不是链尾，后续链校验会显式断链 ——
// 这是有意的：一条已定稿的问答链路不该被改写。
// ============================================================================

// EvidencePackRefusal 拒绝路径标记。空串 = 正常回答；其余值表示本次回答
// 命中了拒绝路径（answer 可为空，candidates/citations 仍保留现场）。
type EvidencePackRefusal string

const (
	// EvidenceRefusalNone 正常回答（未命中拒绝路径）。
	EvidenceRefusalNone EvidencePackRefusal = ""
	// EvidenceRefusalRefused 命中拒答策略（如密级/越权边界）。
	EvidenceRefusalRefused EvidencePackRefusal = "refused"
	// EvidenceRefusalConflict 与权威知识冲突（回答被冲突门禁拦下）。
	EvidenceRefusalConflict EvidencePackRefusal = "conflict"
	// EvidenceRefusalNoEvidence 检索无依据（无候选或全部被阈值拒绝）。
	EvidenceRefusalNoEvidence EvidencePackRefusal = "no_evidence"
	// EvidenceRefusalOutOfScope 问题要求越权信息：拒答且不得透露存在性
	// （§13.5 "问题要求越权信息"）。与 refused 分开是因为两者的对外话术
	// 不同——越权必须连"有没有这条内容"都不能暗示。
	EvidenceRefusalOutOfScope EvidencePackRefusal = "out_of_scope"
	// EvidenceRefusalExpiredOnly 只有过期证据：提示过期，不给当前结论
	// （§13.5 "只有过期证据"）。
	EvidenceRefusalExpiredOnly EvidencePackRefusal = "expired_only"
	// EvidenceRefusalStaleOnly 只有待复审/疑似过期证据：可以回答但必须带上
	// 时效提示，且不作为权威依据（§7.1 状态行为表）。
	EvidenceRefusalStaleOnly EvidencePackRefusal = "stale_only"
	// EvidenceRefusalSpeculative 问题要求无依据推测：拒答并给出"需要哪些
	// 信息"的追问（§13.5 "要求无依据推测"）。
	EvidenceRefusalSpeculative EvidencePackRefusal = "speculative"
	// EvidenceRefusalNoOfficialSource 高风险决策缺少官方来源：只给参考证据，
	// 不给最终建议（§13.5 最后一行）。
	EvidenceRefusalNoOfficialSource EvidencePackRefusal = "no_official_source"
)

// RefusalScenario describes one §13.5 refusal case: the marker stored on the
// pack, the user-facing explanation, and whether a caveat still allows an
// answer. The caveat cases are deliberately separate from the hard refusals so
// a caller cannot turn "answering with a warning" into "answering normally".
type RefusalScenario struct {
	// Marker is the refusal value written to the Evidence Pack.
	Marker EvidencePackRefusal `json:"marker"`
	// Message is what the user sees.
	Message string `json:"message"`
	// AllowsAnswer is true when evidence still backs a useful answer, subject
	// to the caveat in Message.
	AllowsAnswer bool `json:"allows_answer"`
	// HidesExistence is true when the reply must not reveal that matching
	// content exists at all (§2.10 存在性不泄露).
	HidesExistence bool `json:"hides_existence"`
}

// RefusalScenarios is the §13.5 matrix, keyed by marker. It is the single
// source of truth for refusal wording so the answer path, the IM bot and the
// Evidence Pack all describe the same situation the same way.
var RefusalScenarios = map[EvidencePackRefusal]RefusalScenario{
	EvidenceRefusalNoEvidence: {
		Marker:  EvidenceRefusalNoEvidence,
		Message: "知识库中没有找到可以支撑这个问题的依据。建议补充相关知识或咨询领域专家。",
	},
	EvidenceRefusalOutOfScope: {
		Marker:         EvidenceRefusalOutOfScope,
		Message:        "这个问题超出了你当前的访问范围。",
		HidesExistence: true,
	},
	EvidenceRefusalRefused: {
		Marker:  EvidenceRefusalRefused,
		Message: "该问题涉及受限内容，无法回答。",
	},
	EvidenceRefusalConflict: {
		Marker:  EvidenceRefusalConflict,
		Message: "现有权威资料之间存在冲突，暂不给出唯一结论，请先确认适用版本。",
	},
	EvidenceRefusalExpiredOnly: {
		Marker:  EvidenceRefusalExpiredOnly,
		Message: "只找到已过期的依据，不作为当前结论。请确认最新版本后再判断。",
	},
	EvidenceRefusalStaleOnly: {
		Marker:       EvidenceRefusalStaleOnly,
		Message:      "以下依据已到复审期或疑似过期，仅供参考，请以最新发布版本为准。",
		AllowsAnswer: true,
	},
	EvidenceRefusalSpeculative: {
		Marker:  EvidenceRefusalSpeculative,
		Message: "知识库中没有依据支持该推测。要回答这个问题，还需要：具体设备/型号、版本号与发生时间。",
	},
	EvidenceRefusalNoOfficialSource: {
		Marker:       EvidenceRefusalNoOfficialSource,
		Message:      "这是高风险决策事项，但缺少官方来源。以下仅为参考资料，不构成最终建议。",
		AllowsAnswer: true,
	},
}

// Valid 报告该拒绝标记是否在白名单内（请求体/管道写入的入参校验）。
func (r EvidencePackRefusal) Valid() bool {
	switch r {
	case EvidenceRefusalNone, EvidenceRefusalRefused, EvidenceRefusalConflict, EvidenceRefusalNoEvidence,
		EvidenceRefusalOutOfScope, EvidenceRefusalExpiredOnly, EvidenceRefusalStaleOnly,
		EvidenceRefusalSpeculative, EvidenceRefusalNoOfficialSource:
		return true
	default:
		return false
	}
}

// EvidencePackRetentionDays 是 v1 的保留期常量：retention_until =
// created_at + 90 天。租户级差异化保留配置接入后，这里改为读租户配置，
// 清理任务按 audit_logs 的 retention sweep 模式扫 retention_until。
const EvidencePackRetentionDays = 90

// EvidencePackGenesisHash 链首占位：租户第一行 evidence pack 的
// hash_chain_prev。与 ApprovalReceiptGenesisHash 语义一致但独立命名空间
// （两条链互不引用）。
const EvidencePackGenesisHash = "GENESIS"

// EvidenceCandidate 是检索候选快照里的一行：进入过本轮候选池的一个 chunk。
// 脱敏约定：Snippet 只保留片段摘要（截断后的 passage 前缀），不落全量
// chunk 正文；正文本身已在 messages/knowledge 侧，按各自的生命周期管理。
type EvidenceCandidate struct {
	KnowledgeID string  `json:"knowledge_id"`
	ChunkID     string  `json:"chunk_id"`
	Score       float64 `json:"score"`
	// Snippet 候选片段摘要（建议调用方截断到几百字符）。
	Snippet string `json:"snippet"`
}

// EvidenceContextSnapshot 是上下文编译结果摘要：版本 / 冲突标记 / 预算
// 与候选裁剪计数。只存摘要数字，不存编译后的正文（正文随消息的
// rendered_content 持久化，二者配合即可完整回放）。
type EvidenceContextSnapshot struct {
	// ContextVersion 上下文编译器版本标识（编译器演进时更换，回放时区分口径）。
	ContextVersion string `json:"context_version,omitempty"`
	// HasConflict 报告本次上下文是否被标记了知识冲突。
	HasConflict bool `json:"has_conflict"`
	// BudgetTokens 上下文预算上限（token），0 表示本轮未启用预算。
	BudgetTokens int `json:"budget_tokens,omitempty"`
	// RenderedChars 编译后上下文的字符数。
	RenderedChars int `json:"rendered_chars,omitempty"`
	// CandidatesShown 实际进入上下文的候选数。
	CandidatesShown int `json:"candidates_shown,omitempty"`
	// CandidatesTotal 截断前的检索候选总数。
	CandidatesTotal int `json:"candidates_total,omitempty"`
	// TruncationStage 首个截断阶段（rerank|merge|filter_top_k），空 = 未截断。
	TruncationStage string `json:"truncation_stage,omitempty"`
}

// EvidenceCitation 是引用清单里的一行：答案实际引用到的知识出处（与
// assistant message 的 knowledge_references 对齐的稳定子集）。
type EvidenceCitation struct {
	// Index 引用序号（答案内 [n] 标号），从 1 开始；未知时为 0。
	Index int `json:"index"`
	// KnowledgeID 被引用的知识 ID；web 引用为空。
	KnowledgeID string `json:"knowledge_id"`
	// ChunkID 被引用的 chunk ID；web 引用存 URL。
	ChunkID string `json:"chunk_id"`
	// Title 引用标题快照（知识标题 / 网页标题）。
	Title string `json:"title"`
}

// EvidencePermissionSnapshot 是回答时刻的权限基线摘要：按知识记录当时
// lifecycle 与 ACL 版本，回放时可对照"这条知识当时是什么状态"。
type EvidencePermissionSnapshot struct {
	// HighestClassification 本轮候选涉及的最高密级（public|internal|
	// confidential|secret），空表示无知识候选。
	HighestClassification string `json:"highest_classification,omitempty"`
	// LifecycleStatuses 知识 ID → 回答时刻 lifecycle 状态。
	LifecycleStatuses map[string]string `json:"lifecycle_statuses,omitempty"`
	// ACLVersions 知识库 ID → 回答时刻 ACL 生效版本。
	ACLVersions map[string]string `json:"acl_versions,omitempty"`
}

// EvidencePack 是一次问答的证据包行。request_id 全局唯一（一次问答 =
// 一行）；session_id/message_id/user_id 指向这次问答在会话里的落点。
type EvidencePack struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null;index:idx_evidence_packs_tenant_time,priority:1"`
	// RequestID 请求追踪 ID（messages.request_id 同源），全表唯一。
	RequestID string `json:"request_id" gorm:"type:varchar(64);not null;uniqueIndex:uq_evidence_packs_request_id"`
	SessionID string `json:"session_id" gorm:"type:varchar(36);not null;default:'';index:idx_evidence_packs_session"`
	MessageID string `json:"message_id" gorm:"type:varchar(36);not null;default:''"`
	UserID    string `json:"user_id" gorm:"type:varchar(36);not null;default:''"`
	Question  string `json:"question" gorm:"type:text;not null;default:''"`
	// Candidates 检索候选快照（EvidenceCandidate 数组，脱敏后）。
	Candidates JSON `json:"candidates" gorm:"type:jsonb;not null;default:'[]'"`
	// ContextSnapshot 上下文编译结果摘要（EvidenceContextSnapshot）。
	ContextSnapshot JSON `json:"context_snapshot" gorm:"type:jsonb;not null;default:'{}'"`
	// ModelVersion 回答所用模型标识（model id / 版本串）。
	ModelVersion string `json:"model_version" gorm:"type:varchar(128);not null;default:''"`
	// PromptVersion 系统提示词模板版本标识。
	PromptVersion string `json:"prompt_version" gorm:"type:varchar(64);not null;default:''"`
	// Answer 答案全文。拒绝路径下可为空。
	Answer string `json:"answer" gorm:"type:text;not null;default:''"`
	// Citations 引用清单（EvidenceCitation 数组）。
	Citations JSON `json:"citations" gorm:"type:jsonb;not null;default:'[]'"`
	// PermissionSnapshot 当时权限基线摘要（EvidencePermissionSnapshot）。
	PermissionSnapshot JSON `json:"permission_snapshot" gorm:"type:jsonb;not null;default:'{}'"`
	// Refusal 拒绝路径标记：'' | refused | conflict | no_evidence。
	Refusal string `json:"refusal" gorm:"type:varchar(16);not null;default:''"`
	// HashChainPrev 链上前一行的 hash_chain_row（链首为 GENESIS）。
	HashChainPrev string `json:"hash_chain_prev" gorm:"type:varchar(64);not null;default:''"`
	// HashChainRow 本行哈希 = ComputeEvidenceRowHash(prev, pack)。
	HashChainRow   string     `json:"hash_chain_row" gorm:"type:varchar(64);not null"`
	RetentionUntil *time.Time `json:"retention_until"`
	CreatedAt      time.Time  `json:"created_at" gorm:"index:idx_evidence_packs_tenant_time,priority:2"`
}

// TableName 固定 GORM 表名。
func (EvidencePack) TableName() string { return "evidence_packs" }

// SetCandidates 序列化检索候选快照进 JSONB 列。纯数据结构体序列化不会
// 失败；出错时兜底空数组，保证列内容始终是合法 JSON。
func (p *EvidencePack) SetCandidates(candidates []EvidenceCandidate) {
	if p == nil {
		return
	}
	if candidates == nil {
		candidates = []EvidenceCandidate{}
	}
	raw, err := json.Marshal(candidates)
	if err != nil {
		raw = []byte("[]")
	}
	p.Candidates = JSON(raw)
}

// CandidatesList 解码候选快照；坏 JSON/空值按空数组处理（不阻塞回放）。
func (p *EvidencePack) CandidatesList() []EvidenceCandidate {
	if p == nil {
		return []EvidenceCandidate{}
	}
	out := []EvidenceCandidate{}
	if len(p.Candidates) == 0 {
		return out
	}
	var list []EvidenceCandidate
	if err := json.Unmarshal(p.Candidates, &list); err != nil {
		return out
	}
	if list != nil {
		out = list
	}
	return out
}

// SetCitations 序列化引用清单进 JSONB 列。
func (p *EvidencePack) SetCitations(citations []EvidenceCitation) {
	if p == nil {
		return
	}
	if citations == nil {
		citations = []EvidenceCitation{}
	}
	raw, err := json.Marshal(citations)
	if err != nil {
		raw = []byte("[]")
	}
	p.Citations = JSON(raw)
}

// CitationsList 解码引用清单；坏 JSON/空值按空数组处理。
func (p *EvidencePack) CitationsList() []EvidenceCitation {
	if p == nil {
		return []EvidenceCitation{}
	}
	out := []EvidenceCitation{}
	if len(p.Citations) == 0 {
		return out
	}
	var list []EvidenceCitation
	if err := json.Unmarshal(p.Citations, &list); err != nil {
		return out
	}
	if list != nil {
		out = list
	}
	return out
}

// SetContextSnapshot 序列化上下文编译摘要进 JSONB 列。
func (p *EvidencePack) SetContextSnapshot(snapshot EvidenceContextSnapshot) {
	if p == nil {
		return
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		raw = []byte("{}")
	}
	p.ContextSnapshot = JSON(raw)
}

// ContextSnapshotValue 解码上下文编译摘要；坏 JSON 按零值处理。
func (p *EvidencePack) ContextSnapshotValue() EvidenceContextSnapshot {
	if p == nil || len(p.ContextSnapshot) == 0 {
		return EvidenceContextSnapshot{}
	}
	var snapshot EvidenceContextSnapshot
	_ = json.Unmarshal(p.ContextSnapshot, &snapshot)
	return snapshot
}

// SetPermissionSnapshot 序列化权限基线摘要进 JSONB 列。
func (p *EvidencePack) SetPermissionSnapshot(snapshot EvidencePermissionSnapshot) {
	if p == nil {
		return
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		raw = []byte("{}")
	}
	p.PermissionSnapshot = JSON(raw)
}

// PermissionSnapshotValue 解码权限基线摘要；坏 JSON 按零值处理。
func (p *EvidencePack) PermissionSnapshotValue() EvidencePermissionSnapshot {
	if p == nil || len(p.PermissionSnapshot) == 0 {
		return EvidencePermissionSnapshot{}
	}
	var snapshot EvidencePermissionSnapshot
	_ = json.Unmarshal(p.PermissionSnapshot, &snapshot)
	return snapshot
}

// evidencePackHashPayload 是哈希链的规范化载荷。字段顺序即哈希输入顺序
// —— 修改字段、顺序或序列化格式都会让既有链无法复算，属于破坏性变更；
// 新增字段只能追加到末尾。四个 JSONB 列通过各自的类型化结构体重序列化，
// 规避 Postgres jsonb 存储不保留键序/空白导致的复算漂移（与回执链的
// BaselineSnapshot 处理同一手法）。hash_chain_prev/row 与 retention_until
// 不进载荷：prev 是哈希输入、row 是输出，retention 是策略不是证据。
type evidencePackHashPayload struct {
	ID                 string                     `json:"id"`
	TenantID           uint64                     `json:"tenant_id"`
	RequestID          string                     `json:"request_id"`
	SessionID          string                     `json:"session_id"`
	MessageID          string                     `json:"message_id"`
	UserID             string                     `json:"user_id"`
	Question           string                     `json:"question"`
	Candidates         []EvidenceCandidate        `json:"candidates"`
	ContextSnapshot    EvidenceContextSnapshot    `json:"context_snapshot"`
	ModelVersion       string                     `json:"model_version"`
	PromptVersion      string                     `json:"prompt_version"`
	Answer             string                     `json:"answer"`
	Citations          []EvidenceCitation         `json:"citations"`
	PermissionSnapshot EvidencePermissionSnapshot `json:"permission_snapshot"`
	Refusal            string                     `json:"refusal"`
	CreatedAt          string                     `json:"created_at"`
}

// HashPayload 返回该证据包的规范化哈希载荷（供 Save 入链与链校验共用）。
// CreatedAt 统一 RFC3339Nano UTC，跨时区/精度漂移不影响复算。
func (p *EvidencePack) HashPayload() evidencePackHashPayload {
	payload := evidencePackHashPayload{
		Candidates: []EvidenceCandidate{},
		Citations:  []EvidenceCitation{},
		CreatedAt:  time.RFC3339Nano,
	}
	if p == nil {
		return payload
	}
	payload.ID = p.ID
	payload.TenantID = p.TenantID
	payload.RequestID = p.RequestID
	payload.SessionID = p.SessionID
	payload.MessageID = p.MessageID
	payload.UserID = p.UserID
	payload.Question = p.Question
	payload.Candidates = p.CandidatesList()
	payload.ContextSnapshot = p.ContextSnapshotValue()
	payload.ModelVersion = p.ModelVersion
	payload.PromptVersion = p.PromptVersion
	payload.Answer = p.Answer
	payload.Citations = p.CitationsList()
	payload.PermissionSnapshot = p.PermissionSnapshotValue()
	payload.Refusal = p.Refusal
	payload.CreatedAt = p.CreatedAt.UTC().Format(time.RFC3339Nano)
	return payload
}

// ComputeEvidenceRowHash 计算 hash_chain_row = sha256_hex(prevHash + "\n" +
// 规范化JSON字段串)。仓储 Save 在链尾读取事务内调用它。与审批回执的
// ComputeApprovalReceiptRowHash 同风格但独立成链，命名空间互不引用。
func ComputeEvidenceRowHash(prevHash string, pack *EvidencePack) string {
	payload, err := json.Marshal(pack.HashPayload())
	if err != nil {
		// 结构体序列化不会失败；兜底保证哈希总可计算。
		payload = []byte("{}")
	}
	sum := sha256.Sum256([]byte(prevHash + "\n" + string(payload)))
	return hex.EncodeToString(sum[:])
}
