package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func emptyFinalPayload() mockResponse {
	return mockResponse{chunks: []types.StreamResponse{{
		ResponseType: types.ResponseTypeAnswer, Done: true, FinishReason: "stop",
		Usage: &types.TokenUsage{CompletionTokens: 44},
	}}}
}

func TestFinalEmptyDeliveryRecoveryDecodesAndReviewsWithoutChangingThinking(t *testing.T) {
	const answer = "The available evidence is incomplete."
	model := &mockChat{responses: []mockResponse{
		emptyFinalPayload(),
		deliveryReply(`{"answer":"`+answer+`"}`, "stop", types.TokenUsage{CompletionTokens: 12}),
		completenessVerdict(true),
	}}
	engine := newTestEngine(t, model)
	disabled := false
	engine.config.Thinking, engine.config.ReasoningEffort = &disabled, "off"
	engine.config.MaxCompletionTokens = 512
	messages := []chat.Message{
		{Role: "system", Content: "Use the actual evidence."},
		{Role: "user", Content: "Summarize the result and any missing verification."},
		{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: "actual-call", Type: "function", Function: chat.FunctionCall{Name: "read_source", Arguments: `{}`}}}},
		{Role: "tool", ToolCallID: "actual-call", Name: "read_source", Content: "An observation was read; verification is pending."},
	}
	before, err := json.Marshal(messages)
	require.NoError(t, err)
	recorder := &terminalAnswerRecorder{}
	recorder.attach(engine.eventBus)
	state := &types.AgentState{}
	require.NoError(t, engine.streamFinalAnswerToEventBus(t.Context(), "Summarize the result.", state, "session", messages))
	require.Len(t, model.calls, 3, "one empty generation, one repair, and the existing completeness check")
	require.Empty(t, model.opts[0].Format)
	require.JSONEq(t, string(reviewedAnswerSchema), string(model.opts[1].Format))
	for _, opts := range model.opts[:2] {
		require.NotNil(t, opts.Thinking)
		require.False(t, *opts.Thinking)
		require.Equal(t, chat.ReasoningEffort("off"), opts.ReasoningEffort)
		require.Equal(t, "none", opts.ToolChoice)
		require.Equal(t, 512, opts.MaxCompletionTokens)
	}
	require.False(t, engine.structuredReviewMode, "delivery recovery is local to this finalization")
	require.Equal(t, 1, engine.turnReview.Repairs)
	require.Equal(t, 1, engine.turnReview.Checks)
	require.Equal(t, 56, state.TurnUsage.CompletionTokens)
	for _, original := range model.calls[0] {
		require.Contains(t, model.calls[1], original, "recovery preserves user/tool roles, contents and call IDs")
	}
	contracts := 0
	for _, message := range model.calls[1] {
		if message.Role == "system" && message.Content == reviewedAnswerDeliveryInstruction().Content {
			contracts++
		}
	}
	require.Equal(t, 1, contracts)
	var completenessInput struct {
		Candidate string `json:"candidate_answer"`
	}
	require.NoError(t, json.Unmarshal([]byte(model.calls[2][1].Content), &completenessInput))
	require.Equal(t, answer, completenessInput.Candidate, "review receives decoded prose")
	require.Equal(t, answer, state.FinalAnswer)
	done, visible := recorder.snapshot()
	require.Equal(t, 1, done)
	require.Equal(t, answer, visible, "neither the empty draft nor JSON framing is published")
	after, err := json.Marshal(messages)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after))
}

func TestFinalEmptyDeliveryRecoveryCannotBypassNumericGuardOrAddAttempts(t *testing.T) {
	model := &mockChat{responses: []mockResponse{
		emptyFinalPayload(),
		deliveryReply(`{"answer":"The result is 999."}`, "stop", types.TokenUsage{}),
		deliveryReply(`{"answer":"The result is 999."}`, "stop", types.TokenUsage{}),
	}}
	engine := newTestEngine(t, model)
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{
		Name: "structured_analyze_table", Result: &types.ToolResult{Success: true,
			Output: `{"kind":"structured_analysis","status":"ok","rows":[[30]]}`},
	}}}}}
	require.NoError(t, engine.streamFinalAnswerToEventBus(t.Context(), "Report the observed value.", state, "session", emptyMessages()))
	require.Len(t, model.calls, 3)
	require.Equal(t, 2, engine.turnReview.Repairs)
	require.Equal(t, 2, engine.turnReview.Checks)
	require.Equal(t, finalNumericFailureFallback, state.FinalAnswer)
	require.NotContains(t, state.FinalAnswer, "999")
	for _, opts := range model.opts[1:] {
		require.JSONEq(t, string(reviewedAnswerSchema), string(opts.Format))
	}
}

