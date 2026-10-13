package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

const cacheDatasetA = "aaaaaaaaaaaaaaaaaaaaaaaa"
const cacheDatasetB = "bbbbbbbbbbbbbbbbbbbbbbbb"

func structuredCacheTestContext(callID string) context.Context {
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "user-a", Role: types.TenantRoleViewer})
	return WithToolExecContext(ctx, &ToolExecContext{SessionID: "session-a", AssistantMessageID: "turn-a", RequestID: "request-a", ToolCallID: callID})
}

func structuredCacheQuery(dataset string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"operation":"query_series","arguments":{"dataset_id":%q,"series_id":"C","operation":"sum"}}`, dataset))
}

func TestStructuredReadCacheCanonicalTypedQueryKeepsSourceAndCloneIsolation(t *testing.T) {
	var calls atomic.Int64
	body := `{"status":"ok","aggregate":777.008062964705883,"rows":[{"value":777.008062964705883,"source_cell":"C74","date_cell":"A74"}],"source":{"file":"original.xlsx","sha256":"original-sha"}}`
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var input map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
		require.Equal(t, "真实人类问题", input["arguments"].(map[string]any)["source_question"])
		_, _ = io.WriteString(w, body)
	})
	base.BindSourceQuestion("真实人类问题")
	typed := operationToolByName(t, NewStructuredReadOperationTools(base, describedStructuredReadTools(t)), "structured_query_series")
	first, err := typed.Execute(structuredCacheTestContext("original-call"), json.RawMessage(
		`{"dataset_id":"`+cacheDatasetA+`","series_id":"C","operation":"sum"}`))
	require.NoError(t, err)
	first.Data["source"].(map[string]any)["sha256"] = "polluted"
	first.Data["rows"].([]any)[0].(map[string]any)["value"] = float64(-1)
	hit, err := base.Execute(structuredCacheTestContext("repeat-call"), json.RawMessage(
		`{ "arguments": { "source_question":"模型改写", "operation":"sum", "series_id":"C", "dataset_id":"`+cacheDatasetA+`" }, "operation":"query_series" }`))
	require.NoError(t, err)
	require.Equal(t, int64(1), calls.Load())
	require.Equal(t, body, hit.Output, "original numeric/source bytes survive reuse")
	require.Equal(t, "original-sha", hit.Data["source"].(map[string]any)["sha256"])
	require.Equal(t, float64(777.008062964705883), hit.Data["rows"].([]any)[0].(map[string]any)["value"])
	trace := hit.Data["_execution_cache"].(map[string]any)
	require.Equal(t, true, trace["cache_hit"])
	require.Equal(t, "original-call", trace["origin_tool_call_id"])
	require.NotEmpty(t, trace["original_executed_at"])
	require.GreaterOrEqual(t, trace["original_execution_ms"].(int64), int64(0))
	hit.Data["rows"].([]any)[0].(map[string]any)["source_cell"] = "X1"
	next, err := base.Execute(structuredCacheTestContext("third-call"), structuredCacheQuery(cacheDatasetA))
	require.NoError(t, err)
	require.Equal(t, "C74", next.Data["rows"].([]any)[0].(map[string]any)["source_cell"])
	require.Equal(t, int64(1), calls.Load())
	rowsArgs := json.RawMessage(`{"dataset_id":"` + cacheDatasetA + `","series_id":"C","operation":"rows"}`)
	_, err = typed.Execute(structuredCacheTestContext("typed-rows"), rowsArgs)
	require.NoError(t, err)
	rowsHit, err := base.Execute(structuredCacheTestContext("base-rows"), json.RawMessage(
		`{"operation":"query_series","arguments":{"operation":"rows","series_id":"C","dataset_id":"`+cacheDatasetA+`"}}`))
	require.NoError(t, err)
	require.Equal(t, int64(2), calls.Load(), "typed/base rows share one actual execution, distinct from sum")
	require.Equal(t, "typed-rows", rowsHit.Data["_execution_cache"].(map[string]any)["origin_tool_call_id"])
}

func TestStructuredReadCacheCallerTurnAndInstancePartition(t *testing.T) {
	var calls atomic.Int64
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	ctx := structuredCacheTestContext("first")
	_, err := base.Execute(ctx, structuredCacheQuery(cacheDatasetA))
	require.NoError(t, err)
	_, err = base.Execute(types.WithExecutionTenant(ctx, 99), structuredCacheQuery(cacheDatasetA))
	require.NoError(t, err)
	require.Equal(t, int64(1), calls.Load(), "execution tenant cannot replace caller identity")
	for _, caller := range []types.Caller{
		{TenantID: 8, UserID: "user-a", Role: types.TenantRoleViewer},
		{TenantID: 7, UserID: "user-b", Role: types.TenantRoleViewer},
		{TenantID: 7, UserID: "user-a", Role: types.TenantRoleAdmin},
	} {
		result, err := base.Execute(types.WithCaller(ctx, caller), structuredCacheQuery(cacheDatasetA))
		require.NoError(t, err)
		require.Nil(t, result.Data["_execution_cache"])
	}
	_, err = base.Execute(context.Background(), structuredCacheQuery(cacheDatasetA))
	require.ErrorContains(t, err, "已核验")
	_, err = base.Execute(ctx, json.RawMessage(`{"operation":"query_series","arguments":{"dataset_id":"`+cacheDatasetA+`","series_id":"C","tenant_id":8}}`))
	require.ErrorContains(t, err, "身份")
	require.Equal(t, int64(4), calls.Load())
	for _, meta := range []*ToolExecContext{
		{SessionID: "other", AssistantMessageID: "turn-a", RequestID: "request-a"},
		{SessionID: "session-a", AssistantMessageID: "turn-b", RequestID: "request-a"},
		{SessionID: "session-a", AssistantMessageID: "turn-a", RequestID: "request-b"},
	} {
		_, err = base.Execute(WithToolExecContext(ctx, meta), structuredCacheQuery(cacheDatasetA))
		require.NoError(t, err)
	}
	_, err = NewStructuredDataTool().Execute(ctx, structuredCacheQuery(cacheDatasetA))
	require.NoError(t, err)
	require.Equal(t, int64(8), calls.Load())
}

func TestStructuredReadCachePinnedOperationsParametersAndRebind(t *testing.T) {
	var calls atomic.Int64
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		_, _ = w.Write(body)
	})
	ctx := structuredCacheTestContext("call")
	for _, op := range []string{"query_series", "list_series", "analyze_table", "inspect_source", "list_charts", "compare_series", "convert_price"} {
		args := json.RawMessage(`{"operation":"` + op + `","arguments":{"dataset_id":"` + cacheDatasetA + `"}}`)
		for i := 0; i < 2; i++ {
			_, err := base.Execute(ctx, args)
			require.NoError(t, err)
		}
	}
	require.Equal(t, int64(7), calls.Load())
	for _, args := range []string{
		`{"operation":"query_series","arguments":{"series_id":"C"}}`,
		`{"operation":"query_series","arguments":{"dataset_id":null}}`,
		`{"operation":"query_series","arguments":{"dataset_id":"latest"}}`,
		`{"operation":"list_datasets","arguments":{}}`,
		`{"operation":"resolve_fields","arguments":{"dataset_id":"` + cacheDatasetA + `"}}`,
		`{"operation":"confirm_fields","arguments":{"dataset_id":"` + cacheDatasetA + `"}}`,
		`{"operation":"execute_field_plan","arguments":{"dataset_id":"` + cacheDatasetA + `"}}`,
		`{"operation":"get_result_evidence","arguments":{"dataset_id":"` + cacheDatasetA + `"}}`,
	} {
		before := calls.Load()
		for i := 0; i < 2; i++ {
			_, err := base.Execute(ctx, json.RawMessage(args))
			require.NoError(t, err)
		}
		require.Equal(t, before+2, calls.Load())
	}
	base.BindSourceQuestion("原始口径")
	for _, args := range []string{
		string(structuredCacheQuery(cacheDatasetA)), string(structuredCacheQuery(cacheDatasetB)),
		`{"operation":"query_series","arguments":{"dataset_id":"` + cacheDatasetA + `","series_id":"C","operation":"sum","start_date":"2024-01-01"}}`,
		`{"operation":"query_series","arguments":{"dataset_id":"` + cacheDatasetA + `","series_id":"X","operation":"sum"}}`,
		`{"operation":"query_series","arguments":{"dataset_id":"` + cacheDatasetA + `","series_id":"C","operation":"mean"}}`,
	} {
		_, err := base.Execute(ctx, json.RawMessage(args))
		require.NoError(t, err)
	}
	before := calls.Load()
	base.BindSourceQuestion("原始口径") // Same text still resets a new turn.
	_, err := base.Execute(ctx, structuredCacheQuery(cacheDatasetA))
	require.NoError(t, err)
	require.Equal(t, before+1, calls.Load())
	base.BindSourceQuestion("明确新口径")
	result, err := base.Execute(ctx, structuredCacheQuery(cacheDatasetA))
	require.NoError(t, err)
	require.Contains(t, result.Output, "明确新口径")
	require.Equal(t, before+2, calls.Load())
}

func TestStructuredReadCacheErrorsClarificationsAndBackendMetadataAreNeverReused(t *testing.T) {
	for _, test := range []struct {
		name string
		code int
		body string
	}{
		{"validation", 400, `{"detail":"invalid"}`},
		{"permission", 403, `{"detail":"denied"}`},
		{"service", 503, `{"detail":"unavailable"}`},
		{"query error", 200, `{"status":"error","error":"query failed"}`},
		{"clarification", 200, `{"status":"needs_clarification","candidates":[]}`},
		{"backend metadata", 200, `{"status":"ok","_execution_cache":{"server_field":"preserve"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(test.code)
				_, _ = io.WriteString(w, test.body)
			})
			for i := 0; i < 2; i++ {
				result, _ := base.Execute(structuredCacheTestContext("call"), structuredCacheQuery(cacheDatasetA))
				if test.name == "backend metadata" {
					require.Equal(t, "preserve", result.Data["_execution_cache"].(map[string]any)["server_field"])
				}
			}
			require.Equal(t, int64(2), calls.Load())
		})
	}
}

