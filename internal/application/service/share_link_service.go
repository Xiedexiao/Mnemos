package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/Tencent/WeKnora/internal/utils"
)

// shareTokenEntropyBytes is the entropy budget for a share link token.
// 32 bytes → 43 base64url chars (no padding), ~256 bits of randomness:
// guessing is not an attack surface, only leaking the URL is.
const shareTokenEntropyBytes = 32

// shareLinkDownloadTTL is how long a minted presigned download URL stays
// valid. 0 lets the presign helper apply its own default.
const shareLinkDownloadTTL = 0

// shareLinkService implements interfaces.ShareLinkService (V3.0 §2.5 v1).
// It is the only door that mints, resolves or revokes external share
// links: the source document's classification is the hard visibility
// ceiling, tokens are minted server-side, and every issuance, revocation
// and resolved visit writes an audit row.
type shareLinkService struct {
	kgRepo interfaces.KnowledgeRepository
	repo   interfaces.ShareLinkRepository
	audit  interfaces.AuditLogService
	acl    interfaces.KnowledgeACLService
	// governance gates external sharing behind an approved receipt (§6.2
	// 外部分享). Optional: nil keeps the classification ceiling as the only
	// gate, which is the pre-existing behaviour.
	governance interfaces.GovernanceApprovalService
	now        func() time.Time
}

// NewShareLinkService constructs the share link service.
//
//	kgRepo — source-document lookups (GetKnowledgeByIDOnly, no tenant filter;
//	         the tenant match is re-checked here, never trusted from the URL)
//	repo   — share_links persistence
//	audit  — audit trail (nil-tolerant degraded mode, like the lifecycle
//	         service: a failed audit write never blocks the business op)
func NewShareLinkService(
	kgRepo interfaces.KnowledgeRepository,
	repo interfaces.ShareLinkRepository,
	audit interfaces.AuditLogService,
	acl interfaces.KnowledgeACLService,
	governance interfaces.GovernanceApprovalService,
) interfaces.ShareLinkService {
	return &shareLinkService{
		kgRepo: kgRepo, repo: repo, audit: audit, acl: acl,
		governance: governance, now: time.Now,
	}
}

