package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type coverageScopeDocuments struct {
	*sourceTimeDocumentStore
	documents []*types.Knowledge
}

func (s *coverageScopeDocuments) ListPagedKnowledgeByKnowledgeBaseID(_ context.Context, id string, _ *types.Pagination, _ types.KnowledgeListFilter) (*types.PageResult, error) {
	var found []*types.Knowledge
	for _, document := range s.documents {
		if document.KnowledgeBaseID == id {
			found = append(found, document)
		}
	}
	return &types.PageResult{Data: found, Total: int64(len(found))}, nil
}

type coverageScopeChunks struct {
	interfaces.ChunkRepository
	reads  []string
	bodies map[string]string
}

func (r *coverageScopeChunks) ListPagedChunksByKnowledgeID(_ context.Context, _ uint64, document string, _ *types.Pagination, _ []types.ChunkType, _ []string, _ string, _ string, _ string, _ string, _ *bool) ([]*types.Chunk, int64, error) {
	r.reads = append(r.reads, document)
	content := firstNonEmptyAgent(r.bodies[document], "The maintenance record shows a completed inspection.")
	return []*types.Chunk{{ID: document + "-chunk", KnowledgeID: document, KnowledgeBaseID: "scope-library", TenantID: 7,
		ChunkType: types.ChunkTypeText, IsEnabled: true, Content: content}}, 1, nil
}

func (*coverageScopeChunks) ListChunksByParentIDs(context.Context, uint64, []string) ([]*types.Chunk, error) {
	return nil, nil
}

type coverageScopeChunkService struct {
	interfaces.ChunkService
	repository *coverageScopeChunks
}

func (s *coverageScopeChunkService) GetRepository() interfaces.ChunkRepository { return s.repository }

