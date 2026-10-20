package agent

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func TestOmittedQuoteDerivesOnlyScalarAtExplicitSourcePointer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pointer string
		content string
	}{
		{
			name:    "string",
			pointer: "/data/label",
			content: `{"success":true,"data":{"label":"visible wording"}}`,
		},
		{
			name:    "number preserves source spelling",
			pointer: "/data/amount",
			content: `{"success":true,"data":{"amount":12.50}}`,
		},
		{
			name:    "boolean",
			pointer: "/data/active",
			content: `{"success":true,"data":{"active":true}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record, reader, raw := receiptFixture(t, "omitted-quote-"+tc.name, tc.content)
			var witness reviewSourceWitness
			payload, err := json.Marshal(map[string]any{
				"kind": "source", "evidence_id": record.ID, "json_pointer": tc.pointer,
			})
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(payload, &witness))
			require.Empty(t, witness.Quote, "quote is derived only after resolving the exact immutable source path")
			require.NoError(t, reviewWitnessVisible(receiptVisibleInput(record, raw), reader, witness))
		})
	}
}

func TestOmittedQuoteStillRequiresExactValueToBeExposed(t *testing.T) {
	record, reader, _ := receiptFixture(t, "omitted-hidden", `{"success":true,"data":{"visible":"shown","hidden":"not shown"}}`)
	witness := decodeOmittedQuoteWitness(t, record.ID, "/data/hidden")
	input := receiptVisibleInput(record, map[string]any{
		"success": true,
		"data":    map[string]any{"visible": "shown"},
	})
	require.ErrorContains(t, reviewWitnessVisible(input, reader, witness), "was not exposed")

	args, err := json.Marshal(map[string]any{
		"operation": "read", "evidence_id": record.ID, "json_pointer": "/data/hidden", "max_chars": 1000,
	})
	require.NoError(t, err)
	response := reader.read(string(args))
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	reader.transcript = []chat.Message{
		{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: "read-hidden-leaf", Function: chat.FunctionCall{Name: reviewEvidenceToolName, Arguments: string(args)}}}},
		{Role: "tool", Name: reviewEvidenceToolName, ToolCallID: "read-hidden-leaf", Content: string(encoded)},
	}
	require.NoError(t, reviewWitnessVisible(input, reader, witness), "a successful paired exact-path read exposes the same raw scalar")
}

func TestOmittedQuoteValidatesFullReceiptOnlyAfterExactPairedRead(t *testing.T) {
	message := chat.Message{Role: "tool", Name: "read_document", ToolCallID: "full-receipt-call", Content: `{"success":true,"data":{"visible":"shown","hidden":"private leaf"}}`}
	reader := newReviewEvidenceReader([]chat.Message{message})
	record := reader.records[0]
	snapshot := &sourceReviewSnapshot{
		sourceRecords: reader,
		input: receiptVisibleInput(record, map[string]any{
			"success": true,
			"data":    map[string]any{"visible": "shown"},
		}),
	}

	receiptJSON, err := json.Marshal(map[string]any{
		"claim":      "The report contains the private leaf.",
		"basis":      "source",
		"assessment": "The exact leaf is now shown by a successful read.",
		"evidence": []any{map[string]any{
			"kind": "source", "evidence_id": record.ID, "json_pointer": "/data/hidden",
		}},
	})
	require.NoError(t, err)
	var check reviewClaimCheck
	require.NoError(t, json.Unmarshal(receiptJSON, &check), "the complete receipt decodes with omitted quote")

	answer := "The report contains the private leaf."
	assessment := "The candidate's source claim matches the displayed leaf."
	require.NoError(t, validateReviewReceipt(answer, assessment, []reviewClaimCheck{check}, snapshot), "an unexposed witness is audited, not blocking")
	require.Contains(t, reviewReceiptWitnessSummary([]reviewClaimCheck{check}, snapshot)["diagnostics"], "was not exposed")

	args, err := json.Marshal(map[string]any{
		"operation": "read", "evidence_id": record.ID, "json_pointer": "/data/hidden", "max_chars": 1000,
	})
	require.NoError(t, err)
	response := reader.read(string(args))
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	reader.transcript = []chat.Message{
		{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: "full-receipt-read", Function: chat.FunctionCall{Name: reviewEvidenceToolName, Arguments: string(args)}}}},
		{Role: "tool", Name: reviewEvidenceToolName, ToolCallID: "full-receipt-read", Content: string(encoded)},
	}
	require.NoError(t, validateReviewReceipt(answer, assessment, []reviewClaimCheck{check}, snapshot))
}

func TestOmittedQuoteAllowsVisibleContainersAndRejectsFailuresWrongIdentityAndPaths(t *testing.T) {
	t.Run("container", func(t *testing.T) {
		record, reader, raw := receiptFixture(t, "omitted-container", `{"success":true,"data":{"row":{"period":"2001-01","value":12.5}}}`)
		witness := decodeOmittedQuoteWitness(t, record.ID, "/data/row")
		require.NoError(t, reviewWitnessVisible(receiptVisibleInput(record, raw), reader, witness), "the exact complete canonical container is visible in the source projection")
	})

	t.Run("failed source", func(t *testing.T) {
		record, reader, raw := receiptFixture(t, "omitted-failure", `{"success":false,"error":"temporary issue"}`)
		witness := decodeOmittedQuoteWitness(t, record.ID, "/error")
		require.ErrorContains(t, reviewWitnessVisible(receiptVisibleInput(record, raw), reader, witness), "successful tool result")
	})

	t.Run("wrong identity", func(t *testing.T) {
		record, reader, raw := receiptFixture(t, "omitted-identity", `{"success":true,"data":{"value":12.5}}`)
		other, _, _ := receiptFixture(t, "different-identity", `{"success":true,"data":{"value":12.5}}`)
		witness := decodeOmittedQuoteWitness(t, other.ID, "/data/value")
		require.Error(t, reviewWitnessVisible(receiptVisibleInput(record, raw), reader, witness))
	})

	t.Run("missing exact path", func(t *testing.T) {
		record, reader, raw := receiptFixture(t, "omitted-wrong-path", `{"success":true,"data":{"value":12.5}}`)
		witness := decodeOmittedQuoteWitness(t, record.ID, "/data/other")
		require.Error(t, reviewWitnessVisible(receiptVisibleInput(record, raw), reader, witness))
	})
}

func TestOmittedQuoteDecoderRejectsUnsafeOrMalformedLocatorShapes(t *testing.T) {
	for _, raw := range []string{
		`{"kind":"source","evidence_id":"id"}`,
		`{"kind":"execution","evidence_id":"id","json_pointer":"/data/value"}`,
		`{"kind":"source","evidence_id":"id","json_pointer":"/data/value","quote":""}`,
		`{"kind":"source","evidence_id":"id","json_pointer":"/data/value","quote":"   "}`,
		`{"kind":"source","evidence_id":"id","json_pointer":"/data/value","quote":null}`,
		`{"kind":"source","source_pointer_ref":0}`,
	} {
		t.Run(raw, func(t *testing.T) {
			var witness reviewSourceWitness
			require.Error(t, json.Unmarshal([]byte(raw), &witness))
		})
	}
}

func TestExplicitMismatchedQuoteIsNotReplacedFromPointer(t *testing.T) {
	record, reader, raw := receiptFixture(t, "explicit-mismatch", `{"success":true,"data":{"value":"actual source wording"}}`)
	witness := reviewSourceWitness{
		EvidenceID: record.ID, JSONPointer: "/data/value", Quote: "different wording", Kind: "source",
	}
	require.ErrorContains(t, reviewWitnessVisible(receiptVisibleInput(record, raw), reader, witness), "does not exactly match")
}

func decodeOmittedQuoteWitness(t *testing.T, evidenceID, pointer string) reviewSourceWitness {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"kind": "source", "evidence_id": evidenceID, "json_pointer": pointer,
	})
	require.NoError(t, err)
	var witness reviewSourceWitness
	require.NoError(t, json.Unmarshal(payload, &witness))
	return witness
}
