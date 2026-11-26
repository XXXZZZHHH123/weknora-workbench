package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/modelcontext"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func wideItemCatalog(t *testing.T, operation string, count, offset, total int) *types.ToolResult {
	t.Helper()
	key := "series"
	rows := []map[string]any{}
	for index := 0; index < count; index++ {
		row := map[string]any{"series_id": fmt.Sprintf("field-%03d", offset+index), "metric": "Repeated original label", "sheet": "Original sheet", "unit": "kg", "unit_status": "declared", "date_mode": "date", "first_date": "2020-01-01", "last_valid_date": "2020-04-01", "header_cells": []string{"B2"}, "description": strings.Repeat("Original metadata detail ", 1000)}
		if operation == "list_charts" {
			key = "charts"
			row = map[string]any{"chart_id": fmt.Sprintf("figure-%03d", offset+index), "title": "Repeated original title", "sheet": "Original figures", "part": fmt.Sprintf("xl/charts/chart%d.xml", index+1), "status": "linked", "axis_titles": []string{"kg"}, "axes": []map[string]any{{"id": "original-axis", "position": "l", "title": "kg"}}, "curves": []map[string]any{{"label": "Original legend", "reason": strings.Repeat("Repeated original detail ", 1000)}}}
		}
		rows = append(rows, row)
	}
	var next any
	if offset+count < total {
		next = offset + count
	}
	data := map[string]any{"dataset_id": "actual-dataset", key: rows, "total": total, "offset": offset, "next_offset": next, "truncated": next != nil}
	output, err := json.Marshal(data)
	require.NoError(t, err)
	return &types.ToolResult{Success: true, Data: data, Output: string(output)}
}

func TestStructuredChartDirectoryBriefPreservesEveryIdentityBeforeDetails(t *testing.T) {
	const budget = 6000
	original := wideItemCatalog(t, "list_charts", 10, 0, 10)
	before, _ := json.Marshal(original.Data)
	projected := fitStructuredDirectory(WithOutputBudget(t.Context(), budget), "list_charts", original)
	view := structuredDirectoryView(t, projected, budget)
	charts := structuredRecallRows(view["charts"])
	require.Len(t, charts, 10, "large earlier curve details cannot hide the last chart")
	for index, row := range charts {
		require.Equal(t, fmt.Sprintf("figure-%03d", index), row["chart_id"])
		require.Equal(t, "Repeated original title", row["title"], "identical titles do not select or merge sources")
		require.Equal(t, "kg", row["axes"].([]any)[0].(map[string]any)["title"])
		require.Equal(t, json.Number(fmt.Sprint(index)), row["catalog_offset"])
		require.NotContains(t, row, "curves", "omitted detail is explicitly declared, not cut mid-record")
	}
	require.Equal(t, false, view["truncated"], "all identities in the executed page remain visible")
	require.Nil(t, view["next_offset"])
	require.Equal(t, true, view["output_truncated"], "detail omission is distinct from item pagination")
	require.Equal(t, false, view["source_data_read"])
	require.Equal(t, true, view["directory_projection"].(map[string]any)["details_omitted"])
	require.NotContains(t, projected.Output, "get_chart")
	require.NotContains(t, projected.Output, "... [output truncated:")
	after, _ := json.Marshal(projected.Data)
	require.Equal(t, before, after, "canonical source metadata and identities remain unchanged")
}

func TestStructuredSeriesBriefPaginationNeverSkipsOmittedFields(t *testing.T) {
	const total, start, budget = 23, 7, 1900
	seen := []string{}
	for offset := start; offset < total; {
		original := wideItemCatalog(t, "list_series", min(8, total-offset), offset, total)
		before, _ := json.Marshal(original.Data)
		projected := fitStructuredDirectory(WithOutputBudget(t.Context(), budget), "list_series", original)
		view := structuredDirectoryView(t, projected, budget)
		rows := structuredRecallRows(view["series"])
		require.NotEmpty(t, rows)
		require.Less(t, len(rows), 8)
		require.Equal(t, float64(total), view["total"])
		for _, row := range rows {
			seen = append(seen, row["series_id"].(string))
			require.Equal(t, "kg", row["unit"])
			require.Equal(t, "declared", row["unit_status"])
			require.Equal(t, "2020-04-01", row["last_valid_date"])
		}
		next := offset + len(rows)
		if next < total {
			require.Equal(t, true, view["truncated"])
			require.Equal(t, float64(next), view["next_offset"], "continue at first omitted entry, not the service's later page")
		} else {
			require.Nil(t, view["next_offset"])
		}
		after, _ := json.Marshal(projected.Data)
		require.Equal(t, before, after)
		offset = next
	}
	require.Len(t, seen, total-start)
	for index, id := range seen {
		require.Equal(t, fmt.Sprintf("field-%03d", start+index), id)
	}
}

