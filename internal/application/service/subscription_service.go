package service

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// subscriptionService implements interfaces.SubscriptionService
// (KnowAxis V3.0 §15.3 我的关注).
type subscriptionService struct {
	repo     interfaces.SubscriptionRepository
	acl      interfaces.KnowledgeACLRepository
	subjects aclSubjectResolver
	now      func() time.Time
}

// NewSubscriptionService constructs the service.
func NewSubscriptionService(
	repo interfaces.SubscriptionRepository,
	acl interfaces.KnowledgeACLRepository,
	subjectGroups interfaces.ACLSubjectGroupService,
) interfaces.SubscriptionService {
	return &subscriptionService{
		repo:     repo,
		acl:      acl,
		subjects: newACLSubjectResolver(subjectGroups),
		now:      time.Now,
	}
}

// defaultDigestWindow is how far back a first digest looks.
const defaultDigestWindow = 7 * 24 * time.Hour

// Subscribe adds one follow for the caller.
func (s *subscriptionService) Subscribe(
	ctx context.Context, tenantID uint64, userID string, input types.SubscriptionInput,
) (*types.Subscription, error) {
	if userID == "" {
		return nil, apperrors.NewUnauthorizedError("caller identity is required")
	}
	subjectType := types.SubscriptionSubjectType(strings.TrimSpace(input.SubjectType))
	subjectID, spaceID, err := types.NormalizeSubscriptionSubject(
		subjectType, input.SubjectID, input.SpaceID)
	if err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	subscription := &types.Subscription{
		TenantID:    tenantID,
		UserID:      userID,
		SubjectType: string(subjectType),
		SubjectID:   subjectID,
		SpaceID:     spaceID,
		Label:       trimToRunes(input.Label, 191),
		Enabled:     true,
	}
	if _, err := s.repo.Upsert(ctx, subscription); err != nil {
		return nil, err
	}
	// 从仓库重新读一次，拿到 Upsert 之后的真实状态（重复订阅会复用原行）。
	stored, getErr := s.repo.Get(ctx, tenantID, userID, subscription.ID)
	if getErr != nil || stored == nil {
		return subscription, nil
	}
	return stored, nil
}

// List returns the caller's follows.
func (s *subscriptionService) List(
	ctx context.Context, tenantID uint64, userID string, enabledOnly bool,
) ([]*types.Subscription, error) {
	if userID == "" {
		return nil, apperrors.NewUnauthorizedError("caller identity is required")
	}
	return s.repo.List(ctx, tenantID, userID, enabledOnly)
}

// SetEnabled pauses or resumes one follow.
func (s *subscriptionService) SetEnabled(
	ctx context.Context, tenantID uint64, userID, subscriptionID string, enabled bool,
) error {
	if userID == "" {
		return apperrors.NewUnauthorizedError("caller identity is required")
	}
	if err := s.repo.SetEnabled(ctx, tenantID, userID, subscriptionID, enabled); err != nil {
		return mapSubscriptionError(err)
	}
	return nil
}

// Unsubscribe removes one follow.
func (s *subscriptionService) Unsubscribe(
	ctx context.Context, tenantID uint64, userID, subscriptionID string,
) error {
	if userID == "" {
		return apperrors.NewUnauthorizedError("caller identity is required")
	}
	if err := s.repo.Delete(ctx, tenantID, userID, subscriptionID); err != nil {
		return mapSubscriptionError(err)
	}
	return nil
}

// Digest builds the caller's follow feed.
//
// 三条约束：
//  1. 只看自己的订阅（tenant + user），别人的订阅不会进摘要；
//  2. 每条变更都要过 ACL——订阅不授予访问权，无权条目既不出现也不泄露标题；
//  3. 隐藏条目计数返回给调用者本人，用于解释"为什么只有两条"，
//     但不指名任何内容（§2.10）。
func (s *subscriptionService) Digest(
	ctx context.Context, tenantID uint64, userID string, since time.Time, limit int,
) (*types.Digest, error) {
	if userID == "" {
		return nil, apperrors.NewUnauthorizedError("caller identity is required")
	}
	if limit <= 0 || limit > types.DigestMaxItems {
		limit = types.DigestMaxItems
	}
	now := s.now()
	if since.IsZero() {
		since = now.Add(-defaultDigestWindow)
	}
	if since.After(now) {
		since = now.Add(-defaultDigestWindow)
	}

	subs, err := s.repo.List(ctx, tenantID, userID, true)
	if err != nil {
		return nil, err
	}
	digest := &types.Digest{GeneratedAt: now, Since: since, Items: []types.DigestItem{}}
	if len(subs) == 0 {
		return digest, nil
	}

	var candidates []types.DigestItem
	for _, subscription := range subs {
		rows, subErr := s.changesFor(ctx, tenantID, subscription, since, limit)
		if subErr != nil {
			// 单个订阅查询失败不阻断整份摘要：其余订阅仍然可用。
			logger.Warnf(ctx, "[subscription] digest source %s failed: %v", subscription.ID, subErr)
			continue
		}
		for index := range rows {
			rows[index].SubscriptionID = subscription.ID
			rows[index].SubscriptionLabel = subscriptionLabel(subscription)
			rows[index].SubjectType = subscription.SubjectType
			rows[index].SubjectID = subscription.SubjectID
			if rows[index].ChangeKind == "" {
				rows[index].ChangeKind = "updated"
			}
		}
		candidates = append(candidates, rows...)
	}

	filtered, hidden := s.filterVisible(ctx, tenantID, userID, candidates)
	digest.HiddenCount = hidden

	// 同一篇知识被多个订阅命中时只保留一条（取最新变更时间）。
	deduped := make([]types.DigestItem, 0, len(filtered))
	seen := make(map[string]int, len(filtered))
	for _, item := range filtered {
		if index, ok := seen[item.KnowledgeID]; ok {
			if item.ChangedAt.After(deduped[index].ChangedAt) {
				deduped[index] = item
			}
			continue
		}
		seen[item.KnowledgeID] = len(deduped)
		deduped = append(deduped, item)
	}
	sort.SliceStable(deduped, func(i, j int) bool {
		return deduped[i].ChangedAt.After(deduped[j].ChangedAt)
	})
	if len(deduped) > limit {
		deduped = deduped[:limit]
		digest.Truncated = true
	}
	digest.Items = deduped
	return digest, nil
}

