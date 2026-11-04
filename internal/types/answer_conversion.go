package types

import (
	"fmt"
	"strings"
)

// ============================================================================
// 答案转知识（KnowAxis V3.0 §13 "回答→知识沉淀" / §10.2 经验沉淀入口）。
//
// 平台此前只有"社区问题转知识"，而且那条路径只登记关联（`MarkConverted`
// 记下 knowledge_id），并不真的创建内容——问答答案本身没有任何沉淀通道。
// 一次被反复追问、被采纳、被收藏的回答，本质上就是一条值得沉淀的经验，
// 而它此前只能靠人手动复制粘贴。
//
// 四个决定：
//  1. 走**候选**路径而不是直接建知识：答案由模型生成，可能有幻觉。候选是
//     既有的、带查重与审核门禁的沉淀入口（§10.4 L1→L2→L3），答案转知识
//     必须经过同一道门，不能因为"它是我们自己的回答"就跳过审核。
//  2. 只从**有依据的回答**转：refusal 非空（拒答/冲突/无依据）的答案没有
//     内容可沉淀，转成候选只会制造一条空经验。
//  3. 证据（citations）原样带进候选：候选发布时要用它做派生权限
//     （§2.4 派生知识不得比源证据更可见），凭空生成的知识没有这条约束。
//  4. 幂等：同一 (answer, 目标空间) 重复转换返回已有候选，不重复建行。
//     一键转换的按钮很容易被连点两次。
// ============================================================================

const (
	// AnswerConversionMaxTitleRunes bounds the generated candidate title.
	AnswerConversionMaxTitleRunes = 80
	// AnswerConversionMaxAnswerRunes bounds the answer body copied in.
	//
	// 超长回答会被截断并标注，而不是拒绝：一条 20k 字的长答案仍然值得沉淀，
	// 但候选卡片不该是一整篇文档。
	AnswerConversionMaxAnswerRunes = 8000
)

// AnswerConversionInput is the request to turn one answered turn into a candidate.
type AnswerConversionInput struct {
	// RequestID identifies the Evidence Pack (one answered turn).
	RequestID string `json:"request_id"`
	// Title overrides the generated title (empty = derive from the question).
	Title string `json:"title"`
	// Level is the §10.4 level for the candidate (empty = L1, the default).
	//
	// 刻意不默认 L2：答案还没有经过任何人的确认，L2 是"审核通过"的层级
	// （§10.4），由转换这个动作直接给出 L2 等于自己给自己盖章。
	Level string `json:"level"`
}

// AnswerConversionResult reports what the conversion did.
type AnswerConversionResult struct {
	// CandidateID is the created (or already existing) candidate.
	CandidateID string `json:"candidate_id"`
	// Created is false when an existing candidate was reused (idempotent retry).
	Created bool `json:"created"`
	// Title is the candidate title that was used.
	Title string `json:"title"`
	// SourceRequestID ties the candidate back to the answered turn.
	SourceRequestID string `json:"source_request_id"`
	// EvidenceCount is how many citations were carried into the candidate.
	EvidenceCount int `json:"evidence_count"`
	// TruncatedAnswer reports that the answer exceeded the copy limit.
	TruncatedAnswer bool `json:"truncated_answer,omitempty"`
}

// ValidateAnswerConversionInput checks the request shape.
func ValidateAnswerConversionInput(input AnswerConversionInput) (string, error) {
	requestID := strings.TrimSpace(input.RequestID)
	if requestID == "" {
		return "", fmt.Errorf("request_id is required")
	}
	if level := strings.TrimSpace(input.Level); level != "" {
		if !KnowledgeCandidateLevel(level).Valid() {
			return "", fmt.Errorf("unknown level %q", level)
		}
	}
	if title := strings.TrimSpace(input.Title); title != "" {
		if len([]rune(title)) > AnswerConversionMaxTitleRunes {
			return "", fmt.Errorf("title must be at most %d characters",
				AnswerConversionMaxTitleRunes)
		}
	}
	return requestID, nil
}

// AnswerConversionEligible reports whether a pack can be converted.
//
// 拒答的答案没有内容可沉淀；空答案同理。这两个判断放在类型层，是因为它们
// 定义的是"什么样的回答算一条经验"，而不是某条链路的技术细节。
func AnswerConversionEligible(pack *EvidencePack) error {
	if pack == nil {
		return fmt.Errorf("answered turn not found")
	}
	if strings.TrimSpace(pack.Refusal) != "" {
		return fmt.Errorf("a refusal carries no answer to convert")
	}
	if strings.TrimSpace(pack.Answer) == "" {
		return fmt.Errorf("the answer is empty")
	}
	return nil
}

