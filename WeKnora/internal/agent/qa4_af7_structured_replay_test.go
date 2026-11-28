package agent

import (
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/structuredresult"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type af7StructuredArchive struct {
	Interrupted, Successful af5StructuredTrace
	Reconstructed           []qa4Stage2Call
}

// The directory prefix and KB-07 steps are projections of Af6-1 SQLite
// AgentSteps, checked against SSE call IDs and returned data. Reconstructed
// KB-22 reads were run offline with DataStore.query_series on the same native
// dataset, after verify_selection accepted the complete catalog candidate group.
func loadAf7StructuredArchive(t *testing.T) af7StructuredArchive {
	t.Helper()
	file, err := os.Open("testdata/qa4/replay/qa4_af7_structured.json.gz")
	require.NoError(t, err)
	defer file.Close()
	reader, err := gzip.NewReader(file)
	require.NoError(t, err)
	defer reader.Close()
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	var archive af7StructuredArchive
	require.NoError(t, decoder.Decode(&archive))
	return archive
}

func setAf7StructuredReplayFlags(t *testing.T, substantive bool) {
	t.Helper()
	setAf5StructuredReplayFlags(t, true)
	t.Setenv("WEKNORA_AGENT_SCOPED_READ_RECOVERY", "true")
	t.Setenv("WEKNORA_AGENT_GAP_KNOWLEDGE_RECALL", "false")
	t.Setenv("WEKNORA_AGENT_SUBSTANTIVE_FINAL_ANSWER", fmt.Sprint(substantive))
}

