package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type additionsChat struct {
	mockChat
	plans            []string
	selectionCalls   [][]chat.Message
	selectionOptions []*chat.ChatOptions
	err              error
}

func (m *additionsChat) Chat(_ context.Context, messages []chat.Message, opts *chat.ChatOptions) (*types.ChatResponse, error) {
	m.selectionCalls = append(m.selectionCalls, append([]chat.Message(nil), messages...))
	m.selectionOptions = append(m.selectionOptions, opts)
	if m.err != nil {
		return nil, m.err
	}
	if len(m.plans) == 0 {
		return &types.ChatResponse{Content: additionsSelectionFixture(messages, ""), FinishReason: "stop"}, nil
	}
	content := m.plans[0]
	m.plans = m.plans[1:]
	return &types.ChatResponse{Content: additionsSelectionFixture(messages, content), FinishReason: "stop"}, nil
}

// Existing fixture declarations name exact text for readability. Only this
// test adapter resolves those declarations against the real model-facing ID
// catalog. Production accepts the ID protocol exclusively.
func additionsSelectionFixture(messages []chat.Message, content string) string {
	var packet struct {
		Hash    string                    `json:"candidate_hash"`
		Options []candidateAdditionOption `json:"candidate_spans"`
	}
	if len(messages) < 2 || json.Unmarshal([]byte(messages[1].Content), &packet) != nil {
		return content
	}
	var plan candidateAdditionsPlan
	if content != "" {
		var err error
		plan, err = decodeCandidateAdditions(content)
		if err != nil {
			return content // Includes intentionally invalid and already-ID fixtures.
		}
	}
	ids := func(texts []string) []string {
		result := []string{}
		for _, text := range texts {
			id := "fixture_span_not_in_catalog"
			for _, option := range packet.Options {
				if option.Text == text {
					id = option.ID
					break
				}
			}
			result = append(result, id)
		}
		return result
	}
	encoded, _ := json.Marshal(candidateAdditionSelection{CandidateHash: packet.Hash, RetainIDs: ids(plan.Retain), DeleteIDs: ids(plan.Delete)})
	return string(encoded)
}

func additionsPlanJSON(t *testing.T, keep, remove []string) string {
	t.Helper()
	encoded, err := json.Marshal(candidateAdditionsPlan{Retain: keep, Delete: remove})
	require.NoError(t, err)
	return string(encoded)
}

func TestCandidateAdditionsExactDeletionProtectsMeaningAndReferences(t *testing.T) {
	const main = "The value is not zero; it is 1.61 USD."
	const extra = "This document will be replaced next season."
	candidate := main + "\n\n" + extra + ` <kb doc="report.pdf" chunk_id="actual-chunk" kb_id="kb" />`
	edited, err := applyCandidateAdditions(candidate, candidateAdditionsPlan{Retain: []string{main}, Delete: []string{extra}})
	require.NoError(t, err)
	require.NotContains(t, edited, extra)
	require.Contains(t, edited, main)
	require.Contains(t, edited, "actual-chunk")
	for _, tc := range []struct {
		name, text string
		plan       candidateAdditionsPlan
	}{
		{"negation", candidate, candidateAdditionsPlan{Retain: []string{"1.61"}, Delete: []string{"not"}}},
		{"unit", candidate, candidateAdditionsPlan{Retain: []string{"1.61"}, Delete: []string{"USD"}}},
		{"decimal suffix", "Result 1.61", candidateAdditionsPlan{Retain: []string{"Result"}, Delete: []string{"61"}}},
		{"required result", candidate, candidateAdditionsPlan{Retain: []string{main}, Delete: []string{main}}},
		{"citation only", candidate, candidateAdditionsPlan{Retain: []string{main}, Delete: []string{`<kb doc="report.pdf" chunk_id="actual-chunk" kb_id="kb" />`}}},
		{"duplicate", main + "\n" + extra + "\n" + extra, candidateAdditionsPlan{Retain: []string{main}, Delete: []string{extra}}},
		{"rewrite", candidate, candidateAdditionsPlan{Retain: []string{main}, Delete: []string{"The value is 9.99 USD."}}},
		{"no protection", candidate, candidateAdditionsPlan{Delete: []string{main}}},
		{"overlap", main + "\n\nOne aside. Another aside.", candidateAdditionsPlan{Retain: []string{main}, Delete: []string{"One aside. Another aside.", "Another aside."}}},
		{"punctuation keep", "!!!\n" + main, candidateAdditionsPlan{Retain: []string{"!!!"}, Delete: []string{main}}},
		{"code fence body", main + "\n```text\nAncillary sentence.\n```", candidateAdditionsPlan{Retain: []string{main}, Delete: []string{"Ancillary sentence."}}},
		{"open code fence", main + "\n```text\nAncillary sentence.", candidateAdditionsPlan{Retain: []string{main}, Delete: []string{"Ancillary sentence."}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := applyCandidateAdditions(tc.text, tc.plan)
			require.Error(t, err)
			require.Equal(t, tc.text, result)
		})
	}
	removedWithCitation, err := applyCandidateAdditions(candidate, candidateAdditionsPlan{Retain: []string{main}, Delete: []string{extra + ` <kb doc="report.pdf" chunk_id="actual-chunk" kb_id="kb" />`}})
	require.NoError(t, err)
	require.Equal(t, main, removedWithCitation, "an unsupported optional sentence and its attached citation can leave together")
}

