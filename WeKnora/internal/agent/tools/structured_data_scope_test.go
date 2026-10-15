package tools

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func structuredScopeContext() context.Context {
	return types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "scope-user", Role: types.TenantRoleViewer})
}

func decodeStructuredScopeIdentity(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	encoded := r.Header.Get("X-Research-Identity")
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	require.NoError(t, err)
	var identity map[string]any
	require.NoError(t, json.Unmarshal(raw, &identity))
	return identity
}

func TestStructuredKnowledgeScopeSignsSharedOwnerAndStripsSelector(t *testing.T) {
	var calls atomic.Int64
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		identity := decodeStructuredScopeIdentity(t, r)
		require.Len(t, identity, 7)
		require.Equal(t, float64(7), identity["tenant_id"])
		require.Equal(t, float64(29), identity["knowledge_base_tenant_id"])
		require.Equal(t, "kb-shared", identity["knowledge_base_id"])
		require.Equal(t, "viewer", identity["role"])
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(body, &payload))
		require.NotContains(t, payload, "knowledge_base_id")
		require.NotContains(t, payload["arguments"], "knowledge_base_id")
		hash := sha256.Sum256(body)
		mac := hmac.New(sha256.New, []byte(strings.Repeat("a", 48)))
		_, _ = mac.Write([]byte(strings.Join([]string{r.Header.Get("X-Research-Timestamp"), "POST", "/api/tools/read", r.Header.Get("X-Research-Identity"), hex.EncodeToString(hash[:])}, "\n")))
		require.Equal(t, hex.EncodeToString(mac.Sum(nil)), r.Header.Get("X-Research-Signature"))
		_, _ = io.WriteString(w, `{"status":"ok","datasets":[]}`)
	}).WithSearchTargets(types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-shared", TenantID: 29}})
	ctx := types.WithExecutionTenant(structuredScopeContext(), 99)
	for _, input := range []string{
		`{"operation":"list_datasets","arguments":{}}`,
		`{"operation":"list_datasets","knowledge_base_id":"kb-shared","arguments":{}}`,
	} {
		result, err := base.Execute(ctx, json.RawMessage(input))
		require.NoError(t, err)
		require.True(t, result.Success)
	}
	require.Equal(t, int64(2), calls.Load())
}

