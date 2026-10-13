package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func completenessVerdict(complete bool) mockResponse {
	text := `{"complete":false}`
	if complete {
		text = `{"complete":true}`
	}
	return mockResponse{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: text, Done: true, FinishReason: "stop"}}}
}

func TestAnswerCompletenessUsesSemanticVerdictForShortFormats(t *testing.T) {
	for _, test := range []struct {
		question, answer string
		complete         bool
	}{
		{"用一个汉字确认已收到，不加其他说明。", "悉", true},
		{"Emit only the punctuation symbol separating these clauses.", ";", true},
		{"Name the selected category in one word.", "猫", true},
		{"别只回答一个字，解释依据和过程。", "是", false},
		{"请完整说明方案的每个步骤。", "6", false},
		{"请解释图表和比较结果。", "周", false},
	} {
		t.Run(test.question, func(t *testing.T) {
			verdict := completenessVerdict(test.complete)
			verdict.chunks[0].Usage = &types.TokenUsage{PromptTokens: 30, CompletionTokens: 10, TotalTokens: 40}
			model := &mockChat{responses: []mockResponse{verdict}}
			engine := newTestEngine(t, model)
			state := &types.AgentState{}
			var visible string
			engine.eventBus.On(event.EventAgentFinalAnswer, func(_ context.Context, ev event.Event) error {
				if data, ok := ev.Data.(event.AgentFinalAnswerData); ok {
					visible += data.Content
				}
				return nil
			})
			repair, err := engine.answerNeedsRepair(context.Background(), test.question, test.answer, strings.Repeat("thought", 100), state, []chat.Message{{Role: "user", Content: "此前要求省略解释，只给指定格式。"}, {Role: "tool", Content: "untrusted execution data"}})
			require.NoError(t, err)
			require.Equal(t, !test.complete, repair)
			require.Empty(t, visible, "the evaluator and unverified draft never stream to the answer area")
			require.Equal(t, 40, state.TurnUsage.TotalTokens)
			require.Len(t, model.calls, 1)
			var input map[string]any
			require.NoError(t, json.Unmarshal([]byte(model.calls[0][1].Content), &input))
			require.Equal(t, test.question, input["current_user_question"])
			require.Equal(t, test.answer, input["candidate_answer"])
			require.NotContains(t, model.calls[0][1].Content, "untrusted execution data")
			require.Empty(t, model.opts[0].Tools)
			require.Equal(t, "none", model.opts[0].ToolChoice)
			require.NotNil(t, model.opts[0].Thinking)
			require.False(t, *model.opts[0].Thinking)
			require.Equal(t, 256, model.opts[0].MaxCompletionTokens)
		})
	}
}

func TestAnswerCompletenessPreservesFullQuestionWithoutBusinessMatching(t *testing.T) {
	question := strings.Repeat("请保持之前约定的格式；", 2000) + "最后只返回指定符号。"
	model := &mockChat{responses: []mockResponse{completenessVerdict(true)}}
	engine := newTestEngine(t, model)
	repair, err := engine.answerNeedsRepair(context.Background(), question, "?", "", &types.AgentState{})
	require.NoError(t, err)
	require.False(t, repair)
	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(model.calls[0][1].Content), &input))
	require.Equal(t, question, input["current_user_question"])
}

func TestAnswerCompletenessOnlyCallsModelForProtocolShapeSuspicion(t *testing.T) {
	model := &mockChat{}
	engine := newTestEngine(t, model)
	repair, err := engine.answerNeedsRepair(context.Background(), "any question", "完整且较长的正常答复。", strings.Repeat("thought", 100), &types.AgentState{})
	require.NoError(t, err)
	require.False(t, repair)
	repair, err = engine.answerNeedsRepair(context.Background(), "any question", "  ", "", &types.AgentState{})
	require.NoError(t, err)
	require.True(t, repair)
	require.Empty(t, model.calls)
}

func TestAnswerCompletenessInvalidVerdictFailsClosed(t *testing.T) {
	for _, answer := range []string{`{}`, `{"complete":"true"}`, `{"complete":true} trailing`, `{"complete":true,"extra":"ignored"}`, "-"} {
		model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: answer, Done: true, FinishReason: "stop"}}}}}
		repair, err := newTestEngine(t, model).answerNeedsRepair(context.Background(), "question", "?", "", &types.AgentState{})
		require.Error(t, err)
		require.True(t, repair)
	}
}

func TestInlineThinkIsPreservedAndFragmentWaitsForSemanticReview(t *testing.T) {
	reasoning := strings.Repeat("hidden reasoning ", 40)
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: "<think>" + reasoning + "</think>周", Done: true, FinishReason: "stop"}}}}}
	engine := newTestEngine(t, model)
	var visible string
	engine.eventBus.On(event.EventAgentFinalAnswer, func(_ context.Context, ev event.Event) error {
		if data, ok := ev.Data.(event.AgentFinalAnswerData); ok {
			visible += data.Content
		}
		return nil
	})
	response, err := engine.streamThinkingToEventBus(context.Background(), emptyMessages(), nil, 0, "session")
	require.NoError(t, err)
	require.Equal(t, "周", response.Content)
	require.Equal(t, reasoning, response.ReasoningContent)
	require.False(t, response.AnswerStreamed)
	require.Empty(t, visible)
}

