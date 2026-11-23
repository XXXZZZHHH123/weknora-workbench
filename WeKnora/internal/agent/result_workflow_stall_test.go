package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type stalledChartTestTool struct {
	agenttools.BaseTool
	calls int
}

func (tool *stalledChartTestTool) Execute(context.Context, json.RawMessage) (*types.ToolResult, error) {
	tool.calls++
	return &types.ToolResult{Success: false, Error: "invalid selected columns", Data: map[string]any{"kind": "data_chart", "status": "error"}}, nil
}

func TestExecuteLoopSettlesChartStallWithCheckedData(t *testing.T) {
	var replies []mockResponse
	for i := 0; i < toolProgressStopRounds; i++ {
		replies = append(replies, mockResponse{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Done: true, FinishReason: "tool_calls",
			ToolCalls: []types.LLMToolCall{{ID: fmt.Sprintf("chart-%d", i), Type: "function", Function: types.FunctionCall{Name: renderChartToolName, Arguments: `{}`}}},
		}}})
	}
	const answer = "The observation is 12.500 kg."
	replies = append(replies, chatAnswer(answer), sourceAcceptedVerdict())
	e, initial, model := lightStructuredFixture(t, replies...)
	initial.RoundSteps[0].ToolCalls[0].Result.Data["_structured_execution"] = agenttools.StructuredOperationDescriptor{
		Operation: "read_pdf_table", ToolName: "structured_read_pdf_table", Generation: 1, ScopeMode: "knowledge_base", EffectClass: "read",
	}
	initial.RoundSteps[0].ToolCalls = append([]types.ToolCall{{ID: "source-directory", Name: "structured_list_series", Result: &types.ToolResult{Success: true,
		Data: map[string]any{"catalog_summary": map[string]any{"kind": "catalog_group_counts"}}, Output: `{"catalog_summary":{"kind":"catalog_group_counts"}}`,
	}}}, initial.RoundSteps[0].ToolCalls...)
	e.config.MaxIterations = 50
	tool := &stalledChartTestTool{BaseTool: agenttools.NewBaseTool(renderChartToolName, "isolated chart failure", json.RawMessage(`{"type":"object"}`))}
	e.toolRegistry = agenttools.NewToolRegistry()
	e.toolRegistry.RegisterTool(tool)
	state, err := e.executeLoop(t.Context(), initial, "Report the observation.", emptyMessages(), emptyTools(), "s", "m")
	require.NoError(t, err)
	require.Equal(t, toolProgressStopRounds, tool.calls)
	require.Equal(t, answer, state.FinalAnswer)
	require.True(t, e.candidateApprovalCurrent(answer, state))
	require.Len(t, model.calls, toolProgressStopRounds+2)
}

func TestResultWorkflowStallCanDeliverCheckedExistingEvidence(t *testing.T) {
	const answer = "The observation is 12.500 kg."
	e, state, model := lightStructuredFixture(t, chatAnswer(answer), lightReviewReply("keep", ""))
	state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{{Name: renderChartToolName, Result: &types.ToolResult{Success: false, Error: "invalid selection"}}}})
	recorder := &terminalAnswerRecorder{}
	recorder.attach(e.eventBus)
	finished, err := e.finishResultWorkflowStall(t.Context(), state, "Report the observation.", "s", []chat.Message{{Role: "user", Content: "Report the observation."}})
	require.NoError(t, err)
	require.True(t, finished)
	require.Equal(t, answer, state.FinalAnswer)
	require.True(t, e.candidateApprovalCurrent(answer, state))
	done, visible := recorder.snapshot()
	require.Equal(t, 1, done)
	require.Equal(t, answer, visible)
	require.Len(t, model.calls, 2, "one synthesis and the existing bounded source check")
	for _, options := range model.opts {
		require.Empty(t, options.Tools)
		require.Equal(t, "none", options.ToolChoice)
	}
}

