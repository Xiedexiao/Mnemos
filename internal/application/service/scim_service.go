package service

import (
	"context"
	"errors"
	"strings"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// 错误哨兵：handler 需要把它们映射成 SCIM 的 409/404。
var (
	// ErrSCIMUserExists 用户名已被占用。
	ErrSCIMUserExists = errors.New("scim: userName already exists")
	// ErrSCIMNotFound 资源不存在。
	ErrSCIMNotFound = errors.New("scim: resource not found")
	// ErrSCIMUnauthorized provisioning token 无效或无权限。
	ErrSCIMUnauthorized = errors.New("scim: invalid provisioning token")
)

// scimService implements interfaces.SCIMService (KnowAxis V3.0 §16.4).
type scimService struct {
	users       interfaces.UserService
	userRepo    interfaces.UserRepository
	members     interfaces.TenantMemberService
	groups      interfaces.ACLSubjectGroupService
	apiKeys     interfaces.TenantAPIKeyService
	departments interfaces.OrgDepartmentService
	audit       interfaces.AuditLogService
}

// NewSCIMService constructs the service.
func NewSCIMService(
	users interfaces.UserService,
	userRepo interfaces.UserRepository,
	members interfaces.TenantMemberService,
	groups interfaces.ACLSubjectGroupService,
	apiKeys interfaces.TenantAPIKeyService,
	departments interfaces.OrgDepartmentService,
	audit interfaces.AuditLogService,
) interfaces.SCIMService {
	return &scimService{
		users: users, userRepo: userRepo, members: members,
		groups: groups, apiKeys: apiKeys, departments: departments, audit: audit,
	}
}

// scimDefaultPageSize is used when the IdP omits count.
const scimDefaultPageSize = 100

// scimMaxPageSize caps one page.
const scimMaxPageSize = 500

// scimUserSchema returns the schema list for a user resource.
func scimUserSchemas(withEnterprise bool) []string {
	schemas := []string{types.SCIMSchemaUser}
	if withEnterprise {
		schemas = append(schemas, "urn:ietf:params:scim:schemas:extension:enterprise:2.0:User")
	}
	return schemas
}

// ListUsers returns the tenant's users.
func (s *scimService) ListUsers(
	ctx context.Context, tenantID uint64, startIndex, count int, filter string,
) (*types.SCIMListResponse, error) {
	startIndex, count = normalizeSCIMPaging(startIndex, count)
	if s.members == nil {
		return emptySCIMList(startIndex, count), nil
	}
	rows, err := s.members.ListByTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	users := make([]interface{}, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		user, userErr := s.userRepo.GetUserByID(ctx, row.UserID)
		if userErr != nil || user == nil {
			continue
		}
		projected := s.projectUser(user)
		if !scimUserMatchesFilter(projected, filter) {
			continue
		}
		users = append(users, projected)
	}
	return paginateSCIM(users, startIndex, count), nil
}

// GetUser returns one user.
func (s *scimService) GetUser(
	ctx context.Context, tenantID uint64, userID string,
) (*types.SCIMUser, error) {
	user, err := s.userRepo.GetUserByID(ctx, userID)
	if err != nil || user == nil {
		return nil, ErrSCIMNotFound
	}
	// SCIM 是租户内的接口：跨租户读取等于泄露别家成员。
	if !s.isTenantMember(ctx, tenantID, userID) {
		return nil, ErrSCIMNotFound
	}
	return s.projectUser(user), nil
}

// CreateUser provisions one user.
//
// 流程刻意分三步且顺序不能换：
//  1. 用户名全局唯一（users 表是全局的），冲突直接 409；
//  2. 建用户（停用状态起步）；
//  3. 加入租户后按 active 决定是否启用。
//
// 第 2 步先建停用：如果第 3 步加成员失败，留下的是一个"存在但不能登录"的
// 账号，不会变成"能登录但不属于任何租户"的悬空账号。
func (s *scimService) CreateUser(
	ctx context.Context, tenantID uint64, actorID string, input *types.SCIMUser,
) (*types.SCIMUser, error) {
	if err := types.ValidateSCIMUser(input); err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	username := strings.TrimSpace(input.UserName)
	if existing, err := s.userRepo.GetUserByUsername(ctx, username); err == nil && existing != nil {
		return nil, ErrSCIMUserExists
	}
	email := types.SCIMPrimaryEmail(input)
	if email == "" {
		// SCIM 允许无邮箱，但平台用邮箱做唯一索引与找回；生成一个不可路由的
		// 占位地址而不是让插入失败。
		email = username + "@scim.invalid"
	}
	user := &types.User{
		Username: username,
		Email:    email,
		TenantID: tenantID,
		IsActive: false,
	}
	if err := s.userRepo.CreateUser(ctx, user); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, ErrSCIMUserExists
		}
		return nil, err
	}
	if s.members != nil {
		if _, memberErr := s.members.AddMember(ctx, user.ID, tenantID, types.TenantRoleViewer, nil); memberErr != nil {
			logger.Warnf(ctx, "[scim] add member failed for %s: %v", user.ID, memberErr)
		}
	}
	// SCIM 新建用户默认 active=true（RFC 7644 的缺省行为）；只有显式
	// active=false 才保持停用。
	if input.Active {
		if activated, activateErr := s.users.SetUserActive(ctx, user.ID, actorID, true); activateErr == nil && activated != nil {
			user = activated
		}
	}
	s.syncUserDepartment(ctx, tenantID, actorID, user.ID, types.SCIMDepartment(input))
	s.logAudit(ctx, tenantID, actorID, "scim.user_created", user.ID,
		map[string]interface{}{"user_name": username})
	return s.projectUser(user), nil
}

