package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// ============================================================================
// 导出管控 + DLP 输出扫描（KnowAxis V3.0 §2.2 export 动作 / §13 生成内容
// 校验 v1）。存储原语与服务的接口约定。
// ============================================================================

// ExportRequestRepository persists export requests (approval state machine).
type ExportRequestRepository interface {
	// CreateRequest inserts one request (ID assigned by the model hook).
	CreateRequest(ctx context.Context, req *types.ExportRequest) error
	// GetRequest resolves one request by ID within the tenant; returns
	// gorm.ErrRecordNotFound when missing (service maps to 404).
	GetRequest(ctx context.Context, tenantID uint64, requestID string) (*types.ExportRequest, error)
	// SaveRequest persists the full state of a loaded request (the service
	// loads → mutates → saves, so status transitions stay auditable).
	SaveRequest(ctx context.Context, req *types.ExportRequest) error
	// ListRequests lists the tenant's requests, newest first; an empty
	// status lists all.
	ListRequests(ctx context.Context, tenantID uint64, status string) ([]*types.ExportRequest, error)
}

// ExportControlService owns the export request state machine (V3.0 §2.2).
// Every status change goes through here so the approval policy, the
// four-eyes rule and the audit trail stay enforced in one place.
type ExportControlService interface {
	// Create registers one export request. Default status is pending;
	// watermark_text defaults to "用户名-时间-随机4位". 审批策略 v1：
	// max_classification=secret/confidential 或 scope=audit 必须人工审批
	// （保持 pending）；public/internal 且非审计导出直接置 approved
	// （自动放行）。审计：export.request_created。
	Create(ctx context.Context, req *types.ExportRequest) (*types.ExportRequest, error)
	// Decide applies one approval decision (approve|reject). Admin-only and
	// four-eyes (approver ≠ requester) are enforced here in addition to the
	// route guard. Only pending requests can be decided.
	// 审计：export.request_decided。
	Decide(ctx context.Context, tenantID uint64, requestID string, input *types.ExportDecisionInput) (*types.ExportRequest, error)
	// Fulfill marks an approved request fulfilled and returns the row (its
	// watermark_text is what the frontend renders on the exported artifact).
	// 审计：export.request_fulfilled。
	Fulfill(ctx context.Context, tenantID uint64, requestID, actorID string) (*types.ExportRequest, error)
	// List lists the tenant's requests, newest first; an empty status lists
	// all (invalid non-empty status is a bad request).
	List(ctx context.Context, tenantID uint64, status string) ([]*types.ExportRequest, error)
	// Get resolves one request by ID within the tenant.
	Get(ctx context.Context, tenantID uint64, requestID string) (*types.ExportRequest, error)
}

// DLPRepository persists DLP findings (event stream, no hash chain — the
// source is the service itself, unlike evidence_packs).
type DLPRepository interface {
	// CreateFinding inserts one finding row.
	CreateFinding(ctx context.Context, finding *types.DLPFinding) error
	// ListFindings returns the tenant's most recent findings, newest first.
	// limit <= 0 uses the default; a hard cap keeps unbounded scans off the
	// table (same posture as the evidence/audit lists).
	ListFindings(ctx context.Context, tenantID uint64, limit int) ([]*types.DLPFinding, error)
}

// DLPService scans generated output for sensitive information (V3.0 §13
// 生成内容校验 v1) and records every hit. 扫描不暴露 HTTP：调用方是问答
// 输出挂钩（completeAssistantMessage / saveEvidencePack 附近）与导出管线
// 等内部服务。Admin 注册自定义规则经 /dlp/patterns 路由。
type DLPService interface {
	// ScanText runs the built-in detectors (中国大陆手机号、身份证、银行卡
	// 13-19 位简化、邮箱) plus the tenant's registered custom patterns over
	// text, replaces every hit with a mask (保留前 2 后 2，中间打星) and
	// records one finding row per hit (action=masked). Returns the cleaned
	// text and the number of hits. Finding persistence is best-effort: a
	// storage failure is logged, never blocks the caller's output path.
	ScanText(ctx context.Context, tenantID uint64, requestID, messageID, text string) (cleaned string, findings int, err error)
	// AddCustomPattern registers one tenant-scoped custom regex rule (Admin
	// only; the route guard enforces the role). v1 stores rules in memory:
	// 不建表、重启丢失、多副本不同步。审计：dlp.pattern_added。
	AddCustomPattern(ctx context.Context, tenantID uint64, actorID string, pattern *types.DLPCustomPattern) (*types.DLPCustomPattern, error)
	// ListFindings lists the tenant's recent findings, newest first
	// (Admin-facing 巡检面，limit 有默认值与硬上限).
	ListFindings(ctx context.Context, tenantID uint64, limit int) ([]*types.DLPFinding, error)
}
