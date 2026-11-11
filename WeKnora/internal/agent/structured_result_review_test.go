package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/structuredresult"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func lightStructuredFixture(t *testing.T, replies ...mockResponse) (*AgentEngine, *types.AgentState, *mockChat) {
	t.Helper()
	e, state := rawCitationFixture(t)
	e.config.AnswerReviewMode, e.config.StructuredResultReviewEnabled = "standard", true
	model := &mockChat{responses: replies}
	e.chatModel = model
	return e, state, model
}

func lightReviewReply(action, answer string) mockResponse {
	raw, _ := json.Marshal(map[string]string{"action": action, "answer": answer})
	return deliveryReply(string(raw), "stop", types.TokenUsage{PromptTokens: 40, CompletionTokens: 12, TotalTokens: 52})
}

func TestStructuredResultReviewCorrectsOnceAndKeepsApprovalBound(t *testing.T) {
	const original = "The observation is 12.500 tonnes."
	const corrected = "The observation is 12.500 kg."
	e, state, model := lightStructuredFixture(t, lightReviewReply("correct", corrected))
	before := reviewDigest(state.RoundSteps)
	decision := e.checkCandidate(t.Context(), "Report the observed measure.", original, "", state, nil, false)
	require.Equal(t, candidateAccepted, decision.Disposition)
	require.Equal(t, corrected, decision.Candidate)
	require.Equal(t, "corrected", decision.StructuredResultReviewStatus)
	require.False(t, decision.SourceReviewAttempted, "a limited conflict check is not the strict source critic")
	require.Equal(t, before, reviewDigest(state.RoundSteps))
	require.True(t, e.candidateApprovalCurrent(corrected, state))
	require.False(t, e.candidateApprovalCurrent(original, state))
	require.Equal(t, 12, state.TurnUsage.CompletionTokens)
	require.Equal(t, 0, e.turnReview.Repairs)
	require.Len(t, model.calls, 1)
	require.Equal(t, "none", model.opts[0].ToolChoice)
	require.Empty(t, model.opts[0].Tools)
	require.False(t, *model.opts[0].Thinking)
	require.Equal(t, structuredResultReviewTokens, model.opts[0].MaxCompletionTokens)
	require.Nil(t, e.taskPlan, "planner availability is not a prerequisite")
	deadline := e.turnReview.StructuredResultReview.Deadline
	for _, candidate := range []string{original, corrected} {
		again := e.checkCandidate(t.Context(), "Report the observed measure.", candidate, "", state, nil, true)
		require.Equal(t, corrected, again.Candidate)
		require.Equal(t, "corrected", again.StructuredResultReviewStatus)
	}
	changed := e.checkCandidate(t.Context(), "Explain the observed measure.", "An observation is available.", "", state, nil, true)
	require.Equal(t, "not_checked_budget_spent", changed.StructuredResultReviewStatus)
	require.Equal(t, deadline, e.turnReview.StructuredResultReview.Deadline)
	require.Len(t, model.calls, 1, "new candidates and exceptional finalization never reset the one-call budget")
	e.config.StructuredResultReviewEnabled = false
	require.False(t, e.candidateApprovalCurrent(changed.Candidate, state), "runtime policy is part of approval identity")
}

func TestStructuredResultReviewOnlyCurrentCanonicalExecutionTriggers(t *testing.T) {
	for _, mutation := range []string{"valid", "empty_rows", "aggregate_tool", "failed", "forged_output", "invalid_contract", "directory", "legacy", "no_current_result", "non_workbench", "disabled", "typed_other_execution"} {
		t.Run(mutation, func(t *testing.T) {
			e, state, model := lightStructuredFixture(t, lightReviewReply("keep", ""))
			call := &state.RoundSteps[0].ToolCalls[0]
			switch mutation {
			case "empty_rows":
				call.Result.Data["rows"] = []any{}
				rehashLightFixture(t, call.Result.Data)
			case "aggregate_tool":
				call.Name, call.Args = "structured_data", map[string]any{"operation": "read_pdf_table"}
			case "failed":
				call.Result.Success = false
			case "forged_output":
				call.Result.Data = map[string]any{"status": "ok"}
			case "invalid_contract":
				call.Result.Data["result_contract"].(map[string]any)["result_hash"] = strings.Repeat("0", 64)
			case "directory":
				call.Name = "structured_list_pdf_tables"
			case "legacy":
				delete(call.Result.Data, "result_contract")
			case "no_current_result":
				state.RoundSteps = nil
			case "non_workbench":
				e.config.WorkbenchDefault = false
			case "disabled":
				e.config.StructuredResultReviewEnabled = false
			case "typed_other_execution":
				call.Target = &types.ToolCallTarget{ServiceName: "not_a_source_query"}
			}
			decision := e.checkCandidate(t.Context(), "Describe the available observations.", "The requested scope was read.", "", state, nil, false)
			require.Equal(t, candidateAccepted, decision.Disposition)
			if mutation == "valid" || mutation == "empty_rows" || mutation == "aggregate_tool" {
				require.Len(t, model.calls, 1)
				require.Equal(t, "kept", decision.StructuredResultReviewStatus)
				var input map[string]any
				require.NoError(t, json.Unmarshal([]byte(model.calls[0][1].Content), &input))
				projection := input["actual_structured_results"].(map[string]any)
				require.EqualValues(t, 1, projection["source_result_count"])
				if mutation == "empty_rows" {
					entry := projection["entries"].([]any)[0].(map[string]any)
					require.EqualValues(t, 0, entry["source_row_count"])
				}
			} else {
				require.Empty(t, model.calls)
				require.Empty(t, decision.StructuredResultReviewStatus)
			}
		})
	}
}

