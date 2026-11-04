package types

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// @提及与草稿自动保存（KnowAxis V3.0 §5.1 内容协作 / §5.4 草稿）。
//
// §5.1 要求"@提及（人/知识/专家）与通知"：此前评论只能写正文，被提到的人
// 不会收到任何东西，写 `@张三` 纯属装饰。
// §5.4 要求"草稿自动保存与冲突合并"：此前编辑一篇知识时刷新页面内容就没了
// ——知识本身没有草稿态，评论也没有。
//
// 两个关键决定：
//  1. 提及解析在服务端做，用**结构化 ID**而不是昵称匹配。`@张三` 到底是
//     哪个张三、他还在不在这个工作区、他有没有权看这篇文档——这些只能由
//     服务端回答。前端传 `mentions: [{type,id}]` 或正文里的 `@[label](id)`
//     形式，服务端校验后落库并通知。
//  2. 草稿是**独立的一行**，不与正式内容共用版本号：草稿不进入检索、不
//     参与审批、也不会因为自动保存而产生版本噪音。冲突合并用乐观锁版本
//     (revision)：保存时带上自己读到的 revision，不匹配就 409 并把服务端
//     当前内容一并返回，让前端做合并而不是静默覆盖。
// ============================================================================

// MentionType is what an @ mention points at.
type MentionType string

const (
	// MentionTypeUser 提到一位用户。
	MentionTypeUser MentionType = "user"
	// MentionTypeKnowledge 提到一篇知识（"见 @E45 换型 SOP"）。
	MentionTypeKnowledge MentionType = "knowledge"
	// MentionTypeExpert 提到一位专家（社区专家身份）。
	MentionTypeExpert MentionType = "expert"
)

// Valid reports whether the mention type is known.
func (t MentionType) Valid() bool {
	switch t {
	case MentionTypeUser, MentionTypeKnowledge, MentionTypeExpert:
		return true
	default:
		return false
	}
}

// Mention is one resolved @ mention.
type Mention struct {
	// Type is user / knowledge / expert.
	Type MentionType `json:"type"`
	// ID is the referenced entity.
	ID string `json:"id"`
	// Label is the text the author typed (display only).
	Label string `json:"label,omitempty"`
}

// mentionInlinePattern matches the inline form @[label](id).
//
// 这是唯一被接受的正文内联写法：`@张三` 这种纯文本无法可靠解析（重名、
// 改名、跨工作区同名），所以只解析显式的 `@[显示名](id)`，其它 `@` 一律
// 当普通文本。宁可少解析也不猜错人。
var mentionInlinePattern = regexp.MustCompile(`@\[([^\]]{0,64})\]\(([A-Za-z0-9:_\-]{1,64})\)`)

// mentionPrefixes labels the id space so a bare id cannot be mistaken for
// another type. knowledge:<id> / user:<id> / expert:<id>.
const (
	mentionPrefixUser      = "user:"
	mentionPrefixKnowledge = "knowledge:"
	mentionPrefixExpert    = "expert:"
)

// ParseInlineMentions extracts `@[label](type:id)` occurrences.
//
// 同时接受 `@[label](user:u-1)` 与 `@[label](u-1)`（后者按 user 处理）：
// 前者是推荐写法，后者兼容只写 id 的客户端。返回值已去重并保持出现顺序。
func ParseInlineMentions(body string) []Mention {
	matches := mentionInlinePattern.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]Mention, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		label := strings.TrimSpace(match[1])
		raw := strings.TrimSpace(match[2])
		mentionType := MentionTypeUser
		id := raw
		switch {
		case strings.HasPrefix(raw, mentionPrefixUser):
			id = strings.TrimPrefix(raw, mentionPrefixUser)
		case strings.HasPrefix(raw, mentionPrefixKnowledge):
			mentionType = MentionTypeKnowledge
			id = strings.TrimPrefix(raw, mentionPrefixKnowledge)
		case strings.HasPrefix(raw, mentionPrefixExpert):
			mentionType = MentionTypeExpert
			id = strings.TrimPrefix(raw, mentionPrefixExpert)
		}
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		key := string(mentionType) + "\x00" + id
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, Mention{Type: mentionType, ID: id, Label: label})
	}
	return out
}

// NormalizeMentions trims, validates, dedupes and caps a mention list.
func NormalizeMentions(mentions []Mention) ([]Mention, error) {
	out := make([]Mention, 0, len(mentions))
	seen := make(map[string]struct{}, len(mentions))
	for _, mention := range mentions {
		mentionType := MentionType(strings.TrimSpace(string(mention.Type)))
		if mentionType == "" {
			mentionType = MentionTypeUser
		}
		if !mentionType.Valid() {
			return nil, fmt.Errorf("unknown mention type %q", mention.Type)
		}
		id := strings.TrimSpace(mention.ID)
		if id == "" {
			return nil, fmt.Errorf("mention id is required")
		}
		key := string(mentionType) + "\x00" + id
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, Mention{
			Type: mentionType, ID: id, Label: trimMentionLabel(mention.Label),
		})
	}
	// 一条评论提到几百人通常是粘贴事故，不是协作意图。
	const maxMentions = 50
	if len(out) > maxMentions {
		out = out[:maxMentions]
	}
	return out, nil
}

// trimMentionLabel caps the display label.
func trimMentionLabel(label string) string {
	trimmed := strings.TrimSpace(label)
	runes := []rune(trimmed)
	if len(runes) > 64 {
		return string(runes[:64])
	}
	return trimmed
}

