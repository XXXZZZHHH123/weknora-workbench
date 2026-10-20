package agent

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func TestPresentNullSourceWitnessSupportsObservedMissingUnit(t *testing.T) {
	record, reader, raw := receiptFixture(t, "null-unit", `{"success":true,"data":{"rows":[["2001-01",12.5,null]],"operation":"analyze_table"}}`)
	input := receiptVisibleInput(record, raw)
	input["structured_query_row_companion"] = map[string]any{
		"projection_complete": true,
		"entries": []any{map[string]any{
			"entry_index": 0, "evidence_id": record.ID, "source_json_pointer": "/data/rows",
			"row_encoding": "matrix", "columns": []any{"period", "value", "unit"},
			"rows": []any{[]any{"2001-01", json.Number("12.5"), nil}},
		}},
	}
	snapshot := &sourceReviewSnapshot{sourceRecords: reader, input: input}
	witnessJSON := `{"kind":"source","row_ref":{"entry_index":0,"row_index":0,"column":"unit"}}`
	var witness reviewSourceWitness
	require.NoError(t, json.Unmarshal([]byte(witnessJSON), &witness))

	limitation := reviewClaimCheck{
		Claim: "The returned unit cell is null, so the result does not provide a unit label.",
		Basis: "limitation", Assessment: "The exact displayed unit cell is present and null.",
		Evidence: []reviewSourceWitness{witness},
	}
	require.NoError(t, validateReviewReceipt("The value is 12.5; no unit was supplied.", "The result directly supplies a value and leaves its unit unspecified.", []reviewClaimCheck{limitation}, snapshot))

	sourceClaim := limitation
	sourceClaim.Basis = "source"
	require.NoError(t, validateReviewReceipt("The returned unit field is null.", "The exact unit field is present and unspecified.", []reviewClaimCheck{sourceClaim}, snapshot))
}

func TestPresentNullOmittedPointerRejectsMissingHiddenFailedAndMismatchedValues(t *testing.T) {
	t.Run("visible exact null pointer", func(t *testing.T) {
		record, reader, raw := receiptFixture(t, "null-visible", `{"success":true,"data":{"unit":null,"value":12.5}}`)
		witness := decodeOmittedQuoteWitness(t, record.ID, "/data/unit")
		require.NoError(t, reviewWitnessVisible(receiptVisibleInput(record, raw), reader, witness))
	})
	t.Run("missing is not null", func(t *testing.T) {
		record, reader, raw := receiptFixture(t, "null-missing", `{"success":true,"data":{"value":12.5}}`)
		witness := decodeOmittedQuoteWitness(t, record.ID, "/data/unit")
		require.Error(t, reviewWitnessVisible(receiptVisibleInput(record, raw), reader, witness))
	})
	t.Run("hidden null needs successful exact read", func(t *testing.T) {
		record, reader, _ := receiptFixture(t, "null-hidden", `{"success":true,"data":{"visible":"shown","unit":null}}`)
		witness := decodeOmittedQuoteWitness(t, record.ID, "/data/unit")
		input := receiptVisibleInput(record, map[string]any{"success": true, "data": map[string]any{"visible": "shown"}})
		require.ErrorContains(t, reviewWitnessVisible(input, reader, witness), "was not exposed")
		args := `{"operation":"read","evidence_id":"` + record.ID + `","json_pointer":"/data/unit","max_chars":1000}`
		response := reader.read(args)
		encoded, err := json.Marshal(response)
		require.NoError(t, err)
		reader.transcript = []chat.Message{
			{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: "read-present-null", Function: chat.FunctionCall{Name: reviewEvidenceToolName, Arguments: args}}}},
			{Role: "tool", Name: reviewEvidenceToolName, ToolCallID: "read-present-null", Content: string(encoded)},
		}
		require.NoError(t, reviewWitnessVisible(input, reader, witness))
	})
	t.Run("failed result is not source null", func(t *testing.T) {
		record, reader, raw := receiptFixture(t, "null-failed", `{"success":false,"data":{"unit":null}}`)
		witness := decodeOmittedQuoteWitness(t, record.ID, "/data/unit")
		require.ErrorContains(t, reviewWitnessVisible(receiptVisibleInput(record, raw), reader, witness), "successful tool result")
	})
	t.Run("projection null must match immutable value", func(t *testing.T) {
		record, reader, raw := receiptFixture(t, "null-mismatch", `{"success":true,"data":{"rows":[["2001-01",12.5,"kg"]]}}`)
		input := receiptVisibleInput(record, raw)
		input["structured_query_row_companion"] = map[string]any{"entries": []any{map[string]any{
			"entry_index": 0, "evidence_id": record.ID, "source_json_pointer": "/data/rows", "row_encoding": "matrix",
			"columns": []any{"period", "value", "unit"}, "rows": []any{[]any{"2001-01", json.Number("12.5"), nil}},
		}}}
		witness := reviewSourceWitness{Kind: "source", RowRef: &reviewStructuredRowRef{EntryIndex: 0, RowIndex: 0, Column: "unit"}}
		require.Error(t, reviewWitnessVisible(input, reader, witness))
	})
}

func TestExplicitJSONNullQuoteRemainsRejected(t *testing.T) {
	var witness reviewSourceWitness
	require.Error(t, json.Unmarshal([]byte(`{"kind":"source","evidence_id":"id","json_pointer":"/data/unit","quote":null}`), &witness))
}