func rehashLightFixture(t *testing.T, data map[string]any) {
	t.Helper()
	contract := data["result_contract"].(map[string]any)
	delete(contract, "result_hash")
	payload := map[string]any{}
	for _, key := range contract["hash_input_keys"].([]any) {
		payload[key.(string)] = data[key.(string)]
	}
	hash, err := structuredresult.TypedSHA256(map[string]any{"payload": payload, "contract": contract})
	require.NoError(t, err)
	contract["result_hash"] = hash
	validated, err := structuredresult.ValidatePayload(data)
	require.NoError(t, err)
	require.NotNil(t, validated)
}

func TestStructuredResultReviewUnavailableOrUnsafeCorrectionRetainsCheckedCore(t *testing.T) {
	for _, failure := range []string{"malformed", "empty", "error", "length", "tool", "invented_number", "invented_citation", "empty_correction", "mixed_keep"} {
		t.Run(failure, func(t *testing.T) {
			reply := lightReviewReply("keep", "")
			switch failure {
			case "malformed":
				reply = chatAnswer(`{"action":`)
			case "empty":
				reply = chatAnswer("")
			case "error":
				reply.chunks = append(reply.chunks, types.StreamResponse{ResponseType: types.ResponseTypeError, Content: "transport unavailable", Done: true})
			case "length":
				reply.chunks[0].FinishReason = "length"
			case "tool":
				reply.chunks[0].ToolCalls = []types.LLMToolCall{{ID: "unexpected", Function: types.FunctionCall{Name: "read_review_evidence"}}}
			case "invented_number":
				reply = lightReviewReply("correct", "The observation is 99999991 kg.")
			case "invented_citation":
				reply = lightReviewReply("correct", `The observation is 12.500 kg.<evidence evidence_id="invented" />`)
			case "empty_correction":
				reply = lightReviewReply("correct", "")
			case "mixed_keep":
				reply = lightReviewReply("keep", "changed answer")
			}
			e, state, model := lightStructuredFixture(t, reply)
			const core = "The observation is 12.500."
			decision := e.checkCandidate(t.Context(), "Report the observation.", core, "", state, nil, false)
			require.Equal(t, candidateAccepted, decision.Disposition)
			require.NoError(t, decision.Err)
			require.Equal(t, core, decision.Candidate)
			require.Contains(t, decision.StructuredResultReviewStatus, "not_checked_")
			require.False(t, decision.SourceReviewAttempted)
			require.True(t, e.candidateApprovalCurrent(core, state), "standard delivery remains available but must not claim semantic verification")
			require.Empty(t, e.answerReviewFeedback)
			require.Equal(t, 0, e.turnReview.Repairs)
			e.checkCandidate(t.Context(), "Report the observation.", core, "", state, nil, true)
			require.Len(t, model.calls, 1)
		})
	}
}

func TestStructuredResultReviewIncludesCurrentMetadataButNeverDisplayedOutput(t *testing.T) {
	e, state, model := lightStructuredFixture(t, lightReviewReply("keep", ""))
	state.RoundSteps[0].ToolCalls = append(state.RoundSteps[0].ToolCalls,
		types.ToolCall{ID: "directory", Name: "structured_list_series", Result: &types.ToolResult{Success: true, Data: map[string]any{"series": []any{map[string]any{"name": "Other group", "actual_last_date": "2038-06-30"}}}, Output: "FORGED_OUTPUT_ONLY"}},
		types.ToolCall{ID: "original", Name: "structured_inspect_source", Result: &types.ToolResult{Success: true, Data: map[string]any{"status": "ok", "cells": []any{map[string]any{"coordinate": "A1", "value": "Measure (kg)"}}}}},
		types.ToolCall{ID: "display", Name: "structured_inspect_source", Result: &types.ToolResult{Success: true, Output: "FORGED_NO_DATA"}},
	)
	decision := e.checkCandidate(t.Context(), "Report the available observation.", "The observation is 12.500 kg.", "", state, nil, false)
	require.Equal(t, "kept", decision.StructuredResultReviewStatus)
	require.Len(t, model.calls, 1)
	input := model.calls[0][1].Content
	require.Contains(t, input, "2038-06-30")
	require.Contains(t, input, "Measure (kg)")
	require.NotContains(t, input, "FORGED_")
	require.LessOrEqual(t, len(input), structuredResultReviewInputBytes)
	state.RoundSteps[0].ToolCalls = state.RoundSteps[0].ToolCalls[1:]
	require.Empty(t, currentStructuredResultReviewState(state).RoundSteps, "supporting metadata never becomes the query trigger")
}