func TestStructuredSeriesBriefKeepsFormulaSamplesInRemainingBudget(t *testing.T) {
	const offset, count, total = 7, 8, 23
	const formula = "=VLOOKUP(A8,'原始记录'!A:B,2,0)"
	bare := wideItemCatalog(t, "list_series", count, offset, total)
	limited := fitStructuredDirectory(WithOutputBudget(t.Context(), 1900), "list_series", bare)
	tightBudget := utf8.RuneCountInString(limited.Output)

	original := wideItemCatalog(t, "list_series", count, offset, total)
	rows := original.Data["series"].([]map[string]any)
	references := []map[string]any{
		{
			"sheet": "计算视图", "series_id": "referencing-column", "metric": "出厂读数",
			"headers": []map[string]any{{"cell": "B2", "value": "出厂读数"}},
			"cell":    "B8", "formula": formula, "date": "2020-03-31", "kind": "vlookup",
			"target_column": "B", "target_reference": "A:B", "file_version": "private-version-detail",
		},
		{
			"sheet": "交接记录", "series_id": "handoff-column", "metric": "交接读数",
			"headers": []map[string]any{{"cell": "D2", "value": "交接读数"}},
			"cell":    "D9", "formula": "='原始记录'!B9", "kind": "direct", "target_column": "B", "target_reference": "B9",
		},
	}
	rows[0]["source_context"] = map[string]any{
		"formula_source": "unrelated-formula-context",
		"referenced_by":  append([]map[string]any{{"sheet": "Malformed", "formula": "=A1"}}, references...),
	}
	rows[1]["source_context"] = map[string]any{"formula_source": "unrelated-outbound-formula"}
	serviceOutput, err := json.Marshal(original.Data)
	require.NoError(t, err)
	original.Output = string(serviceOutput)
	original.Data["source_context"] = map[string]any{
		"knowledge_base_id": "private-library", "knowledge_base_tenant_id": 7,
	}
	before, err := json.Marshal(original.Data)
	require.NoError(t, err)

	for _, tc := range []struct {
		name   string
		budget int
		sample bool
	}{{"room for evidence", 4200, true}, {"only room for existing page", tightBudget, false}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := WithOutputBudget(t.Context(), tc.budget)
			baseline := structuredDirectoryView(t, fitStructuredDirectory(ctx, "list_series", bare), tc.budget)
			projected := fitStructuredDirectory(ctx, "list_series", original)
			view := structuredDirectoryView(t, projected, tc.budget)
			if !tc.sample {
				require.Equal(t, limited.Output, projected.Output, "oversized reference group leaves the whole original page intact")
			}
			for _, key := range []string{"offset", "total", "next_offset", "truncated", "directory_projection"} {
				require.Equal(t, baseline[key], view[key], key)
			}
			visible := modelcontext.NewRegistry(true).ModelToolResultForTool("structured_list_series", projected)
			var modelView map[string]any
			require.NoError(t, json.Unmarshal([]byte(visible), &modelView))
			require.NotContains(t, modelView, "source_context", "runtime context stays private")
			require.NotContains(t, visible, "private-library")
			require.NotContains(t, visible, "unrelated-formula-context")
			require.NotContains(t, visible, "private-version-detail")
			shown := structuredRecallRows(modelView["series"])
			want := structuredRecallRows(baseline["series"])
			require.NotEmpty(t, shown)
			require.Len(t, shown, len(want))
			for i, row := range shown {
				require.Equal(t, want[i]["series_id"], row["series_id"])
				require.Equal(t, want[i]["catalog_offset"], row["catalog_offset"])
				if i != 0 || !tc.sample {
					require.NotContains(t, row, "source_context")
					continue
				}
				context := row["source_context"].(map[string]any)
				samples := structuredRecallRows(context["referenced_by"])
				require.Len(t, samples, len(references))
				for j, reference := range references {
					for _, field := range []string{"sheet", "series_id", "metric", "cell", "formula", "date", "kind", "target_column", "target_reference"} {
						require.Equal(t, reference[field], samples[j][field], field)
					}
					require.Equal(t, structuredRecallRows(reference["headers"]), structuredRecallRows(samples[j]["headers"]))
				}
			}
			after, err := json.Marshal(projected.Data)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestStructuredItemDirectoriesKeepProjectionThroughNativeRegistryAndModel(t *testing.T) {
	for _, operation := range []string{"list_charts", "list_series"} {
		t.Run(operation, func(t *testing.T) {
			budget := 3500
			service := wideItemCatalog(t, operation, 10, 0, 10)
			key := "series"
			if operation == "list_charts" {
				key = "charts"
			} else {
				budget = 1900
			}
			calls := 0
			base := nativeReadTestTool(t, func(w http.ResponseWriter, request *http.Request) {
				calls++
				require.Equal(t, "allowed-kb", decodeStructuredScopeIdentity(t, request)["knowledge_base_id"])
				require.NoError(t, json.NewEncoder(w).Encode(service.Data))
			}).WithSearchTargets(types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "allowed-kb", TenantID: 7}})
			registry := NewToolRegistry()
			registry.SetMaxToolOutputSize(budget)
			directory := map[string]any{"tools": []any{map[string]any{"name": operation, "description": "Actual catalog", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}}
			for _, tool := range NewStructuredReadOperationTools(base, directory) {
				registry.RegisterTool(tool)
			}
			result, err := registry.ExecuteTool(structuredScopeContext(), "structured_"+operation, json.RawMessage(`{}`))
			require.NoError(t, err)
			require.True(t, result.Success)
			require.Equal(t, 1, calls)
			view := structuredDirectoryView(t, result, budget)
			require.NotContains(t, result.Output, "... [output truncated:")
			require.Len(t, structuredRecallRows(result.Data[key]), 10)
			require.Equal(t, false, result.Data["truncated"])
			require.Nil(t, result.Data["next_offset"])
			visible := modelcontext.NewRegistry(true).ModelToolResultForTool("structured_"+operation, result)
			var modelView map[string]any
			require.NoError(t, json.Unmarshal([]byte(visible), &modelView))
			coverage := modelView["discovery_coverage"].(map[string]any)
			require.Equal(t, view["next_offset"], coverage["next_offset"])
			require.Equal(t, view["truncated"], coverage["truncated"])
			require.Equal(t, true, coverage["output_truncated"])
			require.Equal(t, false, coverage["source_pagination"].(map[string]any)["truncated"])
			require.Len(t, structuredRecallRows(modelView[key]), len(structuredRecallRows(view[key])))
			require.NotContains(t, visible, "source_context")
			if operation == "list_series" {
				require.Less(t, len(structuredRecallRows(view[key])), 10)
				require.Equal(t, true, coverage["truncated"], "canonical complete page cannot erase model omission")
			}
		})
	}
}

func TestStructuredItemDirectorySmallResponsesAndOtherOperationsStayUnchanged(t *testing.T) {
	for _, operation := range []string{"list_charts", "list_series"} {
		original := &types.ToolResult{Success: true, Output: `{"charts":[],"truncated":false,"next_offset":null}`, Data: map[string]any{"charts": []any{}}}
		require.Same(t, original, fitStructuredDirectory(WithOutputBudget(t.Context(), 500), operation, original))
		original.Success = false
		require.Same(t, original, fitStructuredDirectory(WithOutputBudget(t.Context(), 10), operation, original))
	}
	query := wideItemCatalog(t, "list_series", 1, 0, 1)
	require.Same(t, query, fitStructuredDirectory(WithOutputBudget(t.Context(), 500), "query_series", query))
}

func TestStructuredSeriesBriefKeepsReviewSheetsAndDeclaredUnitOrigin(t *testing.T) {
	original := wideItemCatalog(t, "list_series", 1, 0, 1)
	original.Data["series"].([]map[string]any)[0]["field_metadata"] = map[string]any{
		"unit_source": "declared", "unit_evidence": []any{},
	}
	original.Data["series"].([]map[string]any)[0]["description"] = "单位为人工确认，未在原表找到对应文字"
	original.Data["notice"] = strings.Repeat("Verbose directory detail ", 1000)
	original.Data["catalog_summary"] = map[string]any{
		"unmapped_sheet_count": 1, "unmapped_sheets_truncated": false, "unmapped_sheet_limit": 80,
		"unmapped_sheets": []map[string]any{{"name": "Unmapped synthetic", "status": "needs_review",
			"reasons": []string{"Needs mapping review"}, "structured_query_available": false,
			"next_step": "字段映射待确认：不能用结构化查询取数，可用 inspect_source 读取原单元格"}},
	}
	before, err := json.Marshal(original.Data)
	require.NoError(t, err)
	original.Output = string(before)
	projected := fitStructuredDirectory(WithOutputBudget(t.Context(), 2200), "list_series", original)
	view := structuredDirectoryView(t, projected, 2200)
	rows := structuredRecallRows(view["series"])
	require.Len(t, rows, 1)
	require.Equal(t, "declared", rows[0]["field_metadata"].(map[string]any)["unit_source"])
	require.Contains(t, rows[0]["description"], "单位为人工确认")
	summary := view["catalog_summary"].(map[string]any)
	require.Equal(t, float64(1), summary["unmapped_sheet_count"])
	states := structuredRecallRows(summary["unmapped_sheets"])
	require.Len(t, states, 1)
	require.Equal(t, "Unmapped synthetic", states[0]["name"])
	require.Contains(t, states[0]["next_step"], "inspect_source")
	after, err := json.Marshal(projected.Data)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestStructuredItemDirectoryCannotCutAnOversizedIdentity(t *testing.T) {
	original := wideItemCatalog(t, "list_series", 1, 5, 9)
	rows := structuredRecallRows(original.Data["series"])
	rows[0]["series_id"] = strings.Repeat("real-identity", 1000)
	before, _ := json.Marshal(original.Data)
	projected := fitStructuredDirectory(WithOutputBudget(t.Context(), 700), "list_series", original)
	view := structuredDirectoryView(t, projected, 700)
	require.Equal(t, "directory_output_budget_exceeded", view["status"])
	require.Empty(t, structuredRecallRows(view["series"]))
	require.Equal(t, float64(5), view["next_offset"], "no source identity was displayed so the cursor cannot advance")
	require.Equal(t, true, view["truncated"])
	after, _ := json.Marshal(projected.Data)
	require.Equal(t, before, after)
}

func TestStructuredChartBriefPreservesEveryCurveOfCompositeFigures(t *testing.T) {
	original := wideItemCatalog(t, "list_charts", 10, 0, 10)
	curves := []map[string]any{}
	for index := 0; index < 8; index++ {
		curves = append(curves, map[string]any{"label": fmt.Sprintf("Printed legend %d", index), "chart_type": "lineChart", "axis": "primary", "unit": "kg", "values": []int{17, 23}, "reason": strings.Repeat("Original detail ", 100)})
	}
	// A duplicate printed label is still a separate series on its actual axis.
	curves = append(curves, map[string]any{"label": "Printed legend 0", "chart_type": "barChart", "axis": "secondary", "unit": nil, "values": []int{2, 4}})
	for _, row := range original.Data["charts"].([]map[string]any) {
		row["curves"] = curves
	}
	before, _ := json.Marshal(original.Data)
	original.Output = string(before)
	for _, budget := range []int{DefaultMaxToolOutput, 4000} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			projected := fitStructuredDirectory(WithOutputBudget(t.Context(), budget), "list_charts", original)
			view := structuredDirectoryView(t, projected, budget)
			charts := structuredRecallRows(view["charts"])
			require.NotEmpty(t, charts)
			if budget == DefaultMaxToolOutput {
				require.Len(t, charts, 10, "the last of ten composite charts retains its whole series overview")
				require.Equal(t, "figure-009", charts[9]["chart_id"])
				require.Equal(t, false, view["truncated"])
			} else {
				require.Less(t, len(charts), 10)
				require.Equal(t, float64(len(charts)), view["next_offset"], "omit entire charts, never part of a chart's series")
				require.Equal(t, true, view["truncated"])
			}
			for _, row := range charts {
				require.Equal(t, json.Number("9"), row["curve_count"])
				require.Equal(t, true, row["curve_summary_complete"])
				summary := structuredRecallRows(row["curve_summary"])
				require.Len(t, summary, len(curves))
				for index, curve := range summary {
					for _, field := range []string{"label", "chart_type", "axis", "unit"} {
						require.Equal(t, curves[index][field], curve[field])
					}
					require.NotContains(t, curve, "values", "catalog summaries cannot turn into source observations")
					require.NotContains(t, curve, "reason")
				}
			}
			require.Equal(t, false, view["source_data_read"])
			require.Contains(t, view["directory_projection"].(map[string]any)["curve_summary_scope"], "not observations")
			after, _ := json.Marshal(projected.Data)
			require.Equal(t, before, after)
		})
	}
}

func TestStructuredChartBriefDoesNotClaimUnknownCurveListsAreComplete(t *testing.T) {
	for _, curves := range []any{nil, "unavailable", []any{nil}} {
		original := wideItemCatalog(t, "list_charts", 1, 0, 1)
		original.Data["charts"].([]map[string]any)[0]["curves"] = curves
		projected := fitStructuredDirectory(WithOutputBudget(t.Context(), 2000), "list_charts", original)
		view := structuredDirectoryView(t, projected, 2000)
		row := structuredRecallRows(view["charts"])[0]
		require.Equal(t, false, row["curve_summary_complete"])
	}
}