func TestCandidateAdditionsThreeDeliveryPathsUseEditedCandidateAndHash(t *testing.T) {
	const main = "The measured value is 138.67."
	const extra = "The publisher has no future release planned."
	const draft = main + "\n\n" + extra
	for _, path := range []string{"ordinary", "stalled", "final"} {
		t.Run(path, func(t *testing.T) {
			model := &additionsChat{plans: []string{additionsPlanJSON(t, []string{main}, []string{extra})}, mockChat: mockChat{responses: []mockResponse{chatAnswer(draft)}}}
			engine := numericRecoveryTestEngine(t, &model.mockChat)
			engine.chatModel = model
			engine.config.CandidateAdditionsEnabled = true
			engine.taskPlan = explicitTextDeliveryPlan()
			state, messages, query := numericRecoveryFixture()
			recorder := &terminalAnswerRecorder{}
			recorder.attach(engine.eventBus)
			switch path {
			case "ordinary":
				_, err := engine.executeLoop(t.Context(), state, query, messages, engine.buildToolsForLLM(), "s", "m")
				require.NoError(t, err)
			case "stalled":
				engine.finishStalledTurn(t.Context(), state, "s", &types.ChatResponse{Content: draft}, false, query, messages)
			case "final":
				require.NoError(t, engine.streamFinalAnswerToEventBus(t.Context(), query, state, "s", messages))
			}
			require.Contains(t, state.FinalAnswer, main)
			require.NotContains(t, state.FinalAnswer, extra)
			require.Equal(t, candidateTextDigest(state.FinalAnswer), engine.turnReview.Last.CandidateHash)
			require.Len(t, model.selectionCalls, 1)
			require.JSONEq(t, string(candidateAdditionsSchema), string(model.selectionOptions[0].Format))
			require.Equal(t, "none", model.selectionOptions[0].ToolChoice)
			require.Empty(t, model.selectionOptions[0].Tools)
			done, visible := recorder.snapshot()
			require.Equal(t, 1, done)
			require.Equal(t, state.FinalAnswer, visible)
			require.NotContains(t, visible, extra)
		})
	}
}

func TestCandidateAdditionsMechanicallyExcludesDiscoveryAndSelectionReasons(t *testing.T) {
	model := &additionsChat{}
	engine := numericRecoveryTestEngine(t, &model.mockChat)
	engine.chatModel = model
	engine.config.CandidateAdditionsEnabled = true
	state, messages, query := numericRecoveryFixture()
	messages = append(messages, chat.Message{Role: "tool", Content: "CONVERSATION_DIRECTORY_ONLY_NOTE"})
	state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{
		{Name: "structured_data", Args: map[string]any{"operation": "list_pdf_tables"}, Result: &types.ToolResult{Success: true, Data: map[string]any{"status": "ok", "fragments": []any{map[string]any{"footnotes": []any{"DIRECTORY_ONLY_INVENTED_NOTE"}}}}}},
		{Name: "bind_document_scope", Result: &types.ToolResult{Success: true, Data: map[string]any{"selection_reason": "MODEL_SELF_WRITTEN_UNIQUENESS"}}},
		{Name: "search_knowledge", Result: &types.ToolResult{Success: true, Data: map[string]any{"documents": []any{map[string]any{"description": "METADATA_ONLY_BODY_CLAIM"}}}}},
		{Name: "read_document", Result: documentReadResult("real-doc", "real-chunk", "Actual read body: the duration is six weeks. The literal selection_reason field belongs to this source example.")},
	}})
	_ = engine.selectCandidateAdditions(t.Context(), query, "The measured value is 138.67.", state, messages)
	require.Len(t, model.selectionCalls, 1)
	input := rawMessageText(model.selectionCalls[0])
	for _, excluded := range []string{"DIRECTORY_ONLY_INVENTED_NOTE", "MODEL_SELF_WRITTEN_UNIQUENESS", "METADATA_ONLY_BODY_CLAIM", "CONVERSATION_DIRECTORY_ONLY_NOTE"} {
		require.NotContains(t, input, excluded)
	}
	require.Contains(t, input, "Actual read body")
	require.Contains(t, input, "literal selection_reason", "body text is not metadata-keyword filtered")
	require.Contains(t, input, "138.67")
	onlyDirectories := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: state.RoundSteps[len(state.RoundSteps)-1].ToolCalls[:3]}}}
	_ = engine.selectCandidateAdditions(t.Context(), query, "A reply.", onlyDirectories, messages)
	require.Len(t, model.selectionCalls, 2, "successful discovery receives a presentation check without body evidence")
	discoveryInput := rawMessageText(model.selectionCalls[1])
	for _, excluded := range []string{"DIRECTORY_ONLY_INVENTED_NOTE", "MODEL_SELF_WRITTEN_UNIQUENESS", "METADATA_ONLY_BODY_CLAIM", "CONVERSATION_DIRECTORY_ONLY_NOTE"} {
		require.NotContains(t, discoveryInput, excluded)
	}
	require.NotContains(t, discoveryInput, "Actual read body")
}

