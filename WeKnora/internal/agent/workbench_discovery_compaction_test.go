package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func discoveryCompactionFixture(t *testing.T) map[string]any {
	t.Helper()
	pending := []any{"明确指定待核对表"}
	for i := 0; i < 110; i++ {
		pending = append(pending, fmt.Sprintf("尚未核对的其他业务工作表_%03d_棉花供需口径", i))
	}
	candidates := []any{}
	for _, column := range []string{"C", "F", "U", "X"} {
		candidates = append(candidates, map[string]any{"series_id": "actual." + column, "metric": "20天原料", "sheet": "纱布利润", "unit": "元/吨", "dimensions": map[string]any{"原表列": column},
			"source_context": map[string]any{"headers": []any{map[string]any{"cell": column + "1", "value": "20天原料"}}, "formula_source": map[string]any{"formula": "=原表公式", "references": []any{map[string]any{"source_cell": column + "6", "metric": "对应来源"}}}}, "coverage": map[string]any{"first_date": "2016-01-01", "last_date": "2016-04-07", "missing_dates": []any{"2016-01-02"}}})
	}
	return map[string]any{"datasets": []any{map[string]any{"dataset_id": "authorized-version", "source_file": "真实业务.xlsx", "is_latest_for_source": true,
		"pending_sheets": pending, "sheets": []any{"纱布利润"}, "coverage_notice": "未核对不等于不存在。已导入sheet也仅覆盖parsed_range。", "coverage": map[string]any{"parsed_range": "A1:X100"}}},
		"literal_fields": candidates, "literal_field_matches": candidates, "literal_field_count": 4, "literal_fields_truncated": false, "literal_field_notice": "同名列必须对照来源，不能选择第一列。", "truncated": false}
}

func discoveryTestCall(t *testing.T, operation string, data map[string]any) types.ToolCall {
	t.Helper()
	encoded, err := json.Marshal(data)
	require.NoError(t, err)
	return types.ToolCall{ID: operation, Name: agenttools.ToolStructuredData, Args: map[string]any{"operation": operation}, Result: &types.ToolResult{Success: true, Output: string(encoded), Data: data}}
}

func TestWorkbenchDiscoveryCompactionKeepsAllCandidatesAndImmutableRawEvidence(t *testing.T) {
	catalogue := discoveryCompactionFixture(t)
	step := types.AgentStep{ToolCalls: []types.ToolCall{discoveryTestCall(t, "list_datasets", catalogue)}}
	before, _ := json.Marshal(step)
	compact := compactWorkbenchDiscovery(step, "请问明确指定待核对表里的20天原料？", nil)
	after, _ := json.Marshal(step)
	require.Equal(t, string(before), string(after), "persisted results must retain the full original response")
	model := compact.ToolCalls[0].Result.Data
	for _, key := range []string{"literal_fields", "literal_field_matches", "literal_field_count", "literal_fields_truncated", "literal_field_notice", "truncated"} {
		require.Equal(t, catalogue[key], model[key], key)
	}
	dataset := model["datasets"].([]any)[0].(map[string]any)
	require.Equal(t, []any{"明确指定待核对表"}, dataset["pending_sheets"])
	require.Equal(t, 111, dataset["pending_sheet_count"])
	require.Equal(t, 110, dataset["pending_sheets_omitted_count"])
	require.Equal(t, catalogue["datasets"].([]any)[0].(map[string]any)["coverage"], dataset["coverage"])
	require.Contains(t, model["pending_sheets_notice"], "省略不代表不存在或已导入")
	require.Contains(t, model["literal_fields_query_hint"], "可直接用query_series查询")
	require.Contains(t, model["literal_fields_query_hint"], "口径仍不明确，先澄清")
	require.Len(t, model["literal_fields"], 4, "every duplicate field remains available")
	for _, column := range []string{"C", "F", "U", "X"} {
		require.Contains(t, compact.ToolCalls[0].Result.Output, "actual."+column)
	}
	rawRunes := utf8.RuneCountInString(step.ToolCalls[0].Result.Output)
	modelRunes := utf8.RuneCountInString(compact.ToolCalls[0].Result.Output)
	require.Less(t, modelRunes, rawRunes*3/4)
	t.Logf("catalogue chars %d -> %d (%.1f%% reduction), all 4 duplicate candidates/source context/coverage retained", rawRunes, modelRunes, 100*(1-float64(modelRunes)/float64(rawRunes)))
}

