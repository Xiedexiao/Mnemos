package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/Tencent/WeKnora/internal/utils"
)

// 出站 Webhook（§16.4）的审计动作。
const (
	auditActionWebhookCreated = "webhook.endpoint_created"
	auditActionWebhookUpdated = "webhook.endpoint_updated"
	auditActionWebhookDeleted = "webhook.endpoint_deleted"
	auditActionWebhookReplay  = "webhook.delivery_replayed"
)

// webhookService implements interfaces.WebhookService (KnowAxis V3.0 §16.4).
type webhookService struct {
	repo   interfaces.WebhookRepository
	audit  interfaces.AuditLogService
	client *http.Client
	now    func() time.Time
}

// NewWebhookService constructs the service.
func NewWebhookService(
	repo interfaces.WebhookRepository, audit interfaces.AuditLogService,
) interfaces.WebhookService {
	cfg := utils.DefaultSSRFSafeHTTPClientConfig()
	cfg.Timeout = types.WebhookRequestTimeout
	return &webhookService{
		repo:   repo,
		audit:  audit,
		client: utils.NewSSRFSafeHTTPClient(cfg),
		now:    time.Now,
	}
}

// CreateEndpoint subscribes one URL.
func (s *webhookService) CreateEndpoint(
	ctx context.Context, tenantID uint64, actorID string, input types.WebhookEndpointInput,
) (*types.WebhookEndpoint, error) {
	if tenantID == 0 {
		return nil, apperrors.NewUnauthorizedError("tenant context is required")
	}
	if err := types.ValidateWebhookEndpointInput(input, true); err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	secretEnc, err := encryptWebhookSecret(input.Secret)
	if err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	events := types.NormalizeWebhookEvents(input.Events)
	endpoint := &types.WebhookEndpoint{
		TenantID:    tenantID,
		URL:         strings.TrimSpace(input.URL),
		Description: trimToRunes(input.Description, 255),
		Events:      mustJSONList(events),
		SecretEnc:   secretEnc,
		Enabled:     true,
		CreatedBy:   actorID,
	}
	if input.Enabled != nil {
		endpoint.Enabled = *input.Enabled
	}
	if err := s.repo.CreateEndpoint(ctx, endpoint); err != nil {
		return nil, err
	}
	endpoint.HasSecret = endpoint.SecretEnc != ""
	s.logAudit(ctx, tenantID, actorID, auditActionWebhookCreated, endpoint.ID,
		map[string]interface{}{"url": endpoint.URL, "events": events})
	return endpoint, nil
}