func TestCandidateAdditionsEditedCandidateStillReachesSourceCritic(t *testing.T) {
	const main = "The measured value is 138.67."
	const scope = "The reported scope covers the sampled sites only."
	const extra = "The publisher will never update this document."
	draft := main + "\n\n" + scope + "\n\n" + extra
	model := &additionsChat{plans: []string{additionsPlanJSON(t, []string{main, scope}, []string{extra})}, mockChat: mockChat{responses: []mockResponse{chatAnswer(draft), sourceAcceptedVerdict()}}}
	engine := numericRecoveryTestEngine(t, &model.mockChat)
	engine.chatModel = model
	engine.config.CandidateAdditionsEnabled = true
	engine.taskPlan = &taskEvidencePlan{ReasoningDepth: "synthesis"}
	state, messages, query := numericRecoveryFixture()
	query += " Explain the reported coverage as well."
	state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{{Name: "read_document", ID: "scope-read", Result: documentReadResult("scope-doc", "scope-chunk", scope)}}})
	_, err := engine.executeLoop(t.Context(), state, query, messages, engine.buildToolsForLLM(), "s", "m")
	require.NoError(t, err)
	require.Len(t, model.calls, 2)
	require.Len(t, model.selectionCalls, 1)
	input := candidateReviewInput(t, &model.mockChat, 1, "candidate_answer")
	require.Equal(t, state.FinalAnswer, input["candidate_answer"])
	require.Contains(t, state.FinalAnswer, main)
	require.Contains(t, state.FinalAnswer, scope)
	require.NotContains(t, state.FinalAnswer, extra)
	require.Equal(t, candidateTextDigest(state.FinalAnswer), engine.turnReview.Last.CandidateHash)
}

func TestCandidateAdditionsCacheBindsOriginalEditedTaskAndActualEvidence(t *testing.T) {
	const main = "The measured value is 138.67."
	const extra = "A hypothetical extra claim."
	model := &additionsChat{plans: []string{additionsPlanJSON(t, []string{main}, []string{extra})}}
	engine := numericRecoveryTestEngine(t, &model.mockChat)
	engine.chatModel = model
	engine.config.CandidateAdditionsEnabled = true
	state, messages, query := numericRecoveryFixture()
	draft := main + "\n\n" + extra
	edited := engine.selectCandidateAdditions(t.Context(), query, draft, state, messages)
	require.NotContains(t, edited, extra)
	require.Equal(t, edited, engine.selectCandidateAdditions(t.Context(), query, draft, state, messages))
	require.Equal(t, edited, engine.selectCandidateAdditions(t.Context(), query, edited, state, messages))
	require.Len(t, model.selectionCalls, 1)
	_ = engine.selectCandidateAdditions(t.Context(), query+" Include the unit.", draft, state, messages)
	require.Len(t, model.selectionCalls, 2)
	state.RoundSteps[0].ToolCalls[0].Result.Output = strings.ReplaceAll(state.RoundSteps[0].ToolCalls[0].Result.Output, "138.67", "148.67")
	_ = engine.selectCandidateAdditions(t.Context(), query, draft, state, messages)
	require.Len(t, model.selectionCalls, 3)
	messages = append(messages, chat.Message{Role: "user", Content: "Keep the original qualification."})
	_ = engine.selectCandidateAdditions(t.Context(), query, draft, state, messages)
	require.Len(t, model.selectionCalls, 4)
}

func TestCandidateAdditionsInvalidOrUnavailablePlanContinuesOriginalChecks(t *testing.T) {
	const draft = "The measured value is 138.67. Keep its actual scope."
	for _, invalid := range []string{`{"retain_spans":[],"delete_spans":["138.67"]}`, `{"retain_spans":[],"delete_spans":[],"answer":"Made up rewrite"}`, `{"retain_spans":[],"retain_spans":[],"delete_spans":[]}`} {
		model := &additionsChat{plans: []string{invalid}}
		engine := numericRecoveryTestEngine(t, &model.mockChat)
		engine.chatModel = model
		engine.config.CandidateAdditionsEnabled = true
		engine.taskPlan = explicitTextDeliveryPlan()
		state, messages, query := numericRecoveryFixture()
		decision := engine.checkCandidate(t.Context(), query, draft, "", state, messages, false)
		require.Equal(t, candidateAccepted, decision.Disposition)
		require.Equal(t, draft, decision.Candidate)
	}
	model := &additionsChat{err: errors.New("selector unavailable")}
	engine := numericRecoveryTestEngine(t, &model.mockChat)
	engine.chatModel = model
	engine.config.CandidateAdditionsEnabled = true
	state, messages, query := numericRecoveryFixture()
	require.Equal(t, draft, engine.selectCandidateAdditions(t.Context(), query, draft, state, messages))
	require.Equal(t, draft, engine.selectCandidateAdditions(t.Context(), query, draft, state, messages))
	require.Len(t, model.selectionCalls, 1)
}

