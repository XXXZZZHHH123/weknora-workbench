package agent

import (
	"encoding/json"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
)

func TestNumericVerificationUsesCanonicalSuccessfulSourceMetadata(t *testing.T) {
	call := types.ToolCall{Name: "structured_list_series", Result: &types.ToolResult{Success: true, Output: `{"series":[{"series_id":"actual.quantity","sheet":"records","metric":"quantity","valid_count":1948,"record_count":2821,"unit":"kg"}]}`}}
	var canonical map[string]any
	require.NoError(t, json.Unmarshal([]byte(call.Result.Output), &canonical))
	call.Result.Data = canonical
	call.Result.Output = "display output abbreviated or formatted\n"
	steps := []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}
	messages := []chat.Message{{Role: "tool", Name: "structured_list_series", Content: "Payload abbreviated for model context."}}
	require.Empty(t, ungroundedStructuredNumbers("有效记录1948条，原始记录2821条。", messages, steps))
	require.Contains(t, ungroundedStructuredNumbers("合计7788。", messages, steps), "7788")
	call.Result.Success = false
	require.Contains(t, ungroundedStructuredNumbers("有效记录1948条。", []chat.Message{{Role: "tool", Content: `{"value":11}`}}, []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}), "1948")
}

func TestToolSchemasAndDocumentJSONCannotAuthorizeStatistics(t *testing.T) {
	for _, name := range []string{"read_document", "search_knowledge", "structured_data"} {
		call := types.ToolCall{Name: name, Args: map[string]any{"operation": "describe_tools"}, Result: &types.ToolResult{Success: true, Output: `{"maximum":777,"value":888}`}}
		require.Contains(t, ungroundedStructuredNumbers("合计888。", []chat.Message{{Role: "tool", Content: `{"value":11}`}}, []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}), "888")
	}
}

func TestOptionalRecordedCanonicalNumericVerification(t *testing.T) {
	path := os.Getenv("STRUCTURED_NUMERIC_REPLAY")
	if path == "" {
		t.Skip("isolated live replay opt-in")
	}
	encoded, err := os.ReadFile(path)
	require.NoError(t, err)
	var fixture struct {
		Answer string            `json:"answer"`
		Steps  []types.AgentStep `json:"steps"`
	}
	require.NoError(t, json.Unmarshal(encoded, &fixture))
	require.Empty(t, ungroundedStructuredNumbers(fixture.Answer, nil, fixture.Steps))
}
