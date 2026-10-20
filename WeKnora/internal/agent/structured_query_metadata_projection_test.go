package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestStructuredQueryMetadataProjectionKeepsRowsAndExactSupplementPaths(t *testing.T) {
	state := &types.AgentState{RoundSteps: []types.AgentStep{{}}}
	messages := make([]chat.Message, 0, 3)
	metadataText := strings.Repeat("metadata-value-", 220) + "hidden-metadata-tail-token"
	for i := 0; i < 3; i++ {
		data := map[string]any{
			"status": "ok", "operation": "query_series", "dataset_id": fmt.Sprintf("dataset-%d", i),
			"series":     map[string]any{"series_id": fmt.Sprintf("series-%d", i), "unit": "units"},
			"large/meta": metadataText,
			"rows": []any{
				map[string]any{"date": "2024-01-01", "value": json.Number("10.25")},
				map[string]any{"date": "2024-02-01", "value": json.Number("11.25")},
			},
		}
		call, message := structuredQueryFixture(t, "structured_query_series", fmt.Sprintf("query-%d", i), map[string]any{"operation": "query_series"}, data, true)
		state.RoundSteps[0].ToolCalls = append(state.RoundSteps[0].ToolCalls, call)
		messages = append(messages, message)
	}

	view := structuredQueryRowReviewEvidence(state, messages, structuredQueryRowReviewBudget)
	entries, size := decodeStructuredQueryCompanion(t, view)
	viewEntries, ok := view["entries"].([]structuredQueryRowCompanionEntry)
	require.True(t, ok, "projection fixture should retain its typed entry slice")
	require.LessOrEqual(t, size, structuredQueryRowReviewBudget)
	require.Len(t, entries, 3)
	require.False(t, view["projection_complete"].(bool), "partial metadata must not be reported as a complete projection")
	metadataBytes := 0
	reader := newReviewEvidenceReader(messages)
	input := map[string]any{"structured_query_row_companion": view}
	for i, entry := range entries {
		require.Greater(t, entry.RetainedRowCount, 0, "oversized metadata must not suppress all visible rows")
		require.False(t, entry.MetadataProjectionComplete)
		require.Contains(t, entry.MetadataOmittedPointers, "/data/large~1meta")
		require.Equal(t, "ok", entry.Metadata["status"], "small metadata should remain exact")
		encodedMetadata, err := json.Marshal(entry.Metadata)
		require.NoError(t, err)
		metadataBytes += len(encodedMetadata)

		witness := reviewSourceWitness{Kind: "source", RowRef: &reviewStructuredRowRef{EntryIndex: i, RowIndex: 0, Column: "value"}}
		require.NoError(t, reviewWitnessVisible(input, reader, witness))
		metadataWitness := reviewSourceWitness{EvidenceID: entry.EvidenceID, JSONPointer: "/data/status", Quote: "ok", Kind: "source"}
		require.NoError(t, reviewWitnessVisible(input, reader, metadataWitness), "an exact displayed metadata value remains usable even when other metadata was omitted")

		hiddenWitness := reviewSourceWitness{EvidenceID: entry.EvidenceID, JSONPointer: "/data/large~1meta", Quote: "hidden-metadata-tail-token", Kind: "source"}
		require.Error(t, reviewWitnessVisible(input, reader, hiddenWitness), "undisplayed metadata suffixes still require an exact read")
		metadataMap := viewEntries[i].Metadata
		originalStatus := metadataMap["status"]
		metadataMap["status"] = "different-value"
		require.Equal(t, "different-value", viewEntries[i].Metadata["status"], "mutate the metadata map actually passed as reviewer input")
		require.Error(t, reviewWitnessVisible(input, reader, metadataWitness), "a projection value cannot stand in for a different immutable source value")
		metadataMap["status"] = originalStatus

		readCallID := fmt.Sprintf("read-metadata-%d", i)
		readArguments := fmt.Sprintf(`{"operation":"read","evidence_id":%q,"json_pointer":"/data/large~1meta","max_chars":4000}`, entry.EvidenceID)
		read := reader.read(readArguments)
		require.Equal(t, "ok", read["status"])
		require.Equal(t, metadataText, read["text"], "omitted metadata is recoverable only through its exact immutable source path")
		readResponse, err := json.Marshal(read)
		require.NoError(t, err)
		reader.transcript = append(reader.transcript,
			chat.Message{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: readCallID, Type: "function", Function: chat.FunctionCall{Name: reviewEvidenceToolName, Arguments: readArguments}}}},
			chat.Message{Role: "tool", Name: reviewEvidenceToolName, ToolCallID: readCallID, Content: string(readResponse)},
		)
		require.NoError(t, reviewWitnessVisible(input, reader, hiddenWitness), "the same exact path becomes visible after a successful reader result is in the transcript")
	}
	require.LessOrEqual(t, metadataBytes, structuredQueryRowReviewBudget/structuredQueryRowMetadataBudgetFraction)
}

func TestStructuredQueryMetadataProjectionDoesNotExposeHiddenRows(t *testing.T) {
	rows := make([]any, 1800)
	for i := range rows {
		rows[i] = map[string]any{"date": fmt.Sprintf("period-%04d", i), "value": json.Number(fmt.Sprint(i))}
	}
	data := map[string]any{"status": "ok", "operation": "query_series", "series": map[string]any{"unit": "units"}, "rows": rows}
	call, message := structuredQueryFixture(t, "structured_query_series", "large-rows", map[string]any{"operation": "query_series"}, data, true)
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
	view := structuredQueryRowReviewEvidence(state, []chat.Message{message}, structuredQueryRowReviewBudget)
	entries, size := decodeStructuredQueryCompanion(t, view)
	require.LessOrEqual(t, size, structuredQueryRowReviewBudget)
	require.Len(t, entries, 1)
	entry := entries[0]
	require.Greater(t, entry.RetainedRowCount, 0)
	require.Less(t, entry.RetainedRowCount, len(rows))

	reader := newReviewEvidenceReader([]chat.Message{message})
	input := map[string]any{"structured_query_row_companion": view}
	visible := reviewSourceWitness{Kind: "source", RowRef: &reviewStructuredRowRef{EntryIndex: 0, RowIndex: entry.RetainedRowCount - 1, Column: "value"}}
	require.NoError(t, reviewWitnessVisible(input, reader, visible))
	hidden := reviewSourceWitness{Kind: "source", RowRef: &reviewStructuredRowRef{EntryIndex: 0, RowIndex: entry.RetainedRowCount, Column: "value"}}
	require.Error(t, reviewWitnessVisible(input, reader, hidden), "row_ref cannot promote an omitted row into displayed evidence")
}