func TestCandidateAdditionsMissingDataKeepsBoundedGapAndDeletesUnrequestedAdvice(t *testing.T) {
	const broad = "The entire authorized archive has no requested-period figure."
	const known = "The inspected report gives 138.67 for an earlier period; this does not establish the requested period."
	const advice = "You must wait for the issuer's next bulletin to get that figure; no such bulletin is in the authorized archive."
	const draft = broad + "\n\n" + known + "\n\n" + advice
	model := &additionsChat{plans: []string{additionsPlanJSON(t, []string{known}, []string{broad, advice})}, mockChat: mockChat{responses: []mockResponse{chatAnswer(draft)}}}
	engine := numericRecoveryTestEngine(t, &model.mockChat)
	engine.chatModel, engine.config.CandidateAdditionsEnabled = model, true
	engine.taskPlan = explicitTextDeliveryPlan()
	state, messages, _ := numericRecoveryFixture()
	state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{{Name: "read_document", Result: documentReadResult("scope-doc", "scope-chunk", "This report describes the earlier observation; it does not cover the requested period.")}}})
	query := "Give the measure for the requested period."
	_, err := engine.executeLoop(t.Context(), state, query, messages, engine.buildToolsForLLM(), "s", "m")
	require.NoError(t, err)
	require.Equal(t, known, state.FinalAnswer, "deleting unsupported advice/global absence must preserve the actual gap and known reference result")
	require.Equal(t, candidateTextDigest(known), engine.turnReview.Last.CandidateHash)
	require.Len(t, model.selectionCalls, 1)
}

func TestCandidateAdditionsPreservesExplicitlyRequestedSourceSupportedGuidance(t *testing.T) {
	const known = "The inspected report gives 138.67 for the earlier period only."
	const guidance = "The report says to request missing entries from its records desk."
	const extra = "Its next release will contain all missing entries."
	draft := known + "\n\n" + guidance + "\n\n" + extra
	model := &additionsChat{plans: []string{additionsPlanJSON(t, []string{known, guidance}, []string{extra})}}
	engine := numericRecoveryTestEngine(t, &model.mockChat)
	engine.chatModel, engine.config.CandidateAdditionsEnabled = model, true
	state, messages, _ := numericRecoveryFixture()
	state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{{Name: "read_document", Result: documentReadResult("procedure-doc", "procedure-chunk", "To request missing entries, contact the records desk.")}}})
	query := "Give the observed value and explain how this report says to request missing entries."
	edited := engine.selectCandidateAdditions(t.Context(), query, draft, state, messages)
	require.Contains(t, edited, known)
	require.Contains(t, edited, guidance)
	require.NotContains(t, edited, extra)
	require.Contains(t, rawMessageText(model.selectionCalls[0]), "contact the records desk", "requested guidance retains actual read evidence rather than a guessed recommendation")
}

func TestCandidateAdditionsDiscoveryOnlyChecksDeliveryWithoutTrustingCandidates(t *testing.T) {
	const known = "The current search did not return the requested measurement."
	const extra = "The entire archive lacks it, so a future institutional release is required."
	query, draft := "Give the measurement for the requested period.", known+"\n\n"+extra
	model := &additionsChat{plans: []string{additionsPlanJSON(t, []string{known}, []string{extra})}, mockChat: mockChat{responses: []mockResponse{chatAnswer(draft)}}}
	engine := numericRecoveryTestEngine(t, &model.mockChat)
	engine.chatModel, engine.config.CandidateAdditionsEnabled = model, true
	engine.taskPlan = explicitTextDeliveryPlan()
	state := &types.AgentState{RoundSteps: []types.AgentStep{{Iteration: -1, ToolCalls: []types.ToolCall{
		{Name: "structured_data", Args: map[string]any{"operation": "list_datasets"}, Result: &types.ToolResult{Success: true, Data: map[string]any{"notes": "UNREAD_CATALOG_CONCLUSION"}}},
		{Name: "structured_data", Args: map[string]any{"operation": "describe_tools"}, Result: &types.ToolResult{Success: true, Data: map[string]any{"description": "SCHEMA_NOT_SOURCE_FACT"}}},
		{Name: "search_knowledge", Result: &types.ToolResult{Success: true, Data: map[string]any{"documents": []any{map[string]any{"description": "CANDIDATE_DESCRIPTION_NOT_BODY"}}}}},
	}}}}
	_, err := engine.executeLoop(t.Context(), state, query, []chat.Message{{Role: "user", Content: query}}, engine.buildToolsForLLM(), "s", "m")
	require.NoError(t, err)
	require.Equal(t, known, state.FinalAnswer)
	require.Equal(t, candidateTextDigest(known), engine.turnReview.Last.CandidateHash)
	require.Len(t, model.selectionCalls, 1)
	var packet map[string]any
	require.NoError(t, json.Unmarshal([]byte(model.selectionCalls[0][1].Content), &packet))
	activity := packet["source_discovery_activity"].(map[string]any)
	require.Equal(t, false, activity["body_evidence_available"])
	require.Len(t, activity["successful_operations"], 3)
	for _, marker := range []string{"UNREAD_CATALOG_CONCLUSION", "SCHEMA_NOT_SOURCE_FACT", "CANDIDATE_DESCRIPTION_NOT_BODY"} {
		require.NotContains(t, rawMessageText(model.selectionCalls[0]), marker)
	}
}