func TestEvidenceCompletionNativeOnlyReadsAndMixedTargets(t *testing.T) {
	for _, test := range []struct {
		mode               string
		exact, pair, mixed bool
	}{
		{mode: ""}, {mode: "invalid"}, {mode: "true"},
		{mode: "false", exact: true}, {mode: "true", exact: true},
		{mode: "false", pair: true}, {mode: "true", pair: true},
		{mode: "false", mixed: true}, {mode: "true", mixed: true},
	} {
		name := test.mode
		if test.exact {
			name += "/exact"
		}
		if test.pair {
			name += "/pair"
		}
		if test.mixed {
			name += "/mixed"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", test.mode)
			const question, library = "Use only the selected inspection record for the inspection finding.", "scope-library"
			ctx := types.WithCaller(t.Context(), types.Caller{TenantID: 7, UserID: "reader", Role: types.TenantRoleViewer})
			e := newTestEngine(t, &mockChat{})
			e.config.WorkbenchDefault, e.config.AnswerReviewMode = true, "standard"
			grants := types.SearchTargets{
				{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: library, TenantID: 7},
				{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "new-library", TenantID: 7},
			}
			e.config.SearchTargets = grants
			records := &coverageScopeDocuments{sourceTimeDocumentStore: &sourceTimeDocumentStore{byID: make(map[string]*types.Knowledge)}}
			for _, id := range []string{"original", "second", "unrelated"} {
				document := &types.Knowledge{ID: id, KnowledgeBaseID: library, TenantID: 7, Title: id + " inspection record",
					FileName: id + ".pdf", FileType: "pdf", FileHash: strings.Repeat("a", 64), ParseStatus: types.ParseStatusCompleted}
				records.documents = append(records.documents, document)
				records.byID[document.ID] = document
			}
			repository := &coverageScopeChunks{}
			e.toolRegistry = agenttools.NewToolRegistry()
			e.toolRegistry.RegisterTool(agenttools.NewReadDocumentTool(records, &coverageScopeChunkService{repository: repository}, grants))
			e.toolRegistry.RegisterTool(agenttools.NewListDocumentsTool(records, grants))
			agenttools.PrepareDocumentScope(e.toolRegistry, question)
			plan := &taskEvidencePlan{CoverageTargets: []taskCoverageTarget{{
				RequestBasis: question, StructuredTaskGoal: &StructuredTaskGoal{SourceMode: "only", RestrictionBasis: question, PrimaryRef: "inspection record"},
			}}}
			if test.exact {
				plan.DeliveryRequirements = []taskDeliveryRequirement{{Kind: "text", Format: "exact"}}
			}
			if test.pair {
				plan.CoverageTargets[0].VersionTask = "compare_versions"
			}
			if test.mixed {
				plan.CoverageTargets = append(plan.CoverageTargets, taskCoverageTarget{GoalIndex: 1, RequestBasis: question,
					StructuredTaskGoal: &StructuredTaskGoal{SourceMode: "open"}})
			}
			e.taskPlan = plan
			state := &types.AgentState{}
			messages := []chat.Message{{Role: "user", Content: question}}
			invoke := func(callContext context.Context, id, tool string, args any) types.ToolCall {
				t.Helper()
				raw, err := json.Marshal(args)
				require.NoError(t, err)
				calls := []types.LLMToolCall{{ID: id, Function: types.FunctionCall{Name: tool, Arguments: string(raw)}}}
				e.processEvidenceCompletionCalls(callContext, question, state, messages, calls)
				call := e.runToolCall(callContext, calls[0], 0, 0, 0, "session", "turn")
				state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{call}})
				e.refreshEvidenceCompletion(callContext, question, state, messages)
				return call
			}
			list := invoke(ctx, "directory", agenttools.ToolListDocuments, map[string]any{"knowledge_base_id": library})
			require.True(t, list.Result.Success, list.Result.Error)
			require.Equal(t, test.mode == "true", e.evidenceCompletionEnabled())
			require.Len(t, e.evidenceCompletion.record.Evidence, 3)
			require.Empty(t, repository.reads)
			selection := func(ids ...string) map[string]any {
				var sources []map[string]any
				for _, id := range ids {
					sources = append(sources, map[string]any{"knowledge_id": id, "expected_file_name": id + ".pdf"})
				}
				return map[string]any{"selection_reason": "Selected the requested sources from the authorized directory.", "sources": sources}
			}
			if test.mixed {
				bind := selection("original")
				bind[coverageArgument] = map[string]any{"goal_index": 0}
				require.True(t, invoke(ctx, "native-bind", agenttools.ToolBindDocumentScope, bind).Result.Success)
			} else {
				ids := []string{"original"}
				if test.pair {
					ids = append(ids, "second")
				}
				require.True(t, invoke(ctx, "native-bind", agenttools.ToolBindDocumentScope, selection(ids...)).Result.Success)
			}
			only := &plan.CoverageTargets[0]
			require.Len(t, e.evidenceCompletion.nativeSelections[0], 1+map[bool]int{true: 1}[test.pair])
			allowed := invoke(ctx, "original-read", agenttools.ToolReadDocument, map[string]any{"id": "original",
				coverageArgument: map[string]any{"goal_index": 0, "selection_ref": "forged", "gap_id": "unrelated"}})
			require.True(t, allowed.Result.Success, allowed.Result.Error)
			require.Equal(t, []string{"original"}, repository.reads)
			require.NotContains(t, allowed.Args, coverageArgument)
			if test.pair {
				second := invoke(ctx, "second-read", agenttools.ToolReadDocument, map[string]any{"id": "second"})
				require.True(t, second.Result.Success, second.Result.Error)
				require.Equal(t, []string{"original", "second"}, repository.reads)
			}
			taken := slices.Clone(repository.reads)
			outside := invoke(ctx, "other-read", agenttools.ToolReadDocument, map[string]any{"id": "unrelated",
				coverageArgument: map[string]any{"goal_index": 0}})
			require.Equal(t, coverageScopeRejection, outside.Result.Error)
			require.Equal(t, taken, repository.reads)
			item := coverageEvidence{SourceDomain: "knowledge", SourceID: "unrelated", KnowledgeBaseID: library}
			require.False(t, e.coverageEvidenceAllowed(ctx, only, item))
			records.byID["original"].KnowledgeBaseID = "new-library"
			moved := invoke(ctx, "moved-read", agenttools.ToolReadDocument, map[string]any{"id": "original", coverageArgument: map[string]any{"goal_index": 0}})
			require.True(t, moved.Result.PreflightRejected)
			require.False(t, moved.Result.Success)
			require.Equal(t, taken, repository.reads, "the live native resolver rejected the moved object before reading its content")
			records.byID["original"].KnowledgeBaseID = library
			if test.mixed {
				open := invoke(ctx, "open-read", agenttools.ToolReadDocument, map[string]any{"id": "unrelated", "source_scope": selection("unrelated")})
				require.True(t, open.Result.Success, open.Result.Error)
				require.Equal(t, append(taken, "unrelated"), repository.reads)
				require.False(t, e.coverageEvidenceAllowed(ctx, only, item))
				require.True(t, e.coverageEvidenceAllowed(ctx, &plan.CoverageTargets[1], item))
				require.False(t, e.coverageEvidenceAllowed(ctx, nil, item), "a finding outside the only scope needs independent role attribution")
			}
			revokedCtx := types.WithTenantAPIKeyScope(ctx, types.TenantAPIKeyScope{KeyID: 1, KnowledgeBaseIDs: types.StringArray{"elsewhere"}})
			before := slices.Clone(repository.reads)
			revoked := invoke(revokedCtx, "revoked", agenttools.ToolReadDocument, map[string]any{"id": "original",
				coverageArgument: map[string]any{"goal_index": 0}})
			require.Equal(t, coverageScopeRejection, revoked.Result.Error)
			require.Equal(t, before, repository.reads)
			require.False(t, e.coverageEvidenceAllowed(revokedCtx, only, coverageEvidence{SourceDomain: "knowledge", SourceID: "original", KnowledgeBaseID: library}))
			parallel := []types.LLMToolCall{
				{ID: "parallel-1", Function: types.FunctionCall{Name: agenttools.ToolReadDocument, Arguments: `{"id":"original","task_coverage":{"goal_index":0}}`}},
				{ID: "parallel-2", Function: types.FunctionCall{Name: agenttools.ToolReadDocument, Arguments: `{"id":"original","task_coverage":{"goal_index":0}}`}},
			}
			e.processEvidenceCompletionCalls(revokedCtx, question, state, messages, parallel)
			step := types.AgentStep{}
			require.Nil(t, e.executeToolCallsParallel(revokedCtx, &types.ChatResponse{ToolCalls: parallel}, &step, 0, "session", "turn"))
			require.Len(t, step.ToolCalls, 2)
			for _, call := range step.ToolCalls {
				require.Equal(t, coverageScopeRejection, call.Result.Error)
			}
			require.Equal(t, before, repository.reads)
		})
	}
}

