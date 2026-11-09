package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func tableViewFixture(content string) (*documentTableCalculationTool, *agenttools.ToolRegistry, *[]types.AgentStep) {
	steps := analysisSteps(content)
	registry := agenttools.NewToolRegistry()
	tool := newDocumentTableCalculationTool(func() []types.AgentStep { return steps }, registry)
	registry.RegisterTool(tool)
	return tool, registry, &steps
}

func selectTableViewPlan() map[string]any {
	return map[string]any{"operation": "select", "chunk_id": "actual-chunk", "table_index": 0, "row_start": 1, "row_end": 2, "selected_columns": []int{0, 2}}
}

func appendTableViewResult(steps *[]types.AgentStep, result *types.ToolResult) {
	*steps = append(*steps, types.AgentStep{ToolCalls: []types.ToolCall{{ID: "actual-table-view", Name: documentTableCalculationToolName, Result: result}}})
}

func TestDocumentTableViewSelectPreservesOriginalPrecisionMissingAndSafeLiteralText(t *testing.T) {
	source := "Unit: kg\n| Label | Other | Value |\n| --- | --- | --- |\n| Item\\|one | unused | 1,234.5000 |\n| **raw** <script> [link](https://source.example/a) | ignored |  |\n"
	tool, registry, steps := tableViewFixture(source)
	result := runTableCalculation(t, tool, selectTableViewPlan())
	require.True(t, result.Success, result.Error)
	snapshot := result.Data["view_snapshot"].(documentTableViewSnapshot)
	require.Equal(t, []string{"Item|one", "1,234.5000"}, snapshot.Rows[0])
	require.Equal(t, "", snapshot.Rows[1][1], "blank source values are never zero-filled")
	require.Equal(t, "**raw** <script> [link](https://source.example/a)", snapshot.Rows[1][0])
	require.Contains(t, snapshot.Markdown, `Item&vert;one`)
	require.Contains(t, snapshot.Markdown, `&ast;&ast;raw&ast;&ast; &lt;script&gt; &lbrack;link&rbrack;`)
	require.NotContains(t, snapshot.Markdown, "<script>")
	require.NotContains(t, snapshot.Markdown, "unused", "unselected first context rows must not be copied into the final table")
	require.Contains(t, snapshot.Markdown, `1,234.5000`)
	require.Contains(t, snapshot.Markdown, `<kb chunk_id="actual-chunk" />`)
	appendTableViewResult(steps, result)
	resolved, err := resolveDocumentTableView(registry, *steps, result.Data["view_ref"].(string))
	require.NoError(t, err)
	require.Equal(t, snapshot.Markdown, resolved.Markdown)
	resolved.Rows[0][1] = "tampered copy"
	again, err := resolveDocumentTableView(registry, *steps, result.Data["view_ref"].(string))
	require.NoError(t, err)
	require.Equal(t, "1,234.5000", again.Rows[0][1])
}

func TestDocumentTableViewQuotesDoNotInventNumericEntitiesInFinalAnswer(t *testing.T) {
	tool, _, steps := tableViewFixture("| Label | Value |\n| --- | --- |\n| \"quoted\" and 'single' | 12 |\n")
	plan := selectTableViewPlan()
	plan["row_end"], plan["selected_columns"] = 1, []int{0, 1}
	result := runTableCalculation(t, tool, plan)
	require.True(t, result.Success, result.Error)
	snapshot := result.Data["view_snapshot"].(documentTableViewSnapshot)
	require.Contains(t, snapshot.Markdown, "&quot;quoted&quot;")
	require.Contains(t, snapshot.Markdown, "&apos;single&apos;")
	require.NotContains(t, snapshot.Markdown, "&#34;")
	require.NotContains(t, snapshot.Markdown, "&#39;")
	require.Empty(t, ungroundedStructuredNumbers(snapshot.Markdown, nil, *steps), "escaping must pass the existing unmodified numeric authority check")
}

