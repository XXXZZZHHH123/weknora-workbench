package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestWorkbenchMultiLibraryDiscoveryPreservesPendingScopeAndRegistersSchemas(t *testing.T) {
	var requests atomic.Int64
	registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var payload map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		raw, err := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Research-Identity"))
		require.NoError(t, err)
		var identity map[string]any
		require.NoError(t, json.Unmarshal(raw, &identity))
		switch payload["operation"] {
		case "describe_tools":
			require.NotContains(t, identity, "knowledge_base_id")
			_, _ = io.WriteString(w, `{"tools":[{"name":"list_series","description":"List the actual selected dataset fields","inputSchema":{"type":"object","properties":{"dataset_id":{"type":"string"}}}}]}`)
		case "list_datasets":
			require.Equal(t, "measurements", identity["knowledge_base_id"])
			_, _ = io.WriteString(w, `{"datasets":[{"dataset_id":"real-dataset","source_file":"reviewed.xlsx"}]}`)
		default:
			t.Errorf("unexpected operation %v", payload["operation"])
		}
	})
	tool, err := registry.GetTool(agenttools.ToolStructuredData)
	require.NoError(t, err)
	base := tool.(*agenttools.StructuredDataTool)
	base.WithSearchTargets(types.SearchTargets{
		{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "reports", TenantID: 7},
		{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "measurements", TenantID: 7},
	}).WithKnowledgeBaseMetadata([]*types.KnowledgeBase{
		{ID: "reports", TenantID: 7, Name: "Periodic reports"},
		{ID: "measurements", TenantID: 7, Name: "Reviewed measurements", Description: "Date-indexed workbook tables"},
	})
	engine := newTestEngine(t, &mockChat{})
	engine.toolRegistry = registry
	engine.config.WorkbenchDefault = true
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
	state := &types.AgentState{}
	messages := engine.discoverWorkbenchSources(ctx, state, nil, "Draw the requested source time series", "session", "message")
	require.Len(t, state.RoundSteps, 1)
	require.Equal(t, int64(1), requests.Load(), "preflight may fetch only universal protocol schemas, never an arbitrary business library")
	require.False(t, hasCurrentSourceMetadataEvidence(state.RoundSteps), "pending selection and schemas cannot establish any dataset catalogue, empty or otherwise")
	combined := ""
	for _, m := range messages {
		combined += m.Content
	}
	require.Contains(t, combined, "scope_selection_required")
	require.Contains(t, combined, "not_queried")
	require.Contains(t, combined, "Reviewed measurements")
	require.Contains(t, combined, "Date-indexed workbook tables")
	_, err = registry.GetTool("structured_list_series")
	require.NoError(t, err, "actual described schemas must be available after multi-library preflight")
	result, err := base.Execute(ctx, json.RawMessage(`{"operation":"list_datasets","knowledge_base_id":"measurements","arguments":{}}`))
	require.NoError(t, err)
	require.Contains(t, result.Output, "real-dataset")
	require.Equal(t, int64(2), requests.Load())
}
