package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

// Synthetic rows only; dates and values are fixture data, not acceptance facts.
func arrayIndexReviewFixture(t *testing.T, rowCount int) (*reviewEvidenceReader, string) {
	t.Helper()
	rows := make([]string, rowCount)
	for i := range rows {
		// Every row repeats one value so a wrong row can carry the "right" literal.
		value := "100"
		if i == rowCount-7 {
			value = "250"
		}
		rows[i] = fmt.Sprintf(`{"date":"2000-01-%03d","value":%s,"source_cell":"B%d"}`, i, value, i+2)
	}
	message := chat.Message{Role: "tool", Name: "structured_data", ToolCallID: "rows-call",
		Content: `{"success":true,"data":{"rows":[` + strings.Join(rows, ",") + `],"truncated":false}}`}
	reader := newReviewEvidenceReader([]chat.Message{message})
	return reader, reader.records[0].ID
}

func appendReviewRead(t *testing.T, reader *reviewEvidenceReader, args map[string]any) map[string]any {
	t.Helper()
	callID := fmt.Sprintf("read-%d", len(reader.transcript))
	encodedArgs, err := json.Marshal(args)
	require.NoError(t, err)
	output := reader.read(string(encodedArgs))
	encodedOutput, err := json.Marshal(output)
	require.NoError(t, err)
	reader.transcript = append(reader.transcript,
		chat.Message{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: callID, Type: "function", Function: chat.FunctionCall{Name: reviewEvidenceToolName, Arguments: string(encodedArgs)}}}},
		chat.Message{Role: "tool", Name: reviewEvidenceToolName, ToolCallID: callID, Content: string(encodedOutput)})
	return output
}

func TestReviewEvidenceArrayRejectsCharacterOffsetAsRowIndex(t *testing.T) {
	reader, evidenceID := arrayIndexReviewFixture(t, 300)
	remaining := reader.remaining
	target := 300 - 7

	// A row number from omitted_row_ranges used as "offset" previously paged
	// serialized JSON characters and exposed early rows instead of the target.
	output := appendReviewRead(t, reader, map[string]any{"operation": "read", "evidence_id": evidenceID, "json_pointer": "/data/rows", "offset": target})
	require.Equal(t, "invalid_offset_space", output["status"])
	require.Equal(t, 300, output["array_length"])
	require.NotContains(t, output, "items")
	require.NotContains(t, output, "serialized_json_segment")
	require.Equal(t, remaining, reader.remaining)

	witness := reviewSourceWitness{EvidenceID: evidenceID, JSONPointer: fmt.Sprintf("/data/rows/%d/value", target), Quote: "250", Kind: "source"}
	require.False(t, reviewWitnessVisibleInTranscript(reader, witness, json.Number("250")))

	// max_chars is also a character budget and cannot select array elements.
	output = appendReviewRead(t, reader, map[string]any{"operation": "read", "evidence_id": evidenceID, "json_pointer": "/data/rows", "max_chars": 4000})
	require.Equal(t, "invalid_offset_space", output["status"])
}

func TestReviewEvidenceArrayReadsExactElementsByIndex(t *testing.T) {
	reader, evidenceID := arrayIndexReviewFixture(t, 300)
	target := 300 - 7
	output := appendReviewRead(t, reader, map[string]any{"operation": "read", "evidence_id": evidenceID, "json_pointer": "/data/rows", "start_index": target - 1, "max_items": 3})
	require.Equal(t, "ok", output["status"])
	require.Equal(t, "array_index", output["offset_space"])
	require.Equal(t, target-1, output["start_index"])
	require.Equal(t, target+2, output["end_index_exclusive"])
	require.Equal(t, target+2, output["next_start_index"])
	require.Equal(t, false, output["segment_complete"])
	items := output["items"].([]map[string]any)
	require.Len(t, items, 3)
	require.Equal(t, target, items[1]["index"])
	require.Equal(t, fmt.Sprintf("/data/rows/%d", target), items[1]["json_pointer"])
	row := items[1]["value"].(map[string]any)
	require.Equal(t, json.Number("250"), row["value"])
	require.Equal(t, fmt.Sprintf("2000-01-%03d", target), row["date"])

	visible := reviewSourceWitness{EvidenceID: evidenceID, JSONPointer: fmt.Sprintf("/data/rows/%d/value", target), Quote: "250", Kind: "source"}
	require.True(t, reviewWitnessVisibleInTranscript(reader, visible, json.Number("250")))
	// A read of rows N-1..N+1 does not expose an unread row with an equal literal.
	sibling := reviewSourceWitness{EvidenceID: evidenceID, JSONPointer: "/data/rows/5/value", Quote: "100", Kind: "source"}
	require.False(t, reviewWitnessVisibleInTranscript(reader, sibling, json.Number("100")))
	neighbour := reviewSourceWitness{EvidenceID: evidenceID, JSONPointer: fmt.Sprintf("/data/rows/%d/value", target+1), Quote: "100", Kind: "source"}
	require.True(t, reviewWitnessVisibleInTranscript(reader, neighbour, json.Number("100")))
}