func TestStructuredReadCacheConcurrentReadsAndCancelledWaiter(t *testing.T) {
	var calls atomic.Int64
	entered, release := make(chan struct{}), make(chan struct{})
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		<-release
		_, _ = io.WriteString(w, `{"status":"ok","rows":[{"source_cell":"C74","value":12}]}`)
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
	ctx, cancel := context.WithCancel(structuredCacheTestContext("cancelled"))
	cancel()
	_, err := base.Execute(ctx, structuredCacheQuery(cacheDatasetA))
	require.ErrorIs(t, err, context.Canceled)
	close(release)
	wg.Wait()
	for i := range results {
		require.NoError(t, errs[i])
		require.Equal(t, float64(12), results[i].Data["rows"].([]any)[0].(map[string]any)["value"])
	}
	require.Equal(t, int64(1), calls.Load())
}

func TestStructuredReadCacheOldInflightCannotPublishAfterRebind(t *testing.T) {
	var calls atomic.Int64
	entered, release := make(chan struct{}), make(chan struct{})
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			close(entered)
			<-release
		}
		_, _ = fmt.Fprintf(w, `{"status":"ok","request_count":%d}`, n)
	})
	base.BindSourceQuestion("first")
	done := make(chan error, 1)
	go func() {
		_, err := base.Execute(structuredCacheTestContext("old"), structuredCacheQuery(cacheDatasetA))
		done <- err
	}()
	<-entered
	base.BindSourceQuestion("second")
	second, err := base.Execute(structuredCacheTestContext("new"), structuredCacheQuery(cacheDatasetA))
	require.NoError(t, err)
	require.Equal(t, float64(2), second.Data["request_count"])
	close(release)
	require.NoError(t, <-done)
	hit, err := base.Execute(structuredCacheTestContext("new-repeat"), structuredCacheQuery(cacheDatasetA))
	require.NoError(t, err)
	require.Equal(t, float64(2), hit.Data["request_count"])
	require.Equal(t, int64(2), calls.Load())
}