// ListEndpoints returns the tenant's endpoints.
func (s *webhookService) ListEndpoints(
	ctx context.Context, tenantID uint64,
) ([]*types.WebhookEndpoint, error) {
	rows, err := s.repo.ListEndpoints(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	// 密钥永不出库：只回一个"是否已配置"的标记。
	for _, row := range rows {
		if row != nil {
			row.HasSecret = row.SecretEnc != ""
		}
	}
	return rows, nil
}

// UpdateEndpoint edits one endpoint.
func (s *webhookService) UpdateEndpoint(
	ctx context.Context, tenantID uint64, endpointID, actorID string,
	input types.WebhookEndpointInput,
) (*types.WebhookEndpoint, error) {
	if err := types.ValidateWebhookEndpointInput(input, false); err != nil {
		return nil, apperrors.NewBadRequestError(err.Error())
	}
	endpoint, err := s.repo.GetEndpoint(ctx, tenantID, endpointID)
	if err != nil {
		return nil, mapWebhookError(err)
	}
	if trimmed := strings.TrimSpace(input.URL); trimmed != "" {
		endpoint.URL = trimmed
	}
	if input.Description != "" {
		endpoint.Description = trimToRunes(input.Description, 255)
	}
	if len(input.Events) > 0 {
		endpoint.Events = mustJSONList(types.NormalizeWebhookEvents(input.Events))
	}
	if input.Enabled != nil {
		endpoint.Enabled = *input.Enabled
		// 重新启用时清零失败计数：否则它会在下一次失败时立刻又被停用。
		if *input.Enabled {
			endpoint.FailureStreak = 0
		}
	}
	switch {
	case strings.TrimSpace(input.Secret) == "-":
		endpoint.SecretEnc = ""
	case strings.TrimSpace(input.Secret) != "":
		encrypted, encErr := encryptWebhookSecret(input.Secret)
		if encErr != nil {
			return nil, apperrors.NewBadRequestError(encErr.Error())
		}
		endpoint.SecretEnc = encrypted
	}
	if err := s.repo.UpdateEndpoint(ctx, endpoint); err != nil {
		return nil, mapWebhookError(err)
	}
	endpoint.HasSecret = endpoint.SecretEnc != ""
	s.logAudit(ctx, tenantID, actorID, auditActionWebhookUpdated, endpoint.ID,
		map[string]interface{}{"url": endpoint.URL})
	return endpoint, nil
}

// DeleteEndpoint removes one endpoint.
func (s *webhookService) DeleteEndpoint(
	ctx context.Context, tenantID uint64, endpointID, actorID string,
) error {
	if err := s.repo.DeleteEndpoint(ctx, tenantID, endpointID); err != nil {
		return mapWebhookError(err)
	}
	s.logAudit(ctx, tenantID, actorID, auditActionWebhookDeleted, endpointID, nil)
	return nil
}

// Publish enqueues one event for every subscribed endpoint.
//
// 事件按端点头展开成多行（而不是一行 + 多接收方）：每个接收方有自己的重试
// 状态，共用一行会让"甲成功乙失败"无法表达。
func (s *webhookService) Publish(
	ctx context.Context, tenantID uint64,
	eventType types.WebhookEventType, payload map[string]interface{},
) error {
	if !eventType.Valid() {
		return apperrors.NewBadRequestError("unknown webhook event type")
	}
	endpoints, err := s.repo.ListSubscribedEndpoints(ctx, tenantID, eventType)
	if err != nil {
		return err
	}
	if len(endpoints) == 0 {
		return nil
	}
	raw, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return marshalErr
	}
	if len(raw) > types.WebhookMaxPayloadBytes {
		return apperrors.NewBadRequestError(fmt.Sprintf(
			"webhook payload exceeds the %d byte limit", types.WebhookMaxPayloadBytes))
	}
	now := s.now().UTC()
	for _, endpoint := range endpoints {
		delivery := &types.WebhookDelivery{
			TenantID:      tenantID,
			EndpointID:    endpoint.ID,
			EventType:     string(eventType),
			Payload:       types.JSON(raw),
			State:         string(types.WebhookDeliveryPending),
			NextAttemptAt: now,
		}
		if err := s.repo.Enqueue(ctx, delivery); err != nil {
			// 入队失败向上抛：调用方在业务事务里，回滚比"业务成功但事件丢"
			// 更安全。
			return err
		}
	}
	return nil
}

// DeliverDue performs one dispatch pass.
func (s *webhookService) DeliverDue(
	ctx context.Context, limit int,
) (*types.WebhookSweepResult, error) {
	now := s.now().UTC()
	rows, err := s.repo.ListDueDeliveries(ctx, now, limit)
	if err != nil {
		return nil, err
	}
	result := &types.WebhookSweepResult{RanAt: now}
	for _, delivery := range rows {
		if delivery == nil {
			continue
		}
		result.Attempted++
		statusCode, failure := s.deliverOne(ctx, delivery, now)
		attempts := delivery.Attempts + 1
		if failure == "" {
			if markErr := s.repo.MarkDelivered(ctx, delivery.ID, statusCode, now); markErr != nil {
				result.Errors = append(result.Errors, delivery.ID+": "+markErr.Error())
				continue
			}
			result.Delivered++
			if outcomeErr := s.repo.RecordEndpointOutcome(ctx, delivery.TenantID,
				delivery.EndpointID, true, statusCode, "", now); outcomeErr != nil {
				logger.Warnf(ctx, "[webhook] record success failed: %v", outcomeErr)
			}
			continue
		}
		dead := attempts >= types.WebhookMaxAttempts
		next := now.Add(types.WebhookBackoff(attempts))
		if dead {
			result.Dead++
		} else {
			result.Retrying++
		}
		if markErr := s.repo.MarkAttemptFailed(ctx, delivery.ID, attempts,
			statusCode, failure, next, dead); markErr != nil {
			result.Errors = append(result.Errors, delivery.ID+": "+markErr.Error())
			continue
		}
		// 端点级失败计数只在最终失败时累加：中途重试失败是正常的。
		if dead {
			if outcomeErr := s.repo.RecordEndpointOutcome(ctx, delivery.TenantID,
				delivery.EndpointID, false, statusCode, failure, now); outcomeErr != nil {
				logger.Warnf(ctx, "[webhook] record failure failed: %v", outcomeErr)
			}
		}
		result.Errors = append(result.Errors, fmt.Sprintf("%s: %s", delivery.ID, failure))
	}
	return result, nil
}

