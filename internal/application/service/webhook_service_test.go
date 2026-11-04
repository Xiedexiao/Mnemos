package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
)

const webhookTestDDL = `
CREATE TABLE webhook_endpoints (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    url VARCHAR(2048) NOT NULL,
    description VARCHAR(255) NOT NULL DEFAULT '',
    events TEXT NOT NULL DEFAULT '[]',
    secret_enc TEXT NOT NULL DEFAULT '',
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    failure_streak INTEGER NOT NULL DEFAULT 0,
    last_success_at DATETIME,
    last_error TEXT NOT NULL DEFAULT '',
    created_by VARCHAR(36) NOT NULL DEFAULT '',
    created_at DATETIME,
    updated_at DATETIME
);
CREATE TABLE webhook_deliveries (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    endpoint_id VARCHAR(36) NOT NULL,
    event_id VARCHAR(36) NOT NULL,
    event_type VARCHAR(48) NOT NULL,
    payload TEXT NOT NULL DEFAULT '{}',
    state VARCHAR(16) NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at DATETIME NOT NULL,
    last_status_code INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    delivered_at DATETIME,
    created_at DATETIME
);
`

func setupWebhookDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(webhookTestDDL).Error)
	return db
}

// newWebhookFixture builds a service bound to one test server.
func newWebhookFixture(t *testing.T, handler http.Handler) (*webhookService, *gorm.DB, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	db := setupWebhookDB(t)
	svc := &webhookService{
		repo:   repository.NewWebhookRepository(db),
		client: server.Client(),
		now:    func() time.Time { return time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC) },
	}
	return svc, db, server
}

// TestPublishEnqueuesPerEndpoint keeps one row per receiver so retries are
// independent.
func TestPublishEnqueuesPerEndpoint(t *testing.T) {
	svc, _, server := newWebhookFixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	ctx := context.Background()

	for _, url := range []string{server.URL + "/a", server.URL + "/b"} {
		_, err := svc.CreateEndpoint(ctx, 7, "admin", types.WebhookEndpointInput{
			URL: url, Events: []string{"knowledge.published"},
		})
		require.NoError(t, err)
	}
	// An endpoint not subscribed to this event gets nothing.
	_, err := svc.CreateEndpoint(ctx, 7, "admin", types.WebhookEndpointInput{
		URL: server.URL + "/c", Events: []string{"approval.decided"},
	})
	require.NoError(t, err)

	require.NoError(t, svc.Publish(ctx, 7, types.WebhookEventKnowledgePublished,
		map[string]interface{}{"knowledge_id": "k-1"}))

	rows, err := svc.ListDeliveries(ctx, 7, "", "", 50)
	require.NoError(t, err)
	assert.Len(t, rows, 2, "one row per subscribed endpoint")
	for _, row := range rows {
		assert.Equal(t, string(types.WebhookDeliveryPending), row.State)
		assert.NotEmpty(t, row.EventID, "every row needs an idempotency key")
	}
}