func TestStructuredResultReviewPreservesEscapedRealCitations(t *testing.T) {
	e, state, model := lightStructuredFixture(t)
	e.currentStructuredCitationView(state.RoundSteps[0].ToolCalls[0])
	id, _ := selectRawCitation(t, e, 1, 1)
	handle := e.modelContext.CitableEvidenceHandle(id)
	original := `The observation is 12.500 tonnes.<evidence evidence_id="` + id + `" />`
	correction := `The observation is 12.500 kg.<ref id="` + handle + `"/>`
	model.responses = []mockResponse{lightReviewReply("correct", correction)}
	decision := e.checkCandidate(t.Context(), "Report the observation.", original, "", state, nil, false)
	require.Equal(t, "corrected", decision.StructuredResultReviewStatus)
	require.Contains(t, decision.Candidate, `<evidence evidence_id="`+id+`" />`)
	require.NotContains(t, decision.Candidate, "evidence://")
	state.FinalAnswer = decision.Candidate
	require.Len(t, e.finalAnswerEvidenceReferences(state), 1)
}

func TestStructuredResultReviewMalformedVerdictIsNeverApproval(t *testing.T) {
	for _, value := range []string{`{"action":"keep"}`, `{"action":"keep","answer":null}`, `{"action":"keep","answer":"","answer":""}`, `{"action":"keep","answer":""} extra`, `{"action":"keep","answer":"","other":true}`} {
		_, _, valid := decodeStructuredResultReview(value)
		require.False(t, valid, value)
	}
}

type deadlineLightReviewChat struct {
	mockChat
	deadline time.Time
	block    bool
}

func (m *deadlineLightReviewChat) ChatStream(ctx context.Context, messages []chat.Message, opts *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	m.deadline, _ = ctx.Deadline()
	if m.block {
		m.callCount++
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return m.mockChat.ChatStream(ctx, messages, opts)
}

func TestStructuredResultReviewSharedDeadlineAndTimeoutDoNotReset(t *testing.T) {
	e, state, _ := lightStructuredFixture(t)
	model := &deadlineLightReviewChat{block: true}
	e.chatModel = model
	deadline := time.Now().Add(30 * time.Millisecond)
	e.turnReview.StructuredResultReview = &structuredResultReviewTurn{Deadline: deadline}
	decision := e.checkCandidate(t.Context(), "Report the observation.", "The observation is 12.500.", "", state, nil, false)
	require.Equal(t, deadline, model.deadline)
	require.Equal(t, "not_checked_response", decision.StructuredResultReviewStatus)
	require.Equal(t, candidateAccepted, decision.Disposition)
	require.NoError(t, decision.Err)
	e.checkCandidate(t.Context(), "A new question.", "The observation is 12.500.", "", state, nil, true)
	require.Equal(t, 1, model.callCount)
	require.Equal(t, deadline, e.turnReview.StructuredResultReview.Deadline)

	e, state, _ = lightStructuredFixture(t)
	model = &deadlineLightReviewChat{mockChat: mockChat{responses: []mockResponse{lightReviewReply("keep", "")}}}
	e.chatModel = model
	parentDeadline := time.Now().Add(time.Second)
	ctx, cancel := context.WithDeadline(t.Context(), parentDeadline)
	defer cancel()
	e.checkCandidate(ctx, "Report the observation.", "The observation is 12.500.", "", state, nil, false)
	require.Equal(t, parentDeadline, model.deadline, "the existing final/parent deadline always wins")
}

func TestStructuredResultReviewCorrectionIsTheOnlyFinalStreamAndStoredAnswer(t *testing.T) {
	const original = "The observation is 12.500 tonnes."
	const corrected = "The observation is 12.500 kg."
	e, state, model := lightStructuredFixture(t, chatAnswer(original), lightReviewReply("correct", corrected))
	recorder := &terminalAnswerRecorder{}
	recorder.attach(e.eventBus)
	require.NoError(t, e.streamFinalAnswerToEventBus(t.Context(), "Report the observation.", state, "s", []chat.Message{{Role: "user", Content: "Report the observation."}}))
	require.Equal(t, corrected, state.FinalAnswer)
	done, visible := recorder.snapshot()
	require.Equal(t, 1, done)
	require.Equal(t, corrected, visible)
	require.Len(t, model.calls, 2, "one generation and one check/correction; no second critic")
	require.True(t, e.candidateApprovalCurrent(corrected, state))
}
