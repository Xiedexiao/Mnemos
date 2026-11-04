package mcpserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// ============================================================================
// 委派用户的权限收窄（KnowAxis V3.0 §16.2 受控委派）。
//
// 上一批把委派记进了审计，但权限仍然按 endpoint 的合成身份
// （mcp-<endpointID>、Viewer）判定。这意味着**声明了 delegated_user_id 也
// 不会改变这个调用能看到什么**——委派因此只是"写了一条日志"，而不是
// "代表某人执行"。
//
// 这里的实现把委派用户变成真正的调用主体：
//   - 校验它必须是**本租户的活跃成员**，否则任何人都能用一个编造的 ID
//     "收窄"自己的权限（收窄本身无害，但伪造身份会让审计里的 actor 变成
//     一条不存在的记录）；
//   - 用该用户的租户角色替换 synthetic viewer 角色。endpoint 合成身份的
//     角色恒为 Viewer，若委派用户是 Editor/Admin，权限应当按该用户判定；
//     反过来，若该用户只是 Viewer，权限也不会比 endpoint 更高（§2.4 收窄）；
//   - **只收窄不扩大**：委派用户必须仍落在 endpoint 已开放的知识库范围内。
//     一个 Admin 用户通过一个只开放了 A 空间的 endpoint 调用，看不到 B 空间。
//   - 校验失败直接拒绝调用，而不是退化成合成身份：静默降级会让调用方以为
//     自己代表某个用户，实际却是另一个身份在取数。
// ============================================================================

// delegationResolution is the outcome of applying a delegation to a request.
type delegationResolution struct {
	// Caller is the caller the tool must run as.
	Caller types.Caller
	// Applied reports whether the delegation replaced the synthetic identity.
	Applied bool
	// Reason explains why a delegation was rejected ("" when applied or absent).
	Reason string
}

// resolveDelegatedCaller turns a delegation into the effective caller.
//
// ctx 里的 caller 是 MCP 中间件写入的合成身份（`mcp-<endpointID>`，Viewer）。
// 声明了委派时，主体换成该用户，但租户不变：跨租户委派必须被拒绝——它既
// 不是这个 endpoint 能做的事，也会让审计里的 tenant 与 actor 互相矛盾。
func (s *Server) resolveDelegatedCaller(
	ctx context.Context, delegation types.AgentDelegation,
) delegationResolution {
	caller := types.CallerFromContext(ctx)
	if delegation.DelegatedUserID == "" {
		return delegationResolution{Caller: caller}
	}
	if caller.TenantID == 0 {
		return delegationResolution{
			Caller: caller, Reason: "no tenant is in scope for this call",
		}
	}
	// 用户服务没接上时不能验证委派，按拒绝处理：无法确认的身份不能当主体。
	if s.userService == nil || s.memberService == nil {
		return delegationResolution{
			Caller: caller,
			Reason: "delegated identity cannot be verified on this deployment",
		}
	}

	user, err := s.userService.GetUserByID(ctx, delegation.DelegatedUserID)
	if err != nil || user == nil {
		return delegationResolution{
			Caller: caller, Reason: "the delegated user does not exist",
		}
	}
	if user.TenantID != caller.TenantID {
		// 跨租户委派：拒绝。endpoint 的授权范围止于它自己的租户，而委派
		// 只应当收窄这个范围，不可能把它扩到另一个租户。
		return delegationResolution{
			Caller: caller, Reason: "the delegated user belongs to another workspace",
		}
	}
	if !user.IsActive {
		return delegationResolution{
			Caller: caller, Reason: "the delegated user is inactive",
		}
	}
	membership, err := s.memberService.GetMembership(ctx, user.ID, caller.TenantID)
	if err != nil || membership == nil {
		// 不是本租户成员：即便 user.TenantID 相同（历史数据），没有成员行
		// 就没有角色，权限无从判定。
		return delegationResolution{
			Caller: caller, Reason: "the delegated user is not a member of this workspace",
		}
	}
	role := types.TenantRole(membership.Role)
	if !role.IsValid() {
		return delegationResolution{
			Caller: caller, Reason: "the delegated user has no valid role",
		}
	}

	// 角色的**上限**是 endpoint 合成身份的角色：endpoint 恒为 Viewer，因此
	// 委派一个 Admin 也不会让这个 endpoint 获得写权限。§16.2 的委派是
	// "代表某人**看**"，不是"提权"。
	// 取两者的**较低**者：委派只能收窄。role 高于 caller 时按 caller 封顶；
	// role 低于 caller 时保留 role（这本身就是收窄）。
	effective := role
	if caller.Role.IsValid() && roleAtLeast(role, caller.Role) {
		effective = caller.Role
	}

	return delegationResolution{
		Caller: types.Caller{
			TenantID: caller.TenantID,
			UserID:   user.ID,
			Role:     effective,
		},
		Applied: true,
	}
}

// roleAtLeast reports whether role is at least as privileged as floor.
func roleAtLeast(role, floor types.TenantRole) bool {
	rank := func(candidate types.TenantRole) int {
		switch candidate {
		case types.TenantRoleOwner:
			return 4
		case types.TenantRoleAdmin:
			return 3
		case types.TenantRoleContributor:
			return 2
		case types.TenantRoleViewer:
			return 1
		default:
			return 0
		}
	}
	return rank(role) >= rank(floor)
}

// applyDelegation replaces the caller with the delegated user when the
// delegation is valid.
//
// 返回的 error 让调用方**拒绝**这次调用：一个无法验证的委派不能被静默
// 忽略，否则调用方以为自己代表某人，实际是另一个身份在取数——审计里的
// actor 会与真实主体不符，而这正是委派机制要解决的问题。
func (s *Server) applyDelegation(
	ctx context.Context, toolName string, delegation types.AgentDelegation,
) (context.Context, error) {
	resolution := s.resolveDelegatedCaller(ctx, delegation)
	if resolution.Reason == "" {
		if resolution.Applied {
			logger.Infof(ctx, "[mcpserver] tool %s runs as delegated user %s",
				toolName, resolution.Caller.UserID)
			return types.WithCaller(ctx, resolution.Caller), nil
		}
		return ctx, nil
	}
	logger.Warnf(ctx, "[mcpserver] tool %s rejected delegation to %q: %s",
		toolName, delegation.DelegatedUserID, resolution.Reason)
	return ctx, errors.New(delegationRejectionMessage(resolution.Reason))
}

// delegationRejectionMessage is the caller-facing wording.
//
// 不回显具体的用户 ID 或"存在/不存在"的区分：那会让这个参数变成一次
// 用户枚举。只说明"委派无法生效"以及原因类别。
func delegationRejectionMessage(reason string) string {
	if reason == "" {
		return "delegation rejected"
	}
	return fmt.Sprintf("delegation rejected: %s", reason)
}