// changesFor expands one subscription into change rows.
func (s *subscriptionService) changesFor(
	ctx context.Context, tenantID uint64, subscription *types.Subscription,
	since time.Time, limit int,
) ([]types.DigestItem, error) {
	switch types.SubscriptionSubjectType(subscription.SubjectType) {
	case types.SubscriptionSubjectSpace:
		return s.repo.ChangesSince(ctx, tenantID, subscription.SpaceID, since, limit)
	case types.SubscriptionSubjectFolder:
		rows, err := s.repo.ChangesSince(ctx, tenantID, subscription.SpaceID, since, limit)
		if err != nil {
			return nil, err
		}
		// SubjectID 的格式是 spaceID + "\x00" + path。
		parts := strings.SplitN(subscription.SubjectID, "\x00", 2)
		if len(parts) != 2 || parts[1] == "" {
			return rows, nil
		}
		return s.filterFolderChanges(ctx, tenantID, parts[1], rows)
	case types.SubscriptionSubjectTag:
		return s.repo.TagChangesSince(ctx, tenantID, subscription.SubjectID, since, limit)
	case types.SubscriptionSubjectAuthor:
		return s.repo.AuthorChangesSince(ctx, tenantID, subscription.SubjectID, since, limit)
	case types.SubscriptionSubjectKnowledge:
		rows, err := s.repo.KnowledgeChangesSince(ctx, tenantID, subscription.SubjectID, since)
		if err != nil {
			return nil, err
		}
		for index := range rows {
			rows[index].ChangeKind = "updated"
		}
		return rows, nil
	default:
		return nil, nil
	}
}

// filterFolderChanges keeps the rows stored under one folder path.
//
// 目录订阅的语义是"这个目录及其子目录"，与目录 ACL 的继承口径一致。摘要
// 投影里没有 folder_path，所以按 id 回查一次——这段只在目录订阅存在时执行。
func (s *subscriptionService) filterFolderChanges(
	ctx context.Context, tenantID uint64, folderPath string, rows []types.DigestItem,
) ([]types.DigestItem, error) {
	if len(rows) == 0 || folderPath == "" {
		return rows, nil
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.KnowledgeID)
	}
	folders, err := s.repo.FolderPaths(ctx, tenantID, ids)
	if err != nil {
		return nil, err
	}
	out := make([]types.DigestItem, 0, len(rows))
	for _, row := range rows {
		if types.FolderPathWithin(folders[row.KnowledgeID], folderPath) {
			out = append(out, row)
		}
	}
	return out, nil
}

// filterVisible drops denied rows and counts them.
//
// 订阅不授予访问权：摘要里的每一条都要过文档 ACL 与密级。无权条目既不出
// 现在列表里，也不泄露标题——只让调用者知道"有几条被隐藏"（§2.10）。
// ACL 或主体展开失败时整份摘要清空（fail-closed），因为漏掉一条 deny
// 就是把无权内容推到人眼前。
func (s *subscriptionService) filterVisible(
	ctx context.Context, tenantID uint64, userID string, items []types.DigestItem,
) ([]types.DigestItem, int) {
	if len(items) == 0 {
		return items, 0
	}
	if s.acl == nil {
		return nil, len(items)
	}
	caller := types.CallerFromContext(ctx)
	if caller.UserID == "" {
		caller.UserID = userID
		caller.TenantID = tenantID
	}
	subjects, err := s.subjects.ForCaller(ctx, caller)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"fail_closed": true, "surface": "subscription_digest",
		})
		return nil, len(items)
	}
	kbSet := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item.KnowledgeBaseID != "" {
			kbSet[item.KnowledgeBaseID] = struct{}{}
		}
	}
	kbIDs := make([]string, 0, len(kbSet))
	for kbID := range kbSet {
		kbIDs = append(kbIDs, kbID)
	}
	denied, err := s.acl.DeniedKnowledgeIDsForSubjects(
		ctx, kbIDs, subjects, types.PermissionActionView, s.now(),
	)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"fail_closed": true, "surface": "subscription_acl",
		})
		return nil, len(items)
	}
	deniedSet := make(map[string]struct{}, len(denied))
	for _, id := range denied {
		deniedSet[id] = struct{}{}
	}
	visible := make([]types.DigestItem, 0, len(items))
	hidden := 0
	for _, item := range items {
		if _, isDenied := deniedSet[item.KnowledgeID]; isDenied {
			hidden++
			continue
		}
		visible = append(visible, item)
	}
	return visible, hidden
}

// mapSubscriptionError maps repository misses onto the API 404.
func mapSubscriptionError(err error) error {
	if errors.Is(err, repository.ErrSubscriptionNotFound) {
		return apperrors.NewNotFoundError("subscription not found")
	}
	return err
}

// subscriptionLabel renders a subscription for the digest.
func subscriptionLabel(subscription *types.Subscription) string {
	if subscription.Label != "" {
		return subscription.Label
	}
	if subscription.SubjectType == string(types.SubscriptionSubjectSpace) {
		return subscription.SpaceID
	}
	return subscription.SubjectID
}
