package types

import (
	"time"
)

// Knowledge external share links (KnowAxis V3.0 §2.5, v1 subset).
//
// A share link is a tenant-issued capability token that lets a visitor
// outside the workspace read one knowledge entry through a public,
// unauthenticated endpoint. The classification of the source document is
// the hard ceiling: confidential can never be shared, secret can never be
// shared with download permission (ShareLinkService enforces both).
//
// Token generation规范 (v1): crypto/rand 32 bytes → base64.RawURLEncoding
// (RFC 4648 §5, unpadded) → a 43-character URL-safe string. The token is
// the visitor's only credential, so it must never be derivable from the
// row id or the knowledge id, and it is rendered once on creation; the
// management list also returns it because v1 has no "reveal once" store.
// The varchar(64) column leaves headroom for a future longer format.
//
// Audit actions: share.created / share.revoked / share.visited (constants
// at the bottom of this file) — every issuance, revocation and resolved
// visit leaves an audit_logs row.

// ShareLinkType is the audience of a share link.
type ShareLinkType string

const (
	// ShareLinkTypeOrg 任意持有链接的组织内成员可访问。
	ShareLinkTypeOrg ShareLinkType = "org"
	// ShareLinkTypePeople 指定人员可见（v1 仅记录受众类型，不做逐人凭据）。
	ShareLinkTypePeople ShareLinkType = "people"
)

// ShareLinkPermission is what a resolved link allows the visitor to do.
type ShareLinkPermission string

const (
	// ShareLinkPermissionView 只读：标题/描述/分类/版本/生命周期投影。
	ShareLinkPermissionView ShareLinkPermission = "view"
	// ShareLinkPermissionDownload 在 view 之上额外给文件下载地址。
	ShareLinkPermissionDownload ShareLinkPermission = "download"
)

// ShareLinkApprovalStatus is the review state of the link row. v1 approves
// immediately; 'pending' is reserved for the A2 approval flow.
type ShareLinkApprovalStatus string

const (
	// ShareLinkApprovalApproved v1 直通：创建即生效。
	ShareLinkApprovalApproved ShareLinkApprovalStatus = "approved"
	// ShareLinkApprovalPending 预留：待审批（v1 不会写入该值）。
	ShareLinkApprovalPending ShareLinkApprovalStatus = "pending"
)

// ShareLinkVisitLogMaxEntries caps the visit_log JSONB array. The
// repository trims to the most recent entries on every append so a viral
// link cannot grow the row unboundedly.
const ShareLinkVisitLogMaxEntries = 100