func TestFinalEmptyDeliveryRecoveryDoesNotReclassifyOtherResponses(t *testing.T) {
	for _, test := range []struct {
		name      string
		mutate    func(*mockResponse)
		wantError bool
	}{
		{"no usage", func(r *mockResponse) { r.chunks[0].Usage = nil }, false},
		{"no completion tokens", func(r *mockResponse) { r.chunks[0].Usage.CompletionTokens = 0 }, false},
		{"length stop", func(r *mockResponse) { r.chunks[0].FinishReason = "length" }, true},
		{"unknown stop", func(r *mockResponse) { r.chunks[0].FinishReason = "unknown" }, false},
		{"unsupported nonempty answer", func(r *mockResponse) { r.chunks[0].Content = "The result is 999." }, false},
		{"reasoning only", func(r *mockResponse) {
			r.chunks = append([]types.StreamResponse{{ResponseType: types.ResponseTypeThinking, Content: "Still considering the evidence."}}, r.chunks...)
		}, false},
		{"tool request", func(r *mockResponse) {
			r.chunks[0].ToolCalls = []types.LLMToolCall{{ID: "unexecuted", Type: "function", Function: types.FunctionCall{Name: "read_source", Arguments: `{}`}}}
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := emptyFinalPayload()
			test.mutate(&response)
			model := &mockChat{responses: []mockResponse{response, response, response}}
			engine := newTestEngine(t, model)
			state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{
				Name: "structured_analyze_table", Result: &types.ToolResult{Success: true,
					Output: `{"kind":"structured_analysis","status":"ok","rows":[[30]]}`},
			}}}}}
			err := engine.streamFinalAnswerToEventBus(t.Context(), "Report the result.", state, "session", emptyMessages())
			if test.wantError {
				require.ErrorContains(t, err, "context window")
				require.Len(t, model.calls, 1, "truncation retains its existing error path")
				require.Empty(t, model.opts[0].Format)
				require.Empty(t, state.FinalAnswer)
				return
			}
			require.NoError(t, err)
			require.Len(t, model.calls, 3)
			for _, opts := range model.opts {
				require.Empty(t, opts.Format)
			}
			require.Contains(t, []string{incompleteAnswerFallback, finalNumericFailureFallback}, state.FinalAnswer)
		})
	}
}

func TestFinalEmptyDeliveryRecoveryDoesNotDuplicateExistingContract(t *testing.T) {
	model := &mockChat{responses: []mockResponse{
		emptyFinalPayload(), emptyFinalPayload(),
		deliveryReply(`{"answer":"The available evidence is incomplete."}`, "stop", types.TokenUsage{}),
		completenessVerdict(true),
	}}
	engine := newTestEngine(t, model)
	engine.structuredReviewMode = true
	state := &types.AgentState{}
	require.NoError(t, engine.streamFinalAnswerToEventBus(t.Context(), "Report the result.", state, "session", emptyMessages()))
	require.Len(t, model.calls, 4, "three final generations and one existing completeness check")
	for i, messages := range model.calls[:3] {
		require.JSONEq(t, string(reviewedAnswerSchema), string(model.opts[i].Format))
		contracts := 0
		for _, message := range messages {
			if message.Role == "system" && message.Content == reviewedAnswerDeliveryInstruction().Content {
				contracts++
			}
		}
		require.Equal(t, 1, contracts)
	}
	require.Equal(t, 2, engine.turnReview.Repairs)
	require.Equal(t, "The available evidence is incomplete.", state.FinalAnswer)
}

func TestFinalEmptyDeliveryRecoveryKeepsExistingModeAndExhaustedBudget(t *testing.T) {
	for _, existingMode := range []bool{false, true} {
		model := &mockChat{responses: []mockResponse{emptyFinalPayload()}}
		engine := newTestEngine(t, model)
		engine.structuredReviewMode = existingMode
		engine.turnReview.Repairs = maxTurnCandidateRepairs
		state := &types.AgentState{}
		require.NoError(t, engine.streamFinalAnswerToEventBus(t.Context(), "Report the result.", state, "session", emptyMessages()))
		require.Len(t, model.calls, 1, "delivery recovery cannot create a new repair allowance")
		require.Equal(t, existingMode, isReviewedAnswerFormat(model.opts[0].Format))
		require.Equal(t, incompleteAnswerFallback, state.FinalAnswer)
	}
}

func TestFinalEmptyDeliveryRecoveryKeepsOriginalDeadline(t *testing.T) {
	model := &standardStageChat{blockFinalAt: 2, mockChat: mockChat{responses: []mockResponse{emptyFinalPayload()}}}
	engine := newTestEngine(t, model)
	engine.config.AnswerReviewMode = "standard"
	engine.taskPlan = explicitTextDeliveryPlan()
	engine.taskPlan.Evidence = []string{"Use the evidence already obtained."}
	state := &types.AgentState{}
	deadline := time.Now().Add(50 * time.Millisecond)
	ctx := context.WithValue(t.Context(), standardAnswerFinalDeadlineKey{}, deadline)
	err := engine.streamFinalAnswerToEventBus(ctx, "Report the result.", state, "session", emptyMessages())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, 2, model.finalCalls)
	require.Equal(t, []time.Time{deadline, deadline}, model.deadlines)
	require.Equal(t, 1, engine.turnReview.Repairs)
	require.Empty(t, state.FinalAnswer)
	require.False(t, state.IsComplete)
}

func TestFinalEmptyDeliveryRecoveryDoesNotRetryStreamErrors(t *testing.T) {
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{
		ResponseType: types.ResponseTypeError, Content: "upstream connection failed", Done: true,
		Usage: &types.TokenUsage{CompletionTokens: 44},
	}}}}}
	engine := newTestEngine(t, model)
	state := &types.AgentState{}
	err := engine.streamFinalAnswerToEventBus(t.Context(), "Report the result.", state, "session", emptyMessages())
	require.ErrorContains(t, err, "upstream connection failed")
	require.False(t, errors.Is(err, context.DeadlineExceeded))
	require.Len(t, model.calls, 1)
	require.Empty(t, model.opts[0].Format)
	require.Zero(t, engine.turnReview.Repairs)
	require.Empty(t, state.FinalAnswer)
}