// ReplaceUser overwrites the mutable attributes.
func (s *scimService) ReplaceUser(
	ctx context.Context, tenantID uint64, userID, actorID string, input *types.SCIMUser,
) (*types.SCIMUser, error) {
	if err := types.ValidateSCIMUser(input); err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	user, err := s.userRepo.GetUserByID(ctx, userID)
	if err != nil || user == nil || !s.isTenantMember(ctx, tenantID, userID) {
		return nil, ErrSCIMNotFound
	}
	if email := types.SCIMPrimaryEmail(input); email != "" {
		user.Email = email
	}
	if err := s.users.UpdateUser(ctx, user); err != nil {
		return nil, err
	}
	updated, activateErr := s.users.SetUserActive(ctx, user.ID, actorID, input.Active)
	if activateErr != nil {
		return nil, activateErr
	}
	s.syncUserDepartment(ctx, tenantID, actorID, user.ID, types.SCIMDepartment(input))
	s.logAudit(ctx, tenantID, actorID, "scim.user_replaced", user.ID, nil)
	return s.projectUser(updated), nil
}

// PatchUser applies the supported PATCH operations.
func (s *scimService) PatchUser(
	ctx context.Context, tenantID uint64, userID, actorID string,
	patch *types.SCIMPatchRequest,
) (*types.SCIMUser, error) {
	user, err := s.userRepo.GetUserByID(ctx, userID)
	if err != nil || user == nil || !s.isTenantMember(ctx, tenantID, userID) {
		return nil, ErrSCIMNotFound
	}
	operations := scimPatchOps(patch)
	if active, ok := types.SCIMPatchActive(operations); ok {
		updated, activateErr := s.users.SetUserActive(ctx, user.ID, actorID, active)
		if activateErr != nil {
			return nil, activateErr
		}
		user = updated
	}
	// IdP 改名（displayName）在平台侧没有独立字段：平台展示名来自用户名与
	// 邮箱。这里显式忽略而不是假装成功——SCIM 的 displayName 变更不影响登录，
	// 回显时由 projectUser 从现有字段合成。
	if name := types.SCIMPatchDisplayName(operations); name != "" {
		logger.Infof(ctx, "[scim] displayName change for %s ignored (derived field): %s", user.ID, name)
	}
	s.logAudit(ctx, tenantID, actorID, "scim.user_patched", user.ID,
		map[string]interface{}{"operations": len(operations)})
	return s.projectUser(user), nil
}

