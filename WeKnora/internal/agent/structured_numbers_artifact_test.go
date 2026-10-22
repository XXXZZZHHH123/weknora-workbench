package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

const numericChartHandle = "resource://Ab12Cd34Ef56Gh78Ij90Kl"

func numericChartSteps() []types.AgentStep {
	return []types.AgentStep{{ToolCalls: []types.ToolCall{
		{ID: "query-now", Name: "structured_analyze_table", Result: &types.ToolResult{
			Success: true, Output: `{"kind":"structured_analysis","status":"ok","rows":[[7]]}`,
			Data: map[string]any{"kind": "structured_analysis", "status": "ok", "rows": []any{[]any{7.0}}},
		}},
		{ID: "render-now", Name: "render_chart", Args: map[string]any{"operation": "render"}, Result: &types.ToolResult{
			Success: true, Output: "![Chart](" + numericChartHandle + ")",
			Data: map[string]any{"kind": "data_chart", "status": "ok", "delivery_mode": "chart_only", "source_id": "chart-source-current",
				"artifact": map[string]any{"index": 0, "handle": numericChartHandle, "file_name": "chart-saved.png"}},
		}},
	}}}
}

func numericChartCheck(answer string, steps []types.AgentStep, history ...chat.Message) []string {
	return ungroundedStructuredNumbers(answer, answerNumericEvidence(answer, steps, history), steps)
}

func TestStructuredNumbersRegisteredChartURLIsIdentityNotDecimalClaims(t *testing.T) {
	for _, answer := range []string{
		"![Chart](" + numericChartHandle + ")",
		"[Download](" + numericChartHandle + ")",
		"![Chart](<" + numericChartHandle + ">)",
	} {
		t.Run(answer, func(t *testing.T) {
			require.Empty(t, numericChartCheck(answer, numericChartSteps()))
		})
	}
	steps := numericChartSteps()
	steps[0].ToolCalls[1].Result.Data["artifact"] = &chart.Delivery{Handle: numericChartHandle, Index: 0, FileName: "chart-saved.png"}
	require.Empty(t, numericChartCheck("![Chart]("+numericChartHandle+")", steps), "live typed deliveries and persisted maps register the same target")
}

func TestStructuredNumbersRegisteredChartURLDoesNotGroundVisibleNumbers(t *testing.T) {
	for _, answer := range []string{
		"![999](" + numericChartHandle + ")",
		"![Chart](" + numericChartHandle + ` "999")`,
		"![Chart](" + numericChartHandle + ") value 999",
	} {
		t.Run(answer, func(t *testing.T) {
			require.Contains(t, numericChartCheck(answer, numericChartSteps()), "999")
		})
	}
	require.Contains(t, numericChartCheck("![Chart]("+numericChartHandle+") value 34", numericChartSteps()), "34", "an ID never becomes an observed numeric value")
	require.NotEmpty(t, numericChartCheck("The saved ID is "+numericChartHandle, numericChartSteps()), "plain prose is not a Markdown destination")
}

func TestStructuredNumbersOnlyCurrentSuccessfulChartRegistersItsExactDestination(t *testing.T) {
	mutations := map[string]func(*types.ToolCall){
		"failed":           func(c *types.ToolCall) { c.Result.Success = false },
		"different_tool":   func(c *types.ToolCall) { c.Name = "read_document" },
		"error_status":     func(c *types.ToolCall) { c.Result.Data["status"] = "error" },
		"inspection":       func(c *types.ToolCall) { c.Result.Data["kind"] = "chart_source_catalog" },
		"negative_index":   func(c *types.ToolCall) { c.Result.Data["artifact"].(map[string]any)["index"] = -1 },
		"missing_index":    func(c *types.ToolCall) { delete(c.Result.Data["artifact"].(map[string]any), "index") },
		"fractional_index": func(c *types.ToolCall) { c.Result.Data["artifact"].(map[string]any)["index"] = 0.5 },
		"invalid_handle": func(c *types.ToolCall) {
			c.Result.Data["artifact"].(map[string]any)["handle"] = "resource://invalid999"
		},
		"no_data":         func(c *types.ToolCall) { c.Result.Data = nil },
		"not_saved_image": func(c *types.ToolCall) { c.Result.Data["artifact"].(map[string]any)["file_name"] = "proposed.html" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			steps := numericChartSteps()
			mutate(&steps[0].ToolCalls[1])
			require.NotEmpty(t, numericChartCheck("![Chart]("+numericChartHandle+")", steps))
		})
	}
	for _, target := range []string{
		strings.Replace(numericChartHandle, "90", "91", 1), numericChartHandle + "?value=999",
		"res://9999", "sandbox:chart-999.png",
	} {
		t.Run(target, func(t *testing.T) {
			require.NotEmpty(t, numericChartCheck("![Chart]("+target+")", numericChartSteps()))
		})
	}
	historical := numericChartSteps()[0].ToolCalls[1].Result.Data
	body, err := json.Marshal(historical)
	require.NoError(t, err)
	history := []chat.Message{{Role: "tool", Name: "render_chart", Content: string(body)}, {Role: "tool", Name: "structured_analyze_table", Content: `{"kind":"structured_analysis","status":"ok","rows":[[7]]}`}}
	require.NotEmpty(t, numericChartCheck("![Chart]("+numericChartHandle+")", nil, history...), "a previous turn's saved artifact is not current registration")
}

func TestRegisteredChartCandidateStillRunsTheSourceCritic(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{false: "rejected_scope", true: "accepted_scope"}[accepted], func(t *testing.T) {
			response := sourceConsistencyVerdict(`{"complete":false,"feedback":"The requested scope is not supported by these source rows.","next_action":"revise_answer"}`)
			if accepted {
				response = sourceAcceptedVerdict()
			}
			model := &mockChat{responses: []mockResponse{response}}
			engine := sourceConsistencyEngine(t, model)
			decision := engine.checkCandidate(t.Context(), "Draw the requested observations only", "![Chart]("+numericChartHandle+")", "", &types.AgentState{RoundSteps: numericChartSteps()}, nil, true)
			require.Empty(t, decision.Unsupported)
			require.NotEmpty(t, model.calls, "artifact identity never bypasses semantic source review")
			if accepted {
				require.Equal(t, candidateAccepted, decision.Disposition)
			} else {
				require.Equal(t, candidateRevise, decision.Disposition)
			}
		})
	}
}
