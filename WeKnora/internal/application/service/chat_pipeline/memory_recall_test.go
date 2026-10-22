package chatpipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

// stubMemoryService returns a fixed recall so the test can assert what the
// pipeline does with it, rather than re-testing the memory service.
type stubMemoryService struct {
	interfaces.MemoryService

	recall          interfaces.MemoryRecall
	lastQuery       string
	recallCall      int
	lastTurnRequest interfaces.MemoryRecallRequest
	requestContext  interfaces.MemoryRecallRequest
}

func (s *stubMemoryService) Recall(_ context.Context, query string) interfaces.MemoryRecall {
	s.recallCall++
	s.lastQuery = query
	return s.recall
}

func (s *stubMemoryService) RecallForTurn(ctx context.Context, request interfaces.MemoryRecallRequest) interfaces.MemoryRecall {
	s.recallCall++
	s.lastQuery = request.Query
	s.lastTurnRequest = request
	s.requestContext, _ = interfaces.MemoryRecallRequestFromContext(ctx)
	return s.recall
}

func (s *stubMemoryService) ScheduleExtraction(context.Context, string, string, string) {}

func (s *stubMemoryService) Handle(context.Context, *asynq.Task) error { return nil }

func newMemoryRecallPlugin(memoryService interfaces.MemoryService) *PluginMemoryRecall {
	return NewPluginMemoryRecall(NewEventManager(), memoryService, nil)
}

// TestMemoryReachesTheMessagesSentToTheModel is the assertion that matters:
// recalling memory is pointless if it never lands in the request. It walks the
// recall stage and then the same message assembly the completion plugins use.
func TestMemoryReachesTheMessagesSentToTheModel(t *testing.T) {
	memoryService := &stubMemoryService{
		recall: interfaces.MemoryRecall{
			Prompt: types.WrapMemoryForPrompt("Preferences:\n- 回答请直接给结论", ""),
			Items: []*types.MemoryItem{
				{ID: "m1", Kind: types.MemoryKindPreference, Content: "回答请直接给结论"},
			},
		},
	}
	plugin := newMemoryRecallPlugin(memoryService)

	chatManage := &types.ChatManage{}
	chatManage.Query = "帮我看看这个报错"
	chatManage.UserContent = "帮我看看这个报错"
	chatManage.SummaryConfig.Prompt = "你是一个助手。"

	nextCalled := false
	err := plugin.OnEvent(t.Context(), types.MEMORY_RECALL, chatManage, func() *PluginError {
		nextCalled = true
		return nil
	})
	require.Nil(t, err)
	require.True(t, nextCalled, "the recall stage must never stop the pipeline")
	require.Equal(t, "帮我看看这个报错", memoryService.lastQuery)

	messages := prepareMessagesWithHistory(chatManage)
	require.NotEmpty(t, messages)
	require.Equal(t, "system", messages[0].Role)
	require.Contains(t, messages[0].Content, "回答请直接给结论",
		"the recalled memory must be present in the system message")
	require.Contains(t, messages[0].Content, "<user_memory>")
	require.True(t, strings.HasPrefix(messages[0].Content, "你是一个助手。"),
		"memory must be appended after the configured prompt, not replace it")
}

func TestMemoryIsAbsentWhenNothingRecalled(t *testing.T) {
	plugin := newMemoryRecallPlugin(&stubMemoryService{})

	chatManage := &types.ChatManage{}
	chatManage.Query = "随便问点什么"
	chatManage.SummaryConfig.Prompt = "你是一个助手。"

	require.Nil(t, plugin.OnEvent(t.Context(), types.MEMORY_RECALL, chatManage, func() *PluginError { return nil }))
	require.Empty(t, chatManage.MemoryPrompt)
	require.Empty(t, chatManage.UsedMemories)

	messages := prepareMessagesWithHistory(chatManage)
	require.NotContains(t, messages[0].Content, "<user_memory>")
}

