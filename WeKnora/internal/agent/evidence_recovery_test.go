package agent

import (
	"context"
	"encoding/json"
	"html"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestSourceReviewRecoveryDispositionProtocol(t *testing.T) {
	tests := []struct {
		name       string
		verdict    string
		accepted   bool
		wantRepair bool
		wantAction string
		wantError  bool
	}{
		{name: "collect evidence", verdict: `{"complete":false,"feedback":"The cited excerpt omits the requested period.","next_action":"collect_evidence"}`, wantRepair: true, wantAction: reviewCollectEvidence},
		{name: "revise existing evidence", verdict: `{"complete":false,"feedback":"The source supports the result; remove the extra inference.","next_action":"revise_answer"}`, wantRepair: true, wantAction: reviewReviseAnswer},
		{name: "accepted without disposition", accepted: true, wantRepair: false},
		{name: "accepted with disposition conflicts", verdict: `{"complete":true,"next_action":"collect_evidence"}`, wantRepair: true, wantError: true},
		{name: "null disposition is invalid", verdict: `{"complete":false,"next_action":null}`, wantRepair: true, wantError: true},
		{name: "unknown disposition is invalid", verdict: `{"complete":false,"next_action":"search_everything"}`, wantRepair: true, wantError: true},
		{name: "unknown protocol field is invalid", verdict: `{"complete":false,"next_action":"collect_evidence","decision":"trust the draft"}`, wantRepair: true, wantError: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			first := sourceConsistencyVerdict(tc.verdict)
			if tc.accepted {
				first = sourceAcceptedVerdict()
			}
			responses := []mockResponse{first}
			if tc.wantError {
				responses = append(responses, sourceConsistencyVerdict(`{"complete":false,"feedback":"The excerpt remains incomplete.","next_action":"collect_evidence"}`))
			}
			model := &mockChat{responses: responses}
			engine := sourceConsistencyEngine(t, model)
			repair, err := engine.answerNeedsRepair(t.Context(), "What does the field report say about its sampling period?", "The report states a sampling period.", "", sourceConsistencyState())
			if tc.wantError {
				require.NoError(t, err, "protocol errors should retry the same review once")
				require.Len(t, model.calls, 2)
				require.Equal(t, reviewCollectEvidence, engine.answerReviewAction)
				require.True(t, repair)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantRepair, repair)
			require.Equal(t, tc.wantAction, engine.answerReviewAction)
			require.Len(t, model.calls, 1)
		})
	}
}

func TestResumeEvidenceCollectionRestoresLiveTranscriptAndAuthorizedTools(t *testing.T) {
	model := &mockChat{}
	engine := sourceConsistencyEngine(t, model)
	engine.structuredReviewMode = true
	engine.taskProjection = &taskToolProjection{selected: map[string]bool{"unrelated_tool": true}}
	original := []chat.Message{
		{Role: "system", Content: "Use the authorized sources for this research task."},
		{Role: "user", Content: "What observation interval does the field report specify?"},
		{Role: "tool", Name: "read_document", ToolCallID: "initial-read", Content: `{"chunk_id":"initial-chunk","content":"The field report describes the project but omits the interval."}`},
	}
	draft := "The interval is six weeks."
	review := independentStructuredReview(original, original[1].Content, draft)
	guards := &loopGuards{incompleteAnswerRetries: 2, structuredAnswerReviewed: true, structuredReviewEvidence: append([]chat.Message(nil), original...), structuredReviewInput: review[1].Content, sawPlainRound: true, consecutiveSameContent: 2}
	messages := append([]chat.Message(nil), review...)
	messages = append(messages,
		chat.Message{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: "continuation", Function: chat.FunctionCall{Name: "read_document", Arguments: `{"id":"appendix"}`}}}},
		chat.Message{Role: "tool", Name: "read_document", ToolCallID: "continuation", Content: `{"chunk_id":"appendix-page","content":"The appendix specifies a twelve-week observation interval."}`},
	)
	state := &types.AgentState{CurrentRound: 4}
	engine.resumeEvidenceCollection(t.Context(), state, &messages, guards, "research-session")

	require.Equal(t, 1, guards.evidenceRecoveryAttempts)
	require.Equal(t, 2, guards.incompleteAnswerRetries, "the incomplete-answer retry budget applies to the whole turn")
	require.False(t, guards.structuredAnswerReviewed)
	require.Empty(t, guards.structuredReviewEvidence)
	require.Empty(t, guards.structuredReviewInput)
	require.Zero(t, guards.consecutiveSameContent)
	require.False(t, engine.structuredReviewMode)
	require.Nil(t, engine.taskProjection, "recovery restores all already-authorized tool capabilities")
	require.Equal(t, original, messages[:len(original)])
	require.Equal(t, "continuation", messages[len(original)].ToolCalls[0].ID)
	require.Equal(t, "continuation", messages[len(original)+1].ToolCallID)
	require.Contains(t, messages[len(messages)-1].Content, evidenceRecoveryInstructions)
	serialized, err := json.Marshal(messages)
	require.NoError(t, err)
	require.NotContains(t, string(serialized), review[1].Content, "the isolated review envelope is not reintroduced as conversation facts")
	require.NotContains(t, string(serialized), draft, "the unverified draft is not promoted into the restored transcript")
	projected := engine.projectTaskTools([]types.FunctionDefinition{{Name: "read_document"}, {Name: "unrelated_tool"}})
	require.Len(t, projected, 2)
}

