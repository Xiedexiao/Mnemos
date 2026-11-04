package mcpserver

import (
	"context"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubDelegationUsers serves user lookups.
type stubDelegationUsers struct {
	interfaces.UserService

	users map[string]*types.User
	err   error
}

func (s *stubDelegationUsers) GetUserByID(
	_ context.Context, id string,
) (*types.User, error) {
	if s.err != nil {
		return nil, s.err
	}
	user, ok := s.users[id]
	if !ok {
		return nil, errors.New("record not found")
	}
	return user, nil
}

// stubDelegationMembers serves membership lookups.
type stubDelegationMembers struct {
	interfaces.TenantMemberService

	members map[string]*types.TenantMember
	err     error
}

func (s *stubDelegationMembers) GetMembership(
	_ context.Context, userID string, _ uint64,
) (*types.TenantMember, error) {
	if s.err != nil {
		return nil, s.err
	}
	member, ok := s.members[userID]
	if !ok {
		return nil, errors.New("record not found")
	}
	return member, nil
}

// delegationServer builds a server whose delegation lookups are stubbed.
func delegationServer(
	users map[string]*types.User, members map[string]*types.TenantMember,
) *Server {
	return (&Server{}).WithDelegationServices(
		&stubDelegationUsers{users: users},
		&stubDelegationMembers{members: members},
	)
}

// endpointCallerCtx carries the synthetic endpoint identity the middleware sets.
func endpointCallerCtx() context.Context {
	return types.WithCaller(context.Background(), types.Caller{
		TenantID: 7,
		UserID:   "mcp-ep-1",
		Role:     types.TenantRoleViewer,
	})
}

// TestResolveDelegatedCallerReplacesTheSubject is the core rule: the delegated
// user becomes the principal, so the tools' ACL checks run against them.
func TestResolveDelegatedCallerReplacesTheSubject(t *testing.T) {
	server := delegationServer(
		map[string]*types.User{
			"u-1": {ID: "u-1", TenantID: 7, IsActive: true},
		},
		map[string]*types.TenantMember{
			"u-1": {UserID: "u-1", TenantID: 7, Role: types.TenantRoleViewer},
		},
	)
	resolution := server.resolveDelegatedCaller(endpointCallerCtx(),
		types.AgentDelegation{DelegatedUserID: "u-1", Purpose: "生成设备健康报告"})

	require.Empty(t, resolution.Reason)
	assert.True(t, resolution.Applied)
	assert.Equal(t, "u-1", resolution.Caller.UserID,
		"the synthetic endpoint identity is replaced")
	assert.Equal(t, uint64(7), resolution.Caller.TenantID)
}

// TestResolveDelegatedCallerCapsTheRole is the "narrow, never widen" rule: an
// endpoint is a Viewer identity, so delegating an Admin does not grant writes.
func TestResolveDelegatedCallerCapsTheRole(t *testing.T) {
	server := delegationServer(
		map[string]*types.User{
			"admin-1": {ID: "admin-1", TenantID: 7, IsActive: true},
		},
		map[string]*types.TenantMember{
			"admin-1": {UserID: "admin-1", TenantID: 7, Role: types.TenantRoleAdmin},
		},
	)
	resolution := server.resolveDelegatedCaller(endpointCallerCtx(),
		types.AgentDelegation{DelegatedUserID: "admin-1", Purpose: "查看审核积压"})

	require.Empty(t, resolution.Reason)
	assert.True(t, resolution.Applied)
	assert.Equal(t, types.TenantRoleViewer, resolution.Caller.Role,
		"the endpoint's own role caps the delegated role")
}

// TestResolveDelegatedCallerKeepsALowerRole proves narrowing still works.
func TestResolveDelegatedCallerKeepsALowerRole(t *testing.T) {
	// The endpoint identity is a Contributor here (hypothetically), the user
	// is only a Viewer: the call must run at Viewer.
	ctx := types.WithCaller(context.Background(), types.Caller{
		TenantID: 7, UserID: "mcp-ep-1", Role: types.TenantRoleContributor,
	})
	server := delegationServer(
		map[string]*types.User{"u-1": {ID: "u-1", TenantID: 7, IsActive: true}},
		map[string]*types.TenantMember{
			"u-1": {UserID: "u-1", TenantID: 7, Role: types.TenantRoleViewer},
		},
	)
	resolution := server.resolveDelegatedCaller(ctx,
		types.AgentDelegation{DelegatedUserID: "u-1", Purpose: "查看文档"})
	require.Empty(t, resolution.Reason)
	assert.Equal(t, types.TenantRoleViewer, resolution.Caller.Role)
}

// TestResolveDelegatedCallerRejectsUnknownUsers keeps an invented id from
// becoming an audit actor.
func TestResolveDelegatedCallerRejectsUnknownUsers(t *testing.T) {
	server := delegationServer(nil, nil)
	resolution := server.resolveDelegatedCaller(endpointCallerCtx(),
		types.AgentDelegation{DelegatedUserID: "ghost", Purpose: "生成报告"})
	assert.False(t, resolution.Applied)
	assert.Equal(t, "the delegated user does not exist", resolution.Reason)
}

// TestResolveDelegatedCallerRejectsCrossTenantUsers is the scope rule: an
// endpoint's grant stops at its own workspace.
func TestResolveDelegatedCallerRejectsCrossTenantUsers(t *testing.T) {
	server := delegationServer(
		map[string]*types.User{
			"foreign": {ID: "foreign", TenantID: 9, IsActive: true},
		},
		map[string]*types.TenantMember{
			"foreign": {UserID: "foreign", TenantID: 7, Role: types.TenantRoleViewer},
		},
	)
	resolution := server.resolveDelegatedCaller(endpointCallerCtx(),
		types.AgentDelegation{DelegatedUserID: "foreign", Purpose: "生成报告"})
	assert.False(t, resolution.Applied)
	assert.Equal(t, "the delegated user belongs to another workspace", resolution.Reason)
}

// TestResolveDelegatedCallerRejectsInactiveUsers covers the disabled account.
func TestResolveDelegatedCallerRejectsInactiveUsers(t *testing.T) {
	server := delegationServer(
		map[string]*types.User{
			"gone": {ID: "gone", TenantID: 7, IsActive: false},
		},
		nil,
	)
	resolution := server.resolveDelegatedCaller(endpointCallerCtx(),
		types.AgentDelegation{DelegatedUserID: "gone", Purpose: "生成报告"})
	assert.False(t, resolution.Applied)
	assert.Equal(t, "the delegated user is inactive", resolution.Reason)
}

// TestResolveDelegatedCallerRequiresMembership: same tenant is not enough —
// without a membership row there is no role to apply.
func TestResolveDelegatedCallerRequiresMembership(t *testing.T) {
	server := delegationServer(
		map[string]*types.User{
			"stranger": {ID: "stranger", TenantID: 7, IsActive: true},
		},
		nil,
	)
	resolution := server.resolveDelegatedCaller(endpointCallerCtx(),
		types.AgentDelegation{DelegatedUserID: "stranger", Purpose: "生成报告"})
	assert.False(t, resolution.Applied)
	assert.Equal(t, "the delegated user is not a member of this workspace", resolution.Reason)
}

// TestResolveDelegatedCallerWithoutServicesRejects is the fail-closed rule.
func TestResolveDelegatedCallerWithoutServicesRejects(t *testing.T) {
	server := &Server{}
	resolution := server.resolveDelegatedCaller(endpointCallerCtx(),
		types.AgentDelegation{DelegatedUserID: "u-1", Purpose: "生成报告"})
	assert.False(t, resolution.Applied)
	assert.Equal(t, "delegated identity cannot be verified on this deployment",
		resolution.Reason)
}

// TestResolveDelegatedCallerWithoutTenantRejects keeps an unscoped call honest.
func TestResolveDelegatedCallerWithoutTenantRejects(t *testing.T) {
	server := delegationServer(nil, nil)
	resolution := server.resolveDelegatedCaller(context.Background(),
		types.AgentDelegation{DelegatedUserID: "u-1", Purpose: "生成报告"})
	assert.False(t, resolution.Applied)
	assert.Equal(t, "no tenant is in scope for this call", resolution.Reason)
}

// TestResolveDelegatedCallerWithoutDelegationIsPassthrough keeps the default
// behaviour unchanged.
func TestResolveDelegatedCallerWithoutDelegationIsPassthrough(t *testing.T) {
	server := &Server{}
	ctx := endpointCallerCtx()
	resolution := server.resolveDelegatedCaller(ctx, types.AgentDelegation{})
	assert.False(t, resolution.Applied)
	assert.Empty(t, resolution.Reason)
	assert.Equal(t, types.CallerFromContext(ctx), resolution.Caller)
}

// TestApplyDelegationPutsTheUserOnTheContext is the plumbing assertion: the
// tools downstream read the caller from the context.
func TestApplyDelegationPutsTheUserOnTheContext(t *testing.T) {
	server := delegationServer(
		map[string]*types.User{"u-1": {ID: "u-1", TenantID: 7, IsActive: true}},
		map[string]*types.TenantMember{
			"u-1": {UserID: "u-1", TenantID: 7, Role: types.TenantRoleViewer},
		},
	)
	ctx, err := server.applyDelegation(endpointCallerCtx(), "search_knowledge",
		types.AgentDelegation{DelegatedUserID: "u-1", Purpose: "生成报告"})
	require.NoError(t, err)
	assert.Equal(t, "u-1", types.CallerFromContext(ctx).UserID)
	// The delegation itself is still readable for downstream traceability.
	_, ok := types.AgentDelegationFromContext(ctx)
	assert.False(t, ok, "applyDelegation does not attach the block itself")
}

// TestApplyDelegationRejectsUnverifiableIdentities: an error means the caller
// must refuse the call, not fall back to the endpoint identity.
func TestApplyDelegationRejectsUnverifiableIdentities(t *testing.T) {
	server := delegationServer(nil, nil)
	ctx := endpointCallerCtx()
	_, err := server.applyDelegation(ctx, "search_knowledge",
		types.AgentDelegation{DelegatedUserID: "ghost", Purpose: "生成报告"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delegation rejected")
	assert.Equal(t, "mcp-ep-1", types.CallerFromContext(ctx).UserID,
		"the original context is left untouched")
}

// TestDelegationRejectionMessageHidesUserIds keeps the parameter from becoming
// a user-enumeration oracle.
func TestDelegationRejectionMessageHidesUserIds(t *testing.T) {
	message := delegationRejectionMessage("the delegated user does not exist")
	assert.NotContains(t, message, "ghost")
	assert.Equal(t, "delegation rejected: the delegated user does not exist", message)
}

// TestAuditedToolRunsAsTheDelegatedUser is the end-to-end assertion: the
// handler sees the delegated principal.
func TestAuditedToolRunsAsTheDelegatedUser(t *testing.T) {
	server := delegationServer(
		map[string]*types.User{"u-1": {ID: "u-1", TenantID: 7, IsActive: true}},
		map[string]*types.TenantMember{
			"u-1": {UserID: "u-1", TenantID: 7, Role: types.TenantRoleViewer},
		},
	)
	var seenUserID string
	handler := server.auditToolCall("search_knowledge",
		func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			seenUserID = types.CallerFromContext(ctx).UserID
			return mcp.NewToolResultText("ok"), nil
		})

	ctx := endpointCallerCtx()
	_, err := handler(ctx, callWithArgs(map[string]interface{}{
		delegationArgument: map[string]interface{}{
			"delegated_user_id": "u-1",
			"purpose":           "生成月度设备健康报告",
		},
	}))
	require.NoError(t, err)
	assert.Equal(t, "u-1", seenUserID,
		"the tool ran as the delegated user, not the endpoint identity")
}

// TestAuditedToolRefusesInvalidDelegation proves a rejected delegation never
// reaches the tool.
func TestAuditedToolRefusesInvalidDelegation(t *testing.T) {
	server := delegationServer(nil, nil)
	called := false
	handler := server.auditToolCall("search_knowledge",
		func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			called = true
			return mcp.NewToolResultText("ok"), nil
		})

	result, err := handler(endpointCallerCtx(), callWithArgs(map[string]interface{}{
		delegationArgument: map[string]interface{}{
			"delegated_user_id": "ghost",
			"purpose":           "生成月度设备健康报告",
		},
	}))
	require.NoError(t, err, "the refusal is a tool error, not a transport error")
	require.NotNil(t, result)
	assert.True(t, result.IsError)
	assert.False(t, called, "the tool must not run under an unverified principal")
}
