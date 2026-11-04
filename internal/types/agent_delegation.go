package types

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ============================================================================
// Agent / MCP 委派上下文（KnowAxis V3.0 §16.2 "受控委派"）。
//
// 平台支持把知识库能力通过 Agent 与 MCP 交给外部系统调用，但一次调用里
// 只有"哪个租户/哪个 key"，没有回答三个治理问题：
//   - **代表谁**（delegated user）：这次调用是代替哪位用户发起的？权限应当
//     收敛到该用户，而不是发到租户级；
//   - **为什么**（purpose）：调用方声明这次取数要做什么，事后复盘与合规
//     审查都靠它；
//   - **哪条链**（trace）：跨系统的一次业务动作由多跳组成，没有 trace 就
//     无法把它们串起来。
//
// 三个决定：
//  1. 委派是**收窄**而不是扩权：声明 delegated user 之后，权限按该用户判定；
//     声明的用户与调用者不是同一人时，只允许在**调用者已有权限**范围内收窄，
//     不可能借委派拿到更多。
//  2. purpose 必填且不能是占位词。一个恒为 "query" 的字段等于没有——它
//     必须能让复核者看出"这次取数服务于哪件事"。
//  3. trace 由调用方提供、服务端透传并写入审计；服务端不生成也不解析它的
//     内部结构，因为跨系统追踪的格式由调用方的链路系统定义。
// ============================================================================

const (
	// DelegationMaxPurposeRunes bounds the purpose statement.
	DelegationMaxPurposeRunes = 200
	// DelegationMaxTraceRunes bounds the trace id.
	DelegationMaxTraceRunes = 128
	// DelegationMinPurposeRunes is the shortest useful purpose.
	//
	// 1–2 个字符的表达（"查"、"问"）无法让复核者判断意图，与空值等价。
	DelegationMinPurposeRunes = 4
)

// placeholderPurposes are rejected: they carry no information.
var placeholderPurposes = []string{
	"query", "search", "test", "testing", "n/a", "na", "none", "todo", "xxx",
	"查询", "搜索", "测试", "无", "随便",
}

// AgentDelegation is one delegated call's governance context.
type AgentDelegation struct {
	// DelegatedUserID is the end user this call acts for. Empty means the
	// caller acts for themselves (the tenant-level agent identity).
	DelegatedUserID string `json:"delegated_user_id,omitempty"`
	// Purpose states what this call is for. Required.
	Purpose string `json:"purpose"`
	// TraceID links this call to the caller's own trace chain. Optional but
	// strongly recommended for multi-hop workflows.
	TraceID string `json:"trace_id,omitempty"`
	// Client names the calling system ("bi-portal", "mobile-app"). Optional;
	// it lands in the audit record so "who calls this tool" is answerable.
	Client string `json:"client,omitempty"`
	// ReceivedAt is when the server accepted the delegation.
	ReceivedAt time.Time `json:"received_at"`
}

// ValidateAgentDelegation checks the delegation shape.
//
// purpose 的校验刻意严格：它是这个结构里唯一说明"意图"的字段，允许空值或
// 占位词会让整套委派审计退化成"有人调用了这个工具"。
func ValidateAgentDelegation(delegation AgentDelegation) error {
	purpose := strings.TrimSpace(delegation.Purpose)
	runes := []rune(purpose)
	if len(runes) < DelegationMinPurposeRunes {
		return fmt.Errorf("purpose must state what the call is for (at least %d characters)",
			DelegationMinPurposeRunes)
	}
	if len(runes) > DelegationMaxPurposeRunes {
		return fmt.Errorf("purpose must be at most %d characters", DelegationMaxPurposeRunes)
	}
	lowered := strings.ToLower(purpose)
	for _, placeholder := range placeholderPurposes {
		if lowered == placeholder {
			return fmt.Errorf("purpose %q states nothing; describe the task", purpose)
		}
	}
	if trace := strings.TrimSpace(delegation.TraceID); len([]rune(trace)) > DelegationMaxTraceRunes {
		return fmt.Errorf("trace_id must be at most %d characters", DelegationMaxTraceRunes)
	}
	return nil
}

// Normalize trims the free-form fields.
func (d *AgentDelegation) Normalize() {
	d.DelegatedUserID = strings.TrimSpace(d.DelegatedUserID)
	d.Purpose = strings.TrimSpace(d.Purpose)
	d.TraceID = strings.TrimSpace(d.TraceID)
	d.Client = strings.TrimSpace(d.Client)
}

// delegationContextKey carries the delegation on the request context.
type delegationContextKey struct{}

// WithAgentDelegation attaches a delegation to ctx.
func WithAgentDelegation(ctx context.Context, delegation AgentDelegation) context.Context {
	return context.WithValue(ctx, delegationContextKey{}, delegation)
}

// AgentDelegationFromContext reads the delegation, if any.
func AgentDelegationFromContext(ctx context.Context) (AgentDelegation, bool) {
	delegation, ok := ctx.Value(delegationContextKey{}).(AgentDelegation)
	return delegation, ok && delegation.Purpose != ""
}

// DelegationAuditDetails renders the delegation for one audit record.
//
// 统一在这里生成，避免每个调用点各写一份键名——审计日志靠字段名做检索，
// 键名漂移会让"按 trace 查一次业务动作"失效。
func DelegationAuditDetails(delegation AgentDelegation) map[string]interface{} {
	if strings.TrimSpace(delegation.Purpose) == "" {
		return nil
	}
	details := map[string]interface{}{"purpose": delegation.Purpose}
	if delegation.DelegatedUserID != "" {
		details["delegated_user_id"] = delegation.DelegatedUserID
	}
	if delegation.TraceID != "" {
		details["trace_id"] = delegation.TraceID
	}
	if delegation.Client != "" {
		details["client"] = delegation.Client
	}
	return details
}

// EffectiveSubjectUser returns the user whose permissions apply to the call.
//
// 没有声明委派用户时就是调用者本人。声明了则收敛到该用户——但**不扩大**：
// 调用方仍然必须持有租户级的能力授权（API key capability），委派只决定
// "看得到哪些内容"，不决定"能不能用这个工具"。
func EffectiveSubjectUser(callerUserID string, delegation AgentDelegation) string {
	if delegation.DelegatedUserID != "" {
		return delegation.DelegatedUserID
	}
	return callerUserID
}
