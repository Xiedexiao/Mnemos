package types

import (
	"database/sql/driver"
	"encoding/json"
)

// RetrievalConfig holds the global retrieval/search configuration for a tenant.
// This replaces the retrieval-related fields previously scattered in ConversationConfig
// and ChatHistoryConfig. Both knowledge search and message search share these parameters.
//
// Stored as a JSONB column on the tenants table, managed via the settings UI
// at /tenants/kv/retrieval-config.
type RetrievalConfig struct {
	// EmbeddingTopK is the maximum number of chunks returned by vector search (default: 50)
	EmbeddingTopK int `json:"embedding_top_k"`
	// VectorThreshold is the minimum vector similarity score (0-1, default: 0.15)
	VectorThreshold float64 `json:"vector_threshold"`
	// KeywordThreshold is the minimum keyword match score (0-1, default: 0.3)
	KeywordThreshold float64 `json:"keyword_threshold"`
	// RerankTopK is the maximum number of results after reranking (default: 10)
	RerankTopK int `json:"rerank_top_k"`
	// RerankThreshold is the minimum rerank score (-10 to 10, default: 0.2)
	RerankThreshold float64 `json:"rerank_threshold"`
	// RerankModelID is the ID of the rerank model to use (required for search)
	RerankModelID string `json:"rerank_model_id"`

	// RRFK is the smoothing constant of Reciprocal Rank Fusion. Larger values
	// flatten the curve, reducing the bias towards top-1 results.
	// Default: 60. Sensible range: 30..100 depending on corpus size.
	RRFK int `json:"rrf_k,omitempty"`
	// RRFVectorWeight is the weight applied to the vector retriever inside RRF.
	// RRFVectorWeight + RRFKeywordWeight should usually sum to 1.0 but the math
	// works for any positive weights. Default: 0.7.
	RRFVectorWeight float64 `json:"rrf_vector_weight,omitempty"`
	// RRFKeywordWeight is the keyword counterpart. Default: 0.3.
	RRFKeywordWeight float64 `json:"rrf_keyword_weight,omitempty"`
	// RRFGraphWeight is the weight of the §11 knowledge-graph channel inside the
	// same fusion (KnowAxis V3.0 §12.2 "图谱增强"). Default: 0.2.
	//
	// 图谱召回与向量/关键词**在同一个 RRF 里排序**，而不是像此前那样作为一个
	// 独立后置阶段把结果追加到末尾。追加式合并的问题不是精度不够，而是图谱
	// 命中永远排在所有向量/关键词命中之后——即使它才是唯一真正相关的那条。
	RRFGraphWeight float64 `json:"rrf_graph_weight,omitempty"`
}

// DefaultRetrievalTopK is the retrieval depth used when a caller supplies no
// usable TopK / MatchCount. HybridSearch also floors its over-retrieval pool
// at this value, so the fallback and the pool it draws from stay in step by
// construction rather than by two independently maintained literals.
const DefaultRetrievalTopK = 50

// MaxRequestedResults caps the result counts a retrieval API caller may ask
// for (knowledge-search match_count, rerank.top_k). knowledge-search runs one
// search per scoped document or tag set and reranks every candidate, so an
// unbounded count multiplied into tens of thousands of billed rerank passages.
const MaxRequestedResults = 200

// GetEffectiveEmbeddingTopK returns EmbeddingTopK with a fallback default.
func (c *RetrievalConfig) GetEffectiveEmbeddingTopK() int {
	if c == nil || c.EmbeddingTopK <= 0 {
		return DefaultRetrievalTopK
	}
	return c.EmbeddingTopK
}

// GetEffectiveVectorThreshold returns VectorThreshold with a fallback default.
func (c *RetrievalConfig) GetEffectiveVectorThreshold() float64 {
	if c == nil || c.VectorThreshold <= 0 {
		return 0.15
	}
	return c.VectorThreshold
}

// GetEffectiveKeywordThreshold returns KeywordThreshold with a fallback default.
func (c *RetrievalConfig) GetEffectiveKeywordThreshold() float64 {
	if c == nil || c.KeywordThreshold <= 0 {
		return 0.3
	}
	return c.KeywordThreshold
}

// GetEffectiveRerankTopK returns RerankTopK with a fallback default.
func (c *RetrievalConfig) GetEffectiveRerankTopK() int {
	if c == nil || c.RerankTopK <= 0 {
		return 10
	}
	return c.RerankTopK
}

// GetEffectiveRerankThreshold returns RerankThreshold with a fallback default.
func (c *RetrievalConfig) GetEffectiveRerankThreshold() float64 {
	if c == nil {
		return 0.2
	}
	return c.RerankThreshold
}

// GetEffectiveRRFK returns the RRF smoothing constant with a fallback default.
func (c *RetrievalConfig) GetEffectiveRRFK() int {
	if c == nil || c.RRFK <= 0 {
		return 60
	}
	return c.RRFK
}

// GetEffectiveRRFWeights returns vector / keyword / graph weights.
//
// 默认 0.7 / 0.3 / 0.2：图谱是一个**补充**通道，不该单独压过向量召回（它的
// 召回面只覆盖抽取过实体的文档），但也不该被设成 0 —— 0 等于关掉这一路，
// 那就回到了"图谱只是个后置阶段"的旧行为。三者不要求和为 1：RRF 本身按
// 排名贡献，权重的绝对尺度只影响归一化常数。
func (c *RetrievalConfig) GetEffectiveRRFWeights() (vector, keyword, graph float64) {
	if c == nil {
		return 0.7, 0.3, 0.2
	}
	if c.RRFVectorWeight == 0 && c.RRFKeywordWeight == 0 && c.RRFGraphWeight == 0 {
		return 0.7, 0.3, 0.2
	}
	vector = c.RRFVectorWeight
	if vector <= 0 {
		vector = 0.7
	}
	keyword = c.RRFKeywordWeight
	if keyword <= 0 {
		keyword = 0.3
	}
	// 图谱权重允许显式为 0：那是"关掉图谱通道"的合法配置，与"没配置"不同。
	graph = c.RRFGraphWeight
	if graph < 0 {
		graph = 0
	} else if graph == 0 && c.RRFGraphWeight == 0 && c.RRFVectorWeight == 0 && c.RRFKeywordWeight == 0 {
		graph = 0.2
	}
	return vector, keyword, graph
}

// Value implements the driver.Valuer interface for database serialization
func (c RetrievalConfig) Value() (driver.Value, error) {
	return json.Marshal(c)
}

// Scan implements the sql.Scanner interface for database deserialization
func (c *RetrievalConfig) Scan(value interface{}) error {
	if value == nil {
		return nil
	}
	b, ok := value.([]byte)
	if !ok {
		return nil
	}
	return json.Unmarshal(b, c)
}
