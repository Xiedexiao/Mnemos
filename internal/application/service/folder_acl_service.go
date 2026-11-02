package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// folderACLService implements interfaces.FolderACLService (V3.0 §2.3 / §4.1).
//
// Directory grants are the layer between a space and a document: "this folder is
// readable by the supplier quality team" is a far more common intention than
// granting each of forty SOPs individually, and a document-level ACL cannot
// express it.
type folderACLService struct {
	repo interfaces.FolderACLRepository
	now  func() time.Time
}

// NewFolderACLService constructs the folder ACL service.
func NewFolderACLService(repo interfaces.FolderACLRepository) interfaces.FolderACLService {
	return &folderACLService{repo: repo, now: time.Now}
}

// Grant creates or updates a folder grant, enforcing the action ceiling.
//
// §2.3: 子级不能授予父级未开放的动作. The check runs against the parent path's
// folded verdict; a violation is rejected *and* recorded, because an operator
// who keeps hitting the ceiling needs to see the pattern rather than just a
// rejected request.
func (s *folderACLService) Grant(
	ctx context.Context, grant *types.FolderACL,
) (*types.FolderACL, error) {
	if grant == nil || grant.KnowledgeBaseID == "" || grant.SubjectID == "" {
		return nil, apperrors.NewBadRequestError("knowledge_base_id and subject_id are required")
	}
	if grant.SubjectType == "" {
		grant.SubjectType = string(types.KnowledgeACLSubjectUser)
	}
	action := types.PermissionAction(grant.Action)
	if !action.Valid() {
		return nil, apperrors.NewBadRequestError("unknown permission action")
	}
	if grant.Effect == "" {
		grant.Effect = string(types.KnowledgeACLEffectAllow)
	}
	if grant.Effect != string(types.KnowledgeACLEffectAllow) &&
		grant.Effect != string(types.KnowledgeACLEffectDeny) {
		return nil, apperrors.NewBadRequestError("effect must be allow or deny")
	}
	grant.FolderPath = types.NormalizeKnowledgeFolderPath(grant.FolderPath)

	// 动作上限：父目录没有开放的动作，子目录不能开放（deny 不受限——收紧永远
	// 合法）。
	if grant.Effect == string(types.KnowledgeACLEffectAllow) && grant.FolderPath != "" {
		parentPath := parentFolderPath(grant.FolderPath)
		parentGrants, err := s.repo.ListForFolder(ctx, grant.TenantID, grant.KnowledgeBaseID, parentPath)
		if err != nil {
			return nil, err
		}
		if len(parentGrants) > 0 {
			parentDecision := types.FoldFolderACL(parentPath, parentGrants, s.now())
			if violations := types.CheckActionCeiling(
				parentDecision.AllowedActions, []types.PermissionAction{action},
			); len(violations) > 0 {
				s.recordViolation(ctx, grant, types.CeilingAction, map[string]interface{}{
					"action":      string(action),
					"parent_path": parentPath,
				})
				return nil, apperrors.NewForbiddenError(
					"action " + string(action) + " is not open on the parent folder")
			}
		}
	}

	if grant.CreatedAt.IsZero() {
		grant.CreatedAt = s.now()
	}
	if err := s.repo.UpsertGrant(ctx, grant); err != nil {
		return nil, err
	}
	return grant, nil
}

// recordViolation logs a ceiling breach for the governance list.
func (s *folderACLService) recordViolation(
	ctx context.Context, grant *types.FolderACL,
	kind types.CeilingViolationKind, detail map[string]interface{},
) {
	raw, err := json.Marshal(detail)
	if err != nil {
		raw = []byte("{}")
	}
	if err := s.repo.RecordCeilingViolation(ctx, &types.ACLCeilingViolation{
		TenantID:        grant.TenantID,
		KnowledgeBaseID: grant.KnowledgeBaseID,
		ResourceType:    "folder",
		ResourceID:      grant.FolderPath,
		Kind:            string(kind),
		Detail:          types.JSON(raw),
		DetectedAt:      s.now(),
	}); err != nil {
		logger.Warnf(ctx, "failed to record ACL ceiling violation: %v", err)
	}
}

// List returns every folder grant of a space.
func (s *folderACLService) List(
	ctx context.Context, tenantID uint64, kbID string,
) ([]*types.FolderACL, error) {
	if kbID == "" {
		return nil, apperrors.NewBadRequestError("knowledge_base_id is required")
	}
	return s.repo.ListByKnowledgeBase(ctx, tenantID, kbID)
}

// Revoke deletes one grant.
func (s *folderACLService) Revoke(
	ctx context.Context, tenantID uint64, grantID, actorID string,
) error {
	if grantID == "" {
		return apperrors.NewBadRequestError("grant_id is required")
	}
	return s.repo.Revoke(ctx, tenantID, grantID)
}

// ResolveForFolder folds the grants that apply to one path.
func (s *folderACLService) ResolveForFolder(
	ctx context.Context, tenantID uint64, kbID, folderPath string,
) (*types.FolderACLDecision, error) {
	if kbID == "" {
		return nil, apperrors.NewBadRequestError("knowledge_base_id is required")
	}
	normalized := types.NormalizeKnowledgeFolderPath(folderPath)
	grants, err := s.repo.ListForFolder(ctx, tenantID, kbID, normalized)
	if err != nil {
		return nil, err
	}
	decision := types.FoldFolderACL(normalized, grants, s.now())
	return &decision, nil
}

// OnFolderMoved resets the grants of a moved folder's *descendants*.
//
// §2.3 目录移动触发权限重算，默认重置为继承。The moved folder keeps its own
// grants (the operator moved it intentionally), but grants that were made
// relative to the old location's children must not follow silently — those are
// the ones that can widen access at the new location.
func (s *folderACLService) OnFolderMoved(
	ctx context.Context, tenantID uint64, kbID, fromPath, toPath string,
) (int64, error) {
	if kbID == "" || fromPath == "" || toPath == "" {
		return 0, apperrors.NewBadRequestError("kb_id and both paths are required")
	}
	if fromPath == toPath {
		return 0, nil
	}
	removed, err := s.repo.DeleteByFolder(ctx, tenantID, kbID, fromPath)
	if err != nil {
		return 0, err
	}
	if removed > 0 {
		logger.Warnf(ctx,
			"folder moved %s -> %s in %s: dropped %d grant(s); the folder now inherits from its new parent",
			fromPath, toPath, kbID, removed)
	}
	return removed, nil
}

// ListCeilingViolations returns the governance findings.
func (s *folderACLService) ListCeilingViolations(
	ctx context.Context, tenantID uint64, limit int,
) ([]*types.ACLCeilingViolation, error) {
	return s.repo.ListCeilingViolations(ctx, tenantID, limit)
}

// parentFolderPath returns the parent of a normalized folder path ("" for a
// top-level folder or the root).
func parentFolderPath(folderPath string) string {
	normalized := types.NormalizeKnowledgeFolderPath(folderPath)
	index := strings.LastIndex(normalized, "/")
	if index < 0 {
		return ""
	}
	return normalized[:index]
}
