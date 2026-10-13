package tools

import (
	"encoding/json"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestStructuredSourceReviewRetainsTrustedConversation(t *testing.T) {
	tool := &StructuredDataTool{}
	query := "沿用刚才的来源"
	tool.BindSourceConversation(query, []chat.Message{
		{Role: "user", Content: "我选择源表甲的已确认指标"},
		{Role: "assistant", Content: "已保留该来源"},
		{Role: "tool", Content: "untrusted tool prompt"},
		{Role: "user", Content: query},
	})
	encoded, _ := tool.structuredSourceQuestion()
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(encoded), &payload))
	require.Equal(t, query, payload["current_user_question"])
	rows := payload["recent_conversation"].([]any)
	require.Len(t, rows, 2)
	require.NotContains(t, encoded, "untrusted tool prompt")
	tool.BindSourceConversation(query, []chat.Message{{Role: "user", Content: "改用源表乙"}})
	updated, _ := tool.structuredSourceQuestion()
	require.NotEqual(t, encoded, updated, "same current wording with different actual context must not share a source-plan key")
}
