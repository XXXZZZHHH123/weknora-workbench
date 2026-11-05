package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestStructuredScopeFailuresRetainLegacyErrorAndVersionedAuthorizedRecovery(t *testing.T) {
	var calls atomic.Int64
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	base.WithSearchTargets(types.SearchTargets{
		{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-allowed", TenantID: 7},
		{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-hidden", TenantID: 29},
	}).WithKnowledgeBaseMetadata([]*types.KnowledgeBase{
		{ID: "kb-allowed", TenantID: 7, Name: "Allowed directory name"},
		{ID: "kb-hidden", TenantID: 29, Name: "Forbidden directory name"},
	})
	r := NewToolRegistry()
	r.RegisterTool(base)
	installed, err := r.InstallStructuredManifest(structuredProtocolDirectory("a"))
	require.NoError(t, err)
	require.True(t, installed.Success)
	descriptor, _, native := r.StructuredInvocation("structured_list_datasets", json.RawMessage(`{}`))
	require.True(t, native)
	ctx := types.WithTenantAPIKeyScope(structuredScopeContext(), types.TenantAPIKeyScope{KnowledgeBaseIDs: types.StringArray{"kb-allowed"}})
	for _, tc := range []struct {
		name, argument, code string
		retryable            bool
	}{
		{"durable URI is not a transport ID", `"weknora://kb/kb-allowed"`, "scope_id_invalid", true},
		{"out of source grants", `"kb-outside"`, "scope_not_authorized", false},
		{"API key no longer permits source", `"kb-hidden"`, "scope_not_authorized", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := r.ExecuteTool(ctx, descriptor.ToolName, json.RawMessage(`{"knowledge_base_id":`+tc.argument+`}`))
			require.Error(t, err, "legacy callers still observe the same rejection")
			require.NotNil(t, result)
			require.False(t, result.Success)
			require.Equal(t, tc.code, result.Data["code"])
			require.Equal(t, tc.code, result.Data["reason_code"])
			require.Equal(t, "scope", result.Data["stage"])
			require.Equal(t, tc.retryable, result.Data["retryable"])
			require.Equal(t, false, result.Data["execution_started"])
			require.Equal(t, false, result.Data["source_data_read"])
			require.Equal(t, descriptor.ManifestSHA256, result.Data["manifest_version"])
			require.Equal(t, descriptor.SchemaSHA256, result.Data["schema_sha256"])
			require.Equal(t, descriptor.ProtocolVersion, result.Data["protocol_version"])
			require.Equal(t, descriptor.Generation, result.Data["generation"])
			require.Equal(t, descriptor, result.Data["_structured_execution"])
			recovery := result.Data["scope_recovery"].(map[string]any)
			require.Equal(t, "knowledge_base_id", recovery["selection_argument"])
			require.Equal(t, []map[string]any{{"knowledge_base_id": "kb-allowed", "name": "Allowed directory name"}}, recovery["knowledge_bases"])
			require.NotContains(t, result.Output, "kb-hidden")
			require.NotContains(t, result.Output, "Forbidden directory name")
			var visible map[string]any
			require.NoError(t, json.Unmarshal([]byte(result.Output), &visible))
			require.Equal(t, tc.code, visible["code"], "the model gets the machine diagnostic, not just UI metadata")
		})
	}
	// Registry validation/casting keeps its own earlier boundary. Exercise
	// malformed raw values at the actual adapter too, without changing either
	// contract just to make a scope-diagnostic assertion pass.
	adapter, err := r.GetTool(descriptor.ToolName)
	require.NoError(t, err)
	for _, malformed := range []string{`null`, `5`} {
		result, err := adapter.Execute(ctx, json.RawMessage(`{"knowledge_base_id":`+malformed+`}`))
		require.Error(t, err)
		require.Equal(t, "scope_id_invalid", result.Data["code"])
		require.Equal(t, "scope", result.Data["stage"])
	}
	invalid, err := r.ExecuteTool(ctx, descriptor.ToolName, json.RawMessage(`{"knowledge_base_id":null}`))
	require.Error(t, err, "an optional null selector passes schema validation and is rejected by the scope guard")
	require.Equal(t, "scope_id_invalid", invalid.Data["code"])
	require.Equal(t, "scope", invalid.Data["stage"])
	cast, err := r.ExecuteTool(ctx, descriptor.ToolName, json.RawMessage(`{"knowledge_base_id":5}`))
	require.Error(t, err)
	require.Equal(t, "scope_not_authorized", cast.Data["code"], "the existing string cast does not authorize its resulting ID")
	require.Zero(t, calls.Load(), "all failures remain before transport and cannot replay data/state operations")

	denied := types.WithTenantAPIKeyScope(structuredScopeContext(), types.TenantAPIKeyScope{KnowledgeBaseIDs: types.StringArray{"another-kb"}})
	result, err := r.ExecuteTool(denied, descriptor.ToolName, json.RawMessage(`{"knowledge_base_id":"weknora://kb/kb-allowed"}`))
	require.Error(t, err)
	require.NotContains(t, result.Data, "scope_recovery", "permissions are reapplied before exposing the recovery directory")
	require.NotContains(t, result.Output, "Allowed directory name")
	require.NotContains(t, result.Output, "kb-allowed")
	require.Equal(t, false, result.Data["retryable"])
	result, err = r.ExecuteTool(context.Background(), descriptor.ToolName, json.RawMessage(`{"knowledge_base_id":"weknora://kb/kb-allowed"}`))
	require.Error(t, err)
	require.NotContains(t, result.Data, "scope_recovery", "no authenticated caller means no directory disclosure")
	require.Zero(t, calls.Load())
}

func TestStructuredScopeDiagnosticsDoNotExpandDocumentTargetsOrBadGrants(t *testing.T) {
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) { t.Error("no transport expected") })
	for _, targets := range []types.SearchTargets{
		{{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: "kb-limited", TenantID: 7, KnowledgeIDs: []string{"only-document"}}},
		{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-limited", TenantID: 7}, {Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-invalid", TenantID: 0}},
	} {
		base.WithSearchTargets(targets).WithKnowledgeBaseMetadata([]*types.KnowledgeBase{{ID: "kb-limited", TenantID: 7, Name: "Must not be disclosed"}})
		result, err := base.Execute(structuredScopeContext(), json.RawMessage(`{"operation":"list_datasets","arguments":{}}`))
		require.Error(t, err)
		require.False(t, result.Success)
		require.Equal(t, "scope", result.Data["stage"])
		require.NotContains(t, result.Data, "scope_recovery")
		require.NotContains(t, result.Output, "Must not be disclosed")
	}
}

