package agent

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func receiptFixture(t *testing.T, callID, content string) (reviewEvidenceRecord, *reviewEvidenceReader, any) {
	t.Helper()
	message := chat.Message{Role: "tool", Name: "read_document", ToolCallID: callID, Content: content}
	reader := newReviewEvidenceReader([]chat.Message{message})
	value, err := reviewPointerValue(reader.records[0].Value, "")
	require.NoError(t, err)
	return reader.records[0], reader, value
}

func receiptVisibleInput(record reviewEvidenceRecord, content any) map[string]any {
	return map[string]any{"source_tool_results": []any{map[string]any{
		"evidence_id": record.ID, "tool": record.Tool, "tool_call_id": record.ToolCallID, "content": content,
	}}}
}

func TestReviewWitnessVisibleRequiresSameEvidenceIdentityAndPointer(t *testing.T) {
	recordA, reader, valueA := receiptFixture(t, "call-a", `{"success":true,"data":{"label":"shared phrase"}}`)
	recordB, _, _ := receiptFixture(t, "call-b", `{"success":true,"data":{"label":"shared phrase"}}`)
	input := receiptVisibleInput(recordA, valueA)
	witness := reviewSourceWitness{EvidenceID: recordA.ID, JSONPointer: "/data/label", Quote: "shared phrase", Kind: "source"}
	require.NoError(t, reviewWitnessVisible(input, reader, witness))

	witness.EvidenceID = recordB.ID
	require.Error(t, reviewWitnessVisible(input, reader, witness), "identical wording from another result must not cross-bind")
	witness.EvidenceID = recordA.ID
	witness.JSONPointer = "/data/elsewhere"
	require.Error(t, reviewWitnessVisible(input, reader, witness), "the exact source path must be shown")
}

func TestReviewWitnessVisibleRejectsFailureAsSourceButAllowsFailureReceipt(t *testing.T) {
	record, reader, value := receiptFixture(t, "failed-call", `{"success":false,"error":"source timed out"}`)
	input := receiptVisibleInput(record, value)
	require.Error(t, reviewWitnessVisible(input, reader, reviewSourceWitness{
		EvidenceID: record.ID, JSONPointer: "/error", Quote: "source timed out", Kind: "source",
	}))
	require.NoError(t, reviewWitnessVisible(input, reader, reviewSourceWitness{
		EvidenceID: record.ID, JSONPointer: "/error", Quote: "source timed out", Kind: "execution",
	}), "failure metadata can establish a limitation, not a source fact")
}

func TestReviewWitnessVisibleRejectsPlanningAndToolDirectoryResults(t *testing.T) {
	for _, toolName := range []string{"plan_task", "describe_tools", expandTaskToolsName, "structured_data"} {
		t.Run(toolName, func(t *testing.T) {
			message := chat.Message{Role: "tool", Name: toolName, ToolCallID: "non-source", Content: `{"success":true,"data":{"operation":"describe_tools","name":"SELECT value"}}`}
			reader := newReviewEvidenceReader([]chat.Message{message})
			record := reader.records[0]
			input := receiptVisibleInput(record, record.Value)
			require.Error(t, reviewWitnessVisible(input, reader, reviewSourceWitness{
				EvidenceID: record.ID, JSONPointer: "/data/name", Quote: "SELECT value", Kind: "source",
			}))
		})
	}
}

func TestReviewWitnessVisibleAllowsScopeOnlyAsExecutionLimitation(t *testing.T) {
	record, reader, value := receiptFixture(t, "scoped-call", `{"success":true,"execution_scope":{"query":"SELECT value FROM series","limit":10},"data":{"value":12}}`)
	input := receiptVisibleInput(record, value)
	witness := reviewSourceWitness{EvidenceID: record.ID, JSONPointer: "/execution_scope/query", Quote: "SELECT value FROM series", Kind: "source"}
	require.Error(t, reviewWitnessVisible(input, reader, witness), "planned/execution scope is not a source fact")
	witness.Kind = "execution"
	require.NoError(t, reviewWitnessVisible(input, reader, witness), "the exact scope can establish what was executed or limit coverage")
}

