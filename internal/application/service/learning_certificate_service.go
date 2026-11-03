package service

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// 学习认证的业务哨兵错误。
var (
	// ErrCertificationNotEligible 学习路径未完成。
	ErrCertificationNotEligible = errors.New("learning certificate: every step must be completed first")
	// ErrCertificateNotFound 证书不存在。
	ErrCertificateNotFound = errors.New("learning certificate: not found")
)

// learningCertificateService implements interfaces.LearningCertificateService
// (KnowAxis V3.0 §15.5).
type learningCertificateService struct {
	db  *gorm.DB
	now func() time.Time
}

// NewLearningCertificateService constructs the service.
func NewLearningCertificateService(db *gorm.DB) interfaces.LearningCertificateService {
	return &learningCertificateService{db: db, now: time.Now}
}

// Certify evaluates a learner's progress and issues a certificate when complete.
//
// 认证是"完成全部步骤"的函数，不是一个人工开关：只要进度到 100% 就该发，
// 没到 100% 就明确拒绝并说明差几步。已有的有效证书直接复用，避免重复学习
// 产生一堆重复证书。
func (s *learningCertificateService) Certify(
	ctx context.Context, tenantID uint64, pathID, userID string, validMonths int,
) (*types.LearningCertification, error) {
	if s.db == nil {
		return nil, apperrors.NewInternalServerError("certification store is not configured")
	}
	if pathID == "" || userID == "" {
		return nil, apperrors.NewBadRequestError("path_id and user_id are required")
	}

	var path types.LearningPath
	if err := s.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, pathID).
		First(&path).Error; err != nil {
		return nil, apperrors.NewNotFoundError("learning path not found")
	}
	steps, err := path.DecodeSteps()
	if err != nil {
		return nil, apperrors.NewInternalServerError("learning path steps are unreadable")
	}

	var progress types.LearningProgress
	progressErr := s.db.WithContext(ctx).
		Where("tenant_id = ? AND path_id = ? AND user_id = ?", tenantID, pathID, userID).
		First(&progress).Error
	completed := make([]int, 0)
	if progressErr == nil {
		completed, _ = progress.DecodeCompletedSteps()
	} else if !errors.Is(progressErr, gorm.ErrRecordNotFound) {
		return nil, progressErr
	}

	result := &types.LearningCertification{
		CompletedSteps: len(completed),
		TotalSteps:     len(steps),
	}
	// 空路径不发证书：一个没有步骤的"认证"没有任何含义。
	if len(steps) == 0 {
		result.Reason = "the path has no steps"
		return result, nil
	}
	if len(completed) < len(steps) {
		result.Reason = "every step must be completed first"
		return result, nil
	}
	result.Eligible = true

	// 已有有效证书：直接复用，不重复发。
	var existing types.LearningCertificate
	if findErr := s.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ? AND path_id = ? AND status = ?",
			tenantID, userID, pathID, string(types.LearningCertificateActive)).
		Order("issued_at DESC").
		First(&existing).Error; findErr == nil {
		if existing.Active(s.now()) {
			result.Certificate = &existing
			return result, nil
		}
	}

	now := s.now()
	certificate := &types.LearningCertificate{
		TenantID: tenantID,
		UserID:   userID,
		PathID:   pathID,
		Stage:    string(types.ParseLearningStage(path.Stage)),
		Status:   string(types.LearningCertificateActive),
		IssuedAt: now,
	}
	// 有效期：上岗资格需要周期性复核，因此默认给一个有限的窗口而不是永久。
	if validMonths > 0 {
		expiry := now.AddDate(0, validMonths, 0)
		certificate.ExpiresAt = &expiry
	}
	if err := s.db.WithContext(ctx).Create(certificate).Error; err != nil {
		return nil, err
	}
	result.Certificate = certificate
	return result, nil
}

// List returns a user's certificates with read-time status resolution.
func (s *learningCertificateService) List(
	ctx context.Context, tenantID uint64, userID string,
) ([]*types.LearningCertificate, error) {
	if s.db == nil {
		return nil, nil
	}
	var rows []*types.LearningCertificate
	err := s.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ?", tenantID, userID).
		Order("issued_at DESC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	// 读时解析过期：不改库（§7.1），但返回的状态必须是此刻的真相。
	now := s.now()
	for _, row := range rows {
		if row != nil {
			row.Status = string(row.EffectiveStatus(now))
		}
	}
	return rows, nil
}

// Revoke withdraws a certificate.
func (s *learningCertificateService) Revoke(
	ctx context.Context, tenantID uint64, certificateID, reason, actorID string,
) (*types.LearningCertificate, error) {
	if s.db == nil {
		return nil, apperrors.NewInternalServerError("certification store is not configured")
	}
	if certificateID == "" {
		return nil, apperrors.NewBadRequestError("certificate_id is required")
	}
	now := s.now()
	updates := map[string]interface{}{
		"status":         string(types.LearningCertificateRevoked),
		"revoked_at":     now,
		"revoked_reason": types.NormalizeRelationNote(reason),
	}
	if err := s.db.WithContext(ctx).
		Model(&types.LearningCertificate{}).
		Where("tenant_id = ? AND id = ?", tenantID, certificateID).
		Updates(updates).Error; err != nil {
		return nil, err
	}
	var row types.LearningCertificate
	if err := s.db.WithContext(ctx).
		Where("tenant_id = ? AND id = ?", tenantID, certificateID).
		First(&row).Error; err != nil {
		return nil, ErrCertificateNotFound
	}
	return &row, nil
}