func TestEvidenceCompletionStructuredScopeNativeSelectionAndAuthorizedReads(t *testing.T) {
	for _, mode := range []string{"false", "true"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", mode)
			t.Setenv("STRUCTURED_DATA_URL", "http://structured.invalid")
			token := filepath.Join(t.TempDir(), "fixture-token")
			require.NoError(t, os.WriteFile(token, []byte(strings.Repeat("a", 32)), 0600))
			t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", token)
			const library, otherLibrary, dataset = "field-library", "other-library", "data-instance"
			var sent []string
			originalTransport := http.DefaultTransport
			http.DefaultTransport = qa4Stage2Transport(func(request *http.Request) (*http.Response, error) {
				var packet struct {
					Operation string
					Arguments map[string]any
				}
				require.NoError(t, json.NewDecoder(request.Body).Decode(&packet))
				identityJSON, err := base64.RawURLEncoding.DecodeString(request.Header.Get("X-Research-Identity"))
				require.NoError(t, err)
				var identity map[string]any
				require.NoError(t, json.Unmarshal(identityJSON, &identity))
				require.Equal(t, library, identity["knowledge_base_id"])
				require.NotEmpty(t, request.Header.Get("X-Research-Signature"))
				require.NotContains(t, packet.Arguments, coverageArgument)
				sent = append(sent, packet.Operation)
				var response string
				source := `{"file":"inspection.xlsx","sha256":"` + strings.Repeat("b", 64) + `","sheet":"measurements"}`
				series := `{"series_id":"runtime","sheet":"measurements","metric":"running hours","header_cells":["B1"]}`
				switch packet.Operation {
				case "list_datasets":
					response = `{"status":"ok","datasets_truncated":false,"datasets":[{"dataset_id":"data-instance","title":"inspection.xlsx"},{"dataset_id":"unrelated","title":"inspection.xlsx"}]}`
				case "list_series":
					require.Equal(t, dataset, packet.Arguments["dataset_id"])
					response = `{"status":"ok","dataset_id":"data-instance","truncated":false,"source":` + source + `,"series":[` + series + `]}`
				case "query_series":
					require.Equal(t, dataset, packet.Arguments["dataset_id"])
					response = `{"status":"ok","dataset_id":"data-instance","truncated":false,"source":` + source + `,"series":` + series +
						`,"rows":[{"date":"2025-01-01","date_cell":"A2","source_cell":"B2","value":13,"value_status":"valid"}],"filters":{"series_id":"runtime","value_filters":[]}}`
				default:
					t.Fatalf("unexpected structured read of %s", packet.Operation)
				}
				header := http.Header{}
				header.Set("X-Structured-Manifest-SHA256", request.Header.Get("X-Structured-Manifest-SHA256"))
				return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(response))}, nil
			})
			t.Cleanup(func() { http.DefaultTransport = originalTransport })
			e := newTestEngine(t, &mockChat{})
			e.config.WorkbenchDefault, e.config.AnswerReviewMode = true, "standard"
			e.config.SearchTargets = types.SearchTargets{
				{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: library, TenantID: 7},
				{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: otherLibrary, TenantID: 7},
			}
			e.toolRegistry = agenttools.NewToolRegistry()
			e.toolRegistry.RegisterTool(agenttools.NewStructuredDataTool().WithSearchTargets(e.config.SearchTargets))
			e.toolRegistry.PrepareStructuredProtocol()
			manifest := structuredCodecManifest()
			for _, raw := range manifest["tools"].([]any) {
				entry := raw.(map[string]any)
				properties := entry["inputSchema"].(map[string]any)["properties"].(map[string]any)
				for _, name := range []string{"dataset_id", "series_id", "table_id"} {
					properties[name] = map[string]any{"type": "string"}
				}
			}
			registered, err := e.toolRegistry.InstallStructuredManifest(manifest)
			require.NoError(t, err)
			require.True(t, registered.Success)
			e.taskPlan = &taskEvidencePlan{SourceDiscovery: &taskSourceDiscovery{Mode: "discover", SourceDomains: []string{"structured"}},
				CoverageTargets: []taskCoverageTarget{{StructuredTaskGoal: &StructuredTaskGoal{SourceMode: "only", PrimaryRef: "inspection.xlsx"}}}}
			ctx := types.WithCaller(t.Context(), types.Caller{TenantID: 7, UserID: "reader", Role: types.TenantRoleViewer})
			state := &types.AgentState{}
			const question = "Use only inspection.xlsx to find running hours."
			execute := func(callContext context.Context, id, operation, kb string, args map[string]any, forged any) *types.ToolResult {
				t.Helper()
				input := maps.Clone(args)
				input["knowledge_base_id"] = kb
				if forged != nil {
					input[coverageArgument] = forged
				}
				raw, err := json.Marshal(input)
				require.NoError(t, err)
				calls := []types.LLMToolCall{{ID: id, Function: types.FunctionCall{Name: "structured_" + operation, Arguments: string(raw)}}}
				e.processEvidenceCompletionCalls(callContext, question, state, nil, calls)
				result := e.runToolCall(callContext, calls[0], 0, 0, 0, "session", "turn")
				state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{result}})
				e.refreshEvidenceCompletion(callContext, question, state, nil)
				return result.Result
			}
			catalog := execute(ctx, "catalog", "list_datasets", library, map[string]any{}, nil)
			require.True(t, catalog.Success, catalog.Error)
			require.Empty(t, e.evidenceCompletion.nativeSelections)
			var outsideRef string
			for ref, item := range e.evidenceCompletion.refs {
				if item.DatasetID == "unrelated" {
					outsideRef = ref
				}
			}
			require.NotEmpty(t, outsideRef)
			query := func(id string) map[string]any { return map[string]any{"dataset_id": id, "series_id": "runtime"} }
			forged := map[string]any{"goal_index": 0, "role": "primary", "selection_ref": outsideRef}
			rejected := execute(ctx, "forged", "query_series", library, query("unrelated"), forged)
			require.Equal(t, coverageScopeRejection, rejected.Error)
			require.Empty(t, e.evidenceCompletion.record.Bindings)
			require.Equal(t, []string{"list_datasets"}, sent)
			fields := execute(ctx, "selected-field", "list_series", library, map[string]any{"dataset_id": dataset}, nil)
			require.True(t, fields.Success, fields.Error)
			require.Len(t, e.evidenceCompletion.nativeSelections[0], 1)
			read := execute(ctx, "first-read", "query_series", library, query(dataset), forged)
			require.True(t, read.Success, read.Error, "unverifiable optional metadata is discarded")
			record := e.evidenceCompletion.record
			require.True(t, slices.ContainsFunc(record.Evidence, func(item coverageEvidence) bool {
				return item.Kind == "cell" && item.SourceID == dataset && item.Raw == json.Number("13") && coverageCurrentEvidence(record, item)
			}))
			crossLibrary := execute(ctx, "wrong-library", "query_series", otherLibrary, query(dataset), nil)
			require.Equal(t, coverageScopeRejection, crossLibrary.Error)
			conflict := execute(ctx, "conflicting-table", "read_pdf_table", library, map[string]any{"dataset_id": dataset, "table_id": "unrelated"}, nil)
			require.Equal(t, coverageScopeRejection, conflict.Error)
			revokedCtx := types.WithTenantAPIKeyScope(ctx, types.TenantAPIKeyScope{KeyID: 1, KnowledgeBaseIDs: types.StringArray{otherLibrary}})
			revoked := execute(revokedCtx, "revoked-read", "query_series", library, query(dataset), nil)
			require.Equal(t, coverageScopeRejection, revoked.Error)
			require.Equal(t, []string{"list_datasets", "list_series", "query_series"}, sent)
		})
	}
}