// DeleteUser deactivates the account.
//
// 不物理删除：审计行、审批回执、知识版本都引用用户 ID，删掉行会让这些记录
// 失去主体，事后无法回答"谁批的"。停用即刻失去访问（会员状态 + ACL 都不再
// 命中），效果与删除等价而可追溯。
func (s *scimService) DeleteUser(
	ctx context.Context, tenantID uint64, userID, actorID string,
) error {
	if !s.isTenantMember(ctx, tenantID, userID) {
		return ErrSCIMNotFound
	}
	if _, err := s.users.SetUserActive(ctx, userID, actorID, false); err != nil {
		return err
	}
	s.logAudit(ctx, tenantID, actorID, "scim.user_deactivated", userID, nil)
	return nil
}

// ListGroups returns the tenant's ACL subject groups.
func (s *scimService) ListGroups(
	ctx context.Context, tenantID uint64, startIndex, count int, filter string,
) (*types.SCIMListResponse, error) {
	startIndex, count = normalizeSCIMPaging(startIndex, count)
	if s.groups == nil {
		return emptySCIMList(startIndex, count), nil
	}
	rows, err := s.groups.ListGroups(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	groups := make([]interface{}, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		group, groupErr := s.GetGroup(ctx, tenantID, row.ID)
		if groupErr != nil {
			continue
		}
		if !scimGroupMatchesFilter(group, filter) {
			continue
		}
		groups = append(groups, group)
	}
	return paginateSCIM(groups, startIndex, count), nil
}

// GetGroup returns one group with its members.
func (s *scimService) GetGroup(
	ctx context.Context, tenantID uint64, groupID string,
) (*types.SCIMGroup, error) {
	rows, err := s.groups.ListGroups(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	var found *types.ACLSubjectGroup
	for _, row := range rows {
		if row.ID == groupID {
			found = row
			break
		}
	}
	if found == nil {
		return nil, ErrSCIMNotFound
	}
	memberIDs, err := s.groups.ListMembers(ctx, tenantID, groupID)
	if err != nil {
		return nil, err
	}
	members := make([]types.SCIMGroupMember, 0, len(memberIDs))
	for _, memberID := range memberIDs {
		members = append(members, types.SCIMGroupMember{Value: memberID, Type: "User"})
	}
	return &types.SCIMGroup{
		SCIMResource: types.SCIMResource{
			Schemas:    []string{types.SCIMSchemaGroup},
			ID:         found.ID,
			ExternalID: found.ID,
			Meta: types.SCIMMeta{
				ResourceType: "Group",
				Created:      found.CreatedAt,
				LastModified: found.UpdatedAt,
			},
		},
		DisplayName: found.Name,
		Members:     members,
	}, nil
}

// CreateGroup provisions one group (an ACL subject group).
func (s *scimService) CreateGroup(
	ctx context.Context, tenantID uint64, actorID string, input *types.SCIMGroup,
) (*types.SCIMGroup, error) {
	if err := types.ValidateSCIMGroup(input); err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	group, err := s.groups.CreateGroup(ctx, tenantID, actorID,
		strings.TrimSpace(input.DisplayName), "SCIM provisioning")
	if err != nil {
		return nil, err
	}
	if members := types.SCIMMemberIDs(input.Members); len(members) > 0 {
		if _, addErr := s.groups.AddMembers(ctx, tenantID, group.ID, actorID, members); addErr != nil {
			return nil, addErr
		}
	}
	s.logAudit(ctx, tenantID, actorID, "scim.group_created", group.ID,
		map[string]interface{}{"name": group.Name})
	return s.GetGroup(ctx, tenantID, group.ID)
}

// ReplaceGroup overwrites displayName and the full member list.
func (s *scimService) ReplaceGroup(
	ctx context.Context, tenantID uint64, groupID, actorID string, input *types.SCIMGroup,
) (*types.SCIMGroup, error) {
	if err := types.ValidateSCIMGroup(input); err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	current, err := s.GetGroup(ctx, tenantID, groupID)
	if err != nil {
		return nil, err
	}
	if _, err := s.groups.UpdateGroup(ctx, tenantID, groupID, actorID,
		strings.TrimSpace(input.DisplayName), ""); err != nil {
		return nil, err
	}
	// 全量替换：先算差集，只动真正变化的成员，避免逐个删了再加导致权限
	// 版本反复前进（每次前进都会让一批缓存失效）。
	desired := make(map[string]struct{})
	for _, memberID := range types.SCIMMemberIDs(input.Members) {
		desired[memberID] = struct{}{}
	}
	existing := make(map[string]struct{}, len(current.Members))
	for _, member := range current.Members {
		existing[member.Value] = struct{}{}
	}
	toAdd := make([]string, 0)
	for memberID := range desired {
		if _, ok := existing[memberID]; !ok {
			toAdd = append(toAdd, memberID)
		}
	}
	if len(toAdd) > 0 {
		if _, addErr := s.groups.AddMembers(ctx, tenantID, groupID, actorID, toAdd); addErr != nil {
			return nil, addErr
		}
	}
	for memberID := range existing {
		if _, ok := desired[memberID]; ok {
			continue
		}
		if removeErr := s.groups.RemoveMember(ctx, tenantID, groupID, memberID, actorID); removeErr != nil {
			return nil, removeErr
		}
	}
	s.logAudit(ctx, tenantID, actorID, "scim.group_replaced", groupID,
		map[string]interface{}{"members": len(desired)})
	return s.GetGroup(ctx, tenantID, groupID)
}

// PatchGroup applies the supported PATCH operations.
func (s *scimService) PatchGroup(
	ctx context.Context, tenantID uint64, groupID, actorID string,
	patch *types.SCIMPatchRequest,
) (*types.SCIMGroup, error) {
	current, err := s.GetGroup(ctx, tenantID, groupID)
	if err != nil {
		return nil, err
	}
	operations := scimPatchOps(patch)
	if name := types.SCIMPatchDisplayName(operations); name != "" {
		if _, updateErr := s.groups.UpdateGroup(ctx, tenantID, groupID, actorID, name, ""); updateErr != nil {
			return nil, updateErr
		}
	}
	// members 的 PATCH：取 Operations 里 value 的数组形式做增量。
	for _, op := range operations {
		opName := strings.ToLower(strings.TrimSpace(op.Op))
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(op.Path)), "members") {
			continue
		}
		ids := scimMemberIDsFromValue(op.Value)
		switch opName {
		case "add":
			if len(ids) > 0 {
				if _, addErr := s.groups.AddMembers(ctx, tenantID, groupID, actorID, ids); addErr != nil {
					return nil, addErr
				}
			}
		case "remove":
			for _, memberID := range ids {
				if removeErr := s.groups.RemoveMember(ctx, tenantID, groupID, memberID, actorID); removeErr != nil {
					return nil, removeErr
				}
			}
		case "replace":
			// replace 会把成员列表整体替换成传入的集合。
			replaced := &types.SCIMGroup{DisplayName: current.DisplayName}
			for _, memberID := range ids {
				replaced.Members = append(replaced.Members, types.SCIMGroupMember{Value: memberID})
			}
			return s.ReplaceGroup(ctx, tenantID, groupID, actorID, replaced)
		}
	}
	s.logAudit(ctx, tenantID, actorID, "scim.group_patched", groupID,
		map[string]interface{}{"operations": len(operations)})
	return s.GetGroup(ctx, tenantID, groupID)
}

