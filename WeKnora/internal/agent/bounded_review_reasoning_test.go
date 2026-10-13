package agent

import (
	"context"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestBoundedSynthesisAndRepairUseMinimumSupportedReasoning(t *testing.T) {
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: "根据已取得证据回答。", Done: true, FinishReason: "stop"}}}}}
	e := newTestEngine(t, model)
	e.config.ReviewReasoningEffort = "minimal"
	require.NoError(t, e.streamFinalAnswerToEventBus(context.Background(), "问题", &types.AgentState{}, "s", emptyMessages()))
	require.Equal(t, chat.ReasoningEffort("minimal"), model.opts[0].ReasoningEffort)
	require.LessOrEqual(t, model.opts[0].MaxCompletionTokens, 4096)

	model = &mockChat{responses: []mockResponse{
		{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeThinking, Content: strings.Repeat("推理", 300)}, {ResponseType: types.ResponseTypeAnswer, Content: "-", Done: true, FinishReason: "stop"}}},
		{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: "完整回答。", Done: true, FinishReason: "stop"}}},
	}}
	e = newTestEngine(t, model)
	e.config.ReviewReasoningEffort = "minimal"
	e.config.ReasoningEffort = "high"
	_, err := e.executeLoop(context.Background(), &types.AgentState{}, "问题", emptyMessages(), nil, "s", "m")
	require.NoError(t, err)
	require.Equal(t, chat.ReasoningEffort("high"), model.opts[0].ReasoningEffort, "primary selected reasoning is preserved")
	require.Equal(t, chat.ReasoningEffort("minimal"), model.opts[1].ReasoningEffort, "repair uses bounded minimum")
}
