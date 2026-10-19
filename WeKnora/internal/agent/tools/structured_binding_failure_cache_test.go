package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

const bindingUnavailableFixture = `{"kind":"structured_source_binding","status":"error","code":"source_binding_unavailable","error":"source binding unavailable"}`

func TestStructuredBindingFailureTypedAndBaseShareAttempt(t *testing.T) {
	var calls atomic.Int64
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, bindingUnavailableFixture)
	})
	base.BindSourceQuestion("the full user request")
	typed := operationToolByName(t, NewStructuredReadOperationTools(base, describedStructuredReadTools(t)), "structured_query_series")
	registry := NewToolRegistry()
	registry.RegisterTool(base)
	registry.RegisterTool(typed)
	first, err := registry.ExecuteTool(structuredCacheTestContext("original"), typed.Name(), json.RawMessage(`{"dataset_id":"`+cacheDatasetA+`","series_id":"C","operation":"sum"}`))
	require.NoError(t, err)
	require.False(t, first.Success)
	require.Nil(t, first.Data["_execution_cache"])
	first.Data["error"] = "changed after return"
	hit, err := registry.ExecuteTool(structuredCacheTestContext("repeat"), base.Name(), structuredCacheQuery(cacheDatasetA))
	require.NoError(t, err)
	require.False(t, hit.Success)
	require.Equal(t, "source binding unavailable", hit.Data["error"])
	require.Equal(t, int64(1), calls.Load())
	metadata := hit.Data["_execution_cache"].(map[string]any)
	require.Equal(t, "original", metadata["origin_tool_call_id"])
	require.Equal(t, true, metadata["failure_reused"])
	require.Equal(t, false, metadata["reexecuted"])
	require.Contains(t, hit.Output, "未重新执行")
	require.Contains(t, hit.Output, "original")
	// Registered schemas are still checked on a repeated call.
	invalid, err := registry.ExecuteTool(structuredCacheTestContext("invalid"), typed.Name(), json.RawMessage(`{"dataset_id":"`+cacheDatasetA+`"}`))
	require.NoError(t, err)
	require.False(t, invalid.Success)
	require.NotContains(t, invalid.Output, "failure_reused")
	require.Equal(t, int64(1), calls.Load())
}

func TestStructuredBindingFailureIdentityParametersAndNewTurn(t *testing.T) {
	var calls atomic.Int64
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, bindingUnavailableFixture)
	})
	base.BindSourceQuestion("same question")
	ctx := structuredCacheTestContext("original")
	query := structuredCacheQuery(cacheDatasetA)
	for i := 0; i < 2; i++ {
		_, err := base.Execute(ctx, query)
		require.NoError(t, err)
	}
	require.Equal(t, int64(1), calls.Load())
	base.BindSourceQuestion("same question")
	_, err := base.Execute(ctx, query)
	require.NoError(t, err)
	require.Equal(t, int64(2), calls.Load(), "same text in a new bind must allow a new attempt")
	contexts := []context.Context{
		WithToolExecContext(ctx, &ToolExecContext{SessionID: "session-a", AssistantMessageID: "turn-b", ToolCallID: "new-turn"}),
		types.WithCaller(ctx, types.Caller{TenantID: 8, UserID: "user-a", Role: types.TenantRoleViewer}),
		types.WithCaller(ctx, types.Caller{TenantID: 7, UserID: "user-b", Role: types.TenantRoleViewer}),
		types.WithCaller(ctx, types.Caller{TenantID: 7, UserID: "user-a", Role: types.TenantRoleAdmin}),
	}
	for _, c := range contexts {
		result, err := base.Execute(c, query)
		require.NoError(t, err)
		require.Nil(t, result.Data["_execution_cache"])
	}
	for _, args := range []json.RawMessage{
		structuredCacheQuery(cacheDatasetB),
		json.RawMessage(`{"operation":"query_series","arguments":{"dataset_id":"` + cacheDatasetA + `","series_id":"D","operation":"sum"}}`),
		json.RawMessage(`{"operation":"query_series","arguments":{"dataset_id":"` + cacheDatasetA + `","series_id":"C","operation":"rows"}}`),
	} {
		result, err := base.Execute(ctx, args)
		require.NoError(t, err)
		require.Nil(t, result.Data["_execution_cache"])
	}
	require.Equal(t, int64(9), calls.Load())
}

