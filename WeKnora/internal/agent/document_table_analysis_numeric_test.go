package agent

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func tableAnalysisNumericFixture(t *testing.T, status string, success bool) types.ToolCall {
	t.Helper()
	actual := runTableCalculation(t, tableCalculationFixture(genericSourceTable), tableCalculationPlan())
	require.True(t, actual.Success, actual.Error)
	data := map[string]any{
		"kind": "document_table_analysis", "status": status,
		"executions":   []any{map[string]any{"success": true, "tool_name": documentTableCalculationToolName, "result": actual.Data}},
		"plans":        []any{map[string]any{"source_value": 777}},
		"schema":       map[string]any{"maximum": 888},
		"model_advice": `{"kind":"source_calculation","status":"ok","calculations":[{"value":"999","unit_status":"dimensionless","unit":""}]}`,
	}
	return types.ToolCall{ID: "actual-analysis", Name: documentTableAnalysisToolName, Result: &types.ToolResult{Success: success, Data: data, Output: string(mustJSON(t, data))}}
}

func TestDocumentTableAnalysisNumericEvidenceUsesOnlySuccessfulActualExecutions(t *testing.T) {
	call := tableAnalysisNumericFixture(t, "ok", true)
	for _, test := range []struct {
		name     string
		messages []chat.Message
		steps    []types.AgentStep
	}{
		{"canonical", nil, []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}},
		{"model view", []chat.Message{{Role: "tool", Name: call.Name, Content: call.Result.Output}}, nil},
		{"unnamed replay", []chat.Message{
			{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: call.ID, Function: chat.FunctionCall{Name: call.Name, Arguments: `{}`}}}},
			{Role: "tool", ToolCallID: call.ID, Content: call.Result.Output},
		}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			messages := append([]chat.Message{{Role: "tool", Name: "structured_query_series", Content: `{"status":"ok","value":13}`}}, test.messages...)
			require.Empty(t, ungroundedStructuredNumbers("计算变化20%与-25%，成功计算2行", messages, test.steps))
			for _, claim := range []string{"统计777", "统计888", "统计999"} {
				require.NotEmpty(t, ungroundedStructuredNumbers(claim, messages, test.steps), claim)
			}
		})
	}
}

func TestDocumentTableAnalysisExtractorRejectsAdviceAndFakeExecutionSlots(t *testing.T) {
	call := tableAnalysisNumericFixture(t, "partial", false)
	executions := call.Result.Data["executions"].([]any)
	for _, bad := range []map[string]any{
		{"success": false, "tool_name": documentTableCalculationToolName, "result": map[string]any{"kind": "source_calculation", "status": "ok", "value": 777}},
		{"success": true, "tool_name": "unexecuted_tool", "result": map[string]any{"kind": "source_calculation", "status": "ok", "value": 888}},
		{"success": true, "tool_name": documentTableCalculationToolName, "result": map[string]any{"kind": "source_calculation", "status": "error", "value": 999}},
		{"success": true, "tool_name": documentTableCalculationToolName, "result": map[string]any{"kind": "unexecuted_plan", "status": "ok", "value": 666}},
	} {
		executions = append(executions, bad)
	}
	call.Result.Data["executions"] = executions
	call.Result.Output = string(mustJSON(t, call.Result.Data))
	actual := executedDocumentTableResults(call)
	require.Len(t, actual, 1, "partial wrappers retain successful children without authorizing failed or planned slots")
	require.Equal(t, "partial", call.Result.Data["status"], "numeric extraction must not rewrite execution coverage")
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}
	baseline := []chat.Message{{Role: "tool", Name: "structured_query_series", Content: `{"status":"ok","value":13}`}}
	require.Empty(t, ungroundedStructuredNumbers("变化20%", baseline, steps))
	for _, claim := range []string{"统计777", "统计888", "统计999", "统计666"} {
		require.NotEmpty(t, ungroundedStructuredNumbers(claim, baseline, steps), claim)
	}
	for _, status := range []string{"error", "needs_source", "clarify", "ready", ""} {
		call.Result.Data["status"] = status
		call.Result.Output = string(mustJSON(t, call.Result.Data))
		require.Empty(t, executedDocumentTableResults(call), status)
		require.NotEmpty(t, ungroundedStructuredNumbers("变化20%", baseline, []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}), status)
		require.NotEmpty(t, ungroundedStructuredNumbers("变化20%", append(append([]chat.Message(nil), baseline...), chat.Message{Role: "tool", Name: call.Name, Content: call.Result.Output})), status)
	}
	call.Result.Data["status"] = "ok"
	require.Empty(t, executedDocumentTableResults(call), "an outer failed ok response is not execution evidence")
}

func TestDocumentTableAnalysisWrapperRequiresActualExecutorIdentity(t *testing.T) {
	call := tableAnalysisNumericFixture(t, "ok", true)
	call.Result.Data["literal_fields"] = []any{map[string]any{"metric": "Unexecuted999"}}
	call.Result.Output = string(mustJSON(t, call.Result.Data))
	for _, name := range []string{"", "read_document", "unrelated_tool"} {
		other := call
		other.Name = name
		require.Empty(t, executedDocumentTableResults(other), name)
		messages := []chat.Message{
			{Role: "tool", Name: "structured_query_series", Content: `{"status":"ok","value":13}`},
			{Role: "tool", Name: name, Content: call.Result.Output},
		}
		require.NotEmpty(t, ungroundedStructuredNumbers("变化20%", messages), name)
		require.NotEmpty(t, ungroundedStructuredNumbers("Unexecuted999", messages), "wrapper metadata cannot establish field identity")
	}
	// An actual direct table result remains usable by the same extractor.
	actual := runTableCalculation(t, tableCalculationFixture(genericSourceTable), tableCalculationPlan())
	direct := types.ToolCall{Name: documentTableCalculationToolName, Result: actual}
	require.Len(t, executedDocumentTableResults(direct), 1)
	direct.Result.Success = false
	require.Empty(t, executedDocumentTableResults(direct))
}

