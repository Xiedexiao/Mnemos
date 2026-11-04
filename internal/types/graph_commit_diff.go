package types

import (
	"sort"
	"time"
)

// ============================================================================
// 图谱变更集 diff 与回放（KnowAxis V3.0 §11.4 "GraphCommit 不可变；可重放、
// 可回归、可回滚"）。
//
// 平台此前只有两个端点：ListCommits（看到"某次提交加了几条"）和
// RollbackCommit（把整批标成 superseded）。中间缺的是**这次提交到底改了什么**
// ——计数不是内容，"加了 12 条实体"无法回答"哪 12 条、其中几条是覆盖已有的"。
//
// 三个决定：
//  1. diff 从**行上的 commit_id** 反查，而不是在 commit 行里存快照。行本身
//     记录了产出它的批次，再存一份快照会多出一个会与行状态不一致的真相。
//  2. 回放是**只读的时间旅行**：给定一个时间点，返回当时处于 active 状态的
//     实体与事实。它不修改任何数据，也不需要"重放"写入——这与回滚是两个
//     不同的操作（回滚改状态，回放只读历史）。
//  3. 回放要能回答"这条现在还在不在"，因此每行带 `SupersededBy` 或状态，
//     而不是只给一份"当时的列表"。否则读者无法区分"当时没有它"与"当时有、
//     后来被撤回"。
// ============================================================================

const (
	// GraphDiffDefaultLimit bounds one diff page.
	GraphDiffDefaultLimit = 200
	// GraphDiffMaxLimit caps it hard.
	GraphDiffMaxLimit = 1000
	// GraphReplayMaxEntities bounds a replay snapshot.
	GraphReplayMaxEntities = 2000
)

// GraphCommitDiff is one commit's contents (§11.4 "可回归").
type GraphCommitDiff struct {
	// CommitID the diff describes.
	CommitID string `json:"commit_id"`
	// Description / CreatedBy / CreatedAt echo the commit row.
	Description string    `json:"description"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
	// GoldenGatePass echoes whether the pre-apply regression assertion passed.
	GoldenGatePass bool `json:"golden_gate_pass"`
	// Entities / Facts are the rows this commit produced or last touched.
	Entities []GraphDiffEntity `json:"entities"`
	Facts    []GraphDiffFact   `json:"facts"`
	// Counts are the totals from the commit row, so a truncated list still
	// reports the real size.
	EntitiesAdded   int `json:"entities_added"`
	EntitiesUpdated int `json:"entities_updated"`
	FactsAdded      int `json:"facts_added"`
	FactsSuperseded int `json:"facts_superseded"`
	// Truncated reports that Entities/Facts were cut by the limit.
	Truncated bool `json:"truncated"`
}

// GraphDiffEntity is one entity row inside a diff.
type GraphDiffEntity struct {
	// EntityID / Name identify it.
	EntityID string `json:"entity_id"`
	Name     string `json:"name"`
	// SchemaID / Status / Confidence echo the row.
	SchemaID   string `json:"schema_id,omitempty"`
	Status     string `json:"status"`
	Confidence string `json:"confidence,omitempty"`
}

// GraphDiffFact is one fact row inside a diff.
type GraphDiffFact struct {
	// FactID identifies it.
	FactID string `json:"fact_id"`
	// RelationName / endpoints describe the edge.
	RelationName   string `json:"relation_name"`
	SourceEntityID string `json:"source_entity_id"`
	TargetEntityID string `json:"target_entity_id"`
	// Confidence / Status echo the row.
	Confidence string `json:"confidence"`
	Status     string `json:"status"`
}

// GraphReplayRequest asks for the graph as of one instant.
type GraphReplayRequest struct {
	// AsOf is the instant to reconstruct (UTC).
	AsOf time.Time
	// SchemaID optionally restricts to one schema.
	SchemaID string
	// Limit caps the snapshot size (0 uses GraphReplayMaxEntities).
	Limit int
}

// GraphReplayEntity is one entity in a replay snapshot, with its current fate.
type GraphReplayEntity struct {
	GraphDiffEntity
	// CurrentStatus is the row's status *now*, so a reader can tell "existed
	// then and still active" from "existed then, superseded since".
	CurrentStatus string `json:"current_status"`
	// CurrentCommitID is the last commit that touched it.
	CurrentCommitID string `json:"current_commit_id,omitempty"`
}

// GraphReplayResult is the reconstructed snapshot.
type GraphReplayResult struct {
	// AsOf echoes the requested instant.
	AsOf time.Time `json:"as_of"`
	// AsOfCommitID is the latest commit at or before AsOf ("" when none).
	AsOfCommitID string `json:"as_of_commit_id,omitempty"`
	// Entities / Facts that existed at AsOf.
	Entities []GraphReplayEntity `json:"entities"`
	Facts    []GraphDiffFact     `json:"facts"`
	// EntityCount / FactCount are the full totals before any limit.
	EntityCount int `json:"entity_count"`
	FactCount   int `json:"fact_count"`
	// Truncated reports that the snapshot was cut by the limit.
	Truncated bool `json:"truncated"`
	// Note explains a caveat (no commits before AsOf, limit applied).
	Note string `json:"note,omitempty"`
}

// NormalizeGraphReplayRequest clamps the request.
func NormalizeGraphReplayRequest(request GraphReplayRequest) GraphReplayRequest {
	if request.Limit <= 0 || request.Limit > GraphReplayMaxEntities {
		request.Limit = GraphReplayMaxEntities
	}
	return request
}

// SortGraphDiff orders a diff deterministically: entities by id, facts by id.
//
// 稳定顺序是"可回归"的前提：两次请求同一个 commit 必须得到同样的输出，
// 否则对比两份 diff 时会看到一堆并不存在的差异。
func SortGraphDiff(diff *GraphCommitDiff) {
	if diff == nil {
		return
	}
	sort.SliceStable(diff.Entities, func(i, j int) bool {
		return diff.Entities[i].EntityID < diff.Entities[j].EntityID
	})
	sort.SliceStable(diff.Facts, func(i, j int) bool {
		return diff.Facts[i].FactID < diff.Facts[j].FactID
	})
}