// MergeMentions combines an explicit list with the ones parsed from the body.
func MergeMentions(explicit []Mention, body string) []Mention {
	combined := make([]Mention, 0, len(explicit)+4)
	combined = append(combined, explicit...)
	combined = append(combined, ParseInlineMentions(body)...)
	normalized, err := NormalizeMentions(combined)
	if err != nil {
		// 显式列表有坏值时仍然保留正文里能解析出的提及：评论本身已经成立，
		// 不该因为一个提及格式错误而失败。
		normalized, _ = NormalizeMentions(ParseInlineMentions(body))
	}
	return normalized
}

// SortMentions orders mentions for stable storage and diffing.
func SortMentions(mentions []Mention) {
	sort.SliceStable(mentions, func(i, j int) bool {
		if mentions[i].Type != mentions[j].Type {
			return mentions[i].Type < mentions[j].Type
		}
		return mentions[i].ID < mentions[j].ID
	})
}

// ============================================================================
// 草稿（§5.4）。
// ============================================================================

// DraftKind is what kind of content a draft holds.
type DraftKind string

const (
	// DraftKindKnowledge 知识正文草稿（新建或编辑既有知识）。
	DraftKindKnowledge DraftKind = "knowledge"
	// DraftKindComment 评论草稿。
	DraftKindComment DraftKind = "comment"
	// DraftKindAnswer 官方答案草稿。
	DraftKindAnswer DraftKind = "answer"
)

// Valid reports whether the draft kind is known.
func (k DraftKind) Valid() bool {
	switch k {
	case DraftKindKnowledge, DraftKindComment, DraftKindAnswer:
		return true
	default:
		return false
	}
}

// Draft is one autosaved draft.
//
// 每个 (user, kind, scope) 只有一行：自动保存是"覆盖当前草稿"，不是追加
// 历史。历史内容由正式版本承担，草稿刻意不参与版本号。
type Draft struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null;uniqueIndex:uq_drafts_owner,priority:1"`
	UserID   string `json:"user_id" gorm:"type:varchar(36);not null;uniqueIndex:uq_drafts_owner,priority:2"`
	Kind     string `json:"kind" gorm:"type:varchar(16);not null;uniqueIndex:uq_drafts_owner,priority:3"`
	// ScopeID identifies what the draft is for: a knowledge id, or the
	// knowledge a comment belongs to, or "new" for an unsaved entry.
	ScopeID string `json:"scope_id" gorm:"type:varchar(64);not null;default:'';uniqueIndex:uq_drafts_owner,priority:4"`
	// Title / Body are the draft payload.
	Title string `json:"title" gorm:"type:varchar(512);not null;default:''"`
	Body  string `json:"body" gorm:"type:text;not null;default:''"`
	// Anchor is the optional comment anchor.
	Anchor JSON `json:"anchor" gorm:"type:jsonb"`
	// Mentions are the resolved @ mentions of this draft.
	Mentions JSON `json:"mentions" gorm:"type:jsonb"`
	// Revision increments on every save; clients send the revision they read
	// and get a 409 when it moved (conflict detection, not last-write-wins).
	Revision int `json:"revision" gorm:"not null;default:1"`
	// UpdatedAt is the last autosave.
	UpdatedAt time.Time `json:"updated_at"`
	CreatedAt time.Time `json:"created_at"`
}

// TableName pins the GORM table name.
func (Draft) TableName() string { return "content_drafts" }

// BeforeCreate assigns an ID before persistence.
func (d *Draft) BeforeCreate(_ *gorm.DB) error {
	if d.ID == "" {
		d.ID = uuid.NewString()
	}
	if len(d.Mentions) == 0 {
		d.Mentions = JSON("[]")
	}
	return nil
}

// DraftBodyMaxRunes caps one draft body (wide enough for a long SOP).
const DraftBodyMaxRunes = 200_000

// DraftSaveInput is the autosave payload.
type DraftSaveInput struct {
	Kind     string    `json:"kind"`
	ScopeID  string    `json:"scope_id"`
	Title    string    `json:"title"`
	Body     string    `json:"body"`
	Anchor   JSON      `json:"anchor"`
	Mentions []Mention `json:"mentions"`
	// ExpectedRevision is the revision the client last read. 0 means "I have
	// no baseline" and is treated as a create-or-overwrite, which is what a
	// first autosave sends.
	ExpectedRevision int `json:"expected_revision"`
}

// DraftConflict carries both sides of a save conflict so the client can merge.
type DraftConflict struct {
	// Current is the stored draft the client did not see.
	Current *Draft `json:"current"`
	// Submitted is what the client tried to save.
	Submitted DraftSaveInput `json:"submitted"`
}

// ValidateDraftInput checks the shape shared by every save.
func ValidateDraftInput(input DraftSaveInput) error {
	kind := DraftKind(strings.TrimSpace(input.Kind))
	if !kind.Valid() {
		return fmt.Errorf("unknown draft kind %q", input.Kind)
	}
	if len([]rune(input.Body)) > DraftBodyMaxRunes {
		return fmt.Errorf("draft body is too long (max %d characters)", DraftBodyMaxRunes)
	}
	if len([]rune(input.Title)) > 512 {
		return fmt.Errorf("draft title is too long (max 512 characters)")
	}
	return nil
}

// SetMentions marshals the mention list into the JSONB payload.
func (d *Draft) SetMentions(mentions []Mention) error {
	if len(mentions) == 0 {
		d.Mentions = JSON("[]")
		return nil
	}
	data, err := json.Marshal(mentions)
	if err != nil {
		return err
	}
	d.Mentions = JSON(data)
	return nil
}

// DraftMentions decodes the stored mention list, tolerating a bad payload.
func (d *Draft) DraftMentions() []Mention {
	if d == nil || len(d.Mentions) == 0 {
		return nil
	}
	var mentions []Mention
	if err := json.Unmarshal(d.Mentions, &mentions); err != nil {
		return nil
	}
	return mentions
}
