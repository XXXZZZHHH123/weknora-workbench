package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// These are the archived Af4 failing and successful terminal turns and Af3
// successful controls. The directory and unused display payloads were reduced.
// SQL and series contracts, selected field IDs, source rows, and released
// drafts were kept. The follow-up response comes from the native workbook read.
func TestAf5StructuredPublicationReplay(t *testing.T) {
	raw, err := os.ReadFile("testdata/qa4/replay/af5-structured.json")
	require.NoError(t, err)
	var archived []json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &archived))
	require.Len(t, archived, 8)
	for _, original := range archived {
		var identity struct{ ID string }
		require.NoError(t, json.Unmarshal(original, &identity))
		for _, mode := range []string{"enabled", "permuted", "disabled", "disabled permuted", "revoked", "eight tool batches"} {
			needsOriginal := identity.ID == "Af4-3/KB-16"
			if (mode == "revoked" || mode == "eight tool batches") && !needsOriginal {
				continue
			}
			t.Run(identity.ID+"/"+mode, func(t *testing.T) {
				enabled := !strings.HasPrefix(mode, "disabled")
				setAf5StructuredReplayFlags(t, enabled)
				var trace af5StructuredTrace
				require.NoError(t, json.Unmarshal(original, &trace))
				if len(trace.PlanIssues) != 0 {
					trace.Targets[0].PlanIssues = trace.PlanIssues
				}
				if strings.HasSuffix(mode, "permuted") {
					permuteAf5StructuredTrace(trace.Steps)
				}
				library := af5ReplayLibrary(t, trace.Steps)
				targets := types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: library, TenantID: 1}}
				registry := qa4ReplayRegistry(trace.Steps...)
				var originalReads int
				if needsOriginal {
					registry = af5StructuredOriginalRegistry(t, targets, &originalReads)
				}
				engine, state, model, ctx, messages := prepareAf5StructuredTurn(t, trace, targets, registry, mode == "revoked")
				candidate, failure := trace.Final, terminalFailure(trace.Failure)
				if failure == terminalIncomplete {
					candidate = ""
				}
				answer, completion := publishAf5StructuredTurn(t, ctx, engine, state, func() {
					if mode == "eight tool batches" {
						require.Equal(t, "tool_rounds", engine.standardAnswerBudgetReason(ctx, state, agentExecutionStart(ctx)))
						const response = "棉花数据库.xlsx 的「郑棉期货」工作表在 2024-03-01 – 2024-03-31 没有记录。CF.CZC 收盘价的最后一笔有效记录为 2023-09-22 的 17110（B4705）。"
						budgetModel := &mockChat{responses: []mockResponse{
							chatAnswer(response), discoveryReply("keep", ""), chatAnswer(response), chatAnswer(response),
						}}
						engine.chatModel = budgetModel
						finished, err := engine.finishStandardAnswerBudget(ctx, state, trace.Question, "recorded", "assistant", &messages, &loopGuards{})
						require.NoError(t, err)
						require.True(t, finished)
						require.NotEmpty(t, budgetModel.calls)
						require.True(t, slices.ContainsFunc(budgetModel.calls[0], func(message chat.Message) bool {
							return message.Role == "tool" && message.Name == "structured_inspect_source" &&
								strings.Contains(message.Content, "17110") && strings.Contains(message.Content, "B4705")
						}), "the first synthesis receives the original value as a native tool result")
						for _, options := range budgetModel.opts {
							require.Empty(t, options.Tools, "the existing synthesis and review cannot spend a second read slot")
							require.Equal(t, "none", options.ToolChoice)
						}
						return
					}
					engine.commitTerminalResult(ctx, state, "recorded", candidate, "", "answer", false, failure)
				})
				require.Zero(t, model.callCount)
				if mode == "revoked" {
					require.Zero(t, originalReads)
					require.NotContains(t, answer, "17110")
					require.NotContains(t, answer, "17285")
					return
				}
				if !enabled && failure == terminalIncomplete {
					require.Zero(t, originalReads)
					require.Empty(t, answer)
					require.NotNil(t, completion.Incomplete)
					require.Equal(t, trace.Origin, engine.turnReview.TerminalReceipt.notice)
					return
				}
				if needsOriginal {
					require.Equal(t, 1, originalReads)
					require.True(t, engine.turnReview.FinalCoverageReadAttempted)
					call := state.RoundSteps[len(state.RoundSteps)-1].ToolCalls[0]
					require.Equal(t, "structured_inspect_source", call.Name)
					require.True(t, call.Result.Success, call.Result.Error)
					require.Regexp(t, `(?:没有|无)[^\n]*(?:记录|观测)`, answer)
				} else {
					require.Len(t, state.RoundSteps, len(trace.Steps), "already-read values require no extra tool calls")
				}
				require.NotEmpty(t, answer)
				if failure == terminalIncomplete {
					require.Nil(t, completion.Incomplete)
					require.NotContains(t, answer, "已生成的草稿未覆盖")
					require.NotContains(t, answer, "尚缺")
				} else {
					require.Equal(t, trace.Final, answer, "keep the proven historical answer and its source qualifications")
				}
				require.Contains(t, answer, "棉花数据库.xlsx")
				switch {
				case strings.HasSuffix(trace.ID, "/KB-16"):
					require.Contains(t, answer, "17110")
					require.Contains(t, answer, "B4705")
					require.NotContains(t, answer, "17285")
					require.Regexp(t, `2023(?:\s*年\s*9\s*月\s*22\s*日|-09-22)`, answer)
				case strings.HasSuffix(trace.ID, "/KB-22"):
					require.Regexp(t, `(?:17790[^\n]*2023(?:\s*年\s*9\s*月\s*1\s*日|-09-01)|2023-09-01[^\n]*17790)`, answer)
					require.Regexp(t, `(?:13830[^\n]*2023(?:\s*年\s*3\s*月\s*16\s*日|-03-16)|2023-03-16[^\n]*13830)`, answer)
					require.Contains(t, answer, "B4690")
					require.Contains(t, answer, "B4575")
					if failure == terminalIncomplete {
						require.Contains(t, answer, "2023-01-03 – 2023-09-22")
						require.Contains(t, answer, "CF.CZC")
					}
				case strings.HasSuffix(trace.ID, "/KB-07"):
					for _, value := range []string{"231.93", "110", "298.93", "B87", "C87", "G87", "万吨"} {
						require.Contains(t, answer, value)
					}
					require.NotContains(t, answer, "O87", "an exploratory field from the same SQL read is outside the requested measures")
				}
			})
		}
	}
}

