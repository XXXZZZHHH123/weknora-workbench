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

func TestFailedToolFeedbackSurvivesChangedArgumentsAndUnrelatedReads(t *testing.T) {
	guards := &loopGuards{}
	for attempt := 1; attempt <= 5; attempt++ {
		name, count, nudge := guards.recordFailedToolCall(feedbackFailure("source_bound_fixture", attempt, "exact source evidence did not match"))
		if attempt == maxConsecutiveFailedToolCalls {
			require.True(t, nudge)
			require.Equal(t, "source_bound_fixture", name)
			require.Equal(t, 3, count)
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
		if attempt == 3 {
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
	for attempt := 1; attempt <= 2; attempt++ {
		require.Nil(t, loop.observe(feedbackFailure("fixture", attempt, "source \n validation failed")))
	}
	require.NotNil(t, loop.observe(feedbackFailure("fixture", 3, "source validation failed")), "only whitespace is normalized for prose feedback")
	require.Nil(t, loop.observe(types.ToolCall{Name: "fixture", Result: &types.ToolResult{Success: true}}))
	for attempt := 1; attempt <= 2; attempt++ {
		require.Nil(t, loop.observe(feedbackFailure("fixture", attempt, "source validation failed")), "successful recovery starts a fresh sequence")
	}
	require.NotNil(t, loop.observe(feedbackFailure("fixture", 3, "source validation failed")))
}

func TestFailedToolFeedbackIsBoundedAndDoesNotReuseStaleOrMissingDiagnostics(t *testing.T) {
	loop := &failedToolFeedbackLoop{}
	for attempt := 1; attempt <= 2; attempt++ {
		require.Nil(t, loop.observe(feedbackFailure("fixture", attempt, "source evidence did not match")))
	}
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
	for _, message := range model.calls[5] {
		if message.Role == "user" {
			guidance.WriteString(message.Content)
		}
	}
	require.Contains(t, guidance.String(), "same failure feedback 3 times")
	require.Contains(t, guidance.String(), "A retry with genuinely corrected evidence remains available")
}
