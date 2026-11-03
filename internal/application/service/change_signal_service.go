package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// 变更信号的业务哨兵错误。
var (
	// ErrChangeSignalNotFound 信号不存在（或不在本租户）。
	ErrChangeSignalNotFound = errors.New("change signal: not found")
	// ErrChangeSignalHandled 信号已被处理（转换或驳回）。
	ErrChangeSignalHandled = errors.New("change signal: already handled")
)

// changeSignalService implements interfaces.ChangeSignalService
// (KnowAxis V3.0 §7.3).
type changeSignalService struct {
	repo       interfaces.ChangeSignalRepository
	changeReqs interfaces.ChangeRequestService
}

// NewChangeSignalService constructs the service.
func NewChangeSignalService(
	repo interfaces.ChangeSignalRepository,
	changeReqs interfaces.ChangeRequestService,
) interfaces.ChangeSignalService {
	return &changeSignalService{repo: repo, changeReqs: changeReqs}
}

// Collect records one signal.
//
// 去重由仓储的 partial unique index 完成：同一来源的同类信号在待处理期间
// 只保留一条。返回的 reported=false 表示这次没有新增（已被去重），调用方
// 不需要区分"成功"与"重复"——两者都不是错误。
func (s *changeSignalService) Collect(
	ctx context.Context, signal *types.ChangeSignal,
) (*types.ChangeSignal, error) {
	if signal == nil {
		return nil, apperrors.NewBadRequestError("signal is required")
	}
	if signal.TenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	if err := types.NormalizeChangeSignal(signal); err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	created, err := s.repo.Create(ctx, signal)
	if err != nil {
		return nil, err
	}
	if !created {
		// 去重：返回既有行更符合预期（调用方拿到的是"这个信号现在长这样"）。
		return signal, nil
	}
	return signal, nil
}

// List returns the triage queue.
func (s *changeSignalService) List(
	ctx context.Context, tenantID uint64, status types.ChangeSignalStatus,
	signalType types.ChangeSignalType, limit int,
) ([]*types.ChangeSignal, error) {
	if status != "" && !status.Valid() {
		return nil, apperrors.NewBadRequestError("unknown signal status")
	}
	if signalType != "" && !signalType.Valid() {
		return nil, apperrors.NewBadRequestError("unknown signal type")
	}
	return s.repo.List(ctx, tenantID, status, signalType, limit)
}

// Convert turns a signal into a change-request draft.
//
// 草稿的风险级与变更类型由信号类型决定，而不是让人从零填：外部法规更新
// 默认高风险、源系统作废默认下架——把一个高风险信号误标成低风险正是
// §6.4 要杜绝的事。人之后仍可调整类型，但风险下限由信号保证。
func (s *changeSignalService) Convert(
	ctx context.Context, tenantID uint64, signalID, actorID string,
) (*types.ChangeSignal, error) {
	signal, err := s.repo.Get(ctx, tenantID, signalID)
	if err != nil {
		if errors.Is(err, repository.ErrChangeSignalNotFound) {
			return nil, ErrChangeSignalNotFound
		}
		return nil, err
	}
	if types.ChangeSignalStatus(signal.Status) != types.ChangeSignalPending {
		return nil, ErrChangeSignalHandled
	}
	signalType := types.ChangeSignalType(signal.SignalType)

	title := signal.Summary
	if title == "" {
		title = fmt.Sprintf("%s 信号：%s", signalType, signal.SourceID)
	}
	request, createErr := s.changeReqs.CreateCR(ctx, types.ChangeRequestCreateInput{
		Title:      title,
		Reason:     signalReasonFor(signalType),
		ChangeType: signalType.DefaultChangeType(),
		RiskLevel:  signalType.DefaultRiskLevel(),
		Note:       fmt.Sprintf("由变更信号自动生成（%s / 置信度 %s）", signalType, signal.Confidence),
		CreatedBy:  actorID,
	})
	if createErr != nil {
		return nil, createErr
	}
	return s.repo.MarkConverted(ctx, tenantID, signalID, request.ID, actorID)
}

// Dismiss marks a signal as a false positive.
func (s *changeSignalService) Dismiss(
	ctx context.Context, tenantID uint64, signalID, actorID string,
) (*types.ChangeSignal, error) {
	if signalID == "" {
		return nil, apperrors.NewBadRequestError("signal_id is required")
	}
	return s.repo.Dismiss(ctx, tenantID, signalID, actorID)
}

// signalReasonFor maps a signal type onto the change-request reason enum so the
// generated draft is already classified correctly.
func signalReasonFor(signalType types.ChangeSignalType) types.ChangeRequestReason {
	switch signalType {
	case types.ChangeSignalExternalSubscription:
		return types.ChangeReasonRegulation
	case types.ChangeSignalSourceSystem:
		return types.ChangeReasonProcess
	case types.ChangeSignalUserFeedback:
		return types.ChangeReasonCorrection
	case types.ChangeSignalConflict:
		return types.ChangeReasonCorrection
	default:
		return types.ChangeReasonProcess
	}
}
