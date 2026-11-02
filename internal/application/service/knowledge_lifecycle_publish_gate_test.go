package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubReceiptRepo 只实现回执读取；嵌接口让未用到的 Append/List 直接 panic，
// 顺带保证这条发布路径不会偷偷写链。
type stubReceiptRepo struct {
	interfaces.ApprovalReceiptRepository
	byID map[string]*types.ApprovalReceipt
}

func (s *stubReceiptRepo) Get(_ context.Context, tenantID uint64, receiptID string) (*types.ApprovalReceipt, error) {
	receipt, ok := s.byID[receiptID]
	if !ok || receipt.TenantID != tenantID {
		return nil, ErrReviewReceiptNotFound
	}
	return receipt, nil
}

func newPublishGateService(receipts *stubReceiptRepo) *knowledgeLifecycleService {
	return &knowledgeLifecycleService{receipts: receipts, now: time.Now}
}

func TestRequireReceiptForPublish_LowClassificationIsSelfService(t *testing.T) {
	svc := newPublishGateService(&stubReceiptRepo{})

	for _, classification := range []string{"", string(types.KnowledgeClassificationPublic), string(types.KnowledgeClassificationInternal)} {
		t.Run("classification="+classification, func(t *testing.T) {
			err := svc.requireReceiptForPublish(context.Background(),
				&types.Knowledge{ID: "k1", TenantID: 7, Classification: classification}, "")
			require.NoError(t, err, "public/internal publishing must not require a receipt")
		})
	}
}

func TestRequireReceiptForPublish_HighClassificationNeedsMatchingReceipt(t *testing.T) {
	approved := &types.ApprovalReceipt{
		ReceiptID: "rcpt_ok", TenantID: 7, KnowledgeID: "k1",
		Decision: string(types.ApprovalDecisionApproved),
	}
	rejected := &types.ApprovalReceipt{
		ReceiptID: "rcpt_no", TenantID: 7, KnowledgeID: "k1",
		Decision: string(types.ApprovalDecisionRejected),
	}
	otherKnowledge := &types.ApprovalReceipt{
		ReceiptID: "rcpt_other", TenantID: 7, KnowledgeID: "k2",
		Decision: string(types.ApprovalDecisionApproved),
	}
	svc := newPublishGateService(&stubReceiptRepo{byID: map[string]*types.ApprovalReceipt{
		"rcpt_ok": approved, "rcpt_no": rejected, "rcpt_other": otherKnowledge,
	}})

	cases := []struct {
		name       string
		receiptID  string
		classified types.KnowledgeClassification
		wantCode   int
	}{
		{name: "no receipt", receiptID: "", classified: types.KnowledgeClassificationSecret, wantCode: 403},
		{name: "unknown receipt", receiptID: "rcpt_missing", classified: types.KnowledgeClassificationSecret, wantCode: 403},
		{name: "rejected receipt", receiptID: "rcpt_no", classified: types.KnowledgeClassificationSecret, wantCode: 403},
		{name: "receipt for another knowledge", receiptID: "rcpt_other", classified: types.KnowledgeClassificationSecret, wantCode: 403},
		{name: "approved receipt secret", receiptID: "rcpt_ok", classified: types.KnowledgeClassificationSecret, wantCode: 0},
		{name: "approved receipt confidential", receiptID: "rcpt_ok", classified: types.KnowledgeClassificationConfidential, wantCode: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.requireReceiptForPublish(context.Background(), &types.Knowledge{
				ID: "k1", TenantID: 7, Classification: string(tc.classified),
			}, tc.receiptID)
			if tc.wantCode == 0 {
				require.NoError(t, err)
				return
			}
			var appErr *apperrors.AppError
			require.ErrorAs(t, err, &appErr)
			assert.Equal(t, tc.wantCode, appErr.HTTPCode)
		})
	}
}

func TestRequireReceiptForPublish_FailsClosedWithoutReceiptRepository(t *testing.T) {
	// Assign a nil interface (not a nil *stubReceiptRepo, which would wrap a
	// nil pointer into a non-nil interface and panic instead of failing closed).
	var receipts interfaces.ApprovalReceiptRepository
	svc := &knowledgeLifecycleService{receipts: receipts, now: time.Now}
	err := svc.requireReceiptForPublish(context.Background(), &types.Knowledge{
		ID: "k1", TenantID: 7, Classification: string(types.KnowledgeClassificationSecret),
	}, "rcpt_ok")
	require.Error(t, err, "a missing receipt repository must not silently drop the gate")
	var appErr *apperrors.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 500, appErr.HTTPCode)
}
