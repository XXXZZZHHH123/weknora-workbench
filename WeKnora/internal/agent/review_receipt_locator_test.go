package agent

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func TestReviewSourcePointerRefUsesActualCitationAdapter(t *testing.T) {
	message := citationToolMessage("read_document", citationPayload("receipt-locator", "Observed sample: 42 units.", true))
	messages := []chat.Message{message}
	reader := newReviewEvidenceReader(messages)
	view := citedSourceEvidenceFromMessages(messages, `<kb chunk_id="receipt-locator">p. 7</kb>`, citationReviewEvidenceBudget, true)
	ref := 0
	witness := reviewSourceWitness{SourcePointerRef: &ref, Quote: "42 units", Kind: "source"}
	input := map[string]any{"citation_source_evidence": view}
	require.NoError(t, reviewWitnessVisible(input, reader, witness))
}

func TestReviewCitationBodyLocatorRequiresCurrentDisplayedBody(t *testing.T) {
	record, reader, _ := receiptFixture(t, "locator-partial", `{"success":true,"data":{"body":"visible prefix and hidden suffix"}}`)
	ref := 0
	view := map[string]any{
		"source_results":  []any{[]any{record.Tool, record.ToolCallID, record.ID}},
		"source_pointers": []any{[]any{0, "/data/body"}},
		"items":           []any{map[string]any{"source_pointer_ref": 0, "content_included": true, "content": "visible prefix …"}},
	}
	input := map[string]any{"citation_source_evidence": view}
	witness := reviewSourceWitness{SourcePointerRef: &ref, Quote: "hidden suffix", Kind: "source"}
	reader.transcript = []chat.Message{
		{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: "locator-read", Function: chat.FunctionCall{Name: reviewEvidenceToolName}}}},
		{Role: "tool", Name: reviewEvidenceToolName, ToolCallID: "locator-read", Content: `{"status":"ok","evidence_id":"` + record.ID + `","json_pointer":"/data/body","text":"visible prefix and hidden suffix"}`},
	}
	require.Error(t, reviewWitnessVisible(input, reader, witness), "reader access cannot make a body locator refer to a hidden suffix")
	witness.SourcePointerRef = nil
	witness.EvidenceID, witness.JSONPointer = record.ID, "/data/body"
	require.NoError(t, reviewWitnessVisible(input, reader, witness), "the legacy exact-path form remains usable after the reader exposed the suffix")

	view["items"].([]any)[0].(map[string]any)["content"] = "visible prefix and hidden suffix"
	require.NoError(t, reviewWitnessVisible(input, reader, reviewSourceWitness{SourcePointerRef: &ref, Quote: "hidden suffix", Kind: "source"}))
}

func TestReviewSourcePointerRefRejectsInvalidAndConflictingLocators(t *testing.T) {
	record, reader, _ := receiptFixture(t, "locator-invalid", `{"success":true,"data":{"body":"exact visible source"}}`)
	view := map[string]any{
		"source_results":  []any{[]any{record.Tool, record.ToolCallID, record.ID}},
		"source_pointers": []any{[]any{0, "/data/body"}},
		"items":           []any{map[string]any{"source_pointer_ref": 0, "content_included": true, "content": "exact visible source"}},
	}
	input := map[string]any{"citation_source_evidence": view}
	ref := 0
	base := reviewSourceWitness{SourcePointerRef: &ref, Quote: "exact visible", Kind: "source"}
	require.NoError(t, reviewWitnessVisible(input, reader, base))

	negative, outOfRange := -1, 1
	for name, witness := range map[string]reviewSourceWitness{
		"negative":             {SourcePointerRef: &negative, Quote: "exact visible", Kind: "source"},
		"unmapped ref":         {SourcePointerRef: &outOfRange, Quote: "exact visible", Kind: "source"},
		"other source":         {SourcePointerRef: &ref, Quote: "exact visible", Kind: "execution"},
		"conflicting id":       {SourcePointerRef: &ref, EvidenceID: record.ID, Quote: "exact visible", Kind: "source"},
		"conflicting pointer":  {SourcePointerRef: &ref, JSONPointer: "/data/body", Quote: "exact visible", Kind: "source"},
		"wrong displayed body": {SourcePointerRef: &ref, Quote: "absent text", Kind: "source"},
	} {
		t.Run(name, func(t *testing.T) { require.Error(t, reviewWitnessVisible(input, reader, witness)) })
	}
	view["items"].([]any)[0].(map[string]any)["content"] = "a different displayed body"
	require.Error(t, reviewWitnessVisible(input, reader, base), "a quote from the immutable result cannot be borrowed from another displayed body")

	noCitationInput := map[string]any{"source_tool_results": []any{map[string]any{"evidence_id": record.ID, "content": record.Value}}}
	require.Error(t, reviewWitnessVisible(noCitationInput, reader, base), "a locator cannot address a non-citation source view")
	legacy := reviewSourceWitness{EvidenceID: record.ID, JSONPointer: "/data/body", Quote: "exact visible", Kind: "source"}
	require.NoError(t, reviewWitnessVisible(noCitationInput, reader, legacy), "legacy long-form witnesses remain compatible with other source views")
}

