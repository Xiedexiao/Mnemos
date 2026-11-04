package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// mentionResolver implements interfaces.MentionResolver
// (KnowAxis V3.0 §5.1 @提及).
type mentionResolver struct {
	users      interfaces.UserRepository
	members    interfaces.TenantMemberRepository
	knowledges interfaces.KnowledgeRepository
	acl        interfaces.KnowledgeACLRepository
	subjects   aclSubjectResolver
	notifier   *NotificationService
	now        func() time.Time
}

// NewMentionResolver constructs the resolver.
func NewMentionResolver(
	users interfaces.UserRepository,
	members interfaces.TenantMemberRepository,
	knowledges interfaces.KnowledgeRepository,
	acl interfaces.KnowledgeACLRepository,
	subjectGroups interfaces.ACLSubjectGroupService,
	notifier *NotificationService,
) interfaces.MentionResolver {
	return &mentionResolver{
		users:      users,
		members:    members,
		knowledges: knowledges,
		acl:        acl,
		subjects:   newACLSubjectResolver(subjectGroups),
		notifier:   notifier,
		now:        time.Now,
	}
}

// Resolve validates the mentions and drops the ones the author may not use.
//
// 被剔除的三类：
//  1. 不存在（用户/知识已被删除）；
//  2. 不在本工作区（提到别家租户的人等于跨租户泄露）；
//  3. 知识提及但作者自己都无权查看——提一个自己看不见的文档，等于把它的
//     存在告诉别人。
//
// 剔除而不是报错：评论正文已经写好，一个失效的提及不应该让整条评论失败；
// 但被剔除的提及绝不会进入通知，也不会留在落库的列表里。
func (m *mentionResolver) Resolve(
	ctx context.Context, tenantID uint64, actorID string, mentions []types.Mention,
) ([]types.Mention, error) {
	if len(mentions) == 0 {
		return nil, nil
	}
	normalized, err := types.NormalizeMentions(mentions)
	if err != nil {
		return nil, err
	}
	allowed := make([]types.Mention, 0, len(normalized))

	// 用户与专家提及共用用户表；知识提及单独判定可见性。
	userIDs := make([]string, 0, len(normalized))
	knowledgeIDs := make([]string, 0, len(normalized))
	for _, mention := range normalized {
		switch mention.Type {
		case types.MentionTypeUser, types.MentionTypeExpert:
			userIDs = append(userIDs, mention.ID)
		case types.MentionTypeKnowledge:
			knowledgeIDs = append(knowledgeIDs, mention.ID)
		}
	}

	validUsers := m.validTenantUsers(ctx, tenantID, userIDs, actorID)
	validKnowledge := m.validKnowledge(ctx, tenantID, actorID, knowledgeIDs)

	for _, mention := range normalized {
		switch mention.Type {
		case types.MentionTypeUser, types.MentionTypeExpert:
			if _, ok := validUsers[mention.ID]; ok {
				allowed = append(allowed, mention)
			}
		case types.MentionTypeKnowledge:
			if knowledge, ok := validKnowledge[mention.ID]; ok {
				if mention.Label == "" {
					mention.Label = knowledge.Title
				}
				allowed = append(allowed, mention)
			}
		}
	}
	return allowed, nil
}

// validTenantUsers returns the subset of ids that are live members of the tenant.
//
// 只查 user 表不够：一个用户可能在别的租户存在，但不在**这个**租户。
// 提及的可见性以工作区成员身份为准。
func (m *mentionResolver) validTenantUsers(
	ctx context.Context, tenantID uint64, userIDs []string, actorID string,
) map[string]struct{} {
	out := make(map[string]struct{}, len(userIDs))
	if len(userIDs) == 0 || m.members == nil {
		return out
	}
	// 提及自己永远是合法的（"@我自己记一下"）。
	if actorID != "" {
		for _, id := range userIDs {
			if id == actorID {
				out[id] = struct{}{}
			}
		}
	}
	seen := make(map[string]struct{}, len(userIDs))
	for _, id := range userIDs {
		if _, done := seen[id]; done {
			continue
		}
		seen[id] = struct{}{}
		member, err := m.members.Get(ctx, id, tenantID)
		if err != nil {
			logger.Warnf(ctx, "[mention] membership lookup failed for %s: %v", id, err)
			continue
		}
		if member != nil {
			out[id] = struct{}{}
			continue
		}
		// member == nil 表示该用户不在这个租户。
	}
	return out
}

