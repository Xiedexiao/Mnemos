package service

import (
	"context"
	"encoding/json"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// 派生知识的权限上限（KnowAxis V3.0 §2.4）。
//
// 最容易被忽略的一条规则：由机密工单生成的"案例"，不能因为标成 L2 就变成
// 全公司可见。派生知识（摘要、案例、FAQ、图谱事实、官方答案）的可见范围，
// 取全部源证据中最严格的那一条；交集为空则不可发布。
//
// 这里实现的是文档级版本：派生条目从源证据继承"最高密级"，并且只要任一源
// 证据不可读，就说明派生条目超出了源证据范围。

// derivedPermissionResolver 计算派生知识相对其源证据的权限上限。
type derivedPermissionResolver struct {
	knowledges interfaces.KnowledgeRepository
}

// sourceEvidenceIDs extracts the in-system knowledge IDs a candidate derives
// from. Only evidence of source_type "knowledge" (or a chunk reference, which
// carries the owning knowledge in SourceID) can be resolved to a stored
// document; operational record types (work_order, ncr, ...) are not knowledge
// rows and are skipped rather than treated as "unresolvable, therefore safe".
func candidateSourceKnowledgeIDs(candidate *types.KnowledgeCandidate) []string {
	if candidate == nil {
		return nil
	}
	var evidence []types.KnowledgeCandidateEvidence
	if len(candidate.Evidence) > 0 {
		if err := json.Unmarshal(candidate.Evidence, &evidence); err != nil {
			logger.Warnf(context.Background(),
				"candidate %s has unparsable evidence, treating as no resolvable source: %v",
				candidate.ID, err)
		}
	}
	seen := make(map[string]bool, len(evidence))
	out := make([]string, 0, len(evidence))
	for _, item := range evidence {
		ids := []string{}
		switch item.SourceType {
		case "knowledge":
			ids = append(ids, item.SourceID)
		case "chunk":
			// A chunk reference names the chunk; its owning knowledge is not in
			// this field. Callers that know the mapping should pass knowledge
			// sources instead, so this cannot silently widen permission.
			logger.Warnf(context.Background(),
				"candidate %s cites a chunk (%s) instead of a knowledge id; "+
					"derived permission cannot be computed from it",
				candidate.ID, item.SourceID)
			continue
		default:
			continue
		}
		for _, id := range ids {
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// derivedClassification returns the strictest classification across the source
// evidence, plus the count of sources that could not be resolved. An
// unresolvable source is reported so the caller can refuse rather than assume
// the safest answer.
func (r *derivedPermissionResolver) derivedClassification(
	ctx context.Context, tenantID uint64, sourceIDs []string,
) (types.KnowledgeClassification, int, error) {
	strictest := types.KnowledgeClassificationPublic
	missing := 0
	if len(sourceIDs) == 0 {
		return strictest, 0, nil
	}
	rows, err := r.knowledges.GetKnowledgeBatch(ctx, tenantID, sourceIDs)
	if err != nil {
		return strictest, 0, err
	}
	found := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		found[row.ID] = true
		if classification := types.ParseKnowledgeClassification(row.Classification); classification.Rank() > strictest.Rank() {
			strictest = classification
		}
	}
	for _, id := range sourceIDs {
		if !found[id] {
			missing++
		}
	}
	return strictest, missing, nil
}
