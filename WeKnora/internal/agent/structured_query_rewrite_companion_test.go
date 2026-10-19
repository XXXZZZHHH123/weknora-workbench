package agent

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestIndependentRewriteGetsBoundedQueryRowsAndResolvableSourceHandle(t *testing.T) {
	rows := make([]any, 0, 40)
	for i := 0; i < 40; i++ {
		rows = append(rows, map[string]any{
			"period":       "2025-Q" + string(rune('1'+i%4)),
			"station":      "watershed-site-" + string(rune('A'+i%3)),
			"measurement":  float64(i)*1.25 + 0.5,
			"source_cell":  fmt.Sprintf("C%d", i+2),
			"value_status": "valid",
		})
	}
	data := map[string]any{
		"status": "ok", "dataset_id": "dataset-research", "series": map[string]any{"series_id": "series-research", "unit": "units"},
		"filters":      map[string]any{"start_date": "2025-01-01", "end_date": "2025-12-31"},
		"record_count": 120, "valid_count": 40, "truncated": true, "rows": rows,
	}
	call, rawToolMessage := structuredQueryFixture(t, "structured_query_series", "watershed-query-call", map[string]any{"limit": 40}, data, true)
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
	engine := sourceConsistencyEngine(t, &mockChat{})
	rowSourceMessages := engine.sourceConsistencyMessages(state, nil)
	companion := structuredQueryRowReviewEvidence(state, rowSourceMessages, structuredQueryRowReviewBudget)
	require.NotNil(t, companion)

	conversation := []chat.Message{{Role: "user", Content: "Summarize the observations from the field study."}}
	messages := []chat.Message{
		{Role: "system", Content: "System instructions."},
		{Role: "assistant", Content: "I will inspect the query output."},
		rawToolMessage,
	}
	review := independentStructuredReviewWithCatalogAndRowCompanion(
		messages, conversation, nil, rowSourceMessages, companion,
		"Summarize the observations from the field study.", "Unverified draft.",
	)
	require.Len(t, review, 2)

	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(review[1].Content), &input))
	projected := input["structured_query_row_companion"].(map[string]any)
	encoded, err := json.Marshal(projected)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), structuredQueryRowReviewBudget)
	entries := projected["entries"].([]any)
	require.Len(t, entries, 1)
	entry := entries[0].(map[string]any)
	require.Equal(t, "positional", entry["row_encoding"])
	require.Equal(t, true, entry["projection_complete"], "all rows returned by this query fit in the companion")
	require.Equal(t, true, entry["source_result_truncated"], "projection completeness does not imply complete source coverage")
	require.Equal(t, "units", entry["metadata"].(map[string]any)["series"].(map[string]any)["unit"])
	require.Contains(t, input["structured_query_row_companion_notice"], "不证明查询范围覆盖整个来源")
	require.Contains(t, input["structured_query_row_companion_notice"], "不自行提供读取权限")

	companionEvidenceID := entry["evidence_id"]
	toolResults := input["source_tool_results"].([]any)
	matched := false
	for _, raw := range toolResults {
		toolResult := raw.(map[string]any)
		if toolResult["evidence_id"] == companionEvidenceID {
			matched = true
			content := toolResult["content"].(map[string]any)
			dataView := content["data"].(map[string]any)
			require.Equal(t, float64(40), float64(len(dataView["rows"].([]any))))
			break
		}
	}
	require.True(t, matched, "the companion handle must resolve to this exact real result in source_tool_results")
	require.NotContains(t, string(encoded), "unverified draft")
}

func TestIndependentRewriteOmitsRowCompanionWhenHandleCannotBeVerified(t *testing.T) {
	rows := []any{map[string]any{"period": "2025-Q1", "reading": 9.5}}
	data := map[string]any{"status": "ok", "dataset_id": "dataset-real", "series": map[string]any{"series_id": "series-real"}, "rows": rows, "truncated": false}
	call, _ := structuredQueryFixture(t, "structured_query_series", "real-query-call", nil, data, true)
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
	engine := sourceConsistencyEngine(t, &mockChat{})
	actualSourceMessages := engine.sourceConsistencyMessages(state, nil)
	companion := structuredQueryRowReviewEvidence(state, actualSourceMessages, structuredQueryRowReviewBudget)
	require.NotNil(t, companion)

	conversation := []chat.Message{{Role: "user", Content: "Describe the measurements."}}
	review := independentStructuredReviewWithCatalogAndRowCompanion(
		[]chat.Message{{Role: "system", Content: "System."}}, conversation, nil,
		actualSourceMessages[:0], companion, "Describe the measurements.", "Draft.",
	)
	var input map[string]any
	require.NoError(t, json.Unmarshal([]byte(review[1].Content), &input))
	require.NotContains(t, input, "structured_query_row_companion", "unresolvable synthetic handles must not introduce detached row facts")
}
