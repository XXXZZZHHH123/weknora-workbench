package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func TestPointerlessReviewWitnessDerivesUniqueVisibleScalar(t *testing.T) {
	record, reader, raw := receiptFixture(t, "unique-call", `{"success":true,"data":{"heading":"quarterly report","amount":12.50}}`)
	input := receiptVisibleInput(record, raw)
	witness := reviewSourceWitness{EvidenceID: record.ID, Quote: "quarterly report", Kind: "source"}
	require.NoError(t, reviewWitnessVisible(input, reader, witness))
	data, err := reviewPointerValue(record.Value, "/data")
	require.NoError(t, err)
	pointer, count, overflow := uniqueReviewScalarPointer(data, "quarterly report")
	require.Equal(t, "/data/heading", pointer)
	require.Equal(t, 1, count)
	require.False(t, overflow)
}

func TestPointerlessReviewWitnessNeedsVisibilityOrReaderObservation(t *testing.T) {
	record, reader, _ := receiptFixture(t, "hidden-call", `{"success":true,"data":{"body":"prefix then hidden tail"}}`)
	witness := reviewSourceWitness{EvidenceID: record.ID, Quote: "hidden tail", Kind: "source"}
	input := receiptVisibleInput(record, map[string]any{"success": true, "data": map[string]any{"body": "prefix then …"}})
	require.ErrorContains(t, reviewWitnessVisible(input, reader, witness), "was not exposed")

	args, err := json.Marshal(map[string]any{"operation": "read", "evidence_id": record.ID, "json_pointer": "/data/body", "max_chars": 1000})
	require.NoError(t, err)
	response := reader.read(string(args))
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	reader.transcript = []chat.Message{
		{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: "read-hidden", Function: chat.FunctionCall{Name: reviewEvidenceToolName, Arguments: string(args)}}}},
		{Role: "tool", Name: reviewEvidenceToolName, ToolCallID: "read-hidden", Content: string(encoded)},
	}
	require.NoError(t, reviewWitnessVisible(input, reader, witness))
}

func TestPointerlessReviewWitnessRejectsAmbiguousAndWrongIdentity(t *testing.T) {
	record, reader, raw := receiptFixture(t, "ambiguous-call", `{"success":true,"data":{"left":"shared wording","right":"shared wording"}}`)
	input := receiptVisibleInput(record, raw)
	witness := reviewSourceWitness{EvidenceID: record.ID, Quote: "shared wording", Kind: "source"}
	require.ErrorContains(t, reviewWitnessVisible(input, reader, witness), "multiple scalar paths")
	require.ErrorContains(t, reviewWitnessVisible(input, reader, witness), "explicit json_pointer")

	other, _, _ := receiptFixture(t, "other-call", `{"success":true,"data":{"left":"shared wording"}}`)
	witness.EvidenceID = other.ID
	require.Error(t, reviewWitnessVisible(input, reader, witness), "matching bytes under another evidence identity cannot be borrowed")
}

func TestPointerlessReviewWitnessRejectsFailedToolsAndWrongExplicitPointer(t *testing.T) {
	record, reader, raw := receiptFixture(t, "failed-source", `{"success":false,"error":"matching phrase"}`)
	input := receiptVisibleInput(record, raw)
	require.ErrorContains(t, reviewWitnessVisible(input, reader, reviewSourceWitness{
		EvidenceID: record.ID, Quote: "matching phrase", Kind: "source",
	}), "successful tool result")

	record, reader, raw = receiptFixture(t, "strict-pointer", `{"success":true,"data":{"right":"present phrase"}}`)
	input = receiptVisibleInput(record, raw)
	require.Error(t, reviewWitnessVisible(input, reader, reviewSourceWitness{
		EvidenceID: record.ID, JSONPointer: "/data/wrong", Quote: "present phrase", Kind: "source",
	}), "explicit pointers are never repaired by quote search")
}

func TestPointerlessReviewWitnessEscapesSpecialKeysAndMatchesNumbersExactly(t *testing.T) {
	record, reader, raw := receiptFixture(t, "escaped-key", `{"success":true,"data":{"tilde~/slash":"unique phrase","amount":12.50}}`)
	data, err := reviewPointerValue(record.Value, "/data")
	require.NoError(t, err)
	pointer, count, overflow := uniqueReviewScalarPointer(data, "unique phrase")
	require.Equal(t, "/data/tilde~0~1slash", pointer)
	require.Equal(t, 1, count)
	require.False(t, overflow)
	require.NoError(t, reviewWitnessVisible(receiptVisibleInput(record, raw), reader, reviewSourceWitness{
		EvidenceID: record.ID, Quote: "unique phrase", Kind: "source",
	}))

	require.NoError(t, reviewWitnessVisible(receiptVisibleInput(record, raw), reader, reviewSourceWitness{
		EvidenceID: record.ID, Quote: "12.50", Kind: "source",
	}))
	require.Error(t, reviewWitnessVisible(receiptVisibleInput(record, raw), reader, reviewSourceWitness{
		EvidenceID: record.ID, Quote: "12.5", Kind: "source",
	}), "numeric leaf matches preserve exact JSON number spelling")
}