func TestStructuredKnowledgeScopeMultiRequiresRealModelSelection(t *testing.T) {
	var calls atomic.Int64
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "kb-second", decodeStructuredScopeIdentity(t, r)["knowledge_base_id"])
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	base.WithSearchTargets(types.SearchTargets{
		{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-first", TenantID: 7},
		{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-second", TenantID: 29},
		{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-first", TenantID: 7},
	})
	for _, input := range []string{
		`{"operation":"list_datasets","arguments":{}}`,
		`{"operation":"query_series","arguments":{"series_id":"arbitrary-real-id"}}`,
	} {
		result, err := base.Execute(structuredScopeContext(), json.RawMessage(input))
		require.NoError(t, err)
		require.True(t, result.Success)
		var directory struct {
			Status string                     `json:"status"`
			Scopes []structuredKnowledgeScope `json:"knowledge_bases"`
		}
		require.NoError(t, json.Unmarshal([]byte(result.Output), &directory))
		require.Equal(t, "scope_selection_required", directory.Status)
		require.Equal(t, []structuredKnowledgeScope{{"kb-first", 7}, {"kb-second", 29}}, directory.Scopes)
	}
	require.Zero(t, calls.Load(), "no random library or unscoped fallback may execute")
	result, err := base.Execute(structuredScopeContext(), json.RawMessage(`{"operation":"list_datasets","knowledge_base_id":"kb-second","arguments":{}}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, int64(1), calls.Load())
}

func TestStructuredKnowledgeScopeRejectsSpoofingRestrictedTargetsAndMutableInput(t *testing.T) {
	var calls atomic.Int64
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	allowed := &types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-first", TenantID: 7}
	base.WithSearchTargets(types.SearchTargets{allowed})
	allowed.KnowledgeBaseID, allowed.TenantID = "kb-outside", 29
	for _, input := range []string{
		`{"operation":"list_datasets","knowledge_base_id":"kb-outside","arguments":{}}`,
		`{"operation":"list_datasets","knowledge_base_id":null,"arguments":{}}`,
		`{"operation":"list_datasets","knowledge_base_id":"","arguments":{}}`,
		`{"operation":"list_datasets","knowledge_base_id":"../private","arguments":{}}`,
		`{"operation":"list_datasets","knowledge_base_id":"kb-first","knowledge_base_tenant_id":29,"arguments":{}}`,
		`{"operation":"list_datasets","arguments":{"knowledge_base_id":"kb-outside"}}`,
		`{"operation":"list_datasets","arguments":{"knowledge_base_tenant_id":29}}`,
	} {
		_, err := base.Execute(structuredScopeContext(), json.RawMessage(input))
		require.Error(t, err)
	}
	for _, targets := range []types.SearchTargets{
		{{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: "kb-first", TenantID: 7, KnowledgeIDs: []string{"doc-selected"}}},
		{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-first", TenantID: 7, KnowledgeIDs: []string{"doc-selected"}}},
		{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-first", TenantID: 7, TagIDs: []string{"tag-selected"}}},
		{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-first", TenantID: 7, ScopeTagIDs: []string{"logical-tag-selected"}}},
		{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-first", TenantID: 0}},
		{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-first", TenantID: 7}, {Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-first", TenantID: 29}},
		{nil},
	} {
		base.WithSearchTargets(targets)
		_, err := base.Execute(structuredScopeContext(), json.RawMessage(`{"operation":"list_datasets","arguments":{}}`))
		require.Error(t, err)
	}
	base.WithSearchTargets(nil)
	_, err := base.Execute(structuredScopeContext(), json.RawMessage(`{"operation":"list_datasets","knowledge_base_id":"kb-first","arguments":{}}`))
	require.ErrorContains(t, err, "授权范围")
	require.Zero(t, calls.Load())
}

func TestStructuredKnowledgeScopeNamedOperationKeepsServerContract(t *testing.T) {
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "kb-second", decodeStructuredScopeIdentity(t, r)["knowledge_base_id"])
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "query_series", body["operation"])
		require.Equal(t, map[string]any{"series_id": "selected-series", "operation": "sum"}, body["arguments"])
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}).WithSearchTargets(types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-first", TenantID: 7}, {Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-second", TenantID: 29}})
	directory := describedStructuredReadTools(t)
	original, err := json.Marshal(directory)
	require.NoError(t, err)
	tool := operationToolByName(t, NewStructuredReadOperationTools(base, directory), "structured_query_series")
	var schema map[string]any
	require.NoError(t, json.Unmarshal(tool.Parameters(), &schema))
	require.Contains(t, schema["properties"], "knowledge_base_id")
	require.Equal(t, []any{"series_id"}, schema["required"])
	after, err := json.Marshal(directory)
	require.NoError(t, err)
	require.JSONEq(t, string(original), string(after), "adding outer scope cannot mutate service metadata")
	result, err := tool.Execute(structuredScopeContext(), json.RawMessage(`{"series_id":"selected-series","operation":"sum","knowledge_base_id":"kb-second"}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	_, err = tool.Execute(structuredScopeContext(), json.RawMessage(`{"series_id":"selected-series","knowledge_base_id":"kb-outside"}`))
	require.ErrorContains(t, err, "授权范围")
}

func TestStructuredKnowledgeScopeCachePartitionsLibraryAndOwner(t *testing.T) {
	var calls atomic.Int64
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"status":"ok","source":"actual scoped service"}`)
	}).WithSearchTargets(types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-first", TenantID: 7}, {Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-second", TenantID: 29}})
	for _, kb := range []string{"kb-first", "kb-first", "kb-second", "kb-second"} {
		input := json.RawMessage(`{"operation":"query_series","knowledge_base_id":"` + kb + `","arguments":{"dataset_id":"` + cacheDatasetA + `","series_id":"same-field"}}`)
		_, err := base.Execute(structuredScopeContext(), input)
		require.NoError(t, err)
	}
	require.Equal(t, int64(2), calls.Load(), "identical dataset ID in distinct libraries must execute separately")
	base.WithSearchTargets(types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-first", TenantID: 29}})
	result, err := base.Execute(structuredScopeContext(), json.RawMessage(`{"operation":"query_series","arguments":{"dataset_id":"`+cacheDatasetA+`","series_id":"same-field"}}`))
	require.NoError(t, err)
	require.Nil(t, result.Data["_execution_cache"])
	require.Equal(t, int64(3), calls.Load(), "owning tenant is part of scope even if ID is unchanged")
}

func TestStructuredKnowledgeScopeRestrictedAPIKeyCannotReadLegacyDirectory(t *testing.T) {
	var calls atomic.Int64
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Len(t, decodeStructuredScopeIdentity(t, r), 5)
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	})
	request := json.RawMessage(`{"operation":"list_datasets","arguments":{}}`)
	for _, scope := range []types.TenantAPIKeyScope{
		{KnowledgeBaseIDs: types.StringArray{"kb-allowed"}},
		{},
	} {
		_, err := base.Execute(types.WithTenantAPIKeyScope(structuredScopeContext(), scope), request)
		require.Error(t, err, "a limited key cannot assign the unbound legacy store to a whitelisted native KB")
		require.Zero(t, calls.Load(), "scope rejection must happen before signing or transport")
	}
	for _, ctx := range []context.Context{
		structuredScopeContext(),
		types.WithTenantAPIKeyScope(structuredScopeContext(), types.TenantAPIKeyScope{FullAccess: true}),
	} {
		result, err := base.Execute(ctx, request)
		require.NoError(t, err)
		require.True(t, result.Success)
	}
	require.Equal(t, int64(2), calls.Load(), "unselected JWT and full-access keys retain their existing legacy policy")
}

func TestStructuredKnowledgeScopeRestrictedAPIKeyAllowsVerifiedLibraryAndRejectsCachedOutsideScope(t *testing.T) {
	var calls atomic.Int64
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		identity := decodeStructuredScopeIdentity(t, r)
		require.Equal(t, "kb-allowed", identity["knowledge_base_id"])
		require.Equal(t, float64(29), identity["knowledge_base_tenant_id"])
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}).WithSearchTargets(types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-allowed", TenantID: 29}})
	request := json.RawMessage(`{"operation":"query_series","knowledge_base_id":"kb-allowed","arguments":{"dataset_id":"` + cacheDatasetA + `","series_id":"same-field"}}`)
	allowed := types.WithTenantAPIKeyScope(structuredScopeContext(), types.TenantAPIKeyScope{KnowledgeBaseIDs: types.StringArray{"kb-allowed"}})
	result, err := base.Execute(allowed, request)
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, int64(1), calls.Load())
	denied := types.WithTenantAPIKeyScope(structuredScopeContext(), types.TenantAPIKeyScope{KnowledgeBaseIDs: types.StringArray{"another-kb"}})
	_, err = base.Execute(denied, request)
	require.Error(t, err, "a cached result cannot bypass changed API-key KB permissions")
	require.Equal(t, int64(1), calls.Load())
}

func TestStructuredKnowledgeScopeAPIKeyDirectoryRemainsAuthorizedMetadata(t *testing.T) {
	var calls atomic.Int64
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "kb-first", decodeStructuredScopeIdentity(t, r)["knowledge_base_id"])
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}).WithSearchTargets(types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-first", TenantID: 7}, {Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-second", TenantID: 29}})
	request := json.RawMessage(`{"operation":"list_datasets","arguments":{}}`)
	allowed := types.WithTenantAPIKeyScope(structuredScopeContext(), types.TenantAPIKeyScope{KnowledgeBaseIDs: types.StringArray{"kb-first", "kb-second"}})
	result, err := base.Execute(allowed, request)
	require.NoError(t, err)
	require.Equal(t, "scope_selection_required", result.Data["status"])
	require.Zero(t, calls.Load(), "the directory is metadata, not an unbound Excel read")
	restricted := types.WithTenantAPIKeyScope(structuredScopeContext(), types.TenantAPIKeyScope{KnowledgeBaseIDs: types.StringArray{"kb-first"}})
	_, err = base.Execute(restricted, request)
	require.Error(t, err, "a stale directory containing a newly forbidden KB must not leak")
	require.Zero(t, calls.Load())
	result, err = base.Execute(restricted, json.RawMessage(`{"operation":"list_datasets","knowledge_base_id":"kb-first","arguments":{}}`))
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, int64(1), calls.Load())
}
