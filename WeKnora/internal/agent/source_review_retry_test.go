package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestSourceReviewProtocolRetryPreservesCandidateEvidenceAndUsage(t *testing.T) {
	truncated := sourceConsistencyVerdict(`{"complete":false,"feedback":"unfinished`)
	truncated.chunks[0].FinishReason = "length"
	for name, invalid := range map[string]mockResponse{
		"truncated":          truncated,
		"overlong_unicode":   sourceConsistencyVerdict(`{"complete":false,"feedback":"` + strings.Repeat("改", sourceReviewFeedbackLimit+1) + `"}`),
		"overlong_ascii":     sourceConsistencyVerdict(`{"complete":false,"feedback":"` + strings.Repeat("x", sourceReviewFeedbackLimit+1) + `"}`),
		"conflicting_accept": sourceConsistencyVerdict(`{"complete":true,"feedback":"Delete an unsupported statement."}`),
		"duplicate":          sourceConsistencyVerdict(`{"complete":false,"complete":true}`),
		"null":               sourceConsistencyVerdict(`{"complete":true,"feedback":null}`),
		"unknown":            sourceConsistencyVerdict(`{"complete":true,"analysis":"not allowed"}`),
		"malformed":          sourceConsistencyVerdict(`not JSON`),
	} {
		t.Run(name, func(t *testing.T) {
			valid := sourceConsistencyVerdict(`{"complete":false,"feedback":"保留原始指标和单位。"}`)
			for _, response := range []*mockResponse{&invalid, &valid} {
				response.chunks[0].Usage = &types.TokenUsage{PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30}
			}
			model := &mockChat{responses: []mockResponse{invalid, valid}}
			engine := sourceConsistencyEngine(t, model)
			state := sourceConsistencyState()
			repair, err := engine.answerNeedsRepair(t.Context(), "完整问题与限定范围。", "候选声称指标为产量。", "", state)
			require.NoError(t, err)
			require.True(t, repair)
			require.Len(t, model.calls, 2)
			require.Equal(t, model.calls[0][1], model.calls[1][1])
			require.Contains(t, model.calls[1][0].Content, "SAME candidate")
			require.Equal(t, 60, state.TurnUsage.TotalTokens)
			require.Equal(t, "保留原始指标和单位。", engine.answerReviewFeedback)
			for _, opts := range model.opts {
				require.JSONEq(t, string(sourceConsistencySchema), string(opts.Format))
			require.GreaterOrEqual(t, opts.MaxCompletionTokens, 2048)
			require.LessOrEqual(t, opts.MaxCompletionTokens, 6144)
				require.Equal(t, "none", opts.ToolChoice)
				require.False(t, *opts.Thinking)
			}
		})
	}
}