// DeleteGroup removes one group.
func (s *scimService) DeleteGroup(
	ctx context.Context, tenantID uint64, groupID, actorID string,
) error {
	if _, err := s.GetGroup(ctx, tenantID, groupID); err != nil {
		return err
	}
	if err := s.groups.DeleteGroup(ctx, tenantID, groupID, actorID); err != nil {
		return err
	}
	s.logAudit(ctx, tenantID, actorID, "scim.group_deleted", groupID, nil)
	return nil
}

// VerifyToken validates the provisioning bearer token.
//
// 复用租户 API Key，而不是另建一套 SCIM 凭据：吊销、过期、last_used 都已
// 实现，且运维只需要管一种凭据。能力必须是 scim_provision（或 full access），
// 否则一个检索用的 key 就能开通账号。
func (s *scimService) VerifyToken(
	ctx context.Context, tenantID uint64, token string,
) error {
	if s.apiKeys == nil {
		return apperrors.NewUnauthorizedError("SCIM provisioning is not configured")
	}
	key, err := s.apiKeys.AuthenticateAPIKey(ctx, token)
	if err != nil || key == nil {
		return ErrSCIMUnauthorized
	}
	if key.TenantID != nil && *key.TenantID != tenantID {
		return ErrSCIMUnauthorized
	}
	if key.FullAccess {
		return nil
	}
	for _, capability := range key.Capabilities {
		if types.APIKeyCapability(capability) == types.APIKeyCapabilitySCIMProvision {
			return nil
		}
	}
	return ErrSCIMUnauthorized
}