type evidenceContinuationTool struct {
	agenttools.BaseTool
	result *types.ToolResult
	calls  int
}

func TestEvidenceRecoveryKeepsChangedLiveEnvelopeAndSteering(t *testing.T) {
	engine := sourceConsistencyEngine(t, &mockChat{})
	live := []chat.Message{
		{Role: "system", Content: "Compacted authorized research context."},
		{Role: "user", Content: "Continue with the revised scope."},
		{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: "live-read", Function: chat.FunctionCall{Name: "read_document", Arguments: `{}`}}}},
		{Role: "tool", Name: "read_document", ToolCallID: "live-read", Content: `{"content":"Actual newly read evidence."}`},
		{Role: "user", Content: "Exclude the retired site from the comparison."},
	}
	want := append([]chat.Message(nil), live...)
	guards := &loopGuards{structuredReviewInput: "old review envelope", structuredReviewEvidence: []chat.Message{{Role: "user", Content: "Old scope."}}}
	engine.resumeEvidenceCollection(t.Context(), &types.AgentState{}, &live, guards, "revised-scope")
	require.Equal(t, want, live[:len(want)], "recovery must retain live tool pairs and newer human steering")
	require.Len(t, live, len(want)+1)
	require.Empty(t, guards.structuredReviewEvidence)
}

func (tool *evidenceContinuationTool) Execute(context.Context, json.RawMessage) (*types.ToolResult, error) {
	tool.calls++
	return tool.result, nil
}

func documentReadResult(id, chunkID, text string) *types.ToolResult {
	data := map[string]any{
		"document": map[string]any{"knowledge_id": id, "file_name": "field-study-report.txt"},
		"chunks":   []map[string]any{{"knowledge_id": id, "chunk_id": chunkID, "content": text, "page": 2}},
	}
	encoded, _ := json.Marshal(data)
	return &types.ToolResult{Success: true, Output: string(encoded), Data: data}
}

func chatAnswer(content string) mockResponse {
	return mockResponse{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: content, Done: true, FinishReason: "stop"}}}
}

func chatToolCall(id, name, args string) mockResponse {
	return mockResponse{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, ToolCalls: []types.LLMToolCall{{ID: id, Type: "function", Function: types.FunctionCall{Name: name, Arguments: args}}}, Done: true, FinishReason: "tool_calls"}}}
}

