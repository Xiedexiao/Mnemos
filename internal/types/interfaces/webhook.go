package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// WebhookRepository persists endpoints and the delivery outbox.
type WebhookRepository interface {
	// CreateEndpoint inserts one subscriber.
	CreateEndpoint(ctx context.Context, endpoint *types.WebhookEndpoint) error
	// GetEndpoint returns one endpoint in the tenant.
	GetEndpoint(ctx context.Context, tenantID uint64, endpointID string) (*types.WebhookEndpoint, error)
	// ListEndpoints returns the tenant's endpoints.
	ListEndpoints(ctx context.Context, tenantID uint64) ([]*types.WebhookEndpoint, error)
	// ListSubscribedEndpoints returns the enabled endpoints subscribed to an
	// event type.
	ListSubscribedEndpoints(
		ctx context.Context, tenantID uint64, eventType types.WebhookEventType,
	) ([]*types.WebhookEndpoint, error)
	// UpdateEndpoint persists the mutable fields.
	UpdateEndpoint(ctx context.Context, endpoint *types.WebhookEndpoint) error
	// DeleteEndpoint removes one endpoint and its undelivered rows.
	DeleteEndpoint(ctx context.Context, tenantID uint64, endpointID string) error
	// RecordEndpointOutcome updates the failure streak and last attempt
	// result; it auto-disables the endpoint at the streak limit.
	RecordEndpointOutcome(
		ctx context.Context, tenantID uint64, endpointID string,
		success bool, statusCode int, failure string, at time.Time,
	) error

	// Enqueue inserts one outbox row (inside the caller's transaction when
	// the caller has one).
	Enqueue(ctx context.Context, delivery *types.WebhookDelivery) error
	// ListDueDeliveries returns pending rows whose next attempt is due.
	ListDueDeliveries(ctx context.Context, now time.Time, limit int) ([]*types.WebhookDelivery, error)
	// MarkDelivered closes one row.
	MarkDelivered(ctx context.Context, deliveryID string, statusCode int, at time.Time) error
	// MarkAttemptFailed records a failed attempt and either reschedules or
	// marks the row dead.
	MarkAttemptFailed(
		ctx context.Context, deliveryID string, attempts int,
		statusCode int, failure string, nextAttemptAt time.Time, dead bool,
	) error
	// ListDeliveries returns the tenant's outbox rows, newest first.
	ListDeliveries(
		ctx context.Context, tenantID uint64, endpointID string,
		state types.WebhookDeliveryState, limit int,
	) ([]*types.WebhookDelivery, error)
	// Requeue revives a dead row for one more attempt (manual replay).
	Requeue(ctx context.Context, tenantID uint64, deliveryID string, at time.Time) error
}

// WebhookService is the §16.4 outbound webhook surface.
type WebhookService interface {
	// CreateEndpoint subscribes one URL (Admin).
	CreateEndpoint(
		ctx context.Context, tenantID uint64, actorID string, input types.WebhookEndpointInput,
	) (*types.WebhookEndpoint, error)
	// ListEndpoints returns the tenant's endpoints.
	ListEndpoints(ctx context.Context, tenantID uint64) ([]*types.WebhookEndpoint, error)
	// UpdateEndpoint edits one endpoint (Admin).
	UpdateEndpoint(
		ctx context.Context, tenantID uint64, endpointID, actorID string,
		input types.WebhookEndpointInput,
	) (*types.WebhookEndpoint, error)
	// DeleteEndpoint removes one endpoint (Admin).
	DeleteEndpoint(ctx context.Context, tenantID uint64, endpointID, actorID string) error

	// Publish enqueues one event for every subscribed endpoint.
	//
	// 调用方应当在业务事务内调用它（传进来的 ctx 带着事务），这样"业务提交
	// 但事件丢失"不会发生。没有任何端点订阅时不写行。
	Publish(
		ctx context.Context, tenantID uint64,
		eventType types.WebhookEventType, payload map[string]interface{},
	) error
	// DeliverDue performs one dispatch pass over due rows.
	DeliverDue(ctx context.Context, limit int) (*types.WebhookSweepResult, error)
	// ListDeliveries returns the outbox for inspection.
	ListDeliveries(
		ctx context.Context, tenantID uint64, endpointID string,
		state types.WebhookDeliveryState, limit int,
	) ([]*types.WebhookDelivery, error)
	// Replay revives one dead delivery (Admin).
	Replay(ctx context.Context, tenantID uint64, deliveryID, actorID string) error
	// Verify checks a request signature (exported for receivers and tests).
	Verify(secret, header string, body []byte, now time.Time) error
}