func TestMemoryRecallEmitsWhatTheAnswerSaw(t *testing.T) {
	memoryService := &stubMemoryService{
		recall: interfaces.MemoryRecall{
			Prompt: types.WrapMemoryForPrompt("About the user:\n- 在做医疗影像", ""),
			Items: []*types.MemoryItem{
				{ID: "m1", Kind: types.MemoryKindProfile, Content: "在做医疗影像"},
			},
		},
	}
	plugin := newMemoryRecallPlugin(memoryService)

	bus := event.NewEventBus()
	var received types.UsedMemories
	bus.On(event.EventMemoryRecalled, func(_ context.Context, evt event.Event) error {
		data, ok := evt.Data.(event.MemoryRecalledData)
		require.True(t, ok)
		received, ok = data.Memories.(types.UsedMemories)
		require.True(t, ok)
		return nil
	})

	chatManage := &types.ChatManage{}
	chatManage.Query = "继续上次的事"
	chatManage.EventBus = bus.AsEventBusInterface()

	require.Nil(t, plugin.OnEvent(t.Context(), types.MEMORY_RECALL, chatManage, func() *PluginError { return nil }))

	// The chat UI promises "these are the memories this answer saw", so the
	// streamed list has to be the same one that was injected.
	require.Len(t, received, 1)
	require.Equal(t, "m1", received[0].ID)
	require.Equal(t, "在做医疗影像", received[0].Content)
	require.Equal(t, received, chatManage.UsedMemories)
}

func TestMemoryRecallToleratesNoService(t *testing.T) {
	plugin := newMemoryRecallPlugin(nil)
	chatManage := &types.ChatManage{}
	chatManage.SummaryConfig.Prompt = "你是一个助手。"
	require.Nil(t, plugin.OnEvent(t.Context(), types.MEMORY_RECALL, chatManage, func() *PluginError { return nil }))
	require.Empty(t, chatManage.MemoryPrompt)
}

func TestMemoryRecallStageIsRegisteredInThePipeline(t *testing.T) {
	// A stage nobody runs is the failure mode this whole feature has had
	// before, so assert the plugin declares the event the assembler adds.
	plugin := newMemoryRecallPlugin(&stubMemoryService{})
	require.Equal(t, []types.EventType{types.MEMORY_RECALL}, plugin.ActivationEvents())
}

type completeRecallHistory struct {
	interfaces.MessageService
	rows   []*types.Message
	err    error
	tenant uint64
	owner  string
	calls  int
}

func (s *completeRecallHistory) GetMessagesBySession(ctx context.Context, session string, page, size int) ([]*types.Message, error) {
	s.calls++
	s.tenant, _ = types.TenantIDFromContext(ctx)
	s.owner = types.SessionOwnerIDFromContext(ctx)
	if session != "current-session" {
		return nil, errors.New("wrong session")
	}
	if s.err != nil {
		return nil, s.err
	}
	if page > 1 {
		return nil, nil
	}
	return s.rows, nil
}

func TestMemoryRecallUsesCompleteSessionRatherThanHistoryTurnsTail(t *testing.T) {
	memory := &stubMemoryService{}
	history := &completeRecallHistory{rows: []*types.Message{
		{ID: "old-u", SessionID: "current-session", RequestID: "old", Role: "user", Content: "The original topic outside HistoryTurns", CreatedAt: time.Unix(1, 0)},
		{ID: "old-a", SessionID: "current-session", RequestID: "old", Role: "assistant", Content: "The original answer", IsCompleted: true, CreatedAt: time.Unix(2, 0)},
		{ID: "recent-u", SessionID: "current-session", RequestID: "recent", Role: "user", Content: "A recent question", CreatedAt: time.Unix(3, 0)},
		{ID: "recent-a", SessionID: "current-session", RequestID: "recent", Role: "assistant", Content: "A recent answer", IsCompleted: true, CreatedAt: time.Unix(4, 0)},
		{ID: "current-assistant", SessionID: "current-session", Role: "assistant", CreatedAt: time.Unix(5, 0)},
	}}
	plugin := NewPluginMemoryRecall(NewEventManager(), memory, history)
	cm := &types.ChatManage{PipelineRequest: types.PipelineRequest{SessionID: "current-session", Query: "Continue that topic", ChatModelID: "effective-chat-model", MaxRounds: 1}, PipelineState: types.PipelineState{History: []*types.History{{Query: "A recent question", Answer: "A recent answer"}}, RewriteQuery: "A rewriter cannot replace the actual user task"}, PipelineContext: types.PipelineContext{MessageID: "current-assistant"}}
	ctx := context.WithValue(t.Context(), types.TenantIDContextKey, uint64(7))
	ctx = context.WithValue(ctx, types.UserIDContextKey, "owner")
	ctx = types.WithExecutionTenant(ctx, 99)
	ctx = context.WithValue(ctx, types.SessionTenantIDContextKey, uint64(55))
	require.Nil(t, plugin.OnEvent(ctx, types.MEMORY_RECALL, cm, func() *PluginError { return nil }))
	require.Equal(t, cm.Query, memory.lastTurnRequest.Query)
	require.Equal(t, cm.ChatModelID, memory.lastTurnRequest.ModelID)
	require.Equal(t, cm.SessionID, memory.lastTurnRequest.SessionID)
	require.False(t, memory.lastTurnRequest.ContextIncomplete)
	require.Len(t, memory.lastTurnRequest.Conversation, 4)
	require.Contains(t, fmtMemoryConversation(memory.lastTurnRequest), "original topic outside HistoryTurns")
	require.Equal(t, memory.lastTurnRequest, memory.requestContext)
	require.Equal(t, uint64(7), history.tenant)
	require.Equal(t, "owner", history.owner)
	require.Len(t, cm.History, 1, "memory context does not widen the answer's configured HistoryTurns")
}

