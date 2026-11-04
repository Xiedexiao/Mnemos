package types

import (
	"fmt"
	"strings"
)

// ============================================================================
// Chat 生成侧降级的**可用性边界**（KnowAxis V3.0 §16.5）。
//
// Embedding 降级可以在一次调用内完成（算一个向量，失败就换模型重算）。
// Chat 不能：流式回答一旦开始向用户输出，就不能中途换模型——那会把两段
// 不同模型的文本拼成一条回答，读者看不出来，但质量与责任都断了。
//
// 因此 chat 的降级窗口只有**建立流之前**：
//  1. 模型加载失败（配置错误、凭据失效、provider 未注册）→ 可以换；
//  2. 建立流立即返回错误（provider 拒绝、模型下线）→ 可以换，因为用户
//     还没看到任何内容；
//  3. 流已经开始产出首个 token → **不可换**。此时只能报告失败或让调用方
//     决定是否重试，不能静默拼接。
//
// 这个边界必须由代码显式表达，否则实现者很容易在"流中途报错"处顺手加重试，
// 而那是错的。
// ============================================================================

// ChatFallbackPhase names where in the streaming lifecycle a failure occurred.
type ChatFallbackPhase string

const (
	// ChatFallbackBeforeStream is model resolution (nothing sent yet).
	ChatFallbackBeforeStream ChatFallbackPhase = "before_stream"
	// ChatFallbackStreamOpen is opening the stream (no content delivered yet).
	ChatFallbackStreamOpen ChatFallbackPhase = "stream_open"
	// ChatFallbackMidStream is after the first token reached the user.
	ChatFallbackMidStream ChatFallbackPhase = "mid_stream"
)

// Valid reports whether the phase is known.
func (p ChatFallbackPhase) Valid() bool {
	switch p {
	case ChatFallbackBeforeStream, ChatFallbackStreamOpen, ChatFallbackMidStream:
		return true
	default:
		return false
	}
}

// ChatFallbackDecision is whether the failure may be retried on another model.
type ChatFallbackDecision struct {
	// Allowed is true when the next candidate may be tried.
	Allowed bool
	// Reason explains a refusal.
	Reason string
}

// CanFallbackChat decides whether a failure at `phase` may move to the next
// model in the chain.
//
// 唯一的拒绝条件是"已经向用户输出了内容"：拼接两个模型的文本会让回答
// 在无人察觉的情况下变成混合体。其余两种情况都没有副作用，可以安全重试。
func CanFallbackChat(phase ChatFallbackPhase, deliveredContent bool) ChatFallbackDecision {
	if deliveredContent || phase == ChatFallbackMidStream {
		return ChatFallbackDecision{
			Reason: "the answer already reached the user: switching models now " +
				"would splice two models' text into one reply",
		}
	}
	switch phase {
	case ChatFallbackBeforeStream, ChatFallbackStreamOpen:
		return ChatFallbackDecision{Allowed: true}
	default:
		return ChatFallbackDecision{Reason: "unknown failure phase"}
	}
}

// ChatFallbackTrace records which model produced a streamed answer.
//
// 与 embedding 的 trace 分开：chat 的降级还多一个"在哪个阶段失败"的信息，
// 那是判断"该不该换"的唯一依据。
type ChatFallbackTrace struct {
	// RequestedModelID is the model the session asked for.
	RequestedModelID string `json:"requested_model_id"`
	// ServedBy is the model that produced the answer.
	ServedBy string `json:"served_by"`
	// Degraded is true when ServedBy differs from RequestedModelID.
	Degraded bool `json:"degraded"`
	// Attempts lists every model tried, with the phase it failed at.
	Attempts []ChatFallbackAttempt `json:"attempts"`
}

// ChatFallbackAttempt is one level's outcome.
type ChatFallbackAttempt struct {
	// ModelID tried at this level.
	ModelID string `json:"model_id"`
	// Level is the 0-based chain position.
	Level int `json:"level"`
	// Phase where it failed ("" when it succeeded).
	Phase ChatFallbackPhase `json:"phase,omitempty"`
	// Err is the failure reason ("" on success).
	Err string `json:"error,omitempty"`
}

// DescribeChatFallback renders one line for logs and the audit trail.
func DescribeChatFallback(trace ChatFallbackTrace) string {
	if !trace.Degraded {
		return "requested model served the answer"
	}
	if trace.ServedBy == "" {
		return fmt.Sprintf("no model could answer (%d tried)", len(trace.Attempts))
	}
	return fmt.Sprintf("degraded to %s after %d failed attempt(s)",
		trace.ServedBy, len(trace.Attempts)-1)
}

// ChatFallbackScene is the scene key for chat fallback chains.
const ChatFallbackScene = ModelUsageSceneChat

// NormalizeChatFallbackCandidates dedupes and drops the requested model's own
// duplicates while preserving order.
//
// 与 embedding 不同，这里**不设维度约束**：换一个 chat 模型回答同一个问题，
// 答案质量可能变化，但不会像向量空间错配那样产生"算得出来却无意义"的结果。
// 去重仍然必要：同一个模型试两次只是把延迟翻倍。
func NormalizeChatFallbackCandidates(requestedModelID string, candidates []string) []string {
	seen := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "" {
			continue
		}
		if _, duplicate := seen[trimmed]; duplicate {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	if len(out) == 0 && strings.TrimSpace(requestedModelID) != "" {
		return []string{strings.TrimSpace(requestedModelID)}
	}
	return out
}
