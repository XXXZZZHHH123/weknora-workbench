package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// A catalogue match is evidence for the model, never a programmatic decision
// that this human question requires a particular canned answer or workflow.
func TestMatchingCatalogueFieldStillReachesModel(t *testing.T) {
	for _, metric := range []string{"货物数量", "铝供应", "warehouse_qty"} {
		t.Run(metric, func(t *testing.T) {
			registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) {
				var input map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
				if input["operation"] != "list_datasets" {
					_ = json.NewEncoder(w).Encode(map[string]any{"tools": []any{}})
					return
				}
				fields := []any{}
				for _, column := range []string{"A", "B"} {
					fields = append(fields, map[string]any{"dataset_id": "version", "series_id": "field-" + column,
						"source_file": "records.xlsx", "sheet": "records", "metric": metric, "unit": "kg",
						"source_context": map[string]any{"source_column": column}})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"literal_field_count": len(fields),
					"literal_fields_truncated": false, "literal_fields": fields})
			})
			model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{
				ResponseType: types.ResponseTypeAnswer, Content: "模型根据完整问题与真实证据生成的回复。", Done: true, FinishReason: "stop",
			}}}}}
			e := newTestEngine(t, model)
			e.toolRegistry, e.config.WorkbenchDefault = registry, true
			ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "user", Role: types.TenantRoleViewer})
			state, err := e.Execute(ctx, "session", "message", metric, nil)
			require.NoError(t, err)
			require.Len(t, model.calls, 1, "no direct answer based on literal equality")
			require.Equal(t, "模型根据完整问题与真实证据生成的回复。", state.FinalAnswer)
			found := false
			for _, message := range model.calls[0] {
				found = found || (message.Role == "tool" && message.Name == "structured_data" && message.Content != "")
			}
			require.True(t, found, "actual catalogue facts remain available to the model")
			require.Contains(t, e.RenderUserTurnContent("session", metric), metric)
		})
	}
}

func TestPeriodWordingDoesNotInjectSourceReads(t *testing.T) {
	operations := []string{}
	var mu sync.Mutex
	registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		var input map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
		mu.Lock()
		operations = append(operations, input["operation"].(string))
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"dataset_id": "version", "series": []any{map[string]any{"series_id": "field", "sheet": "records", "metric": "observations"}}})
	})
	model := &mockChat{responses: []mockResponse{
		{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeToolCall, ToolCalls: []types.LLMToolCall{{ID: "catalog", Function: types.FunctionCall{Name: "structured_data", Arguments: `{"operation":"list_series","arguments":{"dataset_id":"version"}}`}}}, Done: true, FinishReason: "tool_calls"}}},
		{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: "模型尚未决定采用哪项时间口径。", Done: true, FinishReason: "stop"}}},
	}}
	engine := newTestEngine(t, model)
	engine.toolRegistry, engine.config.WorkbenchDefault = registry, true
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "reader", Role: types.TenantRoleViewer})
	_, err := engine.Execute(ctx, "session", "message", "2024/25年度，但别改我的来源", nil)
	require.NoError(t, err)
	require.Contains(t, operations, "list_series")
	require.NotContains(t, operations, "inspect_source", "source reads must come from a model plan, never date-pattern triggers")
}