func TestDocumentTableViewSelectRetainsRawJSONCellsWithoutAuthorizingNumericCalculations(t *testing.T) {
	tool, _, _ := tableViewFixture("Unit: kg\n| Label | Amount |\n| --- | --- |\n| actual | {\"value\":12} |\n")
	plan := selectTableViewPlan()
	plan["row_end"], plan["selected_columns"] = 1, []int{0, 1}
	result := runTableCalculation(t, tool, plan)
	require.True(t, result.Success, result.Error)
	require.Equal(t, `{"value":12}`, result.Data["view_snapshot"].(documentTableViewSnapshot).Rows[0][1])
	calculation := map[string]any{"operation": "calculate", "chunk_id": "actual-chunk", "table_index": 0, "row_start": 1, "row_end": 1, "columns": map[string]any{"a": map[string]any{"column": 1, "unit": "kg"}}, "expression": "a", "result_unit": ""}
	require.False(t, runTableCalculation(t, tool, calculation).Success, "original JSON exclusion in the numeric parser remains unchanged")
	for _, source := range []string{"```\n" + genericSourceTable + "\n```", "<tool_result>" + genericSourceTable + "</tool_result>", "<assistant>" + genericSourceTable + "</assistant>"} {
		tool, _, _ := tableViewFixture(source)
		require.False(t, runTableCalculation(t, tool, selectTableViewPlan()).Success)
	}
}

func TestDocumentTableViewSelectRejectsGuessedIndicesFormulasAndBudgets(t *testing.T) {
	for _, mutation := range []func(map[string]any){
		func(p map[string]any) { p["chunk_id"] = "foreign" },
		func(p map[string]any) { p["table_index"] = 9 },
		func(p map[string]any) { p["selected_columns"] = []int{0, 99} },
		func(p map[string]any) { p["selected_columns"] = []int{0, 0} },
		func(p map[string]any) { p["row_end"] = 201 },
		func(p map[string]any) { p["row_indices"] = []int{1} },
		func(p map[string]any) { p["expression"] = "1" },
		func(p map[string]any) { p["source_fingerprint"] = strings.Repeat("0", 64) },
	} {
		tool, _, _ := tableViewFixture(genericSourceTable)
		plan := selectTableViewPlan()
		mutation(plan)
		require.False(t, runTableCalculation(t, tool, plan).Success)
	}
	tool, _, _ := tableViewFixture(genericSourceTable)
	plan := selectTableViewPlan()
	delete(plan, "row_start")
	delete(plan, "row_end")
	plan["row_indices"] = []int{2, 1}
	result := runTableCalculation(t, tool, plan)
	require.True(t, result.Success, result.Error)
	require.Equal(t, []int{2, 1}, result.Data["view_snapshot"].(documentTableViewSnapshot).RowIndices, "only the explicit model-selected ordering is used")
}

func TestDocumentTableViewSnapshotRejectsUnknownTamperedFailedOrChangedSource(t *testing.T) {
	for _, tamper := range []string{"unknown", "snapshot", "failed", "source", "new-version"} {
		t.Run(tamper, func(t *testing.T) {
			tool, registry, steps := tableViewFixture(genericSourceTable)
			result := runTableCalculation(t, tool, selectTableViewPlan())
			require.True(t, result.Success, result.Error)
			appendTableViewResult(steps, result)
			ref := result.Data["view_ref"].(string)
			switch tamper {
			case "unknown":
				ref = documentTableViewPrefix + strings.Repeat("0", 64)
			case "snapshot":
				snapshot := result.Data["view_snapshot"].(documentTableViewSnapshot)
				snapshot.Rows[0][1] = "123456789"
				result.Data["view_snapshot"] = snapshot
			case "failed":
				result.Success = false
			case "source":
				(*steps)[0].ToolCalls[0].Result.Success = false
			case "new-version":
				updated := analysisSteps(strings.ReplaceAll(genericSourceTable, "12", "15"))
				*steps = append(*steps, updated...)
			}
			_, err := resolveDocumentTableView(registry, *steps, ref)
			require.Error(t, err)
		})
	}
}

func TestDocumentTableViewSameVersionContentSegmentsRetainDistinctSourceFingerprints(t *testing.T) {
	tool, registry, steps := tableViewFixture(genericSourceTable)
	origin := sha256.Sum256([]byte(genericSourceTable))
	row := (*steps)[0].ToolCalls[0].Result.Data["chunks"].([]any)[0].(map[string]any)
	row["content_revision"] = 3
	result := runTableCalculation(t, tool, selectTableViewPlan())
	require.True(t, result.Success, result.Error)
	appendTableViewResult(steps, result)
	segmented := analysisSteps("A different page segment of the same source revision.")
	segmentRow := segmented[0].ToolCalls[0].Result.Data["chunks"].([]any)[0].(map[string]any)
	segmentRow["content_revision"] = 3
	segmentRow["content_source_view"] = map[string]any{"origin_sha256": hex.EncodeToString(origin[:]), "origin_revision": 3}
	*steps = append(*steps, segmented...)
	resolved, err := resolveDocumentTableView(registry, *steps, result.Data["view_ref"].(string))
	require.NoError(t, err, "a different clipped segment of the same actual origin/revision must not pretend the source changed")
	require.Contains(t, resolved.Markdown, "2024-01-02")
}

