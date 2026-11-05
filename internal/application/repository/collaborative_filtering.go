package repository

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// collaborativeFilteringRepository implements
// interfaces.CollaborativeFilteringRepository (KnowAxis V3.0 §15.3).
//
// 两条查询、一个窗口：协同过滤的信号必须限时。一个两年前的阅读行为不能代表
// 现在的兴趣，而"不限窗口"会让推荐越来越难解释——用户早已忘了自己读过什么。
type collaborativeFilteringRepository struct {
	db *gorm.DB
}

// NewCollaborativeFilteringRepository constructs the repository.
func NewCollaborativeFilteringRepository(db *gorm.DB) interfaces.CollaborativeFilteringRepository {
	return &collaborativeFilteringRepository{db: db}
}

// InteractionWeights returns the implicit ratings inside one tenant's window.
//
// 两次查询分别取阅读与收藏，再在 Go 侧合并：SQL 里的 UNION + 权重常量会把
// "收藏为什么更重"埋进字符串，而这是需要被讨论的产品决定。
func (r *collaborativeFilteringRepository) Interactions(
	ctx context.Context, tenantID uint64, since time.Time, limit int,
	weights types.CFInteractionWeights,
) ([]types.CFInteraction, error) {
	if limit <= 0 || limit > 20000 {
		limit = 20000
	}
	type row struct {
		UserID          string
		KnowledgeID     string
		KnowledgeBaseID string
	}
	interactions := make([]types.CFInteraction, 0, limit*2)

	var viewRows []row
	if err := r.db.WithContext(ctx).Table("knowledge_view_events").
		Select("user_id, knowledge_id, knowledge_base_id").
		Where("tenant_id = ? AND created_at >= ? AND user_id <> '' AND knowledge_id <> ''",
			tenantID, since.UTC()).
		Order("created_at DESC").
		Limit(limit).
		Scan(&viewRows).Error; err != nil {
		return nil, err
	}
	for _, view := range viewRows {
		interactions = append(interactions, types.CFInteraction{
			UserID: view.UserID, ItemID: view.KnowledgeID,
			KnowledgeBaseID: view.KnowledgeBaseID, Weight: weights.View,
		})
	}

	var favoriteRows []row
	if err := r.db.WithContext(ctx).Table("user_resource_favorites").
		Select("user_id, resource_id AS knowledge_id").
		Where("tenant_id = ? AND resource_type = ? AND created_at >= ? AND user_id <> ''",
			tenantID, types.ResourceTypeKnowledge, since.UTC()).
		Order("created_at DESC").
		Limit(limit).
		Scan(&favoriteRows).Error; err != nil {
		// 收藏表不可用不阻断：阅读信号已经足够建立模型，只是少一档强度。
		return interactions, nil
	}
	// 收藏表只有 resource_id，没有 kb 列。回填一次：没有 kb 的候选会被
	// ACL 层按 fail-closed 拒绝，那会让"只靠收藏交互"的物品永远推不出来。
	if len(favoriteRows) > 0 {
		ids := make([]string, 0, len(favoriteRows))
		seen := make(map[string]struct{}, len(favoriteRows))
		for _, favorite := range favoriteRows {
			if _, duplicate := seen[favorite.KnowledgeID]; duplicate {
				continue
			}
			seen[favorite.KnowledgeID] = struct{}{}
			ids = append(ids, favorite.KnowledgeID)
		}
		kbByKnowledge := make(map[string]string, len(ids))
		var kbRows []row
		if err := r.db.WithContext(ctx).Table("knowledges").
			Select("id AS knowledge_id, knowledge_base_id").
			Where("tenant_id = ? AND id IN ?", tenantID, ids).
			Scan(&kbRows).Error; err == nil {
			for _, kb := range kbRows {
				kbByKnowledge[kb.KnowledgeID] = kb.KnowledgeBaseID
			}
		}
		for index := range favoriteRows {
			favoriteRows[index].KnowledgeBaseID = kbByKnowledge[favoriteRows[index].KnowledgeID]
		}
	}
	for _, favorite := range favoriteRows {
		interactions = append(interactions, types.CFInteraction{
			UserID: favorite.UserID, ItemID: favorite.KnowledgeID,
			KnowledgeBaseID: favorite.KnowledgeBaseID, Weight: weights.Favorite,
		})
	}
	return interactions, nil
}

// UserHistory returns the items one user interacted with in the window.
//
// 只返回知识类收藏：其他 resource_type 不是"物品"，混进历史会让推荐位把
// 知识库或 Agent 当成可推荐的内容。
func (r *collaborativeFilteringRepository) UserHistory(
	ctx context.Context, tenantID uint64, userID string, since time.Time, limit int,
) ([]string, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	seen := make(map[string]struct{}, limit)
	out := make([]string, 0, limit)

	var viewIDs []string
	if err := r.db.WithContext(ctx).Table("knowledge_view_events").
		Where("tenant_id = ? AND user_id = ? AND created_at >= ? AND knowledge_id <> ''",
			tenantID, userID, since.UTC()).
		Order("created_at DESC").
		Limit(limit).
		Pluck("knowledge_id", &viewIDs).Error; err != nil {
		return nil, err
	}
	for _, id := range viewIDs {
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}

	var favoriteIDs []string
	if err := r.db.WithContext(ctx).Table("user_resource_favorites").
		Where("tenant_id = ? AND user_id = ? AND resource_type = ? AND created_at >= ?",
			tenantID, userID, types.ResourceTypeKnowledge, since.UTC()).
		Order("created_at DESC").
		Limit(limit).
		Pluck("resource_id", &favoriteIDs).Error; err != nil {
		return out, nil
	}
	for _, id := range favoriteIDs {
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}