func runCollectEvidenceRecoveryCase(t *testing.T, query, draft, finalAnswer, newSourceText string) {
	t.Helper()
	model := &mockChat{responses: []mockResponse{
		chatAnswer(draft),
		sourceConsistencyVerdict(`{"complete":false,"feedback":"The currently read source does not contain the requested detail.","next_action":"collect_evidence"}`),
		chatToolCall("additional-read", "read_document", `{"id":"appendix"}`),
		chatAnswer(finalAnswer),
		sourceAcceptedVerdict(),
	}}
	engine := sourceConsistencyEngine(t, model)
	reader := &evidenceContinuationTool{
		BaseTool: agenttools.NewBaseTool("read_document", "Read an authorized document source.", json.RawMessage(`{"type":"object","additionalProperties":true}`)),
		result:   documentReadResult("appendix-source", "appendix-page", newSourceText),
	}
	engine.toolRegistry = agenttools.NewToolRegistry()
	engine.toolRegistry.RegisterTool(reader)
	originalResult := documentReadResult("initial-source", "initial-page", "The initial excerpt introduces the study but does not state the requested detail.")
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{ID: "initial-read", Name: "read_document", Result: originalResult}}}}}
	messages := []chat.Message{
		{Role: "system", Content: "Answer the current research question using authorized source material."},
		{Role: "user", Content: query},
		{Role: "tool", Name: "read_document", ToolCallID: "initial-read", Content: originalResult.Output},
	}
	_, err := engine.executeLoop(t.Context(), state, query, messages, engine.buildToolsForLLM(), "research-session", "research-message")
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.Equal(t, finalAnswer, state.FinalAnswer)
	require.Equal(t, 1, reader.calls, "the model-selected continuation must execute once")
	require.Len(t, model.calls, 5, "initial draft, collect disposition, retrieval, new draft, acceptance")
	for _, index := range []int{1, 4} {
		require.JSONEq(t, string(sourceConsistencySchema), string(model.opts[index].Format), "review calls use the source-verdict protocol")
		require.Equal(t, "none", model.opts[index].ToolChoice, "review calls cannot execute tools")
	}
	recoveryInput := rawMessageText(model.calls[2])
	require.Contains(t, recoveryInput, evidenceRecoveryInstructions)
	require.Contains(t, recoveryInput, query)
	require.Contains(t, recoveryInput, "initial-source")
	require.Contains(t, recoveryInput, "The initial excerpt introduces the study but does not state the requested detail.")
	require.NotContains(t, recoveryInput, "独立复核上下文")
	require.NotContains(t, recoveryInput, draft, "the discarded draft is not carried forward as a source claim")
	var recheck struct {
		Candidate string               `json:"candidate_answer"`
		Sources   []reviewToolEvidence `json:"source_tool_results"`
	}
	require.NoError(t, json.Unmarshal([]byte(model.calls[4][1].Content), &recheck))
	require.Equal(t, finalAnswer, recheck.Candidate)
	sourceJSON, err := json.Marshal(recheck.Sources)
	require.NoError(t, err)
	require.Contains(t, string(sourceJSON), newSourceText, "the post-retrieval review must see the new actual source result")
	require.NotContains(t, string(sourceJSON), draft, "the previous draft must not enter the source evidence projection")
	require.NotContains(t, string(sourceJSON), "six weeks", "a test-only erroneous candidate is never source evidence")
}

func serializedMessages(messages []chat.Message) string {
	encoded, _ := json.Marshal(messages)
	return string(encoded)
}

func TestCollectEvidenceRecoveryResearchCasesRecheckAfterToolContinuation(t *testing.T) {
	cases := []struct {
		name, query, draft, finalAnswer, evidence string
	}{
		{"watershed observation window", "What observation window does the watershed field report specify?", "The report states a six-week window.", "The report specifies a twelve-week observation window.", "Appendix: the watershed observation window is twelve weeks."},
		{"archive access site", "Which public access site does the archive plan name?", "The plan names the west entrance.", "The plan names the North Reading Room as the public access site.", "Appendix: the public access site is the North Reading Room."},
		{"field sampling interval", "What interval does the field study prescribe between samples?", "Samples are collected every six days.", "The field study prescribes a fourteen-day interval between samples.", "Appendix: collect samples every fourteen days."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runCollectEvidenceRecoveryCase(t, tc.query, tc.draft, tc.finalAnswer, tc.evidence)
		})
	}
}

