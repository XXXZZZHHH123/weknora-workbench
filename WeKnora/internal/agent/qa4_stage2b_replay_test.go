package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/structuredresult"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

const qa4Stage2Library = "replay-library"

// Projected from the qa4-s2-1 SSE results by one-based event ordinal.
// Blocked calls have null SSE arguments. SQLite agent_steps preserves their
// decoded business arguments and KB-31's two drafts. Library/dataset/document
// IDs are aliases and typed v2 hashes were recalculated after applying them.
type qa4Stage2Trace struct {
	Question        string
	AnswerEvent     int    `json:"answer_event"`
	CompleteEvent   int    `json:"complete_event"`
	HistoricalFinal string `json:"historical_final"`
	Drafts          []string
	Calls           []qa4Stage2Call
}

type qa4Stage2Call struct {
	Event           int
	Iteration       int
	Tool            string
	Arguments       map[string]any
	HistoricalError string `json:"historical_error"`
	Response        json.RawMessage
}

func (c qa4Stage2Call) id() string { return "event-" + strconv.Itoa(c.Event) }

type qa4Stage2Transport func(*http.Request) (*http.Response, error)

func (f qa4Stage2Transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type qa4Stage2Model struct {
	mockChat
	t *testing.T
}

func (m *qa4Stage2Model) Chat(ctx context.Context, messages []chat.Message, opts *chat.ChatOptions) (*types.ChatResponse, error) {
	m.t.Error("unexpected non-streamed model call in historical replay")
	return m.mockChat.Chat(ctx, messages, opts)
}

func (m *qa4Stage2Model) ChatStream(ctx context.Context, messages []chat.Message, opts *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	if m.callCount >= len(m.responses) {
		purpose, _ := types.LLMCallMetadataFromContext(ctx)
		m.t.Errorf("unexpected model call in historical replay: %s", purpose)
	}
	return m.mockChat.ChatStream(ctx, messages, opts)
}

func TestQA4Stage2BHistoricalReplay(t *testing.T) {
	for _, tc := range []struct {
		name, file string
		enabled    bool
		rows       bool
	}{
		{"KB16_directory_only", "s2-1-kb16", true, false},
		{"KB16_authorized_reads", "s2-1-kb16", true, true},
		{"KB31_split_retry_and_incomplete_answer", "s2-1-kb31", true, true},
		{"evidence_completion_false", "s2-1-kb16", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var trace qa4Stage2Trace
			raw, err := os.ReadFile("testdata/qa4/replay/" + tc.file + ".json")
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(raw, &trace))
			runQA4Stage2BReplay(t, trace, tc.enabled, tc.rows)
		})
	}
}