func TestPointerlessReviewWitnessFailsClosedOnSearchOverflow(t *testing.T) {
	data := map[string]any{"first": "single match", "many": []any{1, 2, 3, 4, 5}}
	pointer, count, overflow := uniqueReviewScalarPointerWithLimits(data, "single match", 3, 10)
	require.Empty(t, pointer)
	require.Equal(t, 1, count)
	require.True(t, overflow, "an incomplete scan cannot claim uniqueness")

	record, reader, raw := receiptFixture(t, "overflow-call", `{"success":true,"data":{"first":"single match","many":[1,2,3,4,5]}}`)
	_, _, productionOverflow := uniqueReviewScalarPointer(mustReviewData(t, record), "single match")
	require.False(t, productionOverflow)
	input := receiptVisibleInput(record, raw)
	// The bounded helper's fail-closed result is surfaced as a distinct error;
	// test this exact error path through a data tree beyond the production bound.
	large := make([]any, reviewReceiptUniqueWitnessNodeLimit+1)
	for i := range large {
		large[i] = "filler"
	}
	large[0] = "single match"
	bigContent, err := json.Marshal(map[string]any{"success": true, "data": large})
	require.NoError(t, err)
	record, reader, raw = receiptFixture(t, "large-call", string(bigContent))
	input = receiptVisibleInput(record, raw)
	err = reviewWitnessVisible(input, reader, reviewSourceWitness{EvidenceID: record.ID, Quote: "single match", Kind: "source"})
	require.ErrorContains(t, err, "exceeded its safety bound")
}

func TestContainerQuoteMismatchExplainsCompleteValueRequirement(t *testing.T) {
	record, reader, raw := receiptFixture(t, "container-call", `{"success":true,"data":{"row":{"period":"Q1","value":7}}}`)
	err := reviewWitnessVisible(receiptVisibleInput(record, raw), reader, reviewSourceWitness{
		EvidenceID: record.ID, JSONPointer: "/data/row", Quote: `"period":"Q1"`, Kind: "source",
	})
	require.ErrorContains(t, err, "complete short compact JSON object/array")
	require.ErrorContains(t, err, "object/array fragments do not match")
}

func mustReviewData(t *testing.T, record reviewEvidenceRecord) any {
	t.Helper()
	data, err := reviewPointerValue(record.Value, "/data")
	require.NoError(t, err)
	return data
}

func TestPointerlessReviewWitnessDoesNotSearchFailedOrOutputText(t *testing.T) {
	message := chat.Message{Role: "tool", Name: "read_document", ToolCallID: "failed-output", Content: `{"success":false,"error":"needle","output":"needle","data":{"value":"needle"}}`}
	reader := newReviewEvidenceReader([]chat.Message{message})
	record := reader.records[0]
	err := reviewWitnessVisible(receiptVisibleInput(record, record.Value), reader, reviewSourceWitness{
		EvidenceID: record.ID, Quote: "needle", Kind: "source",
	})
	require.Error(t, err)
	require.False(t, strings.Contains(err.Error(), "was not exposed"), "failure cannot enter the successful-data locator")
}

func TestWikiRenderedOutputWitnessStillRequiresIdentitySuccessAndExposure(t *testing.T) {
	for _, tool := range []string{"wiki_search", "wiki_read_page"} {
		t.Run(tool, func(t *testing.T) {
			content := `{"success":true,"data":{"count":1},"output":"returned page prefix; exact rendered body"}`
			reader := newReviewEvidenceReader([]chat.Message{{Role: "tool", Name: tool, ToolCallID: "wiki-render", Content: content}})
			record := reader.records[0]
			witness := reviewSourceWitness{EvidenceID: record.ID, JSONPointer: "/output", Quote: "exact rendered body", Kind: "source"}
			require.NoError(t, reviewWitnessVisible(receiptVisibleInput(record, record.Value), reader, witness))
			partial := receiptVisibleInput(record, map[string]any{"success": true, "data": map[string]any{"count": 1}, "output": "returned page prefix"})
			require.ErrorContains(t, reviewWitnessVisible(partial, reader, witness), "was not exposed")
			args := `{"operation":"read","evidence_id":"` + record.ID + `","json_pointer":"/output","max_chars":1000}`
			response, err := json.Marshal(reader.read(args))
			require.NoError(t, err)
			reader.transcript = []chat.Message{
				{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: "read-output", Function: chat.FunctionCall{Name: reviewEvidenceToolName, Arguments: args}}}},
				{Role: "tool", Name: reviewEvidenceToolName, ToolCallID: "read-output", Content: string(response)},
			}
			require.NoError(t, reviewWitnessVisible(partial, reader, witness))
		})
	}
	for _, test := range []struct {
		tool    string
		success bool
	}{{"wiki_search", false}, {"wiki_read_page", false}, {"describe_tools", true}, {"unrelated_tool", true}} {
		content, err := json.Marshal(map[string]any{"success": test.success, "data": map[string]any{}, "output": "arbitrary output"})
		require.NoError(t, err)
		reader := newReviewEvidenceReader([]chat.Message{{Role: "tool", Name: test.tool, ToolCallID: "other", Content: string(content)}})
		record := reader.records[0]
		require.Error(t, reviewWitnessVisible(receiptVisibleInput(record, record.Value), reader, reviewSourceWitness{EvidenceID: record.ID, JSONPointer: "/output", Quote: "arbitrary output", Kind: "source"}))
	}
}
