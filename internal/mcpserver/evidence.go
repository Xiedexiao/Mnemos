package mcpserver

import (
	"context"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// ============================================================================
// MCP 问答的证据包（KnowAxis V3.0 §13.4 证据包 / §16.5 外部调用留痕）。
//
// 聊天管道在回答落盘后会写一份证据包；MCP 的 `ask` 工具没有——它只写了
// 审计行。审计回答的是"谁在什么时候调了哪个工具"，证据包回答的是"这次
// 回答检索到了什么、引用了哪几篇、当时是什么权限状态"。一旦知识通过 MCP
// 交给外部系统，后一个问题才是复盘依据：外部系统拿到的结论，与平台内部
// 走问答得到的结论，必须能被同一套证据链解释。
//
// 三个决定：
//  1. 与聊天管道**同一套结构**（candidates / citations / answer / refusal /
//     模型与提示词版本），否则同一份知识在两条链路上的取证口径会不一致。
//  2. 写入**尽力而为**：证据包落库失败不让已经生成的回答作废——外部系统
//     已经拿到内容，把调用改成失败只会让它重试并拿到第二份回答。失败记
//     warning，与聊天管道同口径。
//  3. 权限快照记录**当时的可见范围**（知识库集合与最高密级），而不是引用
//     epoch 账本：MCP 服务器没有那套依赖，而"这次调用时 endpoint 能看到
//     哪些空间"已经足够回答"它当时是否有权看到这些证据"。
// ============================================================================

// evidenceSnippetMaxRunes bounds one candidate snippet in the pack.
//
// 证据包是取证快照，不是正文存储：片段用于事后核对"当时看到的是哪一段"，
// 全文仍在消息与 chunk 里。与聊天管道同一上限（900 字节）。
const evidenceSnippetMaxRunes = 900

// recordAskEvidence persists the evidence pack for one MCP answer.
func (s *Server) recordAskEvidence(
	ctx context.Context, requestID, sessionID, messageID string,
	question, answer string, refs []*types.SearchResult,
) {
	if s.evidenceService == nil || requestID == "" {
		return
	}
	tenantID, _ := types.TenantIDFromContext(ctx)
	if tenantID == 0 {
		return
	}
	// 委派用户是这次回答的真实主体（§16.2），审计与证据包必须指向同一人。
	userID := types.EffectiveSubjectUser(
		types.CallerFromContext(ctx).UserID, delegationOnContext(ctx))

	pack := &types.EvidencePack{
		TenantID:  tenantID,
		RequestID: requestID,
		SessionID: sessionID,
		MessageID: messageID,
		UserID:    userID,
		Question:  question,
		Answer:    answer,
	}

	candidates := make([]types.EvidenceCandidate, 0, len(refs))
	citations := make([]types.EvidenceCitation, 0, len(refs))
	highest := types.KnowledgeClassificationPublic
	for index, ref := range refs {
		if ref == nil {
			continue
		}
		snippet := ref.Content
		if runes := []rune(snippet); len(runes) > evidenceSnippetMaxRunes {
			snippet = string(runes[:evidenceSnippetMaxRunes])
		}
		candidates = append(candidates, types.EvidenceCandidate{
			KnowledgeID: ref.KnowledgeID,
			ChunkID:     ref.ID,
			Score:       ref.Score,
			Snippet:     snippet,
		})
		citations = append(citations, types.EvidenceCitation{
			Index:       index + 1,
			KnowledgeID: ref.KnowledgeID,
			ChunkID:     ref.ID,
			Title:       ref.KnowledgeTitle,
		})
		// 最高密级：回答触达的最敏感内容，回放时据此核对权限基线。
		if rank := types.ParseKnowledgeClassification(ref.KnowledgeClassification).Rank(); rank > highest.Rank() {
			highest = types.ParseKnowledgeClassification(ref.KnowledgeClassification)
		}
	}
	pack.SetCandidates(candidates)
	pack.SetCitations(citations)
	pack.SetContextSnapshot(types.EvidenceContextSnapshot{
		CandidatesShown: len(candidates),
		CandidatesTotal: len(candidates),
	})
	pack.SetPermissionSnapshot(types.EvidencePermissionSnapshot{
		HighestClassification: string(highest),
	})

	// 无引用即"没有依据"：与聊天管道一致，把兜底拒答原因记下来，
	// 审计因此能区分"正常回答但没引用"与"本来就没有证据"。
	if len(citations) == 0 {
		pack.Refusal = string(types.EvidenceRefusalNoEvidence)
	}

	if _, err := s.evidenceService.Save(ctx, pack); err != nil {
		logger.Warnf(ctx, "[mcpserver] evidence pack save failed for request %s: %v",
			requestID, err)
	}
}

// delegationOnContext reads the delegation block, if any.
//
// 单独一层是为了让证据包与审计用同一个读取口径：两处若各自判断"有没有
// 委派"，会出现审计记了委派用户、证据包记了 endpoint 身份的情况。
func delegationOnContext(ctx context.Context) types.AgentDelegation {
	delegation, ok := types.AgentDelegationFromContext(ctx)
	if !ok {
		return types.AgentDelegation{}
	}
	return delegation
}
