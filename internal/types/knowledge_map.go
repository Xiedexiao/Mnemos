package types

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ============================================================================
// 空间知识地图（KnowAxis V3.0 §4.6 知识地图 + §15.5 岗位知识地图的数据底座）。
//
// §4.6 要求"知识地图：按空间、目录、主题三种视角看知识结构，而不是一个扁平
// 列表"。平台此前有关联图（§4.5）与目录树，但没有把它们聚合成一张"地图"：
// 读者看不到"这个空间有哪些知识簇、哪些是枢纽、哪些是孤岛"。
//
// 这份实现刻意不做可视化布局（坐标、力导向）——布局是前端的事，后端只回答
// "有哪些节点、哪些边、哪些组"。给出结构而不是像素，前端换渲染方式时后端
// 不用改。
//
// 三个口径：
//   - 目录树视角：按 folder_path 聚簇，是"东西放在哪"；
//   - 关系视角：按知识关联图的连通分量聚簇，是"东西怎么连"；
//   - 主题视角：按标签聚簇，是"东西讲什么"。
//
// 所有视角都只包含调用者有权查看且当前可搜索的文档（ACL + 生命周期），否则
// 地图会成为最方便的存在性泄露通道：一个孤岛的标题就暴露了它的存在。
// ============================================================================

// KnowledgeMapView is the requested aggregation.
type KnowledgeMapView string

const (
	// KnowledgeMapViewFolder 按目录树聚簇。
	KnowledgeMapViewFolder KnowledgeMapView = "folder"
	// KnowledgeMapViewRelation 按知识关联图的连通分量聚簇。
	KnowledgeMapViewRelation KnowledgeMapView = "relation"
	// KnowledgeMapViewTopic 按标签聚簇。
	KnowledgeMapViewTopic KnowledgeMapView = "topic"
)

// Valid reports whether the view is known.
func (v KnowledgeMapView) Valid() bool {
	switch v {
	case KnowledgeMapViewFolder, KnowledgeMapViewRelation, KnowledgeMapViewTopic:
		return true
	default:
		return false
	}
}

// ParseKnowledgeMapView normalizes the query value, defaulting to folder.
func ParseKnowledgeMapView(raw string) KnowledgeMapView {
	view := KnowledgeMapView(strings.ToLower(strings.TrimSpace(raw)))
	if view.Valid() {
		return view
	}
	return KnowledgeMapViewFolder
}

