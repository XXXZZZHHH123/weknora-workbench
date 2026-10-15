package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/modelcontext"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestFinalReviewSynthesisParsesAnswerJSONBeforeDecodingEscapedCitations(t *testing.T) {
	const image = "resource://AbCdEfGhIjKlMnOpQrStUv"
	for _, payload := range []string{
		`{"answer":"原文结果30。<ref id=\"c1\"/> ![图](res://0001) <ref id=\"c999\"/>"}`,
		`{"answer":"原文结果30。\u003cref id=\u0022c1\u0022/\u003e ![图](res://0001)"}`,
	} {
		t.Run(payload, func(t *testing.T) {
			model := &mockChat{responses: []mockResponse{
				{chunks: []types.StreamResponse{
					{ResponseType: types.ResponseTypeAnswer, Content: payload[:len(payload)/2]},
					{ResponseType: types.ResponseTypeAnswer, Content: payload[len(payload)/2:], Done: true, FinishReason: "stop"},
				}},
				completenessVerdict(true),
			}}
			engine := newTestEngine(t, model)
			engine.structuredReviewMode = true
			engine.config.Temperature = 0.9
			engine.modelContext.RegisterChunk(modelcontext.ChunkReference{ChunkID: "actual-chunk", KnowledgeBaseID: "actual-kb", DocumentTitle: `Report "quoted"`})
			recorder := &terminalAnswerRecorder{}
			recorder.attach(engine.eventBus)
			state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{Name: "structured_analyze_table", Result: &types.ToolResult{Success: true, Output: `{"kind":"structured_analysis","status":"ok","rows":[[30]]}`}}}}}}
			err := engine.streamFinalAnswerToEventBus(context.Background(), "交付原任务的最终答复", state, "s", []chat.Message{{Role: "tool", Content: "actual image=" + image}})
			require.NoError(t, err)
			require.Contains(t, state.FinalAnswer, "原文结果30。")
			require.Contains(t, state.FinalAnswer, `chunk_id="actual-chunk"`)
			require.Contains(t, state.FinalAnswer, "![图]("+image+")")
			require.NotContains(t, state.FinalAnswer, `{"answer"`)
			require.NotContains(t, state.FinalAnswer, "<ref")
			require.NotContains(t, state.FinalAnswer, "res://")
			require.NotContains(t, state.FinalAnswer, "c999")
			require.JSONEq(t, string(reviewedAnswerSchema), string(model.opts[0].Format))
			require.Zero(t, model.opts[0].Temperature)
			require.Equal(t, "none", model.opts[0].ToolChoice)
			var completenessInput struct {
				Candidate string `json:"candidate_answer"`
			}
			require.NoError(t, json.Unmarshal([]byte(model.calls[1][1].Content), &completenessInput))
			require.NotContains(t, completenessInput.Candidate, `{"answer"`, "completeness reviews prose, not the delivery envelope")
			require.Contains(t, completenessInput.Candidate, "原文结果30。", "completeness reviews the extracted answer text")
			require.NotContains(t, completenessInput.Candidate, "c999")
			done, visible := recorder.snapshot()
			require.Equal(t, 1, done)
			require.Equal(t, state.FinalAnswer, visible)
		})
	}
}

func TestFinalReviewSynthesisJSONCannotBypassCurrentNumericGuard(t *testing.T) {
	model := &mockChat{}
	for range 3 {
		model.responses = append(model.responses,
			mockResponse{chunks: []types.StreamResponse{{Content: `{"answer":"结果999。"}`, Done: true, FinishReason: "stop"}}},
			completenessVerdict(true))
	}
	engine := newTestEngine(t, model)
	engine.structuredReviewMode = true
	recorder := &terminalAnswerRecorder{}
	recorder.attach(engine.eventBus)
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{Name: "structured_analyze_table", Result: &types.ToolResult{Success: true, Output: `{"kind":"structured_analysis","status":"ok","rows":[[30]]}`}}}}}}
	require.NoError(t, engine.streamFinalAnswerToEventBus(context.Background(), "回答当前问题", state, "s", emptyMessages()))
	require.NotContains(t, state.FinalAnswer, "999")
	require.Contains(t, state.FinalAnswer, "未通过来源核验")
	done, visible := recorder.snapshot()
	require.Equal(t, 1, done)
	require.Equal(t, state.FinalAnswer, visible)
	require.Len(t, model.calls, 6)
}

type permissiveCompletenessMalformedReviewChat struct {
	mockChat
	payload           string
	reviewCalls       int
	completenessCalls int
}

func (m *permissiveCompletenessMalformedReviewChat) ChatStream(_ context.Context, _ []chat.Message, opts *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	content := `{"complete":true}`
	if isReviewedAnswerFormat(opts.Format) {
		m.reviewCalls++
		content = m.payload
	} else {
		m.completenessCalls++
	}
	stream := make(chan types.StreamResponse, 1)
	stream <- types.StreamResponse{Content: content, Done: true, FinishReason: "stop"}
	close(stream)
	return stream, nil
}

func TestFinalReviewSynthesisRejectsEmptyOrInvalidProtocolBeforePermissiveCompleteness(t *testing.T) {
	for _, payload := range []string{`{"answer":"unfinished`, `{"answer":""}`, `{"answer":"   "}`} {
		t.Run(payload, func(t *testing.T) {
			model := &permissiveCompletenessMalformedReviewChat{payload: payload}
			engine := newTestEngine(t, model)
			engine.structuredReviewMode = true
			recorder := &terminalAnswerRecorder{}
			recorder.attach(engine.eventBus)
			state := &types.AgentState{}
			require.NoError(t, engine.streamFinalAnswerToEventBus(context.Background(), "交付当前任务", state, "s", emptyMessages()))
			require.Equal(t, incompleteAnswerFallback, state.FinalAnswer)
			require.Equal(t, 3, model.reviewCalls)
			require.Zero(t, model.completenessCalls, "a permissive completeness model cannot accept malformed or empty protocol output")
			done, visible := recorder.snapshot()
			require.Equal(t, 1, done)
			require.Equal(t, state.FinalAnswer, visible)
			require.NotContains(t, visible, `{"answer"`)
		})
	}
}