func fmtMemoryConversation(request interfaces.MemoryRecallRequest) string {
	var all strings.Builder
	for _, message := range request.Conversation {
		all.WriteString(message.Content)
	}
	return all.String()
}

func TestMemoryRecallHistoryFailureAndMultiTurnDisabledAreExplicit(t *testing.T) {
	memory := &stubMemoryService{}
	history := &completeRecallHistory{err: errors.New("not authorized")}
	plugin := NewPluginMemoryRecall(NewEventManager(), memory, history)
	cm := &types.ChatManage{PipelineRequest: types.PipelineRequest{SessionID: "current-session", Query: "Current question", ChatModelID: "model", MaxRounds: 1}, PipelineState: types.PipelineState{History: []*types.History{{Query: "window tail", Answer: "window answer"}}}}
	ctx := context.WithValue(t.Context(), types.TenantIDContextKey, uint64(7))
	ctx = context.WithValue(ctx, types.UserIDContextKey, "owner")
	require.Nil(t, plugin.OnEvent(ctx, types.MEMORY_RECALL, cm, func() *PluginError { return nil }))
	require.True(t, memory.lastTurnRequest.ContextIncomplete)
	require.Len(t, memory.lastTurnRequest.Conversation, 2)
	cm.MaxRounds = 0
	history.calls = 0
	require.Nil(t, plugin.OnEvent(ctx, types.MEMORY_RECALL, cm, func() *PluginError { return nil }))
	require.False(t, memory.lastTurnRequest.ContextIncomplete)
	require.Empty(t, memory.lastTurnRequest.Conversation)
	require.Zero(t, history.calls, "disabled multi-turn must not load any historical messages")
}

func TestEmptyMemoryRecallClearsPreviousTurnAndEmitsEmptyList(t *testing.T) {
	memory := &stubMemoryService{}
	plugin := newMemoryRecallPlugin(memory)
	bus := event.NewEventBus()
	calls := 0
	bus.On(event.EventMemoryRecalled, func(_ context.Context, evt event.Event) error {
		calls++
		recalled := evt.Data.(event.MemoryRecalledData).Memories.(types.UsedMemories)
		require.NotNil(t, recalled, "the UI needs an explicit empty array to clear old chips")
		require.Empty(t, recalled)
		return nil
	})
	cm := &types.ChatManage{PipelineState: types.PipelineState{MemoryPrompt: "stale prior memory", UsedMemories: types.UsedMemories{{ID: "old", Content: "stale"}}}}
	cm.EventBus = bus.AsEventBusInterface()
	require.Nil(t, plugin.OnEvent(t.Context(), types.MEMORY_RECALL, cm, func() *PluginError { return nil }))
	require.Empty(t, cm.MemoryPrompt)
	require.Empty(t, cm.UsedMemories)
	require.Equal(t, 1, calls)
}
