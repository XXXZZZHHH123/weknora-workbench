package agent

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/structuredresult"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func v2ChartFixture(t *testing.T) types.ToolCall {
	t.Helper()
	raw, err := os.ReadFile("../structuredresult/testdata/pdf_sql_v2.json")
	require.NoError(t, err)
	var data map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&data))
	data["source_context"] = map[string]any{"knowledge_base_id": "kb-fixture"}
	return types.ToolCall{ID: "actual-v2-sql", Name: "structured_query_pdf_table", Result: &types.ToolResult{Success: true, Data: data}}
}

func TestChartResultV2VerifiedUnitAndFullVersionAuditSurviveMappingAndExecute(t *testing.T) {
	call := v2ChartFixture(t)
	source, err := checkedStructuredChartTable(call)
	require.NoError(t, err)
	require.Equal(t, "kg", source.Columns[1].Unit)
	require.Equal(t, "reviewed_plan_declaration", source.Columns[1].UnitMetadata.EvidenceLevel)
	require.Contains(t, source.Provenance, "bundle_sha256")
	require.Contains(t, source.Provenance, "plan_sha256")
	require.Contains(t, source.Provenance, "record_sha256")
	ctx := chartContext(t)
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}
	writer := &chartWriterFixture{}
	tool := newRenderChartTool(ctx, func() []types.AgentStep { return steps }, writer, "session", "message")
	sources, _ := chartSources(steps)
	require.Len(t, sources, 1)
	result := chartRun(t, tool, ctx, map[string]any{"operation": "map", "source_id": sources[0].ID, "mapping": map[string]any{"orientation": "columns", "x_index": 0, "y_indexes": []int{1}, "value_indexes": []int{0, 1}}})
	require.True(t, result.Success, result.Error)
	id := result.Data["source_id"].(string)
	mapped := tool.mapped[id].Table
	require.Equal(t, "kg", mapped.Columns[1].Unit)
	require.NotNil(t, mapped.Columns[1].UnitMetadata)
	require.Equal(t, sources[0].Rows, mapped.Rows)
	result = chartRun(t, tool, ctx, map[string]any{"operation": "render", "source_id": id, "chart_type": "line", "x_type": "category", "x_column": 0, "y_columns": []int{1}})
	require.True(t, result.Success, result.Error)
	require.Len(t, writer.requests, 1)
	require.Equal(t, mapped.Provenance, result.Data["provenance"])
	// A changed actual value is rejected by the contract before a mapped source
	// or old sealed ID can be reused, and no second image is saved.
	steps[0].ToolCalls[0].Result.Data["rows"].([]any)[0].([]any)[1] = "991"
	result = chartRun(t, tool, ctx, map[string]any{"operation": "render", "source_id": id, "chart_type": "line", "x_type": "category", "x_column": 0, "y_columns": []int{1}})
	require.False(t, result.Success)
	require.Len(t, writer.requests, 1)
}

func TestChartRejectsDeclaredBrokenV2RatherThanLegacyFallback(t *testing.T) {
	for _, field := range []string{"result_hash", "source_snapshot", "column_metadata"} {
		t.Run(field, func(t *testing.T) {
			call := v2ChartFixture(t)
			delete(call.Result.Data["result_contract"].(map[string]any), field)
			_, err := checkedStructuredChartTable(call)
			require.Error(t, err)
		})
	}
	call := v2ChartFixture(t)
	call.Result.Data["source_context"] = map[string]any{"knowledge_base_id": "other-kb"}
	_, err := checkedStructuredChartTable(call)
	require.ErrorContains(t, err, "scope_mismatch")
	_, err = structuredresult.ValidatePayload(call.Result.Data)
	require.Error(t, err)
	// Legacy results retain their prior validation behavior.
	delete(call.Result.Data, "result_contract")
	_, err = checkedStructuredChartTable(call)
	require.NoError(t, err)
}

func TestChartAuditProjectionBindsFullV2CoordinatesAndUnknownUnitLimitations(t *testing.T) {
	call := v2ChartFixture(t)
	contract, err := structuredresult.Parse(call.Result.Data)
	require.NoError(t, err)
	meta := map[string]any{"source": call.Result.Data["source"], "result_contract": contract, "row_source_coordinates": string(bytes.Repeat([]byte("coordinate "), 4000))}
	view, partial, err := chartAuditMetadata(meta)
	require.NoError(t, err)
	require.True(t, partial)
	require.Contains(t, view, contract.ResultHash)
	require.Contains(t, view, "row_source_coordinates")
	require.Contains(t, view, "sha256")
	require.LessOrEqual(t, len(view), 16*1024)
	// Unit evidence affects the seal even when values themselves are unchanged.
	a := chart.Table{Columns: []chart.Column{{Name: "x"}, {Name: "y", Unit: "kg", UnitMetadata: &contract.Columns[1].Unit}}, Rows: [][]string{{"A", "1"}}}
	require.NoError(t, a.Seal("bound"))
	before := a.ID
	a.Columns[1].UnitMetadata.Reason = "Additional actual evidence limit"
	require.NoError(t, a.Seal("bound"))
	require.NotEqual(t, before, a.ID)
}