func runQA4Stage2BReplay(t *testing.T, trace qa4Stage2Trace, enabled, rows bool) {
	t.Helper()
	const endpoint = "https://structured-replay.invalid"
	t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", strconv.FormatBool(enabled))
	t.Setenv("STRUCTURED_DATA_URL", endpoint)
	key := t.TempDir() + "/signing-key"
	require.NoError(t, os.WriteFile(key, []byte(strings.Repeat("a", 32)), 0600))
	t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", key)
	var catalog struct {
		Datasets        []map[string]any
		FieldCandidates []map[string]any `json:"field_candidates"`
	}
	require.NoError(t, json.Unmarshal(trace.Calls[0].Response, &catalog))
	prefetched := trace.Calls[0].Iteration < 0

	targets := types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: qa4Stage2Library, TenantID: 1}}
	registry := agenttools.NewToolRegistry()
	registry.RegisterTool(agenttools.NewStructuredDataTool().WithSearchTargets(targets))
	registry.PrepareStructuredProtocol()
	manifest := structuredCodecManifest()
	for _, raw := range manifest["tools"].([]any) {
		entry := raw.(map[string]any)
		properties := entry["inputSchema"].(map[string]any)["properties"].(map[string]any)
		for _, call := range trace.Calls {
			if call.Tool == "structured_"+entry["name"].(string) {
				for k := range call.Arguments {
					if k != "knowledge_base_id" {
						properties[k] = map[string]any{}
					}
				}
			}
		}
	}
	installed, err := registry.InstallStructuredManifest(manifest)
	require.NoError(t, err)
	require.True(t, installed.Success)

	var active qa4Stage2Call
	var requestEvents []int
	originalTransport := http.DefaultTransport
	// StructuredDataTool uses the default RoundTripper. This serial test
	// replaces it for both the authorized request and the transport assertions.
	http.DefaultTransport = qa4Stage2Transport(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, endpoint+"/api/tools/read", r.URL.String())
		require.Equal(t, http.MethodPost, r.Method)
		var sent struct {
			Operation string
			Arguments map[string]any
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&sent))
		require.Equal(t, "structured_"+sent.Operation, active.Tool)
		expected := make(map[string]any, len(active.Arguments))
		for k, v := range active.Arguments {
			if k != "knowledge_base_id" {
				expected[k] = v
			}
		}
		require.Equal(t, expected, sent.Arguments, "SSE #%d: only historical business arguments reach the service", active.Event)
		rawIdentity, err := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Research-Identity"))
		require.NoError(t, err)
		var principal map[string]any
		require.NoError(t, json.Unmarshal(rawIdentity, &principal))
		require.Equal(t, qa4Stage2Library, principal["knowledge_base_id"])
		require.Equal(t, float64(1), principal["knowledge_base_tenant_id"])
		require.Equal(t, "qa4-replay-reader", principal["user_id"])
		require.NotEmpty(t, r.Header.Get("X-Research-Signature"))
		requestEvents = append(requestEvents, active.Event)

		payload := active.Response
		if len(payload) == 0 {
			// The declaration was stripped before historical persistence, and
			// these calls never executed. All following responses are stubs.
			switch {
			case !rows:
				payload = json.RawMessage(`{"status":"error","error":"offline replay fixture has no row response"}`)
			case !prefetched:
				payload = json.RawMessage(`{"status":"ok","kind":"structured_source_inspection","ranges":[],"truncated":true}`)
			default:
				field := catalog.FieldCandidates[0]
				data := map[string]any{"dataset_id": active.Arguments["dataset_id"], "truncated": false}
				if sent.Operation == "list_series" {
					data["series"], data["total"] = []any{field}, 1
				} else {
					data["series"], data["status"], data["rows"], data["valid_count"] = field, "no_data", []any{}, 0
					data["source"] = map[string]any{
						"file": catalog.Datasets[0]["source_file"], "sha256": catalog.Datasets[0]["sha256"], "sheet": field["sheet"],
					}
					data["filters"] = map[string]any{
						"series_id": active.Arguments["series_id"], "start_date": active.Arguments["start_date"],
						"end_date": active.Arguments["end_date"], "operation": active.Arguments["operation"], "value_filters": []any{},
					}
				}
				payload, err = json.Marshal(data)
				require.NoError(t, err)
			}
		}
		header := http.Header{}
		header.Set("X-Structured-Manifest-SHA256", r.Header.Get("X-Structured-Manifest-SHA256"))
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(string(payload)))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = originalTransport })

	model := &qa4Stage2Model{t: t}
	engine := newTestEngine(t, model, withCitationsEnabled(true), withMaxIterations(20))
	engine.toolRegistry = registry
	engine.config.WorkbenchDefault, engine.config.TaskPlanningEnabled, engine.config.StructuredResultReviewEnabled = true, true, true
	engine.config.AnswerReviewMode, engine.config.SearchTargets = "standard", targets
	engine.syncStructuredToolSourceContracts()
	if prefetched {
		// KB-16 had a valid, two-goal plan. Only its summary is in the log.
		engine.taskPlan = &taskEvidencePlan{
			Goals:           []string{"核实所问范围的收盘价", "说明交易日、粒度、单位和出处"},
			SourceDiscovery: &taskSourceDiscovery{Mode: "discover", SourceDomains: []string{"structured"}},
			CoverageTargets: []taskCoverageTarget{
				{GoalIndex: 0, RequestBasis: trace.Question, StructuredTaskGoal: &StructuredTaskGoal{
					SourceMode: "named_first", PrimaryRef: "棉花数据库", SourceTime: "observed_period",
					Period:   map[string]any{"from": "2024-03-01", "to": "2024-03-31", "grain": "month"},
					Required: []string{"scoped_value"},
				}},
			},
		}
	}
	// KB-31 historically had no usable plan. Its source-progress review
	// requested continuation, so keep the real legacy recovery transition.
	ctx := withAgentExecutionStart(types.WithCaller(t.Context(), types.Caller{TenantID: 1, UserID: "qa4-replay-reader", Role: types.TenantRoleViewer}))
	state := &types.AgentState{}
	messages := []chat.Message{{Role: "system", Content: "Use authorized observations."}, {Role: "user", Content: trace.Question}}
	engine.reviewConversation, engine.reviewConversationReady = conversationThroughLastHuman(messages), true
	engine.turnReview.Query, engine.turnReview.Conversation = trace.Question, engine.reviewConversation
	var finals []event.Event
	engine.eventBus.On(event.EventAgentFinalAnswer, func(_ context.Context, item event.Event) error {
		finals = append(finals, item)
		return nil
	})
	const session = "stage2b-replay"
	var guards loopGuards
	modelCalls := 0
	for _, active = range trace.Calls {
		require.False(t, state.IsComplete, "before SSE #%d", active.Event)
		wireArgs := make(map[string]any, len(active.Arguments)+1)
		for k, v := range active.Arguments {
			wireArgs[k] = v
		}
		if _, scoped := wireArgs["knowledge_base_id"]; scoped {
			wireArgs["knowledge_base_id"] = engine.modelContext.RegisterKnowledgeBase(qa4Stage2Library)
		}
		if active.HistoricalError != "" {
			require.Equal(t, "task_coverage declaration did not match a visible source or a current human goal", active.HistoricalError)
			if enabled {
				// Original metadata was lost, so exercise a syntactically valid
				// declaration with a selection outside the issued evidence refs.
				wireArgs[coverageArgument] = map[string]any{"goal_index": 0, "role": "primary", "selection_ref": "unissued-source-ref"}
			}
		}
		wire, err := json.Marshal(wireArgs)
		require.NoError(t, err)
		state.CurrentRound = active.Iteration
		if active.Iteration < 0 {
			call := engine.runToolCall(ctx, types.LLMToolCall{ID: active.id(), Function: types.FunctionCall{
				Name: active.Tool, Arguments: string(wire),
			}}, active.Iteration, 0, 0, session, "turn")
			step := types.AgentStep{Iteration: active.Iteration, ToolCalls: []types.ToolCall{call}}
			state.RoundSteps = append(state.RoundSteps, step)
			messages = engine.appendToolResults(messages, step)
			engine.refreshEvidenceCompletion(ctx, trace.Question, state, messages)
		} else {
			model.responses = append(model.responses, chatToolCall(active.id(), active.Tool, string(wire)))
			modelCalls++
			if !prefetched && active.Event == 16 || prefetched && !enabled && active.Event == 12 {
				// One existing review. Only its action was available for this
				// projection, so the feedback text comes from the test.
				model.responses = append(model.responses, discoveryReply("recover", "Continue querying the authorized source to cover the remaining request."))
				modelCalls++
			}
			_, err = engine.runReActIteration(ctx, state, &messages, engine.buildToolsForLLM(), session, "turn", trace.Question, &guards)
			require.NoError(t, err)
		}
		step := state.RoundSteps[len(state.RoundSteps)-1]
		require.Len(t, step.ToolCalls, 1)
		call := step.ToolCalls[0]
		require.Equal(t, active.id(), call.ID)
		require.NotNil(t, call.Result)
		require.False(t, call.Result.PreflightRejected, "SSE #%d: %s", active.Event, call.Result.Error)
		descriptor, ok := call.Result.Data["_structured_execution"].(agenttools.StructuredOperationDescriptor)
		require.True(t, ok, "SSE #%d: native executor receipt absent: %s", active.Event, call.Result.Error)
		require.Equal(t, active.Tool, descriptor.ToolName)
		require.Equal(t, len(active.Response) != 0 && active.Event != 31 || active.HistoricalError != "" && rows, call.Result.Success, "SSE #%d: %s", active.Event, call.Result.Error)
		if prefetched && active.Event == 5 {
			require.Equal(t, false, call.Result.Data["source_data_read"])
		}
		if active.Event == 31 {
			require.Contains(t, call.Result.Error, "442")
			require.Contains(t, call.Result.Error, "A3330:AH3340")
			require.Contains(t, call.Result.Error, "A3341:AH3342")
		}
		if call.Result.Data["result_contract"] != nil {
			contract, err := structuredresult.ValidatePayload(call.Result.Data)
			require.NoError(t, err)
			require.NotNil(t, contract)
		}
	}

	if prefetched {
		require.Equal(t, 27, trace.AnswerEvent)
		require.Equal(t, 29, trace.CompleteEvent)
		require.Equal(t, sourceProgressStalledFallback, trace.HistoricalFinal)
		if enabled && rows {
			require.Contains(t, requestEvents, 8)
			require.Contains(t, requestEvents, 23)
			require.True(t, slices.ContainsFunc(engine.evidenceCompletion.record.Evidence, func(e coverageEvidence) bool {
				return e.Kind == "gap" && e.ToolCallID == "event-23" && e.Bound && e.Complete &&
					e.SeriesID == "futures.close.CF.CZC" && e.PeriodFrom == "2024-03-01" && e.PeriodTo == "2024-03-31"
			}), "the counterfactual native query registered its scoped result")
		}
		if enabled {
			for _, observed := range engine.evidenceCompletion.record.Evidence {
				if !rows || observed.Kind != "gap" {
					require.Equal(t, "directory", observed.Kind)
				}
			}
		}
	} else {
		require.Equal(t, 48, trace.AnswerEvent)
		require.Equal(t, 50, trace.CompleteEvent)
		require.Equal(t, incompleteAnswerFallback, trace.HistoricalFinal)
		require.Contains(t, requestEvents, 34, "the rejected retry reached the native transport once caching is accounted for")
		require.True(t, slices.ContainsFunc(engine.evidenceCompletion.record.Evidence, func(e coverageEvidence) bool {
			return e.ToolCallID == "event-43" && e.Kind == "last_valid" && e.Bound && e.Raw == "5160" &&
				e.Date == "2023-09-21" && e.DateCell == "A3340" && e.Cell == "B3340" && e.SourceTitle == "棉花数据库.xlsx" && e.Unit == ""
		}), "the verified historical last observation was registered by the native executor")
		require.True(t, slices.ContainsFunc(engine.evidenceCompletion.record.Evidence, func(e coverageEvidence) bool {
			return e.ToolCallID == "event-16" && e.Kind == "gap" && e.Bound && e.Complete && e.SeriesID == "warehouse.B" &&
				e.Metric == "仓单数量:一号棉花" && e.PeriodFrom == "2024-03-10" && e.PeriodTo == "2024-03-10"
		}), "the exact-date gap came from the historical bounded SQL input")
	}
	for _, draft := range trace.Drafts {
		require.False(t, state.IsComplete)
		state.CurrentRound++
		model.responses = append(model.responses, chatAnswer(draft))
		modelCalls++
		_, err = engine.runReActIteration(ctx, state, &messages, engine.buildToolsForLLM(), session, "turn", trace.Question, &guards)
		require.NoError(t, err)
	}
	if prefetched && rows {
		// The simulated successful reads avert the historical stall. Exercise
		// the same publication boundary with the nonhistorical result.
		require.False(t, state.IsComplete)
		engine.stopEvidenceCompletion(ctx, trace.Question, state, messages, "source_progress_stalled")
		engine.commitTerminalResult(ctx, state, session, trace.HistoricalFinal, "", "stalled", false)
	}
	require.True(t, state.IsComplete, "the original settlement path must end the turn")
	engine.commitTerminalResult(ctx, state, session, trace.HistoricalFinal, "", "stalled", false)
	if notice := engine.turnReview.TerminalReceipt.notice; notice != "" {
		require.Empty(t, state.FinalAnswer)
		require.Empty(t, finals, "calls without verified readings must not stream an answer")
		require.Contains(t, notice, trace.Question)
		require.Len(t, model.calls, modelCalls)
		return
	}
	require.Len(t, finals, 2, "one outward answer followed by its completion marker")
	require.Equal(t, finals[0].ID, finals[1].ID)
	require.Equal(t, state.FinalAnswer, finals[0].Data.(event.AgentFinalAnswerData).Content)
	require.False(t, finals[0].Data.(event.AgentFinalAnswerData).Done)
	done := finals[1].Data.(event.AgentFinalAnswerData)
	require.True(t, done.Done)
	require.Empty(t, done.Content)
	require.Len(t, model.calls, modelCalls, "no model calls were added to the prescribed replay")
	if !enabled {
		require.False(t, terminalCandidateFallback(state.FinalAnswer), "legacy execution skips coverage enforcement but publishes available evidence")
		require.Contains(t, state.FinalAnswer, "棉花数据库.xlsx")
		return
	}
	require.NotEmpty(t, state.FinalAnswer)
	require.False(t, terminalCandidateFallback(state.FinalAnswer))
	require.NotContains(t, state.FinalAnswer, "请稍后重试")
	if prefetched {
		require.Contains(t, state.FinalAnswer, "棉花数据库.xlsx")
		require.NotContains(t, state.FinalAnswer, "17110")
		if rows {
			require.Contains(t, state.FinalAnswer, "2024-03-01")
			require.Contains(t, state.FinalAnswer, "2024-03-31")
			require.Regexp(t, "(?:没有|无)有效(?:观测值|记录)", state.FinalAnswer)
		} else {
			require.Contains(t, state.FinalAnswer, "目录")
			require.Regexp(t, "(?:尚无|尚缺|仍待核实).*读数", state.FinalAnswer)
			require.NotRegexp(t, "(?:没有|无)有效(?:观测值|记录)", state.FinalAnswer)
			for _, line := range strings.Split(state.FinalAnswer, "\n") {
				if strings.Contains(line, "2023-09-22") {
					require.Contains(t, line, "目录")
					require.Contains(t, line, "收盘价")
				}
			}
		}
	} else {
		var valueLine, gapLine string
		for _, statement := range strings.Split(state.FinalAnswer, "。") {
			if strings.Contains(statement, "5160") {
				valueLine = statement
			}
			if strings.Contains(statement, "仓单数量:一号棉花") && strings.Contains(statement, "2024-03-10") {
				gapLine = statement
			}
		}
		for _, expected := range []string{"棉花数据库.xlsx", "郑棉仓单", "仓单数量:一号棉花"} {
			require.Contains(t, gapLine, expected, "the gap retains the actual source and scope")
		}
		for _, expected := range []string{"A3340", "B3340"} {
			require.Contains(t, valueLine, expected, "the historical value stays paired with its actual date and locator")
		}
		require.Regexp(t, `2023(?:-09-21|年9月21日)`, valueLine)
		require.Regexp(t, `单位(?:尚)?未确认`, valueLine)
		require.Equal(t, 1, strings.Count(state.FinalAnswer, "5160"), state.FinalAnswer)
		require.NotContains(t, valueLine, "2024-03-10")
		require.Regexp(t, `(?:没有|无)有效(?:观测值|记录)`, gapLine, "the exact-date gap must cover the requested field: %s", state.FinalAnswer)
		require.NotContains(t, gapLine, "5160")
	}
}
