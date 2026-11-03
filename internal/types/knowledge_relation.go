package types

import (
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ============================================================================
// 知识关联（KnowAxis V3.0 §4.5）。
//
// 文档与文档之间的关系此前无处表达：图谱事实连接的是实体，wiki 反链连接
// 的是 wiki 页面。于是"这条 SOP 依据那份制度""新版替代旧版""两份文件结论
// 冲突"只能写在正文里，检索、影响分析和删除检查都读不到。
//
// 关系双向存储：插入 A→B 时同时写一条反向镜像，让"谁引用了当前页面"变成
// 一次索引查询。镜像共享同一个 pair_key，删除成对进行，不会出现单向残留。
// ============================================================================

// KnowledgeRelationType is the semantic kind of a knowledge-to-knowledge edge.
type KnowledgeRelationType string

const (
	// RelationBasedOn 依据：本条目的结论建立在目标之上。
	RelationBasedOn KnowledgeRelationType = "based_on"
	// RelationReplaces 替代：本条目取代目标（新版本）。
	RelationReplaces KnowledgeRelationType = "replaces"
	// RelationSupplements 补充：目标之外还有本条目承载的内容。
	RelationSupplements KnowledgeRelationType = "supplements"
	// RelationConflicts 冲突：两者结论不一致，不能同时作为权威。
	RelationConflicts KnowledgeRelationType = "conflicts"
	// RelationPrerequisite 前置：阅读/执行本条目之前需要先看目标。
	RelationPrerequisite KnowledgeRelationType = "prerequisite"
	// RelationResponsible 责任：目标描述责任方，本条目描述其义务。
	RelationResponsible KnowledgeRelationType = "responsible"
	// RelationRelated 一般相关（AI 推荐默认落在这里）。
	RelationRelated KnowledgeRelationType = "related"
)

// allRelationTypes is the whitelist in declaration order.
var allRelationTypes = []KnowledgeRelationType{
	RelationBasedOn, RelationReplaces, RelationSupplements, RelationConflicts,
	RelationPrerequisite, RelationResponsible, RelationRelated,
}

// Valid reports whether the relation type is known.
func (t KnowledgeRelationType) Valid() bool {
	for _, candidate := range allRelationTypes {
		if candidate == t {
			return true
		}
	}
	return false
}

// Symmetric reports whether the reverse direction means the same thing.
//
// 冲突是唯一严格对称的关系：A 与 B 冲突，则 B 与 A 冲突。其余都是非对称的
// ——"A 依据 B"的反向是"B 被 A 依据"，语义不同，所以反向行的类型必须由
// Inverse 决定，而不是简单复用原类型。
func (t KnowledgeRelationType) Symmetric() bool {
	return t == RelationConflicts || t == RelationRelated
}

// Inverse returns the relation type to record on the reverse row.
//
// 由于平台只存七种类型的正向语义，"被依据/被替代"没有独立枚举，反向行沿用
// 同一类型但 direction=reverse：查询方按方向决定怎么读（"目标是依据" vs
// "本条目被引用"）。这样既不需要枚举翻倍，也不会把含义读反。
func (t KnowledgeRelationType) Inverse() KnowledgeRelationType {
	return t
}

// KnowledgeRelationOrigin names who created the edge.
type KnowledgeRelationOrigin string

const (
	// RelationOriginManual 人工关联。
	RelationOriginManual KnowledgeRelationOrigin = "manual"
	// RelationOriginAISuggested AI 推荐（§4.5 标注"AI 推荐"）。
	RelationOriginAISuggested KnowledgeRelationOrigin = "ai_suggested"
	// RelationOriginMigration 迁移带入。
	RelationOriginMigration KnowledgeRelationOrigin = "migration"
)

// Valid reports whether the origin is known.
func (o KnowledgeRelationOrigin) Valid() bool {
	switch o {
	case RelationOriginManual, RelationOriginAISuggested, RelationOriginMigration:
		return true
	default:
		return false
	}
}

// KnowledgeRelation is one directed knowledge-to-knowledge edge.
type KnowledgeRelation struct {
	ID       string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID uint64 `json:"tenant_id" gorm:"not null"`
	// FromKnowledgeID / ToKnowledgeID give the direction.
	FromKnowledgeID string `json:"from_knowledge_id" gorm:"type:varchar(36);not null"`
	ToKnowledgeID   string `json:"to_knowledge_id" gorm:"type:varchar(36);not null"`
	// RelationType is the semantic kind.
	RelationType string `json:"relation_type" gorm:"type:varchar(24);not null;default:'related'"`
	// PairKey groups the forward row with its reverse mirror.
	PairKey string `json:"pair_key" gorm:"type:varchar(120);not null"`
	// Direction is forward for the authored edge and reverse for the mirror.
	Direction string `json:"direction" gorm:"type:varchar(8);not null;default:'forward'"`
	// Origin records whether a human or the model proposed the edge.
	Origin string `json:"origin" gorm:"type:varchar(16);not null;default:'manual'"`
	// Note explains why the two are related.
	Note      string `json:"note" gorm:"type:varchar(512);not null;default:''"`
	CreatedBy string `json:"created_by" gorm:"type:varchar(36);not null;default:''"`
	// CreatedAt is indexed with the pair for stable ordering.
	CreatedAt time.Time `json:"created_at"`
}

// TableName pins the GORM table name.
func (KnowledgeRelation) TableName() string { return "knowledge_relations" }

// BeforeCreate assigns an ID and the pair key before persistence.
func (r *KnowledgeRelation) BeforeCreate(_ *gorm.DB) error {
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	if r.PairKey == "" {
		r.PairKey = KnowledgeRelationPairKey(r.FromKnowledgeID, r.ToKnowledgeID, r.RelationType)
	}
	return nil
}

// KnowledgeRelationPairKey builds the canonical key for one undirected pair.
//
// 排序后拼接，使 A→B 与 B→A 得到同一个键：这正是"删除成对进行"的依据，
// 也是唯一性约束能在两个方向上都生效的原因。
func KnowledgeRelationPairKey(fromID, toID, relationType string) string {
	first, second := fromID, toID
	if first > second {
		first, second = second, first
	}
	return first + ":" + second + ":" + relationType
}

// IsReverse reports whether this row is the generated mirror.
func (r *KnowledgeRelation) IsReverse() bool {
	return r != nil && r.Direction == "reverse"
}

// KnowledgeRelationView is one edge as the reading page renders it: the peer
// document plus how it is related, in the reader's direction.
type KnowledgeRelationView struct {
	// Relation is the stored edge.
	Relation *KnowledgeRelation `json:"relation"`
	// PeerID is the other end of the edge.
	PeerID string `json:"peer_id"`
	// RelationType is what the relation means from the current document.
	RelationType string `json:"relation_type"`
	// Outbound is true when the current document is the source of the edge.
	Outbound bool `json:"outbound"`
	// Origin distinguishes a human edge from an AI suggestion.
	Origin string `json:"origin"`
	// Note carries the author's explanation.
	Note string `json:"note"`
}

// BuildRelationViews turns stored edges into reader-facing views.
//
// 反向镜像的展示语义（§4.5 "谁引用了当前页面"）：如果当前文档是反向行的
// from，说明是别人指向它——此时 outbound=false，前端渲染为"被引用"。
func BuildRelationViews(
	currentID string, relations []*KnowledgeRelation,
) []KnowledgeRelationView {
	views := make([]KnowledgeRelationView, 0, len(relations))
	for _, relation := range relations {
		if relation == nil {
			continue
		}
		outbound := relation.FromKnowledgeID == currentID
		peerID := relation.ToKnowledgeID
		if !outbound {
			peerID = relation.FromKnowledgeID
		}
		views = append(views, KnowledgeRelationView{
			Relation:     relation,
			PeerID:       peerID,
			RelationType: relation.RelationType,
			Outbound:     outbound,
			Origin:       relation.Origin,
			Note:         relation.Note,
		})
	}
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].RelationType != views[j].RelationType {
			return views[i].RelationType < views[j].RelationType
		}
		return views[i].PeerID < views[j].PeerID
	})
	return views
}

// NormalizeRelationNote trims and bounds a note.
func NormalizeRelationNote(note string) string {
	trimmed := strings.TrimSpace(note)
	if runes := []rune(trimmed); len(runes) > 512 {
		return string(runes[:512])
	}
	return trimmed
}
