package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type protocolRepairClockChat struct {
	mockChat
	deadlines []time.Time
}

func (m *protocolRepairClockChat) ChatStream(ctx context.Context, messages []chat.Message, opts *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	deadline, _ := ctx.Deadline()
	m.deadlines = append(m.deadlines, deadline)
	return m.mockChat.ChatStream(ctx, messages, opts)
}

func TestStructuredResultProtocolRepairUsesOriginalUniqueSpanAndSharedBudget(t *testing.T) {
	const core = "Alpha is 12.500 tonnes. Beta is 12.500 kg."
	e, state, _ := lightStructuredFixture(t)
	first := lightReviewEdits(structuredResultEdit{"12.500", "12.500 kg"})
	second := lightReviewEdits(structuredResultEdit{"Alpha is 12.500 tonnes.", "Alpha is 12.500 kg."})
	model := &protocolRepairClockChat{mockChat: mockChat{responses: []mockResponse{first, second}}}
	e.chatModel = model
	before := reviewDigest(state.RoundSteps)
	d := e.checkCandidate(t.Context(), "Report the observations.", core, "", state, nil, false)
	require.Equal(t, candidateAccepted, d.Disposition)
	require.Equal(t, "corrected", d.StructuredResultReviewStatus)
	require.Equal(t, "Alpha is 12.500 kg. Beta is 12.500 kg.", d.Candidate)
	require.Len(t, model.calls, 2)
	require.Equal(t, model.deadlines[0], model.deadlines[1])
	require.Equal(t, structuredResultReviewTokens-12, model.opts[1].MaxCompletionTokens, "remaining allowance is based on actual first usage")
	require.Equal(t, 24, state.TurnUsage.CompletionTokens)
	require.Equal(t, model.calls[0][1].Content, model.calls[1][1].Content, "original candidate and evidence are retained exactly")
	require.Contains(t, model.calls[1][2].Content, `"old":"12.500"`)
	require.Contains(t, model.calls[1][3].Content, "patch_not_unique")
	require.Equal(t, before, reviewDigest(state.RoundSteps))
	require.Zero(t, e.turnReview.Repairs)
	e.checkCandidate(t.Context(), "Report the observations.", d.Candidate, "", state, nil, true)
	require.Len(t, model.calls, 2, "ordinary/final paths never replenish protocol recovery")
}

func TestStructuredResultProtocolRepairStillRejectsInventedValues(t *testing.T) {
	e, state, model := lightStructuredFixture(t,
		lightReviewEdits(structuredResultEdit{"missing", "replacement"}),
		lightReviewEdits(structuredResultEdit{"12.500", "87654321"}))
	const core = "Observed 12.500 kg."
	d := e.checkCandidate(t.Context(), "Report the observation.", core, "", state, nil, false)
	require.Equal(t, core, d.Candidate)
	require.Equal(t, "not_checked_invalid_correction", d.StructuredResultReviewStatus)
	require.Len(t, model.calls, 2)
	require.Zero(t, e.turnReview.Repairs)
}

func TestFinalNumericRepairIncludesPersistentTokensAndStillChecksFinalAnswer(t *testing.T) {
	for _, corrected := range []bool{false, true} {
		t.Run(map[bool]string{false: "still_invalid", true: "corrected"}[corrected], func(t *testing.T) {
			final := "走势回升，最低680，最高742.25。"
			if corrected {
				final = "走势回升，最低685，最高742.25。"
			}
			model := &mockChat{responses: []mockResponse{chatAnswer("走势回升，下界680，上界742.25。"), chatAnswer("走势回升，最低680，最高742.25。"), chatAnswer(final)}}
			e := sourceConsistencyEngine(t, model)
			e.config.AnswerReviewMode = "standard"
			e.config.StructuredResultReviewEnabled = false
			data := map[string]any{"kind": "structured_analysis", "status": "ok", "rows": [][]any{{685}, {742.25}}}
			raw, _ := json.Marshal(data)
			state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{ID: "actual", Name: "structured_analyze_table", Result: &types.ToolResult{Success: true, Data: data, Output: string(raw)}}}}}}
			require.NoError(t, e.streamFinalAnswerToEventBus(t.Context(), "根据实际值简析走势。", state, "synthetic", emptyMessages()))
			require.Len(t, model.calls, 3)
			messages := model.calls[2]
			require.Contains(t, messages[len(messages)-1].Content, `上次修订后仍未匹配的数字：["680"]`)
			require.Contains(t, messages[len(messages)-1].Content, "简析、图件必须保留")
			require.Equal(t, 2, e.turnReview.Repairs)
			if corrected {
				require.Equal(t, final, state.FinalAnswer)
			} else {
				require.Equal(t, finalNumericFailureFallback, state.FinalAnswer)
			}
		})
	}
}

func TestStructuredResultProtocolRepairRecoversSchemaWithoutChangingScope(t *testing.T) {
	e, state, model := lightStructuredFixture(t,
		chatAnswer(`{"action":"correct","edits":[`),
		lightReviewEdits(structuredResultEdit{"tonnes", "kg"}))
	const original = "The observation is 12.500 tonnes."
	d := e.checkCandidate(t.Context(), "Report the observation.", original, "", state, nil, false)
	require.Equal(t, "corrected", d.StructuredResultReviewStatus)
	require.Equal(t, "The observation is 12.500 kg.", d.Candidate)
	require.Len(t, model.calls, 2)
	require.Contains(t, model.calls[1][3].Content, "response_schema")
	require.Equal(t, model.calls[0][1].Content, model.calls[1][1].Content)
}

func TestStructuredResultProtocolRepairWithoutUsageStillChargesOutput(t *testing.T) {
	first := lightReviewEdits(structuredResultEdit{"missing", "replacement"})
	first.chunks[0].Usage = nil
	used := len(first.chunks[0].Content)
	e, state, model := lightStructuredFixture(t, first, lightReviewEdits(structuredResultEdit{"tonnes", "kg"}))
	d := e.checkCandidate(t.Context(), "Report the observation.", "The observation is 12.500 tonnes.", "", state, nil, false)
	require.Equal(t, "corrected", d.StructuredResultReviewStatus)
	require.Len(t, model.calls, 2)
	require.Equal(t, structuredResultReviewTokens-used, model.opts[1].MaxCompletionTokens, "unknown usage charges conservative output bytes rather than free repair")
}
