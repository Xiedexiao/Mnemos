package types

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 知识评论（KnowAxis V3.0 §5.2 的 v1 子集）。
//
// knowledge_comments 是知识条目上的协作讨论：一条正文 + 可选的划词锚点
// （anchor，{chunk_id, start_offset, end_offset, quote}；空 = 全局评论）+
// 一层回复（parent_id）。resolved/resolved_by 记录评审收口；删除是软删
// （gorm.DeletedAt），树形列表剔除、审计可回溯。
// ============================================================================

// KnowledgeCommentAnchor is the text-selection anchor of a comment
// (KnowAxis V3.0 §5.2). All fields optional; a nil anchor on the comment
// means "global comment" (not pinned to any passage).
type KnowledgeCommentAnchor struct {
	// ChunkID pins the comment to one chunk of the knowledge (empty = document level).
	ChunkID string `json:"chunk_id,omitempty"`
	// StartOffset is the quote's start offset inside the chunk text (runes).
	StartOffset int `json:"start_offset"`
	// EndOffset is the quote's end offset (exclusive; must be >= StartOffset).
	EndOffset int `json:"end_offset"`
	// Quote is the selected text the comment refers to.
	Quote string `json:"quote,omitempty"`
}

// Validate reports whether the offsets are coherent.
func (a *KnowledgeCommentAnchor) Validate() error {
	if a == nil {
		return nil
	}
	if a.StartOffset < 0 || a.EndOffset < 0 {
		return fmt.Errorf("anchor offsets must be non-negative")
	}
	if a.EndOffset < a.StartOffset {
		return fmt.Errorf("anchor end_offset (%d) is before start_offset (%d)", a.EndOffset, a.StartOffset)
	}
	return nil
}

// KnowledgeComment is one comment (or reply) on a knowledge entry.
type KnowledgeComment struct {
	// ID of the comment.
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// TenantID owning the comment (the knowledge's tenant).
	TenantID uint64 `json:"tenant_id" gorm:"not null"`
	// KnowledgeID is the commented knowledge entry.
	KnowledgeID string `json:"knowledge_id" gorm:"type:varchar(36);not null"`
	// ParentID links a reply to its top-level comment ('' = top level).
	// One level of nesting only: a reply's parent must itself be top level.
	ParentID string `json:"parent_id" gorm:"type:varchar(36);not null;default:''"`
	// AuthorID is the comment author (audit identity).
	AuthorID string `json:"author_id" gorm:"type:varchar(36);not null"`
	// Body is the comment text.
	Body string `json:"body" gorm:"not null"`
	// Anchor is the JSONB text-selection anchor; nil = global comment.
	// Marshalled with SetAnchor (json.Marshal), never inline structs.
	Anchor JSON `json:"anchor" gorm:"type:jsonb"`
	// Mentions holds the resolved @ mentions (§5.1). Only mentions the server
	// validated (existing, same workspace, document visible to the author)
	// are stored here; everything else is dropped before the insert.
	Mentions JSON `json:"mentions" gorm:"type:jsonb"`
	// Resolved marks the discussion as settled.
	Resolved bool `json:"resolved" gorm:"not null;default:false"`
	// ResolvedBy records who resolved the comment.
	ResolvedBy string `json:"resolved_by" gorm:"type:varchar(36);not null;default:''"`
	// CreatedAt of the comment.
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt of the comment (touched by GORM on Updates).
	UpdatedAt time.Time `json:"updated_at"`
	// DeletedAt soft-delete marker: listed threads skip deleted rows, the
	// row itself stays for audit.
	DeletedAt gorm.DeletedAt `json:"deleted_at"`
}

// SetAnchor marshals the typed anchor into the JSONB payload. Passing nil
// clears the anchor (global comment).
func (c *KnowledgeComment) SetAnchor(anchor *KnowledgeCommentAnchor) error {
	if anchor == nil {
		c.Anchor = nil
		return nil
	}
	if err := anchor.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(anchor)
	if err != nil {
		return err
	}
	c.Anchor = JSON(data)
	return nil
}

// AnchorDetails decodes the JSONB payload back into the typed anchor.
// Returns (nil, nil) for a global comment.
func (c *KnowledgeComment) AnchorDetails() (*KnowledgeCommentAnchor, error) {
	if len(c.Anchor) == 0 {
		return nil, nil
	}
	var anchor KnowledgeCommentAnchor
	if err := json.Unmarshal(c.Anchor, &anchor); err != nil {
		return nil, err
	}
	return &anchor, nil
}

// TableName pins the GORM table name.
func (KnowledgeComment) TableName() string { return "knowledge_comments" }

// BeforeCreate assigns an ID before persistence.
func (c *KnowledgeComment) BeforeCreate(_ *gorm.DB) error {
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	return nil
}

// KnowledgeCommentThread is one top-level comment plus its direct replies
// (one level of nesting, V3.0 §5.2). The comment fields are inlined by
// json.Marshal (embedded struct); this type is never persisted.
type KnowledgeCommentThread struct {
	KnowledgeComment
	// Replies are the direct children in chronological order. Replies whose
	// parent was soft-deleted are promoted to top level so no surviving
	// content drops out of the thread view.
	Replies []*KnowledgeCommentThread `json:"replies"`
}

// CommentMentions decodes the stored mention list, tolerating a bad payload.
func (c *KnowledgeComment) CommentMentions() []Mention {
	if c == nil || len(c.Mentions) == 0 {
		return nil
	}
	var mentions []Mention
	if err := json.Unmarshal(c.Mentions, &mentions); err != nil {
		return nil
	}
	return mentions
}

// SetMentions marshals the mention list into the JSONB payload.
func (c *KnowledgeComment) SetMentions(mentions []Mention) error {
	if len(mentions) == 0 {
		c.Mentions = JSON("[]")
		return nil
	}
	data, err := json.Marshal(mentions)
	if err != nil {
		return err
	}
	c.Mentions = JSON(data)
	return nil
}