func TestReviewSourcePointerRefIsSparseAndResolvesResultIdentity(t *testing.T) {
	messages := []chat.Message{
		{Role: "tool", Name: "read_document", ToolCallID: "locator-a", Content: `{"success":true,"data":{"body":"same phrase"}}`},
		{Role: "tool", Name: "read_document", ToolCallID: "locator-b", Content: `{"success":true,"data":{"body":"same phrase"}}`},
	}
	reader := newReviewEvidenceReader(messages)
	first, second := reader.records[0], reader.records[1]
	// The only displayed item is at array position 0 but has source_pointer_ref 2.
	// The model supplies that displayed ref, not the item position; the server
	// then resolves pointer slot 2 and source-result slot 1.
	view := map[string]any{
		"source_results": []any{
			[]any{"read_document", "locator-a", first.ID},
			[]any{"read_document", "locator-b", second.ID},
		},
		"source_pointers": []any{
			[]any{json.Number("0"), "/data/body/unused-a"},
			[]any{json.Number("0"), "/data/body/unused-b"},
			[]any{json.Number("1"), "/data/body"},
		},
		"items": []any{map[string]any{"source_pointer_ref": json.Number("2"), "content_included": true, "content": "same phrase"}},
	}
	ref := 2
	input := map[string]any{"citation_source_evidence": view}
	witness := reviewSourceWitness{SourcePointerRef: &ref, Quote: "same phrase", Kind: "source"}
	require.NoError(t, reviewWitnessVisible(input, reader, witness))
	resolved, err := resolveCitationBodyWitness(input, reader, witness)
	require.NoError(t, err)
	require.Equal(t, second.ID, resolved.EvidenceID, "equal text must not merge source identities")
	view["source_results"].([]any)[1].([]any)[1] = "locator-a"
	require.Error(t, reviewWitnessVisible(input, reader, witness), "the mapped result identity must match its immutable tool call")
}

func TestReviewSourcePointerRefRejectsDuplicateAndAdditionalObservationRefs(t *testing.T) {
	record, reader, _ := receiptFixture(t, "locator-duplicate", `{"success":true,"data":{"body":"exact visible source"}}`)
	view := map[string]any{
		"source_results":  []any{[]any{record.Tool, record.ToolCallID, record.ID}},
		"source_pointers": []any{[]any{0, "/data/body"}, []any{0, "/data/body"}},
		"items": []any{
			map[string]any{"source_pointer_ref": 0, "content_included": true, "content": "wrong body"},
			map[string]any{"source_pointer_ref": 0, "content_included": true, "content": "exact visible source"},
		},
		"additional_source_observations": []any{
			map[string]any{"source_pointer_ref": 1, "body_id": "derived-body", "content": "exact visible source"},
		},
	}
	input := map[string]any{"citation_source_evidence": view}
	duplicateRef := 0
	require.Error(t, reviewWitnessVisible(input, reader, reviewSourceWitness{SourcePointerRef: &duplicateRef, Quote: "exact visible", Kind: "source"}), "a repeated pointer ref is ambiguous even when one body contains the quote")
	additionalOnlyRef := 1
	require.Error(t, reviewWitnessVisible(input, reader, reviewSourceWitness{SourcePointerRef: &additionalOnlyRef, Quote: "exact visible", Kind: "source"}), "additional observations cannot stand in for a displayed main body item")
}
