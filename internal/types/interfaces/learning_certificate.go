package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// LearningCertificateService issues and manages learning credentials
// (KnowAxis V3.0 §15.5).
type LearningCertificateService interface {
	// Certify evaluates a learner's progress and issues a certificate when
	// every step is complete. validMonths bounds the validity window (0 = no
	// expiry).
	Certify(
		ctx context.Context, tenantID uint64, pathID, userID string, validMonths int,
	) (*types.LearningCertification, error)
	// List returns a user's certificates with read-time status resolution.
	List(ctx context.Context, tenantID uint64, userID string) ([]*types.LearningCertificate, error)
	// Revoke withdraws a certificate.
	Revoke(
		ctx context.Context, tenantID uint64, certificateID, reason, actorID string,
	) (*types.LearningCertificate, error)
}
