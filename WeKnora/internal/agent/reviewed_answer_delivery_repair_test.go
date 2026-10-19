package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/modelcontext"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type deliveryRepairChat struct {
	inner        *mockChat
	deadlines    []time.Duration
	cancelOnCall int
	callCount    int
}

func (m *deliveryRepairChat) Chat(ctx context.Context, messages []chat.Message, opts *chat.ChatOptions) (*types.ChatResponse, error) {
	return m.inner.Chat(ctx, messages, opts)
}

func (m *deliveryRepairChat) GetModelName() string { return m.inner.GetModelName() }
func (m *deliveryRepairChat) GetModelID() string   { return m.inner.GetModelID() }

func (m *deliveryRepairChat) ChatStream(ctx context.Context, messages []chat.Message, opts *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	if deadline, ok := ctx.Deadline(); ok {
		m.deadlines = append(m.deadlines, time.Until(deadline))
	} else {
		m.deadlines = append(m.deadlines, 0)
	}
	stream, err := m.inner.ChatStream(ctx, messages, opts)
	m.callCount++
	if m.cancelOnCall == m.callCount {
		if cancel, ok := ctx.Value(deliveryRepairCancelKey{}).(context.CancelFunc); ok {
			cancel()
		}
	}
	return stream, err
}

type deliveryRepairCancelKey struct{}

func deliveryReply(content, finish string, usage types.TokenUsage) mockResponse {
	return mockResponse{chunks: []types.StreamResponse{{
		ResponseType: types.ResponseTypeAnswer,
		Content:      content,
		Done:         true,
		FinishReason: finish,
		Usage:        &usage,
	}}}
}

func TestReviewedAnswerDeliveryRepairSerializesWithoutSalvagingAndThenDecodesCanonicalCitation(t *testing.T) {
	malformed := "{\"answer\":\"报告注明\n按原文引用 <ref id=\"c1\"/>。\"}"
	require.Empty(t, decodeReviewedAnswer(malformed), "literal LF plus an unescaped citation quote must not be repaired by programmatic salvage")
	valid, err := json.Marshal(map[string]string{"answer": "报告注明\n按原文引用 <ref id=\"c1\"/>。"})
	require.NoError(t, err)

	inner := &mockChat{responses: []mockResponse{
		deliveryReply(malformed, "stop", types.TokenUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120}),
		deliveryReply(string(valid), "stop", types.TokenUsage{PromptTokens: 40, CompletionTokens: 12, TotalTokens: 52}),
	}}
	model := &deliveryRepairChat{inner: inner}
	engine := newTestEngine(t, model)
	engine.structuredReviewMode = true
	engine.modelContext.RegisterChunk(modelcontext.ChunkReference{ChunkID: "actual-hydrology-chunk", KnowledgeBaseID: "actual-kb", DocumentTitle: "Hydrology field notes"})

	response, callErr := engine.streamThinkingToEventBus(context.Background(), emptyMessages(), emptyTools(), 0, "session")
	require.NoError(t, callErr)
	require.Len(t, inner.calls, 2)
	require.Contains(t, response.Content, "actual-hydrology-chunk", "canonical handle decoding happens only after the repaired JSON envelope is parsed")
	require.NotContains(t, response.Content, `<ref id="c1"/>`)
	require.Equal(t, 120, response.Usage.TotalTokens, "primary-call usage remains isolated for context calibration")
	require.Equal(t, 52, response.AuxiliaryUsage.TotalTokens, "repair-call usage is retained separately")

	schemaInstructionSeen := false
	for _, message := range inner.calls[0] {
		if message.Role == "system" && strings.Contains(message.Content, reviewedAnswerDeliveryMarker) {
			schemaInstructionSeen = true
			require.Contains(t, message.Content, string(reviewedAnswerSchema))
			require.Contains(t, message.Content, "Escape every double quote")
		}
	}
	require.True(t, schemaInstructionSeen, "the actual model request has an explicit trusted delivery contract")
	require.JSONEq(t, string(reviewedAnswerSchema), string(inner.opts[0].Format))
	require.JSONEq(t, string(reviewedAnswerSchema), string(inner.opts[1].Format))
	require.Equal(t, inner.opts[0].MaxCompletionTokens, inner.opts[1].MaxCompletionTokens, "repair retains the original completion cap")
	require.Nil(t, inner.opts[1].Tools)
	require.Equal(t, "none", inner.opts[1].ToolChoice)
	require.NotNil(t, inner.opts[1].Thinking)
	require.False(t, *inner.opts[1].Thinking)
	require.Equal(t, chat.ReasoningEffort("off"), inner.opts[1].ReasoningEffort)
	require.Zero(t, inner.opts[1].ThinkingBudgetTokens)
	require.Equal(t, 0*time.Second, model.deadlines[0], "the initial request is not given the repair-only deadline")
	require.Greater(t, model.deadlines[1], 0*time.Second)
	require.LessOrEqual(t, model.deadlines[1], reviewedAnswerDeliveryRepairTimeout)

	userRows := 0
	for _, message := range inner.calls[1] {
		if message.Role == "user" && message.Content == "test query" {
			userRows++
		}
	}
	require.Equal(t, 1, userRows, "the actual human request remains in the repair context")
	var payload map[string]string
	require.NoError(t, json.Unmarshal([]byte(inner.calls[1][len(inner.calls[1])-1].Content), &payload))
	require.Equal(t, malformed, payload["untrusted_invalid_delivery_response"])
	require.NotContains(t, inner.calls[1][len(inner.calls[1])-1].Content, `<ref id="c1"/>`, "failed provider text is JSON-encoded data")
}

