package service

import (
	"context"
	"encoding/json"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// networkAccessService implements interfaces.NetworkAccessService
// (KnowAxis V3.0 §16.3 IP 限制).
type networkAccessService struct {
	repo  interfaces.NetworkAccessRepository
	audit interfaces.AuditLogService
}

// NewNetworkAccessService constructs the service.
func NewNetworkAccessService(
	repo interfaces.NetworkAccessRepository, audit interfaces.AuditLogService,
) interfaces.NetworkAccessService {
	return &networkAccessService{repo: repo, audit: audit}
}

// Get returns the tenant's policy.
func (s *networkAccessService) Get(
	ctx context.Context, tenantID uint64,
) (*types.NetworkAccessPolicy, error) {
	row, err := s.repo.Get(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return &types.NetworkAccessPolicy{TenantID: tenantID}, nil
	}
	return row.ToPolicy(), nil
}

// Set validates and persists the policy.
func (s *networkAccessService) Set(
	ctx context.Context, tenantID uint64, actorID string,
	input types.NetworkAccessPolicyInput,
) (*types.NetworkAccessPolicy, error) {
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	policy, err := types.ParseNetworkAccessPolicy(tenantID, input)
	if err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	raw, marshalErr := json.Marshal(policy.AllowEntries)
	if marshalErr != nil {
		return nil, marshalErr
	}
	row := &types.NetworkAccessPolicyRow{
		TenantID:     tenantID,
		AllowEntries: types.JSON(raw),
		Enabled:      policy.Enabled,
		UpdatedBy:    actorID,
	}
	if err := s.repo.Upsert(ctx, row); err != nil {
		return nil, err
	}
	if s.audit != nil {
		// 改白名单会立刻改变"谁能进来"，是安全事件，必须留痕。
		_ = s.audit.Log(ctx, &types.AuditLog{
			TenantID:    tenantID,
			ActorUserID: actorID,
			Action:      types.AuditAction("tenant.network_access_updated"),
			ScopeType:   "tenant",
			TargetType:  "network_access_policy",
			TargetID:    "",
			Outcome:     "success",
			Details: gamificationAuditJSON(map[string]interface{}{
				"enabled":       policy.Enabled,
				"allow_entries": policy.AllowEntries,
			}),
		})
	}
	return policy, nil
}

// Allows answers "may this client address use this tenant".
func (s *networkAccessService) Allows(
	ctx context.Context, tenantID uint64, clientIP string,
) bool {
	if s == nil || s.repo == nil || tenantID == 0 {
		return true
	}
	row, err := s.repo.Get(ctx, tenantID)
	if err != nil {
		// 读取失败放行并记日志：IP 白名单是纵深防御的一层，认证与 ACL 已经在
		// 前面；因一次读失败把整个租户挡在门外，代价高于它挡住的残余风险。
		logger.Warnf(ctx, "[network-access] policy lookup failed for tenant %d: %v", tenantID, err)
		return true
	}
	if row == nil {
		return true
	}
	return row.ToPolicy().Allows(clientIP)
}
