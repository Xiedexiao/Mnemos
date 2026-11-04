package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubSuggestionService records what the handler passed down.
type stubSuggestionService struct {
	interfaces.SearchSuggestionService

	bundle     *types.SuggestionBundle
	suggestErr error

	// captured arguments
	gotPrefix string
	gotLimit  int
	gotDays   int
	gotUserID string
	calls     int
}

func (s *stubSuggestionService) Suggest(
	_ context.Context, _ uint64, prefix string, limit, hotDays int,
) (*types.SuggestionBundle, error) {
	s.calls++
	s.gotPrefix = prefix
	s.gotLimit = limit
	s.gotDays = hotDays
	return s.bundle, s.suggestErr
}

func (s *stubSuggestionService) ClearHistory(
	_ context.Context, _ uint64, userID string,
) error {
	s.gotUserID = userID
	return nil
}

// newSuggestionHandlerRouter mounts the handler with the production error
// envelope and a tenant + caller context, matching how the real middleware
// stack populates them.
func newSuggestionHandlerRouter(svc interfaces.SearchSuggestionService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	r.Use(func(c *gin.Context) {
		// 生产中间件把租户 ID 放在这条键上；WithCaller 另行携带身份。
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, uint64(7))
		ctx = types.WithCaller(ctx, types.Caller{
			TenantID: 7, UserID: "reader", Role: types.TenantRoleViewer,
		})
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	h := NewSearchSuggestionHandler(svc)
	r.GET("/search/suggestions", h.GetSearchSuggestions)
	r.DELETE("/search/history", h.DeleteSearchHistory)
	return r
}

// TestGetSearchSuggestionsForwardsQueryParams proves the handler passes the
// prefix, limit and window through untouched (the service clamps them).
func TestGetSearchSuggestionsForwardsQueryParams(t *testing.T) {
	svc := &stubSuggestionService{
		bundle: &types.SuggestionBundle{
			History: []types.SearchSuggestion{{Text: "E45 报警", Kind: types.SuggestionKindHistory}},
			Hot:     []types.SearchSuggestion{},
			Titles:  []types.SearchSuggestion{},
		},
	}
	router := newSuggestionHandlerRouter(svc)

	params := url.Values{}
	params.Set("q", "  E45 报警 ")
	params.Set("limit", "5")
	params.Set("hot_days", "14")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(
		http.MethodGet, "/search/suggestions?"+params.Encode(), nil))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "  E45 报警 ", svc.gotPrefix, "the service normalizes, the handler does not")
	assert.Equal(t, 5, svc.gotLimit)
	assert.Equal(t, 14, svc.gotDays)

	var payload struct {
		Success bool                   `json:"success"`
		Data    types.SuggestionBundle `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	assert.True(t, payload.Success)
	require.Len(t, payload.Data.History, 1)
}

// TestGetSearchSuggestionsAcceptsEmptyPrefix is the "box just opened" call.
func TestGetSearchSuggestionsAcceptsEmptyPrefix(t *testing.T) {
	svc := &stubSuggestionService{bundle: &types.SuggestionBundle{
		History: []types.SearchSuggestion{},
		Hot:     []types.SearchSuggestion{{Text: "热词", Kind: types.SuggestionKindHot, Count: 3}},
		Titles:  []types.SearchSuggestion{},
	}}
	router := newSuggestionHandlerRouter(svc)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/search/suggestions", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, 1, svc.calls)
}

// TestGetSearchSuggestionsRejectsBadNumbers keeps a typo from silently meaning
// "use the default".
func TestGetSearchSuggestionsRejectsBadNumbers(t *testing.T) {
	router := newSuggestionHandlerRouter(&stubSuggestionService{})

	for _, target := range []string{
		"/search/suggestions?limit=abc",
		"/search/suggestions?hot_days=-1",
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		assert.Equal(t, http.StatusBadRequest, w.Code, target)
	}
}

// TestDeleteSearchHistoryUsesCallerIdentity is the horizontal-privilege check:
// a user_id in the query string must be ignored.
func TestDeleteSearchHistoryUsesCallerIdentity(t *testing.T) {
	svc := &stubSuggestionService{}
	router := newSuggestionHandlerRouter(svc)

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(
		http.MethodDelete, "/search/history?user_id=someone-else", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "reader", svc.gotUserID,
		"identity comes from the context, never from a parameter")
}