func TestCollectEvidenceDoesNotRefreshCumulativeRevisionBudgetButRechecksNewSource(t *testing.T) {
	const query = "What interval does the field report prescribe between samples?"
	const newSource = "Appendix: collect samples every twelve days."
	model := &mockChat{responses: []mockResponse{
		chatAnswer("The initial excerpt does not establish the requested interval."),
		sourceConsistencyVerdict(`{"complete":false,"feedback":"Remove the unsupported interval value.","next_action":"revise_answer"}`),
		chatAnswer("The report specifies an interval, without stating its duration."),
		sourceConsistencyVerdict(`{"complete":false,"feedback":"The candidate adds an unneeded comparison.","next_action":"revise_answer"}`),
		chatAnswer("The available excerpt does not state the interval duration."),
		sourceConsistencyVerdict(`{"complete":false,"feedback":"The duration is absent from the read excerpt.","next_action":"collect_evidence"}`),
		chatToolCall("appendix-read", "read_document", `{"id":"appendix"}`),
		chatAnswer("The appendix says to collect samples every twelve days."),
		sourceConsistencyVerdict(`{"complete":false,"feedback":"Remove the unnecessary aside; the interval is supported.","next_action":"revise_answer"}`),
	}}
	engine := sourceConsistencyEngine(t, model)
	reader := &evidenceContinuationTool{
		BaseTool: agenttools.NewBaseTool("read_document", "Read an authorized report.", json.RawMessage(`{"type":"object","additionalProperties":true}`)),
		result:   documentReadResult("appendix-source", "appendix-page", newSource),
	}
	engine.toolRegistry = agenttools.NewToolRegistry()
	engine.toolRegistry.RegisterTool(reader)
	original := documentReadResult("initial-source", "initial-page", "The initial excerpt introduces the field study but omits the interval.")
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{ID: "initial-read", Name: "read_document", Result: original}}}}}
	messages := []chat.Message{
		{Role: "system", Content: "Use the authorized field report."},
		{Role: "user", Content: query},
		{Role: "tool", Name: "read_document", ToolCallID: "initial-read", Content: original.Output},
	}
	_, err := engine.executeLoop(t.Context(), state, query, messages, engine.buildToolsForLLM(), "research-session", "research-message")
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.Equal(t, incompleteAnswerFallback, state.FinalAnswer, "retrieval remains available after the shared semantic revision budget is spent, but cannot grant another rewrite")
	require.Equal(t, 1, reader.calls, "the model-selected new evidence must be read once")
	require.Len(t, model.calls, 9, "candidate, revise, candidate, revise, candidate, collect, tool continuation, new candidate, exhausted revise")
	for _, index := range []int{1, 3, 5, 8} {
		require.JSONEq(t, string(sourceConsistencySchema), string(model.opts[index].Format), "every source review uses the typed verdict protocol")
		require.Equal(t, "none", model.opts[index].ToolChoice)
	}
	require.Contains(t, model.responses[1].chunks[0].Content, `"next_action":"revise_answer"`)
	require.Contains(t, model.responses[3].chunks[0].Content, `"next_action":"revise_answer"`)
	require.Contains(t, model.responses[5].chunks[0].Content, `"next_action":"collect_evidence"`)
	require.Contains(t, model.responses[8].chunks[0].Content, `"next_action":"revise_answer"`, "the post-retrieval semantic correction is correctly requested but budget-exhausted")
	var finalReview struct {
		Candidate string               `json:"candidate_answer"`
		Sources   []reviewToolEvidence `json:"source_tool_results"`
	}
	require.NoError(t, json.Unmarshal([]byte(model.calls[8][1].Content), &finalReview))
	require.Equal(t, "The appendix says to collect samples every twelve days.", finalReview.Candidate)
	sourceJSON, err := json.Marshal(finalReview.Sources)
	require.NoError(t, err)
	require.Contains(t, string(sourceJSON), newSource, "the final acceptance must verify the evidence obtained in the new collection phase")
}

