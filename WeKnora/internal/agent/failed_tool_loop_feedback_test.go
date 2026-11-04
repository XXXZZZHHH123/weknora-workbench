package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func feedbackFailure(tool string, attempt int, message string) types.ToolCall {
	return types.ToolCall{Name: tool, Args: map[string]any{"quote": fmt.Sprintf("changed wording %d", attempt)}, Result: &types.ToolResult{Success: false, Error: message}}
}

func feedbackOperation(tool, operation string, attempt int, success bool) types.ToolCall {
	call := feedbackFailure(tool, attempt, "current source identity is not eligible")
	call.Args["operation"] = operation
	call.Result.Success = success
	call.Result.Data = map[string]any{"kind": "fixture_source_validation", "reason_code": "source_not_current"}
	return call
}

func TestFailedToolFeedbackSurvivesChangedArgumentsAndUnrelatedReads(t *testing.T) {
	guards := &loopGuards{}
	for attempt := 1; attempt <= 5; attempt++ {
		name, count, nudge := guards.recordFailedToolCall(feedbackFailure("source_bound_fixture", attempt, "exact source evidence did not match"))
		if attempt == failedToolFeedbackAttempts {
			require.True(t, nudge)
			require.Equal(t, "source_bound_fixture", name)
			require.Equal(t, 2, count)
			guidance := guards.failedToolLoopGuidance(name, count)
			require.Contains(t, guidance, "same failure feedback")
			require.Contains(t, guidance, "merely rewording")
			require.Contains(t, guidance, "full user request actually requires")
			require.Contains(t, guidance, "exact evidence")
			require.Contains(t, guidance, "does not mean the task is complete")
		} else {
			require.False(t, nudge, "one diagnostic notice per bounded sequence, never on a single failure")
		}
		_, _, nudge = guards.recordFailedToolCall(types.ToolCall{Name: "read_fixture", Result: &types.ToolResult{Success: true}})
		require.False(t, nudge)
	}
}

func TestFailedToolFeedbackUsesResolvedToolAndActualDiagnosticIdentity(t *testing.T) {
	loop := &failedToolFeedbackLoop{}
	for attempt := 1; attempt <= 3; attempt++ {
		call := feedbackFailure(fmt.Sprintf("model_alias_%d", attempt), attempt, fmt.Sprintf("binding failed for variable_%d", attempt))
		call.Target = &types.ToolCallTarget{Name: "actual_source_validator"}
		call.Result.Data = map[string]any{"kind": "fixture_validation", "reason_code": "binding_mismatch"}
		notice := loop.observe(call)
		if attempt == failedToolFeedbackAttempts {
			require.NotNil(t, notice, "real machine diagnostics survive parameter names in the prose feedback")
			require.Equal(t, "actual_source_validator", notice.name)
		} else {
			require.Nil(t, notice)
		}
	}
	// A genuinely different failure code is not inferred to be the same error.
	call := feedbackFailure("actual_source_validator", 4, "binding failed")
	call.Result.Data = map[string]any{"kind": "fixture_validation", "reason_code": "source_unavailable"}
	require.Nil(t, loop.observe(call))
	call.Target = &types.ToolCallTarget{Name: "different_actual_tool"}
	call.Result.Data["reason_code"] = "binding_mismatch"
	require.Nil(t, loop.observe(call), "one tool's failure must not consume another tool's recovery budget")
}

func TestFailedToolFeedbackPreservesErrorNumbersAndResetsAfterRealSuccess(t *testing.T) {
	loop := &failedToolFeedbackLoop{}
	for attempt := 1; attempt <= 3; attempt++ {
		require.Nil(t, loop.observe(feedbackFailure("fixture", attempt, fmt.Sprintf("unsupported source field %d", attempt))), "distinct numeric diagnostics remain distinct")
	}
	require.Nil(t, loop.observe(feedbackFailure("fixture", 1, "source \n validation failed")))
	require.NotNil(t, loop.observe(feedbackFailure("fixture", 2, "source validation failed")), "only whitespace is normalized for prose feedback")
	require.Nil(t, loop.observe(types.ToolCall{Name: "fixture", Result: &types.ToolResult{Success: true}}))
	require.Nil(t, loop.observe(feedbackFailure("fixture", 1, "source validation failed")), "successful recovery starts a fresh sequence")
	require.NotNil(t, loop.observe(feedbackFailure("fixture", 2, "source validation failed")))
}