func TestReviewWitnessVisibleRequiresReadForPartialTail(t *testing.T) {
	record, reader, _ := receiptFixture(t, "long-call", `{"success":true,"data":{"body":"prefix then hidden tail"}}`)
	projected := map[string]any{"success": true, "data": map[string]any{"body": "prefix then …"}}
	input := receiptVisibleInput(record, projected)
	witness := reviewSourceWitness{EvidenceID: record.ID, JSONPointer: "/data/body", Quote: "hidden tail", Kind: "source"}
	require.Error(t, reviewWitnessVisible(input, reader, witness), "raw existence cannot replace evidence actually shown to the reviewer")

	reader.transcript = []chat.Message{
		{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: "read-1", Function: chat.FunctionCall{Name: reviewEvidenceToolName}}}},
		{Role: "tool", Name: reviewEvidenceToolName, ToolCallID: "read-1", Content: `{"status":"ok","evidence_id":"` + record.ID + `","json_pointer":"/data/body","text":"prefix then hidden tail"}`},
	}
	require.NoError(t, reviewWitnessVisible(input, reader, witness), "the successful exact-path reader result exposes the omitted tail")

	reader.transcript[0].ToolCalls[0].ID = "unmatched-read"
	require.Error(t, reviewWitnessVisible(input, reader, witness), "an unpaired transcript result is not a genuine reader observation")
}

func TestReviewWitnessVisibleHandlesNumbersContainersAndCitationPointerDictionary(t *testing.T) {
	record, reader, raw := receiptFixture(t, "numeric-call", `{"success":true,"data":{"amount":12.50,"row":{"period":"2025-Q1","value":12.5}}}`)
	witness := reviewSourceWitness{EvidenceID: record.ID, JSONPointer: "/data/amount", Quote: "12.50", Kind: "source"}
	input := receiptVisibleInput(record, raw)
	require.NoError(t, reviewWitnessVisible(input, reader, witness), "JSON number spelling is retained")
	witness.JSONPointer, witness.Quote = "/data/row", `{"period":"2025-Q1","value":12.5}`
	require.NoError(t, reviewWitnessVisible(input, reader, witness), "a short exact compact JSON container is supported")
	witness.Quote = `"period":"2025-Q1"`
	require.Error(t, reviewWitnessVisible(input, reader, witness), "a container witness must be an exact JSON value, not arbitrary serialized fragments")

	witness.JSONPointer, witness.Quote = "/data/row/period", "2025-Q1"
	input["source_tool_results"] = []any{}
	input["citation_source_evidence"] = map[string]any{
		"source_results": []any{[]any{"read_document", "numeric-call", record.ID}},
		"source_pointers": []any{
			[]any{json.Number("0"), "/data/row/value"},
			[]any{json.Number("0"), "/data/row/period"},
		},
		"items": []any{map[string]any{"source_pointer_ref": json.Number("1"), "content_included": true, "content": "Observed period 2025-Q1 in the report."}},
	}
	require.NoError(t, reviewWitnessVisible(input, reader, witness))
	input["citation_source_evidence"].(map[string]any)["source_pointers"] = []any{[]any{json.Number("0"), "/data/row/value"}}
	require.Error(t, reviewWitnessVisible(input, reader, witness), "citation prose without its exact source pointer cannot launder a match")
}