// Create issues a share link after the classification ceiling check.
//
//	v1 rules (§2.5):
//	  - confidential 源文档：一律拒绝（禁止外发）
//	  - secret 源文档 + download 权限：拒绝（默认禁止外发导出）
//	  - 其余组合放行，approval_status 直通 approved
func (s *shareLinkService) Create(
	ctx context.Context,
	tenantID uint64,
	knowledgeID string,
	createdBy string,
	linkType types.ShareLinkType,
	permission types.ShareLinkPermission,
	expiresAt *time.Time,
) (*types.ShareLink, error) {
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	if knowledgeID == "" {
		return nil, apperrors.NewBadRequestError("knowledge_id is required")
	}
	if linkType != types.ShareLinkTypeOrg && linkType != types.ShareLinkTypePeople {
		return nil, apperrors.NewValidationError(fmt.Sprintf("link_type must be %s or %s", types.ShareLinkTypeOrg, types.ShareLinkTypePeople))
	}
	if permission != types.ShareLinkPermissionView && permission != types.ShareLinkPermissionDownload {
		return nil, apperrors.NewValidationError(fmt.Sprintf("permission must be %s or %s", types.ShareLinkPermissionView, types.ShareLinkPermissionDownload))
	}

	knowledge, err := s.kgRepo.GetKnowledgeByIDOnly(ctx, knowledgeID)
	if err != nil {
		if errors.Is(err, repository.ErrKnowledgeNotFound) {
			return nil, apperrors.NewNotFoundError("knowledge not found")
		}
		return nil, err
	}
	// 防越权兜底：即便路由守卫被绕过，也只能为本租户的文档建链。
	if knowledge.TenantID != tenantID {
		return nil, apperrors.NewNotFoundError("knowledge not found")
	}

	classification := types.ParseKnowledgeClassification(knowledge.Classification)
	if classification == types.KnowledgeClassificationConfidential {
		return nil, apperrors.NewForbiddenError("confidential knowledge cannot be shared externally")
	}
	if permission == types.ShareLinkPermissionDownload && classification == types.KnowledgeClassificationSecret {
		return nil, apperrors.NewForbiddenError("secret knowledge cannot be shared with download permission")
	}

	// 动作分离（§2.2/§2.5）：能看 ≠ 能分享。KB 角色够、但文档上有 share 的
	// deny 行（或 secret/confidential 无 share 的显式 allow）时不得签链。
	// 权限一旦失效，已签发的链接也必须在 Resolve 时重新判定。
	if err := s.authorizeShare(ctx, knowledge, createdBy); err != nil {
		return nil, err
	}

	// 外部分享是 §6.2 的高风险治理动作：必须有一份获批的审批任务才能签链。
	// 分享链接是外泄的头号入口，仅靠密级上限不够——同密级内容给谁看仍需
	// 有人批准。
	if s.governance != nil && linkType == types.ShareLinkTypePeople {
		if _, gateErr := s.governance.RequireApproval(ctx, tenantID,
			types.ApprovalSubjectShare, knowledgeID); gateErr != nil {
			return nil, apperrors.NewForbiddenError(
				"external sharing requires an approved governance task for this document")
		}
	}

	token, err := mintShareToken()
	if err != nil {
		return nil, apperrors.NewInternalServerError("failed to mint share token")
	}

	link := &types.ShareLink{
		ID:             uuid.NewString(),
		TenantID:       tenantID,
		KnowledgeID:    knowledgeID,
		Token:          token,
		LinkType:       string(linkType),
		Permission:     string(permission),
		ExpiresAt:      expiresAt,
		CreatedBy:      createdBy,
		ApprovalStatus: string(types.ShareLinkApprovalApproved), // v1 直通
		VisitLog:       types.JSON("[]"),
	}
	if err := s.repo.Create(ctx, link); err != nil {
		return nil, err
	}

	s.logAudit(ctx, types.AuditActionShareLinkCreated, link, knowledge.KnowledgeBaseID, createdBy, map[string]interface{}{
		"link_type":  string(linkType),
		"permission": string(permission),
		"has_expiry": expiresAt != nil,
	})
	return link, nil
}

// Resolve turns a visitor token into the public read-only projection.
// Unknown / revoked / expired tokens all answer the same uniform 404 so
// the endpoint never becomes an oracle for "this token used to be valid".
func (s *shareLinkService) Resolve(
	ctx context.Context,
	token string,
	visitor types.ShareLinkVisitorMeta,
) (*types.ShareLinkResolution, error) {
	if token == "" {
		return nil, apperrors.NewNotFoundError(shareLinkUnavailableMessage)
	}
	link, err := s.repo.GetByToken(ctx, token)
	if err != nil {
		if errors.Is(err, repository.ErrShareLinkNotFound) {
			return nil, apperrors.NewNotFoundError(shareLinkUnavailableMessage)
		}
		return nil, err
	}
	if !link.Active(s.now()) {
		// revoked 或过期：与未知 token 同响应，避免状态泄露。
		return nil, apperrors.NewNotFoundError(shareLinkUnavailableMessage)
	}

	knowledge, err := s.kgRepo.GetKnowledgeByIDOnly(ctx, link.KnowledgeID)
	if err != nil {
		if errors.Is(err, repository.ErrKnowledgeNotFound) {
			return nil, apperrors.NewNotFoundError(shareLinkUnavailableMessage)
		}
		return nil, err
	}

	// 链接的权限上限是"源文档此刻的权限"（§2.5）：源文档被提密、下架或加
	// deny 行之后，活链接不能继续外发。与未知 token 同响应，避免泄露状态。
	if !shareLinkStillPermitted(knowledge, link) {
		return nil, apperrors.NewNotFoundError(shareLinkUnavailableMessage)
	}
	if err := s.authorizeShareFor(ctx, knowledge, link.CreatedBy); err != nil {
		return nil, apperrors.NewNotFoundError(shareLinkUnavailableMessage)
	}

	visitedAt := s.now()
	// Visit telemetry is fail-open: a bookkeeping failure must not block a
	// legitimate visitor, the audit row below still records the visit.
	if err := s.repo.AppendVisit(ctx, link.ID, types.ShareLinkVisit{
		At: visitedAt,
		IP: visitor.IP,
		UA: visitor.UserAgent,
	}, types.ShareLinkVisitLogMaxEntries); err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"share_link_id": link.ID,
		})
	}
	s.logAudit(ctx, types.AuditActionShareLinkVisited, link, knowledge.KnowledgeBaseID, "", map[string]interface{}{
		"ip": visitor.IP,
		"ua": visitor.UserAgent,
	})

	return &types.ShareLinkResolution{
		Share: types.ShareLinkPublicMeta{
			Token:      link.Token,
			LinkType:   link.LinkType,
			Permission: link.Permission,
			ExpiresAt:  link.ExpiresAt,
			VisitedAt:  visitedAt,
		},
		Knowledge: buildShareKnowledgeView(ctx, knowledge, link, visitedAt),
	}, nil
}

