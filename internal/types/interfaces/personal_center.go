package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// PersonalCenterService composes the "以我为中心" view of §15.3: my reviews,
// my favorites and my knowledge in one payload.
type PersonalCenterService interface {
	// Get composes the personal center for one user.
	Get(ctx context.Context, tenantID uint64, userID string) (*types.PersonalCenter, error)
}
