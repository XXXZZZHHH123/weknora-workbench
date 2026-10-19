package agent

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func reviewMatrixRowRefFixture(t *testing.T, retainedRows int) (*reviewEvidenceReader, map[string]any) {
	t.Helper()
	message := chat.Message{
		Role: "tool", Name: "structured_data", ToolCallID: "matrix-result",
		Content: `{"success":true,"data":{"operation":"analyze_table","kind":"structured_analysis","status":"ok","columns":["month","value"],"rows":[["2001-01",12.5],["2001-02",11.5]],"truncated":false}}`,
	}
	reader := newReviewEvidenceReader([]chat.Message{message})
	var companion any
	rows := `[["2001-01",12.5]]`
	omissions := `[{"json_pointer":"/data/rows","start_index":1,"end_index_exclusive":2}]`
	if retainedRows >= 2 {
		rows = `[["2001-01",12.5],["2001-02",11.5]]`
		omissions = `[]`
	}
	companionJSON := fmt.Sprintf(`{"projection_complete":%t,"entries":[{"evidence_id":%q,"source_json_pointer":"/data/rows","row_encoding":"matrix","columns":["month","value"],"rows":%s,"source_row_count":2,"retained_row_count":%d,"omitted_row_ranges":%s}]}`,
		retainedRows >= 2, reader.records[0].ID, rows, retainedRows, omissions)
	require.NoError(t, json.Unmarshal([]byte(companionJSON), &companion))
	return reader, map[string]any{"structured_query_row_companion": companion}
}

func decodeReviewRowRef(t *testing.T, raw string) reviewSourceWitness {
	t.Helper()
	var witness reviewSourceWitness
	require.NoError(t, json.Unmarshal([]byte(raw), &witness))
	return witness
}

func TestReviewReceiptRowRefBindsVisibleMatrixCellToExactSource(t *testing.T) {
	reader, input := reviewMatrixRowRefFixture(t, 1)
	witness := decodeReviewRowRef(t, `{"kind":"source","row_ref":{"entry_index":0,"row_index":0,"column":"value"}}`)
	resolved, err := resolveStructuredRowWitness(input, reader, witness)
	require.NoError(t, err)
	require.Equal(t, reader.records[0].ID, resolved.EvidenceID)
	require.Equal(t, "/data/rows/0/1", resolved.JSONPointer)
	require.Equal(t, "12.5", resolved.Quote)
	require.NoError(t, reviewWitnessVisible(input, reader, witness))
}