func TestStructuredBindingFailureScopeAndAPIKeyRechecked(t *testing.T) {
	var calls atomic.Int64
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, bindingUnavailableFixture)
	})
	base.WithSearchTargets(types.SearchTargets{
		{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-first", TenantID: 7},
		{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-second", TenantID: 29},
	})
	request := func(kb string) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"knowledge_base_id":%q,"operation":"query_series","arguments":{"dataset_id":%q,"series_id":"C","operation":"sum"}}`, kb, cacheDatasetA))
	}
	ctx := structuredCacheTestContext("call")
	for _, kb := range []string{"kb-first", "kb-second", "kb-first"} {
		_, err := base.Execute(ctx, request(kb))
		require.NoError(t, err)
	}
	require.Equal(t, int64(2), calls.Load())
	denied := types.WithTenantAPIKeyScope(ctx, types.TenantAPIKeyScope{KnowledgeBaseIDs: types.StringArray{"kb-second"}})
	_, err := base.Execute(denied, request("kb-first"))
	require.Error(t, err)
	require.Equal(t, int64(2), calls.Load())
}

func TestStructuredBindingFailureRequiresExplicitTurnCodeAndPinnedDataset(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		ctx        context.Context
		args       json.RawMessage
	}{
		{"no turn", bindingUnavailableFixture, types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "user-a", Role: types.TenantRoleViewer}), structuredCacheQuery(cacheDatasetA)},
		{"legacy error without code", `{"kind":"structured_source_binding","status":"error","error":"unavailable"}`, structuredCacheTestContext("call"), structuredCacheQuery(cacheDatasetA)},
		{"other error code", `{"kind":"structured_source_binding","status":"error","code":"different_error","error":"unavailable"}`, structuredCacheTestContext("call"), structuredCacheQuery(cacheDatasetA)},
		{"other error kind", `{"kind":"other_kind","status":"error","code":"source_binding_unavailable","error":"unavailable"}`, structuredCacheTestContext("call"), structuredCacheQuery(cacheDatasetA)},
		{"unbound dataset", bindingUnavailableFixture, structuredCacheTestContext("call"), json.RawMessage(`{"operation":"query_series","arguments":{"series_id":"C"}}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = io.WriteString(w, tc.body) })
			for i := 0; i < 2; i++ {
				_, err := base.Execute(tc.ctx, tc.args)
				require.NoError(t, err)
			}
			require.Equal(t, int64(2), calls.Load())
		})
	}
}

func TestStructuredBindingFailureConcurrentWaitersAndCancellation(t *testing.T) {
	var calls atomic.Int64
	entered, release := make(chan struct{}), make(chan struct{})
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		_, _ = io.WriteString(w, bindingUnavailableFixture)
	})
	var wg sync.WaitGroup
	results, errs := make([]*types.ToolResult, 8), make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = base.Execute(structuredCacheTestContext(fmt.Sprint(i)), structuredCacheQuery(cacheDatasetA))
		}(i)
	}
	<-entered
	ctx, cancel := context.WithTimeout(structuredCacheTestContext("cancelled"), 20*time.Millisecond)
	defer cancel()
	_, err := base.Execute(ctx, structuredCacheQuery(cacheDatasetA))
	require.ErrorIs(t, err, context.DeadlineExceeded, "a waiter must leave without waiting for the original request")
	close(release)
	wg.Wait()
	for i := range results {
		require.NoError(t, errs[i])
		require.False(t, results[i].Success)
	}
	require.Equal(t, int64(1), calls.Load())
}
