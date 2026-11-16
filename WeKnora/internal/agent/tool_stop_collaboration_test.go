package agent

import (
	"context"
	"encoding/json"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

type repeatedSourceProgressTool struct {
	agenttools.BaseTool
	data  map[string]any
	calls int
}

func (tool *repeatedSourceProgressTool) Execute(context.Context, json.RawMessage) (*types.ToolResult, error) {
	tool.calls++
	body, _ := json.Marshal(tool.data)
	return &types.ToolResult{Success: true, Data: tool.data, Output: string(body)}, nil
}

func TestToolStopParentWithDelegatesRetainsFailureBoundary(t *testing.T) {
	for _, mode := range []string{"max_iterations", "source_stall"} {
		t.Run(mode, func(t *testing.T) {
			runtime, binding := engineCollaboration(t)
			_, err := runtime.Spawn(t.Context(), binding.Scope, "spawn", "pending_research", "template", "{}", "Read further authorized sources")
			require.NoError(t, err)
			model := &mockChat{}
			e := newTestEngine(t, model, withMaxIterations(1))
			e.SetCollaboration(binding)
			state := &types.AgentState{CurrentRound: 1}
			expectedCalls := 0
			if mode == "source_stall" {
				e.config.MaxIterations, e.config.WorkbenchDefault = 50, true
				state.CurrentRound = 0
				e.toolRegistry = agenttools.NewToolRegistry()
				source := v2ChartFixture(t)
				tool := &repeatedSourceProgressTool{BaseTool: agenttools.NewBaseTool(source.Name, "real structured test result", json.RawMessage(`{"type":"object"}`)), data: source.Result.Data}
				e.toolRegistry.RegisterTool(tool)
				expectedCalls = 1 + toolProgressStopRounds
				for i := 0; i < expectedCalls; i++ {
					model.responses = append(model.responses, mockResponse{chunks: []types.StreamResponse{{Done: true, FinishReason: "tool_calls", ToolCalls: []types.LLMToolCall{{ID: "read", Type: "function", Function: types.FunctionCall{Name: source.Name, Arguments: `{}`}}}}}})
				}
			}
			_, err = e.executeLoop(t.Context(), state, "Summarize the available findings.", emptyMessages(), nil, "s", "m")
			require.Error(t, err)
			require.Empty(t, state.FinalAnswer)
			require.Len(t, model.calls, expectedCalls, "delegated failure must not become an extra final synthesis")
			group, readErr := runtime.Store.Read(t.Context(), binding.Scope)
			require.NoError(t, readErr)
			require.Equal(t, "cancelled", group.Status)
			members, readErr := binding.List(t.Context(), "")
			require.NoError(t, readErr)
			require.Len(t, members, 1)
			require.Equal(t, "cancelled", members[0].Status)
		})
	}
}
