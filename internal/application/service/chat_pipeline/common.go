package chatpipeline

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/Tencent/WeKnora/internal/common"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

var regThinkTags = regexp.MustCompile(`(?s)<think>.*?</think>`)

// pipelineInfo logs pipeline info level entries.
func pipelineInfo(ctx context.Context, stage, action string, fields map[string]interface{}) {
	common.PipelineInfo(ctx, stage, action, fields)
}

// pipelineWarn logs pipeline warning level entries.
func pipelineWarn(ctx context.Context, stage, action string, fields map[string]interface{}) {
	common.PipelineWarn(ctx, stage, action, fields)
}

// pipelineError logs pipeline error level entries.
func pipelineError(ctx context.Context, stage, action string, fields map[string]interface{}) {
	common.PipelineError(ctx, stage, action, fields)
}

// prepareChatModel shared logic to prepare chat model and options
// it gets the chat model and sets up the chat options based on the chat manage.
func prepareChatModel(ctx context.Context, modelService interfaces.ModelService,
	chatManage *types.ChatManage,
) (chat.Chat, *chat.ChatOptions, error) {
	chatModel, err := prepareChatModelWithFallback(ctx, modelService, chatManage)
	if err != nil {
		logger.Errorf(ctx, "Failed to get chat model: %v", err)
		return nil, nil, err
	}

	effort := chat.SanitizeReasoningEffort(ctx, chatManage.SummaryConfig.ReasoningEffort, "session summary config")
	opt := &chat.ChatOptions{
		Temperature:         chatManage.SummaryConfig.Temperature,
		TopP:                chatManage.SummaryConfig.TopP,
		Seed:                chatManage.SummaryConfig.Seed,
		MaxTokens:           chatManage.SummaryConfig.MaxTokens,
		MaxCompletionTokens: chatManage.SummaryConfig.MaxCompletionTokens,
		FrequencyPenalty:    chatManage.SummaryConfig.FrequencyPenalty,
		PresencePenalty:     chatManage.SummaryConfig.PresencePenalty,
		Thinking:            chatManage.SummaryConfig.Thinking,
		ReasoningEffort:     effort,
		PromptCacheKey:      chatManage.SessionID,
	}
	if level, requested := opt.Reasoning(); requested {
		pipelineInfo(ctx, "Stream", "thinking_option", map[string]interface{}{
			"enabled": level.Enabled(),
			"level":   string(level),
		})
	}

	return chatModel, opt, nil
}

// prepareMessagesWithHistory prepare complete messages including history.
// When SystemPromptOverride is set (e.g. by intent-specific prompt logic),
// it takes precedence over the default SummaryConfig.Prompt.
func prepareMessagesWithHistory(chatManage *types.ChatManage) []chat.Message {
	base := chatManage.SummaryConfig.Prompt
	if chatManage.SystemPromptOverride != "" {
		base = chatManage.SystemPromptOverride
	}
	systemPrompt := types.RenderPromptPlaceholders(base, types.PlaceholderValues{
		"query":    chatManage.Query,
		"language": chatManage.Language,
		"contexts": chatManage.RenderedContexts,
	})
	systemPrompt += "\n\n" + types.SourceDataBoundaryPrompt + "\n\n" + types.SourcedAnswerOutputPrompt
	// Memory goes at the end of the system prompt, after the retrieved-context
	// placeholders have been rendered, so a remembered sentence can never be
	// substituted into prompt structure.
	systemPrompt += chatManage.MemoryPrompt

	chatMessages := []chat.Message{
		{Role: "system", Content: systemPrompt},
	}

	chatMessages = AppendHistoryMessages(chatMessages, chatManage.History)

	// Add current user message. Only include images when the chat model supports
	// vision; non-vision models rely on the text description in UserContent.
	userMsg := chat.Message{Role: "user", Content: chatManage.UserContent}
	if chatManage.ChatModelSupportsVision && len(chatManage.Images)+len(chatManage.ContextImages) > 0 {
		// The user's own images first, then the retrieved ones the content
		// says are attached.
		userMsg.Images = append(slices.Clone(chatManage.Images), chatManage.ContextImages...)
	}
	chatMessages = append(chatMessages, userMsg)

	return chatMessages
}