func TestResultWorkflowStallDoesNotSynthesizeWithoutExecutedEvidence(t *testing.T) {
	e, _, model := lightStructuredFixture(t)
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{Name: renderChartToolName, Result: &types.ToolResult{Success: false, Error: "no source"}}}}}}
	finished, err := e.finishResultWorkflowStall(t.Context(), state, "Report the observation.", "s", nil)
	require.NoError(t, err)
	require.False(t, finished)
	require.Empty(t, model.calls)
	require.Empty(t, state.FinalAnswer)
	state.RoundSteps[0].ToolCalls = append(state.RoundSteps[0].ToolCalls, types.ToolCall{Name: "structured_list_series", Result: &types.ToolResult{Success: true,
		Data:   map[string]any{"status": "ok", "catalog_summary": map[string]any{"kind": "catalog_group_counts"}},
		Output: `{"status":"ok","catalog_summary":{"kind":"catalog_group_counts"}}`,
	}})
	finished, err = e.finishResultWorkflowStall(t.Context(), state, "Report the observation.", "s", nil)
	require.NoError(t, err)
	require.False(t, finished, "directory counts do not justify another result synthesis")
	require.Empty(t, model.calls)
}

func TestResultWorkflowStallMarksUnsupportedMeasurementsAfterRepairs(t *testing.T) {
	const unsupported = "The observation is 987654.321 kg."
	e, state, _ := lightStructuredFixture(t, chatAnswer(unsupported), chatAnswer(unsupported), chatAnswer(unsupported))
	finished, _ := e.finishResultWorkflowStall(t.Context(), state, "Report the observation.", "s", nil)
	require.True(t, finished)
	require.Contains(t, state.FinalAnswer, "987654.321〔数字待核对〕")
	require.False(t, e.candidateApprovalCurrent(state.FinalAnswer, state))
	require.False(t, e.candidateApprovalCurrent(unsupported, state))
}

func TestResultWorkflowStallCancellationDoesNotSynthesize(t *testing.T) {
	e, state, model := lightStructuredFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	finished, err := e.finishResultWorkflowStall(ctx, state, "Report the observation.", "s", nil)
	require.True(t, finished)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, model.calls)
	require.Empty(t, state.FinalAnswer)
}

func TestResultWorkflowLoopDoesNotSynthesizeWithoutExecutedResults(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run(fmt.Sprint(directory), func(t *testing.T) {
			name, args := renderChartToolName, `{"operation":"render"}`
			expectedCalls := maxChartRenderFailures
			var tool types.Tool
			if directory {
				name, args = "structured_list_series", `{}`
				expectedCalls = 1 + toolProgressStopRounds
				tool = &repeatedSourceProgressTool{BaseTool: agenttools.NewBaseTool(name, "real directory only", json.RawMessage(`{"type":"object"}`)), data: map[string]any{
					"dataset_id": "fixture-source", "series": []any{map[string]any{"series_id": "fixture-series", "unit": "kg"}}, "total": 1,
				}}
			} else {
				tool = &failedProgressChartTool{BaseTool: agenttools.NewBaseTool(name, "render without data", json.RawMessage(`{"type":"object"}`))}
			}
			model := &mockChat{}
			for i := 0; i < expectedCalls; i++ {
				model.responses = append(model.responses, mockResponse{chunks: []types.StreamResponse{{Done: true, FinishReason: "tool_calls", ToolCalls: []types.LLMToolCall{{ID: fmt.Sprint(i), Type: "function", Function: types.FunctionCall{Name: name, Arguments: args}}}}}})
			}
			e := newTestEngine(t, model, withMaxIterations(50))
			e.toolRegistry = agenttools.NewToolRegistry()
			e.toolRegistry.RegisterTool(tool)
			state, err := e.executeLoop(t.Context(), &types.AgentState{}, "Summarize the actual available results.", emptyMessages(), nil, "s", "m")
			require.NoError(t, err)
			require.Len(t, model.calls, expectedCalls, "neither pure directories nor failed renders justify an extra synthesis")
			require.Equal(t, sourceProgressStalledFallback, state.FinalAnswer)
		})
	}
}