func TestCollectEvidenceRecoveryStopsAfterTwoAttempts(t *testing.T) {
	const query = "What observation window does the watershed field report specify?"
	const draft = "The report states a six-week window."
	responses := []mockResponse{
		chatAnswer(draft),
		sourceConsistencyVerdict(`{"complete":false,"feedback":"The current source excerpt omits the observation window.","next_action":"collect_evidence"}`),
		chatAnswer("The report still does not identify the observation window."),
		sourceConsistencyVerdict(`{"complete":false,"feedback":"The needed observation window remains absent.","next_action":"collect_evidence"}`),
		chatAnswer("The report does not provide an observation window in the available excerpts."),
		sourceConsistencyVerdict(`{"complete":false,"feedback":"The needed observation window remains absent.","next_action":"collect_evidence"}`),
		chatAnswer("The available excerpts do not state an observation window."),
		sourceAcceptedVerdict(),
	}
	model := &mockChat{responses: responses}
	engine := sourceConsistencyEngine(t, model)
	reader := &evidenceContinuationTool{BaseTool: agenttools.NewBaseTool("read_document", "Read an authorized report.", json.RawMessage(`{"type":"object"}`)), result: documentReadResult("unused", "unused", "unused")}
	engine.toolRegistry = agenttools.NewToolRegistry()
	engine.toolRegistry.RegisterTool(reader)
	original := documentReadResult("initial-source", "initial-page", "The initial excerpt introduces the study but omits its window.")
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{ID: "initial-read", Name: "read_document", Result: original}}}}}
	messages := []chat.Message{{Role: "system", Content: "Use the authorized report."}, {Role: "user", Content: query}, {Role: "tool", Name: "read_document", ToolCallID: "initial-read", Content: original.Output}}
	_, err := engine.executeLoop(t.Context(), state, query, messages, engine.buildToolsForLLM(), "research-session", "research-message")
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.Contains(t, state.FinalAnswer, "do not state an observation window")
	require.Len(t, model.calls, 8, "three collection dispositions are checked, only two resume retrieval, then a bounded answer rewrite is reviewed")
	for _, index := range []int{1, 3, 5, 7} {
		require.JSONEq(t, string(sourceConsistencySchema), string(model.opts[index].Format))
		require.Equal(t, "none", model.opts[index].ToolChoice)
	}
	maxRecoveryPrompts := 0
	for _, call := range model.calls {
		count := 0
		for _, message := range call {
			count += strings.Count(message.Content, evidenceRecoveryInstructions)
		}
		if count > maxRecoveryPrompts {
			maxRecoveryPrompts = count
		}
	}
	require.Equal(t, maxEvidenceRecoveryAttempts, maxRecoveryPrompts)
	require.Zero(t, reader.calls, "third collect_evidence disposition must not start another recovery/tool round")
	for _, response := range model.responses {
		for _, chunk := range response.chunks {
			require.Empty(t, chunk.ToolCalls, "the scripted exhausted path contains no tool continuation")
		}
	}
}

type cancelAtReviewCallChat struct {
	mockChat
	cancel   context.CancelFunc
	cancelAt int
}

func (model *cancelAtReviewCallChat) ChatStream(ctx context.Context, messages []chat.Message, opts *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	stream, err := model.mockChat.ChatStream(ctx, messages, opts)
	if model.callCount == model.cancelAt {
		model.cancel()
	}
	return stream, err
}