func withPromptCacheMetadata(
	ctx context.Context,
	chatModel chat.Chat,
	messages []chat.Message,
	opts *chat.ChatOptions,
	purpose string,
) context.Context {
	prefixFingerprint := chat.PromptPrefixFingerprint(messages, opts)
	_ = chatModel // model identity is already captured by the usage sink
	return types.WithLLMCallMetadata(ctx, purpose, prefixFingerprint)
}

// AppendHistoryMessages appends prior Q&A rounds in chronological order.
// History is already filtered and truncated upstream by the load_history plugin.
func AppendHistoryMessages(messages []chat.Message, history []*types.History) []chat.Message {
	for _, history := range history {
		messages = append(messages, chat.Message{Role: "user", Content: history.Query})
		messages = append(messages, chat.Message{Role: "assistant", Content: history.Answer})
	}
	return messages
}

// loadAndProcessHistory fetches recent messages, groups them into Q&A pairs,
// strips <think> tags from assistant answers, sorts by recency, and limits to maxRounds.
// fetchCount controls how many raw messages to fetch (typically maxRounds*2+10).
func loadAndProcessHistory(
	ctx context.Context,
	messageService interfaces.MessageService,
	sessionID string,
	maxRounds int,
	fetchCount int,
) ([]*types.History, error) {
	history, err := messageService.GetRecentMessagesBySession(ctx, sessionID, fetchCount)
	if err != nil {
		return nil, err
	}

	historyMap := make(map[string]*types.History)
	for _, message := range history {
		h, ok := historyMap[message.RequestID]
		if !ok {
			h = &types.History{}
		}
		if message.Role == "user" {
			// RenderedContent is a snapshot of the prompt/context format used by
			// the original turn. Replaying it would mix legacy <context id="…">
			// envelopes and old citation instructions into the current protocol.
			// Historical references are carried separately in KnowledgeReferences
			// and can be re-merged into this turn's freshly rendered context.
			h.Query = message.Content
			if strings.TrimSpace(h.Query) == "" && (len(message.Images) > 0 || len(message.Attachments) > 0) {
				h.Query = types.UploadOnlyQuestion(types.LanguageFromContextOrDefault(ctx))
			}
			h.CreateAt = message.CreatedAt
			if desc := extractImageCaptions(message.Images); desc != "" {
				h.Query += "\n\n[用户上传图片内容]\n" + desc
			}
			if len(message.Attachments) > 0 {
				h.Query += message.Attachments.BuildPrompt()
			}
		} else {
			h.Answer = regThinkTags.ReplaceAllString(message.Content, "")
			h.KnowledgeReferences = message.KnowledgeReferences
		}
		historyMap[message.RequestID] = h
	}

	historyList := make([]*types.History, 0, len(historyMap))
	for _, h := range historyMap {
		if h.Answer != "" && h.Query != "" {
			historyList = append(historyList, h)
		}
	}

	sort.Slice(historyList, func(i, j int) bool {
		return historyList[i].CreateAt.After(historyList[j].CreateAt)
	})

	if len(historyList) > maxRounds {
		historyList = historyList[:maxRounds]
	}

	slices.Reverse(historyList)
	return historyList, nil
}

// extractImageCaptions concatenates non-empty Caption fields from stored
// message images. Used when loading history so that previous turns' image
// descriptions are visible to the model.
func extractImageCaptions(images types.MessageImages) string {
	var parts []string
	for _, img := range images {
		if img.Caption != "" {
			parts = append(parts, img.Caption)
		}
	}
	return strings.Join(parts, "\n")
}

// ---------------------------------------------------------------------------
// Concurrency utilities
// ---------------------------------------------------------------------------

// ParallelTask represents a named unit of concurrent work.
type ParallelTask struct {
	Name string
	Run  func() *PluginError
}

