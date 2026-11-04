package chatpipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// fallbackChatModel is a named model that reports its own identity.
type fallbackChatModel struct {
	id string
}

func (m fallbackChatModel) GetModelID() string   { return m.id }
func (m fallbackChatModel) GetModelName() string { return m.id }

func (m fallbackChatModel) Chat(
	context.Context, []chat.Message, *chat.ChatOptions,
) (*types.ChatResponse, error) {
	return &types.ChatResponse{}, nil
}

func (m fallbackChatModel) ChatStream(
	context.Context, []chat.Message, *chat.ChatOptions,
) (<-chan types.StreamResponse, error) {
	ch := make(chan types.StreamResponse)
	close(ch)
	return ch, nil
}

// fallbackModelService resolves named models and can fail specific ids.
type fallbackModelService struct {
	interfaces.ModelService

	failing map[string]error
	// requested records lookups in order.
	requested []string
}

func (s *fallbackModelService) GetChatModel(
	_ context.Context, id string,
) (chat.Chat, error) {
	s.requested = append(s.requested, id)
	if err, broken := s.failing[id]; broken {
		return nil, err
	}
	return fallbackChatModel{id: id}, nil
}

// manageWith builds a ChatManage whose request fields are set.
func manageWith(modelID string, fallbackIDs ...string) *types.ChatManage {
	return &types.ChatManage{PipelineRequest: types.PipelineRequest{
		ChatModelID:          modelID,
		ChatFallbackModelIDs: fallbackIDs,
	}}
}

// TestPrepareChatModelWithoutChainUsesTheRequestedModel is the compatibility
// rule: no chain configured means one lookup, exactly as before.
func TestPrepareChatModelWithoutChainUsesTheRequestedModel(t *testing.T) {
	models := &fallbackModelService{}
	manage := manageWith("primary")

	model, err := prepareChatModelWithFallback(context.Background(), models, manage)
	require.NoError(t, err)
	assert.Equal(t, "primary", model.GetModelID())
	assert.Equal(t, []string{"primary"}, models.requested)
	assert.Nil(t, manage.ChatFallbackTrace,
		"the healthy path carries no trace")
}

// TestPrepareChatModelDegradesToTheNextCandidate covers the headline behaviour.
func TestPrepareChatModelDegradesToTheNextCandidate(t *testing.T) {
	models := &fallbackModelService{failing: map[string]error{
		"primary": errors.New("credentials expired"),
	}}
	manage := manageWith("primary", "primary", "backup")

	model, err := prepareChatModelWithFallback(context.Background(), models, manage)
	require.NoError(t, err)
	assert.Equal(t, "backup", model.GetModelID())
	assert.Equal(t, []string{"primary", "backup"}, models.requested)

	require.NotNil(t, manage.ChatFallbackTrace, "a degrade is recorded")
	assert.True(t, manage.ChatFallbackTrace.Degraded)
	assert.Equal(t, "primary", manage.ChatFallbackTrace.RequestedModelID)
	assert.Equal(t, "backup", manage.ChatFallbackTrace.ServedBy)
	require.Len(t, manage.ChatFallbackTrace.Attempts, 2)
	assert.Equal(t, types.ChatFallbackBeforeStream,
		manage.ChatFallbackTrace.Attempts[0].Phase)
	assert.Contains(t, manage.ChatFallbackTrace.Attempts[0].Err, "credentials")
	assert.Empty(t, manage.ChatFallbackTrace.Attempts[1].Err)
}

// TestPrepareChatModelReportsEveryModelFailing keeps the failure legible.
func TestPrepareChatModelReportsEveryModelFailing(t *testing.T) {
	models := &fallbackModelService{failing: map[string]error{
		"primary": errors.New("down"),
		"backup":  errors.New("also down"),
	}}
	manage := manageWith("primary", "primary", "backup")

	_, err := prepareChatModelWithFallback(context.Background(), models, manage)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "also down", "the last failure is the one reported")
	assert.Len(t, models.requested, 2)
}

// TestPrepareChatModelDropsDuplicateCandidates proves the same model is not
// retried: that only doubles the latency.
func TestPrepareChatModelDropsDuplicateCandidates(t *testing.T) {
	models := &fallbackModelService{}
	manage := manageWith("primary", "primary", "primary", "backup")

	_, err := prepareChatModelWithFallback(context.Background(), models, manage)
	require.NoError(t, err)
	assert.Equal(t, []string{"primary"}, models.requested,
		"the primary succeeded, so the duplicate never runs")
}

// TestPrepareChatModelDefersEmptyIDToTheModelService keeps the previous
// contract: an empty model id is still handed to modelService, which is the
// component that knows whether a default applies. Failing early here would turn
// "the caller did not fill the field" into "the implementation is unavailable".
func TestPrepareChatModelDefersEmptyIDToTheModelService(t *testing.T) {
	models := &fallbackModelService{}
	_, err := prepareChatModelWithFallback(context.Background(), models, &types.ChatManage{})
	require.NoError(t, err)
	assert.Equal(t, []string{""}, models.requested,
		"one lookup with the empty id, exactly as before the fallback feature")
}

// TestPrepareChatModelPreparesOptionsFromTheChatManage proves the options are
// still built from the turn's config after a degrade.
func TestPrepareChatModelPreparesOptionsFromTheChatManage(t *testing.T) {
	models := &fallbackModelService{failing: map[string]error{
		"primary": errors.New("down"),
	}}
	manage := manageWith("primary", "primary", "backup")
	manage.SessionID = "sess-1"
	manage.SummaryConfig = types.SummaryConfig{Temperature: 0.4, MaxTokens: 256}

	model, opt, err := prepareChatModel(context.Background(), models, manage)
	require.NoError(t, err)
	assert.Equal(t, "backup", model.GetModelID())
	require.NotNil(t, opt)
	assert.InDelta(t, 0.4, opt.Temperature, 0.001)
	assert.Equal(t, 256, opt.MaxTokens)
	assert.Equal(t, "sess-1", opt.PromptCacheKey)
}