func TestReviewReceiptRowRefRejectsOmittedRowsAndBadSelectors(t *testing.T) {
	reader, input := reviewMatrixRowRefFixture(t, 1)
	for name, raw := range map[string]string{
		"omitted row":    `{"kind":"source","row_ref":{"entry_index":0,"row_index":1,"column":"value"}}`,
		"unknown column": `{"kind":"source","row_ref":{"entry_index":0,"row_index":0,"column":"missing"}}`,
		"unknown entry":  `{"kind":"source","row_ref":{"entry_index":1,"row_index":0,"column":"value"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, reviewWitnessVisible(input, reader, decodeReviewRowRef(t, raw)))
		})
	}
}

func TestReviewReceiptRowRefRejectsProjectionMismatchAndUntrustedRecords(t *testing.T) {
	witness := decodeReviewRowRef(t, `{"kind":"source","row_ref":{"entry_index":0,"row_index":0,"column":"value"}}`)
	t.Run("projection differs from immutable raw value", func(t *testing.T) {
		reader, input := reviewMatrixRowRefFixture(t, 1)
		view := input["structured_query_row_companion"].(map[string]any)
		entry := view["entries"].([]any)[0].(map[string]any)
		row := entry["rows"].([]any)[0].([]any)
		row[1] = json.Number("99.5")
		require.Error(t, reviewWitnessVisible(input, reader, witness))
	})
	t.Run("entry points to an unknown result", func(t *testing.T) {
		reader, input := reviewMatrixRowRefFixture(t, 1)
		view := input["structured_query_row_companion"].(map[string]any)
		entry := view["entries"].([]any)[0].(map[string]any)
		entry["evidence_id"] = "not-an-observed-result"
		require.Error(t, reviewWitnessVisible(input, reader, witness))
	})
	t.Run("failed original result", func(t *testing.T) {
		message := chat.Message{Role: "tool", Name: "structured_data", ToolCallID: "failed-matrix-result",
			Content: `{"success":false,"error":"query failed","data":{"operation":"analyze_table","kind":"structured_analysis","status":"ok","columns":["month","value"],"rows":[["2001-01",12.5]],"truncated":false}}`}
		reader := newReviewEvidenceReader([]chat.Message{message})
		input := map[string]any{"structured_query_row_companion": map[string]any{"entries": []any{map[string]any{
			"evidence_id": reader.records[0].ID, "source_json_pointer": "/data/rows", "row_encoding": "matrix",
			"columns": []any{"month", "value"}, "rows": []any{[]any{"2001-01", json.Number("12.5")}},
		}}}}
		require.Error(t, reviewWitnessVisible(input, reader, witness))
	})
}

func TestReviewReceiptRowRefRejectsDuplicateColumnNames(t *testing.T) {
	reader, input := reviewMatrixRowRefFixture(t, 1)
	view := input["structured_query_row_companion"].(map[string]any)
	entry := view["entries"].([]any)[0].(map[string]any)
	entry["columns"] = []any{"month", "value", "value"}
	entry["rows"] = []any{[]any{"2001-01", json.Number("12.5"), json.Number("12.5")}}
	witness := decodeReviewRowRef(t, `{"kind":"source","row_ref":{"entry_index":0,"row_index":0,"column":"value"}}`)
	require.Error(t, reviewWitnessVisible(input, reader, witness))
}

func TestReviewReceiptRowRefRequiresExclusiveSourceLocator(t *testing.T) {
	reader, input := reviewMatrixRowRefFixture(t, 1)
	for _, raw := range []string{
		`{"kind":"source","evidence_id":"x","row_ref":{"entry_index":0,"row_index":0,"column":"value"}}`,
		`{"kind":"source","json_pointer":"/data/rows/0/1","row_ref":{"entry_index":0,"row_index":0,"column":"value"}}`,
		`{"kind":"source","quote":"131.34","row_ref":{"entry_index":0,"row_index":0,"column":"value"}}`,
		`{"kind":"source","source_pointer_ref":0,"row_ref":{"entry_index":0,"row_index":0,"column":"value"}}`,
		`{"kind":"source","row_ref":{"row_index":0,"column":"value"}}`,
		`{"kind":"source","row_ref":{"entry_index":null,"row_index":0,"column":"value"}}`,
		`{"kind":"source","row_ref":{"entry_index":0,"row_index":null,"column":"value"}}`,
		`{"kind":"source","row_ref":{"entry_index":0,"row_index":0,"column":null}}`,
		`{"kind":"source","row_ref":{"entry_index":-1,"row_index":0,"column":"value"}}`,
		`{"kind":"source","row_ref":{"entry_index":0,"row_index":-1,"column":"value"}}`,
		`{"kind":"source","row_ref":{"entry_index":"","row_index":0,"column":"value"}}`,
		`{"kind":"source","row_ref":{"entry_index":0,"row_index":"","column":"value"}}`,
		`{"kind":"source","row_ref":{"entry_index":0,"row_index":0,"column":""}}`,
		`{"kind":"source","row_ref":{"entry_index":0,"column":"value"}}`,
	} {
		var witness reviewSourceWitness
		require.Error(t, json.Unmarshal([]byte(raw), &witness), raw)
	}
	execution := decodeReviewRowRef(t, `{"kind":"execution","row_ref":{"entry_index":0,"row_index":0,"column":"value"}}`)
	require.Error(t, reviewWitnessVisible(input, reader, execution))
	// Explicit locators retain the prior strict quote-to-pointer behavior.
	explicit := reviewSourceWitness{EvidenceID: reader.records[0].ID, JSONPointer: "/data/rows/0/1", Quote: "11.5", Kind: "source"}
	require.Error(t, reviewWitnessVisible(input, reader, explicit))
}

func TestReviewReceiptRowRefSupportsObjectAndPositionalCompanions(t *testing.T) {
	message := chat.Message{Role: "tool", Name: "structured_data", ToolCallID: "object-result",
		Content: `{"success":true,"data":{"operation":"query_series","status":"ok","rows":[{"month":"2001-01","va/l~ue":12.5}]}}`}
	reader := newReviewEvidenceReader([]chat.Message{message})
	entry := map[string]any{"evidence_id": reader.records[0].ID, "source_json_pointer": "/data/rows", "row_encoding": "objects",
		"columns": []any{"month", "va/l~ue"}, "rows": []any{map[string]any{"month": "2001-01", "va/l~ue": json.Number("12.5")}}}
	input := map[string]any{"structured_query_row_companion": map[string]any{"entries": []any{entry}}}
	entry["row_encoding"] = "objects"
	objectWitness := decodeReviewRowRef(t, `{"kind":"source","row_ref":{"entry_index":0,"row_index":0,"column":"va/l~ue"}}`)
	resolved, err := resolveStructuredRowWitness(input, reader, objectWitness)
	require.NoError(t, err)
	require.Equal(t, "/data/rows/0/va~1l~0ue", resolved.JSONPointer)
	require.NoError(t, reviewWitnessVisible(input, reader, objectWitness))

	// Positional companions retain only varying row columns and place constant
	// values in constant_fields; the selector maps that displayed value back to
	// the original row object key.
	message = chat.Message{Role: "tool", Name: "structured_data", ToolCallID: "positional-result",
		Content: `{"success":true,"data":{"operation":"query_series","status":"ok","rows":[{"month":"2001-01","value":12.5}]}}`}
	reader = newReviewEvidenceReader([]chat.Message{message})
	entry["evidence_id"] = reader.records[0].ID
	entry["row_encoding"] = "positional"
	entry["columns"] = []any{"month"}
	entry["constant_fields"] = map[string]any{"value": json.Number("12.5")}
	entry["rows"] = []any{[]any{"2001-01"}}
	input = map[string]any{"structured_query_row_companion": map[string]any{"entries": []any{entry}}}
	constantWitness := decodeReviewRowRef(t, `{"kind":"source","row_ref":{"entry_index":0,"row_index":0,"column":"value"}}`)
	resolved, err = resolveStructuredRowWitness(input, reader, constantWitness)
	require.NoError(t, err)
	require.Equal(t, "/data/rows/0/value", resolved.JSONPointer)
	require.NoError(t, reviewWitnessVisible(input, reader, constantWitness))
	entry["columns"] = []any{"month", "value"}
	require.Error(t, reviewWitnessVisible(input, reader, constantWitness), "constant and positional column mappings cannot be ambiguous")
}