// validKnowledge returns the subset of knowledge ids the actor may view.
func (m *mentionResolver) validKnowledge(
	ctx context.Context, tenantID uint64, actorID string, knowledgeIDs []string,
) map[string]*types.Knowledge {
	out := make(map[string]*types.Knowledge, len(knowledgeIDs))
	if len(knowledgeIDs) == 0 || m.knowledges == nil {
		return out
	}
	cleaned := types.NormalizeKnowledgeIDs(knowledgeIDs)
	if len(cleaned) == 0 {
		return out
	}
	rows, err := m.knowledges.GetKnowledgeBatch(ctx, tenantID, cleaned)
	if err != nil {
		logger.Warnf(ctx, "[mention] knowledge lookup failed: %v", err)
		return out
	}
	candidates := make(map[string]*types.Knowledge, len(rows))
	kbSet := make(map[string]struct{}, len(rows))
	for _, knowledge := range rows {
		if knowledge == nil || knowledge.DeletedAt.Valid {
			continue
		}
		candidates[knowledge.ID] = knowledge
		kbSet[knowledge.KnowledgeBaseID] = struct{}{}
	}
	if len(candidates) == 0 {
		return out
	}
	// ACL：提一个自己看不见的文档等于泄露它的存在。
	if m.acl != nil && actorID != "" {
		kbIDs := make([]string, 0, len(kbSet))
		for kbID := range kbSet {
			kbIDs = append(kbIDs, kbID)
		}
		denied, aclErr := m.acl.DeniedKnowledgeIDsForSubjects(
			ctx, kbIDs, []types.ACLSubject{
				{Type: types.KnowledgeACLSubjectUser, ID: actorID},
			}, types.PermissionActionView, m.now())
		if aclErr != nil {
			// 无法判定可见性时全部剔除：宁可少一个提及，不可泄露存在性。
			logger.Warnf(ctx, "[mention] ACL check failed, dropping knowledge mentions: %v", aclErr)
			return out
		}
		for _, id := range denied {
			delete(candidates, id)
		}
	}
	return candidates
}

// Notify delivers the mentions of one comment.
//
// 通知失败只记日志：一条评论已经落库，通知是附加动作。被提及人拿到的链接
// 指向评论所在的文档，不带正文——通知里嵌正文会把内容推到通知渠道，绕过
// 阅读页的权限判定。
func (m *mentionResolver) Notify(
	ctx context.Context, tenantID uint64, actorID string, knowledgeID, commentID string,
	mentions []types.Mention,
) {
	if m.notifier == nil || len(mentions) == 0 {
		return
	}
	for _, mention := range mentions {
		if mention.Type == types.MentionTypeKnowledge {
			// 知识提及没有"收件人"，它只是正文里的引用。
			continue
		}
		if mention.ID == actorID {
			// 不必通知自己。
			continue
		}
		title := "有人在知识评论中提到了你"
		if mention.Type == types.MentionTypeExpert {
			title = "有人以专家身份提到了你"
		}
		m.notifier.Notify(ctx, &types.Notification{
			TenantID:   tenantID,
			UserID:     mention.ID,
			Kind:       "comment_mention",
			Title:      title,
			Body:       fmt.Sprintf("来自 %s 的评论", defaultActorLabel(actorID)),
			Link:       "/knowledge/" + knowledgeID + "?comment=" + commentID,
			SourceType: "knowledge_comment",
			SourceID:   commentID,
		})
	}
}

// defaultActorLabel renders the actor for the notification body.
func defaultActorLabel(actorID string) string {
	if strings.TrimSpace(actorID) == "" {
		return "一位同事"
	}
	return actorID
}