// ShareLink is one issued external share link. Rows are never deleted;
// revocation flips RevokedAt so audit history stays reconstructible.
type ShareLink struct {
	// Unique identifier of the share link.
	ID string `json:"id" gorm:"type:varchar(36);primaryKey" extensions:"x-agent=share_link_id"`
	// TenantID is the owning workspace (the knowledge owner's tenant).
	TenantID uint64 `json:"tenant_id" gorm:"type:integer;not null;index:idx_share_links_tenant_knowledge,priority:1"`
	// KnowledgeID is the shared source document.
	KnowledgeID string `json:"knowledge_id" gorm:"type:varchar(36);not null;index:idx_share_links_tenant_knowledge,priority:2"`
	// Token is the visitor credential (see the generation规范 above).
	Token string `json:"token" gorm:"type:varchar(64);not null;uniqueIndex"`
	// LinkType is the audience (org | people).
	LinkType string `json:"link_type" gorm:"type:varchar(16);not null"`
	// Permission is the capability (view | download).
	Permission string `json:"permission" gorm:"type:varchar(16);not null;default:'view'"`
	// ExpiresAt is the link expiry; nil means the link lives until revoked.
	ExpiresAt *time.Time `json:"expires_at"`
	// RevokedAt is set when the link is revoked; nil means still active.
	RevokedAt *time.Time `json:"revoked_at"`
	// CreatedBy is the authenticated user that issued the link.
	CreatedBy string `json:"created_by" gorm:"type:varchar(36);not null"`
	// ApprovalStatus is 'approved' in v1 (直通); 'pending' is reserved.
	ApprovalStatus string `json:"approval_status" gorm:"type:varchar(16);not null;default:'approved'"`
	// VisitCount is the total number of resolved visits (never trimmed).
	VisitCount int64 `json:"visit_count" gorm:"type:bigint;not null;default:0"`
	// VisitLog holds the most recent ShareLinkVisit entries (newest first,
	// capped at ShareLinkVisitLogMaxEntries).
	VisitLog JSON `json:"visit_log" gorm:"type:jsonb;not null;default:'[]'"`
	// Creation time.
	CreatedAt time.Time `json:"created_at"`
	// Last update time.
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName pins the GORM table name.
func (ShareLink) TableName() string { return "share_links" }

// Active reports whether the link is currently resolvable ignoring the
// classification/knowledge checks: not revoked and not past its expiry.
func (l *ShareLink) Active(now time.Time) bool {
	if l == nil || l.RevokedAt != nil {
		return false
	}
	if l.ExpiresAt != nil && now.After(*l.ExpiresAt) {
		return false
	}
	return true
}

// ShareLinkVisitorMeta describes the visitor of one resolve call. The IP
// and User-Agent are persisted into visit_log for the audit trail; they
// are never rendered back as tenant-internal data.
type ShareLinkVisitorMeta struct {
	IP        string `json:"ip"`
	UserAgent string `json:"ua"`
}

// ShareLinkVisit is one persisted visit_log entry ({at, ip, ua}).
type ShareLinkVisit struct {
	At time.Time `json:"at"`
	IP string    `json:"ip"`
	UA string    `json:"ua"`
}

// ShareLinkPublicMeta is the share-side metadata returned by Resolve. The
// visitor watermark (who is viewing, when) is rendered by the frontend
// from these fields — the backend deliberately returns token + visited_at
// plus the link's own knobs and nothing else.
type ShareLinkPublicMeta struct {
	// Token echoes the resolved credential so the page can render the
	// visitor watermark without re-parsing its own URL.
	Token string `json:"token"`
	// LinkType is the audience (org | people).
	LinkType string `json:"link_type"`
	// Permission is the capability granted (view | download).
	Permission string `json:"permission"`
	// ExpiresAt mirrors the row; nil = valid until revoked.
	ExpiresAt *time.Time `json:"expires_at"`
	// VisitedAt is the timestamp of this very visit.
	VisitedAt time.Time `json:"visited_at"`
}

// ShareLinkKnowledgeView is the read-only knowledge projection returned to
// an anonymous visitor. It is built field-by-field (never serialised from
// types.Knowledge) so tenant-internal columns — TenantID, FilePath,
// Metadata, EmbeddingModelID, parse bookkeeping — can never leak through a
// public response.
type ShareLinkKnowledgeView struct {
	// ID of the knowledge entry.
	ID string `json:"id"`
	// Title of the document.
	Title string `json:"title"`
	// Description of the document.
	Description string `json:"description"`
	// Classification is the sensitivity level (public/internal/secret).
	Classification string `json:"classification"`
	// CurrentVersion is the latest published version number.
	CurrentVersion int `json:"current_version"`
	// LifecycleStatus is the stored lifecycle state.
	LifecycleStatus string `json:"lifecycle_status"`
	// EffectiveStatus is the read-time resolved behaviour (e.g. a valid row
	// past its expires_at reads as expired here too).
	EffectiveStatus string `json:"effective_status"`
	// LifecycleReasons carries the machine-readable signals behind
	// EffectiveStatus (mirrors KnowledgeLifecycleResolved.Reasons).
	LifecycleReasons []string `json:"lifecycle_reasons,omitempty"`
	// FileName / FileType / FileSize describe the source file.
	FileName string `json:"file_name,omitempty"`
	FileType string `json:"file_type,omitempty"`
	FileSize int64  `json:"file_size,omitempty"`
	// DownloadURL is the presigned file address. Present only when the
	// link's permission is download AND the deployment can sign URLs; it is
	// a relative URL (the visitor's origin is the prefix).
	DownloadURL string `json:"download_url,omitempty"`
	// DownloadAvailable distinguishes "no permission" from "signing not
	// configured": true whenever permission=download and a URL was minted.
	DownloadAvailable bool `json:"download_available,omitempty"`
}

// ShareLinkResolution is the public resolve payload: share metadata plus
// the knowledge read-only projection.
type ShareLinkResolution struct {
	Share     ShareLinkPublicMeta    `json:"share"`
	Knowledge ShareLinkKnowledgeView `json:"knowledge"`
}

// Share-link audit actions (§2.5: issuance, revocation and every resolved
// visit are auditable). Defined here rather than audit_log.go so the
// share-link vertical stays self-contained; they join the same dot-
// namespaced AuditAction space.
const (
	// AuditActionShareLinkCreated fires when a link is issued.
	AuditActionShareLinkCreated AuditAction = "share.created"
	// AuditActionShareLinkRevoked fires when a link is revoked.
	AuditActionShareLinkRevoked AuditAction = "share.revoked"
	// AuditActionShareLinkVisited fires on every public resolve. Details
	// carry {ip, ua}; there is no actor (the visitor is anonymous).
	AuditActionShareLinkVisited AuditAction = "share.visited"
)
