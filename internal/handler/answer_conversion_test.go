package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubAnswerConversion records what the handler passed down.
type stubAnswerConversion struct {
	interfaces.AnswerConversionService

	result   *types.AnswerConversionResult
	err      error
	gotInput types.AnswerConversionInput
	gotActor string
	calls    int
}

func (s *stubAnswerConversion) Convert(
	_ context.Context, _ uint64, actorID string, input types.AnswerConversionInput,
) (*types.AnswerConversionResult, error) {
	s.calls++
	s.gotActor = actorID
	s.gotInput = input
	return s.result, s.err
}

// newAnswerConversionRouter mounts the handler with a tenant and caller context.
func newAnswerConversionRouter(svc interfaces.AnswerConversionService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	r.Use(func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, uint64(7))
		ctx = types.WithCaller(ctx, types.Caller{
			TenantID: 7, UserID: "reader", Role: types.TenantRoleContributor,
		})
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	h := NewAnswerConversionHandler(svc)
	r.POST("/knowledge-candidates/from-answer", h.ConvertAnswerToKnowledge)
	return r
}

// TestConvertAnswerToKnowledgeForwardsInput covers the happy path.
func TestConvertAnswerToKnowledgeForwardsInput(t *testing.T) {
	svc := &stubAnswerConversion{result: &types.AnswerConversionResult{
		CandidateID: "cand-1", Created: true, Title: "E45 处理",
		SourceRequestID: "req-1", EvidenceCount: 2,
	}}
	router := newAnswerConversionRouter(svc)

	body, err := json.Marshal(types.AnswerConversionInput{
		RequestID: "req-1", Title: "自定义标题", Level: "L2",
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/knowledge-candidates/from-answer",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "reader", svc.gotActor, "the actor comes from context, never the body")
	assert.Equal(t, "req-1", svc.gotInput.RequestID)
	assert.Equal(t, "自定义标题", svc.gotInput.Title)
	assert.Equal(t, "L2", svc.gotInput.Level)

	var payload struct {
		Success bool                         `json:"success"`
		Data    types.AnswerConversionResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	assert.True(t, payload.Success)
	assert.Equal(t, "cand-1", payload.Data.CandidateID)
}

// TestConvertAnswerToKnowledgeIgnoresBodyActor proves a caller cannot attribute
// the conversion to someone else.
func TestConvertAnswerToKnowledgeIgnoresBodyActor(t *testing.T) {
	svc := &stubAnswerConversion{result: &types.AnswerConversionResult{CandidateID: "c"}}
	router := newAnswerConversionRouter(svc)

	// "requested_by" / "user_id" are not part of the input type; a body that
	// carries them must not influence the actor.
	body := []byte(`{"request_id":"req-1","requested_by":"someone-else","user_id":"x"}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/knowledge-candidates/from-answer",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "reader", svc.gotActor)
}

// TestConvertAnswerToKnowledgeRejectsMalformedBody keeps binding errors visible.
func TestConvertAnswerToKnowledgeRejectsMalformedBody(t *testing.T) {
	router := newAnswerConversionRouter(&stubAnswerConversion{})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/knowledge-candidates/from-answer",
		bytes.NewReader([]byte("{not json")))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestConvertAnswerToKnowledgeSurfacesServiceErrors covers the 400 path.
func TestConvertAnswerToKnowledgeSurfacesServiceErrors(t *testing.T) {
	svc := &stubAnswerConversion{
		err: apperrors.NewBadRequestError("a refusal carries no answer to convert"),
	}
	router := newAnswerConversionRouter(svc)

	body, err := json.Marshal(types.AnswerConversionInput{RequestID: "req-1"})
	require.NoError(t, err)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/knowledge-candidates/from-answer",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestConvertAnswerToKnowledgeRequiresTenant refuses a request without one.
func TestConvertAnswerToKnowledgeRequiresTenant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	h := NewAnswerConversionHandler(&stubAnswerConversion{})
	r.POST("/knowledge-candidates/from-answer", h.ConvertAnswerToKnowledge)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/knowledge-candidates/from-answer",
		bytes.NewReader([]byte(`{"request_id":"req-1"}`)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
