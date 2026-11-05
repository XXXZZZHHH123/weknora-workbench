package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/chart"
	"github.com/Tencent/WeKnora/internal/structuredresult"
	"github.com/stretchr/testify/require"
)

func TestV2NestedMapCompactsRepeatedEvidenceWithoutLosingCoordinatesOrUnits(t *testing.T) {
	call := v2ChartFixture(t)
	contract := call.Result.Data["result_contract"].(map[string]any)
	unit := contract["column_metadata"].([]any)[1].(map[string]any)["unit"].(map[string]any)
	one := unit["evidence"].([]any)[0]
	evidence := []any{}
	for i := 0; i < 64; i++ {
		evidence = append(evidence, one)
	}
	unit["evidence"] = evidence
	delete(contract, "result_hash")
	payload := map[string]any{}
	for _, raw := range contract["hash_input_keys"].([]any) {
		key := raw.(string)
		payload[key] = call.Result.Data[key]
	}
	digest, err := structuredresult.TypedSHA256(map[string]any{"payload": payload, "contract": contract})
	require.NoError(t, err)
	contract["result_hash"] = digest
	source, err := checkedStructuredChartTable(call)
	require.NoError(t, err)
	require.NoError(t, source.Seal(call.ID+"/structured"))
	originalID, originalRootHash := source.ID, chartMappingDigest([]byte(source.Provenance))
	x := 0
	mapping := chartSourceMapping{Orientation: "columns", XIndex: &x, YIndexes: []int{1}, ValueIndexes: []int{0, 1}}
	first, err := mapChartSource(source, mapping)
	require.NoError(t, err)
	second, err := mapChartSource(first, mapping)
	require.NoError(t, err)
	require.Equal(t, source.Rows, second.Rows)
	require.Equal(t, "kg", second.Columns[1].Unit)
	require.Len(t, second.Columns[1].UnitMetadata.Evidence, 64)
	require.LessOrEqual(t, len(first.Provenance), chartMappingMetadataBudget)
	require.LessOrEqual(t, len(second.Provenance), chartMappingMetadataBudget)
	require.Contains(t, second.Provenance, "projection_partial")
	require.Contains(t, second.Provenance, "complete_metadata_sha256")
	require.Contains(t, second.Notice, "受限投影")
	coordinates, ok := chartOriginalProjectionCoordinates(second.ID, second.Provenance, originalID, []chartProjectionCoordinate{{0, 0}, {0, 1}, {1, 0}, {1, 1}}, 0)
	require.True(t, ok)
	require.Equal(t, []chartProjectionCoordinate{{0, 0}, {0, 1}, {1, 0}, {1, 1}}, coordinates)
	var outer map[string]any
	require.NoError(t, json.Unmarshal([]byte(second.Provenance), &outer))
	require.Equal(t, first.ID, outer["source_id"])
	var parent, root map[string]any
	require.NoError(t, json.Unmarshal([]byte(outer["source_provenance"].(string)), &parent))
	require.NoError(t, json.Unmarshal([]byte(parent["source_provenance"].(string)), &root))
	require.Equal(t, originalID, root["root_source_id"])
	require.Equal(t, originalRootHash, root["root_provenance_sha256"])
	summary := root["result_contract_summary"].(map[string]any)
	require.Equal(t, digest, summary["result_hash"])
	require.Equal(t, "dependency_only", summary["evidence_capabilities"].(map[string]any)["level"])
	require.Contains(t, second.Provenance, "plan_sha256")
	require.Contains(t, second.Provenance, "bundle_sha256")
	// A change to an actual mapping or source value still produces another
	// sealed source, even though the optional audit view is compact.
	mapping.ValueIndexes = []int{1, 0}
	changed, err := mapChartSource(first, mapping)
	require.NoError(t, err)
	require.NotEqual(t, second.ID, changed.ID)
}

func TestMappingByteBudgetRejectsCompleteChainWithoutTruncatingCoordinates(t *testing.T) {
	source := chart.Table{Columns: []chart.Column{{Name: "Dimension"}, {Name: "Measured", Unit: "kg"}}, Rows: [][]string{{"A", "2"}, {"B", "3"}}, Provenance: `{"kind":"synthetic_scope","source":{"sha256":"` + strings.Repeat("a", 64) + `"}}`}
	require.NoError(t, source.Seal("synthetic-depth-only"))
	originalID := source.ID
	x := 0
	mapping := chartSourceMapping{Orientation: "columns", XIndex: &x, YIndexes: []int{1}, ValueIndexes: []int{0, 1}}
	for i := 0; i < chartMappingMaxDepth-1; i++ {
		next, err := mapChartSource(source, mapping)
		require.NoError(t, err, "level %d", i+1)
		source = next
	}
	// Repeated JSON-string escaping can exhaust bytes before the independent
	// depth limit. A complete seven-layer witness remains usable; the next
	// mapping must reject rather than omit coordinates or relax the budget.
	coordinates, ok := chartOriginalProjectionCoordinates(source.ID, source.Provenance, originalID, []chartProjectionCoordinate{{0, 0}, {0, 1}, {1, 0}, {1, 1}}, 0)
	require.True(t, ok)
	require.Equal(t, []chartProjectionCoordinate{{0, 0}, {0, 1}, {1, 0}, {1, 1}}, coordinates)
	_, err := mapChartSource(source, mapping)
	require.ErrorContains(t, err, "完整映射链超过元数据预算")
}

func TestMappingDepthIsBoundedWithoutTruncatingOrGuessingChain(t *testing.T) {
	// Exercise the depth guard separately from the byte budget. The nine-layer
	// input need not fit into a sealed Table: it must reject before compaction.
	layer := map[string]any{"kind": "synthetic_scope"}
	for i := 0; i < chartMappingMaxDepth; i++ {
		encoded, err := json.Marshal(layer)
		require.NoError(t, err)
		layer = map[string]any{"kind": "source_cell_projection", "source_provenance": string(encoded)}
	}
	depth, err := chartMappingDepth(layer, 0)
	require.NoError(t, err)
	require.Equal(t, chartMappingMaxDepth, depth)
	encoded, err := json.Marshal(layer)
	require.NoError(t, err)
	layer = map[string]any{"kind": "source_cell_projection", "source_provenance": string(encoded)}
	_, _, err = boundedChartMappingMetadata(layer)
	require.ErrorContains(t, err, "最多8层")
}
