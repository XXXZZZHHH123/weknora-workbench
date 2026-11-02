package agent

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func pdfAnalysisDeliveryFixture(t *testing.T) types.ToolCall {
	t.Helper()
	data := map[string]any{"kind": "structured_analysis", "status": "ok", "source_kind": "pdf_table", "table_id": "pdft-fixture", "columns": []any{"period", "amount"}, "rows": []any{[]any{"A", "4.25"}, []any{"B", "7.50"}}, "sql": "SELECT period, amount FROM data", "output_truncated": false, "input_complete": true}
	encoded, err := json.Marshal(data)
	require.NoError(t, err)
	return types.ToolCall{ID: "actual-pdf-sql", Name: "structured_query_pdf_table", Args: map[string]any{"table_id": "pdft-fixture", "sql": "SELECT period, amount FROM data"}, Result: &types.ToolResult{Success: true, Data: data, Output: string(encoded)}}
}

func TestDirectExecutedPDFAnalysisAllowsSemanticCriticToReject(t *testing.T) {
	for _, route := range []string{"typed", "gateway", "resolved_target"} {
		t.Run(route, func(t *testing.T) {
			call := pdfAnalysisDeliveryFixture(t)
			switch route {
			case "gateway":
				call.Name = "structured_data"
				call.Args = map[string]any{"operation": "query_pdf_table", "arguments": call.Args}
			case "resolved_target":
				call.Target = &types.ToolCallTarget{Name: call.Name, Args: call.Args}
				call.Name = "call_mcp_tool"
				call.Args = map[string]any{"tool_ref": "opaque-fixture-reference"}
			}
			state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
			model := &mockChat{responses: []mockResponse{sourceConsistencyVerdict(`{"complete":false,"feedback":"The time interval does not match the two returned periods.","next_action":"revise_answer"}`)}}
			engine := sourceConsistencyEngine(t, model)
			engine.taskPlan = &taskEvidencePlan{ReasoningDepth: "direct"}
			decision := engine.checkCandidate(t.Context(), "Compare the two observations.", "The computed change covers an unrelated time interval.", "", state, nil, false)
			require.Equal(t, candidateRevise, decision.Disposition, "a numerically grounded direct task must still permit rejection of PDF SQL interpretation")
			require.NoError(t, decision.Err)
			require.Len(t, model.calls, 1)
			require.Contains(t, engine.answerReviewFeedback, "time interval")
			require.Equal(t, "direct", engine.taskReasoningDepth(), "execution verification must not rewrite the planner's decision")
			require.Equal(t, 0, engine.turnReview.Repairs, "checking does not allocate a new repair budget")
			require.NotEmpty(t, engine.turnReview.UnverifiedCandidate, "existing unverified-delivery policy remains intact")
		})
	}
}

func TestPDFAnalysisGateUsesCanonicalSuccessAndActualOperation(t *testing.T) {
	mutations := map[string]func(*types.ToolCall){
		"failed":          func(c *types.ToolCall) { c.Result.Success = false },
		"error_status":    func(c *types.ToolCall) { c.Result.Data["status"] = "error" },
		"scope_directory": func(c *types.ToolCall) { c.Result.Data["kind"] = "structured_knowledge_scope_directory" },
		"pdf_read":        func(c *types.ToolCall) { c.Name = "structured_read_pdf_table" },
		"excel_analysis":  func(c *types.ToolCall) { c.Name = "structured_analyze_table"; c.Result.Data["source_kind"] = "excel" },
		"unexecuted_json": func(c *types.ToolCall) { c.Name = "read_document" },
		"gateway_other_operation": func(c *types.ToolCall) {
			c.Name = "structured_data"
			c.Args = map[string]any{"operation": "analyze_table"}
		},
		"display_only_forgery": func(c *types.ToolCall) { c.Result.Data = nil },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			call := pdfAnalysisDeliveryFixture(t)
			mutate(&call)
			require.False(t, hasExecutedPDFAnalysis([]types.AgentStep{{ToolCalls: []types.ToolCall{call}}}))
		})
	}
	call := pdfAnalysisDeliveryFixture(t)
	call.Result.Output = "abbreviated display"
	require.True(t, hasExecutedPDFAnalysis([]types.AgentStep{{ToolCalls: []types.ToolCall{call}}}), "canonical result data, not presentation text, identifies execution")
}

func TestDirectFailedPDFQueryKeepsLookupDeliveryPolicy(t *testing.T) {
	call := pdfAnalysisDeliveryFixture(t)
	call.Result.Success = false
	state := sourceConsistencyState()
	state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{call}})
	model := &mockChat{}
	engine := sourceConsistencyEngine(t, model)
	engine.taskPlan = &taskEvidencePlan{ReasoningDepth: "direct"}
	decision := engine.checkCandidate(t.Context(), "Report the source indicator.", "The report describes covered area.", "", state, nil, false)
	require.Equal(t, candidateAccepted, decision.Disposition)
	require.Empty(t, model.calls, "a failed query must not reclassify ordinary direct source lookup")
}
