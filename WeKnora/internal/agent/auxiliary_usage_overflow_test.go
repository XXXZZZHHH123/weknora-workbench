package agent

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestRunReActIterationRetainsRepairUsageWhenOverflowReplacesResponse(t *testing.T) {
	malformed := `{"answer":"broken <ref id="c1"/>"}`
	model := &mockChat{responses: []mockResponse{
		deliveryReply(malformed, "stop", types.TokenUsage{PromptTokens: 150, CompletionTokens: 3, TotalTokens: 153}),
		deliveryReply(`{"answer":"unverified draft"}`, "stop", types.TokenUsage{PromptTokens: 20, CompletionTokens: 4, TotalTokens: 24}),
		deliveryReply(`{"answer":"Hello."}`, "stop", types.TokenUsage{PromptTokens: 80, CompletionTokens: 10, TotalTokens: 90}),
	}}
	engine := newTestEngine(t, model,
		withMaxContextTokens(100), withMaxCompletionTokens(40))
	engine.structuredReviewMode = true
	state := &types.AgentState{}
	guards := &loopGuards{structuredAnswerReviewed: true}
	messages := emptyMessages()
	tools := emptyTools()

	outcome, err := engine.runReActIteration(
		context.Background(), state, &messages, tools, "session", "message", "Say hello.", guards)
	require.NoError(t, err)
	require.Equal(t, iterOutcomeBreak, outcome)
	require.Equal(t, "Hello.", state.FinalAnswer)
	require.True(t, state.IsComplete)
	require.True(t, engine.overflowRecovered, "the natural-stop primary response reports a prompt beyond the configured window")
	require.True(t, engine.reviewedAnswerDeliveryRepairUsed)
	require.Len(t, model.calls, 3,
		"calls are malformed primary, one protocol repair, and the overflow retry")

	// Both primary calls actually consumed usage. The first was superseded by
	// overflow recovery, while the retry's primary usage is aggregated once by
	// the normal final-response path; the protocol repair is counted separately.
	require.Equal(t, 250, state.TurnUsage.PromptTokens)
	require.Equal(t, 17, state.TurnUsage.CompletionTokens)
	require.Equal(t, 267, state.TurnUsage.TotalTokens)
}
