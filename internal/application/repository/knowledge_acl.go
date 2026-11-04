package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrKnowledgeACLGrantNotFound is returned when a grant lookup misses.
var ErrKnowledgeACLGrantNotFound = errors.New("knowledge acl grant not found")

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

// GetByID returns one grant within the tenant.
func (r *knowledgeACLRepository) GetByID(
	ctx context.Context, tenantID uint64, grantID string,
) (*types.KnowledgeACL, error) {
	var grant types.KnowledgeACL
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, grantID).
		First(&grant).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrKnowledgeACLGrantNotFound
	}
	if err != nil {
		return nil, err
	}
	return &grant, nil
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
	if subjectID == "" {
		return nil, nil
	}
	return r.DeniedKnowledgeIDsForSubjects(ctx, knowledgeBaseIDs,
		[]types.ACLSubject{{Type: subjectType, ID: subjectID}}, action, now)
}

// DeniedKnowledgeIDsForSubjects answers the same query for every subject the
// caller holds at once (V3.0 §2.1: a user inherits grants made to them, to
// their groups, and to their role).
//
// 一次查询覆盖全部主体，而不是每个主体查一遍再合并：合并需要保留"哪个主体
// 命中了 deny"的信息，分步查会让 deny-wins 语义与 allow 判定分开两次执行，
// 中间可能出现只算了 user 没算 group 的窗口。
//
// 参数顺序按子查询出现顺序严格排列：deny 一次、密级两次、allow 一次。
func (r *knowledgeACLRepository) DeniedKnowledgeIDsForSubjects(
	ctx context.Context,
	knowledgeBaseIDs []string,
	subjects []types.ACLSubject,
	action types.PermissionAction,
	now time.Time,
) ([]string, error) {
	if len(knowledgeBaseIDs) == 0 {
		return nil, nil
	}
	cleaned := make([]types.ACLSubject, 0, len(subjects))
	for _, subject := range subjects {
		if subject.Valid() {
			cleaned = append(cleaned, subject)
		}
	}
	if len(cleaned) == 0 {
		return nil, nil
	}

	typePairs := make([]string, 0, len(cleaned))
	ids := make([]string, 0, len(cleaned))
	for _, subject := range cleaned {
		typePairs = append(typePairs, string(subject.Type))
		ids = append(ids, subject.ID)
	}
	// (subject_type, subject_id) 成对匹配：同一批里可能出现 (user,"admin-1")
	// 与 (role,"admin") 这种同值不同类的组合，两个 IN 并排会串味。
	pairCondition := "(a.subject_type, a.subject_id) IN ("
	for i := range cleaned {
		if i > 0 {
			pairCondition += ", "
		}
		pairCondition += "(?, ?)"
	}
	pairCondition += ")"

	liveGrant := pairCondition + " AND a.action = ?" +
		" AND (a.expires_at IS NULL OR a.expires_at > ?)"
	grantArgs := make([]interface{}, 0, len(cleaned)*2+2)
	for _, subject := range cleaned {
		grantArgs = append(grantArgs, string(subject.Type), subject.ID)
	}
	grantArgs = append(grantArgs, string(action), now)

	allArgs := []interface{}{}
	allArgs = append(allArgs, grantArgs...) // deny EXISTS
	allArgs = append(allArgs,
		string(types.KnowledgeClassificationSecret),
		string(types.KnowledgeClassificationConfidential),
	)
	allArgs = append(allArgs, grantArgs...) // allow NOT EXISTS

	var denied []string
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
		Pluck("k.id", &denied).Error
	if err != nil {
		return nil, err
	}
	return denied, nil
}
