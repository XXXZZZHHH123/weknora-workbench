package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func candidateReviewInput(t *testing.T, model *mockChat, callIndex int, field string) map[string]any {
	t.Helper()
	require.Greater(t, callIndex, -1)
	require.Less(t, callIndex, len(model.calls))
	for _, message := range model.calls[callIndex] {
		if message.Role != "user" {
			continue
		}
		var value map[string]any
		if json.Unmarshal([]byte(message.Content), &value) == nil {
			if _, ok := value[field]; ok {
				return value
			}
		}
	}
	t.Fatalf("call %d has no user JSON input containing %q", callIndex, field)
	return nil
}

func callWithFormat(model *mockChat, format json.RawMessage) int {
	return callWithFormatFrom(model, format, 0)
}

func callWithFormatFrom(model *mockChat, format json.RawMessage, start int) int {
	for i := start; i < len(model.opts); i++ {
		opts := model.opts[i]
		if opts != nil && string(opts.Format) == string(format) {
			return i
		}
	}
	return -1
}

func TestDirectCandidateReviewAcceptsExactAnswerWithoutRewrite(t *testing.T) {
	const query = "What indicator does the field report describe?"
	const candidate = "The report describes covered area as its indicator."
	model := &mockChat{responses: []mockResponse{
		chatAnswer(candidate),
		sourceAcceptedVerdict(),
	}}
	engine := sourceConsistencyEngine(t, model)
	state := sourceConsistencyState()
	messages := []chat.Message{
		{Role: "system", Content: "Use the current turn's authorized source evidence."},
		{Role: "user", Content: query},
	}
	recorder := &terminalAnswerRecorder{}
	recorder.attach(engine.eventBus)

	_, err := engine.executeLoop(t.Context(), state, query, messages, emptyTools(), "candidate-session", "candidate-message")
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.Equal(t, candidate, state.FinalAnswer, "acceptance must deliver the exact candidate without an answer rewrite")
	require.Len(t, model.calls, 2, "the only calls are the answer generator and its source critic")
	criticCall := callWithFormat(model, sourceConsistencySchema)
	require.Greater(t, criticCall, 0, "the source critic follows the original generator directly")
	review := candidateReviewInput(t, model, criticCall, "candidate_answer")
	require.Equal(t, candidate, review["candidate_answer"])
	sources, err := json.Marshal(review["source_tool_results"])
	require.NoError(t, err)
	require.Contains(t, string(sources), "统计对象为覆盖面积，单位为公顷。", "the critic must use actual successful source evidence")
	require.NotContains(t, string(sources), candidate, "the candidate is not promoted into source evidence")
	done, streamed := recorder.snapshot()
	require.Equal(t, 1, done, "the reviewed answer closes exactly one final-answer stream")
	require.Equal(t, candidate, streamed, "the accepted candidate is streamed once, unchanged")
}

func TestUnsupportedNumericCandidateIsRepairedBeforeSourceCritic(t *testing.T) {
	const query = "What value does the executed table report?"
	const unsupported = "The value is 999."
	const corrected = "The value is 30."
	structuredResult := `{"kind":"structured_analysis","status":"ok","rows":[[30]]}`
	model := &mockChat{responses: []mockResponse{
		chatAnswer(unsupported),
		chatAnswer(corrected),
		sourceAcceptedVerdict(),
	}}
	engine := sourceConsistencyEngine(t, model)
	state := sourceConsistencyState()
	state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{{
		ID: "actual-table-query", Name: "structured_data",
		Result: &types.ToolResult{Success: true, Output: structuredResult},
	}}})
	messages := []chat.Message{
		{Role: "system", Content: "Use actual executed results."},
		{Role: "user", Content: query},
		{Role: "tool", Name: "structured_data", ToolCallID: "actual-table-query", Content: structuredResult},
	}

	_, err := engine.executeLoop(t.Context(), state, query, messages, emptyTools(), "numeric-session", "numeric-message")
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.Equal(t, corrected, state.FinalAnswer)
	require.Len(t, model.calls, 3, "unsupported numeric output is corrected before its source critic runs")
	criticCall := callWithFormat(model, sourceConsistencySchema)
	require.Equal(t, 2, criticCall, "both generation calls precede the first source critic")
	require.Equal(t, -1, callWithFormat(model, reviewedAnswerSchema), "numeric recovery resumes the authorized tool loop rather than forcing an answer rewrite")
	var numericFeedbackSeen, rejectedCandidateSeen bool
	for _, message := range model.calls[1] {
		if message.Role == "assistant" && message.Content == unsupported {
			rejectedCandidateSeen = true
		}
		if message.Role == "system" && strings.Contains(message.Content, numericValidationRepairInstructions) {
			numericFeedbackSeen = true
		}
	}
	require.True(t, rejectedCandidateSeen, "the generator can revise its untrusted candidate")
	require.True(t, numericFeedbackSeen, "the generator receives runtime numeric feedback")
	require.Equal(t, corrected, candidateReviewInput(t, model, criticCall, "candidate_answer")["candidate_answer"])
	review := candidateReviewInput(t, model, criticCall, "candidate_answer")
	sources, err := json.Marshal(review["source_tool_results"])
	require.NoError(t, err)
	require.Contains(t, string(sources), "30")
	require.NotContains(t, string(sources), "999", "the rejected draft must never enter source evidence")
}