type af5StructuredTrace struct {
	ID, Question, Final, Failure, Origin string
	Targets                              []taskCoverageTarget
	PlanIssues                           []string
	Steps                                []types.AgentStep
}

func setAf5StructuredReplayFlags(t *testing.T, enabled bool) {
	t.Helper()
	t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "true")
	t.Setenv("WEKNORA_AGENT_RELAXED_TERMINAL_EVIDENCE", "true")
	t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "false")
	t.Setenv("WEKNORA_AGENT_STRUCTURED_TASK_PLAN", "true")
	t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
	t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", "true")
	t.Setenv("STRUCTURED_DATA_CATALOG_MANIFEST_REUSE", "false")
	t.Setenv("STRUCTURED_DATA_URL", "")
	t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", "")
	setting := "false"
	if enabled {
		setting = "true"
	}
	t.Setenv("WEKNORA_AGENT_FINAL_READ_ANSWER", setting)
}

func af5ReplayLibrary(t *testing.T, steps []types.AgentStep) string {
	t.Helper()
	for _, step := range steps {
		for _, call := range step.ToolCalls {
			if call.Result != nil {
				if library := sourceNavigationString(coverageObject(call.Result.Data["source_context"]), "knowledge_base_id"); library != "" {
					return library
				}
			}
		}
	}
	t.Fatal("archived turn has no native source context")
	return ""
}

func permuteAf5StructuredTrace(steps []types.AgentStep) {
	for i := range steps {
		slices.Reverse(steps[i].ToolCalls)
		for _, call := range steps[i].ToolCalls {
			if call.Result == nil || call.Result.Data["result_contract"] != nil {
				continue
			}
			for _, key := range []string{"series", "field_candidates", "datasets"} {
				if values, ok := call.Result.Data[key].([]any); ok {
					slices.Reverse(values)
				}
			}
		}
	}
}