func TestReviewEvidenceArrayReadHandoffKeepsExactElementPointers(t *testing.T) {
	reader, evidenceID := arrayIndexReviewFixture(t, 40)
	appendReviewRead(t, reader, map[string]any{"operation": "read", "evidence_id": evidenceID, "json_pointer": "/data/rows", "start_index": 30, "max_items": 2})
	engine := &AgentEngine{}
	engine.captureReviewEvidence(reader)
	require.NotNil(t, engine.turnReview.ReadEvidence)
	require.Len(t, engine.turnReview.ReadEvidence.Items, 2)
	require.Equal(t, "/data/rows/30", engine.turnReview.ReadEvidence.Items[0].JSONPointer)
	require.Equal(t, "/data/rows/31", engine.turnReview.ReadEvidence.Items[1].JSONPointer)
	require.True(t, engine.turnReview.ReadEvidence.Items[0].FragmentComplete)
}

func TestReviewEvidenceArrayReadRejectsForgedOrMismatchedItems(t *testing.T) {
	reader, evidenceID := arrayIndexReviewFixture(t, 40)
	// A transcript item whose value differs from the immutable source cannot
	// expose that path, even if it claims the right pointer.
	forged := map[string]any{"status": "ok", "evidence_id": evidenceID, "offset_space": "array_index",
		"items": []any{map[string]any{"index": 3, "json_pointer": "/data/rows/3", "value": map[string]any{"date": "2000-01-003", "value": json.Number("999"), "source_cell": "B5"}}}}
	encoded, err := json.Marshal(forged)
	require.NoError(t, err)
	reader.transcript = append(reader.transcript,
		chat.Message{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: "forged", Type: "function", Function: chat.FunctionCall{Name: reviewEvidenceToolName, Arguments: `{"operation":"read"}`}}}},
		chat.Message{Role: "tool", Name: reviewEvidenceToolName, ToolCallID: "forged", Content: string(encoded)})
	witness := reviewSourceWitness{EvidenceID: evidenceID, JSONPointer: "/data/rows/3/value", Quote: "999", Kind: "source"}
	require.False(t, reviewWitnessVisibleInTranscript(reader, witness, json.Number("999")))
}

func TestReviewEvidenceIndexArgumentsOnlyApplyToArrays(t *testing.T) {
	reader, evidenceID := arrayIndexReviewFixture(t, 10)
	output := appendReviewRead(t, reader, map[string]any{"operation": "read", "evidence_id": evidenceID, "json_pointer": "/data", "start_index": 1})
	require.Equal(t, "invalid_arguments", output["status"])
	output = appendReviewRead(t, reader, map[string]any{"operation": "list", "start_index": 1})
	require.Equal(t, "invalid_arguments", output["status"])
	// Objects and text keep Unicode character paging.
	output = appendReviewRead(t, reader, map[string]any{"operation": "read", "evidence_id": evidenceID, "json_pointer": "/data/rows/2", "offset": 1, "max_chars": 10})
	require.Equal(t, "ok", output["status"])
	require.Equal(t, "unicode_serialized_json", output["offset_space"])
}

func TestStructuredRowOmissionUsesElementIndexContinuation(t *testing.T) {
	encoded, err := json.Marshal(rowOmissionRanges("/data/rows", 12, 40))
	require.NoError(t, err)
	require.JSONEq(t, `[{"json_pointer":"/data/rows","start_index":12,"end_index_exclusive":40,"next_start_index":12}]`, string(encoded))
}