func TestDocumentTableCalculatedViewIncludesActualFailedRowsAndNestedAnalysisResolves(t *testing.T) {
	source := strings.Replace(genericSourceTable, "| Item\\|two | 4 | 3 |", "| Missing | | 3 |", 1)
	tool, registry, steps := tableViewFixture(source)
	result := runTableCalculation(t, tool, tableCalculationPlan())
	require.True(t, result.Success, result.Error)
	snapshot := result.Data["view_snapshot"].(documentTableViewSnapshot)
	require.Equal(t, "calculate", snapshot.Operation)
	require.Equal(t, 3, snapshot.ComputedColumnCount)
	require.Len(t, snapshot.Rows, 2)
	require.Len(t, snapshot.FailedRows, 1)
	require.Equal(t, "", snapshot.Rows[1][3])
	require.Contains(t, snapshot.Rows[1][5], "空白或非数值未补零")
	*steps = append(*steps, types.AgentStep{ToolCalls: []types.ToolCall{{Name: documentTableAnalysisToolName, Result: &types.ToolResult{Success: false, Data: map[string]any{"kind": "document_table_analysis", "status": "partial", "executions": []map[string]any{{"success": true, "tool_name": documentTableCalculationToolName, "result": result.Data}}}}}}})
	resolved, err := resolveDocumentTableView(registry, *steps, result.Data["view_ref"].(string))
	require.NoError(t, err, "successful nested child snapshot survives canonical typed/map JSON representation and explicit partial parent")
	require.Equal(t, snapshot.Markdown, resolved.Markdown)
}

func TestDocumentTableAnalysisSharesRegisteredViewStore(t *testing.T) {
	_, registry, steps := tableViewFixture(analysisTableFixture)
	analysis := newDocumentTableAnalysisTool(func() []types.AgentStep { return *steps }, func() string { return "Compute the requested source changes." }, analysisPlannerReply(analysisReadyPlan(t)), registry)
	result := runDocumentAnalysis(t, analysis, `{}`)
	require.True(t, result.Success, result.Error)
	*steps = append(*steps, types.AgentStep{ToolCalls: []types.ToolCall{{Name: documentTableAnalysisToolName, Result: result}}})
	child := result.Data["executions"].([]map[string]any)[0]["result"].(map[string]any)
	_, err := resolveDocumentTableView(registry, *steps, child["view_ref"].(string))
	require.NoError(t, err)
}

func TestDocumentTableAnalysisBindsCatalogViewRatherThanLaterSameChunkProjection(t *testing.T) {
	second := "| Label | Before | After |\n| --- | --- | --- |\n| Later view | 20 | 30 |\n"
	full := genericSourceTable + "\nSecond section\n" + second
	_, registry, allSteps := tableViewFixture(full)
	row := (*allSteps)[0].ToolCalls[0].Result.Data["chunks"].([]any)[0].(map[string]any)
	row["content_revision"] = 7
	readSteps := append([]types.AgentStep(nil), (*allSteps)...)
	projected := analysisSteps(second)
	projected[0].ToolCalls[0].Name = "search_knowledge"
	projectionRow := projected[0].ToolCalls[0].Result.Data["chunks"].([]any)[0].(map[string]any)
	origin := sha256.Sum256([]byte(full))
	start := strings.Index(full, second)
	projectionRow["content_revision"] = 7
	projectionRow["content_source_view"] = map[string]any{"origin_sha256": hex.EncodeToString(origin[:]), "origin_revision": 7, "origin_rune_count": len([]rune(full)), "ranges": []map[string]any{{"start": start, "end": start + len(second)}}}
	*allSteps = append(*allSteps, projected...)
	plan := tableCalculationPlan()
	plan["row_end"] = 1
	analysis := newDocumentTableAnalysisTool(func() []types.AgentStep { return readSteps }, func() string { return "Calculate the selected first source row." }, analysisPlannerReply(analysisReadyPlan(t, plan)), registry)
	result := runDocumentAnalysis(t, analysis, `{}`)
	require.True(t, result.Success, result.Error)
	child := result.Data["executions"].([]map[string]any)[0]["result"].(map[string]any)
	require.Equal(t, "20", child["calculations"].([]any)[0].(map[string]any)["value"], "model-selected catalog row computes from its actual full read, not the later projected table yielding50")
}

