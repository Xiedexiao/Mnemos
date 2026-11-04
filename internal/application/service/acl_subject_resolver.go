package service

import (
	"context"
	"fmt"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// aclSubjectResolver expands one caller into every ACL subject they hold:
// the user themselves, their tenant role, and each group they belong to
// (KnowAxis V3.0 §2.1 group/role subjects).
//
// 单独成类型而不是各服务各写一份：主体展开的口径必须唯一。若检索用一套、
// 阅读页用另一套，"能搜到但打不开"或"能打开但搜不到"迟早出现。
type aclSubjectResolver struct {
	groups interfaces.ACLSubjectGroupService
}

// newACLSubjectResolver constructs the resolver. groups may be nil, in which
// case only the user and role subjects are produced (the pre-group behaviour:
// narrower, never wider for denies, and group allows simply do not apply).
func newACLSubjectResolver(groups interfaces.ACLSubjectGroupService) aclSubjectResolver {
	return aclSubjectResolver{groups: groups}
}

// ForCaller returns the subjects. A group lookup failure is returned to the
// caller rather than swallowed: a missed group deny is an over-permission, so
// every authorization path treats this as fail-closed.
func (r aclSubjectResolver) ForCaller(
	ctx context.Context, caller types.Caller,
) ([]types.ACLSubject, error) {
	if caller.UserID == "" {
		return nil, fmt.Errorf("acl: caller identity is required")
	}
	subjects := []types.ACLSubject{
		{Type: types.KnowledgeACLSubjectUser, ID: caller.UserID},
	}
	if caller.Role.IsValid() {
		subjects = append(subjects, types.ACLSubject{
			Type: types.KnowledgeACLSubjectRole, ID: string(caller.Role),
		})
	}
	if r.groups == nil {
		return subjects, nil
	}
	expanded, err := r.groups.SubjectsForUser(ctx, caller.TenantID, caller.UserID, caller.Role)
	if err != nil {
		return nil, err
	}
	return expanded, nil
}
