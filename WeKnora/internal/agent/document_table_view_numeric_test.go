package agent

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// The seventh source row/column has no business value of seven. Neither source
// labels nor headers contain numeric indices that could accidentally ground it.
const tableViewNumericSource = "Unit: kg\n" +
	"| Label | A | B | C | D | E | F | G |\n" +
	"| --- | --- | --- | --- | --- | --- | --- | --- |\n" +
	"| Alpha | 10 | 10 | 10 | 10 | 10 | 10 | 20 |\n" +
	"| Beta | 10 | 10 | 10 | 10 | 10 | 10 | 20 |\n" +
	"| Gamma | 10 | 10 | 10 | 10 | 10 | 10 | 20 |\n" +
	"| Delta | 10 | 10 | 10 | 10 | 10 | 10 | 20 |\n" +
	"| Epsilon | 10 | 10 | 10 | 10 | 10 | 10 | 20 |\n" +
	"| Zeta | 10 | 10 | 10 | 10 | 10 | 10 | 20 |\n" +
	"| Eta | 10 | 10 | 10 | 10 | 10 | 10 | 20 |\n"

func TestDocumentTableViewAndCatalogDoNotAuthorizeCoordinatesCountsOrRawCells(t *testing.T) {
	for _, operation := range []string{"select", "inspect"} {
		t.Run(operation, func(t *testing.T) {
			tool, _, _ := tableViewFixture(tableViewNumericSource)
			plan := map[string]any{"operation": operation, "chunk_id": "actual-chunk"}
			if operation == "select" {
				plan["table_index"], plan["row_indices"], plan["selected_columns"] = 0, []int{7}, []int{0, 7}
			}
			result := runTableCalculation(t, tool, plan)
			require.True(t, result.Success, result.Error)
			if operation == "select" {
				snapshot := result.Data["view_snapshot"].(documentTableViewSnapshot)
				require.Equal(t, []int{7}, snapshot.RowIndices)
				require.Equal(t, []int{0, 7}, snapshot.ColumnIndices)
				require.Equal(t, [][]string{{"Eta", "20"}}, snapshot.Rows)
			} else {
				table := result.Data["tables"].([]any)[0].(map[string]any)
				require.Equal(t, 8, table["row_count"])
				require.Equal(t, 8, table["column_count"])
			}
			call := types.ToolCall{ID: "actual-view-or-catalog", Name: documentTableCalculationToolName, Args: plan, Result: result}
			for _, transport := range []struct {
				name     string
				messages []chat.Message
				steps    []types.AgentStep
			}{
				{"canonical", nil, []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}},
				{"named replay", []chat.Message{{Role: "tool", Name: call.Name, Content: result.Output}}, nil},
				{"unnamed replay", []chat.Message{
					{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: call.ID, Function: chat.FunctionCall{Name: call.Name, Arguments: `{}`}}}},
					{Role: "tool", ToolCallID: call.ID, Content: result.Output},
				}, nil},
			} {
				t.Run(transport.name, func(t *testing.T) {
					baseline := []chat.Message{{Role: "tool", Name: "structured_query_series", Content: `{"status":"ok","value":13}`}}
					messages := append(baseline, transport.messages...)
					for _, claim := range []string{"统计7", "统计700%", "统计8", "统计800%", "统计1", "统计100%", "统计20"} {
						require.NotEmpty(t, ungroundedStructuredNumbers(claim, messages, transport.steps), claim)
						// Exercise the strict source-task path independently of the
						// baseline, so empty evidence cannot make this test pass.
						require.NotEmpty(t, ungroundedStructuredNumbersWithEmptyEvidence(claim, transport.messages, true, transport.steps), claim)
					}
				})
			}
		})
	}
}

func TestDocumentTableViewNumbersStillRequireOriginalSuccessfulCitedRead(t *testing.T) {
	tool, _, steps := tableViewFixture(tableViewNumericSource)
	result := runTableCalculation(t, tool, map[string]any{"operation": "select", "chunk_id": "actual-chunk", "table_index": 0, "row_indices": []int{7}, "selected_columns": []int{0, 7}})
	require.True(t, result.Success, result.Error)
	appendTableViewResult(steps, result)
	candidate := result.Data["view_snapshot"].(documentTableViewSnapshot).Markdown
	check := func(answer string) []string {
		return ungroundedStructuredNumbersWithEmptyEvidence(answer, answerNumericEvidence(answer, *steps), true, *steps)
	}
	require.Empty(t, check(candidate), "the expanded table retains its original successful cited source authority")
	require.NotEmpty(t, check(candidate+"\n统计7"), "real cited values cannot authorize a view coordinate")
	require.NotEmpty(t, check("统计20"), "a view snapshot alone cannot replace the original citation")
	(*steps)[0].ToolCalls[0].Result.Success = false
	require.NotEmpty(t, check(candidate), "a surviving view result cannot promote a failed original read")
}

func TestDocumentTableViewNestedAnalysisAndJSONCellsDoNotCreateNumericAuthority(t *testing.T) {
	tool, _, _ := tableViewFixture("Unit: kg\n| Label | Amount |\n| --- | --- |\n| Actual | {\"value\":777} |\n")
	result := runTableCalculation(t, tool, map[string]any{"operation": "select", "chunk_id": "actual-chunk", "table_index": 0, "row_start": 1, "row_end": 1, "selected_columns": []int{0, 1}})
	require.True(t, result.Success, result.Error)
	require.Equal(t, `{"value":777}`, result.Data["view_snapshot"].(documentTableViewSnapshot).Rows[0][1])
	for _, status := range []string{"ok", "partial"} {
		data := map[string]any{"kind": "document_table_analysis", "status": status, "executions": []any{map[string]any{"success": true, "tool_name": documentTableCalculationToolName, "result": result.Data}}}
		call := types.ToolCall{ID: "actual-selected-analysis", Name: documentTableAnalysisToolName, Result: &types.ToolResult{Success: status == "ok", Data: data, Output: string(mustJSON(t, data))}}
		steps := []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}
		require.NotEmpty(t, ungroundedStructuredNumbersWithEmptyEvidence("统计777", nil, true, steps), status)
		require.NotEmpty(t, ungroundedStructuredNumbersWithEmptyEvidence("统计777", []chat.Message{{Role: "tool", Name: call.Name, Content: call.Result.Output}}, true), status)
	}
	for _, messages := range [][]chat.Message{
		{{Role: "tool", Name: documentTableCalculationToolName, Content: result.Output}},
		{{Role: "tool", Content: result.Output}},
	} {
		require.NotEmpty(t, ungroundedStructuredNumbersWithEmptyEvidence("统计777", messages, true))
	}
}
