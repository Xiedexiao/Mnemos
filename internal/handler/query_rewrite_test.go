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

// stubRewriteService records what the handler passed down.
type stubRewriteService struct {
	interfaces.QueryRewriteService

	plan     *types.QueryRewrite
	err      error
	gotKBIDs []string
	gotQuery string
	calls    int
}

func (s *stubRewriteService) Rewrite(
	_ context.Context, _ uint64, kbIDs []string, query string,
) (*types.QueryRewrite, error) {
	s.calls++
	s.gotKBIDs = kbIDs
	s.gotQuery = query
	return s.plan, s.err
}

// newRewriteHandlerRouter mounts the handler with the production error
// envelope and a tenant context.
func newRewriteHandlerRouter(svc interfaces.QueryRewriteService) *gin.Engine {
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
	h := NewQueryRewriteHandler(svc)
	r.GET("/search/rewrite", h.GetQueryRewrite)
	return r
}

// TestGetQueryRewriteReturnsPlan covers the happy path.
func TestGetQueryRewriteReturnsPlan(t *testing.T) {
	svc := &stubRewriteService{plan: &types.QueryRewrite{
		Original: "报井处理",
		Pinyin:   "baojingchuli",
		Corrections: []types.FuzzyCorrection{{
			Original: "报井", Suggestion: "报警", Distance: 1, Reason: types.FuzzyReasonHomophone,
		}},
		Alternatives: []string{"报警处理"},
	}}
	router := newRewriteHandlerRouter(svc)

	params := url.Values{}
	params.Set("q", " 报井处理 ")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/search/rewrite?"+params.Encode(), nil))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "报井处理", svc.gotQuery, "the handler trims but does not rewrite")

	var payload struct {
		Success bool               `json:"success"`
		Data    types.QueryRewrite `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &payload))
	assert.True(t, payload.Success)
	assert.Equal(t, "报井处理", payload.Data.Original)
	assert.Equal(t, []string{"报警处理"}, payload.Data.Alternatives)
}

// TestGetQueryRewriteParsesBaseScope covers the comma-separated scope.
func TestGetQueryRewriteParsesBaseScope(t *testing.T) {
	svc := &stubRewriteService{plan: &types.QueryRewrite{Original: "报警"}}
	router := newRewriteHandlerRouter(svc)

	params := url.Values{}
	params.Set("q", "报警")
	params.Set("knowledge_base_ids", " kb-1 , ,kb-2 ")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/search/rewrite?"+params.Encode(), nil))

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, []string{"kb-1", "kb-2"}, svc.gotKBIDs, "blanks are dropped")
}

// TestGetQueryRewriteRequiresQuery rejects an empty q rather than guessing.
func TestGetQueryRewriteRequiresQuery(t *testing.T) {
	router := newRewriteHandlerRouter(&stubRewriteService{})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/search/rewrite", nil))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestGetQueryRewriteRejectsWhitespaceQuery treats " " as empty.
func TestGetQueryRewriteRejectsWhitespaceQuery(t *testing.T) {
	router := newRewriteHandlerRouter(&stubRewriteService{})
	params := url.Values{}
	params.Set("q", "   ")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/search/rewrite?"+params.Encode(), nil))
	assert.Equal(t, http.StatusBadRequest, w.Code)
}
