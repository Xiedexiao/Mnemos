package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// NetworkAccessService reads and writes the tenant IP allow-list (§16.3) and
// answers the per-request verdict.
type NetworkAccessService interface {
	// Get returns the tenant's policy (an empty, disabled one when unset).
	Get(ctx context.Context, tenantID uint64) (*types.NetworkAccessPolicy, error)
	// Set validates and persists the policy.
	Set(
		ctx context.Context, tenantID uint64, actorID string,
		input types.NetworkAccessPolicyInput,
	) (*types.NetworkAccessPolicy, error)
	// Allows answers "may this client address use this tenant".
	//
	// 查询失败时返回 true 并记日志：这是可用性优先的取舍——IP 白名单是
	// 纵深防御的一层，而认证与 ACL 已经在前面；因为一次读失败把全租户挡在
	// 门外，代价高于它挡住的残余风险。
	Allows(ctx context.Context, tenantID uint64, clientIP string) bool
}

// NetworkAccessRepository persists the tenant IP allow-list.
type NetworkAccessRepository interface {
	// Get returns the stored row, or nil when never configured.
	Get(ctx context.Context, tenantID uint64) (*types.NetworkAccessPolicyRow, error)
	// Upsert writes the row.
	Upsert(ctx context.Context, row *types.NetworkAccessPolicyRow) error
}
