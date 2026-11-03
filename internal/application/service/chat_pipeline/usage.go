package chatpipeline

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// recordChatUsage attributes one answer-generation call to the §16.1 cost
// dimensions.
//
// 空间分摊只在"这次问答只针对一个知识库"时成立：多知识库会话的 token
// 无法诚实地拆到某一个空间，所以留空、由成本看板计入"未分摊"，而不是
// 随机挑一个知识库。场景固定为 chat——流式与非流式只是传输方式，不是
// 成本场景。
func recordChatUsage(
	ctx context.Context,
	gateway interfaces.ModelGatewayService,
	chatManage *types.ChatManage,
	promptTokens, completionTokens int,
) {
	if gateway == nil || chatManage == nil {
		return
	}
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		return
	}
	knowledgeBaseID := ""
	if len(chatManage.KnowledgeBaseIDs) == 1 {
		knowledgeBaseID = chatManage.KnowledgeBaseIDs[0]
	}
	_ = gateway.RecordUsageDetailed(ctx, tenantID, chatManage.ChatModelID,
		types.ModelUsageSceneChat, knowledgeBaseID, int64(promptTokens), int64(completionTokens))
}
