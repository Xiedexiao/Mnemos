package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ============================================================================
// MCP 调用的审计与委派（KnowAxis V3.0 §16.2 受控委派 / §16.5 外部调用留痕）。
//
// 此前 MCP 工具调用**没有任何审计**：可以回答"某个 endpoint 调过几次"
// （last_used_at），但回答不了"谁通过它看了什么、为什么、属于哪条业务链路"。
// 一旦知识通过 MCP 交给外部系统，这三件事就是唯一的追责依据。
//
// 两个决定：
//  1. 审计包在**工具注册层**（decorator），不散在每个 handler 里。16 个工具
//     各写一遍必然会有几个漏掉，而"哪几个漏了"没人能发现。
//  2. 委派从**工具参数**读取，不从 HTTP header 读。MCP 的 header 由客户端
//     库接管，且同一条连接上可能连续发起代表不同用户的调用；参数才是逐次
//     调用的事实来源。
// ============================================================================

// delegationArgument is the argument name carrying the §16.2 delegation block.
//
// 用嵌套对象而不是三个平铺字段：委派是一个整体概念，平铺之后 reading
// 调用方很容易只传 purpose 忘了 delegated_user_id，而两者必须一起被审视。
const delegationArgument = "_delegation"

// auditToolCall wraps one tool handler with delegation extraction and auditing.
//
// 审计写入是**尽力而为**的：审计服务不可用时调用仍然成功（记日志）。
// 反过来（审计失败就让外部系统拿不到知识）会把一个可观测性问题放大成
// 可用性问题；而"审计必须成功"这条要求应当由审计服务自身的持久化保证，
// 不是靠在这里阻塞业务。
func (s *Server) auditToolCall(
	toolName string, next server.ToolHandlerFunc,
) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		delegation := delegationFromArguments(request.GetArguments())
		if delegation.Purpose != "" {
			// 只有在声明了委派时才放进 context：没有声明时下游看到的
			// 就是"没有委派"，与既有行为一致。
			ctx = types.WithAgentDelegation(ctx, delegation)
		}
		startedAt := time.Now()
		// 委派必须在**工具执行之前**换成真实主体：否则工具已经按 endpoint
		// 的合成身份取完了数，事后再记一条"代表某人"的审计只是装饰。
		delegatedCtx, delegationErr := s.applyDelegation(ctx, toolName, delegation)
		if delegationErr != nil {
			// 无法验证的委派直接拒绝：静默退化成合成身份会让调用方以为
			// 自己代表某个用户，实际却是另一个身份在取数。
			result := mcp.NewToolResultError(delegationErr.Error())
			s.recordToolAudit(ctx, toolName, delegation, result, delegationErr,
				time.Since(startedAt))
			return result, nil
		}
		ctx = delegatedCtx
		result, err := next(ctx, request)
		s.recordToolAudit(ctx, toolName, delegation, result, err, time.Since(startedAt))
		return result, err
	}
}

// delegationFromArguments reads the §16.2 delegation block out of the tool
// arguments.
//
// 解析失败按"没有委派"处理而不是拒绝调用：委派是治理增强项，格式错误
// 不该让一次合法的检索失败。但缺失会被审计记录（delegation_present=false），
// 因此"没声明"是可观测的，不会悄悄消失。
func delegationFromArguments(args map[string]interface{}) types.AgentDelegation {
	if len(args) == 0 {
		return types.AgentDelegation{}
	}
	raw, ok := args[delegationArgument]
	if !ok {
		return types.AgentDelegation{}
	}
	var delegation types.AgentDelegation
	encoded, err := json.Marshal(raw)
	if err != nil {
		return types.AgentDelegation{}
	}
	if err := json.Unmarshal(encoded, &delegation); err != nil {
		return types.AgentDelegation{}
	}
	delegation.Normalize()
	if err := types.ValidateAgentDelegation(delegation); err != nil {
		// 无效的委派按未声明处理：审计里会把它记成"没有有效委派"，而不是
		// 记一条内容错误的委派记录。
		return types.AgentDelegation{}
	}
	delegation.ReceivedAt = time.Now()
	return delegation
}

