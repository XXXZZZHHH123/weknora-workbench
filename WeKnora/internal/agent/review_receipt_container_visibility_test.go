package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func TestOmittedContainerQuoteRequiresExactWholeContainerVisibility(t *testing.T) {
	record, reader, _ := receiptFixture(t, "container-hidden-parent", `{"success":true,"data":{"row":{"period":"2001-01","value":12.5,"unit":"tons"}}}`)
	witness := decodeOmittedQuoteWitness(t, record.ID, "/data/row")

	for _, tc := range []struct {
		name    string
		content any
	}{
		{
			name: "parent omitted",
			content: map[string]any{"success": true, "data": map[string]any{
				"other": "shown",
			}},
		},
		{
			name: "partial parent projection",
			content: map[string]any{"success": true, "data": map[string]any{
				"row": map[string]any{"period": "2001-01", "value": 12.5},
			}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := receiptVisibleInput(record, tc.content)
			require.ErrorContains(t, reviewWitnessVisible(input, reader, witness), "was not exposed")
		})
	}
}

func TestOmittedArrayQuoteRequiresTheWholeArray(t *testing.T) {
	record, reader, _ := receiptFixture(t, "array-container", `{"success":true,"data":{"rows":[{"month":"m1","value":12},{"month":"m2","value":15}]}}`)
	witness := decodeOmittedQuoteWitness(t, record.ID, "/data/rows")

	complete := receiptVisibleInput(record, map[string]any{"success": true, "data": map[string]any{
		"rows": []any{
			map[string]any{"month": "m1", "value": json.Number("12")},
			map[string]any{"month": "m2", "value": json.Number("15")},
		},
	}})
	require.NoError(t, reviewWitnessVisible(complete, reader, witness))

	partial := receiptVisibleInput(record, map[string]any{"success": true, "data": map[string]any{
		"rows": []any{map[string]any{"month": "m1", "value": json.Number("12")}},
	}})
	require.ErrorContains(t, reviewWitnessVisible(partial, reader, witness), "was not exposed")
}

func TestOmittedContainerQuoteChildReadDoesNotExposeParent(t *testing.T) {
	record, reader, _ := receiptFixture(t, "container-child-read", `{"success":true,"data":{"row":{"period":"2001-01","value":12.5}}}`)
	witness := decodeOmittedQuoteWitness(t, record.ID, "/data/row")
	input := receiptVisibleInput(record, map[string]any{"success": true, "data": map[string]any{}})
	appendContainerRead(t, reader, record.ID, "/data/row/value", 0)
	require.ErrorContains(t, reviewWitnessVisible(input, reader, witness), "was not exposed")
}

func TestOmittedContainerQuoteCompleteExactPointerReadIsVisible(t *testing.T) {
	record, reader, _ := receiptFixture(t, "container-exact-read", `{"success":true,"data":{"row":{"period":"2001-01","value":12.5}}}`)
	witness := decodeOmittedQuoteWitness(t, record.ID, "/data/row")
	input := receiptVisibleInput(record, map[string]any{"success": true, "data": map[string]any{}})
	appendContainerRead(t, reader, record.ID, "/data/row", 0)
	require.NoError(t, reviewWitnessVisible(input, reader, witness))
}

func TestOmittedContainerQuoteAcceptsCompleteVerifiedPagesOnlyForCoveredSpan(t *testing.T) {
	data := map[string]any{
		"a_large":  strings.Repeat("x", reviewEvidenceSegmentLimit+500),
		"z_target": map[string]any{"period": "2001-01", "value": json.Number("12.50")},
	}
	reader, evidenceID := reviewJSONPagesFixture(t, data)
	record, ok := reviewRecordByID(reader, evidenceID)
	require.True(t, ok)
	input := receiptVisibleInput(record, map[string]any{"success": true, "data": map[string]any{}})
	witness := decodeOmittedQuoteWitness(t, evidenceID, "/data/z_target")

	serializedData, err := json.Marshal(data)
	require.NoError(t, err)
	runes := len([]rune(string(serializedData)))
	appendReviewReadPage(t, reader, evidenceID, "/data", 0, nil, nil)
	require.ErrorContains(t, reviewWitnessVisible(input, reader, witness), "was not exposed", "the target is beyond the first page")
	for offset := reviewEvidenceSegmentLimit; offset < runes; offset += reviewEvidenceSegmentLimit {
		appendReviewReadPage(t, reader, evidenceID, "/data", offset, nil, nil)
	}
	require.NoError(t, reviewWitnessVisible(input, reader, witness), "the target's exact serialized span is covered by the complete verified pages")
}