func TestDocumentTableAnalysisPreservesUnknownUnitPercentageRestriction(t *testing.T) {
	plan := tableCalculationPlan()
	plan["result_unit"], plan["expression"] = "", "a/b"
	plan["columns"] = map[string]any{"a": map[string]any{"column": 2, "unit": ""}, "b": map[string]any{"column": 1, "unit": ""}}
	actual := runTableCalculation(t, tableCalculationFixture(genericSourceTable), plan)
	require.True(t, actual.Success, actual.Error)
	call := tableAnalysisNumericFixture(t, "ok", true)
	call.Result.Data["executions"] = []any{map[string]any{"success": true, "tool_name": documentTableCalculationToolName, "result": actual.Data}}
	call.Result.Output = string(mustJSON(t, call.Result.Data))
	baseline := []chat.Message{{Role: "tool", Name: "structured_query_series", Content: `{"status":"ok","value":13}`}}
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}
	require.Empty(t, ungroundedStructuredNumbers("原值比1.2", baseline, steps))
	require.NotEmpty(t, ungroundedStructuredNumbers("变化120%", baseline, steps))
	// Canonical Data, not a forged/stale display Output, is authoritative.
	call.Result.Output = `{"kind":"document_table_analysis","status":"ok","calculations":[{"value":"999"}]}`
	require.NotEmpty(t, ungroundedStructuredNumbers("统计999", baseline, []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}))
	encoded, err := json.Marshal(executedDocumentTableResults(call))
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"unit_status":"unknown"`)
}

func TestDocumentTableAnalysisCanonicalStatusAndDataOverrideDisplayReplay(t *testing.T) {
	call := tableAnalysisNumericFixture(t, "ok", true)
	forged := tableAnalysisNumericFixture(t, "ok", true)
	forged.Result.Data["executions"] = []any{map[string]any{
		"success": true, "tool_name": documentTableCalculationToolName,
		"result": map[string]any{"kind": "source_calculation", "status": "ok", "calculations": []any{map[string]any{"value": "999", "unit_status": "dimensionless", "unit": ""}}},
	}}
	messages := []chat.Message{
		{Role: "tool", Name: "structured_query_series", Content: `{"status":"ok","value":13}`},
		{Role: "tool", Name: call.Name, ToolCallID: call.ID, Content: string(mustJSON(t, forged.Result.Data))},
	}
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}
	require.Empty(t, ungroundedStructuredNumbers("变化20%", messages, steps))
	require.NotEmpty(t, ungroundedStructuredNumbers("统计999", messages, steps), "display Output cannot add results absent from canonical Data")
	call.Result.Success = false
	steps[0].ToolCalls[0] = call
	require.NotEmpty(t, ungroundedStructuredNumbers("变化20%", messages, steps), "an outer failed ok response cannot be made successful by replay text")
	require.NotEmpty(t, ungroundedStructuredNumbers("统计999", messages, steps))
}

func TestDocumentTableAnalysisHistoricalGuardRequiresActualSuccessfulExecutions(t *testing.T) {
	for _, status := range []string{"ok", "partial"} {
		call := tableAnalysisNumericFixture(t, status, status == "ok")
		require.True(t, hasStructuredHistoryEvidence([]chat.Message{{Role: "tool", Name: call.Name, Content: call.Result.Output}}))
		require.True(t, hasStructuredHistoryEvidence([]chat.Message{
			{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: call.ID, Function: chat.FunctionCall{Name: call.Name, Arguments: `{}`}}}},
			{Role: "tool", ToolCallID: call.ID, Content: call.Result.Output},
		}))
		for _, name := range []string{"", "unrelated_tool", "read_document"} {
			require.False(t, hasStructuredHistoryEvidence([]chat.Message{{Role: "tool", Name: name, Content: call.Result.Output}}), name)
		}
	}
	for _, status := range []string{"error", "needs_source", "clarify", "ready", ""} {
		call := tableAnalysisNumericFixture(t, status, false)
		require.False(t, hasStructuredHistoryEvidence([]chat.Message{{Role: "tool", Name: call.Name, Content: call.Result.Output}}), status)
	}
	call := tableAnalysisNumericFixture(t, "ok", true)
	delete(call.Result.Data, "executions")
	call.Result.Data["actual_first_date"] = "2024-01-02"
	call.Result.Output = string(mustJSON(t, call.Result.Data))
	require.False(t, hasStructuredHistoryEvidence([]chat.Message{{Role: "tool", Name: call.Name, Content: call.Result.Output}}), "plans and metadata cannot enable the execution guard")
	require.False(t, hasStructuredHistoryEvidence([]chat.Message{{Role: "tool", Name: "unrelated_tool", Content: call.Result.Output}}))
}