// recordToolAudit writes one audit row per MCP tool call.
func (s *Server) recordToolAudit(
	ctx context.Context, toolName string, delegation types.AgentDelegation,
	result *mcp.CallToolResult, callErr error, elapsed time.Duration,
) {
	if s.audit == nil {
		return
	}
	endpoint, _ := endpointFromContext(ctx)
	tenantID := uint64(0)
	endpointID := ""
	if endpoint != nil {
		tenantID = endpoint.TenantID
		endpointID = endpoint.ID
	}
	if tenantID == 0 {
		// 没有端点上下文说明这次调用没通过 /mcp/:endpoint_id 的认证；
		// 那样的请求根本到不了工具层，这里只是防御。
		return
	}

	outcome := types.AuditOutcomeSuccess
	reason := ""
	switch {
	case callErr != nil:
		outcome = types.AuditOutcomeFailed
		reason = callErr.Error()
	case result != nil && result.IsError:
		outcome = types.AuditOutcomeFailed
		reason = "tool reported an error"
	}

	details := map[string]interface{}{
		"tool":        toolName,
		"endpoint_id": endpointID,
		"duration_ms": elapsed.Milliseconds(),
		// 委派缺失本身是重要信号：受控委派要求外部调用声明"代表谁、做什么"。
		"delegation_present": delegation.Purpose != "",
	}
	for key, value := range types.DelegationAuditDetails(delegation) {
		details[key] = value
	}
	if reason != "" {
		details["error"] = truncateAuditText(reason, 512)
	}

	payload, err := json.Marshal(details)
	if err != nil {
		payload = []byte("{}")
	}
	// 审计写入不阻塞调用方：用 WithoutCancel + 超时，即使客户端断开也要留下记录。
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	actor := ""
	if delegation.DelegatedUserID != "" {
		actor = delegation.DelegatedUserID
	}
	if logErr := s.audit.Log(writeCtx, &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: actor,
		Action:      types.AuditActionMCPToolCalled,
		ScopeType:   "mcp_endpoint",
		TargetType:  "mcp_tool",
		TargetID:    truncateAuditText(toolName, 128),
		Outcome:     outcome,
		Details:     types.JSON(payload),
	}); logErr != nil {
		logger.Warnf(writeCtx, "[mcpserver] audit write failed for %s: %v", toolName, logErr)
	}
}

// truncateAuditText bounds one free-form audit field.
func truncateAuditText(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit])
}

// withAuditedTools decorates every catalog entry: the delegation argument is
// advertised in the schema and the handler is wrapped for auditing.
func (s *Server) withAuditedTools(tools []server.ServerTool) []server.ServerTool {
	out := make([]server.ServerTool, 0, len(tools))
	for _, tool := range tools {
		entry := tool
		entry.Tool = withDelegationArgument(tool.Tool)
		entry.Handler = s.auditToolCall(tool.Tool.Name, tool.Handler)
		out = append(out, entry)
	}
	return out
}

// delegationArgumentSchema documents the optional §16.2 block.
//
// 在注册层统一注入而不是改 16 个工具定义：委派是横切关注点，逐个改必然
// 会有几个漏掉，而"哪几个漏了"没人能发现。它不进 required — 没有声明委派
// 的调用仍然合法（此时权限就是调用者本人），但"没声明"会被审计记录下来。
var delegationArgumentSchema = map[string]any{
	"type":        "object",
	"description": "Optional delegation context: who this call acts for, what it is for, and which business trace it belongs to. Recorded in the audit ledger.",
	"properties": map[string]any{
		"delegated_user_id": map[string]any{
			"type":        "string",
			"description": "End user this call acts for; permissions narrow to this user.",
		},
		"purpose": map[string]any{
			"type":        "string",
			"minLength":   types.DelegationMinPurposeRunes,
			"maxLength":   types.DelegationMaxPurposeRunes,
			"description": "What this call is for, in plain words; placeholders like query are rejected.",
		},
		"trace_id": map[string]any{
			"type":        "string",
			"maxLength":   types.DelegationMaxTraceRunes,
			"description": "Caller's trace id, so a multi-hop workflow can be reassembled.",
		},
		"client": map[string]any{
			"type":        "string",
			"description": "Name of the calling system.",
		},
	},
	"additionalProperties": false,
}

// withDelegationArgument adds the delegation property to one tool's schema.
func withDelegationArgument(tool mcp.Tool) mcp.Tool {
	if tool.InputSchema.Properties == nil {
		tool.InputSchema.Properties = map[string]any{}
	}
	if _, exists := tool.InputSchema.Properties[delegationArgument]; exists {
		return tool
	}
	tool.InputSchema.Properties[delegationArgument] = delegationArgumentSchema
	// Keep the argument last so generated clients show the tool's own
	// parameters first.
	tool.InputSchema.PropertyOrder = append(
		tool.InputSchema.PropertyOrder, delegationArgument)
	return tool
}

// compile-time reminder that the audit service is optional.
var _ interfaces.AuditLogService = (interfaces.AuditLogService)(nil)