func TestCandidateAdditionsDiscoveryReceiptsInvalidateCacheAndSkipNonSourceWork(t *testing.T) {
	model := &additionsChat{}
	engine := numericRecoveryTestEngine(t, &model.mockChat)
	engine.chatModel, engine.config.CandidateAdditionsEnabled = model, true
	query, draft := "Answer the request.", "No requested value was returned by the current search."
	conversation := []chat.Message{{Role: "user", Content: query}}
	state := &types.AgentState{}
	for _, call := range []types.ToolCall{
		{},
		{Name: "unrelated_tool", Result: &types.ToolResult{Success: true}},
		{Name: "search_knowledge", Result: &types.ToolResult{Success: false}},
	} {
		state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{call}})
		require.Equal(t, draft, engine.selectCandidateAdditions(t.Context(), query, draft, state, conversation))
		require.Empty(t, model.selectionCalls)
	}
	call := types.ToolCall{Name: "structured_data", Args: map[string]any{"operation": "describe_tools"}, Result: &types.ToolResult{Success: true, Data: map[string]any{"version": "first"}}}
	state.RoundSteps = []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}
	_ = engine.selectCandidateAdditions(t.Context(), query, draft, state, conversation)
	_ = engine.selectCandidateAdditions(t.Context(), query, draft, state, conversation)
	require.Len(t, model.selectionCalls, 1)
	// Capability messages are excluded from source-consistency evidence. The
	// actual observation digest must independently invalidate the cache.
	call.Result.Data["version"] = "changed"
	_ = engine.selectCandidateAdditions(t.Context(), query, draft, state, conversation)
	require.Len(t, model.selectionCalls, 2)
	state.RoundSteps = append(state.RoundSteps, types.AgentStep{Iteration: 1, ToolCalls: []types.ToolCall{call}})
	_ = engine.selectCandidateAdditions(t.Context(), query, draft, state, conversation)
	require.Len(t, model.selectionCalls, 3)
}

func TestCandidateAdditionsSentenceBoundariesIgnoreWholeRenderedReferences(t *testing.T) {
	const citation = `<kb doc="report.pdf" chunk_id="real" kb_id="kb" />`
	for _, language := range []struct{ main, extra, tail string }{
		{"The result is 138.67.", "An unsupported publication claim.", "The required scope is retained."},
		{"结果为138.67。", "未经证实的附加声明。", "保留用户要求的口径。"},
	} {
		for _, extra := range []string{language.extra, language.extra + citation, citation + language.extra + citation} {
			candidate := language.main + citation + extra + language.tail
			edited, err := applyCandidateAdditions(candidate, candidateAdditionsPlan{Retain: []string{language.main + citation, language.tail}, Delete: []string{extra}})
			require.NoError(t, err)
			require.Equal(t, language.main+citation+language.tail, edited)
		}
	}
	const main = "The result is retained."
	for _, tc := range []struct{ text, remove string }{
		{main + citation + "The result is not safe.", "not"},
		{main + citation + "Another value is 1.61 USD.", "61 USD."},
		{main + citation + "Optional claim " + citation + "continues here.", "Optional claim " + citation},
		{main + citation + "Another statement.", citation},
		{main + citation + "Another statement.", `chunk_id="real"`},
	} {
		edited, err := applyCandidateAdditions(tc.text, candidateAdditionsPlan{Retain: []string{main}, Delete: []string{tc.remove}})
		require.Error(t, err)
		require.Equal(t, tc.text, edited)
	}
}

func TestCandidateAdditionsNumericDiagnosticUsesActualSourcesNotDiscoveryOrConversation(t *testing.T) {
	const candidate = `The reference measurement is 138.67. <kb doc="report.pdf" chunk_id="read-chunk" kb_id="read-doc" />`
	for _, source := range []string{"discovery", "query", "read"} {
		t.Run(source, func(t *testing.T) {
			model := &additionsChat{plans: []string{additionsPlanJSON(t, []string{candidate}, nil)}}
			engine := numericRecoveryTestEngine(t, &model.mockChat)
			engine.chatModel, engine.config.CandidateAdditionsEnabled = model, true
			state, _, query := numericRecoveryFixture()
			discovery := types.ToolCall{Name: "structured_data", Args: map[string]any{"operation": "list_datasets"}, Result: &types.ToolResult{Success: true, Data: map[string]any{"sample_rows": []any{map[string]any{"value": 138.67}}, "description": "138.67"}}}
			switch source {
			case "discovery":
				state.RoundSteps = []types.AgentStep{{ToolCalls: []types.ToolCall{discovery}}}
			case "query":
				state.RoundSteps[0].ToolCalls = append(state.RoundSteps[0].ToolCalls, discovery)
			case "read":
				state.RoundSteps = []types.AgentStep{{ToolCalls: []types.ToolCall{discovery, {Name: "read_document", ID: "actual-read", Result: documentReadResult("read-doc", "read-chunk", "The measured value is 138.67.")}}}}
			}
			conversation := []chat.Message{{Role: "user", Content: query + " I previously mentioned 138.67."}, {Role: "assistant", Content: "Earlier I guessed 138.67."}}
			require.Equal(t, candidate, engine.selectCandidateAdditions(t.Context(), query, candidate, state, conversation), "diagnostics do not automatically remove required text")
			require.Len(t, model.selectionCalls, 1)
			var packet map[string]any
			require.NoError(t, json.Unmarshal([]byte(model.selectionCalls[0][1].Content), &packet))
			diagnostic := packet["actual_body_numeric_diagnostic"].(map[string]any)
			if source == "discovery" {
				require.Empty(t, ungroundedStructuredNumbers(candidate, nil), "ordinary no-source conversation keeps its existing skip")
				require.Equal(t, float64(1), diagnostic["unsupported_token_count"])
				issue := diagnostic["unsupported_tokens"].([]any)[0].(map[string]any)
				require.Equal(t, "138.67", issue["token"])
			} else {
				require.Equal(t, float64(0), diagnostic["unsupported_token_count"])
			}
		})
	}
}