// AnswerConversionTitle derives the candidate title from the question.
//
// 用问题而不是答案开头当作标题：候选列表里读者是按"这解决什么问题"来找的，
// 而答案的第一句通常是背景铺垫。去掉问号并截断，标题读起来是一个陈述。
func AnswerConversionTitle(question, override string) string {
	if title := strings.TrimSpace(override); title != "" {
		runes := []rune(title)
		if len(runes) > AnswerConversionMaxTitleRunes {
			runes = runes[:AnswerConversionMaxTitleRunes]
		}
		return string(runes)
	}
	base := strings.TrimSpace(question)
	base = strings.Trim(base, "？?。.！! \t\n")
	if base == "" {
		base = "未命名问答经验"
	}
	runes := []rune(base)
	if len(runes) > AnswerConversionMaxTitleRunes {
		runes = runes[:AnswerConversionMaxTitleRunes]
	}
	return string(runes)
}

// BuildAnswerConversionCard assembles the §10.3 candidate card from a pack.
//
// 七段结构里只有"结论"与"适用范围"能由一次问答确定：
//   - What：答案正文（截断到上限）；
//   - Scope：检索命中的知识库/文档范围，作为这条经验的适用面；
//   - Phenomenon/Cause/Measures/Validation 留空——它们需要人补充，模型
//     从答案里"推断"出来只会在候选卡片里塞进更多未经确认的内容。
//
// 空字段在卡片里是诚实的"待补充"，而不是编造。
func BuildAnswerConversionCard(pack *EvidencePack, truncated bool) KnowledgeCandidateCard {
	answer := strings.TrimSpace(pack.Answer)
	if truncated {
		answer += "\n\n（原回答较长，已截断；完整内容见证据包 " + pack.RequestID + "）"
	}
	scope := answerConversionScope(pack)
	return KnowledgeCandidateCard{
		What:  answer,
		Scope: scope,
	}
}

// answerConversionScope describes where the answer's evidence came from.
//
// 适用范围取引用的知识库口径而不是逐篇列标题：候选卡片是给人看的，
// 一长串文档标题会把它变成检索结果列表。具体引用由 Evidence 承载。
func answerConversionScope(pack *EvidencePack) string {
	citations := pack.CitationsList()
	if len(citations) == 0 {
		return "来自一次问答（无引用证据）"
	}
	seen := make(map[string]struct{}, len(citations))
	titles := make([]string, 0, len(citations))
	for _, citation := range citations {
		title := strings.TrimSpace(citation.Title)
		if title == "" {
			continue
		}
		if _, duplicate := seen[title]; duplicate {
			continue
		}
		seen[title] = struct{}{}
		titles = append(titles, title)
	}
	if len(titles) == 0 {
		return "来自一次问答（引用未标注标题）"
	}
	return "依据 " + strings.Join(titles, "、")
}

// BuildAnswerConversionEvidence carries the citations into candidate evidence.
//
// §2.4 派生权限：候选发布时会继承源证据最严密的密级，因此这里必须保住
// 每一条引用的知识 ID。web 引用（KnowledgeID 为空）只保留标题，不参与
// 权限推导——它没有可继承的源文档。
func BuildAnswerConversionEvidence(pack *EvidencePack) []KnowledgeCandidateEvidence {
	citations := pack.CitationsList()
	evidence := make([]KnowledgeCandidateEvidence, 0, len(citations))
	for _, citation := range citations {
		title := strings.TrimSpace(citation.Title)
		knowledgeID := strings.TrimSpace(citation.KnowledgeID)
		if title == "" && knowledgeID == "" {
			continue
		}
		evidence = append(evidence, KnowledgeCandidateEvidence{
			SourceType: string(KnowledgeCandidateSourceQAAnswer),
			SourceID:   knowledgeID,
			Locator:    strings.TrimSpace(citation.ChunkID),
			Quote:      title,
		})
	}
	return evidence
}

// TruncateAnswerForConversion cuts an answer to the copy limit.
func TruncateAnswerForConversion(answer string) (string, bool) {
	runes := []rune(answer)
	if len(runes) <= AnswerConversionMaxAnswerRunes {
		return answer, false
	}
	return string(runes[:AnswerConversionMaxAnswerRunes]), true
}
