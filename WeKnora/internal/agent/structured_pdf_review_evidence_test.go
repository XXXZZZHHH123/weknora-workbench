package agent

import (
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestPDFQueryReviewPreservesExecutedSQLAndInputIdentity(t *testing.T) {
	state := &types.AgentState{RoundSteps: []types.AgentStep{{}}}
	messages := []chat.Message{}
	statements := []string{"SELECT period, amount FROM data WHERE period IN ('A','B')", "SELECT MAX(CASE WHEN period='B' THEN amount END)-MAX(CASE WHEN period='A' THEN amount END) AS delta FROM data"}
	for i, sql := range statements {
		data := map[string]any{"kind": "structured_analysis", "source_kind": "pdf_table", "status": "ok", "table_id": "pdft-fixture", "sql": sql, "columns": []any{"period", "amount"}, "rows": []any{[]any{"A", "10.00"}, []any{"B", "14.00"}},
			"input_complete": false, "input_count": 8, "output_truncated": false, "output_complete": true,
			"source":     map[string]any{"sha256": "immutable-source-fixture", "file_name": "fixture.pdf"},
			"provenance": map[string]any{"plan_sha256": "reviewed-plan-fixture", "table_record_sha256": "stored-record-fixture", "sql_result_lineage_verified": false, "scope": "all_loaded_inputs_before_user_sql"},
			"coverage":   map[string]any{"table_input_complete": false, "quality_issues": []any{map[string]any{"code": "historic_fixture", "message": strings.Repeat("historical diagnostic ", 800)}}}}
		name := "structured_query_pdf_table"
		args := map[string]any{"table_id": "pdft-fixture", "sql": sql}
		if i == 1 {
			name = "structured_data"
			args = map[string]any{"operation": "query_pdf_table", "arguments": args}
		}
		call, message := structuredQueryFixture(t, name, string(rune('a'+i)), args, data, true)
		state.RoundSteps[0].ToolCalls = append(state.RoundSteps[0].ToolCalls, call)
		messages = append(messages, message)
	}
	view := structuredQueryRowReviewEvidence(state, messages, structuredQueryRowReviewBudget)
	entries, size := decodeStructuredQueryCompanion(t, view)
	require.LessOrEqual(t, size, structuredQueryRowReviewBudget)
	require.Len(t, entries, 2)
	reader := newReviewEvidenceReader(messages)
	for i, entry := range entries {
		require.True(t, entry.ProjectionComplete)
		require.False(t, entry.MetadataProjectionComplete)
		require.Equal(t, statements[i], entry.Metadata["sql"])
		require.Equal(t, "pdft-fixture", entry.Metadata["table_id"])
		require.Equal(t, false, entry.Metadata["input_complete"], "bounded observations must never become complete-document coverage")
		provenance := entry.Metadata["provenance"].(map[string]any)
		require.Equal(t, "stored-record-fixture", provenance["table_record_sha256"])
		require.Equal(t, false, provenance["sql_result_lineage_verified"], "projection must not manufacture cell lineage")
		require.Equal(t, []string{"period", "amount"}, entry.Columns)
		require.Equal(t, 2, entry.RetainedRowCount)
		require.NoError(t, reviewWitnessVisible(map[string]any{"structured_query_row_companion": view}, reader, reviewSourceWitness{EvidenceID: entry.EvidenceID, JSONPointer: "/data/sql", Quote: statements[i], Kind: "source"}))
	}
	require.Contains(t, entries[0].MetadataOmittedPointers, "/data/coverage")
}

func TestPDFQueryReviewDoesNotMergeDistinctTables(t *testing.T) {
	state := &types.AgentState{RoundSteps: []types.AgentStep{{}}}
	messages := []chat.Message{}
	for _, id := range []string{"pdft-first", "pdft-second"} {
		data := map[string]any{"kind": "structured_analysis", "source_kind": "pdf_table", "status": "ok", "table_id": id, "sql": "SELECT amount FROM data", "columns": []any{"amount"}, "rows": []any{[]any{"4.25"}}, "input_complete": false, "output_truncated": false}
		call, message := structuredQueryFixture(t, "structured_query_pdf_table", id, map[string]any{"table_id": id, "sql": "SELECT amount FROM data"}, data, true)
		state.RoundSteps[0].ToolCalls = append(state.RoundSteps[0].ToolCalls, call)
		messages = append(messages, message)
	}
	view := structuredQueryRowReviewEvidence(state, messages, structuredQueryRowReviewBudget)
	entries, _ := decodeStructuredQueryCompanion(t, view)
	require.Len(t, entries, 2)
	require.NotEqual(t, entries[0].Metadata["table_id"], entries[1].Metadata["table_id"])
	require.NotEqual(t, entries[0].EvidenceID, entries[1].EvidenceID)
}