// TestPublishWithoutSubscribersWritesNothing keeps the outbox clean.
func TestPublishWithoutSubscribersWritesNothing(t *testing.T) {
	svc, _, _ := newWebhookFixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	ctx := context.Background()
	require.NoError(t, svc.Publish(ctx, 7, types.WebhookEventKnowledgePublished, nil))
	rows, err := svc.ListDeliveries(ctx, 7, "", "", 50)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

// TestDeliverDueSignsAndDelivers covers the happy path including the signature.
func TestDeliverDueSignsAndDelivers(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", "weknora-test-aes-key-32bytes!!!!")
	var (
		gotSignature, gotEventID, gotEventType string
		gotBody                                []byte
	)
	svc, _, server := newWebhookFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSignature = r.Header.Get("X-KnowAxis-Signature")
		gotEventID = r.Header.Get("X-KnowAxis-Event-Id")
		gotEventType = r.Header.Get("X-KnowAxis-Event-Type")
		gotBody = make([]byte, r.ContentLength)
		_, _ = r.Body.Read(gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	ctx := context.Background()

	secret := "super-secret-signing-key"
	endpoint, err := svc.CreateEndpoint(ctx, 7, "admin", types.WebhookEndpointInput{
		URL: server.URL, Events: []string{"knowledge.published"}, Secret: secret,
	})
	require.NoError(t, err)
	assert.True(t, endpoint.HasSecret)
	assert.NotContains(t, endpoint.SecretEnc, secret, "the secret must not be stored in the clear")

	require.NoError(t, svc.Publish(ctx, 7, types.WebhookEventKnowledgePublished,
		map[string]interface{}{"knowledge_id": "k-1"}))
	result, err := svc.DeliverDue(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Attempted)
	assert.Equal(t, 1, result.Delivered)

	assert.Equal(t, "knowledge.published", gotEventType)
	assert.NotEmpty(t, gotEventID)
	assert.NotEmpty(t, gotSignature)

	// The receiver can verify the signature against the same secret.
	require.NoError(t, types.VerifyWebhookSignature(secret, gotSignature, gotBody, svc.now()))
	// A tampered body must fail.
	assert.Error(t, types.VerifyWebhookSignature(secret, gotSignature,
		append(gotBody, 'x'), svc.now()))
	// A replayed signature outside the tolerance window must fail.
	assert.Error(t, types.VerifyWebhookSignature(secret, gotSignature, gotBody,
		svc.now().Add(types.WebhookSignatureTolerance+time.Minute)))
}

// TestDeliverDueRetriesThenDies covers the retry ladder and the dead state.
func TestDeliverDueRetriesThenDies(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", "weknora-test-aes-key-32bytes!!!!")
	svc, db, server := newWebhookFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	ctx := context.Background()
	_, err := svc.CreateEndpoint(ctx, 7, "admin", types.WebhookEndpointInput{
		URL: server.URL, Events: []string{"knowledge.published"},
	})
	require.NoError(t, err)
	require.NoError(t, svc.Publish(ctx, 7, types.WebhookEventKnowledgePublished, nil))

	// Drive the row through its attempt budget by moving the clock forward.
	clock := svc.now()
	for attempt := 1; attempt <= types.WebhookMaxAttempts; attempt++ {
		svc.now = func() time.Time { return clock }
		require.NoError(t, db.Model(&types.WebhookDelivery{}).
			Where("1 = 1").Update("next_attempt_at", clock).Error)
		result, err := svc.DeliverDue(ctx, 10)
		require.NoError(t, err)
		assert.Equal(t, 1, result.Attempted)
		if attempt < types.WebhookMaxAttempts {
			assert.Equal(t, 1, result.Retrying, "attempt %d should be retried", attempt)
			assert.Zero(t, result.Dead)
		} else {
			assert.Equal(t, 1, result.Dead, "the final attempt gives up")
			assert.Zero(t, result.Retrying)
		}
		clock = clock.Add(types.WebhookMaxBackoff)
	}

	rows, err := svc.ListDeliveries(ctx, 7, "", types.WebhookDeliveryDead, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, types.WebhookMaxAttempts, rows[0].Attempts)
	assert.Contains(t, rows[0].LastError, "500")

	// A dead row can be replayed after the receiver is fixed.
	require.NoError(t, svc.Replay(ctx, 7, rows[0].ID, "admin"))
	replayed, err := svc.ListDeliveries(ctx, 7, "", types.WebhookDeliveryPending, 10)
	require.NoError(t, err)
	require.Len(t, replayed, 1)
	assert.Zero(t, replayed[0].Attempts)
}

// TestEndpointAutoDisablesAfterRepeatedFinalFailures keeps a dead receiver from
// consuming retries forever.
func TestEndpointAutoDisablesAfterRepeatedFinalFailures(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", "weknora-test-aes-key-32bytes!!!!")
	svc, _, server := newWebhookFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	ctx := context.Background()
	endpoint, err := svc.CreateEndpoint(ctx, 7, "admin", types.WebhookEndpointInput{
		URL: server.URL, Events: []string{"knowledge.published"},
	})
	require.NoError(t, err)
	require.NoError(t, svc.Publish(ctx, 7, types.WebhookEventKnowledgePublished, nil))
	_, deliverErr := svc.DeliverDue(ctx, 10)
	require.NoError(t, deliverErr)

	// Emulate the streak reaching the limit (the repository increments it).
	for i := 0; i < types.WebhookFailureStreakLimit; i++ {
		require.NoError(t, svc.repo.RecordEndpointOutcome(ctx, 7, endpoint.ID,
			false, 502, "boom", svc.now()))
	}
	endpoints, err := svc.ListEndpoints(ctx, 7)
	require.NoError(t, err)
	require.Len(t, endpoints, 1)
	assert.False(t, endpoints[0].Enabled,
		"an endpoint that keeps failing on the final attempt is disabled")
}

// TestCreateEndpointValidation covers the shape rules.
func TestCreateEndpointValidation(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", "weknora-test-aes-key-32bytes!!!!")
	svc, _, _ := newWebhookFixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	ctx := context.Background()

	_, err := svc.CreateEndpoint(ctx, 7, "admin", types.WebhookEndpointInput{
		Events: []string{"knowledge.published"},
	})
	require.Error(t, err, "url is required")

	_, err = svc.CreateEndpoint(ctx, 7, "admin", types.WebhookEndpointInput{
		URL: "ftp://example.com/hook", Events: []string{"knowledge.published"},
	})
	require.Error(t, err)

	_, err = svc.CreateEndpoint(ctx, 7, "admin", types.WebhookEndpointInput{
		URL: "https://example.com/hook",
	})
	require.Error(t, err, "at least one event is required")

	_, err = svc.CreateEndpoint(ctx, 7, "admin", types.WebhookEndpointInput{
		URL: "https://example.com/hook", Events: []string{"knowledge.exploded"},
	})
	require.Error(t, err)

	_, err = svc.CreateEndpoint(ctx, 7, "admin", types.WebhookEndpointInput{
		URL: "https://example.com/hook", Events: []string{"knowledge.published"},
		Secret: "short",
	})
	require.Error(t, err, "a weak signing secret must be rejected")
}

// TestSecretNeverLeavesTheService covers the API projection.
func TestSecretNeverLeavesTheService(t *testing.T) {
	t.Setenv("SYSTEM_AES_KEY", "weknora-test-aes-key-32bytes!!!!")
	svc, _, server := newWebhookFixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	ctx := context.Background()
	_, err := svc.CreateEndpoint(ctx, 7, "admin", types.WebhookEndpointInput{
		URL: server.URL, Events: []string{"knowledge.published"},
		Secret: "super-secret-signing-key",
	})
	require.NoError(t, err)

	raw, err := json.Marshal(mustList(t, svc, ctx))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "super-secret-signing-key")
	assert.Contains(t, string(raw), `"has_secret":true`)
}

// mustList is a helper that fails the test on error.
func mustList(t *testing.T, svc *webhookService, ctx context.Context) []*types.WebhookEndpoint {
	t.Helper()
	rows, err := svc.ListEndpoints(ctx, 7)
	require.NoError(t, err)
	return rows
}