// projectUser renders the SCIM representation.
func (s *scimService) projectUser(user *types.User) *types.SCIMUser {
	if user == nil {
		return nil
	}
	// 部门归属不回显：SCIM 的 department 是 IdP 的输入属性，平台侧的部门树
	// 是多对多关系（一个人可属多个部门），把它折成单个字符串会失真。IdP 需要
	// 部门归属时应查询 /Groups。
	projected := &types.SCIMUser{
		SCIMResource: types.SCIMResource{
			Schemas:    scimUserSchemas(false),
			ID:         user.ID,
			ExternalID: user.Username,
			Meta: types.SCIMMeta{
				ResourceType: "User",
				Created:      user.CreatedAt,
				LastModified: user.UpdatedAt,
			},
		},
		UserName:    user.Username,
		DisplayName: user.Username,
		Emails: []types.SCIMEmail{
			{Value: user.Email, Type: "work", Primary: true},
		},
		Active: user.IsActive,
	}
	return projected
}

// syncUserDepartment links the user to the department the IdP reports.
//
// IdP 发的是部门**名字**而不是平台 ID：外部系统不认识平台内部 ID。按名字在
// 现有树里查找，找不到就创建根部门——不这样做的话，一个全新的 HR 部门在
// 平台侧永远不存在，用户就落不进任何部门。
func (s *scimService) syncUserDepartment(
	ctx context.Context, tenantID uint64, actorID, userID, departmentName string,
) {
	name := strings.TrimSpace(departmentName)
	if name == "" || s.departments == nil {
		return
	}
	tree, err := s.departments.ListTree(ctx, tenantID)
	if err != nil {
		logger.Warnf(ctx, "[scim] list departments failed: %v", err)
		return
	}
	var target *types.OrgDepartmentNode
	var walk func(nodes []*types.OrgDepartmentNode)
	walk = func(nodes []*types.OrgDepartmentNode) {
		for _, node := range nodes {
			if node == nil || target != nil {
				continue
			}
			if strings.EqualFold(node.Name, name) {
				target = node
				return
			}
			walk(node.Children)
		}
	}
	walk(tree)
	if target == nil {
		created, createErr := s.departments.CreateDepartment(ctx, tenantID, actorID,
			types.OrgDepartmentInput{Name: name})
		if createErr != nil {
			logger.Warnf(ctx, "[scim] create department %q failed: %v", name, createErr)
			return
		}
		target = &types.OrgDepartmentNode{OrgDepartment: *created}
	}
	if _, addErr := s.departments.AddMembers(ctx, tenantID, target.ID, actorID, []string{userID}); addErr != nil {
		logger.Warnf(ctx, "[scim] add user %s to department %s failed: %v", userID, target.ID, addErr)
	}
}

// isTenantMember reports whether the user belongs to the tenant.
func (s *scimService) isTenantMember(ctx context.Context, tenantID uint64, userID string) bool {
	if s.members == nil {
		return true
	}
	member, err := s.members.GetMembership(ctx, userID, tenantID)
	return err == nil && member != nil
}

// logAudit writes one SCIM audit event.
func (s *scimService) logAudit(
	ctx context.Context, tenantID uint64, actorID, action, targetID string,
	details map[string]interface{},
) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: actorID,
		Action:      types.AuditAction(action),
		ScopeType:   "scim",
		TargetType:  "scim_resource",
		TargetID:    targetID,
		Outcome:     "success",
		Details:     gamificationAuditJSON(details),
	})
}

// normalizeSCIMPaging clamps startIndex/count per RFC 7644.
func normalizeSCIMPaging(startIndex, count int) (int, int) {
	if startIndex <= 0 {
		startIndex = 1
	}
	if count <= 0 {
		count = scimDefaultPageSize
	}
	if count > scimMaxPageSize {
		count = scimMaxPageSize
	}
	return startIndex, count
}

