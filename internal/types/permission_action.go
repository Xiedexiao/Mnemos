package types

// PermissionAction enumerates the fine-grained permission actions of the
// platform (KnowAxis V3.0 §2.2). "能读到"和"能进入 AI 回答""能下载""能转发"
// 是四件事：每个动作独立判定，不合并。
type PermissionAction string

const (
	// PermissionActionView 查看文档详情与正文。
	PermissionActionView PermissionAction = "view"
	// PermissionActionSearch 出现在搜索结果中（可被搜到但不一定能打开）。
	PermissionActionSearch PermissionAction = "search"
	// PermissionActionAsk 可作为 RAG 证据进入回答（与 view 分离：涉密可看不可问）。
	PermissionActionAsk PermissionAction = "ask"
	// PermissionActionCite 可作为引用展示给用户。
	PermissionActionCite PermissionAction = "cite"
	// PermissionActionDownload 下载原文件或附件（按密级与空间策略，水印留痕）。
	PermissionActionDownload PermissionAction = "download"
	// PermissionActionShare 创建分享链接或邀请访客。
	PermissionActionShare PermissionAction = "share"
	// PermissionActionManage 编辑内容、移动、归档（不自动包含 authorize）。
	PermissionActionManage PermissionAction = "manage"
	// PermissionActionAuthorize 配置权限、授权他人（不能自授；受上限约束）。
	PermissionActionAuthorize PermissionAction = "authorize"
	// PermissionActionPublishOfficial 发布官方答案、规则、L3 权威知识。
	PermissionActionPublishOfficial PermissionAction = "publish_official"
	// PermissionActionReview 审核、抽检、驳回（作者不能自审）。
	PermissionActionReview PermissionAction = "review"
	// PermissionActionExport 批量导出、导出证据包（阈值审批、水印留痕）。
	PermissionActionExport PermissionAction = "export"
	// PermissionActionAPICall 通过 API 调用知识能力。
	PermissionActionAPICall PermissionAction = "api_call"
	// PermissionActionAgentCall Agent / MCP 获取知识上下文（身份 ∩ 委托用户权限）。
	PermissionActionAgentCall PermissionAction = "agent_call"
)

// Valid reports whether the value is a known permission action.
func (a PermissionAction) Valid() bool {
	switch a {
	case PermissionActionView, PermissionActionSearch, PermissionActionAsk,
		PermissionActionCite, PermissionActionDownload, PermissionActionShare,
		PermissionActionManage, PermissionActionAuthorize, PermissionActionPublishOfficial,
		PermissionActionReview, PermissionActionExport, PermissionActionAPICall,
		PermissionActionAgentCall:
		return true
	default:
		return false
	}
}

// DefaultRoleActions is the default role→action matrix (V3.0 §2.9). Spaces
// may override per-KB, but no override may breach classification ceilings.
// Conditional actions (marked in the design matrix) stay out of the static
// map until the approval engine can gate them; the comment names the target.
var DefaultRoleActions = map[TenantRole][]PermissionAction{
	TenantRoleOwner: {
		PermissionActionView, PermissionActionSearch, PermissionActionAsk, PermissionActionCite,
		PermissionActionDownload, PermissionActionShare, PermissionActionManage,
		PermissionActionAuthorize, PermissionActionReview, PermissionActionExport,
		// 条件动作：publish_official 需绑定审批凭证（A2 闭环后接入判定）。
	},
	TenantRoleAdmin: {
		PermissionActionView, PermissionActionSearch, PermissionActionAsk, PermissionActionCite,
		PermissionActionDownload, PermissionActionShare, PermissionActionManage,
		PermissionActionReview, PermissionActionExport,
	},
	TenantRoleContributor: {
		PermissionActionView, PermissionActionSearch, PermissionActionAsk, PermissionActionCite,
		PermissionActionDownload,
		// share/manage 条件：manage 仅限自己创建/协作的内容（所有权判定在路由层）。
	},
	TenantRoleViewer: {
		PermissionActionView, PermissionActionSearch, PermissionActionAsk, PermissionActionCite,
		// download 条件：按密级与空间策略。
	},
}

// RoleHasAction reports whether the fixed role ladder grants the action by
// default. It is the v1 bridge between the legacy numeric role floors
// (RequireRole) and the 13-action model; per-document ACL always applies on
// top (deny wins).
func RoleHasAction(role TenantRole, action PermissionAction) bool {
	actions, ok := DefaultRoleActions[role]
	if !ok {
		return false
	}
	for _, a := range actions {
		if a == action {
			return true
		}
	}
	return false
}
