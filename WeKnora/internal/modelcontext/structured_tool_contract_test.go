package modelcontext

import (
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestCurrentStructuredContractsOwnOnlyRootSelectorAndSnapshotRegistrations(t *testing.T) {
	r := NewRegistry(true)
	handle := r.RegisterKnowledgeBase("kb-actual")
	contracts := map[string]bool{"native_read": true, "structured_query_series": false}
	r.SetStructuredToolSourceContracts(contracts)
	contracts["native_read"] = false // The caller cannot mutate the installed snapshot.
	original := ` { "knowledge_base_id" : "` + handle + `", "sql":"SELECT '` + handle + `'", "data":{"knowledge_base_id":"` + handle + `"} } `
	metadata := types.ToolCallMetadata{"provider": json.RawMessage(`{"opaque":"retained"}`)}
	calls := []types.LLMToolCall{{ProviderMetadata: metadata, Function: types.FunctionCall{Name: "native_read", Arguments: original}}}
	r.DecodeToolCalls(calls)
	require.JSONEq(t, `{"knowledge_base_id":"kb-actual","sql":"SELECT '`+handle+`'","data":{"knowledge_base_id":"`+handle+`"}}`, calls[0].Function.Arguments)
	require.Equal(t, original, calls[0].ModelArguments)
	require.Equal(t, metadata, calls[0].ProviderMetadata)
	require.Empty(t, calls[0].UnresolvedHandles)
	result := `{"knowledge_base_id":"kb-actual","rows":[["` + handle + `"]]}`
	require.Equal(t, result, r.ModelToolResultForTool("native_read", &types.ToolResult{Success: true, Output: result}), "native result payloads remain opaque")

	historyArgs := ` { "knowledge_base_id" : "kb-actual", "data":{"knowledge_base_id":"` + handle + `"} } `
	history := []chat.Message{{Role: "assistant", ToolCalls: []chat.ToolCall{{ID: "old-pair", Function: chat.FunctionCall{Name: "native_read", Arguments: historyArgs}}}}, {Role: "tool", Name: "native_read", ToolCallID: "old-pair", Content: result}}
	encoded := r.EncodeMessages(history)
	require.Equal(t, historyArgs, history[0].ToolCalls[0].Function.Arguments, "stored original bytes are not rewritten")
	require.Equal(t, result, history[1].Content)
	require.JSONEq(t, `{"knowledge_base_id":"`+handle+`","data":{"knowledge_base_id":"`+handle+`"}}`, encoded[0].ToolCalls[0].Function.Arguments)
	require.Equal(t, "old-pair", encoded[1].ToolCallID)

	for _, name := range []string{"structured_query_series", "structured_unregistered"} {
		call := []types.LLMToolCall{{Function: types.FunctionCall{Name: name, Arguments: `{"knowledge_base_id":"` + handle + `"}`}}}
		r.DecodeToolCalls(call)
		require.NotContains(t, call[0].Function.Arguments, `"knowledge_base_id":"kb-actual"`, "a current external/unknown tool must not inherit bare-ID ownership")
	}
	r.SetStructuredToolSourceContracts(map[string]bool{"native_read": false})
	call := []types.LLMToolCall{{Function: types.FunctionCall{Name: "native_read", Arguments: `{"knowledge_base_id":"` + handle + `"}`}}}
	r.DecodeToolCalls(call)
	require.NotContains(t, call[0].Function.Arguments, `"knowledge_base_id":"kb-actual"`, "refresh replaces rather than accumulates contracts")
	legacy := []types.LLMToolCall{{Function: types.FunctionCall{Name: "structured_query_series", Arguments: `{"knowledge_base_id":"` + handle + `"}`}}}
	r.DecodeToolCalls(legacy)
	require.JSONEq(t, `{"knowledge_base_id":"kb-actual"}`, legacy[0].Function.Arguments, "an omitted historical name retains the existing legacy policy")
}
