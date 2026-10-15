package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func tableCalculationFixture(content string) *documentTableCalculationTool {
	return newDocumentTableCalculationTool(func() []types.AgentStep {
		return documentReviewFixture("read_document", true, map[string]any{"chunks": []any{map[string]any{"chunk_id": "actual-chunk", "knowledge_id": "actual-document", "content": content}}})
	})
}

func runTableCalculation(t *testing.T, tool *documentTableCalculationTool, request map[string]any) *types.ToolResult {
	t.Helper()
	raw, err := json.Marshal(request)
	require.NoError(t, err)
	result, err := tool.Execute(context.Background(), raw)
	require.NoError(t, err)
	return result
}

func tableCalculationPlan() map[string]any {
	return map[string]any{"operation": "calculate", "chunk_id": "actual-chunk", "table_index": 0, "row_start": 1, "row_end": 2, "columns": map[string]any{"a": map[string]any{"column": 2, "unit": "kg"}, "b": map[string]any{"column": 1, "unit": "kg"}}, "expression": "a/b-1", "result_unit": "%"}
}

const genericSourceTable = "Unit: kg\n| Label | Before | After |\n| --- | ---: | :---: |\n| 2024-01-02 | 10 | 12 |\n| Item\\|two | 4 | 3 |\n"

func TestDocumentTableSyntaxRejectsIncompleteDelimitersWithoutPanicking(t *testing.T) {
	for _, text := range []string{"", "|", "||", " | ", "\\|", "| x"} {
		require.NotPanics(t, func() { _, _ = documentTableCells(text); _ = documentSourceTables(text + "\n" + text) })
	}
}

func TestDocumentTableInspectionRetainsActualIndicesDatesAndEscapedPipes(t *testing.T) {
	result := runTableCalculation(t, tableCalculationFixture(genericSourceTable), map[string]any{"operation": "inspect", "chunk_id": "actual-chunk"})
	require.True(t, result.Success, result.Error)
	require.Equal(t, "document_table_catalog", result.Data["kind"])
	tables := result.Data["tables"].([]any)
	require.Len(t, tables, 1)
	table := tables[0].(map[string]any)
	require.Equal(t, 3, table["row_count"])
	rows := table["rows"].([][]string)
	require.Equal(t, "2024-01-02", rows[1][0])
	require.Equal(t, "Item|two", rows[2][0])
	require.Contains(t, table["unit_context"], "Unit: kg")
}

func TestDocumentTableExecutesModelSelectedColumnsAndFormulaWithExactProvenance(t *testing.T) {
	result := runTableCalculation(t, tableCalculationFixture(genericSourceTable), tableCalculationPlan())
	require.True(t, result.Success, result.Error)
	rows := result.Data["calculations"].([]any)
	require.Len(t, rows, 2)
	first := rows[0].(map[string]any)
	require.Equal(t, "20", first["value"])
	require.Equal(t, "20", first["exact_value"])
	require.Equal(t, "%", first["unit"])
	require.Equal(t, "2024-01-02", first["row_label"])
	require.Equal(t, "| 2024-01-02 | 10 | 12 |", first["source_row"])
	require.Equal(t, "-25", rows[1].(map[string]any)["value"])
	require.Equal(t, "actual-document", result.Data["knowledge_id"])
	inputs := first["inputs"].(map[string]any)
	require.Equal(t, "12", inputs["a"].(map[string]any)["source_value"])
}

func TestDocumentTableRefusesUnknownSourceInvalidParametersUnitGuessAndLiteralFormula(t *testing.T) {
	for _, mutation := range []func(map[string]any){
		func(p map[string]any) { p["chunk_id"] = "unread" },
		func(p map[string]any) { p["table_index"] = 1 },
		func(p map[string]any) { delete(p, "row_start") },
		func(p map[string]any) { p["row_end"] = 3 },
		func(p map[string]any) { p["expression"] = "12/10-1" },
		func(p map[string]any) { p["expression"] = "a/b*100" },
		func(p map[string]any) {
			p["columns"] = map[string]any{"a": map[string]any{"column": 2, "unit": "tonnes"}, "b": map[string]any{"column": 1, "unit": "tonnes"}}
		},
		func(p map[string]any) { p["columns"] = map[string]any{"a": map[string]any{"unit": "kg"}} },
		func(p map[string]any) { p["columns"] = map[string]any{"a": map[string]any{"column": 99, "unit": "kg"}} },
		func(p map[string]any) {
			p["columns"] = map[string]any{"a": map[string]any{"column": 2, "unit": ""}, "b": map[string]any{"column": 1, "unit": ""}}
		},
	} {
		plan := tableCalculationPlan()
		mutation(plan)
		result := runTableCalculation(t, tableCalculationFixture(genericSourceTable), plan)
		require.False(t, result.Success, result.Output)
	}
}

