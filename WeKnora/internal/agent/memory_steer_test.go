package agent

import (
	"context"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type steerMemoryRecorder struct {
	interfaces.MemoryService
	requests []interfaces.MemoryRecallRequest
}

func (s *steerMemoryRecorder) SearchMemory(ctx context.Context, _ string, _ int) interfaces.MemorySearchResult {
	request, _ := interfaces.MemoryRecallRequestFromContext(ctx)
	s.requests = append(s.requests, request)
	return interfaces.MemorySearchResult{Available: true}
}

func TestMemorySearchUsesOnlyPersistedSteerCorrections(t *testing.T) {
	for _, tc := range []struct {
		name       string
		history    []interfaces.MemoryConversationMessage
		incomplete bool
	}{
		{name: "complete history", history: []interfaces.MemoryConversationMessage{{Role: "user", Content: "Previous authorized task"}, {Role: "assistant", Content: "Previous completed answer"}}},
		{name: "multi turn disabled"},
		{name: "incomplete remains closed", incomplete: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := newTestEngine(t, &mockChat{})
			recorder := &steerMemoryRecorder{}
			engine.toolRegistry = agenttools.NewToolRegistry()
			engine.toolRegistry.RegisterTool(agenttools.NewSearchMemoryTool(recorder))
			sink := &fakeSteerSink{queued: []map[string]interface{}{steerEntry("override", "Do not apply my earlier formatting preference to this answer")}, failPersist: true}
			engine.SetSteerSink(sink)
			original := interfaces.MemoryRecallRequest{SessionID: "session", Query: "Apply my usual formatting", ModelID: "effective-model", Conversation: tc.history, ContextIncomplete: tc.incomplete}
			ctx := interfaces.WithMemoryRecallRequest(t.Context(), original)
			state := &types.AgentState{}
			messages := []chat.Message{{Role: "user", Content: original.Query}}
			call := func() {
				result := engine.runToolCall(ctx, types.LLMToolCall{ID: "search", Function: types.FunctionCall{Name: agenttools.ToolSearchMemory, Arguments: `{"query":"usual formatting"}`}}, 0, 0, 1, "session", "assistant")
				require.NotNil(t, result.Result)
				require.True(t, result.Result.Success)
			}
			require.Zero(t, engine.drainSteerMessages(ctx, state, &messages, "session", "assistant"))
			call()
			require.Equal(t, original, recorder.requests[0], "unpersisted instructions must not enter a different model context")
			sink.failPersist = false
			require.Equal(t, 1, engine.drainSteerMessages(ctx, state, &messages, "session", "assistant"))
			sink.queued = append(sink.queued, steerEntry("follow-up", "Keep the response concise"))
			require.Equal(t, 1, engine.drainSteerMessages(ctx, state, &messages, "session", "assistant"))
			call()
			request := recorder.requests[1]
			require.Equal(t, "Keep the response concise", request.Query)
			require.Equal(t, original.ModelID, request.ModelID)
			require.Equal(t, original.SessionID, request.SessionID)
			require.Equal(t, original.ContextIncomplete, request.ContextIncomplete)
			require.Len(t, request.Conversation, len(original.Conversation)+2)
			require.Equal(t, original.Query, request.Conversation[len(original.Conversation)].Content)
			require.Equal(t, "Do not apply my earlier formatting preference to this answer", request.Conversation[len(original.Conversation)+1].Content)
			if len(original.Conversation) > 0 {
				require.Equal(t, original.Conversation, request.Conversation[:len(original.Conversation)])
			}
			unchanged, _ := interfaces.MemoryRecallRequestFromContext(ctx)
			require.Equal(t, original, unchanged, "tool calls must not mutate the base server turn")
			call()
			require.Equal(t, request, recorder.requests[2], "repeated tools must not append the same steer twice")
		})
	}
}

func TestMemorySteerContextCannotBorrowAnotherSession(t *testing.T) {
	engine := newTestEngine(t, &mockChat{})
	engine.memoryAcceptedSteers = []string{"A persisted correction"}
	original := interfaces.MemoryRecallRequest{SessionID: "original", ModelID: "model", Query: "original task"}
	ctx := interfaces.WithMemoryRecallRequest(t.Context(), original)
	request, ok := interfaces.MemoryRecallRequestFromContext(engine.memoryContextForTools(ctx, "another"))
	require.True(t, ok)
	require.True(t, request.ContextIncomplete)
	require.Equal(t, original.Query, request.Query)
	require.Empty(t, request.Conversation)
	empty := engine.memoryContextForTools(t.Context(), "session")
	_, ok = interfaces.MemoryRecallRequestFromContext(empty)
	require.False(t, ok, "accepted steer cannot invent a missing authorized server request")
}
