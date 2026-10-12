package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

func TestWorkbenchSourceDiscoveryShowsAuthorizedFilesBeforeFirstModelCall(t *testing.T) {
	operations := []string{}
	var mu sync.Mutex
	registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Operation string `json:"operation"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
		mu.Lock()
		operations = append(operations, input.Operation)
		mu.Unlock()
		_, _ = io.WriteString(w, `{"datasets":[{"dataset_id":"real-version","source_file":"真实原表.xlsx","is_latest_for_source":true}]}`)
	})
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: "你好。", Done: true, FinishReason: "stop"}}}}}
	engine := newTestEngine(t, model)
	engine.toolRegistry = registry
	engine.config.WorkbenchDefault = true
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
	state, err := engine.Execute(ctx, "session", "message", "你好", []chat.Message{})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"list_datasets", "describe_tools"}, operations, "discovery never runs a numeric query for a greeting")
	require.Len(t, model.calls, 1)
	found := false
	for _, message := range model.calls[0] {
		if message.Role == "tool" && strings.Contains(message.Content, "真实原表.xlsx") {
			found = true
		}
	}
	require.True(t, found, "the model must see the actual catalogue before choosing document-only retrieval")
	require.Equal(t, "你好。", state.FinalAnswer)
	require.False(t, hasStructuredEvidence(state.RoundSteps), "catalogue metadata is not computed evidence")
}

// Exercise the production SearchKnowledgeTool: discovery supplies the question,
// while this tool retains the authorized targets computed at request entry.
// Unimplemented service methods panic if discovery silently expands its reads.
type sourceDiscoveryKnowledgeService struct {
	interfaces.KnowledgeBaseService
	mu       sync.Mutex
	lookups  [][]string
	searches []sourceDiscoverySearch
}

type sourceDiscoverySearch struct {
	baseID string
	params types.SearchParams
	caller types.Caller
}

func (s *sourceDiscoveryKnowledgeService) GetKnowledgeBasesByIDsOnly(_ context.Context, ids []string) ([]*types.KnowledgeBase, error) {
	s.mu.Lock()
	s.lookups = append(s.lookups, append([]string(nil), ids...))
	s.mu.Unlock()
	var bases []*types.KnowledgeBase
	for _, id := range ids {
		base := &types.KnowledgeBase{ID: id, Type: types.KnowledgeBaseTypeDocument}
		base.IndexingStrategy.KeywordEnabled = true
		bases = append(bases, base)
	}
	return bases, nil
}

func (*sourceDiscoveryKnowledgeService) ResolveEmbeddingModelKeys(context.Context, []*types.KnowledgeBase) map[string]string {
	return nil
}

func (s *sourceDiscoveryKnowledgeService) HybridSearch(ctx context.Context, id string, params types.SearchParams) ([]*types.SearchResult, error) {
	s.mu.Lock()
	s.searches = append(s.searches, sourceDiscoverySearch{baseID: id, params: params, caller: types.CallerFromContext(ctx)})
	s.mu.Unlock()
	return nil, nil
}

func TestWorkbenchSourceDiscoveryPrefetchesOriginalQuestionWithinExistingKnowledgeScope(t *testing.T) {
	const question = "2022/23年度中国棉花产量加进口，合计是多少？"
	service := &sourceDiscoveryKnowledgeService{}
	targets := types.SearchTargets{
		{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "granted-base", TenantID: 7},
		{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: "granted-doc-scope", TenantID: 7,
			KnowledgeIDs: []string{"selected-document"}, TagIDs: []string{"physical-tag"}, ScopeTagIDs: []string{"user-selected-tag"}},
	}
	registry := agenttools.NewToolRegistry()
	registry.RegisterTool(agenttools.NewSearchKnowledgeTool(service, nil, nil, targets, nil, nil))
	unrelated := newCountingTool(agenttools.ToolReadDocument)
	registry.RegisterTool(unrelated)
	before := registry.ListTools()
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: "需要核对年度定义。", Done: true, FinishReason: "stop"}}}}}
	engine := newTestEngine(t, model)
	engine.toolRegistry = registry
	engine.config.WorkbenchDefault = true
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
	state, err := engine.Execute(ctx, "session", "message", question, nil)
	require.NoError(t, err)
	require.Equal(t, before, registry.ListTools(), "knowledge discovery cannot register unrelated providers")
	require.Zero(t, unrelated.calls, "prefetch searches sources; it does not read unrequested documents")
	require.Len(t, service.lookups, 1)
	require.ElementsMatch(t, []string{"granted-base", "granted-doc-scope"}, service.lookups[0])
	require.Len(t, service.searches, 2)
	for _, request := range service.searches {
		require.Equal(t, question, request.params.QueryText, "do not replace the original question with guessed keywords or answers")
		require.Equal(t, uint64(7), request.caller.TenantID)
		require.Equal(t, "verified", request.caller.UserID)
		switch request.baseID {
		case "granted-base":
			require.Equal(t, []string{"granted-base"}, request.params.KnowledgeBaseIDs)
			require.Empty(t, request.params.KnowledgeIDs)
		case "granted-doc-scope":
			require.Empty(t, request.params.KnowledgeBaseIDs, "a document scope cannot become a full-base search")
			require.Equal(t, []string{"selected-document"}, request.params.KnowledgeIDs)
			require.Equal(t, []string{"physical-tag"}, request.params.TagIDs)
			require.Equal(t, []string{"user-selected-tag"}, request.params.ScopeTagIDs)
		default:
			t.Fatalf("unexpected expanded knowledge base %q", request.baseID)
		}
	}
	require.Len(t, model.calls, 1)
	require.Len(t, state.RoundSteps[0].ToolCalls, 1)
	call := state.RoundSteps[0].ToolCalls[0]
	require.Equal(t, agenttools.ToolSearchKnowledge, call.Name)
	require.Equal(t, question, call.Args["query"])
	require.True(t, call.Result.Success, "an empty authorized retrieval is still a real completed search")
	found := false
	for _, message := range model.calls[0] {
		found = found || (message.Role == "tool" && message.Name == agenttools.ToolSearchKnowledge && strings.Contains(message.Content, question))
	}
	require.True(t, found, "the first model call must already see the actual document search outcome")
}

func TestWorkbenchSourceDiscoveryExposesDescribedNativeSchemasInFirstModelCall(t *testing.T) {
	fixture, err := os.ReadFile("tools/testdata/structured_read_operations.json")
	require.NoError(t, err)
	var directory struct {
		Tools []struct {
			Name        string          `json:"name"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(fixture, &directory))
	require.Len(t, directory.Tools, 5)
	var requested []string
	var requestedMu sync.Mutex
	registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Operation string `json:"operation"`
			Arguments struct {
				Operations []string `json:"operations"`
			} `json:"arguments"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
		switch input.Operation {
		case "list_datasets":
			_, _ = io.WriteString(w, `{"datasets":[]}`)
		case "describe_tools":
			requestedMu.Lock()
			requested = append([]string(nil), input.Arguments.Operations...)
			requestedMu.Unlock()
			_, _ = w.Write(fixture)
		default:
			t.Errorf("discovery must not perform an unsolicited calculation: %s", input.Operation)
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	retained := newCountingTool(agenttools.ToolReadDocument)
	registry.RegisterTool(retained)
	model := &mockChat{responses: []mockResponse{{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, Content: "你好。", Done: true, FinishReason: "stop"}}}}}
	engine := newTestEngine(t, model)
	engine.toolRegistry = registry
	engine.config.WorkbenchDefault = true
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
	_, err = engine.Execute(ctx, "session", "message", "你好", nil)
	require.NoError(t, err)
	require.Len(t, model.opts, 1)
	requestedMu.Lock()
	describedOperations := append([]string(nil), requested...)
	requestedMu.Unlock()
	require.ElementsMatch(t, []string{"list_series", "query_series", "analyze_table", "inspect_source", "get_result_evidence"}, describedOperations)
	firstDefinitions := map[string]json.RawMessage{}
	for _, tool := range model.opts[0].Tools {
		firstDefinitions[tool.Function.Name] = tool.Function.Parameters
	}
	for _, described := range directory.Tools {
		name := "structured_" + described.Name
		require.Contains(t, firstDefinitions, name, "schema registration must precede the first reasoning call")
		require.JSONEq(t, string(described.InputSchema), string(firstDefinitions[name]), "use the real service schema, not a reduced argument wrapper")
	}
	require.Contains(t, firstDefinitions, agenttools.ToolReadDocument, "existing tools remain available")
	require.NotContains(t, firstDefinitions, "structured_confirm_fields", "source discovery cannot advertise a write or user-confirmation action")
	require.Zero(t, retained.calls)
}

func TestWorkbenchSourceDiscoveryKeepsFailureDistinctFromNoFiles(t *testing.T) {
	registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	engine := newTestEngine(t, nil)
	engine.toolRegistry = registry
	engine.config.WorkbenchDefault = true
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
	state := &types.AgentState{}
	messages := engine.discoverWorkbenchSources(ctx, state, nil, "问题", "session", "message")
	require.Len(t, state.RoundSteps, 1)
	require.False(t, state.RoundSteps[0].ToolCalls[0].Result.Success)
	require.Contains(t, messages[len(messages)-1].Content, "503")
	require.NotContains(t, messages[len(messages)-1].Content, `"datasets":[]`)
}

func TestWorkbenchSourceDiscoveryDoesNotExpandUnrelatedAgentsOrMissingIdentity(t *testing.T) {
	calls := 0
	registry := nativeStructuredRegistry(t, func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = io.WriteString(w, `{}`) })
	engine := newTestEngine(t, nil)
	engine.toolRegistry = registry
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
	state := &types.AgentState{}
	require.Empty(t, engine.discoverWorkbenchSources(ctx, state, nil, "问题", "session", "message"))
	engine.config.WorkbenchDefault = true
	require.Empty(t, engine.discoverWorkbenchSources(context.Background(), state, nil, "问题", "session", "message"))
	require.Zero(t, calls)
	require.Empty(t, state.RoundSteps)
}