func TestOmittedContainerQuoteDoesNotCrossBindIDsOrFailures(t *testing.T) {
	first, firstReader, firstRaw := receiptFixture(t, "container-id-one", `{"success":true,"data":{"row":{"period":"2001-01","value":12.5}}}`)
	second, _, _ := receiptFixture(t, "container-id-two", `{"success":true,"data":{"row":{"period":"2001-01","value":12.5}}}`)
	witness := decodeOmittedQuoteWitness(t, second.ID, "/data/row")
	require.Error(t, reviewWitnessVisible(receiptVisibleInput(first, firstRaw), firstReader, witness))

	failed, failedReader, failedRaw := receiptFixture(t, "container-failed", `{"success":false,"data":{"row":{"period":"2001-01"}},"error":"failed"}`)
	failedWitness := decodeOmittedQuoteWitness(t, failed.ID, "/data/row")
	require.ErrorContains(t, reviewWitnessVisible(receiptVisibleInput(failed, failedRaw), failedReader, failedWitness), "successful tool result")
}

func TestOmittedContainerQuoteEnforcesSizeAndLocatorBoundaries(t *testing.T) {
	large := strings.Repeat("x", reviewReceiptContainerQuoteLimit)
	record, reader, raw := receiptFixture(t, "container-too-large", fmt.Sprintf(`{"success":true,"data":{"row":{"text":%q}}}`, large))
	witness := decodeOmittedQuoteWitness(t, record.ID, "/data/row")
	require.ErrorContains(t, reviewWitnessVisible(receiptVisibleInput(record, raw), reader, witness), "within 1024 bytes")

	for _, rawWitness := range []string{
		`{"kind":"execution","evidence_id":"id","json_pointer":"/data/row"}`,
		`{"kind":"source","source_pointer_ref":0}`,
		`{"kind":"source","evidence_id":"id","json_pointer":"/data/row","quote":{"period":"2001-01"}}`,
		`{"kind":"source","evidence_id":"id","json_pointer":"/data/row","quote":["2001-01"]}`,
	} {
		t.Run(rawWitness, func(t *testing.T) {
			var parsed reviewSourceWitness
			require.Error(t, json.Unmarshal([]byte(rawWitness), &parsed), "non-raw locators are not accepted as omitted-quote witnesses")
		})
	}
}

func TestExplicitPartialContainerQuoteStillNeedsWholeContainerVisible(t *testing.T) {
	record, reader, _ := receiptFixture(t, "container-explicit-fragment", `{"success":true,"data":{"row":{"period":"2001-01","value":"displayed"}}}`)
	witness := reviewSourceWitness{EvidenceID: record.ID, JSONPointer: "/data/row", Quote: "displayed", Kind: "source"}
	input := receiptVisibleInput(record, map[string]any{"success": true, "data": map[string]any{}})
	require.Error(t, reviewWitnessVisible(input, reader, witness), "a matching child quote does not expose the named container")
}

func appendContainerRead(t *testing.T, reader *reviewEvidenceReader, evidenceID, pointer string, offset int) {
	t.Helper()
	callID := fmt.Sprintf("container-read-%d", len(reader.transcript))
	args := map[string]any{"operation": "read", "evidence_id": evidenceID, "json_pointer": pointer, "offset": offset, "max_chars": reviewEvidenceSegmentLimit}
	encodedArgs, err := json.Marshal(args)
	require.NoError(t, err)
	response := reader.read(string(encodedArgs))
	encodedResponse, err := json.Marshal(response)
	require.NoError(t, err)
	reader.transcript = append(reader.transcript,
		chat.Message{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: callID, Type: "function", Function: chat.FunctionCall{Name: reviewEvidenceToolName, Arguments: string(encodedArgs)}}}},
		chat.Message{Role: "tool", Name: reviewEvidenceToolName, ToolCallID: callID, Content: string(encodedResponse)},
	)
}
