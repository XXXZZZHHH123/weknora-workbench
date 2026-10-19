package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
)

func TestSourceCriticConversationExcludesCurrentPlansAndKeepsHumanJSON(t *testing.T) {
	humanJSON := `{"unverified_draft":"this is a real human request, not a runtime envelope"}`
	messages := []chat.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "Compare the watershed surveys."},
		{Role: "assistant", Content: "Earlier delivered answer with source identity."},
		{Role: "user", Content: humanJSON},
		{Role: "assistant", Content: "CURRENT_INTERNAL_PLAN"},
		{Role: "tool", Content: "ACTUAL_TOOL_RESULT"},
	}
	m := &mockChat{responses: []mockResponse{sourceAcceptedVerdict()}}
	e := sourceConsistencyEngine(t, m)
	_, err := e.evaluateAnswerCompletenessOnce(t.Context(), humanJSON, "Candidate answer.", sourceConsistencyState(), 0, messages)
	require.NoError(t, err)
	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(m.calls[0][1].Content), &input))
	rows := input["recent_conversation"].([]any)
	require.Len(t, rows, 3)
	encoded, _ := json.Marshal(rows)
	require.Contains(t, string(encoded), "Earlier delivered answer")
	require.Contains(t, rows[2].(map[string]any)["content"], "this is a real human request")
	require.NotContains(t, string(encoded), "CURRENT_INTERNAL_PLAN")
	require.NotContains(t, string(encoded), "ACTUAL_TOOL_RESULT")
	require.Contains(t, m.calls[0][1].Content, "覆盖面积", "real source evidence remains independently available")
}

func TestReviewConversationViewBoundsAndExplicitOmissions(t *testing.T) {
	messages := []chat.Message{
		{Role: "user", Content: "Old question"},
		{Role: "assistant", Content: "Old delivered answer"},
		{Role: "user", Content: strings.Repeat("流域", 20)},
		{Role: "assistant", Content: "Current draft"},
	}
	rows, omitted := reviewConversationView(messages, 12, 2)
	require.Len(t, rows, 1)
	require.False(t, rows[0]["view_complete"].(bool))
	require.Equal(t, 2, omitted)
	require.NotContains(t, rows[0]["content"], "Current draft")
	rows, omitted = reviewConversationView(messages, 1000, 2)
	require.Len(t, rows, 2)
	require.Equal(t, 1, omitted)
}