func TestFailedToolFeedbackIsBoundedAndDoesNotReuseStaleOrMissingDiagnostics(t *testing.T) {
	loop := &failedToolFeedbackLoop{}
	require.Nil(t, loop.observe(feedbackFailure("fixture", 1, "source evidence did not match")))
	for attempt := 0; attempt < failedToolFeedbackWindow; attempt++ {
		require.Nil(t, loop.observe(types.ToolCall{Name: "reader", Result: &types.ToolResult{Success: true}}))
	}
	require.Nil(t, loop.observe(feedbackFailure("fixture", 3, "source evidence did not match")), "old failures expire across a bounded tool-call window")
	for attempt := 0; attempt < maxFailedToolFeedbackEntries*3; attempt++ {
		require.Nil(t, loop.observe(feedbackFailure("fixture", attempt, fmt.Sprintf("distinct feedback %d", attempt))))
		require.LessOrEqual(t, len(loop.entries), maxFailedToolFeedbackEntries)
	}
	for attempt := 0; attempt < 5; attempt++ {
		require.Nil(t, loop.observe(types.ToolCall{Name: "fixture", Result: &types.ToolResult{Success: false}}))
		require.Nil(t, loop.observe(types.ToolCall{Name: "fixture"}))
	}
}

func TestFailedToolFeedbackInspectSuccessDoesNotResetMapFailure(t *testing.T) {
	guards := &loopGuards{}
	_, count, nudge := guards.recordFailedToolCall(feedbackOperation(agenttools.ToolRenderChart, "map", 1, false))
	require.Equal(t, 1, count)
	require.False(t, nudge, "one failed call leaves the normal recovery path available")
	_, _, nudge = guards.recordFailedToolCall(feedbackOperation(agenttools.ToolRenderChart, "inspect", 1, true))
	require.False(t, nudge, "a successful catalog read is not a failure")
	name, count, nudge := guards.recordFailedToolCall(feedbackOperation(agenttools.ToolRenderChart, "map", 2, false))
	require.True(t, nudge, "inspecting between two failed maps must not erase the repeated actual feedback")
	require.Equal(t, 2, count)
	require.Equal(t, agenttools.ToolRenderChart, name)
	guidance := guards.failedToolLoopGuidance(name, count)
	require.Contains(t, guidance, `operation "map"`)
	require.Contains(t, guidance, "mapping_supported=true")
	require.Contains(t, guidance, "current matrix_shape")
	require.Contains(t, guidance, "copy exactly a source_id")
	require.Contains(t, guidance, "does not mean the task is complete")
	_, _, nudge = guards.recordFailedToolCall(feedbackOperation(agenttools.ToolRenderChart, "map", 3, true))
	require.False(t, nudge)
	_, count, nudge = guards.recordFailedToolCall(feedbackOperation(agenttools.ToolRenderChart, "map", 4, false))
	require.Equal(t, 1, count)
	require.False(t, nudge, "actual map success resets that operation for a later legitimate retry")
}

func TestFailedToolFeedbackSeparatesOperationsAndKeepsSuccessAuthoritative(t *testing.T) {
	loop := &failedToolFeedbackLoop{}
	require.Nil(t, loop.observe(feedbackOperation("operation_fixture", "map", 1, false)))
	require.Nil(t, loop.observe(feedbackOperation("operation_fixture", "render", 1, false)))
	inspection := feedbackOperation("operation_fixture", "inspect", 1, true)
	inspection.Result.Data["status"] = "needs_source"
	require.Nil(t, loop.observe(inspection), "successful diagnostic results must not be reclassified as failures")
	mapNotice := loop.observe(feedbackOperation("operation_fixture", "map", 2, false))
	require.NotNil(t, mapNotice)
	require.Equal(t, "map", mapNotice.operation)
	require.Equal(t, 2, mapNotice.attempts)
	renderNotice := loop.observe(feedbackOperation("operation_fixture", "render", 2, false))
	require.NotNil(t, renderNotice)
	require.Equal(t, "render", renderNotice.operation)
	require.Equal(t, 2, renderNotice.attempts, "a map failure must not consume the render diagnostic sequence")
	require.Nil(t, loop.observe(feedbackOperation("operation_fixture", "map", 3, true)))
	require.Nil(t, loop.observe(feedbackOperation("operation_fixture", "map", 4, false)))
	require.Nil(t, loop.observe(feedbackOperation("operation_fixture", "render", 3, false)), "one notice per operation's bounded sequence")
	require.Nil(t, loop.observe(feedbackOperation("operation_fixture", "render", 4, true)))
	require.Nil(t, loop.observe(feedbackOperation("operation_fixture", "render", 5, false)))
	require.NotNil(t, loop.observe(feedbackOperation("operation_fixture", "render", 6, false)))
}

func TestFailedToolFeedbackUnifiesStructuredWrapperAndNativeOperation(t *testing.T) {
	loop := &failedToolFeedbackLoop{}
	wrapper := feedbackOperation(agenttools.ToolStructuredData, "query_pdf_table", 1, false)
	wrapper.Args["arguments"] = map[string]any{"sql": "fixture query"}
	require.Nil(t, loop.observe(wrapper))
	native := feedbackOperation("structured_query_pdf_table", "ignored_native_argument", 2, false)
	notice := loop.observe(native)
	require.NotNil(t, notice, "switching the model-facing wrapper does not fix the same actual service operation")
	require.Equal(t, "structured_query_pdf_table", notice.name, "advice must still name the tool the model actually called")
	require.Equal(t, "query_pdf_table", notice.operation)
	require.Nil(t, loop.observe(feedbackOperation(agenttools.ToolStructuredData, "describe_tools", 3, true)))
	require.Len(t, loop.entries, 1, "schema discovery cannot erase a query failure")
	require.Nil(t, loop.observe(feedbackOperation("structured_query_pdf_table", "", 4, true)))
	require.Empty(t, loop.entries, "actual native query success also resolves the wrapper's diagnostic sequence")
	require.Nil(t, loop.observe(wrapper))
	require.Nil(t, loop.observe(feedbackOperation("structured_read_pdf_table", "", 5, true)))
	require.NotNil(t, loop.observe(native), "a successful read is not proof the failed query was fixed")
}

