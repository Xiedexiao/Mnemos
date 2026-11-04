package service

import (
	"context"
	"sort"
	"strings"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ACL audit actions (V3.0 §2.8: 权限变更是内容审计的一等事件).
const (
	auditActionACLGranted = "knowledge.acl_granted"
	auditActionACLRevoked = "knowledge.acl_revoked"
)

// knowledgeACLService implements interfaces.KnowledgeACLService.
type knowledgeACLService struct {
	repo   interfaces.KnowledgeACLRepository
	kgRepo interfaces.KnowledgeRepository
	audit  interfaces.AuditLogService
	// kbShares supplies the space-level reachability used by MovePreview.
	// Optional: nil means the preview reports document-level deltas only.
	kbShares interfaces.KBShareRepository
	// versions bumps the authorization epoch on every change so snapshot-holding
	// caches miss immediately (V3.0 §2.6). Optional: nil skips the bump.
	versions interfaces.PermissionVersionService
	// subjects expands a caller into group/role subjects for the deny query.
	subjects aclSubjectResolver
	// departments answers "is this grant target inside the caller's managed
	// subtree" (§2.1). Optional: nil skips that check (installs without an
	// organization tree).
	departments interfaces.OrgDepartmentService
	groups      interfaces.ACLSubjectGroupService
	now         func() time.Time
}

// NewKnowledgeACLService constructs the ACL service.
func NewKnowledgeACLService(
	repo interfaces.KnowledgeACLRepository,
	kgRepo interfaces.KnowledgeRepository,
	audit interfaces.AuditLogService,
	kbShares interfaces.KBShareRepository,
	versions interfaces.PermissionVersionService,
	subjectGroups interfaces.ACLSubjectGroupService,
	departments interfaces.OrgDepartmentService,
) interfaces.KnowledgeACLService {
	return &knowledgeACLService{
		repo: repo, kgRepo: kgRepo, audit: audit, kbShares: kbShares,
		versions: versions, subjects: newACLSubjectResolver(subjectGroups),
		departments: departments, groups: subjectGroups,
		now: time.Now,
	}
}

// Grant creates or updates one ACL row. Callers (Admin+ routes) enforce
// 不能自授 and the authorize action ceiling before reaching here.
func (s *knowledgeACLService) Grant(ctx context.Context, grant *types.KnowledgeACL) (*types.KnowledgeACL, error) {
	if grant == nil || grant.KnowledgeID == "" || grant.SubjectID == "" {
		return nil, apperrors.NewBadRequestError("knowledge_id and subject_id are required")
	}
	subjectType, err := types.SubjectTypeForACLAction(grant.SubjectType, grant.SubjectID)
	if err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	grant.SubjectType = string(subjectType)
	grant.SubjectID = strings.TrimSpace(grant.SubjectID)
	action := types.PermissionAction(grant.Action)
	if !action.Valid() {
		return nil, apperrors.NewBadRequestError("unknown permission action")
	}
	if grant.Effect != string(types.KnowledgeACLEffectAllow) && grant.Effect != string(types.KnowledgeACLEffectDeny) {
		return nil, apperrors.NewBadRequestError("effect must be allow or deny")
	}
	// 授权上限（§2.1/§2.3）：不能自授、不能授予自己没有的动作、按角色授权
	// 仅限 Admin+。放在服务层而不是只放 handler：任何新的调用方（批量导入、
	// Agent 工具）都必须过同一道门。
	caller := types.CallerFromContext(ctx)
	ceiling := types.GrantCeilingInput{
		GrantorRole:   caller.Role,
		Action:        action,
		Effect:        types.KnowledgeACLEffect(grant.Effect),
		SubjectType:   subjectType,
		SubjectID:     grant.SubjectID,
		GrantorUserID: caller.UserID,
	}
	// 部门范围（§2.1）：只有当授权对象是一个部门组时才有"位置"可比较。
	ceiling.SubjectDepartmentPath, ceiling.GrantorScope = s.departmentScope(ctx, caller, subjectType, grant.SubjectID)
	if ceilErr := types.CheckGrantCeiling(ceiling); ceilErr != nil {
		return nil, apperrors.NewForbiddenError(ceilErr.Error())
	}
	if err := s.repo.UpsertGrant(ctx, grant); err != nil {
		return nil, err
	}
	// 任何授权变更都让权限版本前进（§2.6）：新增 deny 会立刻缩小可见范围，
	// 缓存键随之失效，不需要逐通道发失效消息。
	s.bumpEpoch(ctx, grant.TenantID, grant.KnowledgeID, permissionEpochReason(string(types.RevocationTriggerACLGrant), grant.Action))
	if s.audit != nil {
		_ = s.audit.Log(ctx, &types.AuditLog{
			TenantID:    grant.TenantID,
			ActorUserID: grant.GrantedBy,
			Action:      auditActionACLGranted,
			ScopeType:   "knowledge",
			ScopeID:     grant.KnowledgeID,
			TargetType:  "user",
			TargetID:    grant.SubjectID,
			Outcome:     "success",
			Details: lifecycleJSON(map[string]interface{}{
				"acl_id":     grant.ID,
				"action":     grant.Action,
				"effect":     grant.Effect,
				"expires_at": grant.ExpiresAt,
			}),
		})
	}
	return grant, nil
}

// ListByKnowledge returns all grants of one knowledge entry.
func (s *knowledgeACLService) ListByKnowledge(ctx context.Context, knowledgeID string) ([]*types.KnowledgeACL, error) {
	return s.repo.ListByKnowledge(ctx, knowledgeID)
}

// Revoke deletes one grant within the tenant.
func (s *knowledgeACLService) Revoke(ctx context.Context, tenantID uint64, grantID, actorID string) error {
	// Resolve the document first: the row is gone after the delete, and the
	// revocation sweep (§2.6) is recorded against the document it applied to.
	grant, err := s.repo.GetByID(ctx, tenantID, grantID)
	if err != nil {
		return err
	}
	if err := s.repo.Revoke(ctx, tenantID, grantID); err != nil {
		return err
	}
	if s.audit != nil {
		_ = s.audit.Log(ctx, &types.AuditLog{
			TenantID:    tenantID,
			ActorUserID: actorID,
			Action:      auditActionACLRevoked,
			ScopeType:   "knowledge",
			ScopeID:     grant.KnowledgeID,
			TargetType:  "acl_grant",
			TargetID:    grantID,
			Outcome:     "success",
		})
	}
	// 撤权是 §2.6 的核心事件：先推进权限版本让缓存键失效，再把十个出口的
	// 处置结果记进台账，回答"撤权之后每个出口都验证过看不到"。
	s.bumpEpoch(ctx, tenantID, grant.KnowledgeID, string(types.RevocationTriggerACLRevoke))
	s.recordRevocation(ctx, tenantID, grant.KnowledgeID, grant.SubjectID, types.RevocationTriggerACLRevoke)
	return nil
}

// bumpEpoch advances the permission epoch of one document. Failure is logged
// but never fails the authorization change itself: the change already took
// effect in the database (the source of truth), and the epoch is what makes
// snapshot caches miss. Callers that must be strict use the returned error.
func (s *knowledgeACLService) bumpEpoch(
	ctx context.Context, tenantID uint64, knowledgeID, reason string,
) {
	if s.versions == nil || tenantID == 0 || knowledgeID == "" {
		return
	}
	var kbID string
	if s.kgRepo != nil {
		if knowledge, err := s.kgRepo.GetKnowledgeByIDOnly(ctx, knowledgeID); err == nil && knowledge != nil {
			kbID = knowledge.KnowledgeBaseID
		}
	}
	if _, err := s.versions.BumpForKnowledge(ctx, tenantID, knowledgeID, kbID, reason); err != nil {
		logger.Warnf(ctx, "failed to bump permission version for knowledge %s: %v", knowledgeID, err)
	}
}

// recordRevocation writes the §2.6 outlet sweep. Best-effort: the revocation
// itself already happened, and a ledger write failure must not resurrect it.
func (s *knowledgeACLService) recordRevocation(
	ctx context.Context, tenantID uint64, knowledgeID, subjectID string,
	trigger types.RevocationTrigger,
) {
	if s.versions == nil || tenantID == 0 || knowledgeID == "" {
		return
	}
	if _, err := s.versions.RecordRevocation(ctx, tenantID, knowledgeID, subjectID, trigger); err != nil {
		logger.Warnf(ctx, "failed to record revocation propagation for %s: %v", knowledgeID, err)
	}
}

// DeniedKnowledgeIDs answers the deny-wins authorization query used before
// recall and after it (chunk re-check). Failures must fail closed: callers
// treat an error as "deny everything in scope" (V3.0 §2.6 fail-closed).
func (s *knowledgeACLService) DeniedKnowledgeIDs(
	ctx context.Context,
	knowledgeBaseIDs []string,
	subjectID string,
	action types.PermissionAction,
) ([]string, error) {
	// 用上下文里的调用者角色展开组/角色主体；subjectID 与调用者不一致时
	// （管理员代查）只按该用户本身判定，不能借用调用者的组授权。
	caller := types.CallerFromContext(ctx)
	subjects := []types.ACLSubject{
		{Type: types.KnowledgeACLSubjectUser, ID: subjectID},
	}
	if caller.UserID == subjectID && caller.UserID != "" {
		expanded, err := s.subjects.ForCaller(ctx, caller)
		if err != nil {
			// 组展开失败=组 deny 无法确认，越权风险，fail-closed。
			logger.Warnf(ctx, "ACL subject expansion failed (fail-closed applies): %v", err)
			return nil, err
		}
		subjects = expanded
	}
	ids, err := s.repo.DeniedKnowledgeIDsForSubjects(
		ctx, knowledgeBaseIDs, subjects, action, s.now(),
	)
	if err != nil {
		logger.Warnf(ctx, "ACL denied-ID query failed (fail-closed applies): %v", err)
	}
	return ids, err
}

// MovePreview answers "who gains and who loses access" before a cross-space
// move is confirmed (V3.0 §2.3).
//
// The move itself resets per-document grants so the document falls back to the
// target space's policy. That makes the preview a comparison of the two
// spaces' reachability plus a count of the document grants about to be reset:
//
//	retained — subjects holding a grant on the document today. After the move
//	           they depend on the target space, so the operator decides whether
//	           to recreate the grant.
//	gained   — subjects reachable only through the target space. This is the
//	           silent widening the spec forbids without an explicit notice.
//	lost     — subjects reachable only through the source space.
func (s *knowledgeACLService) MovePreview(
	ctx context.Context, knowledgeID, sourceKBID, targetKBID string,
) (*types.KnowledgeMoveAccessPreview, error) {
	if knowledgeID == "" || sourceKBID == "" || targetKBID == "" {
		return nil, apperrors.NewBadRequestError("knowledge_id, source_kb_id and target_kb_id are required")
	}
	preview := &types.KnowledgeMoveAccessPreview{
		KnowledgeID: knowledgeID, SourceKBID: sourceKBID, TargetKBID: targetKBID,
	}

	grants, err := s.repo.ListByKnowledge(ctx, knowledgeID)
	if err != nil {
		return nil, err
	}
	docSubjects := make(map[string]bool, len(grants))
	for _, grant := range grants {
		if grant == nil || grant.SubjectID == "" {
			continue
		}
		docSubjects[grant.SubjectID] = true
	}
	preview.ResetGrantCount = len(docSubjects)
	preview.RetainedSubjectIDs = sortedSubjectIDs(docSubjects)

	// Space-level reachability comes from the KB share graph: it is what
	// actually decides who can open an unshared document in this deployment.
	sourceReach, err := s.kbReachSubjectIDs(ctx, sourceKBID)
	if err != nil {
		return nil, err
	}
	targetReach, err := s.kbReachSubjectIDs(ctx, targetKBID)
	if err != nil {
		return nil, err
	}
	preview.GainedSubjectIDs = differenceSubjectIDs(targetReach, sourceReach)
	preview.LostSubjectIDs = differenceSubjectIDs(sourceReach, targetReach)
	preview.RequiresConfirmation = len(preview.GainedSubjectIDs) > 0 ||
		len(preview.LostSubjectIDs) > 0 || preview.ResetGrantCount > 0
	return preview, nil
}

// kbReachSubjectIDs returns the tenant/org IDs that can read the knowledge base
// through its shares. An empty set means "no explicit share subjects"; the
// preview then reports only document-level differences rather than implying
// that nobody has access.
func (s *knowledgeACLService) kbReachSubjectIDs(ctx context.Context, kbID string) (map[string]bool, error) {
	reach := make(map[string]bool)
	if s.kbShares == nil {
		return reach, nil
	}
	shares, err := s.kbShares.ListByKnowledgeBase(ctx, kbID)
	if err != nil {
		return nil, err
	}
	for _, share := range shares {
		if share == nil || share.OrganizationID == "" {
			continue
		}
		reach[share.OrganizationID] = true
	}
	return reach, nil
}

// sortedSubjectIDs renders a subject set deterministically for the API.
func sortedSubjectIDs(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// differenceSubjectIDs returns a − b as a sorted slice.
func differenceSubjectIDs(a, b map[string]bool) []string {
	out := make([]string, 0, len(a))
	for id := range a {
		if !b[id] {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// departmentScope resolves the subject's department path and the caller's
// managed scope for the grant ceiling (§2.1).
//
// 只有组主体可能是部门：用户的部门归属不能作为比较位置（一个人可以兼任多
// 个部门，授权对象是他本人而不是某个位置）。返回 nil 作用域表示"未评估"，
// 保持既有行为而不是阻断所有授权——没有组织树的部署不该因此无法授权。
func (s *knowledgeACLService) departmentScope(
	ctx context.Context, caller types.Caller,
	subjectType types.KnowledgeACLSubjectType, subjectID string,
) (string, *types.DepartmentScope) {
	if s.departments == nil || subjectType != types.KnowledgeACLSubjectGroup {
		return "", nil
	}
	// Admin+ 是全租户范围，不必查部门表。
	if caller.Role.HasPermission(types.TenantRoleAdmin) {
		scope := types.DepartmentScope{UserID: caller.UserID, TenantWide: true}
		return "", &scope
	}
	subjectPath := ""
	if s.groups != nil {
		if groups, err := s.groups.ListGroups(ctx, caller.TenantID); err == nil {
			for _, group := range groups {
				if group == nil || group.ID != subjectID {
					continue
				}
				// 组名可能对应一个部门；按名字找部门节点。
				if tree, treeErr := s.departments.ListTree(ctx, caller.TenantID); treeErr == nil {
					subjectPath = findDepartmentPathByName(tree, group.Name)
				}
				break
			}
		}
	}
	if subjectPath == "" {
		// 手工创建的组不挂在部门上：没有位置可比，交给其它规则判定。
		return "", nil
	}
	scope, err := s.departments.ScopeOf(ctx, caller.TenantID, caller.UserID)
	if err != nil {
		// 范围查询失败时按"无权限"处理：这是权限判定，不能因为读失败就放行。
		logger.Warnf(ctx, "[acl] department scope lookup failed: %v", err)
		empty := types.DepartmentScope{UserID: caller.UserID}
		return subjectPath, &empty
	}
	return subjectPath, &scope
}

// findDepartmentPathByName returns the path of the first department whose name
// matches, depth-first.
func findDepartmentPathByName(nodes []*types.OrgDepartmentNode, name string) string {
	for _, node := range nodes {
		if node == nil {
			continue
		}
		if node.Name == name {
			return node.Path
		}
		if found := findDepartmentPathByName(node.Children, name); found != "" {
			return found
		}
	}
	return ""
}