func TestReviewWitnessVisibleSupportsRowsAndBoundImageObservation(t *testing.T) {
	content := `{"success":true,"data":{"rows":[{"site":"site-a","value":12.5}],"image_geometry_evidence":[{"reference":"resource://doc/source-a","knowledge_id":"doc","chunk_id":"chunk-a","page":3,"evidence":{"geometry":{"series":[{"legend":"Monthly level"}]}}},{"reference":"resource://doc/source-b","knowledge_id":"doc","chunk_id":"chunk-b","page":4,"evidence":{"geometry":{"series":[{"legend":"Monthly level"}]}}}],"image_evidence":[{"reference":"resource://doc/source-b","knowledge_id":"doc","chunk_id":"chunk-b","page":4,"read_success":true,"returned_image_index":0}]}}`
	record, reader, raw := receiptFixture(t, "mixed-call", content)
	input := receiptVisibleInput(record, raw)
	input["structured_query_row_companion"] = map[string]any{"entries": []any{map[string]any{
		"evidence_id": record.ID, "source_json_pointer": "/data/rows", "row_encoding": "positional",
		"constant_fields": map[string]any{"site": "site-a"}, "columns": []any{"value"}, "rows": []any{[]any{json.Number("12.5")}},
	}}}
	require.NoError(t, reviewWitnessVisible(input, reader, reviewSourceWitness{
		EvidenceID: record.ID, JSONPointer: "/data/rows/0/value", Quote: "12.5", Kind: "source",
	}))

	witness := reviewSourceWitness{EvidenceID: record.ID, JSONPointer: "/data/image_geometry_evidence/1/evidence/geometry/series/0/legend", Quote: "Monthly level", Kind: "source"}
	view := []any{map[string]any{
		"tool": record.Tool, "tool_call_id": record.ToolCallID,
		"content": map[string]any{"reference": "resource://doc/source-b", "knowledge_id": "doc", "chunk_id": "chunk-b", "page": 4,
			"returned_image_index": 0, "original_geometry_evidence": map[string]any{"geometry": map[string]any{"series": []any{map[string]any{"legend": "Monthly level"}}}}},
	}}
	require.True(t, reviewWitnessVisibleInIdentityView(view, record, witness), "geometry item index one maps to the matching source-bound image even though its returned image index is zero")
	witness.JSONPointer = "/data/image_geometry_evidence/0/evidence/geometry/series/0/legend"
	require.False(t, reviewWitnessVisibleInIdentityView(view, record, witness), "another figure with the same quote cannot satisfy a different source pointer")
	witness.JSONPointer = "/data/image_geometry_evidence/1/evidence/geometry/series/0/legend"
	view[0].(map[string]any)["content"].(map[string]any)["original_geometry_evidence"].(map[string]any)["geometry"].(map[string]any)["series"] = []any{
		map[string]any{"legend": "Another legend", "title": "Monthly level"},
	}
	require.False(t, reviewWitnessVisibleInIdentityView(view, record, witness), "a repeated quote under another field is not exposure of the witnessed path")
	view[0].(map[string]any)["content"].(map[string]any)["reference"] = "resource://doc/source-a"
	view[0].(map[string]any)["content"].(map[string]any)["chunk_id"] = "chunk-a"
	view[0].(map[string]any)["content"].(map[string]any)["page"] = 3
	require.False(t, reviewWitnessVisibleInIdentityView(view, record, witness), "a different source/page cannot lend the same quote")
}

func TestReviewWitnessVisibleAcceptsRealCitationDocumentAndRowAdapters(t *testing.T) {
	t.Run("citation pointer and displayed chunk body", func(t *testing.T) {
		message := citationToolMessage("read_document", citationPayload("receipt-chunk", "Observed sample: 42 units.", true))
		messages := []chat.Message{message}
		reader := newReviewEvidenceReader(messages)
		citationView := citedSourceEvidenceFromMessages(messages, `<kb chunk_id="receipt-chunk">p. 7</kb>`, citationReviewEvidenceBudget, true)
		item := reviewAsMap(reviewAsSlice(citationView["items"])[0])
		pointerRef := reviewInt(item["source_pointer_ref"])
		pointer := reviewAsSlice(citationView["source_pointers"])[pointerRef]
		resultRef := reviewInt(reviewAsSlice(pointer)[0])
		result := reviewAsSlice(citationView["source_results"])[resultRef]
		witness := reviewSourceWitness{EvidenceID: reviewAsSlice(result)[2].(string), JSONPointer: reviewAsSlice(pointer)[1].(string), Quote: "42 units", Kind: "source"}
		input := map[string]any{
			"citation_source_evidence": citationView,
			"source_tool_results":      []any{},
		}
		require.NoError(t, reviewWitnessVisible(input, reader, witness))
	})

	t.Run("document body pointer and observation", func(t *testing.T) {
		message := documentRetrievalTestMessage("read_document", "document-call", true, map[string]any{
			"chunks": []any{map[string]any{"chunk_id": "doc-chunk", "knowledge_id": "doc-id", "content": "The reported level was 18.2 metres."}},
		})
		messages := []chat.Message{message}
		reader := newReviewEvidenceReader(messages)
		view := documentRetrievalCompanion(messages, documentRetrievalCompanionBudget)
		observation := reviewAsMap(reviewAsSlice(view["observations"])[0])
		witness := reviewSourceWitness{EvidenceID: observation["evidence_id"].(string), JSONPointer: observation["json_pointer"].(string), Quote: "18.2 metres", Kind: "source"}
		require.NoError(t, reviewWitnessVisible(map[string]any{"source_tool_results": []any{}, "document_retrieval_companion": view}, reader, witness))
	})

	t.Run("structured rows reconstruct the actual positional representation", func(t *testing.T) {
		data := map[string]any{"status": "ok", "dataset_id": "dataset-real", "series": map[string]any{"series_id": "series-real"},
			"rows": []any{map[string]any{"period": "2026-Q1", "value": 7.25}}, "truncated": false}
		call, _ := structuredQueryFixture(t, "structured_query_series", "row-call", nil, data, true)
		state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
		engine := sourceConsistencyEngine(t, &mockChat{})
		messages := engine.sourceConsistencyMessages(state, nil)
		reader := newReviewEvidenceReader(messages)
		view := structuredQueryRowReviewEvidence(state, messages, structuredQueryRowReviewBudget)
		entry := reviewAsMap(reviewAsSlice(view["entries"])[0])
		witness := reviewSourceWitness{EvidenceID: entry["evidence_id"].(string), JSONPointer: "/data/rows/0/value", Quote: "7.25", Kind: "source"}
		require.NoError(t, reviewWitnessVisible(map[string]any{"source_tool_results": []any{}, "structured_query_row_companion": view}, reader, witness))
		require.NoError(t, reviewWitnessVisible(map[string]any{"source_tool_results": []any{}, "structured_query_row_companion": view}, reader, reviewSourceWitness{
			EvidenceID: entry["evidence_id"].(string), JSONPointer: "/data/rows/0/period", Quote: "2026-Q1", Kind: "source",
		}), "positional constant fields remain visible on every projected row")
	})
}