func TestQA4Af7BudgetInterruptedSeriesReplay(t *testing.T) {
	for _, variant := range []struct {
		name                          string
		moreReads, secondTool, legacy bool
		change                        string
	}{
		{name: "native extrema", moreReads: true, secondTool: true},
		{name: "native extrema with a missing value in the draft", moreReads: true, secondTool: true, change: "missing draft value"},
		{name: "Af6 rollback", legacy: true},
		{name: "failed directory", change: "failed directory"},
		{name: "empty directory", change: "empty directory"},
		{name: "eight tool rounds", change: "eight tool rounds"},
		{name: "directory spends the eighth tool round", change: "seven tool rounds"},
		{name: "directed slot spent", change: "directed slot spent"},
		{name: "last iteration stays prose", change: "last iteration"},
		{name: "too little final time", change: "low remaining time"},
		{name: "revoked library", change: "revoked library"},
		{name: "cannot repeat the directory", secondTool: true, change: "repeat directory"},
		{name: "cannot select a field outside this directory", secondTool: true, change: "outside field"},
		{name: "cannot exceed shared tool limit", secondTool: true, change: "four queries"},
		{name: "cannot reuse a previously executed ID", secondTool: true, change: "repeat ID"},
	} {
		t.Run(variant.name, func(t *testing.T) {
			setAf7StructuredReplayFlags(t, !variant.legacy)
			archive := loadAf7StructuredArchive(t)
			trace := archive.Interrupted
			require.Equal(t, "Af6-1/KB-22", trace.ID)
			require.Contains(t, trace.Final, "这些结果未给出可对应所问范围的读数")
			require.Len(t, trace.Steps, 2)
			require.Len(t, trace.Steps[0].ToolCalls, 2)
			require.Len(t, trace.Steps[1].ToolCalls, 1)
			catalog := trace.Steps[1].ToolCalls[0]
			require.Equal(t, "structured_list_series", catalog.Name)
			trace.Steps = trace.Steps[:1]
			library := af5ReplayLibrary(t, trace.Steps)
			targets := types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: library, TenantID: 1}}
			var wireReads []string
			registry := af7StructuredOfflineRegistry(t, targets, catalog, archive.Reconstructed, variant.change, &wireReads)
			knowledge := &sourceTimeDocumentStore{byID: map[string]*types.Knowledge{}}
			registry.RegisterTool(agenttools.NewSearchKnowledgeTool(nil, knowledge, nil, targets, nil, nil))
			registry.RegisterTool(agenttools.NewReadDocumentTool(knowledge, nil, targets))
			agenttools.PrepareDocumentScope(registry, trace.Question)
			engine, state, _, ctx, messages := prepareAf5StructuredTurn(t, trace, targets, registry, variant.change == "revoked library")
			var knowledgeCalls atomic.Int32
			engine.eventBus.On(event.EventAgentToolCall, func(_ context.Context, notice event.Event) error {
				switch notice.Data.(event.AgentToolCallData).ToolName {
				case agenttools.ToolSearchKnowledge, agenttools.ToolBindDocumentScope, agenttools.ToolReadDocument:
					knowledgeCalls.Add(1)
				}
				return nil
			})
			// The archived planner selected structured discovery. Its compacted
			// log preserves the read objective but elides the complete plan.
			engine.taskPlan = &taskEvidencePlan{
				Goals: []string{trace.Question}, Evidence: []string{"Query the selected closing price series and verify observation dates."},
				SourceDiscovery:      &taskSourceDiscovery{SourceDomains: []string{"structured"}},
				DeliveryRequirements: []taskDeliveryRequirement{{Kind: "text", Format: "prose"}},
			}
			engine.reviewConversation, engine.reviewConversationReady = conversationThroughLastHuman(messages), true
			engine.evidenceCompletion = newEvidenceCompletionTurn(0)
			engine.stopEvidenceCompletion(ctx, trace.Question, state, messages, "model_call_time")
			if variant.change == "last iteration" {
				engine.config.MaxIterations = 2
			}
			if variant.change == "low remaining time" {
				ctx = context.WithValue(ctx, standardAnswerFinalDeadlineKey{}, time.Now().Add(finalCoverageReadMinRemaining-time.Second))
			}
			if variant.change == "directed slot spent" {
				engine.turnReview.FinalCoverageReadAttempted = true
			}
			if variant.change == "seven tool rounds" || variant.change == "eight tool rounds" {
				rounds := standardAnswerToolRounds
				if variant.change == "seven tool rounds" {
					rounds--
				}
				for round := range rounds {
					receipt := trace.Steps[0].ToolCalls[1]
					receipt.ID = fmt.Sprintf("read-%d", round)
					state.RoundSteps = append(state.RoundSteps, types.AgentStep{Iteration: round, ToolCalls: []types.ToolCall{receipt}})
				}
				state.CurrentRound = rounds - 1
			}
			priorTools := countTotalToolCalls(state.RoundSteps)
			catalogArgs := maps.Clone(catalog.Args)
			catalogArgs["knowledge_base_id"] = engine.modelContext.RegisterKnowledgeBase(library)
			list := af7StructuredModelCall(t, catalog.ID, catalog.Name, catalogArgs)
			var queries []types.LLMToolCall
			for i, recorded := range archive.Reconstructed {
				args := maps.Clone(recorded.Arguments)
				args["knowledge_base_id"] = catalogArgs["knowledge_base_id"]
				if variant.change == "outside field" {
					args["series_id"] = "missing-from-this-directory"
				}
				id := fmt.Sprintf("reconstructed-extremum-%d", i)
				if variant.change == "repeat ID" {
					id = catalog.ID
				}
				queries = append(queries, af7StructuredModelCall(t, id, "structured_query_series", args))
			}
			if variant.change == "repeat directory" {
				listAgain := list
				listAgain.ID = "new-catalog-request"
				queries = []types.LLMToolCall{listAgain}
			}
			if variant.change == "four queries" {
				for _, query := range slices.Clone(queries) {
					query.ID += "-extra"
					queries = append(queries, query)
				}
			}
			wantCatalog := !slices.Contains([]string{"directed slot spent", "eight tool rounds", "low remaining time", "revoked library"}, variant.change)
			var responses []mockResponse
			if wantCatalog {
				responses = append(responses, af7StructuredToolReply(list))
			}
			if variant.secondTool {
				responses = append(responses, af7StructuredToolReply(queries...))
			}
			final := trace.Final
			if variant.moreReads {
				final = "棉花数据库.xlsx「郑棉期货」CF.CZC 收盘价，最高 17790（2023-09-01，B4690），最低 13830（2023-03-16，B4575）。实际有效数据覆盖 2023-01-03 至 2023-09-22。"
			}
			if variant.change == "missing draft value" {
				final = "棉花数据库.xlsx「郑棉期货」CF.CZC 收盘价，最高 17790（2023-09-01，B4690）。"
			}
			responses = append(responses, chatAnswer(final))
			model := &qa4Stage2Model{mockChat: mockChat{responses: responses}, t: t}
			engine.chatModel = model
			answer, completion := publishAf5StructuredTurn(t, ctx, engine, state, func() {
				require.NoError(t, engine.streamFinalAnswerToEventBus(ctx, trace.Question, state, "recorded", messages, finalAnswerOptions{preliminary: true}))
			})
			require.Equal(t, len(responses), model.callCount)
			require.LessOrEqual(t, model.callCount, 3)
			require.Empty(t, model.opts[model.callCount-1].Tools)
			require.Zero(t, knowledgeCalls.Load())
			if wantCatalog {
				firstTools := model.opts[0].Tools
				require.True(t, slices.ContainsFunc(firstTools, func(tool chat.Tool) bool { return tool.Function.Name == catalog.Name }))
				require.NotEmpty(t, wireReads)
				require.Equal(t, "list_series", wireReads[0])
				priorTools++
			} else {
				require.Empty(t, wireReads)
			}
			if variant.secondTool {
				require.Len(t, model.opts[1].Tools, 1)
				require.Equal(t, "structured_query_series", model.opts[1].Tools[0].Function.Name)
				require.True(t, slices.ContainsFunc(model.calls[1], func(message chat.Message) bool {
					return message.Role == "tool" && message.ToolCallID == catalog.ID && strings.Contains(message.Content, "futures.close.CF.CZC")
				}))
			}
			if variant.moreReads {
				require.Len(t, wireReads, 3)
				require.ElementsMatch(t, []string{"query_series/min", "query_series/max"}, wireReads[1:])
				priorTools += 2
				queries := state.RoundSteps[len(state.RoundSteps)-1].ToolCalls
				require.Len(t, queries, 2)
				for _, query := range queries {
					require.Equal(t, "structured_query_series", query.Name)
					require.True(t, query.Result.Success, query.Result.Error)
				}
				require.Nil(t, completion.Incomplete)
				for _, expected := range []string{"17790", "2023-09-01", "13830", "2023-03-16", "B4690", "B4575", "棉花数据库.xlsx", "郑棉期货"} {
					require.Contains(t, answer, expected)
				}
			} else {
				if wantCatalog {
					require.Len(t, wireReads, 1, "only the directory read reaches the transport")
				}
				require.NotContains(t, answer, "17790")
				require.NotContains(t, answer, "13830")
				if variant.legacy {
					require.Equal(t, trace.Final, answer)
				}
			}
			require.Equal(t, priorTools, countTotalToolCalls(state.RoundSteps))
		})
	}
}

