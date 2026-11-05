package types

import (
	"math"
	"sort"
	"strings"
)

// ============================================================================
// 物品协同过滤（KnowAxis V3.0 §15.3 "推荐：协同过滤"）。
//
// 平台此前只有"规则 + 统计"推荐：关系边、标签、热度、新鲜度。那些是**内容
// 侧**信号——"这条和这条像"。协同过滤补的是**行为侧**信号："看过 A 的人也
// 看了 B"。两者的区别在推荐位上很直观：内容侧说"因为都属于 E45"，行为侧说
// "和你情况类似的人在读它"。
//
// 数据前提（已核实）：`knowledge_view_events (user_id, knowledge_id)` 与
// `user_resource_favorites (user_id, resource_id)` 构成完整的用户-物品矩阵。
// `message_feedback` **不**作为输入：它挂在 message 上，要经 evidence_packs
// 的 citations 才能连到知识，链路长且极稀疏——用它算相似度会引入比信号更多
// 的噪声。
//
// 四个决定：
//  1. item-based 而不是 user-based。用户数远多于物品交互密度，"看过 A 的人
//     也看过 B"的物品相似度可以离线算、可缓存、且对新用户立即生效（只要有
//     一条行为）。user-based 需要找相似用户，冷启动直接失效。
//  2. 相似度用**余弦**而不是 Jaccard。交互次数携带强度（读三次比读一次更能
//     说明偏好），Jaccard 把它压成布尔值。
//  3. 输出带**可解释理由**：推荐的来源物品与相似度一起返回。没有这个，
//     协同过滤的推荐无法向用户或运营解释，出问题也无法归因。
//  4. 相似度低于阈值不推荐。弱相似（0.01）产生的候选看起来是随机的，而
//     "随机但声称是协同过滤"比不推荐更糟。
// ============================================================================

const (
	// DefaultCFMaxNeighbours bounds how many similar items seed a recommendation.
	DefaultCFMaxNeighbours = 12
	// DefaultCFMinSimilarity is the floor below which two items are unrelated.
	//
	// 0.05 是刻意的低阈值：企业知识库的交互通常稀疏，把门槛抬高会让绝大多数
	// 用户没有任何推荐。但它不是 0——见文件头的第 4 条决定。
	DefaultCFMinSimilarity = 0.05
	// DefaultCFMaxRecommendations caps one response.
	DefaultCFMaxRecommendations = 10
)

// CFInteraction is one user-item interaction with an implicit strength.
type CFInteraction struct {
	// UserID who acted.
	UserID string
	// ItemID is the knowledge document.
	ItemID string
	// KnowledgeBaseID scopes the item, so the ACL gate can be applied to the
	// candidates' actual spaces. CF similarity crosses spaces, so the filter
	// cannot assume a single knowledge base.
	KnowledgeBaseID string
	// Weight is the implicit rating (view = 1, favorite = 3 by default).
	Weight float64
}

// CFItemProfile is one item's interaction vector, built once and reused.
type CFItemProfile struct {
	// ItemID the profile belongs to.
	ItemID string
	// Norm is the L2 norm of the weight vector.
	Norm float64
	// ByUser maps user id → weight, for the dot product.
	ByUser map[string]float64
}

// CFModel is a prepared similarity index.
//
// 构建一次、查询多次：见 BuildCFModel 的注释。
type CFModel struct {
	// Profiles are the item vectors keyed by item id.
	Profiles map[string]*CFItemProfile
	// ItemIDs in a stable order, so iteration does not depend on map order.
	ItemIDs []string
	// ItemKnowledgeBase maps item id → its knowledge base, so the caller can
	// apply the ACL gate to the candidates' real spaces.
	ItemKnowledgeBase map[string]string
}

// CFRecommendation is one recommended item with its evidence.
type CFRecommendation struct {
	// ItemID recommended.
	ItemID string `json:"item_id"`
	// Score is the summed similarity across the seed items.
	Score float64 `json:"score"`
	// FromItemIDs are the history items that produced it (largest first).
	//
	// 这是可解释性的载体："因为你读过 A 和 B"必须能回答，否则用户无法
	// 判断这条推荐是否值得点，运营也无法在推荐不准时定位原因。
	FromItemIDs []string `json:"from_item_ids"`
	// Similarity is the strongest single similarity behind the score.
	Similarity float64 `json:"similarity"`
}

// BuildCFModel builds the item similarity index from interactions.
//
// 把同一 (user, item) 的多条行为**累加**而不是取最大：读三次加收藏比只读一次
// 更能说明偏好，取最大会把这份强度丢掉。
func BuildCFModel(interactions []CFInteraction) *CFModel {
	model := &CFModel{
		Profiles:          make(map[string]*CFItemProfile, len(interactions)),
		ItemKnowledgeBase: make(map[string]string, len(interactions)),
	}
	for _, interaction := range interactions {
		userID := strings.TrimSpace(interaction.UserID)
		itemID := strings.TrimSpace(interaction.ItemID)
		if userID == "" || itemID == "" || interaction.Weight <= 0 {
			continue
		}
		profile, ok := model.Profiles[itemID]
		if !ok {
			profile = &CFItemProfile{ItemID: itemID, ByUser: make(map[string]float64, 8)}
			model.Profiles[itemID] = profile
			model.ItemIDs = append(model.ItemIDs, itemID)
		}
		profile.ByUser[userID] += interaction.Weight
		if interaction.KnowledgeBaseID != "" {
			model.ItemKnowledgeBase[itemID] = interaction.KnowledgeBaseID
		}
	}
	for _, profile := range model.Profiles {
		var sum float64
		for _, weight := range profile.ByUser {
			sum += weight * weight
		}
		profile.Norm = math.Sqrt(sum)
	}
	sort.Strings(model.ItemIDs)
	return model
}

