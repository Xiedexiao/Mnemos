package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ErrDraftConflict reports that the client's expected revision is stale.
//
// 用哨兵错误而不是直接返回 409：handler 需要把**双方内容**一起带回去，让
// 前端合并而不是静默覆盖。last-write-wins 在协同编辑里会直接吃掉别人的一段
// 文字，这个代价比一次冲突提示高得多。
var ErrDraftConflict = errors.New("draft: revision conflict")

// draftService implements interfaces.DraftService (KnowAxis V3.0 §5.4).
type draftService struct {
	repo interfaces.DraftRepository
	now  func() time.Time
}

// NewDraftService constructs the service.
func NewDraftService(repo interfaces.DraftRepository) interfaces.DraftService {
	return &draftService{repo: repo, now: time.Now}
}

// Save autosaves one draft.
func (s *draftService) Save(
	ctx context.Context, tenantID uint64, userID string, input types.DraftSaveInput,
) (*types.Draft, error) {
	if userID == "" {
		return nil, apperrors.NewUnauthorizedError("caller identity is required")
	}
	if err := types.ValidateDraftInput(input); err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	mentions, err := types.NormalizeMentions(input.Mentions)
	if err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	mentionsJSON := types.JSON(marshalJSONBytes(mentions))
	draft := &types.Draft{
		TenantID:  tenantID,
		UserID:    userID,
		Kind:      string(types.DraftKind(input.Kind)),
		ScopeID:   input.ScopeID,
		Title:     input.Title,
		Body:      input.Body,
		Anchor:    input.Anchor,
		Mentions:  mentionsJSON,
		UpdatedAt: s.now().UTC(),
	}
	stored, conflicted, saveErr := s.repo.Save(ctx, draft, input.ExpectedRevision)
	if saveErr != nil {
		return nil, saveErr
	}
	if conflicted {
		return stored, ErrDraftConflict
	}
	return stored, nil
}

// Get returns one draft for the caller.
func (s *draftService) Get(
	ctx context.Context, tenantID uint64, userID string, kind types.DraftKind, scopeID string,
) (*types.Draft, error) {
	if userID == "" {
		return nil, apperrors.NewUnauthorizedError("caller identity is required")
	}
	if !kind.Valid() {
		return nil, apperrors.NewBadRequestError("unknown draft kind")
	}
	return s.repo.Get(ctx, tenantID, userID, kind, scopeID)
}

// List returns the caller's drafts.
func (s *draftService) List(
	ctx context.Context, tenantID uint64, userID string, kind string, limit int,
) ([]*types.Draft, error) {
	if userID == "" {
		return nil, apperrors.NewUnauthorizedError("caller identity is required")
	}
	if kind != "" && !types.DraftKind(kind).Valid() {
		return nil, apperrors.NewBadRequestError("unknown draft kind")
	}
	return s.repo.List(ctx, tenantID, userID, kind, limit)
}

// Delete removes one draft.
func (s *draftService) Delete(
	ctx context.Context, tenantID uint64, userID, draftID string,
) error {
	if userID == "" {
		return apperrors.NewUnauthorizedError("caller identity is required")
	}
	if err := s.repo.Delete(ctx, tenantID, userID, draftID); err != nil {
		if errors.Is(err, repository.ErrDraftNotFound) {
			return apperrors.NewNotFoundError("draft not found")
		}
		return err
	}
	return nil
}

// marshalJSONBytes encodes a mention list for storage, never failing on a
// well-formed list.
func marshalJSONBytes(values []types.Mention) []byte {
	if values == nil {
		return []byte("[]")
	}
	data, err := json.Marshal(values)
	if err != nil {
		return []byte("[]")
	}
	return data
}