func TestDocumentTableNeverParsesToolEnvelopesJSONOrCodeAsSourceTables(t *testing.T) {
	for _, source := range []string{"```\n" + genericSourceTable + "\n```", "<tool_result>" + genericSourceTable + "</tool_result>", "<assistant>" + genericSourceTable + "</assistant>"} {
		result := runTableCalculation(t, tableCalculationFixture(source), map[string]any{"operation": "inspect", "chunk_id": "actual-chunk"})
		require.True(t, result.Success)
		require.Equal(t, 0, result.Data["table_count"])
	}
	encoded, _ := json.Marshal(map[string]any{"tool_table": genericSourceTable})
	require.Empty(t, documentSourceTables(string(encoded)))
	failed := newDocumentTableCalculationTool(func() []types.AgentStep {
		return documentReviewFixture("read_document", false, map[string]any{"chunks": []any{map[string]any{"chunk_id": "actual-chunk", "knowledge_id": "actual-document", "content": genericSourceTable}}})
	})
	require.False(t, runTableCalculation(t, failed, tableCalculationPlan()).Success)
}

func TestDocumentTableMissingValuesAndZeroDenominatorsRemainExplicit(t *testing.T) {
	source := strings.Replace(genericSourceTable, "| Item\\|two | 4 | 3 |", "| Missing | | 3 |\n| Zero | 0 | 8 |", 1)
	plan := tableCalculationPlan()
	plan["row_end"] = 3
	result := runTableCalculation(t, tableCalculationFixture(source), plan)
	require.True(t, result.Success, result.Error)
	require.Equal(t, 3, result.Data["selected_row_count"])
	require.Equal(t, 1, result.Data["calculated_row_count"])
	require.Len(t, result.Data["failed_rows"].([]any), 2)
	failed := result.Data["failed_rows"].([]any)[0].(map[string]any)
	require.Equal(t, "| Missing | | 3 |", failed["source_row"])
	require.Equal(t, "", failed["selected_cells"].(map[string]any)["b"].(map[string]any)["source_value"], "review can distinguish actual blank source cells without another read or guessing a value")
	zero := result.Data["failed_rows"].([]any)[1].(map[string]any)
	require.Contains(t, zero["error"], "不能除以零", "formula preflight cannot replace the actual source denominator check")
}

func TestDocumentTableInvalidSharedFormulaFailsOnceBeforeRowExecution(t *testing.T) {
	for _, expression := range []string{"a/b*100", "a/c-1", "a+b", "a/b;run()"} {
		plan := tableCalculationPlan()
		plan["expression"] = expression
		result := runTableCalculation(t, tableCalculationFixture(genericSourceTable), plan)
		require.False(t, result.Success)
		require.Contains(t, result.Error, "本次未逐行执行")
		require.NotContains(t, result.Output, "source_row", "a shared invalid formula must not repeat every row as an independent failure")
		require.NotContains(t, result.Output, "calculations")
	}
}

func TestDocumentTablePreflightDoesNotSubstituteDummyValuesForSourceArithmetic(t *testing.T) {
	plan := tableCalculationPlan()
	// A dummy-value execution would see a-a=0 and incorrectly reject this
	// valid expression. Only the actual source values can decide division.
	plan["expression"] = "a/(a-b)"
	plan["result_unit"] = ""
	result := runTableCalculation(t, tableCalculationFixture(genericSourceTable), plan)
	require.True(t, result.Success, result.Error)
	rows := result.Data["calculations"].([]any)
	require.Equal(t, "6", rows[0].(map[string]any)["exact_value"])
	require.Equal(t, "-3", rows[1].(map[string]any)["exact_value"])
	plan["expression"] = "a/(b-b)"
	result = runTableCalculation(t, tableCalculationFixture(genericSourceTable), plan)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "不能除以零")
	require.NotContains(t, result.Error, "本次未逐行执行")
}