// Revoke flips revoked_at and writes the share.revoked audit row. The
// (tenant, id) scope keeps the route's Contributor gate tenant-safe.
func (s *shareLinkService) Revoke(ctx context.Context, tenantID uint64, shareLinkID, actorID string) error {
	if tenantID == 0 {
		return apperrors.NewUnauthorizedError("tenant context is required")
	}
	if shareLinkID == "" {
		return apperrors.NewBadRequestError("share_link_id is required")
	}
	rows, err := s.repo.Revoke(ctx, tenantID, shareLinkID, s.now())
	if err != nil {
		return err
	}
	if rows == 0 {
		// Unknown id, another tenant's id, or already revoked.
		return apperrors.NewNotFoundError("share link not found or already revoked")
	}
	link := &types.ShareLink{ID: shareLinkID, TenantID: tenantID}
	s.logAudit(ctx, types.AuditActionShareLinkRevoked, link, "", actorID, nil)
	return nil
}

// ListByKnowledge returns the tenant's links for one knowledge entry.
func (s *shareLinkService) ListByKnowledge(
	ctx context.Context,
	tenantID uint64,
	knowledgeID string,
) ([]*types.ShareLink, error) {
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	if knowledgeID == "" {
		return nil, apperrors.NewBadRequestError("knowledge_id is required")
	}
	return s.repo.ListByKnowledge(ctx, tenantID, knowledgeID)
}

// shareLinkStillPermitted re-applies the source-document ceilings to a live
// link (§2.5 链接权限 ≤ 源文档权限). Classification may have been raised or
// the document taken out of service after the link was minted; an active link
// must stop working in that case rather than keep serving stale authority.
func shareLinkStillPermitted(knowledge *types.Knowledge, link *types.ShareLink) bool {
	if knowledge == nil || link == nil {
		return false
	}
	classification := types.ParseKnowledgeClassification(knowledge.Classification)
	if classification == types.KnowledgeClassificationConfidential {
		return false
	}
	if link.Permission == string(types.ShareLinkPermissionDownload) &&
		classification == types.KnowledgeClassificationSecret {
		return false
	}
	// 生命周期：只有仍然可对外提供内容的状态才允许通过外链访问。
	status := types.ParseKnowledgeLifecycleStatus(knowledge.LifecycleStatus)
	resolved := types.ResolveLifecycle(status, knowledge.EffectiveAt, knowledge.ExpiresAt, time.Now())
	if !resolved.EffectiveStatus.Searchable() {
		return false
	}
	return true
}

// authorizeShare applies the per-document share verdict for the link owner.
// Deny-wins and the secret/confidential allow requirement come from the ACL
// repository, so a revoked grant invalidates links that depend on it.
func (s *shareLinkService) authorizeShare(
	ctx context.Context, knowledge *types.Knowledge, createdBy string,
) error {
	if err := s.authorizeShareFor(ctx, knowledge, createdBy); err != nil {
		return apperrors.NewForbiddenError("knowledge cannot be shared: share permission was revoked")
	}
	return nil
}

