package agent

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestUnresolvedSourceBindingRetainsExactCandidateIdentityOnly(t *testing.T) {
	data := map[string]any{
		"kind": "structured_source_binding", "status": "needs_clarification",
		"fields": []any{[]any{map[string]any{
			"series_id": "observed.field.CONTRACT17", "metric": "分级42原料",
			"dimensions":  map[string]any{"contract": "CONTRACT17.EX"},
			"valid_count": 9876,
		}}},
	}
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	baseline := []chat.Message{{Role: "tool", Name: "structured_query_series", Content: `{"status":"ok","value":13}`}}
	answer := "实际候选为分级42原料，合约CONTRACT17.EX，来源observed.field.CONTRACT17；尚未选择口径。"
	for _, test := range []struct {
		name     string
		messages []chat.Message
		steps    []types.AgentStep
	}{
		{
			name:     "canonical result",
			messages: baseline,
			steps: []types.AgentStep{{ToolCalls: []types.ToolCall{{
				Name: "structured_analyze_table", Result: &types.ToolResult{Success: true, Data: data, Output: string(raw)},
			}}}},
		},
		{
			name:     "model context result",
			messages: append(append([]chat.Message(nil), baseline...), chat.Message{Role: "tool", Name: "structured_analyze_table", Content: string(raw)}),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Empty(t, ungroundedStructuredNumbers(answer, test.messages, test.steps))
			for _, claim := range []string{"最大值42", "共有17条", "有效记录9876条", "CONTRACT17.EX9", "分级42原料9"} {
				require.NotEmpty(t, ungroundedStructuredNumbers(answer+claim, test.messages, test.steps), claim)
			}
		})
	}
	for _, success := range []bool{false, true} {
		failed := data
		if success {
			failed = map[string]any{"kind": data["kind"], "status": "error", "fields": data["fields"]}
		}
		steps := []types.AgentStep{{ToolCalls: []types.ToolCall{{Name: "structured_analyze_table", Result: &types.ToolResult{Success: success, Data: failed}}}}}
		require.NotEmpty(t, ungroundedStructuredNumbers(answer, baseline, steps), "a failed result cannot establish an identity")
	}
}

func TestExactCatalogueIdentitiesDoNotApproveShorthandOrInventedCounts(t *testing.T) {
	result := `{"total":13,"series":[{"series_id":"observed.price.CONTRACT17","metric":"分级42原料","dimensions":{"contract":"CONTRACT17.EX"}}]}`
	messages := []chat.Message{{Role: "tool", Name: "structured_list_series", Content: result}}
	require.Empty(t, ungroundedStructuredNumbers("目录total=13；完整表头分级42原料，合约CONTRACT17.EX。", messages))
	for _, claim := range []string{"目录total=28", "分级代码42", "简称CONTRACT17", "合计17", "来源CONTRACT17.EX9"} {
		require.NotEmpty(t, ungroundedStructuredNumbers(claim, messages), claim)
	}
}