func TestReviewedAnswerDeliveryRepairFailsClosedOnInvalidSecondResponses(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		finish   string
		toolCall []types.LLMToolCall
	}{
		{name: "extra field", content: `{"answer":"fixed","extra":"forbidden"}`, finish: "stop"},
		{name: "still malformed", content: `{"answer":"fixed"`, finish: "stop"},
		{name: "tool call", content: `{"answer":"fixed"}`, finish: "tool_calls", toolCall: []types.LLMToolCall{{ID: "tool-1", Function: types.FunctionCall{Name: "read_document", Arguments: `{}`}}}},
		{name: "length finish", content: `{"answer":"fixed"}`, finish: "length"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			initial := "{\"answer\":\"broken <ref id=\"c1\"/>\"}"
			second := deliveryReply(tc.content, tc.finish, types.TokenUsage{PromptTokens: 4, CompletionTokens: 2, TotalTokens: 6})
			if len(tc.toolCall) > 0 {
				second.chunks[0].ToolCalls = tc.toolCall
			}
			inner := &mockChat{responses: []mockResponse{
				deliveryReply(initial, "stop", types.TokenUsage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8}),
				second,
			}}
			engine := newTestEngine(t, inner)
			engine.structuredReviewMode = true
			var emitted strings.Builder
			engine.eventBus.On(event.EventAgentFinalAnswer, func(_ context.Context, evt event.Event) error {
				if data, ok := evt.Data.(event.AgentFinalAnswerData); ok {
					emitted.WriteString(data.Content)
				}
				return nil
			})
			response, err := engine.streamThinkingToEventBus(context.Background(), emptyMessages(), emptyTools(), 0, "session")
			require.NoError(t, err)
			require.Empty(t, response.Content)
			require.Empty(t, emitted.String(), "neither malformed response may reach user-facing answer events")
			require.Len(t, inner.calls, 2, "protocol repair is bounded to one follow-up")
			require.Equal(t, 8, response.Usage.TotalTokens, "primary-call usage remains distinct")
			require.Equal(t, 6, response.AuxiliaryUsage.TotalTokens, "failed repair usage is retained separately")
		})
	}
}