// deliverOne performs the HTTP POST and returns (statusCode, failure).
func (s *webhookService) deliverOne(
	ctx context.Context, delivery *types.WebhookDelivery, now time.Time,
) (int, string) {
	endpoint, err := s.repo.GetEndpoint(ctx, delivery.TenantID, delivery.EndpointID)
	if err != nil {
		return 0, "endpoint lookup failed: " + err.Error()
	}
	body := []byte(delivery.Payload)
	request, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.URL,
		bytes.NewReader(body))
	if reqErr != nil {
		return 0, "build request failed: " + reqErr.Error()
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "KnowAxis-Webhook/1.0")
	// 幂等键：至少一次投递下，接收方必须按这个 id 去重。
	request.Header.Set("X-KnowAxis-Event-Id", delivery.EventID)
	request.Header.Set("X-KnowAxis-Event-Type", delivery.EventType)
	if secret := s.signingSecret(endpoint); secret != "" {
		request.Header.Set("X-KnowAxis-Signature",
			types.WebhookSignature(secret, now, body))
	}
	response, err := s.client.Do(request)
	if err != nil {
		return 0, err.Error()
	}
	defer response.Body.Close()
	// 读掉并丢弃响应体：不读到 EOF 会阻止连接复用。
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, fmt.Sprintf("receiver returned http %d", response.StatusCode)
	}
	return response.StatusCode, ""
}

// signingSecret decrypts the stored secret.
func (s *webhookService) signingSecret(endpoint *types.WebhookEndpoint) string {
	if endpoint == nil || endpoint.SecretEnc == "" {
		return ""
	}
	decrypted, err := utils.DecryptStoredSecret(endpoint.SecretEnc)
	if err != nil {
		logger.Warnf(context.Background(),
			"[webhook] decrypt secret for endpoint %s failed: %v", endpoint.ID, err)
		return ""
	}
	return decrypted
}

// ListDeliveries returns the outbox.
func (s *webhookService) ListDeliveries(
	ctx context.Context, tenantID uint64, endpointID string,
	state types.WebhookDeliveryState, limit int,
) ([]*types.WebhookDelivery, error) {
	if state != "" && !state.Valid() {
		return nil, apperrors.NewBadRequestError("unknown delivery state")
	}
	return s.repo.ListDeliveries(ctx, tenantID, endpointID, state, limit)
}

// Replay revives one dead delivery.
func (s *webhookService) Replay(
	ctx context.Context, tenantID uint64, deliveryID, actorID string,
) error {
	if err := s.repo.Requeue(ctx, tenantID, deliveryID, s.now().UTC()); err != nil {
		if errors.Is(err, repository.ErrWebhookDeliveryNotFound) {
			return apperrors.NewNotFoundError("delivery not found or not in the dead state")
		}
		return err
	}
	s.logAudit(ctx, tenantID, actorID, auditActionWebhookReplay, deliveryID, nil)
	return nil
}

// Verify checks a request signature.
func (s *webhookService) Verify(secret, header string, body []byte, now time.Time) error {
	return types.VerifyWebhookSignature(secret, header, body, now)
}

// logAudit writes one webhook audit event.
func (s *webhookService) logAudit(
	ctx context.Context, tenantID uint64, actorID, action, targetID string,
	details map[string]interface{},
) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Log(ctx, &types.AuditLog{
		TenantID:    tenantID,
		ActorUserID: actorID,
		Action:      types.AuditAction(action),
		ScopeType:   "webhook",
		TargetType:  "webhook_endpoint",
		TargetID:    targetID,
		Outcome:     "success",
		Details:     gamificationAuditJSON(details),
	})
}

// mapWebhookError maps repository misses onto the API 404.
func mapWebhookError(err error) error {
	if errors.Is(err, repository.ErrWebhookEndpointNotFound) {
		return apperrors.NewNotFoundError("webhook endpoint not found")
	}
	return err
}

// encryptWebhookSecret validates and encrypts a signing secret.
//
// 没有密钥时宁可拒绝也不明文入库：webhook 密钥用来给所有出站事件签名，
// 明文入库等于把所有订阅方的信任一起交出去。
func encryptWebhookSecret(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if len([]rune(trimmed)) < 16 {
		return "", fmt.Errorf("webhook secret must be at least 16 characters")
	}
	key := utils.GetAESKey()
	if key == nil {
		return "", fmt.Errorf(
			"SYSTEM_AES_KEY is not configured; refusing to store the webhook secret in the clear")
	}
	return utils.EncryptAESGCM(trimmed, key)
}

// compile-time guard.
var _ interfaces.WebhookService = (*webhookService)(nil)
