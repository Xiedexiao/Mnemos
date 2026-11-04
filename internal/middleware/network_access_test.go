package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubNetworkPolicy answers from a fixed verdict.
type stubNetworkPolicy struct {
	interfaces.NetworkAccessService
	allowed  bool
	lastIP   string
	lastTent uint64
	called   bool
}

func (s *stubNetworkPolicy) Allows(
	_ context.Context, tenantID uint64, clientIP string,
) bool {
	s.called = true
	s.lastTent = tenantID
	s.lastIP = clientIP
	return s.allowed
}

// newNetworkAccessRouter wires the middleware behind a handler that echoes.
func newNetworkAccessRouter(policy interfaces.NetworkAccessService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		// Emulate the auth middleware writing the tenant into the context.
		ctx := context.WithValue(c.Request.Context(), types.TenantIDContextKey, uint64(7))
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	router.Use(NetworkAccess(policy))
	router.GET("/probe", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return router
}

// TestNetworkAccessBlocksOutsideAllowList is the enforcement property.
func TestNetworkAccessBlocksOutsideAllowList(t *testing.T) {
	policy := &stubNetworkPolicy{allowed: false}
	router := newNetworkAccessRouter(policy)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.RemoteAddr = "192.168.1.9:1234"
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.True(t, policy.called)
	assert.EqualValues(t, 7, policy.lastTent, "the tenant comes from the auth context")
	assert.Equal(t, "192.168.1.9", policy.lastIP)
}

// TestNetworkAccessAllowsInsideAllowList covers the pass-through.
func TestNetworkAccessAllowsInsideAllowList(t *testing.T) {
	router := newNetworkAccessRouter(&stubNetworkPolicy{allowed: true})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.RemoteAddr = "10.1.2.3:1234"
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

// TestNetworkAccessSkipsWithoutTenant: an unauthenticated request has no
// tenant to check, so the middleware must not answer for the auth layer.
func TestNetworkAccessSkipsWithoutTenant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	policy := &stubNetworkPolicy{allowed: false}
	router.Use(NetworkAccess(policy))
	router.GET("/probe", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/probe", nil))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.False(t, policy.called, "no tenant means no verdict to give")
}

// TestNetworkAccessNilServiceIsNoop keeps the middleware optional.
func TestNetworkAccessNilServiceIsNoop(t *testing.T) {
	router := newNetworkAccessRouter(nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/probe", nil))
	assert.Equal(t, http.StatusOK, w.Code)
}
