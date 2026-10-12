package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestWorkbenchPeriodLabelsUseOnlyExplicitQuestionLabels(t *testing.T) {
	require.Equal(t, []string{"2022/23", "22/23"}, workbenchPeriodLabels("2022/23年度中国棉花合计？"))
	require.Equal(t, []string{"2022/2023", "22/23"}, workbenchPeriodLabels("2022 / 2023年度？"))
	for _, question := range []string{"2022年棉花产量", "棉花年度供需合计", "22/23年度", "2022-09至2023-08产量合计", "2022/09/12产量", "2022 / 09 / 12产量"} {
		require.Empty(t, workbenchPeriodLabels(question), "do not infer a business year from a calendar date or domain term")
	}
}

func TestWorkbenchPeriodInspectionRunsScopedOnceAndIsSeenByNextModelRound(t *testing.T) {
	fixture, err := os.ReadFile("tools/testdata/structured_read_operations.json")
	require.NoError(t, err)
	var mu sync.Mutex
	var inspections []map[string]any
	registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Operation string         `json:"operation"`
			Arguments map[string]any `json:"arguments"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
		identityJSON, err := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Research-Identity"))
		require.NoError(t, err)
		var identity map[string]any
		require.NoError(t, json.Unmarshal(identityJSON, &identity))
		require.Equal(t, float64(7), identity["tenant_id"], "foreign execution tenant never changes source authorization")
		require.Equal(t, "verified", identity["user_id"])
		switch input.Operation {
		case "list_datasets":
			_, _ = io.WriteString(w, `{"datasets":[{"dataset_id":"actual-version"}]}`)
		case "describe_tools":
			_, _ = w.Write(fixture)
		case "list_series":
			_, _ = io.WriteString(w, `{"dataset_id":"actual-version","series":[{"series_id":"production","sheet":"中国棉花平衡表"},{"series_id":"imports","sheet":"中国棉花平衡表"}]}`)
		case "inspect_source":
			mu.Lock()
			inspections = append(inspections, input.Arguments)
			mu.Unlock()
			_, _ = io.WriteString(w, `{"kind":"structured_source_inspection","status":"ok","dataset_id":"actual-version","source_file":"棉花数据库(2).xlsx","sha256":"verified-original","matches":[{"sheet":"中国棉花平衡表","cell":"A139","value":"22/23年度第一口径"},{"sheet":"中国棉花平衡表","cell":"A179","value":"22/23年度第二口径"}],"truncated":false}`)
		default:
			t.Errorf("supplement must not calculate, resolve fields or write: %s", input.Operation)
		}
	})
	directory := func(id string) mockResponse {
		return mockResponse{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Done: true, FinishReason: "tool_calls", ToolCalls: []types.LLMToolCall{{ID: id, Function: types.FunctionCall{Name: "structured_list_series", Arguments: `{"dataset_id":"model-requested","search":"棉花"}`}}}}}}
	}
	answer := mockResponse{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: "原表有两个口径，请确认使用哪一个。", Done: true, FinishReason: "stop"}}}
	model := &mockChat{responses: []mockResponse{directory("directory-first"), directory("directory-repeated"), answer, answer, answer}}
	engine := newTestEngine(t, model)
	engine.toolRegistry = registry
	engine.config.WorkbenchDefault = true
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
	ctx = types.WithExecutionTenant(ctx, 99)
	state, err := engine.Execute(ctx, "session", "message", "2022/23年度中国棉花产量加进口，合计是多少？", nil)
	require.NoError(t, err)
	require.Len(t, inspections, 1, "duplicate fields and repeated directory reads inspect this sheet only once")
	args := inspections[0]
	require.Equal(t, "actual-version", args["dataset_id"], "use the authenticated directory result, not a model-supplied ID")
	require.Equal(t, "中国棉花平衡表", args["sheet"], "never perform an automatic whole-workbook scan")
	require.Equal(t, []any{"2022/23", "22/23"}, args["searches"])
	for key, value := range map[string]float64{"max_cells": 400, "max_matches": 10, "padding_rows": 3, "padding_cols": 3} {
		require.Equal(t, value, args[key])
	}
	for _, key := range []string{"start_date", "end_date", "sums", "ranges", "unit", "tenant_id"} {
		require.NotContains(t, args, key)
	}
	persisted := 0
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			if strings.HasPrefix(call.ID, workbenchPeriodCallPrefix) {
				persisted++
				require.True(t, call.Result.Success)
				require.Contains(t, call.Result.Output, "第二口径")
			}
		}
	}
	require.Equal(t, 1, persisted)
	require.GreaterOrEqual(t, len(model.calls), 2)
	found := false
	for _, message := range model.calls[1] {
		if message.Role == "tool" && message.Name == "structured_inspect_source" {
			found = true
			require.Contains(t, message.Content, "第一口径")
			require.Contains(t, message.Content, "第二口径")
		}
	}
	require.True(t, found, "the next reasoning round must see both original blocks")
}

func TestWorkbenchPeriodInspectionRejectsUnverifiedOrUnprovenTargetsAndIsBounded(t *testing.T) {
	calls := 0
	registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{"status":"error","error":"original missing"}`)
	})
	engine := newTestEngine(t, nil)
	engine.toolRegistry = registry
	engine.config.WorkbenchDefault = true
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
	listed := func(success bool, dataset string, sheets ...string) types.AgentStep {
		series := []any{}
		for _, sheet := range sheets {
			series = append(series, map[string]any{"sheet": sheet})
		}
		return types.AgentStep{ToolCalls: []types.ToolCall{{Name: "structured_data", Args: map[string]any{"operation": "list_series"}, Result: &types.ToolResult{Success: success, Data: map[string]any{"dataset_id": dataset, "series": series}}}}}
	}
	for _, test := range []struct {
		ctx      context.Context
		question string
		step     types.AgentStep
	}{
		{context.Background(), "2022/23", listed(true, "real", "sheet")}, {ctx, "2022年", listed(true, "real", "sheet")},
		{ctx, "2022/23", listed(false, "real", "sheet")}, {ctx, "2022/23", listed(true, "", "sheet")}, {ctx, "2022/23", listed(true, "real", "")},
	} {
		engine.inspectWorkbenchPeriodSources(test.ctx, &types.AgentState{}, &test.step, test.question, "session", "message")
	}
	require.Zero(t, calls)
	step := listed(true, "real", "sheet")
	engine.config.WorkbenchDefault = false
	engine.inspectWorkbenchPeriodSources(ctx, &types.AgentState{}, &step, "2022/23", "session", "message")
	require.Zero(t, calls)
	engine.config.WorkbenchDefault = true
	sheets := []string{}
	for i := 0; i < 20; i++ {
		sheets = append(sheets, fmt.Sprintf("sheet-%d", i))
	}
	step = listed(true, "real", sheets...)
	state := &types.AgentState{}
	engine.inspectWorkbenchPeriodSources(ctx, state, &step, "2022/23", "session", "message")
	require.Equal(t, maxWorkbenchPeriodSources, calls)
	require.Len(t, step.ToolCalls, 1+maxWorkbenchPeriodSources)
	state.RoundSteps = append(state.RoundSteps, step)
	step = listed(true, "real", "sheet-0", "sheet-99")
	engine.inspectWorkbenchPeriodSources(ctx, state, &step, "2022/23", "session", "message")
	require.Equal(t, maxWorkbenchPeriodSources, calls, "failed reads and later sheets cannot restart unlimited supplemental calls")
}