func TestWorkbenchDiscoveryNeverCompactsFailedRetrievalOrUnknownCoverageShape(t *testing.T) {
	failed := discoveryTestCall(t, "list_datasets", discoveryCompactionFixture(t))
	failed.Result.Success = false
	failed.Result.Error = "503 service unavailable"
	read := discoveryTestCall(t, "query_series", map[string]any{"pending_sheets": []any{"some sheet"}, "value": 1})
	knowledge := types.ToolCall{Name: agenttools.ToolSearchKnowledge, Result: &types.ToolResult{Success: true, Output: "actual page evidence", Data: map[string]any{"chunks": []any{"actual chart"}}}}
	step := types.AgentStep{ToolCalls: []types.ToolCall{failed, read, knowledge}}
	result := compactWorkbenchDiscovery(step, "问题", nil)
	for i := range step.ToolCalls {
		require.Same(t, step.ToolCalls[i].Result, result.ToolCalls[i].Result)
	}
	catalogue := map[string]any{"datasets": []any{map[string]any{"pending_sheets": []any{map[string]any{"name": "unknown semantic shape", "reason": "unsupported"}}}}}
	require.Nil(t, compactDiscoveryCatalogue(catalogue, "问题"), "unknown structured coverage stays intact")
}

func TestWorkbenchDiscoveryOmitsOnlySchemasActuallyRegistered(t *testing.T) {
	data := map[string]any{"tools": []any{
		map[string]any{"name": "query_series", "description": "already supplied with native tool", "inputSchema": map[string]any{"required": []any{"dataset_id"}}, "annotations": map[string]any{"readOnlyHint": true}},
		map[string]any{"name": "unregistered_operation", "description": "keep fallback contract", "inputSchema": map[string]any{"required": []any{"needed"}}},
	}}
	call := discoveryTestCall(t, "describe_tools", data)
	result := compactWorkbenchDiscovery(types.AgentStep{ToolCalls: []types.ToolCall{call}}, "问题", map[string]string{"query_series": "structured_query_series"})
	entries := result.ToolCalls[0].Result.Data["tools"].([]any)
	registered := entries[0].(map[string]any)
	require.NotContains(t, registered, "inputSchema")
	require.NotContains(t, registered, "description")
	require.Equal(t, "structured_query_series", registered["typed_tool"])
	require.Equal(t, true, registered["schema_in_function_definition"])
	require.Equal(t, data["tools"].([]any)[1], entries[1], "unregistered operations retain the complete contract")
	require.Contains(t, call.Result.Data["tools"].([]any)[0].(map[string]any), "inputSchema", "raw evidence unchanged")
	require.Nil(t, compactDiscoverySchemas(data, nil), "failed registration never claims schemas already exist")
}