func TestRejectedCandidateIsRevisedThenCriticRechecksExactRevision(t *testing.T) {
	const query = "What indicator does the field report describe?"
	const draft = "The report describes production as its indicator."
	const revised = "The report describes covered area as its indicator."
	model := &mockChat{responses: []mockResponse{
		chatAnswer(draft),
		sourceConsistencyVerdict(`{"complete":false,"feedback":"The source supports covered area, not production.","next_action":"revise_answer"}`),
		chatAnswer(`{"answer":"` + revised + `"}`),
		sourceAcceptedVerdict(),
	}}
	engine := sourceConsistencyEngine(t, model)
	state := sourceConsistencyState()
	messages := []chat.Message{{Role: "system", Content: "Use current actual evidence."}, {Role: "user", Content: query}}

	_, err := engine.executeLoop(t.Context(), state, query, messages, emptyTools(), "revision-session", "revision-message")
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.Equal(t, revised, state.FinalAnswer)
	require.Len(t, model.calls, 4, "a valid rejection triggers one isolated revision and a second source check")
	firstCriticCall := callWithFormat(model, sourceConsistencySchema)
	criticCall := callWithFormatFrom(model, sourceConsistencySchema, firstCriticCall+1)
	require.Greater(t, criticCall, firstCriticCall)
	revisionCall := callWithFormat(model, reviewedAnswerSchema)
	require.Greater(t, revisionCall, firstCriticCall)
	require.Less(t, revisionCall, criticCall)
	firstVerdict := candidateReviewInput(t, model, firstCriticCall, "candidate_answer")
	finalVerdict := candidateReviewInput(t, model, criticCall, "candidate_answer")
	require.Equal(t, draft, firstVerdict["candidate_answer"])
	require.Equal(t, revised, finalVerdict["candidate_answer"])
	serializedSources, err := json.Marshal(finalVerdict["source_tool_results"])
	require.NoError(t, err)
	require.Contains(t, string(serializedSources), "统计对象为覆盖面积，单位为公顷。")
	require.NotContains(t, string(serializedSources), draft, "the rejected draft is not promoted into evidence")
}

func TestExhaustedSourceCriticProtocolDoesNotLeakCandidate(t *testing.T) {
	const candidate = "Unverified draft that must not be delivered."
	responses := []mockResponse{chatAnswer(candidate)}
	for range answerReviewAttempts {
		responses = append(responses, sourceConsistencyVerdict(`{"complete":true,"feedback":"conflicting fields"}`))
	}
	model := &mockChat{responses: responses}
	engine := sourceConsistencyEngine(t, model)
	state := sourceConsistencyState()
	recorder := &terminalAnswerRecorder{}
	recorder.attach(engine.eventBus)
	query := "What indicator does the field report describe?"

	_, err := engine.executeLoop(t.Context(), state, query, []chat.Message{{Role: "user", Content: query}}, emptyTools(), "exhaustion-session", "exhaustion-message")
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	// Delivery policy (2026-10-20): an unavailable reviewer no longer hides a
	// numerically grounded candidate; it is delivered with an explicit notice.
	require.True(t, strings.HasPrefix(state.FinalAnswer, candidate))
	require.Contains(t, state.FinalAnswer, "未能完成自动核验")
	require.Len(t, model.calls, 1+answerReviewAttempts)
	done, visible := recorder.snapshot()
	require.Equal(t, 1, done)
	require.Equal(t, state.FinalAnswer, visible)
}

func TestCustomModeRetainsLegacyIndependentSourceAudit(t *testing.T) {
	const query = "What indicator does the field report describe?"
	const draft = "The report describes output as its indicator."
	const revised = "The report describes covered area as its indicator."
	model := &mockChat{responses: []mockResponse{
		chatAnswer(draft),
		chatAnswer(`{"answer":"` + revised + `"}`),
	}}
	engine := newTestEngine(t, model)
	engine.config.WorkbenchDefault = false
	engine.config.TaskPlanningEnabled = true
	state := sourceConsistencyState()
	messages := []chat.Message{
		{Role: "system", Content: "Use the current turn's authorized source evidence."},
		{Role: "user", Content: query},
	}

	_, err := engine.executeLoop(t.Context(), state, query, messages, emptyTools(), "legacy-session", "legacy-message")
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.Equal(t, revised, state.FinalAnswer, "custom mode must retain its independent source-audit rewrite")
	require.Len(t, model.calls, 2, "the audit rewrite follows the original answer")
	revisionCall := callWithFormat(model, reviewedAnswerSchema)
	require.Greater(t, revisionCall, 0)
	revisionInput := candidateReviewInput(t, model, revisionCall, "unverified_draft")
	require.Equal(t, draft, revisionInput["unverified_draft"], "the old answer is explicitly an unverified draft for the independent audit")
	serializedCall, err := json.Marshal(model.calls[revisionCall])
	require.NoError(t, err)
	require.Contains(t, string(serializedCall), "统计对象为覆盖面积，单位为公顷。", "the draft is checked against actual fetched source text")
}
