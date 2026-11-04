package types

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 订阅（KnowAxis V3.0 §15.3 我的关注 / §5 内容协作）。
//
// §15.3 要求"我的关注"：订阅空间、目录、单篇知识、标签或作者，有新内容或
// 变更时收到摘要。此前没有订阅表，读者只能靠搜索发现更新。
//
// 订阅只描述"我想知道什么变化"，不参与权限判定：订阅不会让人获得访问权，
// 摘要生成时逐条过 ACL；无权的条目直接不进摘要（而不是显示标题）。
// ============================================================================

// SubscriptionSubjectType is what a subscription points at.
type SubscriptionSubjectType string

const (
	// SubscriptionSubjectSpace 整个知识空间。
	SubscriptionSubjectSpace SubscriptionSubjectType = "space"
	// SubscriptionSubjectFolder 一个目录（含子目录）。
	SubscriptionSubjectFolder SubscriptionSubjectType = "folder"
	// SubscriptionSubjectKnowledge 单篇知识的后续版本。
	SubscriptionSubjectKnowledge SubscriptionSubjectType = "knowledge"
	// SubscriptionSubjectTag 一个标签下的新增/变更。
	SubscriptionSubjectTag SubscriptionSubjectType = "tag"
	// SubscriptionSubjectAuthor 一位作者发布的内容。
	SubscriptionSubjectAuthor SubscriptionSubjectType = "author"
)

// Valid reports whether the subject type is known.
func (t SubscriptionSubjectType) Valid() bool {
	switch t {
	case SubscriptionSubjectSpace, SubscriptionSubjectFolder, SubscriptionSubjectKnowledge,
		SubscriptionSubjectTag, SubscriptionSubjectAuthor:
		return true
	default:
		return false
	}
}

// Subscription is one (user, subject) follow.
type Subscription struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null;index:idx_subscriptions_user,priority:1"`
	UserID   string `json:"user_id" gorm:"type:varchar(36);not null;index:idx_subscriptions_user,priority:2"`
	// SubjectType / SubjectID identify what is followed.
	SubjectType string `json:"subject_type" gorm:"type:varchar(16);not null;index:idx_subscriptions_user,priority:3"`
	SubjectID   string `json:"subject_id" gorm:"type:varchar(191);not null;index:idx_subscriptions_user,priority:4"`
	// SpaceID is the enclosing knowledge base for tag/folder/knowledge
	// subjects, so digest queries can scope by space first.
	SpaceID string `json:"space_id" gorm:"type:varchar(36);not null;default:''"`
	// Label is a display name captured at subscribe time (a tag or author
	// label). It is not authoritative — the digest resolves current names.
	Label string `json:"label" gorm:"type:varchar(191);not null;default:''"`
	// Enabled lets a user pause one subscription without deleting it.
	Enabled bool `json:"enabled" gorm:"not null"`
	// LastDigestAt is when the user last read a digest containing this
	// subscription; the digest reports changes after it.
	LastDigestAt *time.Time `json:"last_digest_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// TableName pins the GORM table name.
func (Subscription) TableName() string { return "subscriptions" }

// BeforeCreate assigns an ID before persistence.
func (s *Subscription) BeforeCreate(_ *gorm.DB) error {
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	return nil
}

// NormalizeSubscriptionSubject validates one subject pair.
//
// folder 主体把"空间 + 路径"折进 SubjectID（spaceID + "\x00" + path），这样
// 唯一键与去重都不需要额外列；author 主体是用户 ID；tag 主体是标签名。
func NormalizeSubscriptionSubject(
	subjectType SubscriptionSubjectType, subjectID, spaceID string,
) (normalizedID, normalizedSpace string, err error) {
	subjectID = strings.TrimSpace(subjectID)
	spaceID = strings.TrimSpace(spaceID)
	if !subjectType.Valid() {
		return "", "", fmt.Errorf("unknown subscription subject type %q", subjectType)
	}
	switch subjectType {
	case SubscriptionSubjectSpace:
		// 空间订阅的主体就是空间本身，subject_id 与 space_id 必须一致。
		if spaceID == "" {
			spaceID = subjectID
		}
		if spaceID == "" {
			return "", "", fmt.Errorf("space_id is required for a space subscription")
		}
		if subjectID != "" && subjectID != spaceID {
			return "", "", fmt.Errorf("subject_id and space_id disagree")
		}
		return spaceID, spaceID, nil
	case SubscriptionSubjectFolder:
		if spaceID == "" {
			return "", "", fmt.Errorf("space_id is required for a folder subscription")
		}
		path := NormalizeKnowledgeFolderPath(subjectID)
		return spaceID + "\x00" + path, spaceID, nil
	case SubscriptionSubjectKnowledge:
		if subjectID == "" {
			return "", "", fmt.Errorf("subject_id is required")
		}
		return subjectID, spaceID, nil
	case SubscriptionSubjectTag:
		if subjectID == "" {
			return "", "", fmt.Errorf("subject_id is required")
		}
		return subjectID, spaceID, nil
	case SubscriptionSubjectAuthor:
		if subjectID == "" {
			return "", "", fmt.Errorf("subject_id is required")
		}
		return subjectID, spaceID, nil
	}
	return "", "", fmt.Errorf("unknown subscription subject type")
}

// SubscriptionInput is the create payload.
type SubscriptionInput struct {
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id"`
	SpaceID     string `json:"space_id"`
	Label       string `json:"label"`
}

// DigestItem is one changed document in a digest.
type DigestItem struct {
	// SubscriptionID is the subscription that produced this row.
	SubscriptionID string `json:"subscription_id"`
	// SubscriptionLabel is the subscription's display label.
	SubscriptionLabel string `json:"subscription_label"`
	// SubjectType / SubjectID point back at the followed subject.
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id"`
	// KnowledgeID / KnowledgeBaseID / Title describe the changed document.
	KnowledgeID     string `json:"knowledge_id"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
	Title           string `json:"title"`
	// ChangeKind is "new" or "updated".
	ChangeKind string `json:"change_kind"`
	// LifecycleStatus lets the reader spot pending-review entries.
	LifecycleStatus string    `json:"lifecycle_status"`
	ChangedAt       time.Time `json:"changed_at"`
}

// Digest is the §15.3 follow feed.
type Digest struct {
	// GeneratedAt is when the digest was computed.
	GeneratedAt time.Time `json:"generated_at"`
	// Since is the window start the digest covers.
	Since time.Time `json:"since"`
	// Items are permission-filtered changes, newest first.
	Items []DigestItem `json:"items"`
	// HiddenCount is how many changes were withheld for permission reasons.
	// It is an aggregate for the caller only and never names anything.
	HiddenCount int `json:"hidden_count"`
	// Truncated reports that the window held more items than the limit.
	Truncated bool `json:"truncated"`
}

// DigestMaxItems bounds one digest.
const DigestMaxItems = 100

// FolderPathWithin reports whether folderPath is the given folder or one of
// its descendants (KnowAxis V3.0 §4 folder tree / §2.3 目录继承).
func FolderPathWithin(candidate, folderPath string) bool {
	candidate = NormalizeKnowledgeFolderPath(candidate)
	folderPath = NormalizeKnowledgeFolderPath(folderPath)
	if folderPath == "" {
		return true
	}
	if candidate == folderPath {
		return true
	}
	return strings.HasPrefix(candidate, folderPath+"/")
}
