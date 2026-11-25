package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image/png"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/structuredresult"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

const (
	firstContractID  = "fixture.close.CF01M.CZC"
	secondContractID = "fixture.close.CF03M.CZC"
)

func seriesBindingFixture(t *testing.T, variant string) types.ToolCall {
	t.Helper()
	first := map[string]any{"series_id": firstContractID, "metric": "收盘价", "dimensions": map[string]any{"block": "close", "contract": "CF01M.CZC"}}
	second := map[string]any{"series_id": secondContractID, "metric": "收盘价", "dimensions": map[string]any{"block": "close", "contract": "CF03M.CZC"}}
	expression := "series_id"
	operation := map[string]any{"kind": "column_reference", "names": []string{"series_id"}, "direct_source_column": "series_id"}
	rows := [][]any{{"2023-01", firstContractID, 12}, {"2023-01", secondContractID, 18}, {"2023-02", secondContractID, 20}, {"2023-02", firstContractID, 13}}
	unknown := structuredresult.Unit{Status: "unknown", BindingScope: "result_column", EvidenceLevel: "none", Evidence: []map[string]any{}}
	valueUnit := unknown
	switch variant {
	case "CASE":
		expression = "CASE WHEN value IS NOT NULL THEN series_id ELSE series_id END"
		operation = map[string]any{"kind": "unknown"}
	case "collision":
		first["display_name"], second["display_name"] = "同名合约", "同名合约"
	case "duplicate":
		rows = append(rows, []any{"2023-01", firstContractID, 19})
	case "unit":
		first["unit"], second["unit"] = "元", "吨"
		valueUnit.Status = "mixed"
	case "unmatched":
		rows[1][1] = "unknown.id"
	case "internal label":
		second["dimensions"] = map[string]any{"contract": secondContractID}
		second["metric"] = "收盘价 " + secondContractID
	}
	sourceHash := strings.Repeat("ab", 32)
	sql := fmt.Sprintf("WITH d AS (SELECT date, %s AS ref, value FROM data) SELECT strftime(date, '%%Y-%%m') AS month, ref AS label, avg(value) AS price FROM d GROUP BY 1, 2", expression)
	payload := map[string]any{
		"columns": []string{"month", "label", "price"}, "rows": rows, "sql": sql,
		"source": map[string]any{"file": "fixture.xlsx", "sha256": sourceHash}, "dataset_id": "test-dataset",
		"inputs":           map[string]any{firstContractID: map[string]any{"series": first}, secondContractID: map[string]any{"series": second}},
		"output_truncated": false,
	}
	contract := map[string]any{
		"schema_version": 2, "hash_algorithm": structuredresult.HashAlgorithm, "result_id": strings.Repeat("b", 24),
		"hash_input_keys": []string{"columns", "dataset_id", "inputs", "output_truncated", "rows", "source", "sql"},
		"source_snapshot": structuredresult.Snapshot{Kind: "excel_observations", SourceSHA256: sourceHash, File: "fixture.xlsx", DatasetID: "test-dataset",
			Versions: map[string]string{"manifest_sha256": sourceHash}, InputDigestSHA256: sourceHash, Scope: "loaded_inputs_before_user_sql"},
		"execution": structuredresult.Execution{Kind: "sql", Engine: "DuckDB", EngineVersion: "fixture", NumericSemantics: "DOUBLE",
			SQLSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(sql))), InputScope: "loaded_inputs_before_user_sql"},
		"evidence_capabilities": structuredresult.Capabilities{Level: "dependency_only", ColumnDependenciesVerified: true},
		"column_metadata": []structuredresult.Column{
			{Index: 0, Name: "month", DisplayName: "月份", LogicalType: "VARCHAR", Unit: unknown, Dependency: structuredresult.Dependency{Kind: "source_value", SourceColumns: []string{"date"}}},
			{Index: 1, Name: "label", DisplayName: "label", LogicalType: "VARCHAR", Unit: unknown, Dependency: structuredresult.Dependency{Kind: "source_value", SourceColumns: []string{"series_id"}, TechnicalMetadata: true}, Operation: operation},
			{Index: 2, Name: "price", DisplayName: "月均价", LogicalType: "DOUBLE", Unit: valueUnit, Dependency: structuredresult.Dependency{Kind: "source_value", SourceColumns: []string{"value"}}},
		},
	}
	// Digest the same JSON types that the transport delivers to the validator.
	raw, err := json.Marshal(map[string]any{"payload": payload, "contract": contract})
	require.NoError(t, err)
	var witness map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&witness))
	contract["result_hash"], err = structuredresult.TypedSHA256(witness)
	require.NoError(t, err)
	payload["kind"], payload["status"], payload["result_contract"] = "structured_analysis", "ok", contract
	return types.ToolCall{ID: "series-binding", Name: "structured_analyze_table", Result: &types.ToolResult{Success: true, Data: payload}}
}

func TestDirectSeriesProjectionUsesSourceContractNames(t *testing.T) {
	ctx := chartContext(t)
	source := seriesBindingFixture(t, "")
	tool, writer, _, args := monthlyRenderTool(t, ctx, source)
	args["series_column"], args["y_columns"] = 1, []int{2}
	result := chartRun(t, tool, ctx, args)
	require.True(t, result.Success, result.Error)
	require.Len(t, writer.requests, 1)
	_, err := png.Decode(bytes.NewReader(writer.requests[0].PNG))
	require.NoError(t, err)
	columns := result.Data["selected_columns"].([]chart.Column)
	require.Equal(t, []string{"月份", "CF01M.CZC", "CF03M.CZC"}, []string{columns[0].Label(), columns[1].Label(), columns[2].Label()})
	require.Equal(t, [][]string{{"2023-01", "12", "18"}, {"2023-02", "13", "20"}}, result.Data["selected_rows"])
	require.NotContains(t, result.Output, firstContractID)
	var proof struct {
		Bindings map[string]string `json:"series_label_bindings"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.Data["provenance"].(string)), &proof))
	require.Equal(t, map[string]string{firstContractID: "CF01M.CZC", secondContractID: "CF03M.CZC"}, proof.Bindings)
	// The direct ID still cannot serve as an axis.
	delete(args, "series_column")
	args["x_column"] = 1
	rejected := chartRun(t, tool, ctx, args)
	require.False(t, rejected.Success)
	require.Len(t, writer.requests, 1)
	// Editing the display metadata without the validated contract invalidates the source.
	source.Result.Data["inputs"].(map[string]any)[firstContractID].(map[string]any)["series"].(map[string]any)["display_name"] = "未核对的名称"
	_, err = checkedStructuredChartTable(source)
	require.ErrorContains(t, err, "hash_mismatch")
}

func TestDirectSeriesProjectionRejectsUnverifiedOrAmbiguousLabels(t *testing.T) {
	for variant, reason := range map[string]string{
		"CASE": "CASE技术ID", "collision": "图例名称相同", "duplicate": "存在多行",
		"unit": "混合单位", "unmatched": "找不到对应", "internal label": "找不到对应",
	} {
		t.Run(variant, func(t *testing.T) {
			ctx := chartContext(t)
			tool, writer, _, args := monthlyRenderTool(t, ctx, seriesBindingFixture(t, variant))
			args["series_column"], args["y_columns"] = 1, []int{2}
			result := chartRun(t, tool, ctx, args)
			require.False(t, result.Success)
			require.Contains(t, result.Error, reason)
			require.Empty(t, writer.requests)
		})
	}
}