func TestDocumentTablePreservesMalformedNumbersInsteadOfInventingValues(t *testing.T) {
	source := "Unit: kg\n| Label | Amount |\n| --- | --- |\n| Valid | 1,234.5 |\n| Comma | 1,2 |\n| Group | 12,34,567 |\n| Slash | 12\\34 |\n"
	tool := tableCalculationFixture(source)
	inspected := runTableCalculation(t, tool, map[string]any{"operation": "inspect", "chunk_id": "actual-chunk"})
	require.True(t, inspected.Success)
	view := inspected.Data["tables"].([]any)[0].(map[string]any)["rows"].([][]string)
	require.Equal(t, `12\34`, view[4][1], "a non-delimiter escape must remain source text")
	result := runTableCalculation(t, tool, map[string]any{
		"operation": "calculate", "chunk_id": "actual-chunk", "table_index": 0, "row_start": 1, "row_end": 4,
		"columns": map[string]any{"x": map[string]any{"column": 1, "unit": "kg"}}, "expression": "x", "result_unit": "",
	})
	require.True(t, result.Success, result.Error)
	require.Equal(t, 4, result.Data["selected_row_count"])
	require.Equal(t, 1, result.Data["calculated_row_count"])
	require.Len(t, result.Data["failed_rows"].([]any), 3, "ambiguous source tokens remain explicit failed rows")
	value := result.Data["calculations"].([]any)[0].(map[string]any)
	require.Equal(t, "1234.5", value["value"])
	require.Equal(t, "1,234.5", value["inputs"].(map[string]any)["x"].(map[string]any)["source_value"])
}

func TestDocumentTableMalformedPipeRowExplicitlyLimitsParsingAndCalculationCoverage(t *testing.T) {
	for _, malformed := range []string{"| Incomplete | 5 |", "| Unclosed | 5 | 6"} {
		source := "Unit: kg\n| Label | Before | After |\n| --- | --- | --- |\n| First | 10 | 12 |\n" + malformed + "\n| Later | 4 | 3 |\n"
		tool := tableCalculationFixture(source)
		inspected := runTableCalculation(t, tool, map[string]any{"operation": "inspect", "chunk_id": "actual-chunk"})
		require.True(t, inspected.Success, inspected.Error)
		tables := inspected.Data["tables"].([]any)
		require.Len(t, tables, 1)
		view := tables[0].(map[string]any)
		require.Equal(t, 2, view["row_count"], "only the actual valid prefix has parsed indices")
		require.Equal(t, false, view["parse_complete"], "a following valid row cannot hide the malformed source row")
		require.Equal(t, false, view["rows_truncated"], "presentation clipping and source parsing completeness are separate")
		require.NotEmpty(t, view["parse_warning"])
		plan := tableCalculationPlan()
		plan["row_end"] = 1
		result := runTableCalculation(t, tool, plan)
		require.True(t, result.Success, result.Error)
		require.Equal(t, false, result.Data["parse_complete"])
		require.NotEmpty(t, result.Data["parse_warning"])
		require.Equal(t, 1, result.Data["selected_row_count"])
		require.Equal(t, 1, result.Data["calculated_row_count"])
		require.Contains(t, result.Data["notice"], "不能宣称原表完整覆盖")
		plan["row_end"] = 2
		require.False(t, runTableCalculation(t, tool, plan).Success, "unparsed later rows cannot acquire executable indices")
	}
	complete := documentSourceTables(genericSourceTable + "\nAn ordinary paragraph after the table.")
	require.Len(t, complete, 1)
	require.True(t, complete[0].ParseComplete)
	require.Empty(t, complete[0].ParseWarning)
}

func TestDocumentTableOCRRegressionUsesOneShortModelPlanForAllSourceRows(t *testing.T) {
	raw, err := os.ReadFile("testdata/document_ocr_regression.json")
	require.NoError(t, err)
	var fixture struct {
		Steps []types.AgentStep `json:"steps"`
	}
	require.NoError(t, json.Unmarshal(raw, &fixture))
	tool := newDocumentTableCalculationTool(func() []types.AgentStep { return fixture.Steps })
	chunks := fetchedDocumentChunks(fixture.Steps[0].ToolCalls[0])
	require.NotEmpty(t, chunks)
	plan := map[string]any{"operation": "calculate", "chunk_id": chunks[0].ID, "table_index": 0, "row_start": 3, "row_end": 20, "columns": map[string]any{"current": map[string]any{"column": 3, "unit": "千英亩"}, "previous": map[string]any{"column": 2, "unit": "千英亩"}}, "expression": "current/previous-1", "result_unit": "%"}
	result := runTableCalculation(t, tool, plan)
	require.True(t, result.Success, result.Error)
	require.Equal(t, 18, result.Data["calculated_row_count"])
	rows := result.Data["calculations"].([]any)
	require.Equal(t, "Alabama", rows[0].(map[string]any)["row_label"])
	require.Equal(t, "250/19", rows[0].(map[string]any)["exact_value"])
	require.Equal(t, "United States", rows[17].(map[string]any)["row_label"])
	encoded, _ := json.Marshal(plan)
	require.Less(t, len(encoded), 600, "source cells and quotes must be read by program, not repeated per row by model")
}