// RunParallel executes tasks concurrently.
// Returns a map of task name → error for tasks that returned non-nil errors.
func RunParallel(tasks ...ParallelTask) map[string]*PluginError {
	errs := make(map[string]*PluginError)
	var mu sync.Mutex
	var wg sync.WaitGroup

	wg.Add(len(tasks))
	for _, task := range tasks {
		go func(t ParallelTask) {
			defer wg.Done()
			if err := t.Run(); err != nil {
				mu.Lock()
				errs[t.Name] = err
				mu.Unlock()
			}
		}(task)
	}
	wg.Wait()
	return errs
}

// ParallelMap applies fn to each element of items concurrently (up to
// maxWorkers goroutines) and returns results in the same order as items.
// If maxWorkers <= 0, concurrency is unbounded (one goroutine per item).
func ParallelMap[T, R any](items []T, maxWorkers int, fn func(int, T) R) []R {
	n := len(items)
	if n == 0 {
		return nil
	}
	results := make([]R, n)

	if maxWorkers <= 0 || maxWorkers > n {
		maxWorkers = n
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, maxWorkers)

	for i, item := range items {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, it T) {
			defer func() { <-sem; wg.Done() }()
			results[idx] = fn(idx, it)
		}(i, item)
	}
	wg.Wait()
	return results
}

// prepareChatModelWithFallback resolves the chat model, degrading when the
// requested one cannot be loaded (§16.5 chat-side fallback).
//
// 为什么只在**建立流之前**降级：流式回答一旦开始输出，换模型会把两段不同
// 模型的文本拼成一条回答——读者看不出拼接点，但质量与责任都断了。因此这里
// 只处理"模型加载失败"这种零副作用的失败；流中途的失败必须如实报错。
// 判定逻辑在 types.CanFallbackChat 里，避免调用点各自实现一套。
//
// 候选由 chatManage.ChatFallbackModelIDs 提供（调用方从降级链解析，空表示
// 未配置）。未配置时只尝试请求的模型，行为与改动前一致。
func prepareChatModelWithFallback(
	ctx context.Context, modelService interfaces.ModelService,
	chatManage *types.ChatManage,
) (chat.Chat, error) {
	requested := strings.TrimSpace(chatManage.ChatModelID)
	candidates := types.NormalizeChatFallbackCandidates(requested, chatManage.ChatFallbackModelIDs)
	if len(candidates) == 0 {
		// 空模型 ID 仍然交给 modelService 判定：它比这里更清楚"空 ID 是否
		// 有默认值可用"，而提前报错会把"调用方没填"变成"实现不可用"。
		// 未配置降级链的部署保持改动前同样的调用序列。
		candidates = []string{requested}
	}

	trace := &types.ChatFallbackTrace{RequestedModelID: requested}
	var lastErr error
	for level, modelID := range candidates {
		chatModel, err := modelService.GetChatModel(chatManage.ChatModelContext(ctx), modelID)
		if err != nil {
			trace.Attempts = append(trace.Attempts, types.ChatFallbackAttempt{
				ModelID: modelID, Level: level,
				Phase: types.ChatFallbackBeforeStream, Err: err.Error(),
			})
			lastErr = err
			// 加载失败没有副作用：按判定逻辑必然允许换下一级，这里仍然走
			// 一次判定，让"什么时候可以换"只有一处定义。
			if decision := types.CanFallbackChat(types.ChatFallbackBeforeStream, false); !decision.Allowed {
				return nil, fmt.Errorf("chat model %s unavailable and fallback refused: %s",
					modelID, decision.Reason)
			}
			logger.Warnf(ctx, "chat model %s unavailable at level %d: %v", modelID, level, err)
			continue
		}

		trace.ServedBy = modelID
		trace.Attempts = append(trace.Attempts,
			types.ChatFallbackAttempt{ModelID: modelID, Level: level})
		trace.Degraded = modelID != requested
		if trace.Degraded {
			// 降级是成本与质量事件：留痕，事后能解释"为什么这次答案风格变了"。
			logger.Warnf(ctx, "answer served by a fallback model: %s",
				types.DescribeChatFallback(*trace))
			chatManage.ChatFallbackTrace = trace
		} else {
			// 正常路径不携带 trace：它不是每次回答都需要的字段。
			chatManage.ChatFallbackTrace = nil
		}
		return chatModel, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no chat model is available")
	}
	return nil, lastErr
}