func TestDocumentTableViewPersistedMapSnapshotHasCanonicalDigest(t *testing.T) {
	tool, registry, steps := tableViewFixture(genericSourceTable)
	result := runTableCalculation(t, tool, selectTableViewPlan())
	require.True(t, result.Success, result.Error)
	encoded, err := json.Marshal(result.Data)
	require.NoError(t, err)
	var restored map[string]any
	require.NoError(t, json.Unmarshal(encoded, &restored))
	result.Data = restored
	appendTableViewResult(steps, result)
	_, err = resolveDocumentTableView(registry, *steps, result.Data["view_ref"].(string))
	require.NoError(t, err)
}

func TestDocumentTableViewCannotSurviveNarrowedActualSourceScope(t *testing.T) {
	tool, registry, steps := tableViewFixture(genericSourceTable)
	doc := &types.Knowledge{ID: "actual-document", Title: "Source", FileName: "source.pdf", TenantID: 7, KnowledgeBaseID: "granted-base"}
	read := agenttools.NewReadDocumentTool(&plannedDocumentKnowledge{document: doc}, &plannedDocumentChunks{repo: &plannedDocumentRepo{}}, types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "granted-base", TenantID: 7}})
	registry.RegisterTool(read)
	agenttools.PrepareDocumentScope(registry, "Only physical page 1.")
	binder, err := registry.GetTool(agenttools.ToolBindDocumentScope)
	require.NoError(t, err)
	bind := func(source string) {
		result, err := binder.Execute(context.Background(), json.RawMessage(`{"selection_reason":"actual model source plan","sources":[`+source+`]}`))
		require.NoError(t, err)
		require.True(t, result.Success, result.Error)
	}
	bind(`{"knowledge_id":"actual-document","expected_title":"Source"}`)
	(*steps)[0].ToolCalls[0].Result.Data["document_scope"] = map[string]any{"matched": true, "knowledge_id": "actual-document", "page": 2}
	result := runTableCalculation(t, tool, selectTableViewPlan())
	require.True(t, result.Success, result.Error)
	appendTableViewResult(steps, result)
	_, err = resolveDocumentTableView(registry, *steps, result.Data["view_ref"].(string))
	require.NoError(t, err)
	bind(`{"knowledge_id":"actual-document","expected_title":"Source","user_page_constraint":{"pages":[1],"user_quote":"Only physical page 1."}}`)
	_, err = resolveDocumentTableView(registry, *steps, result.Data["view_ref"].(string))
	require.Error(t, err, "an actual source rebind must invalidate out-of-scope cached table delivery")
}

func TestDocumentTableCalculatedViewFailureDiagnosticDoesNotInventColumnNumbers(t *testing.T) {
	content := "Unit: kg\n| Label | A | B | C | D | Amount |\n| --- | --- | --- | --- | --- | --- |\n| Valid | 10 | 20 | 30 | 40 | 50 |\n| Missing | 10 | 20 | 30 | 40 | |\n"
	tool, _, steps := tableViewFixture(content)
	plan := map[string]any{"operation": "calculate", "chunk_id": "actual-chunk", "table_index": 0, "row_start": 1, "row_end": 2, "columns": map[string]any{"a": map[string]any{"column": 5, "unit": "kg"}}, "expression": "a", "result_unit": ""}
	result := runTableCalculation(t, tool, plan)
	require.True(t, result.Success, result.Error)
	appendTableViewResult(steps, result)
	snapshot := result.Data["view_snapshot"].(documentTableViewSnapshot)
	require.Contains(t, snapshot.FailedRows[0]["error"], "列5", "full technical diagnostics remain auditable")
	require.Empty(t, ungroundedStructuredNumbers(snapshot.Markdown, nil, *steps), "column indices in technical diagnostics are not observed business values")
}
