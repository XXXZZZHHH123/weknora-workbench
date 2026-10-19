package agent

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestStructuredAnalysisCompanionPreservesSQLRowsAndOriginalPointers(t *testing.T) {
	for _, operation := range []string{"analyze_table", "get_result_evidence"} {
		t.Run(operation, func(t *testing.T) {
			rows := make([]any, 120)
			for i := range rows {
				rows[i] = []any{fmt.Sprintf("period-%03d", i), json.Number(fmt.Sprintf("%d.125", i)), fmt.Sprintf("D%d", i+2)}
			}
			data := map[string]any{"kind": "structured_analysis", "status": "ok", "columns": []string{"period", "value", "source_cell"}, "rows": rows,
				"output_truncated": false, "input_complete": true, "input_count": 147, "sql": "SELECT period,value,source_cell FROM data WHERE value IS NOT NULL"}
			call, message := structuredQueryFixture(t, "structured_data", "analysis-call", map[string]any{"operation": operation}, data, true)
			state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
			view := structuredQueryRowReviewEvidence(state, []chat.Message{message}, structuredQueryRowReviewBudget)
			entries, size := decodeStructuredQueryCompanion(t, view)
			require.LessOrEqual(t, size, structuredQueryRowReviewBudget)
			require.Len(t, entries, 1)
			require.True(t, entries[0].ProjectionComplete)
			require.Equal(t, "matrix", entries[0].RowEncoding)
			require.Equal(t, 120, entries[0].RetainedRowCount)
			require.Equal(t, float64(147), entries[0].Metadata["input_count"])
			require.True(t, entries[0].SourceTruncationKnown)
			require.Equal(t, false, entries[0].SourceResultTruncated)
			reader := newReviewEvidenceReader([]chat.Message{message})
			witness := reviewSourceWitness{EvidenceID: reviewEvidenceID(message), JSONPointer: "/data/rows/119/1", Quote: "119.125", Kind: "source"}
			require.NoError(t, reviewWitnessVisible(map[string]any{"structured_query_row_companion": view}, reader, witness))
			witness.JSONPointer = "/data/rows/119/value"
			require.Error(t, reviewWitnessVisible(map[string]any{"structured_query_row_companion": view}, reader, witness), "SQL aliases are not JSON object keys in matrix rows")
			witness.JSONPointer = "/data/rows/0/1"
			require.Error(t, reviewWitnessVisible(map[string]any{"structured_query_row_companion": view}, reader, witness), "another row cannot supply this value")
		})
	}
}

func TestStructuredAnalysisCompanionKeepsTruncationAndRejectsMalformedMatrix(t *testing.T) {
	data := map[string]any{"kind": "structured_analysis", "status": "ok", "columns": []string{"value"}, "rows": []any{[]any{7}}, "output_truncated": true}
	call, message := structuredQueryFixture(t, "structured_analyze_table", "sql", nil, data, true)
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
	view := structuredQueryRowReviewEvidence(state, []chat.Message{message}, structuredQueryRowReviewBudget)
	entries, _ := decodeStructuredQueryCompanion(t, view)
	require.Len(t, entries, 1)
	require.True(t, entries[0].ProjectionComplete, "all returned rows can be visible while SQL output was limited")
	require.Equal(t, true, entries[0].SourceResultTruncated)
	data["columns"] = []string{"value", "missing"}
	call, message = structuredQueryFixture(t, "structured_analyze_table", "sql-bad", nil, data, true)
	state.RoundSteps[0].ToolCalls = []types.ToolCall{call}
	require.Nil(t, structuredQueryRowReviewEvidence(state, []chat.Message{message}, structuredQueryRowReviewBudget))
}
