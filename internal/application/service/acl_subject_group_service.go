package service

import (
	"context"
	"errors"
	"strings"

	"github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// 主体组（§2.1）的审计动作。
const (
	auditActionACLGroupCreated = "knowledge.acl_group_created"
	auditActionACLGroupUpdated = "knowledge.acl_group_updated"
	auditActionACLGroupDeleted = "knowledge.acl_group_deleted"
	auditActionACLGroupMembers = "knowledge.acl_group_members_changed"
)

// aclSubjectGroupService implements interfaces.ACLSubjectGroupService.
type aclSubjectGroupService struct {
	repo     interfaces.ACLSubjectGroupRepository
	versions interfaces.PermissionVersionService
	audit    interfaces.AuditLogService
}

// NewACLSubjectGroupService constructs the service.
func NewACLSubjectGroupService(
	repo interfaces.ACLSubjectGroupRepository,
	versions interfaces.PermissionVersionService,
	audit interfaces.AuditLogService,
) interfaces.ACLSubjectGroupService {
	return &aclSubjectGroupService{repo: repo, versions: versions, audit: audit}
}

// CreateGroup creates one group.
func (s *aclSubjectGroupService) CreateGroup(
	ctx context.Context, tenantID uint64, actorID, name, description string,
) (*types.ACLSubjectGroup, error) {
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	normalized, err := types.NormalizeACLGroupName(name)
	if err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	group := &types.ACLSubjectGroup{
		TenantID: tenantID, Name: normalized,
		Description: trimToRunes(description, 512), CreatedBy: actorID,
	}
	if err := s.repo.CreateGroup(ctx, group); err != nil {
		return nil, mapACLGroupError(err)
	}
	s.logAudit(ctx, tenantID, actorID, auditActionACLGroupCreated, group.ID,
		map[string]interface{}{"name": group.Name})
	return group, nil
}

// ListGroups returns the tenant's groups.
func (s *aclSubjectGroupService) ListGroups(
	ctx context.Context, tenantID uint64,
) ([]*types.ACLSubjectGroup, error) {
	return s.repo.ListGroups(ctx, tenantID)
}

// UpdateGroup renames / re-describes one group.
//
// 改名会让已经按 group_id 写好的授权继续生效（授权存的是 ID 不是名字），
// 这是刻意的：重命名不应该等于收权，否则运维改个名字就悄悄改动了访问范围。
func (s *aclSubjectGroupService) UpdateGroup(
	ctx context.Context, tenantID uint64, groupID, actorID, name, description string,
) (*types.ACLSubjectGroup, error) {
	group, err := s.repo.GetGroup(ctx, tenantID, groupID)
	if err != nil {
		return nil, mapACLGroupError(err)
	}
	if trimmed := trimToRunes(name, types.MaxACLGroupNameLen); trimmed != "" {
		normalized, nameErr := types.NormalizeACLGroupName(trimmed)
		if nameErr != nil {
			return nil, apperrors.NewBadRequestError(nameErr.Error())
		}
		group.Name = normalized
	}
	if description != "" {
		group.Description = trimToRunes(description, 512)
	}
	if err := s.repo.UpdateGroup(ctx, group); err != nil {
		return nil, mapACLGroupError(err)
	}
	s.logAudit(ctx, tenantID, actorID, auditActionACLGroupUpdated, group.ID,
		map[string]interface{}{"name": group.Name})
	return group, nil
}

// DeleteGroup removes one group and its memberships.
//
// 删除组不会删除按该组写的授权行：它们是历史事实，而且组 ID 不再被任何人
// 持有，等价于自然失效。这里只推进权限版本，让缓存立刻失效。
func (s *aclSubjectGroupService) DeleteGroup(
	ctx context.Context, tenantID uint64, groupID, actorID string,
) error {
	if err := s.repo.DeleteGroup(ctx, tenantID, groupID); err != nil {
		return mapACLGroupError(err)
	}
	s.bumpTenantEpoch(ctx, tenantID, "acl_group_deleted")
	s.logAudit(ctx, tenantID, actorID, auditActionACLGroupDeleted, groupID, nil)
	return nil
}

// AddMembers adds users to a group.
func (s *aclSubjectGroupService) AddMembers(
	ctx context.Context, tenantID uint64, groupID, actorID string, userIDs []string,
) (int64, error) {
	if _, err := s.repo.GetGroup(ctx, tenantID, groupID); err != nil {
		return 0, mapACLGroupError(err)
	}
	added, err := s.repo.AddMembers(ctx, tenantID, groupID, userIDs, actorID)
	if err != nil {
		return 0, err
	}
	if added > 0 {
		// 新增成员意味着这些人立刻获得组上的全部授权：缓存必须失效。
		s.bumpTenantEpoch(ctx, tenantID, "acl_group_member_added")
	}
	s.logAudit(ctx, tenantID, actorID, auditActionACLGroupMembers, groupID,
		map[string]interface{}{"added": added, "user_ids": userIDs})
	return added, nil
}

// RemoveMember removes one user from a group.
func (s *aclSubjectGroupService) RemoveMember(
	ctx context.Context, tenantID uint64, groupID, userID, actorID string,
) error {
	if _, err := s.repo.GetGroup(ctx, tenantID, groupID); err != nil {
		return mapACLGroupError(err)
	}
	if err := s.repo.RemoveMember(ctx, tenantID, groupID, userID); err != nil {
		return err
	}
	// 退组是撤权：必须推进版本，否则缓存里的旧判定会继续放行。
	s.bumpTenantEpoch(ctx, tenantID, "acl_group_member_removed")
	s.logAudit(ctx, tenantID, actorID, auditActionACLGroupMembers, groupID,
		map[string]interface{}{"removed": userID})
	return nil
}

// ListMembers returns a group's member ids.
func (s *aclSubjectGroupService) ListMembers(
	ctx context.Context, tenantID uint64, groupID string,
) ([]string, error) {
	return s.repo.ListMembers(ctx, tenantID, groupID)
}

// SubjectsForUser expands one user into every subject they hold.
//
// 组查询失败时返回错误而不是只给 user 主体：漏掉组会让 deny 失效（越权），
// 调用方必须 fail-closed，所以这里不吞错。
func (s *aclSubjectGroupService) SubjectsForUser(
	ctx context.Context, tenantID uint64, userID string, role types.TenantRole,
) ([]types.ACLSubject, error) {
	subjects := make([]types.ACLSubject, 0, 8)
	if userID != "" {
		subjects = append(subjects, types.ACLSubject{
			Type: types.KnowledgeACLSubjectUser, ID: userID,
		})
	}
	if role.IsValid() {
		subjects = append(subjects, types.ACLSubject{
			Type: types.KnowledgeACLSubjectRole, ID: string(role),
		})
	}
	if s.repo != nil && userID != "" {
		groupIDs, err := s.repo.GroupIDsForUser(ctx, tenantID, userID)
		if err != nil {
			return nil, err
		}
		for _, groupID := range groupIDs {
			subjects = append(subjects, types.ACLSubject{
				Type: types.KnowledgeACLSubjectGroup, ID: groupID,
			})
		}
	}
	return subjects, nil
}

// bumpTenantEpoch advances the tenant epoch so snapshot caches miss.
func (s *aclSubjectGroupService) bumpTenantEpoch(
	ctx context.Context, tenantID uint64, reason string,
) {
	if s.versions == nil || tenantID == 0 {
		return
	}
	if _, err := s.versions.Bump(ctx, tenantID, types.PermissionScopeTenant, "", reason); err != nil {
		logger.Warnf(ctx, "failed to bump permission version for tenant %d: %v", tenantID, err)
	}
}

// logAudit writes one group audit event; failures never block the write.
func (s *aclSubjectGroupService) logAudit(
	ctx context.Context, tenantID uint64, actorID, action, groupID string,
	details map[string]interface{},
) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: actorID,
		Action:      types.AuditAction(action),
		ScopeType:   "acl_subject_group",
		TargetType:  "group",
		TargetID:    groupID,
		Outcome:     "success",
		Details:     gamificationAuditJSON(details),
	})
}

// mapACLGroupError maps repository misses onto the API 404.
func mapACLGroupError(err error) error {
	if errors.Is(err, repository.ErrACLGroupNotFound) {
		return apperrors.NewNotFoundError("subject group not found")
	}
	return err
}

// trimToRunes trims and caps a string by runes.
func trimToRunes(value string, limit int) string {
	trimmed := []rune(strings.TrimSpace(value))
	if len(trimmed) <= limit {
		return string(trimmed)
	}
	return string(trimmed[:limit])
}