func TestStructuredMissingSelectionDirectoryKeepsVersionAndDoesNotChooseLibrary(t *testing.T) {
	var calls atomic.Int64
	base := nativeReadTestTool(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }).WithSearchTargets(types.SearchTargets{
		{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-one", TenantID: 7},
		{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-two", TenantID: 7},
	})
	r := NewToolRegistry()
	r.RegisterTool(base)
	_, err := r.InstallStructuredManifest(structuredProtocolDirectory("b"))
	require.NoError(t, err)
	result, err := base.Execute(structuredScopeContext(), json.RawMessage(`{"operation":"query_pdf_table","arguments":{"table_id":"actual-table","sql":"SELECT * FROM data"}}`))
	require.NoError(t, err)
	require.False(t, result.Success)
	require.Equal(t, "scope_selection_required", result.Data["code"])
	require.Equal(t, "scope", result.Data["stage"])
	require.Equal(t, false, result.Data["execution_started"])
	require.Equal(t, strings.Repeat("b", 64), result.Data["manifest_version"])
	require.NotEmpty(t, result.Data["schema_sha256"])
	require.Equal(t, []map[string]any{{"knowledge_base_id": "kb-one", "knowledge_base_tenant_id": uint64(7), "metadata_status": "unavailable"}, {"knowledge_base_id": "kb-two", "knowledge_base_tenant_id": uint64(7), "metadata_status": "unavailable"}}, result.Data["knowledge_bases"])
	require.Zero(t, calls.Load())
}