func TestCollectEvidenceDispositionCancelledBeforeRecoveryDoesNotCallTools(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	model := &cancelAtReviewCallChat{mockChat: mockChat{responses: []mockResponse{
		chatAnswer("The report states a six-week window."),
		chatAnswer(`{"answer":"The report states a six-week window."}`),
		chatAnswer(`{"complete":false,"feedback":"The source lacks the requested interval.","next_action":"collect_evidence"}`),
	}}, cancel: cancel, cancelAt: 3}
	engine := newTestEngine(t, model)
	engine.config.WorkbenchDefault = true
	engine.config.TaskPlanningEnabled = true
	reader := &evidenceContinuationTool{BaseTool: agenttools.NewBaseTool("read_document", "Read an authorized report.", json.RawMessage(`{"type":"object"}`)), result: documentReadResult("unused", "unused", "unused")}
	engine.toolRegistry = agenttools.NewToolRegistry()
	engine.toolRegistry.RegisterTool(reader)
	original := documentReadResult("initial-source", "initial-page", "The initial excerpt does not identify the observation window.")
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{ID: "initial-read", Name: "read_document", Result: original}}}}}
	query := "What observation window does the field report specify?"
	messages := []chat.Message{{Role: "system", Content: "Use the authorized report."}, {Role: "user", Content: query}, {Role: "tool", Name: "read_document", ToolCallID: "initial-read", Content: original.Output}}
	_, err := engine.executeLoop(ctx, state, query, messages, engine.buildToolsForLLM(), "research-session", "research-message")
	if err != nil {
		require.ErrorIs(t, err, context.Canceled)
	}
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.False(t, state.IsComplete)
	require.Empty(t, state.FinalAnswer)
	require.Zero(t, reader.calls)
	require.Len(t, model.calls, 3, "cancellation after the disposition must prevent a recovery round")
	for _, call := range model.calls {
		for _, message := range call {
			require.NotContains(t, message.Content, evidenceRecoveryInstructions)
		}
	}
}

func numericRecoveryFixture() (*types.AgentState, []chat.Message, string) {
	query := "Summarize the measured value and include only supported statistics."
	data := map[string]any{"kind": "structured_result_evidence", "status": "ok", "values": []any{map[string]any{"value": 138.67}}}
	encoded, _ := json.Marshal(data)
	result := &types.ToolResult{Success: true, Output: string(encoded), Data: data}
	call := types.ToolCall{ID: "initial-query", Name: "structured_query_series", Result: result}
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
	messages := []chat.Message{
		{Role: "system", Content: "Use authorized source results and complete the current request."},
		{Role: "user", Content: query},
		{Role: "tool", Name: "structured_query_series", ToolCallID: "initial-query", Content: string(encoded)},
	}
	return state, messages, query
}

func numericRecoveryTestEngine(t *testing.T, model *mockChat) *AgentEngine {
	t.Helper()
	engine := sourceConsistencyEngine(t, model)
	engine.toolRegistry = agenttools.NewToolRegistry()
	return engine
}

func TestNumericValidationRepairCanRemoveOptionalUnsupportedNumberAndPassFeedbackToCritic(t *testing.T) {
	const candidate = "The measured value is 138.67; an optional approximation is 138."
	const corrected = "The measured value is 138.67."
	model := &mockChat{responses: []mockResponse{
		chatAnswer(candidate),
		chatAnswer(`{"answer":"` + candidate + `"}`),
		chatAnswer(corrected),
		sourceAcceptedVerdict(),
	}}
	engine := numericRecoveryTestEngine(t, model)
	state, messages, query := numericRecoveryFixture()
	_, err := engine.executeLoop(t.Context(), state, query, messages, engine.buildToolsForLLM(), "numeric-session", "numeric-message")
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.Equal(t, corrected, state.FinalAnswer)
	require.Len(t, model.calls, 4)

	repairPrompt := rawMessageText(model.calls[2])
	require.Contains(t, repairPrompt, query, "the exact human request is restored")
	unescapedRepairPrompt := html.UnescapeString(repairPrompt)
	require.Contains(t, unescapedRepairPrompt, `<untrusted_tool_result name="structured_query_series">`, "the actual result remains in its protected tool wrapper")
	require.Contains(t, unescapedRepairPrompt, `"kind":"structured_result_evidence"`)
	require.Contains(t, unescapedRepairPrompt, `"value":138.67`)
	require.Contains(t, repairPrompt, numericValidationRepairInstructions)
	require.Contains(t, repairPrompt, `"token":"138"`)
	require.Contains(t, repairPrompt, `"check_status":"unsupported_tokens"`)
	require.NotContains(t, repairPrompt, "独立复核上下文", "the synthetic reviewer envelope is not carried into generation")

	criticPrompt := rawMessageText(model.calls[3])
	require.Contains(t, criticPrompt, query)
	require.Contains(t, criticPrompt, `"check_status":"no_unmatched_numbers"`, "the critic receives typed validator status")
	require.NotContains(t, criticPrompt, numericValidationRepairInstructions, "runtime instructions are not represented as source facts")
}