// Similarity computes the cosine similarity between two items.
//
// 任一物品没有交互向量时返回 0：没有行为就没有可比之处，而不是"恰好相同"。
func (m *CFModel) Similarity(itemA, itemB string) float64 {
	if m == nil || itemA == itemB {
		return 0
	}
	left, ok := m.Profiles[itemA]
	if !ok || left.Norm == 0 {
		return 0
	}
	right, ok := m.Profiles[itemB]
	if !ok || right.Norm == 0 {
		return 0
	}
	// 遍历较短的一侧求点积。
	if len(left.ByUser) > len(right.ByUser) {
		left, right = right, left
	}
	var dot float64
	for userID, weight := range left.ByUser {
		if other, ok := right.ByUser[userID]; ok {
			dot += weight * other
		}
	}
	if dot == 0 {
		return 0
	}
	return dot / (left.Norm * right.Norm)
}

// Neighbours returns the items most similar to one item.
func (m *CFModel) Neighbours(
	itemID string, limit int, minSimilarity float64,
) []CFRecommendation {
	if m == nil || limit <= 0 {
		return nil
	}
	itemID = strings.TrimSpace(itemID)
	if _, ok := m.Profiles[itemID]; !ok {
		return nil
	}
	if minSimilarity <= 0 {
		minSimilarity = DefaultCFMinSimilarity
	}
	out := make([]CFRecommendation, 0, limit)
	for _, candidateID := range m.ItemIDs {
		if candidateID == itemID {
			continue
		}
		similarity := m.Similarity(itemID, candidateID)
		if similarity < minSimilarity {
			continue
		}
		out = append(out, CFRecommendation{
			ItemID: candidateID, Score: similarity, Similarity: similarity,
			FromItemIDs: []string{itemID},
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ItemID < out[j].ItemID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Recommend suggests items for one user from their own history.
//
// exclude 是调用方已见过的条目（通常是历史本身与已推荐的），必须排除：
// "推荐你刚读过的东西"是推荐位最常见也最伤信任的错误。
func (m *CFModel) Recommend(
	userHistory []string, exclude []string, limit int, minSimilarity float64,
) []CFRecommendation {
	if m == nil || len(userHistory) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = DefaultCFMaxRecommendations
	}
	excluded := make(map[string]struct{}, len(userHistory)+len(exclude))
	for _, id := range userHistory {
		excluded[strings.TrimSpace(id)] = struct{}{}
	}
	for _, id := range exclude {
		excluded[strings.TrimSpace(id)] = struct{}{}
	}

	// 逐条历史展开邻居并按目标聚合，同时保留来源以便解释。
	aggregated := make(map[string]*CFRecommendation, limit*3)
	for _, seedID := range userHistory {
		seedID = strings.TrimSpace(seedID)
		if seedID == "" {
			continue
		}
		for _, neighbour := range m.Neighbours(seedID, DefaultCFMaxNeighbours, minSimilarity) {
			if _, skip := excluded[neighbour.ItemID]; skip {
				continue
			}
			existing, ok := aggregated[neighbour.ItemID]
			if !ok {
				copyOf := neighbour
				copyOf.FromItemIDs = []string{seedID}
				aggregated[neighbour.ItemID] = &copyOf
				continue
			}
			existing.Score += neighbour.Score
			if neighbour.Similarity > existing.Similarity {
				existing.Similarity = neighbour.Similarity
			}
			// 来源去重并保持"相似度大者在前"。
			if !containsString(existing.FromItemIDs, seedID) {
				existing.FromItemIDs = append(existing.FromItemIDs, seedID)
			}
		}
	}

	out := make([]CFRecommendation, 0, len(aggregated))
	for _, recommendation := range aggregated {
		out = append(out, *recommendation)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ItemID < out[j].ItemID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// containsString reports whether values holds target.
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// CFInteractionWeights is how each behaviour counts as an implicit rating.
//
// 收藏 3 倍于阅读：阅读可以是误点或扫一眼就关，收藏是明确的"我要留着它"。
// 这两个数字是行为的相对强度，不是精确度量——写在这里是为了让"为什么收藏
// 权重更高"可被讨论，而不是埋在查询里。
type CFInteractionWeights struct {
	// View is the weight of one view event.
	View float64
	// Favorite is the weight of one favorite.
	Favorite float64
}

// DefaultCFInteractionWeights is the v1 weighting.
func DefaultCFInteractionWeights() CFInteractionWeights {
	return CFInteractionWeights{View: 1, Favorite: 3}
}
