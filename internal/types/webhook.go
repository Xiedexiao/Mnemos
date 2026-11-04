package types

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 出站 Webhook（KnowAxis V3.0 §16.4 "Webhook：知识发布、更新、下架、审批、
// 权限事件推送；签名与重试"）。
//
// 平台此前只有入站 API：业务系统想知道"知识发布了"只能轮询。轮询既延迟又
// 浪费配额，而且永远拿不到事件顺序。
//
// 三个决定：
//  1. 投递是**至少一次**，不是恰好一次：网络分区下无法同时保证不丢与不重。
//     订阅方必须按 event_id 幂等处理，这一点写在事件头与文档里，而不是靠
//     平台假装自己能做到 exactly-once。
//  2. 签名覆盖时间戳 + 正文：只签正文的话，重放同一个请求永远有效。时间戳
//     进签名让过期重放失效，订阅方按 ±5 分钟窗口校验。
//  3. 重试是指数退避 + 有上限，失败落地为 dead（可查询、可手动重投），
//     而不是无限重试把队列堵死。
// ============================================================================

// WebhookEventType is one subscribable event class.
type WebhookEventType string

const (
	// WebhookEventKnowledgePublished 知识发布。
	WebhookEventKnowledgePublished WebhookEventType = "knowledge.published"
	// WebhookEventKnowledgeUpdated 知识更新。
	WebhookEventKnowledgeUpdated WebhookEventType = "knowledge.updated"
	// WebhookEventKnowledgeDelisted 知识下架。
	WebhookEventKnowledgeDelisted WebhookEventType = "knowledge.delisted"
	// WebhookEventApprovalDecided 审批完成。
	WebhookEventApprovalDecided WebhookEventType = "approval.decided"
	// WebhookEventPermissionChanged 权限变更。
	WebhookEventPermissionChanged WebhookEventType = "permission.changed"
)

// allWebhookEventTypes is the whitelist in declaration order.
var allWebhookEventTypes = []WebhookEventType{
	WebhookEventKnowledgePublished, WebhookEventKnowledgeUpdated,
	WebhookEventKnowledgeDelisted, WebhookEventApprovalDecided,
	WebhookEventPermissionChanged,
}

// Valid reports whether the event type is known.
func (t WebhookEventType) Valid() bool {
	for _, candidate := range allWebhookEventTypes {
		if candidate == t {
			return true
		}
	}
	return false
}

// AllWebhookEventTypes returns the subscribable catalogue.
func AllWebhookEventTypes() []WebhookEventType {
	out := make([]WebhookEventType, len(allWebhookEventTypes))
	copy(out, allWebhookEventTypes)
	return out
}