// authorizeShareFor is authorizeShare with the caller's error shaping left to
// the caller (Resolve must not distinguish "denied" from "unknown token").
func (s *shareLinkService) authorizeShareFor(
	ctx context.Context, knowledge *types.Knowledge, createdBy string,
) error {
	if knowledge == nil || createdBy == "" {
		return nil
	}
	if s.acl == nil {
		if types.ParseKnowledgeClassification(knowledge.Classification).Rank() >=
			types.KnowledgeClassificationSecret.Rank() {
			return errors.New("share: ACL service unavailable for a classified document")
		}
		return nil
	}
	denied, err := s.acl.DeniedKnowledgeIDs(
		ctx, []string{knowledge.KnowledgeBaseID}, createdBy, types.PermissionActionShare,
	)
	if err != nil {
		return err
	}
	for _, id := range denied {
		if id == knowledge.ID {
			return errors.New("share: the document denies the share action")
		}
	}
	return nil
}

// buildShareKnowledgeView projects the knowledge row to the public,
// read-only visitor view. Built field-by-field — never serialised from
// types.Knowledge — so tenant-internal columns cannot leak through a
// public response. The presigned download URL is minted only when the
// link's permission is download (and the deployment has a signing key);
// otherwise the visitor gets metadata only.
func buildShareKnowledgeView(
	ctx context.Context,
	knowledge *types.Knowledge,
	link *types.ShareLink,
	visitedAt time.Time,
) types.ShareLinkKnowledgeView {
	view := types.ShareLinkKnowledgeView{
		ID:              knowledge.ID,
		Title:           knowledge.Title,
		Description:     knowledge.Description,
		Classification:  string(types.ParseKnowledgeClassification(knowledge.Classification)),
		CurrentVersion:  knowledge.CurrentVersion,
		LifecycleStatus: knowledge.LifecycleStatus,
		FileName:        knowledge.FileName,
		FileType:        knowledge.FileType,
		FileSize:        knowledge.FileSize,
	}
	resolved := types.ResolveLifecycle(
		types.ParseKnowledgeLifecycleStatus(knowledge.LifecycleStatus),
		knowledge.EffectiveAt, knowledge.ExpiresAt, visitedAt,
	)
	view.EffectiveStatus = string(resolved.EffectiveStatus)
	view.LifecycleReasons = resolved.Reasons

	if types.ShareLinkPermission(link.Permission) == types.ShareLinkPermissionDownload && knowledge.FilePath != "" {
		if url, err := utils.SignFileURL("", knowledge.FilePath, knowledge.TenantID, shareLinkDownloadTTL); err == nil {
			view.DownloadURL = url
			view.DownloadAvailable = true
		} else {
			// Signing key not configured: degrade to metadata-only instead
			// of failing the whole resolve.
			logger.ErrorWithFields(ctx, err, map[string]interface{}{
				"share_link_id": link.ID,
				"knowledge_id":  knowledge.ID,
			})
		}
	}
	return view
}

// mintShareToken generates the visitor credential:
// crypto/rand 32 bytes → base64.RawURLEncoding (43 URL-safe chars).
func mintShareToken() (string, error) {
	buf := make([]byte, shareTokenEntropyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("share token: crypto/rand failed: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// shareLinkUnavailableMessage is the uniform public rejection text used
// for unknown, revoked and expired tokens alike.
const shareLinkUnavailableMessage = "share link not found or no longer available"

// logAudit writes one share.* audit row. Nil-tolerant and fail-open,
// mirroring the lifecycle service: an audit write failure is logged but
// never blocks the operation.
func (s *shareLinkService) logAudit(
	ctx context.Context,
	action types.AuditAction,
	link *types.ShareLink,
	kbID, actorID string,
	extra map[string]interface{},
) {
	if s.audit == nil {
		return
	}
	details := map[string]interface{}{
		"share_link_id": link.ID,
		"knowledge_id":  link.KnowledgeID,
	}
	for k, v := range extra {
		details[k] = v
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    link.TenantID,
		ActorUserID: actorID,
		Action:      action,
		ScopeType:   "knowledge",
		ScopeID:     kbID,
		TargetType:  "share_link",
		TargetID:    link.ID,
		Outcome:     types.AuditOutcomeSuccess,
		Details:     shareJSON(details),
	})
}

// shareJSON marshals an audit detail payload, falling back to an empty
// object on failure so a bad payload can never block the operation that
// is being audited.
func shareJSON(v interface{}) types.JSON {
	data, err := json.Marshal(v)
	if err != nil {
		return types.JSON([]byte("{}"))
	}
	return types.JSON(data)
}