func TestWorkbenchDiscoveryModelViewCompactsButFreshAuthorizedCallsAndRawStepsRemain(t *testing.T) {
	schema, err := os.ReadFile("tools/testdata/structured_read_operations.json")
	require.NoError(t, err)
	catalogue := discoveryCompactionFixture(t)
	counts := map[string]int{}
	var mu sync.Mutex
	registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Operation string `json:"operation"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.NotEmpty(t, r.Header.Get("X-Research-Identity"))
		require.NotEmpty(t, r.Header.Get("X-Research-Signature"))
		mu.Lock()
		counts[request.Operation]++
		mu.Unlock()
		if request.Operation == "describe_tools" {
			_, _ = w.Write(schema)
		} else {
			require.NoError(t, json.NewEncoder(w).Encode(catalogue))
		}
	})
	engine := newTestEngine(t, nil)
	engine.toolRegistry = registry
	engine.config.WorkbenchDefault = true
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "authorized-user", Role: types.TenantRoleViewer})
	state := &types.AgentState{}
	for i := 0; i < 2; i++ {
		messages := engine.discoverWorkbenchSources(ctx, state, nil, "20天原料", "session", fmt.Sprint(i))
		var catalogText, schemaText string
		for _, message := range messages {
			if message.Role != "tool" {
				continue
			}
			if strings.HasPrefix(message.ToolCallID, "source-catalog-") {
				catalogText = message.Content
			}
			if strings.HasPrefix(message.ToolCallID, "source-schemas-") {
				schemaText = message.Content
			}
		}
		require.NotContains(t, catalogText, "尚未核对的其他业务工作表_109")
		for _, column := range []string{"C", "F", "U", "X"} {
			require.Contains(t, catalogText, "actual."+column)
		}
		require.Contains(t, catalogText, "source_context")
		require.Contains(t, catalogText, "coverage")
		require.NotContains(t, schemaText, "inputSchema")
		require.Contains(t, schemaText, "schema_in_function_definition")
		for _, name := range []string{"list_series", "query_series", "analyze_table", "inspect_source", "get_result_evidence"} {
			_, err := registry.GetTool("structured_" + name)
			require.NoError(t, err)
		}
	}
	mu.Lock()
	require.Equal(t, 2, counts["list_datasets"], "each turn must repeat the signed permission-scoped discovery, never use cached directory")
	mu.Unlock()
	require.Len(t, state.RoundSteps, 2)
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			if call.Args["operation"] == "list_datasets" {
				require.Contains(t, call.Result.Output, "尚未核对的其他业务工作表_109")
			}
			if call.Args["operation"] == "describe_tools" {
				require.Contains(t, call.Result.Output, "inputSchema")
			}
		}
	}
}

func TestWorkbenchDiscoveryKeepsChangedSchemaWhenRegistrationRetainsOlderTool(t *testing.T) {
	fixture, err := os.ReadFile("tools/testdata/structured_read_operations.json")
	require.NoError(t, err)
	var previous, current map[string]any
	require.NoError(t, json.Unmarshal(fixture, &previous))
	require.NoError(t, json.Unmarshal(fixture, &current))
	for _, entry := range current["tools"].([]any) {
		tool := entry.(map[string]any)
		if tool["name"] == "query_series" {
			tool["inputSchema"].(map[string]any)["properties"].(map[string]any)["new_filter"] = map[string]any{"type": "string"}
		}
	}
	registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Operation string `json:"operation"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		if request.Operation == "describe_tools" {
			require.NoError(t, json.NewEncoder(w).Encode(current))
		} else {
			_, _ = w.Write([]byte(`{"datasets":[]}`))
		}
	})
	base, err := registry.GetTool(agenttools.ToolStructuredData)
	require.NoError(t, err)
	for _, tool := range agenttools.NewStructuredReadOperationTools(base.(*agenttools.StructuredDataTool), previous) {
		registry.RegisterTool(tool)
	}
	engine := newTestEngine(t, nil)
	engine.toolRegistry = registry
	engine.config.WorkbenchDefault = true
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "authorized-user", Role: types.TenantRoleViewer})
	messages := engine.discoverWorkbenchSources(ctx, &types.AgentState{}, nil, "问题", "session", "schema-change")
	found := false
	for _, message := range messages {
		if message.Role != "tool" || !strings.HasPrefix(message.ToolCallID, "source-schemas-") {
			continue
		}
		var model map[string]any
		require.NoError(t, json.Unmarshal([]byte(message.Content), &model))
		found = true
		for _, entry := range model["tools"].([]any) {
			tool := entry.(map[string]any)
			if tool["name"] == "query_series" {
				require.Contains(t, tool, "inputSchema", "a new schema not actually registered must remain visible")
				require.Contains(t, tool["inputSchema"].(map[string]any)["properties"].(map[string]any), "new_filter")
				require.NotContains(t, tool, "schema_in_function_definition")
			} else {
				require.NotContains(t, tool, "inputSchema")
			}
		}
	}
	require.True(t, found, "the first model context includes the described service directory")
	retained, err := registry.GetTool("structured_query_series")
	require.NoError(t, err)
	require.NotContains(t, string(retained.Parameters()), "new_filter", "the first-wins retained contract differs from the latest response")
}

// Optional local audit replays a saved actual acceptance trace without any
// service access. It reports character reduction using this production helper.
func TestWorkbenchDiscoveryCompactionActualAudit(t *testing.T) {
	path := os.Getenv("WORKBENCH_DISCOVERY_AUDIT_FILE")
	if path == "" {
		t.Skip("optional recorded acceptance audit")
	}
	encoded, err := os.ReadFile(path)
	require.NoError(t, err)
	var trace struct {
		Message struct {
			AgentSteps []types.AgentStep `json:"agent_steps"`
		} `json:"message"`
	}
	require.NoError(t, json.Unmarshal(encoded, &trace))
	registered := map[string]string{}
	for _, name := range []string{"list_series", "query_series", "analyze_table", "inspect_source", "get_result_evidence"} {
		registered[name] = "structured_" + name
	}
	for _, step := range trace.Message.AgentSteps {
		if step.Iteration != -1 {
			continue
		}
		query := ""
		for _, call := range step.ToolCalls {
			if args, ok := call.Args["arguments"].(map[string]any); ok {
				if q, ok := args["question"].(string); ok {
					query = q
				}
			}
		}
		view := compactWorkbenchDiscovery(step, query, registered)
		rawTotal, modelTotal := 0, 0
		for i, call := range step.ToolCalls {
			if call.Name != agenttools.ToolStructuredData || call.Result == nil {
				continue
			}
			rawChars := utf8.RuneCountInString(call.Result.Output)
			modelChars := utf8.RuneCountInString(view.ToolCalls[i].Result.Output)
			rawTotal += rawChars
			modelTotal += modelChars
			t.Logf("actual %v chars %d -> %d (%.1f%% reduction)", call.Args["operation"], rawChars, modelChars, 100*(1-float64(modelChars)/float64(rawChars)))
		}
		t.Logf("actual total discovery structured chars %d -> %d (%.1f%% reduction)", rawTotal, modelTotal, 100*(1-float64(modelTotal)/float64(rawTotal)))
		require.Less(t, modelTotal, rawTotal)
	}
}
