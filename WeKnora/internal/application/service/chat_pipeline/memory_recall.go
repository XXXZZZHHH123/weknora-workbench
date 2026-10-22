package chatpipeline

import (
	"context"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/memorycontext"
	"github.com/Tencent/WeKnora/internal/tracing/langfuse"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// emitMemoryRecalled tells the client which memories this turn saw. Best
// effort: an event failure must not stop the answer.
func emitMemoryRecalled(
	ctx context.Context, bus types.EventBusInterface, sessionID string, used types.UsedMemories,
) {
	if bus == nil {
		return
	}
	if err := bus.Emit(ctx, types.Event{
		Type:      types.EventType(event.EventMemoryRecalled),
		SessionID: sessionID,
		Data:      event.MemoryRecalledData{Memories: used},
	}); err != nil {
		pipelineWarn(ctx, "MemoryRecall", "emit_failed", map[string]interface{}{"error": err.Error()})
	}
}

// PluginMemoryRecall injects the user's long-term memory into the turn.
//
// It runs after LOAD_HISTORY and before retrieval so the memory is available
// to downstream stages. The service judges relevance using the actual user
// question and complete authorized conversation, including follow-up context.
type PluginMemoryRecall struct {
	memoryService  interfaces.MemoryService
	messageService interfaces.MessageService
}

func NewPluginMemoryRecall(
	eventManager *EventManager,
	memoryService interfaces.MemoryService,
	messageService interfaces.MessageService,
) *PluginMemoryRecall {
	res := &PluginMemoryRecall{memoryService: memoryService, messageService: messageService}
	eventManager.Register(res)
	return res
}

func (p *PluginMemoryRecall) ActivationEvents() []types.EventType {
	return []types.EventType{types.MEMORY_RECALL}
}

func (p *PluginMemoryRecall) OnEvent(ctx context.Context,
	eventType types.EventType, chatManage *types.ChatManage, next func() *PluginError,
) *PluginError {
	chatManage.MemoryPrompt = ""
	chatManage.UsedMemories = types.UsedMemories{}
	if p.memoryService == nil {
		emitMemoryRecalled(ctx, chatManage.EventBus, chatManage.SessionID, chatManage.UsedMemories)
		pipelineInfo(ctx, "MemoryRecall", "skip", map[string]interface{}{
			"session_id": chatManage.SessionID,
			"reason":     "no_service",
		})
		return next()
	}

	pipelineInfo(ctx, "MemoryRecall", "input", map[string]interface{}{
		"session_id":    chatManage.SessionID,
		"query_len":     len(chatManage.Query),
		"query_preview": langfuse.TruncateRunes(chatManage.Query, 200),
	})

	request := p.turnRecallRequest(ctx, chatManage)
	recall := p.memoryService.RecallForTurn(interfaces.WithMemoryRecallRequest(ctx, request), request)
	if recall.Prompt != "" {
		chatManage.MemoryPrompt = recall.Prompt
		chatManage.UsedMemories = types.UsedMemoriesFromItems(recall.Items)
	}
	emitMemoryRecalled(ctx, chatManage.EventBus, chatManage.SessionID, chatManage.UsedMemories)
	if recall.Prompt == "" {
		pipelineInfo(ctx, "MemoryRecall", "output", map[string]interface{}{
			"session_id": chatManage.SessionID,
			"items":      0,
			"injected":   false,
			"note":       "no relevant memories selected for this turn",
		})
		return next()
	}

	memoryIDs := make([]string, 0, len(recall.Items))
	for _, item := range recall.Items {
		if item != nil && item.ID != "" {
			memoryIDs = append(memoryIDs, item.ID)
		}
	}
	pipelineInfo(ctx, "MemoryRecall", "output", map[string]interface{}{
		"session_id":    chatManage.SessionID,
		"items":         len(chatManage.UsedMemories),
		"injected":      true,
		"prompt_runes":  len([]rune(recall.Prompt)),
		"memory_ids":    memoryIDs,
		"query_preview": langfuse.TruncateRunes(chatManage.Query, 200),
	})
	return next()
}

func (p *PluginMemoryRecall) turnRecallRequest(ctx context.Context, chatManage *types.ChatManage) interfaces.MemoryRecallRequest {
	request := interfaces.MemoryRecallRequest{Query: chatManage.Query, ModelID: chatManage.ChatModelID, SessionID: chatManage.SessionID}
	if chatManage.MaxRounds <= 0 {
		return request // Multi-turn history was explicitly disabled.
	}
	for _, turn := range chatManage.History {
		if turn != nil {
			request.Conversation = append(request.Conversation,
				interfaces.MemoryConversationMessage{Role: "user", Content: turn.Query},
				interfaces.MemoryConversationMessage{Role: "assistant", Content: turn.Answer})
		}
	}
	request.ContextIncomplete = true
	if p.messageService == nil {
		return request
	}
	readCtx, err := memorycontext.SessionReadContext(ctx)
	if err != nil {
		return request
	}
	conversation, err := memorycontext.LoadCompleted(readCtx, p.messageService.GetMessagesBySession, chatManage.SessionID, chatManage.MessageID, chatManage.UserMessageID)
	if err != nil {
		return request
	}
	request.Conversation, request.ContextIncomplete = conversation, false
	return request
}
