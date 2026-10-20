package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// Synthetic rows; whole-row witnesses must bind to the exact displayed row.
func TestRowCompanionExposesWholeDisplayedRowOnly(t *testing.T) {
	content := `{"success":true,"data":{"rows":[{"date":"2000-01-31","series":"s","value":1.5},{"date":"2000-02-29","series":"s","value":2.5},{"date":"2000-03-31","series":"s","value":3.5}]}}`
	record, reader, _ := receiptFixture(t, "rows-call", content)
	companion := map[string]any{"entries": []any{map[string]any{
		"evidence_id": record.ID, "source_json_pointer": "/data/rows", "row_encoding": "positional",
		"constant_fields": map[string]any{"series": "s"}, "columns": []any{"date", "value"},
		"rows": []any{[]any{"2000-01-31", json.Number("1.5")}, []any{"2000-02-29", json.Number("2.5")}},
	}}}
	input := map[string]any{"source_tool_results": []any{}, "structured_query_row_companion": companion}
	// Omitted quote: the server derives the exact row from the immutable source.
	require.NoError(t, reviewWitnessVisible(input, reader, reviewSourceWitness{EvidenceID: record.ID, JSONPointer: "/data/rows/1", Kind: "source"}))
	// A row beyond the displayed projection is not exposed.
	require.Error(t, reviewWitnessVisible(input, reader, reviewSourceWitness{EvidenceID: record.ID, JSONPointer: "/data/rows/2", Kind: "source"}))
	// A tampered display of the same index cannot lend support.
	companion["entries"].([]any)[0].(map[string]any)["rows"] = []any{[]any{"2000-01-31", json.Number("1.5")}, []any{"2000-02-29", json.Number("9.9")}}
	require.Error(t, reviewWitnessVisible(input, reader, reviewSourceWitness{EvidenceID: record.ID, JSONPointer: "/data/rows/1", Kind: "source"}))
}