func prepareAf5StructuredTurn(t *testing.T, trace af5StructuredTrace, targets types.SearchTargets, registry *agenttools.ToolRegistry, revoke bool) (*AgentEngine, *types.AgentState, *qa4Stage2CModel, context.Context, []chat.Message) {
	t.Helper()
	model := &qa4Stage2CModel{t: t}
	engine := newTestEngine(t, model, withCitationsEnabled(true))
	engine.config.WorkbenchDefault, engine.config.EvidenceReferencesEnabled = true, true
	engine.config.AnswerReviewMode, engine.config.MaxIterations = "standard", 16
	engine.config.SearchTargets = targets
	engine.toolRegistry = registry
	engine.syncStructuredToolSourceContracts()
	if len(trace.Targets) != 0 {
		goals := make([]string, len(trace.Targets))
		for i := range goals {
			goals[i] = trace.Question
		}
		engine.taskPlan = &taskEvidencePlan{Goals: goals, CoverageTargets: trace.Targets, DeliveryRequirements: []taskDeliveryRequirement{{Kind: "text", Format: "prose"}}}
	}
	messages := []chat.Message{{Role: "user", Content: trace.Question}}
	engine.turnReview.Query = trace.Question
	engine.turnReview.Conversation = conversationThroughLastHuman(messages)
	engine.turnReview.TaskConversation = engine.turnReview.Conversation
	engine.turnReview.TaskConversationCaptured, engine.turnReview.DeferTerminalClose = true, true
	state := &types.AgentState{RoundSteps: trace.Steps}
	for _, step := range trace.Steps {
		state.CurrentRound = max(state.CurrentRound, step.Iteration)
		messages = engine.appendToolResults(messages, step)
	}
	ctx := withAgentExecutionStart(types.WithCaller(t.Context(), types.Caller{TenantID: 1, UserID: "replay", Role: types.TenantRoleViewer}))
	ctx = context.WithValue(ctx, types.LanguageContextKey, "zh-CN")
	ctx = context.WithValue(ctx, standardAnswerFinalDeadlineKey{}, time.Now().Add(30*time.Second))
	engine.refreshEvidenceCompletion(ctx, trace.Question, state, messages)
	if revoke {
		ctx = types.WithTenantAPIKeyScope(ctx, types.TenantAPIKeyScope{KeyID: 1, ScopeType: types.APIKeyScopeTenant,
			KnowledgeBaseIDs: types.StringArray{"another-library"}, Capabilities: types.StringArray{"chat", "retrieve"}})
	}
	return engine, state, model, ctx, messages
}

func publishAf5StructuredTurn(t *testing.T, ctx context.Context, engine *AgentEngine, state *types.AgentState, publish func()) (string, event.AgentCompleteData) {
	t.Helper()
	var visible string
	var completion []event.AgentCompleteData
	engine.eventBus.On(event.EventAgentFinalAnswer, func(_ context.Context, item event.Event) error {
		answer := item.Data.(event.AgentFinalAnswerData)
		require.NotContains(t, answer.Content, "<evidence")
		require.NotContains(t, answer.Content, "<kb")
		if answer.Replace {
			visible = ""
		}
		visible += answer.Content
		return nil
	})
	engine.eventBus.On(event.EventAgentComplete, func(_ context.Context, item event.Event) error {
		completion = append(completion, item.Data.(event.AgentCompleteData))
		return nil
	})
	publish()
	engine.emitCompletionEvent(ctx, state, "recorded", "assistant", time.Now())
	require.True(t, state.IsComplete)
	require.Len(t, completion, 1)
	require.Equal(t, state.FinalAnswer, completion[0].FinalAnswer)
	if state.FinalAnswer != "" {
		require.Equal(t, state.FinalAnswer, visible)
	}
	return state.FinalAnswer, completion[0]
}

type af5StructuredTransport func(*http.Request) (*http.Response, error)