func af7StructuredModelCall(t *testing.T, id, name string, args map[string]any) types.LLMToolCall {
	t.Helper()
	body, err := json.Marshal(args)
	require.NoError(t, err)
	return types.LLMToolCall{ID: id, Type: "function", Function: types.FunctionCall{Name: name, Arguments: string(body)}}
}

func af7StructuredToolReply(calls ...types.LLMToolCall) mockResponse {
	return mockResponse{chunks: []types.StreamResponse{{ResponseType: types.ResponseTypeAnswer, ToolCalls: calls, Done: true, FinishReason: "tool_calls"}}}
}

func af7StructuredOfflineRegistry(t *testing.T, targets types.SearchTargets, catalog types.ToolCall, reconstructed []qa4Stage2Call, variant string, reads *[]string) *agenttools.ToolRegistry {
	t.Helper()
	const destination = "http://af7-structured-replay.invalid"
	t.Setenv("STRUCTURED_DATA_URL", destination)
	key := filepath.Join(t.TempDir(), "signing-key")
	require.NoError(t, os.WriteFile(key, []byte(strings.Repeat("a", 32)), 0600))
	t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", key)
	registry := agenttools.NewToolRegistry()
	registry.RegisterTool(agenttools.NewStructuredDataTool().WithSearchTargets(targets))
	registry.PrepareStructuredProtocol()
	manifest := structuredCodecManifest()
	for _, tool := range sourceNavigationRows(manifest["tools"]) {
		properties := coverageObject(coverageObject(tool["inputSchema"])["properties"])
		switch tool["name"] {
		case "list_series":
			for _, field := range []string{"dataset_id", "sheet"} {
				properties[field] = map[string]any{"type": "string"}
			}
			properties["limit"] = map[string]any{"type": "integer"}
		case "query_series":
			for _, field := range []string{"dataset_id", "series_id", "start_date", "end_date", "operation", "source_question"} {
				properties[field] = map[string]any{"type": "string"}
			}
			properties["limit"] = map[string]any{"type": "integer"}
			properties["source_binding"] = map[string]any{"type": "array", "items": map[string]any{"type": "object"}}
		}
	}
	result, err := registry.InstallStructuredManifest(manifest)
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	catalogData := maps.Clone(catalog.Result.Data)
	delete(catalogData, "_structured_execution")
	if variant == "empty directory" {
		catalogData["series"] = []any{}
		catalogData["total"] = 0
	}
	encodedCatalog, err := json.Marshal(catalogData)
	require.NoError(t, err)
	for _, recorded := range reconstructed {
		var data map[string]any
		decoder := json.NewDecoder(strings.NewReader(string(recorded.Response)))
		decoder.UseNumber()
		require.NoError(t, decoder.Decode(&data))
		contract, err := structuredresult.ValidatePayload(data)
		require.NoError(t, err)
		require.NotNil(t, contract)
		require.Equal(t, catalog.Args["dataset_id"], contract.SourceSnapshot.DatasetID)
		require.Equal(t, targets[0].KnowledgeBaseID, contract.SourceSnapshot.KnowledgeBaseID)
		require.True(t, slices.ContainsFunc(sourceNavigationRows(catalog.Result.Data["series"]), func(field map[string]any) bool {
			return field["series_id"] == recorded.Arguments["series_id"] && coverageObject(field["source_context"])["source_sha256"] == contract.SourceSnapshot.SourceSHA256
		}))
	}
	previous := http.DefaultTransport
	var seenMu sync.Mutex
	http.DefaultTransport = af5StructuredTransport(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, destination+"/api/tools/read", request.URL.String())
		assert.NotEmpty(t, request.Header.Get("X-Research-Signature"))
		encoded, err := base64.RawURLEncoding.DecodeString(request.Header.Get("X-Research-Identity"))
		if err != nil {
			return nil, err
		}
		var identity map[string]any
		if err := json.Unmarshal(encoded, &identity); err != nil {
			return nil, err
		}
		assert.Equal(t, targets[0].KnowledgeBaseID, identity["knowledge_base_id"])
		var call struct {
			Operation string
			Arguments map[string]any
		}
		decoder := json.NewDecoder(request.Body)
		decoder.UseNumber()
		if err := decoder.Decode(&call); err != nil {
			return nil, err
		}
		var body []byte
		operation := call.Operation
		switch call.Operation {
		case "list_series":
			body = encodedCatalog
			assert.Equal(t, catalog.Args["dataset_id"], call.Arguments["dataset_id"])
			assert.Equal(t, catalog.Args["sheet"], call.Arguments["sheet"])
		case "query_series":
			operation += "/" + sourceNavigationString(call.Arguments, "operation")
			for _, recorded := range reconstructed {
				if recorded.Arguments["operation"] != call.Arguments["operation"] {
					continue
				}
				for _, key := range []string{"dataset_id", "series_id", "start_date", "end_date", "limit", "source_binding"} {
					assert.Equal(t, recorded.Arguments[key], call.Arguments[key], key)
				}
				body = recorded.Response
			}
		}
		seenMu.Lock()
		*reads = append(*reads, operation)
		seenMu.Unlock()
		if body == nil || call.Operation == "list_series" && variant == "failed directory" {
			return nil, fmt.Errorf("offline read unavailable: %s", operation)
		}
		header := http.Header{}
		header.Set("X-Structured-Manifest-SHA256", request.Header.Get("X-Structured-Manifest-SHA256"))
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	return registry
}

