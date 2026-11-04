package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpsdk "github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// recordingAudit captures audit entries.
type recordingAudit struct {
	interfaces.AuditLogService

	mu      sync.Mutex
	entries []*types.AuditLog
	err     error
}

func (r *recordingAudit) Log(_ context.Context, entry *types.AuditLog) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	copyOf := *entry
	r.entries = append(r.entries, &copyOf)
	return nil
}

// entriesSnapshot returns a copy under the lock.
func (r *recordingAudit) entriesSnapshot() []*types.AuditLog {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*types.AuditLog(nil), r.entries...)
}

// endpointContext carries an authenticated MCP endpoint.
func endpointContext(endpoint *types.MCPEndpoint) context.Context {
	return context.WithValue(context.Background(), types.MCPEndpointContextKey, endpoint)
}

// callWithArgs builds an MCP tool call with the given arguments.
func callWithArgs(args map[string]interface{}) mcp.CallToolRequest {
	request := mcp.CallToolRequest{}
	request.Params.Name = "search_knowledge"
	request.Params.Arguments = args
	return request
}

// okHandler returns a successful tool result.
func okHandler(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultText("ok"), nil
}

// TestDelegationFromArgumentsParsesTheBlock covers the happy path.
func TestDelegationFromArgumentsParsesTheBlock(t *testing.T) {
	delegation := delegationFromArguments(map[string]interface{}{
		delegationArgument: map[string]interface{}{
			"delegated_user_id": "u-1",
			"purpose":           "生成月度设备健康报告",
			"trace_id":          "trace-1",
			"client":            "bi-portal",
		},
	})
	assert.Equal(t, "u-1", delegation.DelegatedUserID)
	assert.Equal(t, "生成月度设备健康报告", delegation.Purpose)
	assert.Equal(t, "trace-1", delegation.TraceID)
	assert.Equal(t, "bi-portal", delegation.Client)
	assert.False(t, delegation.ReceivedAt.IsZero())
}

// TestDelegationFromArgumentsRejectsInvalidBlocks treats a malformed block as
// "no delegation" instead of failing the call.
func TestDelegationFromArgumentsRejectsInvalidBlocks(t *testing.T) {
	cases := map[string]map[string]interface{}{
		"missing":       {},
		"placeholder":   {delegationArgument: map[string]interface{}{"purpose": "query"}},
		"too short":     {delegationArgument: map[string]interface{}{"purpose": "查"}},
		"wrong type":    {delegationArgument: "not an object"},
		"empty purpose": {delegationArgument: map[string]interface{}{"purpose": "   "}},
	}
	for label, args := range cases {
		delegation := delegationFromArguments(args)
		assert.Empty(t, delegation.Purpose, label)
	}
}

// TestAuditToolCallRecordsSuccess covers the ledger write.
func TestAuditToolCallRecordsSuccess(t *testing.T) {
	audit := &recordingAudit{}
	server := &Server{}
	server.WithAudit(audit)
	handler := server.auditToolCall("search_knowledge", okHandler)

	result, err := handler(endpointContext(&types.MCPEndpoint{
		ID: "ep-1", TenantID: 7,
	}), callWithArgs(map[string]interface{}{
		delegationArgument: map[string]interface{}{
			"delegated_user_id": "u-1",
			"purpose":           "生成月度设备健康报告",
			"trace_id":          "trace-1",
		},
	}))
	require.NoError(t, err)
	require.NotNil(t, result)

	entries := audit.entriesSnapshot()
	require.Len(t, entries, 1)
	entry := entries[0]
	assert.Equal(t, uint64(7), entry.TenantID)
	assert.Equal(t, types.AuditActionMCPToolCalled, entry.Action)
	assert.Equal(t, "u-1", entry.ActorUserID,
		"the delegated user is the actor, not the endpoint owner")
	assert.Equal(t, types.AuditOutcomeSuccess, entry.Outcome)
	assert.Equal(t, "mcp_tool", entry.TargetType)
	assert.Equal(t, "search_knowledge", entry.TargetID)

	var details map[string]interface{}
	require.NoError(t, json.Unmarshal(entry.Details, &details))
	assert.Equal(t, "search_knowledge", details["tool"])
	assert.Equal(t, "ep-1", details["endpoint_id"])
	assert.Equal(t, "生成月度设备健康报告", details["purpose"])
	assert.Equal(t, "trace-1", details["trace_id"])
	assert.Equal(t, true, details["delegation_present"])
	assert.Contains(t, details, "duration_ms")
}

// TestAuditToolCallFlagsMissingDelegation is the governance signal: an
// undeclared call is still allowed, but the ledger shows it was undeclared.
func TestAuditToolCallFlagsMissingDelegation(t *testing.T) {
	audit := &recordingAudit{}
	server := &Server{}
	server.WithAudit(audit)
	handler := server.auditToolCall("search_knowledge", okHandler)

	_, err := handler(endpointContext(&types.MCPEndpoint{ID: "ep-1", TenantID: 7}),
		callWithArgs(map[string]interface{}{"query": "报警"}))
	require.NoError(t, err)

	entries := audit.entriesSnapshot()
	require.Len(t, entries, 1)
	var details map[string]interface{}
	require.NoError(t, json.Unmarshal(entries[0].Details, &details))
	assert.Equal(t, false, details["delegation_present"])
	assert.NotContains(t, details, "purpose")
	assert.Empty(t, entries[0].ActorUserID)
}