func (f af5StructuredTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func af5StructuredOriginalRegistry(t *testing.T, targets types.SearchTargets, reads *int) *agenttools.ToolRegistry {
	t.Helper()
	var workbook struct {
		Arguments map[string]any
		Result    json.RawMessage
	}
	raw, err := os.ReadFile("testdata/qa4/replay/af5-structured-original.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &workbook))
	key := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(key, []byte(strings.Repeat("k", 32)), 0600))
	t.Setenv("STRUCTURED_DATA_URL", "http://af5-structured-replay.invalid")
	t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", key)
	registry := agenttools.NewToolRegistry()
	base := agenttools.NewStructuredDataTool().WithSearchTargets(targets)
	registry.RegisterTool(base)
	registry.PrepareStructuredProtocol()
	manifest := structuredCodecManifest()
	for _, entry := range sourceNavigationRows(manifest["tools"]) {
		if entry["name"] == "inspect_source" {
			coverageObject(entry["inputSchema"])["properties"] = map[string]any{
				"dataset_id": map[string]any{"type": "string"}, "sheet": map[string]any{"type": "string"},
				"ranges": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "max_cells": map[string]any{"type": "integer"},
			}
		}
	}
	for _, tool := range agenttools.NewStructuredReadOperationTools(base, manifest) {
		registry.RegisterTool(tool)
	}
	previous := http.DefaultTransport
	http.DefaultTransport = af5StructuredTransport(func(request *http.Request) (*http.Response, error) {
		*reads += 1
		var input struct {
			Operation string
			Arguments map[string]any
		}
		require.Equal(t, "/api/tools/read", request.URL.Path)
		require.NoError(t, json.NewDecoder(request.Body).Decode(&input))
		require.Equal(t, "inspect_source", input.Operation)
		require.Equal(t, workbook.Arguments, input.Arguments)
		encoded, err := base64.RawURLEncoding.DecodeString(request.Header.Get("X-Research-Identity"))
		require.NoError(t, err)
		var identity map[string]any
		require.NoError(t, json.Unmarshal(encoded, &identity))
		require.Equal(t, targets[0].KnowledgeBaseID, identity["knowledge_base_id"])
		header := http.Header{}
		header.Set("X-Structured-Manifest-SHA256", request.Header.Get("X-Structured-Manifest-SHA256"))
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(string(workbook.Result)))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	return registry
}

// The executed dataset, field and row identify an exact-date observation
// independently of optional snapshot and last-valid metadata.
func TestAf5StructuredPublicationDirectRows(t *testing.T) {
	for _, scenario := range []struct {
		name, sourceHash, replyField, replyDataset, cell string
		feature, revoked, changed, want                  bool
	}{
		{name: "missing optional metadata", replyField: "flow", replyDataset: "measurements", cell: "B17", feature: true, want: true},
		{name: "original without cell address", replyField: "flow", replyDataset: "measurements", feature: true, want: true},
		{name: "optional endpoint unverified", sourceHash: strings.Repeat("a", 64), replyField: "flow", replyDataset: "measurements", cell: "B17", feature: true, want: true},
		{name: "disabled", replyField: "flow", replyDataset: "measurements", cell: "B17"},
		{name: "revoked", replyField: "flow", replyDataset: "measurements", cell: "B17", feature: true, revoked: true},
		{name: "new source snapshot", sourceHash: strings.Repeat("a", 64), replyField: "flow", replyDataset: "measurements", cell: "B17", feature: true, changed: true},
		{name: "different selected dataset", replyField: "flow", replyDataset: "unselected", cell: "B17", feature: true},
		{name: "different selected field", replyField: "temperature", replyDataset: "measurements", cell: "B17", feature: true},
		{name: "neighboring field", replyField: "flow", replyDataset: "measurements", cell: "C17", feature: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			setAf5StructuredReplayFlags(t, scenario.feature)
			registry := qa4ReplayRegistry()
			descriptor, _, ok := registry.StructuredInvocation("structured_query_series", json.RawMessage(`{}`))
			require.True(t, ok)
			reading := func(id, date, value, sourceHash string) types.ToolCall {
				return types.ToolCall{ID: id, Name: "structured_query_series", Args: map[string]any{
					"knowledge_base_id": "station-library", "dataset_id": "measurements", "series_id": "flow",
					"operation": "latest", "start_date": date, "end_date": date,
				}, Result: &types.ToolResult{Success: true, Data: map[string]any{
					"kind": "structured_series_query", "status": "ok", "dataset_id": scenario.replyDataset,
					"_structured_execution": descriptor, "source_context": map[string]any{"knowledge_base_id": "station-library"},
					"source":   map[string]any{"file": "Station logs.xlsx", "sha256": sourceHash, "knowledge_base_id": "station-library"},
					"series":   map[string]any{"series_id": scenario.replyField, "metric": "Average flow", "sheet": "Station readings", "column": "B"},
					"filters":  map[string]any{"series_id": scenario.replyField, "operation": "latest", "start_date": date, "end_date": date, "value_filters": []any{}},
					"rows":     []any{map[string]any{"date": date, "value": json.Number(value), "value_status": "valid", "source_cell": scenario.cell}},
					"coverage": map[string]any{"last_valid": map[string]any{"status": "present"}},
				}}}
			}
			const date = "2022-05-06"
			const question = "What was the average flow on May 6, 2022?"
			trace := af5StructuredTrace{Question: question, Steps: []types.AgentStep{{Iteration: 1,
				ToolCalls: []types.ToolCall{reading("first", date, "47.25", scenario.sourceHash)}},
			}, Targets: []taskCoverageTarget{{GoalIndex: 0, RequestBasis: question, StructuredTaskGoal: &StructuredTaskGoal{
				Metric: "Average flow", SourceMode: "open", SourceTime: "observed_period", VersionTask: "single",
				Period: map[string]any{"from": date, "to": date, "grain": "day"}, Required: []string{"scoped_value"}},
			}}}
			if scenario.changed {
				trace.Steps = append(trace.Steps, types.AgentStep{Iteration: 2, ToolCalls: []types.ToolCall{reading("reloaded", "2022-05-07", "84.25", strings.Repeat("b", 64))}})
			}
			targets := types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "station-library", TenantID: 1}}
			engine, state, model, ctx, _ := prepareAf5StructuredTurn(t, trace, targets, registry, scenario.revoked)
			answer, _ := publishAf5StructuredTurn(t, ctx, engine, state, func() {
				engine.commitTerminalResult(ctx, state, "recorded", "", "", "answer", false, terminalIncomplete)
			})
			require.Zero(t, model.callCount)
			if scenario.want {
				for _, fact := range []string{"47.25", "2022-05-06", "Station logs.xlsx", "Average flow"} {
					require.Contains(t, answer, fact)
				}
			} else {
				require.NotContains(t, answer, "47.25")
			}
			require.NotContains(t, answer, "84.25")
		})
	}
}