func TestCandidateAdditionsNoBodyReferenceParagraphCanLeaveWhileGapRemains(t *testing.T) {
	const gap = "The current search has not supplied the requested-period measurement."
	const reference = "A nearby-period reference is 138.67, according to the candidate table."
	model := &additionsChat{plans: []string{additionsPlanJSON(t, []string{gap}, []string{reference})}}
	engine := numericRecoveryTestEngine(t, &model.mockChat)
	engine.chatModel, engine.config.CandidateAdditionsEnabled = model, true
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{Name: "search_knowledge", Result: &types.ToolResult{Success: true, Data: map[string]any{"documents": []any{map[string]any{"description": "A candidate measurement is 138.67."}}}}}}}}}
	require.Equal(t, gap, engine.selectCandidateAdditions(t.Context(), "Give the requested-period measurement.", gap+"\n\n"+reference, state, nil))
	var packet map[string]any
	require.NoError(t, json.Unmarshal([]byte(model.selectionCalls[0][1].Content), &packet))
	diagnostic := packet["actual_body_numeric_diagnostic"].(map[string]any)
	require.Equal(t, float64(1), diagnostic["unsupported_token_count"])
	require.Equal(t, float64(0), packet["source_discovery_activity"].(map[string]any)["omitted_operation_count"])
}

func additionOptionForText(t *testing.T, catalog candidateAdditionCatalog, text string) candidateAdditionOption {
	t.Helper()
	for _, option := range catalog.Options {
		if option.Text == text {
			return option
		}
	}
	t.Fatalf("missing candidate option for %q", text)
	return candidateAdditionOption{}
}

func additionSelectionJSON(t *testing.T, catalog candidateAdditionCatalog, retain, remove []string) string {
	t.Helper()
	encoded, err := json.Marshal(candidateAdditionSelection{CandidateHash: catalog.CandidateHash, RetainIDs: retain, DeleteIDs: remove})
	require.NoError(t, err)
	return string(encoded)
}

func TestCandidateAdditionIDsPreserveUnicodeFormattingAndBoundReferences(t *testing.T) {
	const main = "测量值为 **138.67 单位**"
	const aside = "（未请求的换算为7.25）"
	const citation = `<kb doc="report.pdf" chunk_id="real" kb_id="kb" />`
	const scope = "保留用户要求的范围。" + citation
	candidate := main + aside + "。\n\n" + scope
	catalog := buildCandidateAdditionCatalog(candidate)
	require.Equal(t, catalog, buildCandidateAdditionCatalog(candidate))
	require.True(t, catalog.Complete)
	core := additionOptionForText(t, catalog, main)
	optional := additionOptionForText(t, catalog, aside)
	protectedScope := additionOptionForText(t, catalog, scope)
	require.False(t, core.CanDelete)
	require.Equal(t, "parenthetical", optional.Kind)
	require.True(t, optional.CanDelete)
	edited, err := applyCandidateAdditionSelection(candidate, catalog, candidateAdditionSelection{CandidateHash: catalog.CandidateHash, RetainIDs: []string{core.ID, protectedScope.ID}, DeleteIDs: []string{optional.ID}})
	require.NoError(t, err)
	require.Equal(t, main+"。\n\n"+scope, edited)
	require.Contains(t, edited, citation)
	_, err = applyCandidateAdditions(candidate, candidateAdditionsPlan{Retain: []string{main}, Delete: []string{aside}})
	require.ErrorContains(t, err, "partial sentence", "legacy exact-text helpers must not silently gain inline deletion")
}

func TestCandidateAdditionIDsDistinguishRepeatedWholeStatements(t *testing.T) {
	const main = "Keep the requested measurement and its unit."
	const repeated = "An optional ancillary statement."
	candidate := main + "\n\n" + repeated + "\n\n" + repeated
	catalog := buildCandidateAdditionCatalog(candidate)
	var copies []candidateAdditionOption
	for _, option := range catalog.Options {
		if option.Text == repeated {
			copies = append(copies, option)
		}
	}
	require.Len(t, copies, 2)
	require.NotEqual(t, copies[0].ID, copies[1].ID)
	core := additionOptionForText(t, catalog, main)
	edited, err := applyCandidateAdditionSelection(candidate, catalog, candidateAdditionSelection{CandidateHash: catalog.CandidateHash, RetainIDs: []string{core.ID}, DeleteIDs: []string{copies[1].ID}})
	require.NoError(t, err)
	require.Equal(t, main+"\n\n"+repeated, edited)
}

