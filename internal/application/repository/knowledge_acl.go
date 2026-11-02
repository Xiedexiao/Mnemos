package repository

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// knowledgeACLRepository implements interfaces.KnowledgeACLRepository.
type knowledgeACLRepository struct {
	db *gorm.DB
}

// NewKnowledgeACLRepository constructs the ACL repository.
func NewKnowledgeACLRepository(db *gorm.DB) interfaces.KnowledgeACLRepository {
	return &knowledgeACLRepository{db: db}
}

// UpsertGrant creates or updates one (knowledge, subject, action) grant. The
// unique key mirrors the migration's UNIQUE constraint.
func (r *knowledgeACLRepository) UpsertGrant(ctx context.Context, grant *types.KnowledgeACL) error {
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "knowledge_id"}, {Name: "subject_type"},
				{Name: "subject_id"}, {Name: "action"},
			},
			DoUpdates: clause.AssignmentColumns([]string{"effect", "granted_by", "expires_at"}),
		}).
		Create(grant).Error
}

// ListByKnowledge returns all ACL rows of one knowledge entry.
func (r *knowledgeACLRepository) ListByKnowledge(ctx context.Context, knowledgeID string) ([]*types.KnowledgeACL, error) {
	var grants []*types.KnowledgeACL
	err := r.db.WithContext(ctx).
		Where("knowledge_id = ?", knowledgeID).
		Order("created_at ASC").
		Find(&grants).Error
	return grants, err
}

// DeleteByKnowledge removes every ACL row of one knowledge entry.
func (r *knowledgeACLRepository) DeleteByKnowledge(ctx context.Context, knowledgeID string) (int64, error) {
	result := r.db.WithContext(ctx).
		Where("knowledge_id = ?", knowledgeID).
		Delete(&types.KnowledgeACL{})
	return result.RowsAffected, result.Error
}

// Revoke deletes one grant row within the tenant.
func (r *knowledgeACLRepository) Revoke(ctx context.Context, tenantID uint64, grantID string) error {
	return r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, grantID).
		Delete(&types.KnowledgeACL{}).Error
}

// DeniedKnowledgeIDs answers the deny-wins authorization query.
//
// Semantics (V3.0 §2.1/§2.4, fail-closed):
//   - any live deny row for (subject, action) denies the entry;
//   - classification secret/confidential requires a live allow row for the
//     action — absence is denial;
//   - public/internal entries are allowed by KB permission upstream and only
//     excluded when a deny row hits.
func (r *knowledgeACLRepository) DeniedKnowledgeIDs(
	ctx context.Context,
	knowledgeBaseIDs []string,
	subjectType types.KnowledgeACLSubjectType,
	subjectID string,
	action types.PermissionAction,
	now time.Time,
) ([]string, error) {
	if len(knowledgeBaseIDs) == 0 || subjectID == "" {
		return nil, nil
	}
	// liveGrant 的参数在 deny 与 allow 两个子查询中各出现一次，按占位符顺序拼接。
	liveGrant := "a.subject_type = ? AND a.subject_id = ? AND a.action = ?" +
		" AND (a.expires_at IS NULL OR a.expires_at > ?)"
	grantArgs := []interface{}{string(subjectType), subjectID, string(action), now}
	allArgs := []interface{}{}
	allArgs = append(allArgs, grantArgs...) // deny EXISTS
	allArgs = append(allArgs,
		string(types.KnowledgeClassificationSecret),
		string(types.KnowledgeClassificationConfidential),
	)
	allArgs = append(allArgs, grantArgs...) // allow NOT EXISTS

	var ids []string
	err := r.db.WithContext(ctx).Table("knowledges AS k").
		Where("k.knowledge_base_id IN ? AND k.deleted_at IS NULL", knowledgeBaseIDs).
		Where(
			// deny 优先：任何一条命中即拒绝。
			"EXISTS (SELECT 1 FROM knowledge_acl a WHERE a.knowledge_id = k.id AND a.effect = 'deny' AND "+liveGrant+")"+
				// 密级门槛：secret/confidential 无 allow 即拒绝。
				" OR (k.classification IN (?, ?) AND NOT EXISTS ("+
				"SELECT 1 FROM knowledge_acl a WHERE a.knowledge_id = k.id AND a.effect = 'allow' AND "+liveGrant+"))",
			allArgs...,
		).
		Pluck("k.id", &ids).Error
	if err != nil {
		return nil, err
	}
	return ids, nil
}
