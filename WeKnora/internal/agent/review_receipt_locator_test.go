package agent

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func TestReviewCitationBodyLocatorUsesActualCitationAdapter(t *testing.T) {
	message := citationToolMessage("read_document", citationPayload("receipt-locator", "Observed sample: 42 units.", true))
	messages := []chat.Message{message}
	reader := newReviewEvidenceReader(messages)
	view := citedSourceEvidenceFromMessages(messages, `<kb chunk_id="receipt-locator">p. 7</kb>`, citationReviewEvidenceBudget, true)
	ref := 0
	witness := reviewSourceWitness{CitationItem: &ref, Quote: "42 units", Kind: "source"}
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
	witness := reviewSourceWitness{CitationItem: &ref, Quote: "hidden suffix", Kind: "source"}
	reader.transcript = []chat.Message{
		{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: "locator-read", Function: chat.FunctionCall{Name: reviewEvidenceToolName}}}},
		{Role: "tool", Name: reviewEvidenceToolName, ToolCallID: "locator-read", Content: `{"status":"ok","evidence_id":"` + record.ID + `","json_pointer":"/data/body","text":"visible prefix and hidden suffix"}`},
	}
	require.Error(t, reviewWitnessVisible(input, reader, witness), "reader access cannot make a body locator refer to a hidden suffix")
	witness.CitationItem = nil
	witness.EvidenceID, witness.JSONPointer = record.ID, "/data/body"
	require.NoError(t, reviewWitnessVisible(input, reader, witness), "the legacy exact-path form remains usable after the reader exposed the suffix")

	view["items"].([]any)[0].(map[string]any)["content"] = "visible prefix and hidden suffix"
	require.NoError(t, reviewWitnessVisible(input, reader, reviewSourceWitness{CitationItem: &ref, Quote: "hidden suffix", Kind: "source"}))
}

func TestReviewCitationBodyLocatorRejectsInvalidAndConflictingLocators(t *testing.T) {
	record, reader, _ := receiptFixture(t, "locator-invalid", `{"success":true,"data":{"body":"exact visible source"}}`)
	view := map[string]any{
		"source_results":  []any{[]any{record.Tool, record.ToolCallID, record.ID}},
		"source_pointers": []any{[]any{0, "/data/body"}},
		"items":           []any{map[string]any{"source_pointer_ref": 0, "content_included": true, "content": "exact visible source"}},
	}
	input := map[string]any{"citation_source_evidence": view}
	ref := 0
	base := reviewSourceWitness{CitationItem: &ref, Quote: "exact visible", Kind: "source"}
	require.NoError(t, reviewWitnessVisible(input, reader, base))

	negative, outOfRange := -1, 1
	for name, witness := range map[string]reviewSourceWitness{
		"negative":            {CitationItem: &negative, Quote: "exact visible", Kind: "source"},
		"out of range":        {CitationItem: &outOfRange, Quote: "exact visible", Kind: "source"},
		"other source":        {CitationItem: &ref, Quote: "exact visible", Kind: "execution"},
		"conflicting id":      {CitationItem: &ref, EvidenceID: record.ID, Quote: "exact visible", Kind: "source"},
		"conflicting pointer": {CitationItem: &ref, JSONPointer: "/data/body", Quote: "exact visible", Kind: "source"},
		"not displayed":       {CitationItem: &ref, Quote: "absent text", Kind: "source"},
	} {
		t.Run(name, func(t *testing.T) { require.Error(t, reviewWitnessVisible(input, reader, witness)) })
	}

	noCitationInput := map[string]any{"source_tool_results": []any{map[string]any{"evidence_id": record.ID, "content": record.Value}}}
	require.Error(t, reviewWitnessVisible(noCitationInput, reader, base), "a locator cannot address a non-citation source view")
	legacy := reviewSourceWitness{EvidenceID: record.ID, JSONPointer: "/data/body", Quote: "exact visible", Kind: "source"}
	require.NoError(t, reviewWitnessVisible(noCitationInput, reader, legacy), "legacy long-form witnesses remain compatible with other source views")
}

func TestReviewCitationItemResolvesPointerIndexThenResultIndex(t *testing.T) {
	messages := []chat.Message{
		{Role: "tool", Name: "read_document", ToolCallID: "locator-a", Content: `{"success":true,"data":{"body":"same phrase"}}`},
		{Role: "tool", Name: "read_document", ToolCallID: "locator-b", Content: `{"success":true,"data":{"body":"same phrase"}}`},
	}
	reader := newReviewEvidenceReader(messages)
	first, second := reader.records[0], reader.records[1]
	// Item 0 resolves pointer slot 2 and then source-result slot 1. The model
	// supplies only the item position, never these internal dictionary indexes.
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
	ref := 0
	input := map[string]any{"citation_source_evidence": view}
	witness := reviewSourceWitness{CitationItem: &ref, Quote: "same phrase", Kind: "source"}
	require.NoError(t, reviewWitnessVisible(input, reader, witness))
	resolved, err := resolveCitationBodyWitness(input, reader, witness)
	require.NoError(t, err)
	require.Equal(t, second.ID, resolved.EvidenceID, "equal text must not merge source identities")
	view["source_results"].([]any)[1].([]any)[1] = "locator-a"
	require.Error(t, reviewWitnessVisible(input, reader, witness), "the mapped result identity must match its immutable tool call")
}