func TestCandidateAdditionIDsRejectStaleUnknownOverlappingAndPartialOptions(t *testing.T) {
	const main = "The requested value is **138.67 units**"
	const aside = "（an optional aside）"
	candidate := main + aside + ".\n\nA required explanation."
	catalog := buildCandidateAdditionCatalog(candidate)
	core := additionOptionForText(t, catalog, main)
	optional := additionOptionForText(t, catalog, aside)
	whole := additionOptionForText(t, catalog, main+aside+".")
	for _, tc := range []struct {
		name string
		plan candidateAdditionSelection
	}{
		{"unknown ID", candidateAdditionSelection{catalog.CandidateHash, []string{core.ID}, []string{"span_999"}}},
		{"text is not an ID", candidateAdditionSelection{catalog.CandidateHash, []string{core.ID}, []string{aside}}},
		{"retain-only deletion", candidateAdditionSelection{catalog.CandidateHash, []string{whole.ID}, []string{core.ID}}},
		{"protected whole answer", candidateAdditionSelection{catalog.CandidateHash, []string{whole.ID}, []string{optional.ID}}},
		{"repeated deletion ID", candidateAdditionSelection{catalog.CandidateHash, []string{core.ID}, []string{optional.ID, optional.ID}}},
		{"same retain and delete ID", candidateAdditionSelection{catalog.CandidateHash, []string{optional.ID}, []string{optional.ID}}},
		{"wrong candidate hash", candidateAdditionSelection{strings.Repeat("0", 64), []string{core.ID}, []string{optional.ID}}},
		{"no protected answer", candidateAdditionSelection{catalog.CandidateHash, nil, []string{optional.ID}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			edited, err := applyCandidateAdditionSelection(candidate, catalog, tc.plan)
			require.Error(t, err)
			require.Equal(t, candidate, edited)
		})
	}
	changed := strings.Replace(candidate, "units", "unit", 1)
	edited, err := applyCandidateAdditionSelection(changed, catalog, candidateAdditionSelection{catalog.CandidateHash, []string{core.ID}, []string{optional.ID}})
	require.ErrorContains(t, err, "hash mismatch")
	require.Equal(t, changed, edited)
}

func TestCandidateAdditionCatalogKeepsStructuralCodeCitationAndParenthesisBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       []string
		reject     []string
	}{
		{"nested", "Main result（a complete (nested) aside）.", []string{"（a complete (nested) aside）"}, []string{"(nested)"}},
		{"mixed mismatch", "Main result（a mismatched aside).", nil, []string{"（a mismatched aside)"}},
		{"inline code", "Keep `f(argument)` and （optional note）.", []string{"（optional note）"}, []string{"(argument)"}},
		{"fenced code", "Keep the result.\n```text\nf(argument)\n```", nil, []string{"(argument)"}},
		{"Markdown link", "Keep [source](https://example.invalid/reference).", nil, []string{"(https://example.invalid/reference)"}},
		{"citation identity", `Keep the result.<kb doc="report(extra).pdf" chunk_id="actual" kb_id="kb" />`, nil, []string{"(extra)"}},
		{"crossed emphasis", "Keep result（**optional note）**.", nil, []string{"（**optional note）"}},
		{"crossed single star", "Keep result（*optional note）*.", nil, []string{"（*optional note）"}},
		{"crossed single underscore", "Keep result（_optional note）_.", nil, []string{"（_optional note）"}},
		{"crossed strikethrough", "Keep result（~~optional note）~~.", nil, []string{"（~~optional note）"}},
		{"strikethrough starts before bracket", "Keep ~~result（optional note~~）.", nil, []string{"（optional note~~）"}},
		{"complete emphasis inside", "Keep result（an *optional* note）.", []string{"（an *optional* note）"}, nil},
		{"complete strikethrough inside", "Keep result（an ~~optional~~ note）.", []string{"（an ~~optional~~ note）"}, nil},
		{"escaped brackets", `Keep result \(optional\).`, nil, []string{`(optional\)`}},
		{"escaped math with nested parentheses", `Keep \(f(argument)\) and （optional note）.`, []string{"（optional note）"}, []string{"(argument)"}},
		{"escaped opening bracket", `Keep result \(optional).`, nil, []string{"(optional)"}},
		{"escaped closing bracket", `Keep result (optional\).`, nil, []string{`(optional\)`}},
		{"bare URI", "Keep result custom+type://local/part(note).", nil, []string{"(note)"}},
		{"autolink URI", "Keep <custom://local/part(note)>.", nil, []string{"(note)"}},
		{"artifact URI", "Keep artifact://current/plot(note).", nil, []string{"(note)"}},
		{"URN without authority", "Keep urn:example:part(optional).", nil, []string{"(optional)"}},
		{"data URI without authority", "Keep data:text/plain,part(optional).", nil, []string{"(optional)"}},
		{"mail URI without authority", "Keep mailto:reader+part(optional)@example.invalid.", nil, []string{"(optional)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := buildCandidateAdditionCatalog(tc.text)
			for _, text := range tc.want {
				require.Equal(t, "parenthetical", additionOptionForText(t, catalog, text).Kind)
			}
			for _, option := range catalog.Options {
				for _, text := range tc.reject {
					require.NotEqual(t, text, option.Text)
				}
				for _, loc := range documentAnswerCitation.FindAllStringIndex(tc.text, -1) {
					citation := candidateTextSpan{loc[0], loc[1]}
					if additionsOverlap(option.span, citation) {
						require.LessOrEqual(t, option.span.start, citation.start)
						require.GreaterOrEqual(t, option.span.end, citation.end)
					}
				}
			}
		})
	}
	const protected = "Keep the requested result."
	const openFence = protected + "\n\n```text\nAn optional statement."
	catalog := buildCandidateAdditionCatalog(openFence)
	core := additionOptionForText(t, catalog, protected)
	optional := additionOptionForText(t, catalog, "An optional statement.")
	edited, err := applyCandidateAdditionSelection(openFence, catalog, candidateAdditionSelection{catalog.CandidateHash, []string{core.ID}, []string{optional.ID}})
	require.ErrorContains(t, err, "unbalanced code fence")
	require.Equal(t, openFence, edited)
}

