package agent

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
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
	if action == "keep" && answer == "" {
		raw, _ := json.Marshal(map[string]any{"action": "keep", "edits": []structuredResultEdit{}})
		return deliveryReply(string(raw), "stop", types.TokenUsage{PromptTokens: 40, CompletionTokens: 12, TotalTokens: 52})
	}
	raw, _ := json.Marshal(map[string]string{"action": action, "answer": answer})
	return deliveryReply(string(raw), "stop", types.TokenUsage{PromptTokens: 40, CompletionTokens: 12, TotalTokens: 52})
}

func lightReviewEdits(edits ...structuredResultEdit) mockResponse {
	raw, _ := json.Marshal(map[string]any{"action": "correct", "edits": edits})
	return deliveryReply(string(raw), "stop", types.TokenUsage{PromptTokens: 40, CompletionTokens: 12, TotalTokens: 52})
}

func TestStructuredResultReviewCorrectsOnceAndKeepsApprovalBound(t *testing.T) {
	const original = "The observation is 12.500 tonnes."
	const corrected = "The observation is 12.500 kg."
	e, state, model := lightStructuredFixture(t, lightReviewEdits(structuredResultEdit{"tonnes", "kg"}))
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
	require.Equal(t, structuredResultReviewInitialTokens, model.opts[0].MaxCompletionTokens)
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

func TestStructuredResultReviewDoesNotDeleteAValidRowOutsideItsProjection(t *testing.T) {
	const original = "The requested final observation is 12.500 kg."
	e, state, model := lightStructuredFixture(t, lightReviewEdits(structuredResultEdit{original, "Only earlier observations are available."}))
	data := state.RoundSteps[0].ToolCalls[0].Result.Data
	for i, row := range data["rows"].([]any) {
		row.(map[string]any)["provenance_detail"] = strings.Repeat(string(rune('a'+i)), 9000)
	}
	rehashLightFixture(t, data)
	selected := currentStructuredResultReviewState(state)
	view := structuredQueryRowReviewEvidence(selected, e.sourceConsistencyMessages(selected, nil), structuredQueryRowReviewBudget)
	entries := view["entries"].([]structuredQueryRowCompanionEntry)
	require.Less(t, entries[0].RetainedRowCount, entries[0].SourceRowCount)
	before := reviewDigest(state.RoundSteps)
	decision := e.checkCandidate(t.Context(), "Report the final observation.", original, "", state, nil, false)
	require.Equal(t, candidateAccepted, decision.Disposition)
	require.Equal(t, original, decision.Candidate)
	require.Equal(t, "not_checked_row_omission", decision.StructuredResultReviewStatus)
	require.Empty(t, model.calls)
	require.Equal(t, before, reviewDigest(state.RoundSteps))
	require.True(t, e.candidateApprovalCurrent(original, state))
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
				reply = lightReviewEdits(structuredResultEdit{"12.500", "99999991"})
			case "invented_citation":
				reply = lightReviewEdits(structuredResultEdit{"12.500.", `12.500 kg.<evidence evidence_id="invented" />`})
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
	model.responses = []mockResponse{lightReviewEdits(structuredResultEdit{"tonnes", "kg"})}
	decision := e.checkCandidate(t.Context(), "Report the observation.", original, "", state, nil, false)
	require.Equal(t, "corrected", decision.StructuredResultReviewStatus)
	require.Contains(t, decision.Candidate, `<evidence evidence_id="`+id+`" />`)
	require.NotContains(t, decision.Candidate, "evidence://")
	require.Contains(t, model.calls[0][1].Content, handle)
	state.FinalAnswer = decision.Candidate
	require.Len(t, e.finalAnswerEvidenceReferences(state), 1)
}

func TestStructuredResultReviewMalformedVerdictIsNeverApproval(t *testing.T) {
	for _, value := range []string{`{"action":"keep"}`, `{"action":"keep","answer":null}`, `{"action":"keep","answer":"","answer":""}`, `{"action":"keep","answer":""} extra`, `{"action":"keep","answer":"","other":true}`,
		`{"action":"keep","edits":null}`, `{"action":"correct","edits":[]}`, `{"action":"correct","edits":[{"old":"x","new":"y","new":"z"}]}`,
		`{"action":"correct","edits":[{"old":"","new":"x"}]}`, `{"action":"correct","edits":[{"old":"x","new":null}]}`,
		`{"action":"correct","edits":[{"old":"x","new":"x"}]}`, `{"action":"correct","answer":"rewritten answer"}`} {
		_, _, valid := decodeStructuredResultReview(value)
		require.False(t, valid, value)
	}
	for _, value := range []string{`{"action":"keep","answer":""}`, `{"action":"keep","edits":[]}`} {
		action, edits, valid := decodeStructuredResultReview(value)
		require.True(t, valid)
		require.Equal(t, "keep", action)
		require.Empty(t, edits)
	}
}

func TestStructuredResultReviewExactEditsPreserveCheckedCoreAndCitation(t *testing.T) {
	e, state, model := lightStructuredFixture(t)
	e.currentStructuredCitationView(state.RoundSteps[0].ToolCalls[0])
	id, _ := selectRawCitation(t, e, 1, 1)
	core := `The observation is 12.500 kg.<evidence evidence_id="` + id + `" />`
	const comparison = " This group's average is the highest across every group."
	const cause = " Missing observations must be caused by holidays."
	model.responses = []mockResponse{lightReviewEdits(structuredResultEdit{comparison, ""}, structuredResultEdit{cause, ""})}
	decision := e.checkCandidate(t.Context(), "Report the observation.", core+comparison+cause, "", state, nil, false)
	require.Equal(t, candidateAccepted, decision.Disposition)
	require.Equal(t, core, decision.Candidate)
	require.Equal(t, "corrected", decision.StructuredResultReviewStatus)
	require.Equal(t, 0, e.turnReview.Repairs)
	require.Len(t, model.calls, 1)
	require.True(t, e.candidateApprovalCurrent(core, state))
	require.NotContains(t, string(model.opts[0].Format), `"answer"`, "the model is never asked to regenerate the checked answer")
}

func TestStructuredResultReviewExactPatchBoundsAndRejections(t *testing.T) {
	for _, test := range []struct {
		name, candidate, expected, failure string
		edits                              []structuredResultEdit
	}{
		{name: "unicode", candidate: "原值12.500；原因待查。", expected: "原值12.500。", edits: []structuredResultEdit{{"；原因待查", ""}}},
		{name: "order", candidate: "Alpha 12.500 kg. Beta uncertain.", expected: "Alpha 12.500 kg.", edits: []structuredResultEdit{{" Beta uncertain.", ""}}},
		{name: "missing", candidate: "Alpha 12.500 kg.", failure: "patch_not_unique", edits: []structuredResultEdit{{"absent", "correct"}}},
		{name: "ambiguous", candidate: "Alpha Alpha", failure: "patch_not_unique", edits: []structuredResultEdit{{"Alpha", "Beta"}}},
		{name: "overlapping_occurrences", candidate: "aaa", failure: "patch_not_unique", edits: []structuredResultEdit{{"aa", "b"}}},
		{name: "overlap", candidate: "Alpha Beta Gamma", failure: "patch_overlap", edits: []structuredResultEdit{{"Alpha Beta", "A"}, {"Beta Gamma", "B"}}},
		{name: "empty_result", candidate: "Alpha", failure: "patch_answer_budget", edits: []structuredResultEdit{{"Alpha", ""}}},
		{name: "answer_budget", candidate: "Alpha", failure: "patch_answer_budget", edits: []structuredResultEdit{{"Alpha", strings.Repeat("x", structuredResultReviewAnswerBytes+1)}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			answer, failure := applyStructuredResultEdits(test.candidate, test.edits)
			require.Equal(t, test.failure, failure)
			if failure == "" {
				require.Equal(t, test.expected, answer)
			}
		})
	}
	for _, edits := range [][]structuredResultEdit{
		{{"a", "b"}, {"c", "d"}, {"e", "f"}, {"g", "h"}, {"i", "j"}},
		{{"a", strings.Repeat("b", structuredResultReviewAnswerBytes)}},
	} {
		raw, _ := json.Marshal(map[string]any{"action": "correct", "edits": edits})
		_, _, valid := decodeStructuredResultReview(string(raw))
		require.False(t, valid)
	}
}

func TestStructuredResultReviewInvalidPatchHasOnlyOneProtocolRepair(t *testing.T) {
	for _, edits := range [][]structuredResultEdit{
		{{"missing", "replacement"}},
		{{"The observation", "Observed"}, {"observation is", "value is"}},
	} {
		e, state, model := lightStructuredFixture(t, lightReviewEdits(edits...), lightReviewEdits(edits...))
		const core = "The observation is 12.500 kg."
		decision := e.checkCandidate(t.Context(), "Report the observation.", core, "", state, nil, false)
		require.Equal(t, core, decision.Candidate)
		require.Equal(t, "not_checked_invalid_correction", decision.StructuredResultReviewStatus)
		e.checkCandidate(t.Context(), "Report the observation.", core, "", state, nil, true)
		require.Len(t, model.calls, 2)
		require.Equal(t, 0, e.turnReview.Repairs)
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
	e, state, model := lightStructuredFixture(t, chatAnswer(original), lightReviewEdits(structuredResultEdit{"tonnes", "kg"}))
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

func TestStructuredResultReviewExecuteLoopWithLargeRuntimePrompt(t *testing.T) {
	for _, outcome := range []string{"keep", "correct", "unavailable"} {
		t.Run(outcome, func(t *testing.T) {
			const original = "The observation is 12.500."
			const correction = "The observation is 12.500 kg."
			review := lightReviewReply("keep", "")
			expected := original
			if outcome == "correct" {
				review, expected = lightReviewEdits(structuredResultEdit{"12.500.", "12.500 kg."}), correction
			}
			if outcome == "unavailable" {
				review = chatAnswer(`{"action":`)
			}
			e, state, model := lightStructuredFixture(t, chatAnswer(original), review)
			// Native tools attach this typed transport witness after the service
			// response was parsed. Validation must not turn it into a generic map:
			// changed JSON field ordering would invalidate the source identity.
			state.RoundSteps[0].ToolCalls[0].Result.Data["_structured_execution"] = agenttools.StructuredOperationDescriptor{
				Operation: "read_pdf_table", ToolName: "structured_read_pdf_table", Generation: 1,
				ScopeMode: "knowledge_base", EffectClass: "read",
			}
			// The real source flow reads a directory before querying. Its existing
			// catalog facts qualify source review without visiting the later v2
			// result, so the new trigger becomes that result's first validation.
			state.RoundSteps[0].ToolCalls = append([]types.ToolCall{{ID: "current-directory", Name: "structured_list_series", Result: &types.ToolResult{
				Success: true, Data: map[string]any{"catalog_summary": map[string]any{"kind": "catalog_group_counts"}},
				Output: `{"catalog_summary":{"kind":"catalog_group_counts"}}`,
			}}}, state.RoundSteps[0].ToolCalls...)
			originalEvidence := reviewDigest(e.sourceConsistencyMessages(state, nil))
			recorder := &terminalAnswerRecorder{}
			recorder.attach(e.eventBus)
			messages := []chat.Message{{Role: "system", Content: strings.Repeat("Runtime protocol. ", 3000)}, {Role: "user", Content: "Report the observation."}}
			_, err := e.executeLoop(t.Context(), state, "Report the observation.", messages, emptyTools(), "s", "m")
			require.NoError(t, err)
			require.Equal(t, originalEvidence, reviewDigest(e.sourceConsistencyMessages(state, nil)), "checking must preserve the exact native execution witness")
			t.Logf("candidate_identity=%v task_identity=%v evidence_identity=%v policy_identity=%v status=%s", e.turnReview.Last.CandidateHash == candidateTextDigest(expected), e.turnReview.Last.TaskHash == e.candidateTaskDigest(e.turnReview.Query, e.turnReview.TaskConversation), e.turnReview.Last.EvidenceHash == reviewDigest(e.sourceConsistencyMessages(state, e.turnReview.Conversation)), e.turnReview.Last.StructuredResultReview == e.structuredResultReviewEnabled(), e.turnReview.Last.StructuredResultReviewStatus)
			require.Equal(t, expected, state.FinalAnswer)
			require.Equal(t, 2, len(model.calls))
			require.NotContains(t, model.calls[1][1].Content, "Runtime protocol.")
			require.LessOrEqual(t, len(model.calls[1][1].Content), structuredResultReviewInputBytes)
			require.True(t, e.candidateApprovalCurrent(expected, state))
			done, visible := recorder.snapshot()
			require.Equal(t, 1, done)
			require.Equal(t, expected, visible)
		})
	}
}

func TestStructuredResultReviewTypedWitnessProjectionStillRejectsChangedValues(t *testing.T) {
	e, state, _ := lightStructuredFixture(t)
	state.RoundSteps[0].ToolCalls[0].Result.Data["_structured_execution"] = agenttools.StructuredOperationDescriptor{
		Operation: "read_pdf_table", ToolName: "structured_read_pdf_table", Generation: 1, ScopeMode: "knowledge_base", EffectClass: "read",
	}
	before := reviewDigest(state.RoundSteps)
	messages := e.sourceConsistencyMessages(state, nil)
	require.NotNil(t, structuredQueryRowReviewEvidence(state, messages, structuredQueryRowReviewBudget))
	require.Equal(t, before, reviewDigest(state.RoundSteps))
	changed := false
	for i := range messages {
		if strings.Contains(messages[i].Content, `"kg"`) {
			messages[i].Content = strings.ReplaceAll(messages[i].Content, `"kg"`, `"other_unit"`)
			changed = true
		}
	}
	require.True(t, changed)
	require.Nil(t, structuredQueryRowReviewEvidence(state, messages, structuredQueryRowReviewBudget), "field ordering may differ, but changed evidence values never qualify")
	require.Equal(t, before, reviewDigest(state.RoundSteps))
}

func TestStructuredResultReviewPopulationChangeKeepsOriginalFindingsAndHumanScope(t *testing.T) {
	const original = "The observed value is 12.500 kg. Filtered summary: 12.500 kg after dropping high observations."
	const optional = " Filtered summary: 12.500 kg after dropping high observations."
	for _, requested := range []bool{false, true} {
		t.Run(map[bool]string{false: "not_requested", true: "requested"}[requested], func(t *testing.T) {
			reply := lightReviewEdits(structuredResultEdit{optional, ""})
			question := "Describe the original observations."
			if requested {
				reply = lightReviewReply("keep", "")
				question = "Describe the observations and additionally summarize the subset below the specified upper bound."
			}
			e, state, model := lightStructuredFixture(t, reply)
			data := state.RoundSteps[0].ToolCalls[0].Result.Data
			data["sql"] = "SELECT value FROM data WHERE value < 80"
			contract := data["result_contract"].(map[string]any)
			keys := append(contract["hash_input_keys"].([]any), "sql")
			sort.Slice(keys, func(i, j int) bool { return keys[i].(string) < keys[j].(string) })
			contract["hash_input_keys"] = keys
			rehashLightFixture(t, data)
			before := reviewDigest(state.RoundSteps)
			d := e.checkCandidate(t.Context(), question, original, "", state, nil, false)
			require.Equal(t, candidateAccepted, d.Disposition)
			require.Contains(t, d.Candidate, "The observed value is 12.500 kg.")
			if requested {
				require.Equal(t, original, d.Candidate)
			} else {
				require.NotContains(t, d.Candidate, "Filtered summary")
			}
			require.Equal(t, before, reviewDigest(state.RoundSteps), "the editor never changes the executed filter or raw source")
			require.Len(t, model.calls, 1)
			require.Contains(t, model.calls[0][1].Content, question)
			require.Contains(t, model.calls[0][1].Content, "SELECT value FROM data WHERE value")
			require.Contains(t, model.calls[0][0].Content, "disclosing such cleaning does not authorize")
			require.Contains(t, model.calls[0][0].Content, "genuine null/parse-error records")
			require.Contains(t, model.calls[0][0].Content, "source-verified invalid-record handling")
			require.Contains(t, model.calls[0][0].Content, "Never remove only the cleaning disclosure")
		})
	}
}