// TestAuditToolCallRecordsFailures covers both failure shapes.
func TestAuditToolCallRecordsFailures(t *testing.T) {
	t.Run("handler error", func(t *testing.T) {
		audit := &recordingAudit{}
		server := &Server{}
		server.WithAudit(audit)
		handler := server.auditToolCall("search_knowledge",
			func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return nil, errors.New("backend down")
			})
		_, err := handler(endpointContext(&types.MCPEndpoint{ID: "ep", TenantID: 7}), callWithArgs(nil))
		require.Error(t, err)

		entries := audit.entriesSnapshot()
		require.Len(t, entries, 1)
		assert.Equal(t, types.AuditOutcomeFailed, entries[0].Outcome)
		var details map[string]interface{}
		require.NoError(t, json.Unmarshal(entries[0].Details, &details))
		assert.Equal(t, "backend down", details["error"])
	})

	t.Run("tool error result", func(t *testing.T) {
		audit := &recordingAudit{}
		server := &Server{}
		server.WithAudit(audit)
		handler := server.auditToolCall("search_knowledge",
			func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return mcp.NewToolResultError("no knowledge base in scope"), nil
			})
		_, err := handler(endpointContext(&types.MCPEndpoint{ID: "ep", TenantID: 7}), callWithArgs(nil))
		require.NoError(t, err)
		entries := audit.entriesSnapshot()
		require.Len(t, entries, 1)
		assert.Equal(t, types.AuditOutcomeFailed, entries[0].Outcome)
	})
}

// TestAuditToolCallSurvivesAuditFailure is the availability rule: a broken
// audit store must not break the tool call.
func TestAuditToolCallSurvivesAuditFailure(t *testing.T) {
	audit := &recordingAudit{err: errors.New("audit table locked")}
	server := &Server{}
	server.WithAudit(audit)
	handler := server.auditToolCall("search_knowledge", okHandler)

	result, err := handler(endpointContext(&types.MCPEndpoint{ID: "ep", TenantID: 7}),
		callWithArgs(nil))
	require.NoError(t, err, "the audit write failure is logged, not returned")
	require.NotNil(t, result)
}

// TestAuditToolCallWithoutServiceIsNoop covers the unconfigured case.
func TestAuditToolCallWithoutServiceIsNoop(t *testing.T) {
	server := &Server{}
	handler := server.auditToolCall("search_knowledge", okHandler)
	result, err := handler(endpointContext(&types.MCPEndpoint{ID: "ep", TenantID: 7}),
		callWithArgs(nil))
	require.NoError(t, err)
	require.NotNil(t, result)
}

// TestAuditToolCallSkipsCallsWithoutEndpoint keeps an unauthenticated call out
// of the ledger (it never reached a real endpoint).
func TestAuditToolCallSkipsCallsWithoutEndpoint(t *testing.T) {
	audit := &recordingAudit{}
	server := &Server{}
	server.WithAudit(audit)
	handler := server.auditToolCall("search_knowledge", okHandler)

	_, err := handler(context.Background(), callWithArgs(nil))
	require.NoError(t, err)
	assert.Empty(t, audit.entriesSnapshot())
}

// TestWithAuditedToolsAdvertisesDelegation proves every tool's schema carries
// the argument, so generated clients can send it.
func TestWithAuditedToolsAdvertisesDelegation(t *testing.T) {
	server := &Server{}
	tools := server.withAuditedTools([]mcpsdk.ServerTool{
		{Tool: mcp.NewTool("a"), Handler: okHandler},
		{Tool: mcp.NewTool("b"), Handler: okHandler},
	})
	require.Len(t, tools, 2)
	for _, tool := range tools {
		properties := tool.Tool.InputSchema.Properties
		require.Contains(t, properties, delegationArgument,
			"%s must advertise the delegation block", tool.Tool.Name)
		// It is optional: undeclared calls stay legal.
		assert.NotContains(t, tool.Tool.InputSchema.Required, delegationArgument)
		// The delegation argument is not required, and the tool's own
		// required list (if any) is untouched.
		assert.NotNil(t, tool.Handler)
	}
}

// TestWithDelegationArgumentIsIdempotent keeps a doubled wrap from duplicating
// the property order entry.
func TestWithDelegationArgumentIsIdempotent(t *testing.T) {
	tool := withDelegationArgument(mcp.NewTool("a"))
	tool = withDelegationArgument(tool)
	count := 0
	for _, name := range tool.InputSchema.PropertyOrder {
		if name == delegationArgument {
			count++
		}
	}
	assert.Equal(t, 1, count)
}