func TestQA4Af7PreservesSuccessfulStructuredAnswers(t *testing.T) {
	for _, substantive := range []bool{false, true} {
		t.Run(fmt.Sprintf("substantive=%t", substantive), func(t *testing.T) {
			setAf7StructuredReplayFlags(t, substantive)
			t.Run("Af6-1/KB-07", func(t *testing.T) {
				trace := loadAf7StructuredArchive(t).Successful
				require.Equal(t, "Af6-1/KB-07", trace.ID)
				library := af5ReplayLibrary(t, trace.Steps)
				targets := types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: library, TenantID: 1}}
				registry := qa4ReplayRegistry(trace.Steps...)
				engine, state, model, ctx, _ := prepareAf5StructuredTurn(t, trace, targets, registry, false)
				answer, completion := publishAf5StructuredTurn(t, ctx, engine, state, func() {
					engine.commitTerminalResult(ctx, state, "recorded", trace.Final, "", "answer", false, terminalNoFailure)
				})
				require.Equal(t, trace.Final, answer)
				require.Nil(t, completion.Incomplete)
				require.Zero(t, model.callCount)
				require.Len(t, state.RoundSteps, len(trace.Steps))
				for _, expected := range []string{"231.93", "110", "298.93", "B87", "C87", "G87"} {
					require.Contains(t, answer, expected)
				}
			})
			t.Run("Af5-1/KB-07 and KB-22", func(t *testing.T) { verifyAf5StructuredPublicAnswers(t, substantive) })
		})
	}
}
