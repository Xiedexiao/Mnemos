package chatpipeline

import (
	"context"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// PluginChatCompletion implements chat completion functionality
// as a plugin that can be registered to EventManager
type PluginChatCompletion struct {
	modelService interfaces.ModelService // Interface for model operations
	// gateway records per-call token usage and resolves tenant prompt versions
	// (§16.5 成本与 Prompt 管理). Optional: nil keeps built-in prompts and
	// leaves the usage ledger empty.
	gateway interfaces.ModelGatewayService
}

// NewPluginChatCompletion creates a new PluginChatCompletion instance
// and registers it with the EventManager
func NewPluginChatCompletion(
	eventManager *EventManager,
	modelService interfaces.ModelService,
	gateway interfaces.ModelGatewayService,
) *PluginChatCompletion {
	res := &PluginChatCompletion{
		modelService: modelService,
		gateway:      gateway,
	}
	eventManager.Register(res)
	return res
}

// applyTenantPromptVersion overlays a stored prompt version onto the configured
// system prompt. No stored version means the built-in template stays in place.
func (p *PluginChatCompletion) applyTenantPromptVersion(
	ctx context.Context, chatManage *types.ChatManage,
) {
	if p.gateway == nil || chatManage == nil {
		return
	}
	tenantID, ok := types.TenantIDFromContext(ctx)
	if !ok || tenantID == 0 {
		return
	}
	version, err := p.gateway.LatestVersion(ctx, tenantID, chatPromptTemplateKey)
	if err != nil || version == nil || strings.TrimSpace(version.Content) == "" {
		return
	}
	chatManage.SystemPromptOverride = version.Content
	chatManage.PromptVersionTag = chatPromptTemplateKey + "@v" + itoaVersion(version.Version)
}

// chatPromptTemplateKey is the prompt_versions key for the knowledge-QA system
// prompt. It matches config/prompt_templates/system_prompt.yaml.
const chatPromptTemplateKey = "system_prompt"

// itoaVersion formats a 1-based version number for the tag.
func itoaVersion(version int) string {
	if version <= 0 {
		return "0"
	}
	var digits [20]byte
	pos := len(digits)
	for version > 0 {
		pos--
		digits[pos] = byte('0' + version%10)
		version /= 10
	}
	return string(digits[pos:])
}

// ActivationEvents returns the event types this plugin handles
func (p *PluginChatCompletion) ActivationEvents() []types.EventType {
	return []types.EventType{types.CHAT_COMPLETION}
}

// OnEvent handles chat completion events
// It prepares the chat model, messages, and calls the model to generate responses
func (p *PluginChatCompletion) OnEvent(
	ctx context.Context, eventType types.EventType, chatManage *types.ChatManage, next func() *PluginError,
) *PluginError {
	pipelineInfo(ctx, "Completion", "input", map[string]interface{}{
		"session_id":     chatManage.SessionID,
		"user_question":  chatManage.UserContent,
		"history_rounds": len(chatManage.History),
		"chat_model":     chatManage.ChatModelID,
	})

	// Prepare chat model and options
	chatModel, opt, err := prepareChatModel(ctx, p.modelService, chatManage)
	if err != nil {
		return ErrGetChatModel.WithError(err)
	}

	// Prepare messages including conversation history
	pipelineInfo(ctx, "Completion", "messages_ready", map[string]interface{}{
		"message_count": len(chatManage.History) + 2,
	})
	// Prompt 版本覆盖（§16.5）：租户保存过 prompt 版本时，用它替换内置模板，
	// 并把版本标签挂到本轮结果上，Evidence Pack 据此可复现"当时用的是哪一版"。
	// 这一步必须在消息装配之前完成，否则改的是空气。
	p.applyTenantPromptVersion(ctx, chatManage)
	chatMessages, modelContext := prepareMessagesWithModelContext(ctx, chatManage)
	chatMessages = modelContext.EncodeMessages(chatMessages)
	reportModelContextLeaks(ctx, "Completion", modelContext, chatMessages)
	ctx = withPromptCacheMetadata(ctx, chatModel, chatMessages, opt, "knowledge_qa")

	// Call the chat model to generate response
	pipelineInfo(ctx, "Completion", "model_call", map[string]interface{}{
		"chat_model": chatManage.ChatModelID,
	})
	chatResponse, err := chatModel.Chat(ctx, chatMessages, opt)
	if err != nil {
		pipelineError(ctx, "Completion", "model_call", map[string]interface{}{
			"chat_model": chatManage.ChatModelID,
			"error":      err.Error(),
		})
		return ErrModelCall.WithError(err)
	}
	modelContext.DecodeResponse(chatResponse)
	if orphans := modelContext.OrphanResourceHandles(chatResponse.Content); len(orphans) > 0 {
		pipelineWarn(ctx, "Completion", "orphan_resource_handles", map[string]interface{}{
			"session_id": chatManage.SessionID,
			"handles":    orphans,
		})
	}

	// 用量台账（§16.5）：成本看板、预算告警与按空间分摊都依赖它。此前
	// RecordUsage 没有任何调用方，台账恒为空。记录失败只告警，不阻断生成。
	if p.gateway != nil {
		if tenantID, ok := types.TenantIDFromContext(ctx); ok && tenantID != 0 {
			_ = p.gateway.RecordUsage(ctx, tenantID, chatManage.ChatModelID,
				int64(chatResponse.Usage.PromptTokens), int64(chatResponse.Usage.CompletionTokens))
		}
	}

	pipelineInfo(ctx, "Completion", "output", map[string]interface{}{
		"answer_preview":    chatResponse.Content,
		"finish_reason":     chatResponse.FinishReason,
		"completion_tokens": chatResponse.Usage.CompletionTokens,
		"prompt_tokens":     chatResponse.Usage.PromptTokens,
	})
	chatManage.ChatResponse = chatResponse
	return next()
}