func TestNumericValidationRepairKeepsAuthorizedToolsAndCanCallOne(t *testing.T) {
	const candidate = "The measured value is 138.67; an optional approximation is 138."
	const supported = `{"kind":"structured_result_evidence","status":"ok","values":[{"value":138}]}`
	const corrected = "The measured value is 138.67; the verified calculation is 138."
	model := &mockChat{responses: []mockResponse{
		chatAnswer(candidate),
		chatAnswer(`{"answer":"` + candidate + `"}`),
		chatToolCall("numeric-recheck", "structured_query_series", `{}`),
		chatAnswer(corrected),
		sourceAcceptedVerdict(),
	}}
	engine := numericRecoveryTestEngine(t, model)
	tool := &evidenceContinuationTool{
		BaseTool: agenttools.NewBaseTool("structured_query_series", "Execute an authorized structured query.", json.RawMessage(`{"type":"object","additionalProperties":true}`)),
		result:   &types.ToolResult{Success: true, Output: supported},
	}
	engine.toolRegistry.RegisterTool(tool)
	state, messages, query := numericRecoveryFixture()
	_, err := engine.executeLoop(t.Context(), state, query, messages, engine.buildToolsForLLM(), "numeric-session", "numeric-message")
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.Equal(t, corrected, state.FinalAnswer)
	require.Equal(t, 1, tool.calls)
	require.Len(t, model.calls, 5)
	toolNames := listToolNames(model.opts[2].Tools)
	require.Contains(t, toolNames, "structured_query_series", "numeric repair resumes with the authorized tool catalog")
	require.NotEqual(t, "none", model.opts[2].ToolChoice)
	require.Contains(t, rawMessageText(model.calls[2]), numericValidationRepairInstructions)
	require.Contains(t, rawMessageText(model.calls[2]), query)
}

func TestNumericValidationRepairStopsAfterTwoAttemptsAndUsesFallback(t *testing.T) {
	const unsupported = "The measured value is 138.67; an optional approximation is 138."
	model := &mockChat{responses: []mockResponse{
		chatAnswer(unsupported),
		chatAnswer(`{"answer":"` + unsupported + `"}`),
		chatAnswer(unsupported),
	}}
	engine := numericRecoveryTestEngine(t, model)
	state, messages, query := numericRecoveryFixture()
	_, err := engine.executeLoop(t.Context(), state, query, messages, engine.buildToolsForLLM(), "numeric-session", "numeric-message")
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.Equal(t, "已经取得原表依据，但完整回答的部分数字未通过来源核验，暂不提供未经核验的统计结论。请查看取数依据。", state.FinalAnswer)
	require.Len(t, model.calls, 3, "the initial candidate and two numeric repair attempts exhaust the shared repair budget; no critic accepts the failing candidate")
	require.Equal(t, 2, strings.Count(rawMessageText(model.calls[1]), numericValidationRepairInstructions)+strings.Count(rawMessageText(model.calls[2]), numericValidationRepairInstructions))
	require.NotContains(t, state.FinalAnswer, "138")
}

func rawMessageText(messages []chat.Message) string {
	var text strings.Builder
	for _, message := range messages {
		text.WriteString(message.Content)
		text.WriteByte('\n')
	}
	return text.String()
}

func TestIndependentReviewUsesDeliveredHistoryButNotCurrentDraftPlans(t *testing.T) {
	messages := []chat.Message{
		{Role: "system", Content: "Use actual evidence."},
		{Role: "user", Content: "Explain two candidate sampling methods."},
		{Role: "assistant", Content: "Method A is stratified; method B is systematic sampling."},
		{Role: "user", Content: "Apply the second method to the source."},
		{Role: "assistant", Content: "I will silently omit the final sampling interval."},
	}
	result := independentStructuredReview(messages, "Apply the second method to the source.", "Unverified candidate.")
	require.Contains(t, result[1].Content, "method B is systematic sampling")
	require.NotContains(t, result[1].Content, "silently omit")
	require.Contains(t, result[1].Content, "not source evidence")
}
