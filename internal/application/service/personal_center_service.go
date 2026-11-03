package service

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// personalCenterService implements interfaces.PersonalCenterService
// (KnowAxis V3.0 §15.3).
//
// 这一层刻意只做聚合，不引入新的业务规则：每块数据都来自已有的垂直领域
// （收藏、审批任务、知识行），个人中心只是把它们按"当前用户"收拢。这样
// 权限判定仍然发生在各自的源头，不会在这里出现第二套口径。
type personalCenterService struct {
	db        *gorm.DB
	favorites interfaces.UserResourceFavoriteService
	now       func() time.Time
}

// personalCenterMaxItems bounds each list so the page stays a summary.
const personalCenterMaxItems = 50

// NewPersonalCenterService constructs the personal center service.
func NewPersonalCenterService(
	db *gorm.DB,
	favorites interfaces.UserResourceFavoriteService,
) interfaces.PersonalCenterService {
	return &personalCenterService{db: db, favorites: favorites, now: time.Now}
}

// Get composes the personal center for one user.
func (s *personalCenterService) Get(
	ctx context.Context, tenantID uint64, userID string,
) (*types.PersonalCenter, error) {
	if tenantID == 0 || userID == "" {
		return nil, nil
	}
	center := &types.PersonalCenter{
		Overview:  types.PersonalCenterOverview{UserID: userID},
		Knowledge: []types.PersonalKnowledgeItem{},
		Reviews:   []types.PersonalReviewItem{},
		Favorites: []types.PersonalFavoriteItem{},
	}

	// 我提交的审批：requested_by = 我。
	reviews, err := s.listMyReviews(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}
	center.Reviews = reviews
	for _, review := range reviews {
		if review.Status == string(types.ApprovalTaskStatusPending) {
			center.Overview.PendingReviewCount++
		}
	}

	// 我的收藏：只展示仍然可见的资源（§15.3 权限失效后自动隐藏）。
	favorites, err := s.listMyFavorites(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}
	center.Favorites = favorites
	for _, favorite := range favorites {
		if favorite.Visible {
			center.Overview.FavoriteCount++
		}
	}

	// 我的知识：当前用户作为 Owner/审核人可见、且仍在流转中的条目。
	knowledge, err := s.listMyKnowledge(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}
	center.Knowledge = knowledge

	return center, nil
}

// listMyReviews returns the approval tasks the user submitted.
func (s *personalCenterService) listMyReviews(
	ctx context.Context, tenantID uint64, userID string,
) ([]types.PersonalReviewItem, error) {
	if s.db == nil {
		return nil, nil
	}
	var rows []types.ApprovalTask
	err := s.db.WithContext(ctx).
		Where("tenant_id = ? AND requested_by = ?", tenantID, userID).
		Order("created_at DESC").
		Limit(personalCenterMaxItems).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]types.PersonalReviewItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, types.PersonalReviewItem{
			TaskID:      row.ID,
			KnowledgeID: row.KnowledgeID,
			Title:       row.Title,
			Status:      row.Status,
			RiskLevel:   row.RiskLevel,
			SLADue:      formatOptionalTime(row.SLADue),
			CreatedAt:   row.CreatedAt.Format(time.RFC3339),
		})
	}
	return out, nil
}

// listMyFavorites returns the user's starred resources with a visibility flag.
//
// 可见性判定：知识类收藏按文档是否仍然存在判断。收藏表本身不存权限快照
// （§15.3 收藏不改变权限），所以这里只做"对象还在不在"的最小判断，真正的
// 权限过滤发生在读取该文档时。
func (s *personalCenterService) listMyFavorites(
	ctx context.Context, tenantID uint64, userID string,
) ([]types.PersonalFavoriteItem, error) {
	if s.db == nil {
		return nil, nil
	}
	var rows []types.UserResourceFavorite
	err := s.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ?", tenantID, userID).
		Order("created_at DESC").
		Limit(personalCenterMaxItems).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]types.PersonalFavoriteItem, 0, len(rows))
	for _, row := range rows {
		item := types.PersonalFavoriteItem{
			ResourceType: row.ResourceType,
			ResourceID:   row.ResourceID,
			Visible:      true,
			CreatedAt:    row.CreatedAt.Format(time.RFC3339),
		}
		if row.ResourceType == types.ResourceTypeKnowledge {
			var knowledge types.Knowledge
			lookupErr := s.db.WithContext(ctx).
				Select("id", "title", "deleted_at").
				Where("tenant_id = ? AND id = ?", tenantID, row.ResourceID).
				First(&knowledge).Error
			if lookupErr != nil || knowledge.DeletedAt.Valid {
				// 文档已删除：收藏自动隐藏而不是显示一个死链接。
				item.Visible = false
				out = append(out, item)
				continue
			}
			item.Label = knowledge.Title
		}
		out = append(out, item)
	}
	return out, nil
}

// listMyKnowledge returns entries the user has acted on as an approver.
//
// 平台的知识行没有 Owner/creator 列，"我的知识"因此只能按真实存在的关系
// 聚合：我审批过的条目。虚构一个"我的"归属会让读者看到一个并不属于他的
// 清单，所以这里如实只报可查的那部分。
func (s *personalCenterService) listMyKnowledge(
	ctx context.Context, tenantID uint64, userID string,
) ([]types.PersonalKnowledgeItem, error) {
	if s.db == nil {
		return nil, nil
	}
	var rows []types.Knowledge
	err := s.db.WithContext(ctx).
		Where("tenant_id = ? AND deleted_at IS NULL AND id IN ("+
			"SELECT knowledge_id FROM approval_tasks WHERE tenant_id = ? AND decided_by = ?)",
			tenantID, tenantID, userID).
		Order("updated_at DESC").
		Limit(personalCenterMaxItems).
		Find(&rows).Error
	if err != nil {
		return nil, nil
	}
	out := make([]types.PersonalKnowledgeItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, types.PersonalKnowledgeItem{
			ID:              row.ID,
			Title:           row.Title,
			KnowledgeBaseID: row.KnowledgeBaseID,
			LifecycleStatus: row.LifecycleStatus,
			ParseStatus:     row.ParseStatus,
			UpdatedAt:       row.UpdatedAt.Format(time.RFC3339),
		})
	}
	return out, nil
}

// formatOptionalTime renders a nullable time for JSON output.
func formatOptionalTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.Format(time.RFC3339)
}