func TestStructuredReadCacheConcurrentTransientFailureIsRetried(t *testing.T) {
	var calls atomic.Int64
	entered, release := make(chan struct{}), make(chan struct{})
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"status":"ok","value":12}`)
	})
	firstDone := make(chan error, 1)
	go func() {
		_, err := base.Execute(structuredCacheTestContext("first-fails"), structuredCacheQuery(cacheDatasetA))
		firstDone <- err
	}()
	<-entered
	secondDone := make(chan error, 1)
	go func() {
		result, err := base.Execute(structuredCacheTestContext("second-retries"), structuredCacheQuery(cacheDatasetA))
		if err == nil && (result == nil || !result.Success) {
			err = fmt.Errorf("second caller did not perform a successful retry")
		}
		secondDone <- err
	}()
	close(release)
	require.ErrorContains(t, <-firstDone, "HTTP 503")
	require.NoError(t, <-secondDone)
	require.Equal(t, int64(2), calls.Load())
}

func TestStructuredReadCacheBoundedFIFOEviction(t *testing.T) {
	for _, large := range []bool{false, true} {
		t.Run(fmt.Sprint(large), func(t *testing.T) {
			var calls atomic.Int64
			payload := "small"
			count := 40
			if large {
				payload = strings.Repeat("x", 250*1024)
				count = 8
			}
			base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "payload": payload})
			})
			for i := 0; i < count; i++ {
				args := json.RawMessage(fmt.Sprintf(`{"operation":"list_series","arguments":{"dataset_id":%q,"offset":%d}}`, cacheDatasetA, i))
				_, err := base.Execute(structuredCacheTestContext("call"), args)
				require.NoError(t, err)
				require.LessOrEqual(t, len(base.readCache.entries), structuredReadCacheEntries)
				require.LessOrEqual(t, base.readCache.bytes, structuredReadCacheBytes)
			}
			before := calls.Load()
			_, err := base.Execute(structuredCacheTestContext("repeat"), json.RawMessage(`{"operation":"list_series","arguments":{"dataset_id":"`+cacheDatasetA+`","offset":0}}`))
			require.NoError(t, err)
			require.Equal(t, before+1, calls.Load())
			base.BindSourceQuestion("next")
			require.Empty(t, base.readCache.entries)
			require.Zero(t, base.readCache.bytes)
		})
	}
}