func TestFailedToolFeedbackDistinguishesStructuredServiceOperationFromAggregation(t *testing.T) {
	loop := &failedToolFeedbackLoop{}
	wrapper := feedbackOperation(agenttools.ToolStructuredData, "query_series", 1, false)
	wrapper.Args["arguments"] = map[string]any{"operation": "sum"}
	require.Nil(t, loop.observe(wrapper))
	// The named tool binds query_series as the service operation; this
	// operation argument is only the aggregation inside that service call.
	native := feedbackOperation("structured_query_series", "mean", 2, false)
	notice := loop.observe(native)
	require.NotNil(t, notice)
	require.Equal(t, "query_series", notice.operation)
	require.Equal(t, 2, notice.attempts)
	require.Nil(t, loop.observe(feedbackOperation(agenttools.ToolStructuredData, "read_pdf_table", 3, false)), "another service operation remains independent")
}

func TestFailedToolFeedbackUsesResolvedOperationArguments(t *testing.T) {
	loop := &failedToolFeedbackLoop{}
	proxy := feedbackOperation("model_proxy_fixture", "inspect", 1, false)
	proxy.Target = &types.ToolCallTarget{Name: agenttools.ToolRenderChart, Args: map[string]any{"operation": "map"}}
	require.Nil(t, loop.observe(proxy))
	notice := loop.observe(feedbackOperation(agenttools.ToolRenderChart, "map", 2, false))
	require.NotNil(t, notice)
	require.Equal(t, "map", notice.operation, "the replay-facing proxy args must not replace the resolved operation")
	proxy.Result.Success = true
	proxy.Target.Args["operation"] = "inspect"
	require.Nil(t, loop.observe(proxy))
	require.Len(t, loop.entries, 1, "resolved inspect success cannot clear a resolved map failure")
	proxy.Target.Args["operation"] = "map"
	require.Nil(t, loop.observe(proxy))
	require.Empty(t, loop.entries)
}

func TestExecuteLoopRepeatedFeedbackAdvisesRecoveryWithoutDisablingTools(t *testing.T) {
	toolResponse := func(name string, attempt int) mockResponse {
		arguments, err := json.Marshal(map[string]string{"quote": fmt.Sprintf("changed wording %d", attempt)})
		require.NoError(t, err)
		return mockResponse{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer,
			ToolCalls: []types.LLMToolCall{{ID: fmt.Sprintf("%s-%d", name, attempt), Type: "function", Function: types.FunctionCall{Name: name, Arguments: string(arguments)}}},
			Done:      true, FinishReason: "tool_calls"}}}
	}
	model := &mockChat{responses: []mockResponse{
		toolResponse("failing_fixture", 1), toolResponse("inspect_fixture", 1),
		toolResponse("failing_fixture", 2), toolResponse("inspect_fixture", 2),
		toolResponse("failing_fixture", 3),
		// The notice is guidance, not a tool ban or a forced terminal answer.
		toolResponse("failing_fixture", 4),
		{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: "The requested action is still blocked by actual tool validation.", Done: true, FinishReason: "stop"}}},
	}}
	engine := newTestEngine(t, model)
	engine.toolRegistry = agenttools.NewToolRegistry()
	failed := &alwaysFailingTool{BaseTool: agenttools.NewBaseTool("failing_fixture", "always fails in this fixture", json.RawMessage(`{"type":"object"}`))}
	reader := newCountingTool("inspect_fixture")
	engine.toolRegistry.RegisterTool(failed)
	engine.toolRegistry.RegisterTool(reader)
	state, err := engine.executeLoop(context.Background(), &types.AgentState{}, "Complete the required fixture action.", emptyMessages(), emptyTools(), "session", "message")
	require.NoError(t, err)
	require.True(t, state.IsComplete)
	require.Equal(t, 4, failed.calls, "a genuinely corrected future call must remain executable")
	require.Equal(t, 2, reader.calls)
	require.Len(t, model.calls, 7, "the notice cannot terminate or accept the task on its own")
	var guidance strings.Builder
	for _, message := range model.calls[3] {
		if message.Role == "user" {
			guidance.WriteString(message.Content)
		}
	}
	require.Contains(t, guidance.String(), "same failure feedback 2 times")
	require.Contains(t, guidance.String(), "A retry with genuinely corrected evidence remains available")
}