func TestSourceReviewFeedbackBoundaryAndValidRejectionDoNotRetry(t *testing.T) {
	feedback := strings.Repeat("修", sourceReviewFeedbackLimit)
	model := &mockChat{responses: []mockResponse{sourceConsistencyVerdict(`{"complete":false,"feedback":"` + feedback + `"}`)}}
	engine := sourceConsistencyEngine(t, model)
	repair, err := engine.answerNeedsRepair(t.Context(), "说明指标。", "候选答复。", "", sourceConsistencyState())
	require.NoError(t, err)
	require.True(t, repair)
	require.Len(t, model.calls, 1)
	require.Equal(t, feedback, engine.answerReviewFeedback)
	var schema struct {
		Properties map[string]struct {
			MaxLength int `json:"maxLength"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(sourceConsistencySchema, &schema))
	require.Equal(t, sourceReviewFeedbackLimit, schema.Properties["feedback"].MaxLength)
}

func TestSourceReviewProtocolErrorsDoNotSpendTwoAnswerRepairs(t *testing.T) {
	model := &mockChat{responses: []mockResponse{
		sourceConsistencyVerdict("该指标代表产量。"),
		sourceConsistencyVerdict(`{"complete":false`),
		sourceConsistencyVerdict(`{"complete":false,"feedback":"` + strings.Repeat("修", sourceReviewFeedbackLimit+1) + `"}`),
		sourceConsistencyVerdict(`{"complete":false,"feedback":"必须保留覆盖面积的原始含义。"}`),
		sourceConsistencyVerdict(`{"answer":"该指标代表重量。"}`),
		sourceConsistencyVerdict(`{"complete":true,"feedback":"仍需修正。"}`),
		sourceConsistencyVerdict(`{"complete":false,"feedback":"应当说明覆盖面积及其原单位。"}`),
		sourceConsistencyVerdict(`{"answer":"该指标代表覆盖面积，单位为公顷。"}`),
		sourceAcceptedVerdict(),
	}}
	engine := sourceConsistencyEngine(t, model)
	recorder := &terminalAnswerRecorder{}
	recorder.attach(engine.eventBus)
	state := sourceConsistencyState()
	_, err := engine.executeLoop(t.Context(), state, "只说明指标和单位。", emptyMessages(), nil, "session", "message")
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.Equal(t, "该指标代表覆盖面积，单位为公顷。", state.FinalAnswer)
	require.Len(t, model.calls, 9)
	require.Equal(t, model.calls[1][1], model.calls[2][1])
	require.Equal(t, model.calls[1][1], model.calls[3][1])
	require.Equal(t, model.calls[5][1], model.calls[6][1])
	done, visible := recorder.snapshot()
	require.Equal(t, 1, done)
	require.Equal(t, state.FinalAnswer, visible)
	require.NotContains(t, visible, "产量")
	require.NotContains(t, visible, "重量")
}

func TestSourceReviewExhaustionDoesNotRewriteOrReleaseCandidate(t *testing.T) {
	for _, synthesis := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "synthesis"}[synthesis], func(t *testing.T) {
			responses := []mockResponse{sourceConsistencyVerdict("尚未通过核验的正文。")}
			for range answerReviewAttempts {
				responses = append(responses, sourceConsistencyVerdict(`{"complete":true,"feedback":"协议冲突，不能当作修改意见。"}`))
			}
			model := &mockChat{responses: responses}
			engine := sourceConsistencyEngine(t, model)
			recorder := &terminalAnswerRecorder{}
			recorder.attach(engine.eventBus)
			state := sourceConsistencyState()
			if synthesis {
				require.NoError(t, engine.streamFinalAnswerToEventBus(t.Context(), "说明指标。", state, "session", emptyMessages()))
			} else {
				_, err := engine.executeLoop(t.Context(), state, "说明指标。", emptyMessages(), nil, "session", "message")
				require.NoError(t, err)
			}
			require.Len(t, model.calls, len(responses))
			require.True(t, strings.HasPrefix(state.FinalAnswer, "尚未通过核验的正文。"))
			require.Contains(t, state.FinalAnswer, "未能完成自动核验")
			require.NotContains(t, state.FinalAnswer, "协议冲突", "protocol-conflicting feedback is not presented as review advice")
			require.Empty(t, engine.answerReviewFeedback)
			done, visible := recorder.snapshot()
			require.Equal(t, 1, done)
			require.Equal(t, state.FinalAnswer, visible)
		})
	}
}

type cancelSourceReviewChat struct {
	mockChat
	cancel context.CancelFunc
}

func (model *cancelSourceReviewChat) ChatStream(ctx context.Context, messages []chat.Message, opts *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	model.cancel()
	return model.mockChat.ChatStream(ctx, messages, opts)
}

func TestSourceReviewCancellationStopsIndependentRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	model := &cancelSourceReviewChat{mockChat: mockChat{responses: []mockResponse{sourceAcceptedVerdict()}}, cancel: cancel}
	engine := newTestEngine(t, model)
	engine.config.WorkbenchDefault, engine.config.TaskPlanningEnabled = true, true
	_, err := engine.answerNeedsRepair(ctx, "说明指标。", "候选正文。", "", sourceConsistencyState())
	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, model.calls, 1)
	require.Empty(t, engine.answerReviewFeedback)
}