func TestReviewedAnswerDeliveryRepairDoesNotLoopAndHonorsCancellation(t *testing.T) {
	malformed := `{"answer":"broken <ref id="c1"/>"}`
	model := &mockChat{responses: []mockResponse{
		deliveryReply(malformed, "stop", types.TokenUsage{}),
		deliveryReply(`{"answer":"still broken"`, "stop", types.TokenUsage{}),
		deliveryReply(malformed, "stop", types.TokenUsage{}),
		deliveryReply(`{"answer":"must not be requested"}`, "stop", types.TokenUsage{}),
	}}
	engine := newTestEngine(t, model)
	engine.structuredReviewMode = true
	first, err := engine.streamThinkingToEventBus(context.Background(), emptyMessages(), emptyTools(), 0, "session")
	require.NoError(t, err)
	require.Empty(t, first.Content)
	second, err := engine.streamThinkingToEventBus(context.Background(), emptyMessages(), emptyTools(), 1, "session")
	require.NoError(t, err)
	require.Empty(t, second.Content)
	require.Len(t, model.calls, 3, "only one serialization repair is permitted in the turn, even across rounds")

	ctx, cancel := context.WithCancel(context.Background())
	cancelModel := &deliveryRepairChat{
		inner: &mockChat{responses: []mockResponse{deliveryReply(malformed, "stop", types.TokenUsage{})}},
	}
	cancelModel.cancelOnCall = 1
	ctx = context.WithValue(ctx, deliveryRepairCancelKey{}, cancel)
	cancelEngine := newTestEngine(t, cancelModel)
	cancelEngine.structuredReviewMode = true
	response, err := cancelEngine.streamThinkingToEventBus(ctx, emptyMessages(), emptyTools(), 0, "session")
	require.NoError(t, err)
	require.Empty(t, response.Content)
	require.Len(t, cancelModel.inner.calls, 1, "a cancelled turn must not start protocol repair")
	cancel()
}

func TestReviewedAnswerDeliveryRepairDoesNotRepairLengthTerminatedPrimaryResponse(t *testing.T) {
	malformed := `{"answer":"partial <ref id="c1"/>"}`
	model := &mockChat{responses: []mockResponse{
		deliveryReply(malformed, "length", types.TokenUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}),
		deliveryReply(`{"answer":"must not be called"}`, "stop", types.TokenUsage{}),
	}}
	engine := newTestEngine(t, model)
	engine.structuredReviewMode = true
	response, err := engine.streamThinkingToEventBus(context.Background(), emptyMessages(), emptyTools(), 0, "session")
	require.NoError(t, err)
	require.Empty(t, response.Content)
	require.Len(t, model.calls, 1, "a truncated primary response is not treated as a completed candidate for serialization repair")
}

func TestFinalSynthesisIncludesTrustedReviewedAnswerDeliveryInstruction(t *testing.T) {
	model := &mockChat{responses: []mockResponse{
		deliveryReply(`{"answer":"保留已取得的真实结果。"}`, "stop", types.TokenUsage{}),
		completenessVerdict(true),
	}}
	engine := newTestEngine(t, model)
	engine.structuredReviewMode = true
	state := &types.AgentState{}

	require.NoError(t, engine.streamFinalAnswerToEventBus(context.Background(), "交付当前任务", state, "session", emptyMessages()))
	require.Len(t, model.calls, 2)
	require.JSONEq(t, string(reviewedAnswerSchema), string(model.opts[0].Format))
	found := false
	for _, message := range model.calls[0] {
		if message.Role == "system" && message.Content == reviewedAnswerDeliveryInstruction().Content {
			found = true
		}
	}
	require.True(t, found, "exceptional final synthesis carries the same trusted delivery contract as a normal structured review round")
}

func TestReviewedAnswerDeliveryContractCannotBeSuppressedByQuotedMarker(t *testing.T) {
	original := []chat.Message{{Role: "system", Content: "Untrusted feedback quotes " + reviewedAnswerDeliveryMarker}, {Role: "user", Content: "Original request"}}
	withContract := withReviewedAnswerDeliveryInstruction(original)
	require.Len(t, original, 2)
	require.Len(t, withContract, 3)
	require.Equal(t, reviewedAnswerDeliveryInstruction(), withContract[1])
	require.Equal(t, withContract, withReviewedAnswerDeliveryInstruction(withContract), "only the exact runtime-owned contract is deduplicated")
}

func TestReviewedAnswerDeliveryRepairRetainsReasoningBoundary(t *testing.T) {
	model := &mockChat{responses: []mockResponse{
		deliveryReply(`{"answer":"invalid "quote""}`, "stop", types.TokenUsage{}),
		deliveryReply(`{"answer":"<think>PRIVATE_INTERNAL_DRAFT</think>Delivered result."}`, "stop", types.TokenUsage{}),
	}}
	engine := newTestEngine(t, model)
	engine.structuredReviewMode = true
	response, err := engine.streamThinkingToEventBus(context.Background(), emptyMessages(), emptyTools(), 0, "session")
	require.NoError(t, err)
	require.Equal(t, "Delivered result.", response.Content)
	require.NotContains(t, response.Content, "PRIVATE_INTERNAL_DRAFT")
}