func TestCandidateAdditionURIIdentityAllowsOnlyWholeStatementDeletion(t *testing.T) {
	const main = "Keep the requested measurement and its unit."
	for _, uri := range []string{"urn:example:part(optional)", "data:text/plain,part(optional)", "custom+type:part(optional)"} {
		t.Run(uri, func(t *testing.T) {
			optional := "An unrequested reference is " + uri + "."
			candidate := main + "\n\n" + optional
			catalog := buildCandidateAdditionCatalog(candidate)
			for _, option := range catalog.Options {
				require.NotEqual(t, "(optional)", option.Text)
			}
			core := additionOptionForText(t, catalog, main)
			whole := additionOptionForText(t, catalog, optional)
			require.True(t, whole.CanDelete)
			edited, err := applyCandidateAdditionSelection(candidate, catalog, candidateAdditionSelection{catalog.CandidateHash, []string{core.ID}, []string{whole.ID}})
			require.NoError(t, err)
			require.Equal(t, main, edited)
		})
	}
}

func TestCandidateAdditionIDProtocolIsStrictAndDoesNotNormalizeText(t *testing.T) {
	hash := candidateTextDigest("A current candidate.")
	valid := `{"candidate_hash":"` + hash + `","retain_ids":[],"delete_ids":[]}`
	_, err := decodeCandidateAdditionSelection(valid)
	require.NoError(t, err)
	for _, invalid := range []string{
		`{"retain_spans":[],"delete_spans":[]}`,
		strings.Replace(valid, `"retain_ids":[]`, `"retain_ids":null`, 1),
		strings.Replace(valid, `"retain_ids":[]`, `"retain_ids":[],"retain_ids":[]`, 1),
		strings.Replace(valid, `"delete_ids":[]`, `"delete_ids":[],"answer":"replacement"`, 1),
		valid + valid,
		strings.Replace(valid, `"delete_ids":[]`, `"delete_ids":[""]`, 1),
	} {
		_, err := decodeCandidateAdditionSelection(invalid)
		require.Error(t, err)
	}
	composed := "Keep Café（optional）."
	decomposed := "Keep Cafe\u0301（optional）."
	catalog := buildCandidateAdditionCatalog(composed)
	core := additionOptionForText(t, catalog, "Keep Café")
	optional := additionOptionForText(t, catalog, "（optional）")
	edited, err := applyCandidateAdditionSelection(decomposed, catalog, candidateAdditionSelection{catalog.CandidateHash, []string{core.ID}, []string{optional.ID}})
	require.ErrorContains(t, err, "hash mismatch")
	require.Equal(t, decomposed, edited)
}

func TestCandidateAdditionCatalogBoundsModelFacingOptions(t *testing.T) {
	candidate := strings.Repeat("A supported measurement（an optional aside）.\n", 1000)
	catalog := buildCandidateAdditionCatalog(candidate)
	require.LessOrEqual(t, len(catalog.Options), 128)
	require.Positive(t, catalog.Omitted)
	require.False(t, catalog.Complete)
	textBytes := 0
	for _, option := range catalog.Options {
		textBytes += len(option.Text)
		require.Equal(t, option.Text, candidate[option.span.start:option.span.end])
	}
	require.LessOrEqual(t, textBytes, 32768)
}

func TestCandidateAdditionInlineIDsStillRunNumericChecksWithoutExtraSourceCalls(t *testing.T) {
	for _, value := range []string{"138.67", "9.99"} {
		t.Run(value, func(t *testing.T) {
			main := "The measured value is **" + value + " units**"
			const aside = "（an unrequested conversion is 7.25）"
			draft := main + aside + "."
			catalog := buildCandidateAdditionCatalog(draft)
			core := additionOptionForText(t, catalog, main)
			optional := additionOptionForText(t, catalog, aside)
			model := &additionsChat{plans: []string{additionSelectionJSON(t, catalog, []string{core.ID}, []string{optional.ID})}}
			engine := numericRecoveryTestEngine(t, &model.mockChat)
			engine.chatModel, engine.config.CandidateAdditionsEnabled = model, true
			engine.taskPlan = explicitTextDeliveryPlan()
			state, conversation, query := numericRecoveryFixture()
			before := countTotalToolCalls(state.RoundSteps)
			decision := engine.checkCandidate(t.Context(), query, draft, "", state, conversation, false)
			require.Equal(t, main+".", decision.Candidate)
			if value == "138.67" {
				// "units" is not an invented numerical observation; the actual
				// read fixture supports this value under the ordinary guard.
				require.Equal(t, candidateAccepted, decision.Disposition)
			} else {
				require.Equal(t, candidateNumericFailure, decision.Disposition)
			}
			require.Equal(t, before, countTotalToolCalls(state.RoundSteps))
			require.Len(t, model.selectionCalls, 1)
			require.Empty(t, model.calls)
		})
	}
}