func TestReviewWitnessCompleteParentReadExposesChildOnlyAfterTranscript(t *testing.T) {
	record, reader, _ := receiptFixture(t, "parent-read", `{"success":true,"data":{"row":{"value":7.5,"unit":"kg"}}}`)
	witness := reviewSourceWitness{EvidenceID: record.ID, JSONPointer: "/data/row/unit", Quote: "kg", Kind: "source"}
	input := map[string]any{}
	args, _ := json.Marshal(map[string]any{"operation": "read", "evidence_id": record.ID, "json_pointer": "/data/row", "max_chars": 1000})
	response := reader.read(string(args))
	require.Error(t, reviewWitnessVisible(input, reader, witness), "executing a read without delivering its result is not exposure")
	encoded, _ := json.Marshal(response)
	reader.transcript = []chat.Message{
		{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: "parent-read-call", Function: chat.FunctionCall{Name: reviewEvidenceToolName, Arguments: string(args)}}}},
		{Role: "tool", Name: reviewEvidenceToolName, ToolCallID: "parent-read-call", Content: string(encoded)},
	}
	require.NoError(t, reviewWitnessVisible(input, reader, witness))
	witness.JSONPointer = "/data/row/value"
	witness.Quote = "kg"
	require.Error(t, reviewWitnessVisible(input, reader, witness), "a parent's other field cannot support a wrong child pointer")
}

func TestReviewContainerScalarQuoteRequiresCompleteScopedExposure(t *testing.T) {
	record, reader, raw := receiptFixture(t, "cell-object", `{"success":true,"data":{"cell":{"address":"B7","value":42.70,"raw_value":42.70,"unit":"kg"},"other":{"value":99}}}`)
	witness := reviewSourceWitness{EvidenceID: record.ID, JSONPointer: "/data/cell", Quote: "42.70", Kind: "source"}
	require.NoError(t, reviewWitnessVisible(receiptVisibleInput(record, raw), reader, witness))
	partial := receiptVisibleInput(record, map[string]any{"success": true, "data": map[string]any{"cell": map[string]any{"value": json.Number("42.70")}}})
	require.Error(t, reviewWitnessVisible(partial, reader, witness), "matching a scalar in a partial container does not expose the original container")
	appendHandoffRead(reader, "inspect-cell", record.ID, "/data/cell")
	require.NoError(t, reviewWitnessVisible(partial, reader, witness), "a real full container read restores the stated scope")
	witness.Quote = "99"
	require.Error(t, reviewWitnessVisible(receiptVisibleInput(record, raw), reader, witness), "another row's value cannot support the named container")
	witness.JSONPointer, witness.Quote = "/data/cell/value", "kg"
	require.Error(t, reviewWitnessVisible(receiptVisibleInput(record, raw), reader, witness), "a wrong scalar pointer is never repaired")
	witness.JSONPointer, witness.Quote = "/data/cell", `"value":42.70`
	require.Error(t, reviewWitnessVisible(receiptVisibleInput(record, raw), reader, witness), "serialized field fragments remain invalid")
}
