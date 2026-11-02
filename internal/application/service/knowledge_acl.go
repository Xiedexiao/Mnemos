package service

import (
	"context"
	"sort"
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
	now      func() time.Time
}

// NewKnowledgeACLService constructs the ACL service.
func NewKnowledgeACLService(
	repo interfaces.KnowledgeACLRepository,
	kgRepo interfaces.KnowledgeRepository,
	audit interfaces.AuditLogService,
	kbShares interfaces.KBShareRepository,
) interfaces.KnowledgeACLService {
	return &knowledgeACLService{
		repo: repo, kgRepo: kgRepo, audit: audit, kbShares: kbShares, now: time.Now,
	}
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
