package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Comment audit actions (V3.0 §5.2: 协作讨论是知识治理的一等事件).
const (
	auditActionCommentCreated  = "comment.created"
	auditActionCommentResolved = "comment.resolved"
	auditActionCommentDeleted  = "comment.deleted"
)

// maxCommentBodyLen bounds one comment body (10000 runes ≈ 一屏多的讨论
// 发言；长内容应进文档而不是评论). Mirrors no column limit (TEXT), it is a
// sanity guard against paste-dumps.
const maxCommentBodyLen = 10000

// commentService implements interfaces.CommentService.
type commentService struct {
	repo  interfaces.CommentRepository
	audit interfaces.AuditLogService
	now   func() time.Time
}

// NewCommentService constructs the comment service.
func NewCommentService(
	repo interfaces.CommentRepository,
	audit interfaces.AuditLogService,
) interfaces.CommentService {
	return &commentService{repo: repo, audit: audit, now: time.Now}
}

// commentDetailsJSON marshals an audit detail payload, falling back to an
// empty object on failure so logging never blocks a comment write. Local to
// this vertical and deliberately not named like the lifecycle/feedback
// helpers (same keep-verticals-independent rationale as glossaryDetailsJSON).
func commentDetailsJSON(v interface{}) types.JSON {
	data, err := json.Marshal(v)
	if err != nil {
		return types.JSON([]byte("{}"))
	}
	return types.JSON(data)
}

// AddComment creates one comment or reply. Replies must reference a live
// top-level comment of the SAME knowledge entry (parent 存在性 + 同知识校验
// + 一层嵌套). The route guard already verified the caller's KB write
// access; tenant/knowledge/author come from the handler-built comment.
func (s *commentService) AddComment(
	ctx context.Context, comment *types.KnowledgeComment,
) (*types.KnowledgeComment, error) {
	if comment == nil {
		return nil, apperrors.NewBadRequestError("comment body is required")
	}
	if comment.TenantID == 0 {
		return nil, apperrors.NewBadRequestError("tenant context is required")
	}
	if comment.KnowledgeID == "" {
		return nil, apperrors.NewBadRequestError("knowledge_id is required")
	}
	comment.Body = strings.TrimSpace(comment.Body)
	if comment.Body == "" {
		return nil, apperrors.NewBadRequestError("comment body cannot be empty")
	}
	if utf8.RuneCountInString(comment.Body) > maxCommentBodyLen {
		return nil, apperrors.NewBadRequestError("comment body is too long (max 10000 chars)")
	}
	if _, err := comment.AnchorDetails(); err != nil {
		// SetAnchor already validated before marshalling; a malformed payload
		// here means a caller bypassed the typed setter.
		return nil, apperrors.NewBadRequestError("anchor payload is malformed")
	}
	if comment.ID == "" {
		comment.ID = uuid.NewString()
	}
	if comment.ParentID != "" {
		parent, err := s.loadComment(ctx, comment.TenantID, comment.ParentID)
		if err != nil {
			return nil, apperrors.NewBadRequestError("parent comment not found")
		}
		if parent.KnowledgeID != comment.KnowledgeID {
			return nil, apperrors.NewBadRequestError("parent comment belongs to a different knowledge entry")
		}
		if parent.ParentID != "" {
			return nil, apperrors.NewBadRequestError("replies to replies are not allowed (one level of nesting)")
		}
	}
	if err := s.repo.CreateComment(ctx, comment); err != nil {
		return nil, err
	}
	hasAnchor := len(comment.Anchor) > 0
	s.logAudit(ctx, comment.TenantID, comment.AuthorID, auditActionCommentCreated,
		comment.ID, map[string]interface{}{
			"knowledge_id": comment.KnowledgeID,
			"parent_id":    comment.ParentID,
			"has_anchor":   hasAnchor,
		})
	return comment, nil
}

