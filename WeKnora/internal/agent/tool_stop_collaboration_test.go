package agent

import (
	"encoding/json"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestToolStopParentInterruptsDelegatesAndSynthesizesBody(t *testing.T) {
	for _, mode := range []string{"max_iterations", "source_stall"} {
		t.Run(mode, func(t *testing.T) {
			r, binding := engineCollaboration(t)
			_, err := r.Spawn(t.Context(), binding.Scope, "spawn", "pending_research", "template", "{}", "Read further authorized sources")
			require.NoError(t, err)
			model := &mockChat{}
			e := newTestEngine(t, model, withMaxIterations(1))
			e.SetCollaboration(binding)
			state := &types.AgentState{CurrentRound: 1}
			if mode == "source_stall" {
				e.config.MaxIterations = 50
				state.CurrentRound = 0
				e.toolRegistry = agenttools.NewToolRegistry()
				e.toolRegistry.RegisterTool(&repeatedSourceProgressTool{BaseTool: agenttools.NewBaseTool(renderChartToolName, "synthetic directory", json.RawMessage(`{"type":"object"}`)), data: map[string]any{"kind": "chart_source_catalog", "sources": []any{map[string]any{"source_id": "actual-source", "row_count": 1}}}})
				for i := 0; i < 1+toolProgressStopRounds; i++ {
					model.responses = append(model.responses, mockResponse{chunks: []types.StreamResponse{{Done: true, FinishReason: "tool_calls", ToolCalls: []types.LLMToolCall{{ID: "inspect", Type: "function", Function: types.FunctionCall{Name: renderChartToolName, Arguments: `{"operation":"inspect"}`}}}}}})
				}
			}
			const answer = "Further research remains unfinished. The pending delegated task was interrupted, so it provides no completed findings."
			model.responses = append(model.responses, chatAnswer(answer), completenessVerdict(true))
			_, err = e.executeLoop(t.Context(), state, "Summarize the available findings.", emptyMessages(), nil, "s", "m")
			require.NoError(t, err)
			require.Equal(t, answer, state.FinalAnswer)
			require.True(t, state.IsComplete)
			finalIndex := len(model.opts) - 2
			require.Equal(t, "none", model.opts[finalIndex].ToolChoice)
			require.Contains(t, model.opts[finalIndex].RuntimeNotice, "child executions were interrupted")
			require.True(t, strings.Contains(model.calls[finalIndex+1][1].Content, "runtime_tool_execution_stop"), "review knows the runtime boundary without treating it as source data")
			group, err := r.Store.Read(t.Context(), binding.Scope)
			require.NoError(t, err)
			require.Equal(t, "cancelled", group.Status)
			members, err := binding.List(t.Context(), "")
			require.NoError(t, err)
			require.Len(t, members, 1)
			require.Equal(t, "interrupted", members[0].Status)
		})
	}
}
