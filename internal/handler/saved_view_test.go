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

	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubSavedViewService records what the handler passed down.
type stubSavedViewService struct {
	interfaces.SavedViewService

	list        *types.SavedViewList
	view        *types.SavedKnowledgeView
	err         error
	gotUserID   string
	gotViewID   string
	gotInput    types.SavedViewInput
	createCalls int
	listCalls   int
}

func (s *stubSavedViewService) List(
	_ context.Context, _ uint64, userID string,
) (*types.SavedViewList, error) {
	s.listCalls++
	s.gotUserID = userID
	return s.list, s.err
}

func (s *stubSavedViewService) Get(
	_ context.Context, _ uint64, userID, viewID string,
) (*types.SavedKnowledgeView, error) {
	s.gotUserID = userID
	s.gotViewID = viewID
	return s.view, s.err
}

func (s *stubSavedViewService) Create(
	_ context.Context, _ uint64, userID string, input types.SavedViewInput,
) (*types.SavedKnowledgeView, error) {
	s.createCalls++
	s.gotUserID = userID
	s.gotInput = input
	return s.view, s.err
}

func (s *stubSavedViewService) Update(
	_ context.Context, _ uint64, userID, viewID string, input types.SavedViewInput,
) (*types.SavedKnowledgeView, error) {
	s.gotUserID = userID
	s.gotViewID = viewID
	s.gotInput = input
	return s.view, s.err
}

func (s *stubSavedViewService) Delete(
	_ context.Context, _ uint64, userID, viewID string,
) error {
	s.gotUserID = userID
	s.gotViewID = viewID
	return s.err
}

func (s *stubSavedViewService) SetDefault(
	_ context.Context, _ uint64, userID, viewID string,
) (*types.SavedKnowledgeView, error) {
	s.gotUserID = userID
	s.gotViewID = viewID
	return s.view, s.err
}

// newSavedViewHandlerRouter mounts the handler with the production error
// envelope and a caller identity.
func newSavedViewHandlerRouter(svc interfaces.SavedViewService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	r.Use(func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, uint64(7))
		ctx = types.WithCaller(ctx, types.Caller{
			TenantID: 7, UserID: "reader", Role: types.TenantRoleViewer,
		})
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	h := NewSavedViewHandler(svc)
	r.GET("/saved-views", h.ListSavedViews)
	r.POST("/saved-views", h.CreateSavedView)
	r.GET("/saved-views/:view_id", h.GetSavedView)
	r.PUT("/saved-views/:view_id", h.UpdateSavedView)
	r.DELETE("/saved-views/:view_id", h.DeleteSavedView)
	r.POST("/saved-views/:view_id/default", h.SetDefaultSavedView)
	return r
}

// TestListSavedViewsUsesCallerIdentity is the horizontal-privilege check.
func TestListSavedViewsUsesCallerIdentity(t *testing.T) {
	svc := &stubSavedViewService{list: &types.SavedViewList{
		Views: []*types.SavedKnowledgeView{{ID: "v1", Name: "我的待复审"}},
	}}
	router := newSavedViewHandlerRouter(svc)

	w := httptest.NewRecorder()
	// A user_id in the query string must be ignored.
	router.ServeHTTP(w, httptest.NewRequest(
		http.MethodGet, "/saved-views?user_id=someone-else", nil))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "reader", svc.gotUserID,
		"identity comes from the context, never from a parameter")

	var payload struct {
		Success bool                `json:"success"`
		Data    types.SavedViewList `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	assert.True(t, payload.Success)
	require.Len(t, payload.Data.Views, 1)
}

// TestCreateSavedViewPassesTheFilterThrough proves the filter survives binding.
func TestCreateSavedViewPassesTheFilterThrough(t *testing.T) {
	svc := &stubSavedViewService{view: &types.SavedKnowledgeView{ID: "v1", Name: "视图"}}
	router := newSavedViewHandlerRouter(svc)

	body, err := json.Marshal(types.SavedViewInput{
		Name:            "视图",
		KnowledgeBaseID: "kb-1",
		Filter: types.SavedViewFilter{
			Keyword:           "E45",
			LifecycleStatuses: []string{"valid", "stale"},
			SortBy:            "updated_at",
		},
		IsDefault: true,
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/saved-views", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "reader", svc.gotUserID)
	assert.Equal(t, "视图", svc.gotInput.Name)
	assert.Equal(t, "E45", svc.gotInput.Filter.Keyword)
	assert.Equal(t, []string{"valid", "stale"}, svc.gotInput.Filter.LifecycleStatuses)
	assert.True(t, svc.gotInput.IsDefault)
}

// TestCreateSavedViewRejectsMalformedBody keeps a typo from being swallowed.
func TestCreateSavedViewRejectsMalformedBody(t *testing.T) {
	router := newSavedViewHandlerRouter(&stubSavedViewService{})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/saved-views",
		bytes.NewReader([]byte("{not json")))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestSavedViewPathParameterIsForwarded covers the per-view routes.
func TestSavedViewPathParameterIsForwarded(t *testing.T) {
	svc := &stubSavedViewService{view: &types.SavedKnowledgeView{ID: "view-9"}}
	router := newSavedViewHandlerRouter(svc)

	for _, target := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/saved-views/view-9"},
		{http.MethodDelete, "/saved-views/view-9"},
		{http.MethodPost, "/saved-views/view-9/default"},
	} {
		svc.gotViewID = ""
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(target.method, target.path, nil))
		require.Equal(t, http.StatusOK, w.Code, target.method+" "+target.path)
		assert.Equal(t, "view-9", svc.gotViewID, target.method+" "+target.path)
	}
}

// TestSavedViewRequiresTenantContext refuses a request without one.
func TestSavedViewRequiresTenantContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	h := NewSavedViewHandler(&stubSavedViewService{})
	r.GET("/saved-views", h.ListSavedViews)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/saved-views", nil))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
