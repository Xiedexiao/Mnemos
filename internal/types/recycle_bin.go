package types

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 回收站（KnowAxis V3.0 §5.5 的 v1 子集）。
//
// recycle_bin 是软删实体的登记簿：知识删除管线把行软删（Knowledge 的
// gorm.DeletedAt）后在这里登记一条 (entity_type, entity_id) 记录并打上
// purge_at = 删除时刻 + 30 天。恢复（RecycleBinService.Restore）把实体行
// 的 deleted_at 清空并盖 restored_at；超期未恢复的条目由 PurgeDue 物理
// 删除（v1 只提供方法，定时任务后续接入）。
// ============================================================================

// RecycleBinRetentionDays is the purge window: an entry becomes eligible for
// physical deletion purge_at = deleted_at + 30 days.
const RecycleBinRetentionDays = 30

// RecycleEntityType enumerates the entity kinds the recycle bin tracks
// (v1: knowledge only).
type RecycleEntityType string

const (
	// RecycleEntityTypeKnowledge marks a soft-deleted knowledge entry.
	RecycleEntityTypeKnowledge RecycleEntityType = "knowledge"
)

// Valid reports whether the entity type is known to this build.
func (t RecycleEntityType) Valid() bool {
	switch t {
	case RecycleEntityTypeKnowledge:
		return true
	default:
		return false
	}
}

// String renders the entity type for logs.
func (t RecycleEntityType) String() string { return string(t) }

// RecycleBinEntry is one soft-deleted entity awaiting restore or purge.
type RecycleBinEntry struct {
	// ID of the recycle-bin row itself.
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// TenantID owning the entry (and the deleted entity).
	TenantID uint64 `json:"tenant_id" gorm:"not null"`
	// EntityType is the deleted entity kind (v1: "knowledge").
	EntityType string `json:"entity_type" gorm:"type:varchar(16);not null"`
	// EntityID is the soft-deleted entity's ID (UNIQUE with EntityType;
	// re-deleting an entity upserts this row and restarts the 30-day window).
	EntityID string `json:"entity_id" gorm:"type:varchar(36);not null"`
	// Title is the display title captured at delete time (empty falls back
	// to file name upstream; truncated to 255 runes to fit the column).
	Title string `json:"title" gorm:"type:varchar(255);default:''"`
	// DeletedBy records who deleted the entity (audit identity; empty for
	// system-driven cleanup tasks).
	DeletedBy string `json:"deleted_by" gorm:"type:varchar(36);not null;default:''"`
	// PurgeAt is the hard-delete deadline (created_at + 30 days). PurgeDue
	// physically removes entities at or before this instant.
	PurgeAt time.Time `json:"purge_at" gorm:"not null"`
	// RestoredAt is set when the entity was restored; restored entries are
	// excluded from PurgeDue (restoring must not put a live entity on the
	// hard-delete path).
	RestoredAt *time.Time `json:"restored_at"`
	// CreatedAt is when the entity was soft-deleted and enqueued.
	CreatedAt time.Time `json:"created_at"`
}

// TableName pins the GORM table name.
func (RecycleBinEntry) TableName() string { return "recycle_bin" }

// BeforeCreate assigns an ID before persistence.
func (e *RecycleBinEntry) BeforeCreate(_ *gorm.DB) error {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	return nil
}

// IsRestored reports whether the entry has already been restored.
func (e *RecycleBinEntry) IsRestored() bool {
	return e != nil && e.RestoredAt != nil
}

// IsDue reports whether the entry is past its purge deadline and not restored.
func (e *RecycleBinEntry) IsDue(now time.Time) bool {
	return e != nil && !e.IsRestored() && !e.PurgeAt.After(now)
}