// ListByKnowledge assembles the thread view: top-level comments each with
// their replies, both chronological. Soft-deleted comments are excluded by
// the repository (gorm.DeletedAt); replies whose parent was deleted are
// promoted to top level so surviving content never drops out of the view.
func (s *commentService) ListByKnowledge(
	ctx context.Context, tenantID uint64, knowledgeID string,
) ([]*types.KnowledgeCommentThread, error) {
	comments, err := s.repo.ListByKnowledge(ctx, tenantID, knowledgeID)
	if err != nil {
		return nil, err
	}
	threads := make(map[string]*types.KnowledgeCommentThread, len(comments))
	var roots []*types.KnowledgeCommentThread
	for _, comment := range comments {
		thread := &types.KnowledgeCommentThread{
			KnowledgeComment: *comment,
			Replies:          []*types.KnowledgeCommentThread{},
		}
		threads[comment.ID] = thread
	}
	// Second pass attaches replies; a reply whose parent is missing (soft-
	// deleted while the reply survived) is promoted to top level.
	for _, comment := range comments {
		thread := threads[comment.ID]
		if comment.ParentID == "" {
			roots = append(roots, thread)
			continue
		}
		if parent, ok := threads[comment.ParentID]; ok {
			parent.Replies = append(parent.Replies, thread)
		} else {
			roots = append(roots, thread)
		}
	}
	if roots == nil {
		roots = []*types.KnowledgeCommentThread{}
	}
	return roots, nil
}

// ResolveComment marks one comment resolved. 作者或 Admin：the author closes
// their own question; an Admin moderates anyone's. Resolving is a one-way
// state flip (re-resolve is rejected, same explicit-state-machine policy as
// the lifecycle vertical).
func (s *commentService) ResolveComment(ctx context.Context, tenantID uint64, commentID, actorID string) error {
	comment, err := s.loadComment(ctx, tenantID, commentID)
	if err != nil {
		return err
	}
	if !s.canModerate(ctx, actorID, comment) {
		return apperrors.NewForbiddenError("only the comment author or an admin can resolve a comment")
	}
	if comment.Resolved {
		return apperrors.NewBadRequestError("comment is already resolved")
	}
	if err := s.repo.ResolveComment(ctx, tenantID, commentID, actorID); err != nil {
		return err
	}
	s.logAudit(ctx, tenantID, actorID, auditActionCommentResolved,
		commentID, map[string]interface{}{
			"knowledge_id": comment.KnowledgeID,
			"author_id":    comment.AuthorID,
		})
	return nil
}

// DeleteComment soft-deletes one comment (row kept for audit). 作者或 Admin;
// replies of a deleted comment stay visible (promoted to top level on read).
func (s *commentService) DeleteComment(ctx context.Context, tenantID uint64, commentID, actorID string) error {
	comment, err := s.loadComment(ctx, tenantID, commentID)
	if err != nil {
		return err
	}
	if !s.canModerate(ctx, actorID, comment) {
		return apperrors.NewForbiddenError("only the comment author or an admin can delete a comment")
	}
	if err := s.repo.SoftDeleteComment(ctx, tenantID, commentID); err != nil {
		return err
	}
	s.logAudit(ctx, tenantID, actorID, auditActionCommentDeleted,
		commentID, map[string]interface{}{
			"knowledge_id": comment.KnowledgeID,
			"author_id":    comment.AuthorID,
		})
	return nil
}

// canModerate reports whether the actor may resolve/delete the comment:
// the author themselves, or an Admin of the active tenant (same role-from-
// context pattern as the candidate/KB-share services).
func (s *commentService) canModerate(ctx context.Context, actorID string, comment *types.KnowledgeComment) bool {
	if actorID != "" && actorID == comment.AuthorID {
		return true
	}
	return types.TenantRoleFromContext(ctx).HasPermission(types.TenantRoleAdmin)
}

// loadComment resolves a tenant-scoped live comment, translating
// record-not-found into the domain error the handler layer maps to 404.
func (s *commentService) loadComment(
	ctx context.Context, tenantID uint64, commentID string,
) (*types.KnowledgeComment, error) {
	comment, err := s.repo.GetComment(ctx, tenantID, commentID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperrors.NewNotFoundError("comment not found")
		}
		return nil, err
	}
	return comment, nil
}

// logAudit writes one comment audit event; audit failures never block the
// business write (same policy as the other verticals).
func (s *commentService) logAudit(
	ctx context.Context, tenantID uint64, actorID, action, commentID string,
	details map[string]interface{},
) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: actorID,
		Action:      types.AuditAction(action),
		ScopeType:   "knowledge",
		TargetType:  "knowledge_comment",
		TargetID:    commentID,
		Outcome:     "success",
		Details:     commentDetailsJSON(details),
	})
}
