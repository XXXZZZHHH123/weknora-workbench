package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func TestPagedJSONEvidenceReachesReceiptAndRevisionOnlyWhenComplete(t *testing.T) {
	data := map[string]any{"states": []any{map[string]any{"name": "Measurements", "status": "needs_review"}}, "note": strings.Repeat("字段说明", 800), "number": json.Number("9007199254740993")}
	payload, err := json.Marshal(map[string]any{"success": true, "data": data})
	require.NoError(t, err)
	reader := newReviewEvidenceReader([]chat.Message{{Role: "tool", Name: "structured_data", ToolCallID: "observed-catalog", Content: string(payload)}})
	id := reader.records[0].ID
	witness := reviewSourceWitness{EvidenceID: id, JSONPointer: "/data/states/0/status", Quote: "needs_review", Kind: "source"}
	engine := &AgentEngine{}
	offset := 0
	for page := 0; page < 4; page++ {
		args, err := json.Marshal(map[string]any{"operation": "read", "evidence_id": id, "json_pointer": "/data", "offset": offset, "max_chars": 1500})
		require.NoError(t, err)
		callID := fmt.Sprintf("page-%d", page)
		reader.transcript = append(reader.transcript, chat.Message{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: callID, Type: "function", Function: chat.FunctionCall{Name: reviewEvidenceToolName, Arguments: string(args)}}}})
		response := reader.read(string(args))
		require.Equal(t, "ok", response["status"])
		body, err := json.Marshal(response)
		require.NoError(t, err)
		reader.transcript = append(reader.transcript, chat.Message{Role: "tool", Name: reviewEvidenceToolName, ToolCallID: callID, Content: string(body)})
		next, more := response["next_content_offset"].(int)
		if !more {
			break
		}
		require.Error(t, reviewWitnessVisible(nil, reader, witness), "a serialized partial parent does not establish arbitrary child paths")
		engine.captureReviewEvidence(reader)
		require.Nil(t, engine.turnReview.ReadEvidence)
		offset = next
	}
	require.NoError(t, reviewWitnessVisible(nil, reader, witness))
	engine.captureReviewEvidence(reader)
	require.NotNil(t, engine.turnReview.ReadEvidence)
	require.Len(t, engine.turnReview.ReadEvidence.Items, 1)
	item := engine.turnReview.ReadEvidence.Items[0]
	require.Equal(t, "/data", item.JSONPointer)
	require.True(t, item.FragmentComplete)
	require.True(t, reviewJSONValuesEqual(data, item.ExactFragment))
	require.NoError(t, reviewWitnessVisible(nil, reader, reviewSourceWitness{EvidenceID: id, JSONPointer: "/data/number", Quote: "9007199254740993", Kind: "source"}))
}