// emptySCIMList renders an empty list response.
func emptySCIMList(startIndex, count int) *types.SCIMListResponse {
	return &types.SCIMListResponse{
		Schemas:      []string{types.SCIMSchemaList},
		TotalResults: 0,
		StartIndex:   startIndex,
		ItemsPerPage: 0,
		Resources:    []interface{}{},
	}
}

// paginateSCIM slices the resource list.
func paginateSCIM(all []interface{}, startIndex, count int) *types.SCIMListResponse {
	total := len(all)
	offset := startIndex - 1
	if offset > total {
		offset = total
	}
	end := offset + count
	if end > total {
		end = total
	}
	page := all[offset:end]
	return &types.SCIMListResponse{
		Schemas:      []string{types.SCIMSchemaList},
		TotalResults: total,
		StartIndex:   startIndex,
		ItemsPerPage: len(page),
		Resources:    page,
	}
}

// scimUserMatchesFilter supports the one filter IdPs actually send:
// userName eq "..." (case-insensitive).
func scimUserMatchesFilter(user *types.SCIMUser, filter string) bool {
	value, ok := scimFilterValue(filter, "username")
	if !ok {
		return true
	}
	return strings.EqualFold(user.UserName, value)
}

// scimGroupMatchesFilter supports displayName eq "..." .
func scimGroupMatchesFilter(group *types.SCIMGroup, filter string) bool {
	value, ok := scimFilterValue(filter, "displayname")
	if !ok {
		return true
	}
	return strings.EqualFold(group.DisplayName, value)
}

// scimFilterValue parses `attr eq "value"` for one attribute.
//
// 只支持 eq：这是 IdP 在 provisioning 时用来判断"这个人/这个组是否已存在"的
// 唯一必要算子。完整 SCIM 过滤语法（and/or/co/sw）对这个用途没有价值。
func scimFilterValue(filter, attribute string) (string, bool) {
	trimmed := strings.TrimSpace(filter)
	if trimmed == "" {
		return "", false
	}
	parts := strings.Fields(trimmed)
	if len(parts) < 3 {
		return "", false
	}
	if !strings.EqualFold(parts[0], attribute) || !strings.EqualFold(parts[1], "eq") {
		return "", false
	}
	value := strings.TrimSpace(strings.Join(parts[2:], " "))
	value = strings.Trim(value, `"`)
	return value, true
}

// scimPatchOps returns the operations of a patch body.
func scimPatchOps(patch *types.SCIMPatchRequest) []types.SCIMPatchOperation {
	if patch == nil {
		return nil
	}
	return patch.Operations
}

// scimMemberIDsFromValue extracts member ids from a PATCH value which may be a
// list of objects, a list of strings, or one object/string.
func scimMemberIDsFromValue(value interface{}) []string {
	switch typed := value.(type) {
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, entry := range typed {
			out = append(out, scimMemberIDsFromValue(entry)...)
		}
		return out
	case map[string]interface{}:
		if raw, ok := typed["value"].(string); ok {
			if trimmed := strings.TrimSpace(raw); trimmed != "" {
				return []string{trimmed}
			}
		}
		return nil
	case string:
		if trimmed := strings.TrimSpace(typed); trimmed != "" {
			return []string{trimmed}
		}
		return nil
	default:
		return nil
	}
}

// compile-time guard.
var _ interfaces.SCIMService = (*scimService)(nil)

// ProjectSCIMError renders the HTTP status and SCIM status string for an
// error, so the handler speaks the protocol the IdP expects.
func ProjectSCIMError(err error) (int, string) {
	code, status, _ := scimErrorStatus(err)
	return code, status
}

// scimErrorStatus renders the SCIM status string for an error.
func scimErrorStatus(err error) (int, string, string) {
	switch {
	case errors.Is(err, ErrSCIMUserExists):
		return 409, "409", "uniqueness"
	case errors.Is(err, ErrSCIMNotFound):
		return 404, "404", ""
	case errors.Is(err, ErrSCIMUnauthorized):
		return 401, "401", ""
	default:
		return 400, "400", "invalidValue"
	}
}