// WebhookEndpoint is one subscriber.
type WebhookEndpoint struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null;index:idx_webhook_endpoints_tenant,priority:1"`
	// URL receives the POST.
	URL string `json:"url" gorm:"type:varchar(2048);not null"`
	// Description is the operator-facing label.
	Description string `json:"description" gorm:"type:varchar(255);not null;default:''"`
	// Events is the subscribed event list (JSON array of strings).
	Events JSON `json:"events" gorm:"type:json;not null;default:'[]'"`
	// SecretEnc is the HMAC signing secret, AES-GCM encrypted at rest and
	// never returned by the API.
	SecretEnc string `json:"-" gorm:"column:secret_enc;type:text;not null;default:''"`
	// HasSecret tells the UI whether a secret is configured.
	HasSecret bool `json:"has_secret" gorm:"-"`
	// Enabled pauses delivery without deleting the endpoint.
	Enabled bool `json:"enabled" gorm:"not null"`
	// FailureStreak counts consecutive failed deliveries; after the threshold
	// the endpoint is auto-disabled so a dead receiver stops consuming retries.
	FailureStreak int `json:"failure_streak" gorm:"not null;default:0"`
	// LastSuccessAt / LastError describe the latest attempt.
	LastSuccessAt *time.Time `json:"last_success_at"`
	LastError     string     `json:"last_error" gorm:"type:text;not null;default:''"`
	CreatedBy     string     `json:"created_by" gorm:"type:varchar(36);not null;default:''"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// TableName pins the GORM table name.
func (WebhookEndpoint) TableName() string { return "webhook_endpoints" }

// BeforeCreate assigns an ID before persistence.
func (e *WebhookEndpoint) BeforeCreate(_ *gorm.DB) error {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	if len(e.Events) == 0 {
		e.Events = JSON("[]")
	}
	return nil
}

// WebhookEventTypes decodes the subscribed events, tolerating a bad payload.
func (e *WebhookEndpoint) WebhookEventTypes() []WebhookEventType {
	if e == nil || len(e.Events) == 0 {
		return nil
	}
	var raw []string
	if err := json.Unmarshal(e.Events, &raw); err != nil {
		return nil
	}
	out := make([]WebhookEventType, 0, len(raw))
	for _, value := range raw {
		eventType := WebhookEventType(strings.TrimSpace(value))
		if eventType.Valid() {
			out = append(out, eventType)
		}
	}
	return out
}

// Subscribes reports whether the endpoint wants one event type.
func (e *WebhookEndpoint) Subscribes(eventType WebhookEventType) bool {
	for _, subscribed := range e.WebhookEventTypes() {
		if subscribed == eventType {
			return true
		}
	}
	return false
}

// WebhookDeliveryState is the delivery lifecycle.
type WebhookDeliveryState string

const (
	// WebhookDeliveryPending is queued for the next attempt.
	WebhookDeliveryPending WebhookDeliveryState = "pending"
	// WebhookDeliveryDelivered succeeded.
	WebhookDeliveryDelivered WebhookDeliveryState = "delivered"
	// WebhookDeliveryDead exhausted its retries.
	WebhookDeliveryDead WebhookDeliveryState = "dead"
)

// Valid reports whether the state is known.
func (s WebhookDeliveryState) Valid() bool {
	switch s {
	case WebhookDeliveryPending, WebhookDeliveryDelivered, WebhookDeliveryDead:
		return true
	default:
		return false
	}
}

// WebhookDelivery is one queued/attempted delivery (the outbox row).
//
// outbox 模式：写事件与业务变更在同一个事务里，投递在事务外做。这样"业务
// 提交了但事件丢了"不会发生；反过来"事件投了但业务回滚"由事务保证。
type WebhookDelivery struct {
	ID         string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID   uint64 `json:"tenant_id" gorm:"not null;index:idx_webhook_deliveries_due,priority:1"`
	EndpointID string `json:"endpoint_id" gorm:"type:varchar(36);not null;index:idx_webhook_deliveries_due,priority:2"`
	// EventID is the idempotency key the receiver should dedupe on.
	EventID string `json:"event_id" gorm:"type:varchar(36);not null"`
	// EventType is one of the catalogue.
	EventType string `json:"event_type" gorm:"type:varchar(48);not null"`
	// Payload is the JSON body sent.
	Payload JSON `json:"payload" gorm:"type:json;not null"`
	// State is pending/delivered/dead.
	State string `json:"state" gorm:"type:varchar(16);not null;default:'pending';index:idx_webhook_deliveries_due,priority:3"`
	// Attempts counts attempted deliveries.
	Attempts int `json:"attempts" gorm:"not null;default:0"`
	// NextAttemptAt is when the next try is due (exponential backoff).
	NextAttemptAt time.Time `json:"next_attempt_at" gorm:"index:idx_webhook_deliveries_due,priority:4"`
	// LastStatusCode / LastError describe the latest attempt.
	LastStatusCode int        `json:"last_status_code" gorm:"not null;default:0"`
	LastError      string     `json:"last_error" gorm:"type:text;not null;default:''"`
	DeliveredAt    *time.Time `json:"delivered_at"`
	CreatedAt      time.Time  `json:"created_at"`
}

// TableName pins the GORM table name.
func (WebhookDelivery) TableName() string { return "webhook_deliveries" }

// BeforeCreate assigns an ID before persistence.
func (d *WebhookDelivery) BeforeCreate(_ *gorm.DB) error {
	if d.ID == "" {
		d.ID = uuid.NewString()
	}
	if d.EventID == "" {
		d.EventID = uuid.NewString()
	}
	if len(d.Payload) == 0 {
		d.Payload = JSON("{}")
	}
	return nil
}

// Webhook retry policy.
const (
	// WebhookMaxAttempts is how many times one delivery is tried.
	WebhookMaxAttempts = 6
	// WebhookBaseBackoff is the first retry delay; each attempt doubles it.
	WebhookBaseBackoff = 30 * time.Second
	// WebhookMaxBackoff caps one delay.
	WebhookMaxBackoff = 30 * time.Minute
	// WebhookFailureStreakLimit auto-disables an endpoint after this many
	// consecutive deliveries fail on the final attempt.
	WebhookFailureStreakLimit = 20
	// WebhookSignatureTolerance is the replay window receivers should enforce.
	WebhookSignatureTolerance = 5 * time.Minute
	// WebhookRequestTimeout bounds one POST.
	WebhookRequestTimeout = 15 * time.Second
	// WebhookMaxPayloadBytes caps one body.
	WebhookMaxPayloadBytes = 256 * 1024
)

// WebhookBackoff returns the delay before the next attempt.
//
// 指数退避：30s、1m、2m、4m、8m…直到上限。抖动被刻意省略——同一租户的所有
// 投递本来就分散在不同时刻，加抖动只会让"什么时候会重试"变得不可预测。
func WebhookBackoff(attempt int) time.Duration {
	if attempt <= 1 {
		return WebhookBaseBackoff
	}
	delay := WebhookBaseBackoff
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= WebhookMaxBackoff {
			return WebhookMaxBackoff
		}
	}
	return delay
}

// WebhookSignature renders the HMAC header value for one delivery.
//
// 格式：t=<unix>,v1=<hex>。签的是 "<t>.<body>"，不是正文单独——只签正文的话
// 同一个请求可以无限重放。订阅方需要用同样的密钥与同一套拼接方式校验，并
// 拒绝时间戳超出容差窗口的请求。
func WebhookSignature(secret string, timestamp time.Time, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(fmt.Sprintf("%d.", timestamp.Unix())))
	mac.Write(body)
	return "t=" + fmt.Sprintf("%d", timestamp.Unix()) + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifyWebhookSignature validates a received signature.
//
// 用 hmac.Equal 做常量时间比较：逐字节比较配合错误信息会让攻击者按字节试探。
func VerifyWebhookSignature(secret, header string, body []byte, now time.Time) error {
	parts := strings.Split(header, ",")
	values := make(map[string]string, len(parts))
	for _, part := range parts {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		values[kv[0]] = kv[1]
	}
	rawTimestamp, ok := values["t"]
	if !ok {
		return fmt.Errorf("signature header has no timestamp")
	}
	signature, ok := values["v1"]
	if !ok {
		return fmt.Errorf("signature header has no v1 digest")
	}
	var unix int64
	if _, err := fmt.Sscanf(rawTimestamp, "%d", &unix); err != nil {
		return fmt.Errorf("signature timestamp is not numeric")
	}
	sentAt := time.Unix(unix, 0).UTC()
	skew := now.Sub(sentAt)
	if skew < 0 {
		skew = -skew
	}
	if skew > WebhookSignatureTolerance {
		return fmt.Errorf("signature timestamp is outside the tolerance window")
	}
	expected := WebhookSignature(secret, sentAt, body)
	expectedParts := strings.Split(expected, ",")
	expectedDigest := strings.TrimPrefix(expectedParts[len(expectedParts)-1], "v1=")
	if !hmac.Equal([]byte(expectedDigest), []byte(signature)) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}

// WebhookEndpointInput is the create/update payload.
type WebhookEndpointInput struct {
	URL         string   `json:"url"`
	Description string   `json:"description"`
	Events      []string `json:"events"`
	Enabled     *bool    `json:"enabled"`
	// Secret sets a new signing secret; "" keeps the stored one, "-" clears it.
	Secret string `json:"secret"`
}

// ValidateWebhookEndpointInput checks the shape shared by create and update.
func ValidateWebhookEndpointInput(input WebhookEndpointInput, create bool) error {
	if create && strings.TrimSpace(input.URL) == "" {
		return fmt.Errorf("url is required")
	}
	if raw := strings.TrimSpace(input.URL); raw != "" {
		if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
			return fmt.Errorf("url must be an absolute http(s) URL")
		}
	}
	if create && len(input.Events) == 0 {
		return fmt.Errorf("at least one event type is required")
	}
	for _, value := range input.Events {
		if !WebhookEventType(strings.TrimSpace(value)).Valid() {
			return fmt.Errorf("unknown event type %q", value)
		}
	}
	return nil
}

// NormalizeWebhookEvents dedupes and validates an event list.
func NormalizeWebhookEvents(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if !WebhookEventType(trimmed).Valid() {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	sort.Strings(out)
	return out
}

// WebhookSweepResult reports one dispatch pass.
type WebhookSweepResult struct {
	// Attempted is how many rows were tried.
	Attempted int `json:"attempted"`
	// Delivered is how many succeeded.
	Delivered int `json:"delivered"`
	// Retrying is how many will be retried.
	Retrying int `json:"retrying"`
	// Dead is how many exhausted their retries in this pass.
	Dead int `json:"dead"`
	// Errors carries per-row failures for the log.
	Errors []string `json:"errors,omitempty"`
	// RanAt is the pass time.
	RanAt time.Time `json:"ran_at"`
}
