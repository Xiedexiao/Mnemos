package types

import (
	"fmt"
	"strings"
)

// ============================================================================
// Embedding 降级的额外约束（KnowAxis V3.0 §16.5 模型降级链）。
//
// 通用降级链对 chat 模型是安全的：换一个模型回答同一个问题，答案质量可能
// 变化，但不会变成**错的**。对 embedding 则不是：
//
//   - 索引里的向量是用**某个具体模型**算出来的。换一个模型把同一个查询编码
//     到另一个向量空间，余弦相似度就变成两个不同空间里两个点的"夹角"——
//     数值算得出来，含义不存在。
//   - 这种错误是**静默**的：检索不会报错，只会返回一批无关的 chunk，然后
//     模型拿着它们编出看似合理的答案。它比直接失败危险得多。
//
// 因此这里给 embedding 通道加两道硬约束：
//  1. 只有**维度相同**的模型才可能作为候选。维度不同时向量存储根本存不进去，
//     是唯一能被机械判定的必要条件。
//  2. 维度相同也**不充分**：两个模型即使都输出 1024 维，空间也不一定相同。
//     所以降级必须显式声明为"可接受有损"，并留下痕迹——默认不降级。
// ============================================================================

// EmbeddingFallbackCandidate is one model considered for embedding fallback.
type EmbeddingFallbackCandidate struct {
	// ModelID as configured.
	ModelID string
	// Dimensions the model produces (0 when unknown).
	Dimensions int
	// Name is the display name for the audit record.
	Name string
}

// EmbeddingFallbackDecision reports whether a candidate may serve the query.
type EmbeddingFallbackDecision struct {
	// Allowed is true when the candidate may be used.
	Allowed bool
	// Reason explains a rejection or a caveat.
	Reason string
	// Lossy is true when the candidate is dimensionally compatible but is not
	// the model the index was built with.
	Lossy bool
}

// CheckEmbeddingFallback decides whether one candidate may replace the
// index's model for a query embedding.
//
// indexDimensions 是索引模型产出的维度（kb 上配置的那个模型）。候选维度未知
// 时按拒绝处理：无法确认维度一致就不能换——这条路径的失败是静默的，宁可让
// 检索报错也不要返回无关结果。
func CheckEmbeddingFallback(
	indexModelID string, indexDimensions int, candidate EmbeddingFallbackCandidate,
) EmbeddingFallbackDecision {
	if strings.TrimSpace(candidate.ModelID) == "" {
		return EmbeddingFallbackDecision{Reason: "candidate has no model id"}
	}
	if candidate.ModelID == indexModelID {
		// 同一个模型：不是降级，正常路径。
		return EmbeddingFallbackDecision{Allowed: true}
	}
	if indexDimensions <= 0 {
		return EmbeddingFallbackDecision{
			Reason: "the index model's dimension is unknown, so compatibility cannot be checked",
		}
	}
	if candidate.Dimensions <= 0 {
		return EmbeddingFallbackDecision{
			Reason: "the candidate model's dimension is unknown",
		}
	}
	if candidate.Dimensions != indexDimensions {
		return EmbeddingFallbackDecision{
			Reason: fmt.Sprintf("dimension mismatch: index=%d candidate=%d",
				indexDimensions, candidate.Dimensions),
		}
	}
	// 维度一致：可以尝试，但这是**有损**的——两个 1024 维模型不共享同一个
	// 向量空间。调用方必须把 Lossy 记进审计，让"这次检索结果为什么不对"
	// 在事后可解释。
	return EmbeddingFallbackDecision{
		Allowed: true,
		Lossy:   true,
		Reason: "same dimension but a different model: query vectors do not " +
			"share the index's embedding space, so results are approximate",
	}
}

// EmbeddingFallbackTrace records what the embedding channel did.
//
// 与 ModelFallbackOutcome 分开：embedding 的降级需要额外记录 Lossy，因为
// "换了模型但仍能检索"与"换了模型且结果可信"是两件事。
type EmbeddingFallbackTrace struct {
	// IndexModelID is the model the index was built with.
	IndexModelID string `json:"index_model_id"`
	// ServedBy is the model that produced the query vector.
	ServedBy string `json:"served_by"`
	// Degraded is true when ServedBy is not IndexModelID.
	Degraded bool `json:"degraded"`
	// Lossy is true when the vector came from a different model than the index.
	Lossy bool `json:"lossy"`
	// Attempts lists every model tried, in order.
	Attempts []ModelFallbackAttempt `json:"attempts"`
	// Reason carries the caveat for a lossy degrade.
	Reason string `json:"reason,omitempty"`
}

// DescribeEmbeddingFallback renders one line for the audit details.
func DescribeEmbeddingFallback(trace EmbeddingFallbackTrace) string {
	if !trace.Degraded {
		return "index model served the query embedding"
	}
	if trace.Lossy {
		return fmt.Sprintf(
			"degraded to %s (lossy: different embedding space from index model %s)",
			trace.ServedBy, trace.IndexModelID)
	}
	return fmt.Sprintf("degraded to %s", trace.ServedBy)
}

// EmbeddingFallbackScene is the scene key used for embedding fallback chains.
//
// 与 model_usage_daily.scene 的取值保持一致，配额与降级用同一个键才能对上。
const EmbeddingFallbackScene = ModelUsageSceneEmbedding