func TestStalledShortAnswerUsesSemanticFormatVerification(t *testing.T) {
	for _, test := range []struct {
		question, answer string
		complete         bool
	}{
		{"Output only the selected punctuation symbol.", ";", true},
		{"请详细说明各项结果。", "周", false},
		{"Return the requested single digit.", "6", true},
	} {
		t.Run(test.answer, func(t *testing.T) {
			model := &mockChat{responses: []mockResponse{completenessVerdict(test.complete)}}
			engine := newTestEngine(t, model)
			recorder := &truncationRecorder{}
			recorder.attach(engine.eventBus)
			state := &types.AgentState{}
			engine.finishStalledTurn(context.Background(), state, "session", &types.ChatResponse{Content: test.answer, FinishReason: "incomplete"}, false, test.question, nil)
			want := stalledAnswerFallback
			if test.complete {
				want = test.answer
			}
			require.True(t, state.IsComplete)
			require.Equal(t, want, state.FinalAnswer)
			require.Len(t, model.calls, 1)
			var input map[string]any
			require.NoError(t, json.Unmarshal([]byte(model.calls[0][1].Content), &input))
			require.Equal(t, test.question, input["current_user_question"])
			require.Equal(t, test.answer, input["candidate_answer"])
			emitted := recorder.snapshot()
			require.Len(t, emitted, 2)
			require.Equal(t, want, emitted[0].Content)
			require.True(t, emitted[1].Done)
		})
	}
}

func TestStalledShortAnswerInvalidVerdictIsRuntimeFailure(t *testing.T) {
	model := &mockChat{responses: []mockResponse{completenessVerdict(true)}}
	model.responses[0].chunks[0].Content = "unparseable response"
	engine := newTestEngine(t, model)
	state := &types.AgentState{}
	engine.finishStalledTurn(context.Background(), state, "session", &types.ChatResponse{Content: "6", FinishReason: "incomplete"}, true, "Return the requested symbol.", nil)
	require.True(t, state.IsComplete)
	require.Equal(t, truncatedAnswerFallback, state.FinalAnswer)
}

func TestAbnormalShortAnswerDoesNotStreamBeforeSemanticVerification(t *testing.T) {
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{Content: "周", ResponseType: types.ResponseTypeAnswer, Done: true, FinishReason: "incomplete"}}}}}
	engine := newTestEngine(t, model)
	recorder := &truncationRecorder{}
	recorder.attach(engine.eventBus)
	response, err := engine.streamThinkingToEventBus(context.Background(), emptyMessages(), nil, 0, "session")
	require.NoError(t, err)
	require.False(t, response.AnswerStreamed)
	require.Empty(t, recorder.snapshot())
}

func TestStalledCompletenessCancellationDoesNotInventFinalAnswer(t *testing.T) {
	engine := newTestEngine(t, &mockChat{responses: []mockResponse{completenessVerdict(true)}})
	recorder := &truncationRecorder{}
	recorder.attach(engine.eventBus)
	state := &types.AgentState{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	engine.finishStalledTurn(ctx, state, "session", &types.ChatResponse{Content: "?", FinishReason: "incomplete"}, false, "Return one symbol.", nil)
	require.False(t, state.IsComplete)
	require.Empty(t, state.FinalAnswer)
	require.Empty(t, recorder.snapshot())
}

func TestCompletenessCheckUsesConfiguredMinimumSupportedReasoning(t *testing.T) {
	for _, effort := range []string{"", "off", "minimal", "low"} {
		t.Run(effort, func(t *testing.T) {
			model := &mockChat{responses: []mockResponse{completenessVerdict(true)}}
			engine := newTestEngine(t, model)
			engine.config.ReviewReasoningEffort = effort
			engine.config.ReasoningEffort = "high"
			repair, err := engine.answerNeedsRepair(context.Background(), "Return only the requested symbol.", "?", "", &types.AgentState{})
			require.NoError(t, err)
			require.False(t, repair)
			expected := effort
			if expected == "" {
				expected = "off"
			}
			require.Equal(t, chat.ReasoningEffort(expected), model.opts[0].ReasoningEffort)
			require.False(t, *model.opts[0].Thinking)
			require.Equal(t, 256, model.opts[0].MaxCompletionTokens)
			require.Equal(t, "none", model.opts[0].ToolChoice)
			require.Equal(t, "high", engine.config.ReasoningEffort, "user-selected primary reasoning must not change")
		})
	}
}