// KnowledgeMapNode is one document on the map.
type KnowledgeMapNode struct {
	// KnowledgeID / KnowledgeBaseID identify the document.
	KnowledgeID     string `json:"knowledge_id"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
	// Title is shown on the node.
	Title string `json:"title"`
	// FolderPath is the document's directory (folder view).
	FolderPath string `json:"folder_path,omitempty"`
	// LifecycleStatus / Classification let the UI badge the node.
	LifecycleStatus string `json:"lifecycle_status"`
	Classification  string `json:"classification"`
	// Degree is how many map edges touch this node. A high degree marks a
	// hub; zero marks an island.
	Degree int `json:"degree"`
	// ClusterID names the group the node belongs to under the requested view.
	ClusterID string `json:"cluster_id"`
	// Tags are the document's tag names (topic view / tooltip).
	Tags []string `json:"tags,omitempty"`
	// UpdatedAt is the document's last change.
	UpdatedAt string `json:"updated_at,omitempty"`
}

// KnowledgeMapEdge is one knowledge-to-knowledge relation on the map.
type KnowledgeMapEdge struct {
	// From / To are knowledge ids; direction follows the stored relation.
	From string `json:"from"`
	To   string `json:"to"`
	// RelationType is the semantic kind.
	RelationType string `json:"relation_type"`
	// Origin is manual or ai_suggested.
	Origin string `json:"origin"`
}

// KnowledgeMapCluster is one group of documents.
type KnowledgeMapCluster struct {
	// ID is the cluster identity under the requested view (folder path,
	// component index, or tag name).
	ID string `json:"id"`
	// Label is the display name.
	Label string `json:"label"`
	// NodeCount / EdgeCount describe the cluster.
	NodeCount int `json:"node_count"`
	EdgeCount int `json:"edge_count"`
	// KnowledgeIDs lists the members (bounded by the caller's limit).
	KnowledgeIDs []string `json:"knowledge_ids"`
}

// KnowledgeMap is the whole payload.
type KnowledgeMap struct {
	// KnowledgeBaseID the map describes.
	KnowledgeBaseID string `json:"knowledge_base_id"`
	// View is the aggregation that produced the clusters.
	View KnowledgeMapView `json:"view"`
	// Nodes / Edges are the graph itself.
	Nodes []KnowledgeMapNode `json:"nodes"`
	Edges []KnowledgeMapEdge `json:"edges"`
	// Clusters group the nodes per the requested view.
	Clusters []KnowledgeMapCluster `json:"clusters"`
	// Islands are documents with no relations — §4.6 "无主/孤岛治理" 的输入。
	Islands []string `json:"islands"`
	// Hubs are the documents with the most relations, highest first.
	Hubs []string `json:"hubs"`
	// RootFolders lists the top-level directories seen (folder view helper).
	RootFolders []string `json:"root_folders,omitempty"`
	// Truncated reports that the space held more documents than the cap.
	Truncated bool `json:"truncated"`
	// HiddenCount is how many of the space's documents are NOT on the map —
	// filtered by permission or lifecycle, or simply beyond the node cap. It is
	// an aggregate only and never names anything (§2.10 存在性不泄露); the UI
	// uses it to explain "为什么图只有这些".
	HiddenCount int `json:"hidden_count"`
}

// Knowledge map limits.
const (
	// DefaultKnowledgeMapNodes caps one map.
	DefaultKnowledgeMapNodes = 300
	// MaxKnowledgeMapNodes caps one map hard.
	MaxKnowledgeMapNodes = 1000
	// KnowledgeMapTopHubs is how many hubs / islands are named.
	KnowledgeMapTopHubs = 20
)

// NormalizeKnowledgeMapLimit clamps the node cap.
func NormalizeKnowledgeMapLimit(limit int) int {
	if limit <= 0 {
		return DefaultKnowledgeMapNodes
	}
	if limit > MaxKnowledgeMapNodes {
		return MaxKnowledgeMapNodes
	}
	return limit
}

// BuildKnowledgeMapClusters groups nodes per view.
//
// 分组规则：
//   - folder：按 folder_path 的**完整路径**分组（不滚到根目录），因为读者
//     关心的是"sop/e45 下面有哪些文档"而不是"sop 下有 200 篇"；
//   - topic：一篇文档属于它拥有的每个标签，因此节点可能出现在多个簇里，
//     这是主题视角的正常语义，调用方不应假设簇之间互斥；
//   - relation：按连通分量分组，孤立点各自成簇（并同时进入 Islands）。
//
// 簇之间不要求互斥是刻意的；但 node_count 总是该簇内去重后的文档数。
func BuildKnowledgeMapClusters(
	view KnowledgeMapView, nodes []KnowledgeMapNode, edges []KnowledgeMapEdge,
) []KnowledgeMapCluster {
	switch view {
	case KnowledgeMapViewRelation:
		return buildRelationClusters(nodes, edges)
	case KnowledgeMapViewTopic:
		return buildTopicClusters(nodes)
	default:
		return buildFolderClusters(nodes)
	}
}

// buildFolderClusters groups by exact folder path.
func buildFolderClusters(nodes []KnowledgeMapNode) []KnowledgeMapCluster {
	byFolder := make(map[string][]string)
	for _, node := range nodes {
		path := NormalizeKnowledgeFolderPath(node.FolderPath)
		byFolder[path] = append(byFolder[path], node.KnowledgeID)
	}
	clusters := make([]KnowledgeMapCluster, 0, len(byFolder))
	for path, ids := range byFolder {
		label := path
		if label == "" {
			label = "/"
		}
		clusters = append(clusters, KnowledgeMapCluster{
			ID: path, Label: label, KnowledgeIDs: ids, NodeCount: len(ids),
		})
	}
	sort.Slice(clusters, func(i, j int) bool {
		if clusters[i].NodeCount != clusters[j].NodeCount {
			return clusters[i].NodeCount > clusters[j].NodeCount
		}
		return clusters[i].ID < clusters[j].ID
	})
	return clusters
}

// buildTopicClusters groups by tag.
func buildTopicClusters(nodes []KnowledgeMapNode) []KnowledgeMapCluster {
	byTag := make(map[string][]string)
	for _, node := range nodes {
		for _, tag := range node.Tags {
			trimmed := strings.TrimSpace(tag)
			if trimmed == "" {
				continue
			}
			byTag[trimmed] = append(byTag[trimmed], node.KnowledgeID)
		}
	}
	clusters := make([]KnowledgeMapCluster, 0, len(byTag))
	for tag, ids := range byTag {
		clusters = append(clusters, KnowledgeMapCluster{
			ID: tag, Label: tag, KnowledgeIDs: ids, NodeCount: len(ids),
		})
	}
	sort.Slice(clusters, func(i, j int) bool {
		if clusters[i].NodeCount != clusters[j].NodeCount {
			return clusters[i].NodeCount > clusters[j].NodeCount
		}
		return clusters[i].ID < clusters[j].ID
	})
	return clusters
}

// buildRelationClusters groups by connected component.
//
// 组件标识用组件内字典序最小的 knowledge id：同一份图重复计算得到同样的
// 标识，前端可以据此做增量渲染与对比，而"组件 #3"这种序号会随节点顺序漂移。
func buildRelationClusters(
	nodes []KnowledgeMapNode, edges []KnowledgeMapEdge,
) []KnowledgeMapCluster {
	adjacency := make(map[string][]string, len(nodes))
	for _, node := range nodes {
		adjacency[node.KnowledgeID] = nil
	}
	for _, edge := range edges {
		// 边可能指向被权限过滤掉的节点（对端不可见）：不把它当节点，
		// 但也不影响可见端的度数。
		if _, ok := adjacency[edge.From]; ok {
			adjacency[edge.From] = append(adjacency[edge.From], edge.To)
		}
		if _, ok := adjacency[edge.To]; ok {
			adjacency[edge.To] = append(adjacency[edge.To], edge.From)
		}
	}

	visited := make(map[string]bool, len(nodes))
	clusters := make([]KnowledgeMapCluster, 0)
	for _, node := range nodes {
		if visited[node.KnowledgeID] {
			continue
		}
		component := make([]string, 0, 8)
		queue := []string{node.KnowledgeID}
		visited[node.KnowledgeID] = true
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			component = append(component, current)
			for _, peer := range adjacency[current] {
				if visited[peer] {
					continue
				}
				if _, ok := adjacency[peer]; !ok {
					continue
				}
				visited[peer] = true
				queue = append(queue, peer)
			}
		}
		sort.Strings(component)
		clusterID := component[0]
		clusters = append(clusters, KnowledgeMapCluster{
			ID: clusterID, Label: clusterID, KnowledgeIDs: component,
			NodeCount: len(component),
		})
	}
	// 组件内边数：两端都在本组件里的边。
	clusterIndex := make(map[string]int, len(clusters))
	for index, cluster := range clusters {
		for _, id := range cluster.KnowledgeIDs {
			clusterIndex[id] = index
		}
	}
	for _, edge := range edges {
		fromIndex, fromOK := clusterIndex[edge.From]
		toIndex, toOK := clusterIndex[edge.To]
		if fromOK && toOK && fromIndex == toIndex {
			clusters[fromIndex].EdgeCount++
		}
	}
	sort.Slice(clusters, func(i, j int) bool {
		if clusters[i].NodeCount != clusters[j].NodeCount {
			return clusters[i].NodeCount > clusters[j].NodeCount
		}
		return clusters[i].ID < clusters[j].ID
	})
	return clusters
}

// KnowledgeMapStats derives islands and hubs from the finished graph.
//
// 孤岛 = 度数为 0 的可见文档；枢纽 = 度数最高的若干文档。两者都是 §4.6
// 治理动作的输入：孤岛提示"这篇没人引用、可能没人找得到"，枢纽提示"改它
// 的影响面最大"。
func KnowledgeMapStats(nodes []KnowledgeMapNode, edges []KnowledgeMapEdge) (
	islands []string, hubs []string,
) {
	degree := make(map[string]int, len(nodes))
	for _, node := range nodes {
		degree[node.KnowledgeID] = 0
	}
	for _, edge := range edges {
		if _, ok := degree[edge.From]; ok {
			degree[edge.From]++
		}
		if _, ok := degree[edge.To]; ok {
			degree[edge.To]++
		}
	}
	type ranked struct {
		id     string
		degree int
	}
	rankedNodes := make([]ranked, 0, len(nodes))
	islands = make([]string, 0)
	for _, node := range nodes {
		if degree[node.KnowledgeID] == 0 {
			islands = append(islands, node.KnowledgeID)
			continue
		}
		rankedNodes = append(rankedNodes, ranked{id: node.KnowledgeID, degree: degree[node.KnowledgeID]})
	}
	sort.Slice(rankedNodes, func(i, j int) bool {
		if rankedNodes[i].degree != rankedNodes[j].degree {
			return rankedNodes[i].degree > rankedNodes[j].degree
		}
		return rankedNodes[i].id < rankedNodes[j].id
	})
	if len(rankedNodes) > KnowledgeMapTopHubs {
		rankedNodes = rankedNodes[:KnowledgeMapTopHubs]
	}
	hubs = make([]string, 0, len(rankedNodes))
	for _, node := range rankedNodes {
		hubs = append(hubs, node.id)
	}
	sort.Strings(islands)
	return islands, hubs
}

// KnowledgeMapClusterPreview bounds one cluster's id list so a 500-node folder
// does not inflate the payload. Full membership is available through the
// document list, which applies the same permissions.
func KnowledgeMapClusterPreview(cluster *KnowledgeMapCluster, limit int) {
	if cluster == nil || limit <= 0 || len(cluster.KnowledgeIDs) <= limit {
		return
	}
	cluster.KnowledgeIDs = cluster.KnowledgeIDs[:limit]
}

// KnowledgeMapRootFolders returns the distinct top-level directories.
func KnowledgeMapRootFolders(nodes []KnowledgeMapNode) []string {
	seen := make(map[string]struct{})
	for _, node := range nodes {
		path := NormalizeKnowledgeFolderPath(node.FolderPath)
		if path == "" {
			continue
		}
		root := path
		if index := strings.IndexByte(path, '/'); index > 0 {
			root = path[:index]
		}
		seen[root] = struct{}{}
	}
	roots := make([]string, 0, len(seen))
	for root := range seen {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	return roots
}

// ValidateKnowledgeMapTarget guards the request shape.
func ValidateKnowledgeMapTarget(knowledgeBaseID string) error {
	if strings.TrimSpace(knowledgeBaseID) == "" {
		return fmt.Errorf("knowledge_base_id is required")
	}
	return nil
}

// RoleKnowledgeMap is the §15.5 岗位知识地图 payload.
type RoleKnowledgeMap struct {
	// RoleTags are the tags that define the role.
	RoleTags []string `json:"role_tags"`
	// Nodes are the role's documents (permission-filtered).
	Nodes []KnowledgeMapNode `json:"nodes"`
	// Clusters group them by folder.
	Clusters []KnowledgeMapCluster `json:"clusters"`
	// Islands / Hubs mirror the space map's governance signals.
	Islands []string `json:"islands"`
	Hubs    []string `json:"hubs"`
	// TotalDocuments is how many documents carry the role tags (visible ones).
	TotalDocuments int `json:"total_documents"`
	// RequiredKnowledgeIDs are the documents the caller has an open learning
	// task for; CompletedKnowledgeIDs are the ones already learned.
	RequiredKnowledgeIDs  []string `json:"required_knowledge_ids"`
	CompletedKnowledgeIDs []string `json:"completed_knowledge_ids"`
	// CoveragePct is completed / required for this role, 0 when nothing is
	// required (an empty requirement is not "100% covered").
	CoveragePct float64 `json:"coverage_pct"`
	// Truncated reports the node cap was hit.
	Truncated bool `json:"truncated"`
	// GeneratedAt is the computation time.
	GeneratedAt time.Time `json:"generated_at"`
}
