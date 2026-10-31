package service

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
)

// ACL audit actions (V3.0 §2.8: 权限变更是内容审计的一等事件).
const (
	auditActionACLGranted = "knowledge.acl_granted"
	auditActionACLRevoked = "knowledge.acl_revoked"
)

// knowledgeACLService implements interfaces.KnowledgeACLService.
type knowledgeACLService struct {
	repo  interfaces.KnowledgeACLRepository
	kgRepo interfaces.KnowledgeRepository
	audit interfaces.AuditLogService
	now   func() time.Time
}

// NewKnowledgeACLService constructs the ACL service.
func NewKnowledgeACLService(
	repo interfaces.KnowledgeACLRepository,
	kgRepo interfaces.KnowledgeRepository,
	audit interfaces.AuditLogService,
) interfaces.KnowledgeACLService {
	return &knowledgeACLService{repo: repo, kgRepo: kgRepo, audit: audit, now: time.Now}
}

// Grant creates or updates one ACL row. Callers (Admin+ routes) enforce
// 不能自授 and the authorize action ceiling before reaching here.
func (s *knowledgeACLService) Grant(ctx context.Context, grant *types.KnowledgeACL) (*types.KnowledgeACL, error) {
	if grant == nil || grant.KnowledgeID == "" || grant.SubjectID == "" {
		return nil, apperrors.NewBadRequestError("knowledge_id and subject_id are required")
	}
	if grant.SubjectType == "" {
		grant.SubjectType = string(types.KnowledgeACLSubjectUser)
	}
	if !types.KnowledgeACLSubjectType(grant.SubjectType).Valid() {
		return nil, apperrors.NewBadRequestError("unknown subject type")
	}
	action := types.PermissionAction(grant.Action)
	if !action.Valid() {
		return nil, apperrors.NewBadRequestError("unknown permission action")
	}
	if grant.Effect != string(types.KnowledgeACLEffectAllow) && grant.Effect != string(types.KnowledgeACLEffectDeny) {
		return nil, apperrors.NewBadRequestError("effect must be allow or deny")
	}
	if err := s.repo.UpsertGrant(ctx, grant); err != nil {
		return nil, err
	}
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
				"acl_id":    grant.ID,
				"action":    grant.Action,
				"effect":    grant.Effect,
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
	if err := s.repo.Revoke(ctx, tenantID, grantID); err != nil {
		return err
	}
	if s.audit != nil {
		_ = s.audit.Log(ctx, &types.AuditLog{
			TenantID:    tenantID,
			ActorUserID: actorID,
			Action:      auditActionACLRevoked,
			ScopeType:   "knowledge",
			TargetType:  "acl_grant",
			TargetID:    grantID,
			Outcome:     "success",
		})
	}
	return nil
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
	ids, err := s.repo.DeniedKnowledgeIDs(
		ctx, knowledgeBaseIDs, types.KnowledgeACLSubjectUser, subjectID, action, s.now(),
	)
	if err != nil {
		logger.Warnf(ctx, "ACL denied-ID query failed (fail-closed applies): %v", err)
	}
	return ids, err
}
