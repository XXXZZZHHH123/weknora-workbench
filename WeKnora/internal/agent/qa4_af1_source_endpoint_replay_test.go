package agent

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"os"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// The A-1 extrema catalog and six rejected calls are from KB-22 SSE #7,
// #10/14/17/21/25/28 and the corresponding persisted execution steps. The
// rejected declaration itself was discarded by the runtime. Its fixture
// reconstructs the logged unverified_restriction_basis, with no replacement
// planner. The continuation uses R7-1's #10/17/18 on the same source hash.
// The three KB-31 lookup results and A-2's row-window result are from A-1
// #15, A-2 #16/26, and A-3 #15. Alternate protocol receipts were returned by
// the local native store from the same archived dataset, preserving selectors
// and versions. After aliasing IDs, the canonical typed hashes were recomputed.
type af1SourceFixture struct {
	Extrema struct {
		Question        string
		HistoricalFinal string `json:"historical_final"`
		Catalog         qa4Stage2Call
		Blocked         []qa4Stage2Call
		Fields          qa4Stage2Call
		Reads           []qa4Stage2Call
	}
	Endpoints []struct {
		Run             string
		Question        string
		HistoricalFinal string `json:"historical_final"`
		Observed        qa4Stage2Call
		OtherProtocol   json.RawMessage `json:"other_protocol"`
		Window          qa4Stage2Call
	}
}

func loadAf1SourceFixture(t *testing.T) af1SourceFixture {
	t.Helper()
	var fixture af1SourceFixture
	raw, err := os.ReadFile("testdata/qa4/replay/af1-source-endpoints.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &fixture))
	return fixture
}

type af1SourceReplay struct {
	t         *testing.T
	question  string
	engine    *AgentEngine
	state     types.AgentState
	ctx       context.Context
	response  qa4Stage2Call
	transport int
	wantFlag  any
}

func newAf1SourceReplay(t *testing.T, question string, calls ...qa4Stage2Call) *af1SourceReplay {
	t.Helper()
	const destination = "https://af1-source-replay.invalid"
	t.Setenv("STRUCTURED_DATA_URL", destination)
	key := t.TempDir() + "/signing-key"
	require.NoError(t, os.WriteFile(key, []byte(strings.Repeat("a", 32)), 0o600))
	t.Setenv("COMMODITY_RESEARCH_TOKEN_FILE", key)
	targets := types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: qa4Stage2Library, TenantID: 1}}
	e := newTestEngine(t, &qa4Stage2CModel{t: t}, withCitationsEnabled(true))
	e.config.WorkbenchDefault, e.config.AnswerReviewMode, e.config.SearchTargets = true, "standard", targets
	e.toolRegistry = agenttools.NewToolRegistry()
	e.toolRegistry.RegisterTool(agenttools.NewStructuredDataTool().WithSearchTargets(targets))
	e.toolRegistry.PrepareStructuredProtocol()
	manifest := structuredCodecManifest()
	for _, raw := range manifest["tools"].([]any) {
		entry := raw.(map[string]any)
		name := entry["name"].(string)
		properties := entry["inputSchema"].(map[string]any)["properties"].(map[string]any)
		if name == "query_series" || name == "analyze_table" {
			properties["include_last_valid"] = map[string]any{"type": "boolean"}
		}
		for _, call := range calls {
			if call.Tool != "structured_"+name {
				continue
			}
			for field := range call.Arguments {
				if field != "knowledge_base_id" && field != "include_last_valid" {
					properties[field] = map[string]any{}
				}
			}
		}
	}
	manifestResult, err := e.toolRegistry.InstallStructuredManifest(manifest)
	require.NoError(t, err)
	require.True(t, manifestResult.Success)
	r := &af1SourceReplay{
		t: t, question: question, engine: e,
		ctx: types.WithCaller(t.Context(), types.Caller{UserID: "fixture-reader", TenantID: 1, Role: types.TenantRoleViewer}),
	}
	original := http.DefaultTransport
	http.DefaultTransport = qa4Stage2Transport(func(request *http.Request) (*http.Response, error) {
		r.transport++
		require.Equal(t, destination+"/api/tools/read", request.URL.String())
		require.NotEmpty(t, request.Header.Get("X-Research-Signature"))
		var actual struct {
			Operation string
			Arguments map[string]any
		}
		require.NoError(t, json.NewDecoder(request.Body).Decode(&actual))
		require.Equal(t, r.response.Tool, "structured_"+actual.Operation)
		require.Equal(t, r.wantFlag, actual.Arguments["include_last_valid"])
		for _, field := range []string{"dataset_id", "series_id", "series_ids", "start_date", "end_date", "sheet"} {
			require.Equal(t, r.response.Arguments[field], actual.Arguments[field], field)
		}
		require.NotEmpty(t, r.response.Response)
		header := http.Header{}
		header.Set("X-Structured-Manifest-SHA256", request.Header.Get("X-Structured-Manifest-SHA256"))
		return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(string(r.response.Response)))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	return r
}

func (r *af1SourceReplay) execute(id string, call qa4Stage2Call, extra map[string]any, expectedFlag any) types.ToolCall {
	r.t.Helper()
	r.response, r.wantFlag = call, expectedFlag
	args := maps.Clone(call.Arguments)
	maps.Copy(args, extra)
	raw, err := json.Marshal(args)
	require.NoError(r.t, err)
	calls := []types.LLMToolCall{{ID: id, Function: types.FunctionCall{Name: call.Tool, Arguments: string(raw)}}}
	r.engine.processEvidenceCompletionCalls(r.ctx, r.question, &r.state, nil, calls)
	result := r.engine.runToolCall(r.ctx, calls[0], 0, 0, 0, "session", "turn")
	r.state.RoundSteps = append(r.state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{result}})
	r.engine.refreshEvidenceCompletion(r.ctx, r.question, &r.state, nil)
	return result
}

func TestQA4Af1RestrictedCatalogReplay(t *testing.T) {
	fixture := loadAf1SourceFixture(t).Extrema
	require.Contains(t, fixture.HistoricalFinal, "重复的来源请求已停止")
	require.Len(t, fixture.Blocked, 6)
	for _, call := range fixture.Blocked {
		require.Equal(t, coverageScopeRejection, call.HistoricalError)
		require.Equal(t, fixture.Fields.Arguments["dataset_id"], call.Arguments["dataset_id"])
		require.Equal(t, fixture.Fields.Arguments["sheet"], call.Arguments["sheet"])
	}
	t.Run("document-table-evidence-off", func(t *testing.T) {
		t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "false")
		calls := append([]qa4Stage2Call{fixture.Catalog, fixture.Fields}, fixture.Reads...)
		r := newAf1SourceReplay(t, fixture.Question, calls...)
		for _, call := range calls {
			result := r.execute(call.id(), call, nil, nil)
			require.True(t, result.Result.Success, result.Result.Error)
		}
		require.Equal(t, 4, r.transport)
	})
	for _, forced := range []string{"false", "true"} {
		t.Run(forced, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", forced)
			t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
			r := newAf1SourceReplay(t, fixture.Question, append([]qa4Stage2Call{fixture.Catalog, fixture.Fields}, fixture.Reads...)...)
			catalog := r.execute("original-catalog", fixture.Catalog, nil, nil)
			require.True(t, catalog.Result.Success, catalog.Result.Error)
			declaration := map[string]any{
				"goal_index": 0,
				"goal":       map[string]any{"goal_index": 0, "request_basis": fixture.Question, "source_mode": "only", "restriction_basis": "a paraphrase absent from the actual request"},
			}
			invalid := r.execute("unseen-dataset", fixture.Fields, map[string]any{"dataset_id": "unseen", coverageArgument: declaration}, nil)
			require.Equal(t, coverageScopeRejection, invalid.Result.Error)
			require.Equal(t, 1, r.transport)
			goals := r.engine.evidenceCompletionTargets()
			require.Len(t, goals, 1)
			require.Equal(t, []string{"unverified_restriction_basis"}, goals[0].PlanIssues)
			fields := r.execute("original-blocked-fields", fixture.Fields, nil, nil)
			require.True(t, fields.Result.Success, fields.Result.Error)
			require.NotNil(t, fields.CoverageSelectionGoal)
			for _, recorded := range fixture.Reads {
				result := r.execute(recorded.id(), recorded, nil, nil)
				require.True(t, result.Result.Success, result.Result.Error)
				rows := sourceNavigationRows(result.Result.Data["extremum_rows"])
				require.Len(t, rows, 1)
				if recorded.Arguments["operation"] == "max" {
					require.Equal(t, []any{"2023-09-01", "B4690", "17790"}, []any{rows[0]["date"], rows[0]["source_cell"], rows[0]["raw_value"]})
				} else {
					require.Equal(t, []any{"2023-03-16", "B4575", "13830"}, []any{rows[0]["date"], rows[0]["source_cell"], rows[0]["raw_value"]})
				}
			}
			r.engine.turnReview.Query = fixture.Question
			for _, path := range []string{"answer", "budget"} {
				reviewBefore := r.engine.turnReview
				state := r.state
				ctx := context.WithValue(r.ctx, types.LanguageContextKey, "zh-CN")
				if path == "answer" {
					const draft = "棉花数据库.xlsx 的郑棉期货表中，2023 年查到的主力合约收盘价最高为 17790，出现在 2023-09-01，原格 B4690。最低为 13830，出现在 2023-03-16，原格 B4575。"
					decision := r.engine.checkCandidate(ctx, fixture.Question, draft, "", &state, nil, false)
					if forced == "true" {
						require.Equal(t, candidateNeedSource, decision.Disposition)
						r.engine.commitTerminalResult(ctx, &state, "session", "", "", "failed", false, terminalBudget)
					} else {
						require.Equal(t, candidateAccepted, decision.Disposition)
						r.engine.commitTerminalResult(ctx, &state, "session", decision.Candidate, "", "answer", false)
					}
					require.Equal(t, draft, state.FinalAnswer)
				} else {
					r.engine.commitTerminalResult(ctx, &state, "session", "", "", "failed", false, terminalBudget)
				}
				require.True(t, state.IsComplete)
				for _, item := range []string{"棉花数据库.xlsx", "最高", "17790", "2023-09-01", "B4690", "最低", "13830", "2023-03-16", "B4575"} {
					require.Contains(t, state.FinalAnswer, item, path)
				}
				r.engine.turnReview = reviewBefore
			}
			outside := r.execute("other-dataset", fixture.Reads[0], map[string]any{"dataset_id": "other"}, nil)
			require.Equal(t, coverageScopeRejection, outside.Result.Error)
			r.ctx = types.WithTenantAPIKeyScope(r.ctx, types.TenantAPIKeyScope{KeyID: 1, KnowledgeBaseIDs: types.StringArray{"elsewhere"}})
			revoked := r.execute("revoked", fixture.Reads[0], nil, nil)
			require.Equal(t, coverageScopeRejection, revoked.Result.Error)
			require.Equal(t, 4, r.transport)
		})
	}
}

func TestQA4Af1SameFieldEndpointReplay(t *testing.T) {
	fixture := loadAf1SourceFixture(t)
	for _, enabled := range []string{"false", "true"} {
		for _, trace := range fixture.Endpoints {
			t.Run(enabled+"/"+trace.Run, func(t *testing.T) {
				t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", enabled)
				t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "false")
				r := newAf1SourceReplay(t, trace.Question, trace.Observed, trace.Window)
				original := trace.Observed
				var flag any
				if trace.Run == "qa4-A-3" {
					if enabled == "true" {
						flag = true
					} else {
						original.Response = trace.OtherProtocol
					}
				}
				read := r.execute("original", original, nil, flag)
				require.True(t, read.Result.Success, read.Result.Error)
				require.Empty(t, read.Result.Data["rows"])
				if trace.Window.Tool != "" {
					window := r.execute("earlier-window", trace.Window, nil, nil)
					require.True(t, window.Result.Success, window.Result.Error)
					require.Contains(t, trace.HistoricalFinal, "B3338")
				}
				feedback := r.engine.lastValidCoverageFeedback(r.ctx)
				if enabled == "false" {
					require.Empty(t, feedback)
					return
				}
				require.Len(t, feedback, 1)
				if trace.Run == "qa4-A-3" {
					assertAf1WarehouseEndpoint(t, feedback[0])
					return
				}
				require.Equal(t, "unavailable", feedback[0].Endpoint)
				require.Empty(t, feedback[0].Date, "a directory's last date is not an endpoint observation")
				require.Nil(t, feedback[0].Raw)
				// A normal tool retry chosen by the model, using the same period and
				// field and requesting the existing service capability explicitly.
				retry := trace.Observed
				retry.Response = trace.OtherProtocol
				read = r.execute("read-with-endpoint", retry, map[string]any{"include_last_valid": true}, true)
				require.True(t, read.Result.Success, read.Result.Error)
				feedback = r.engine.lastValidCoverageFeedback(r.ctx)
				require.Len(t, feedback, 1)
				if trace.Run == "qa4-A-1" {
					assertAf1WarehouseEndpoint(t, feedback[0])
				} else {
					require.Equal(t, "warehouse.AF", feedback[0].Series)
					require.Equal(t, json.Number("20.784"), feedback[0].Raw)
					require.Equal(t, "AF3340", feedback[0].Cell)
					require.Equal(t, "万吨", feedback[0].Unit)
					require.NotEqual(t, "B3338", feedback[0].Cell)
					require.NotEqual(t, "B3340", feedback[0].Cell)
				}
			})
		}
	}
}

func assertAf1WarehouseEndpoint(t *testing.T, endpoint coverageLastValidFeedback) {
	t.Helper()
	require.Equal(t, "棉花数据库.xlsx", endpoint.Source)
	require.Equal(t, "仓单数量:一号棉花", endpoint.Field)
	require.Equal(t, "warehouse.B", endpoint.Series)
	require.Equal(t, "observed", endpoint.Endpoint)
	require.Equal(t, "2023-09-21", endpoint.Date)
	require.Equal(t, "A3340", endpoint.DateCell)
	require.Equal(t, "B3340", endpoint.Cell)
	require.Empty(t, endpoint.Unit)
	number, ok := coverageFiniteNumber(endpoint.Raw)
	require.True(t, ok)
	require.Equal(t, 5160.0, number)
}

func TestQA4Af1ScopedLastValidDefault(t *testing.T) {
	trace := loadAf1SourceFixture(t).Endpoints[0]
	for _, scenario := range []string{"verified", "missing-primary-ref", "window-only", "mixed", "unverified-field", "explicit-false", "feature-disabled"} {
		t.Run(scenario, func(t *testing.T) {
			enabled := scenario != "feature-disabled"
			if enabled {
				t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", "true")
			} else {
				t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", "false")
			}
			r := newAf1SourceReplay(t, trace.Question, trace.Observed)
			read := trace.Observed
			first := r.execute("first", read, nil, nil)
			require.True(t, first.Result.Success, first.Result.Error)
			turn := &r.engine.evidenceCompletion
			require.Len(t, turn.record.Evidence, 1)
			item := turn.record.Evidence[0]
			target := taskCoverageTarget{GoalIndex: 0, RequestBasis: trace.Question, StructuredTaskGoal: &StructuredTaskGoal{
				SourceMode: "named_first", SourceTime: "observed_period", OnVerifiedGap: []string{"last_valid_same_field"},
				Period: map[string]any{"from": item.PeriodFrom, "to": item.PeriodTo, "grain": "day"},
			}}
			binding := coverageBinding{GoalIndex: 0, Role: "primary", KnowledgeBaseID: item.KnowledgeBaseID, SourceID: item.SourceID,
				SeriesID: item.SeriesID, EvidenceID: item.EvidenceID, JSONPointer: item.JSONPointer, SnapshotHash: item.SnapshotHash,
				PeriodFrom: item.PeriodFrom, PeriodTo: item.PeriodTo}
			var extra map[string]any
			var expected any
			switch scenario {
			case "missing-primary-ref":
				target.PlanIssues = []string{"unverified_primary_ref"}
			case "window-only":
				target.SourceMode, target.OnVerifiedGap = "only", []string{"report_gap"}
			case "unverified-field":
				binding.SeriesID = "another-field"
			case "explicit-false":
				extra, expected = map[string]any{"include_last_valid": false}, false
			}
			turn.setCoverageGoal(target)
			if scenario == "mixed" {
				turn.setCoverageGoal(taskCoverageTarget{GoalIndex: 1, RequestBasis: trace.Question, StructuredTaskGoal: &StructuredTaskGoal{SourceMode: "open"}})
			}
			turn.record.Bindings = append(turn.record.Bindings, binding)
			if scenario == "verified" || scenario == "missing-primary-ref" {
				read.Response = trace.OtherProtocol
				expected = true
			}
			result := r.execute("next", read, extra, expected)
			require.True(t, result.Result.Success, result.Result.Error)
			require.Equal(t, expected, result.ExecutionArgs()["include_last_valid"])
			if extra == nil {
				require.NotContains(t, result.Args, "include_last_valid", "the replay retains the exact model call")
			}
		})
	}
}

func TestQA4Af1EndpointDraftRequiresOriginalTuple(t *testing.T) {
	trace := loadAf1SourceFixture(t).Endpoints[2]
	t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", "true")
	r := newAf1SourceReplay(t, trace.Question, trace.Observed)
	r.engine.turnReview.Query = trace.Question
	read := r.execute("verified-sql", trace.Observed, nil, true)
	require.True(t, read.Result.Success, read.Result.Error)
	record := r.engine.evidenceCompletion.record
	var fact coverageDraftFact
	for _, gap := range record.Evidence {
		if gap.Kind == "gap" && gap.SeriesID == "warehouse.B" {
			endpoint, ok := coverageEndpointForGap(record, gap)
			require.True(t, ok)
			fact.coverageFact = coverageFact{Role: "last_valid_value", Evidence: endpoint}
		}
	}
	require.NotEmpty(t, fact.Evidence.EvidenceID)
	correct := "棉花数据库.xlsx 的郑棉仓单字段最后有效日为 2023-09-21，B3340 的原值是 5,160。"
	require.True(t, newCoverageDraftParagraph(correct).displaysVerifiedRole(fact, nil))
	review := coverageDraftReview{record: record, facts: []coverageDraftFact{fact}}
	missing := r.engine.assessCoverageDraft(r.ctx, review, "棉花数据库.xlsx 在所问日无值，最后有效日是 2023-09-21。", &r.state)
	feedback := r.engine.coverageDraftFeedback(review, missing)
	require.NotNil(t, feedback)
	require.Len(t, feedback.Missing, 1)
	require.Equal(t, "B3340", feedback.Missing[0].Cell)
	require.Contains(t, feedback.text(), "5160")
	shown := r.engine.assessCoverageDraft(r.ctx, review, correct, &r.state)
	require.Empty(t, shown.missing)

	budget := r.engine.terminalCoverageFailure(r.ctx, &r.state, terminalBudget)
	for _, evidence := range []string{"棉花数据库.xlsx", "2024-03-10", "2023-09-21", "5160", "B3340"} {
		require.Contains(t, budget, evidence)
	}
	require.Equal(t, 1, strings.Count(budget, "5160"))
	for _, mutation := range [][2]string{{"2023-09-21", "2023-09-19"}, {"B3340", "B3338"}, {"B3340", "B33401"}, {"5,160", "5,467"}, {"棉花数据库.xlsx", "同日周报"}} {
		t.Run(mutation[1], func(t *testing.T) {
			wrong := strings.Replace(correct, mutation[0], mutation[1], 1)
			require.False(t, newCoverageDraftParagraph(wrong).displaysVerifiedRole(fact, nil))
		})
	}
}

// The earlier replay has the same cited page-18 excerpt from the stored source
// snapshot. A-1, A-2 and A-3 all cited it; only the structured lookups differ.
func (r *af1SourceReplay) readAf1Report() {
	r.t.Helper()
	var fixture qa4Stage2CTrace
	raw, err := os.ReadFile("testdata/qa4/replay/s2-3-kb31.json")
	require.NoError(r.t, err)
	require.NoError(r.t, json.Unmarshal(raw, &fixture))
	doc := fixture.Documents[1]
	doc.TenantID, doc.KnowledgeBaseID, doc.ParseStatus = 1, qa4Stage2Library, types.ParseStatusCompleted
	repo := &qa4Stage2CChunkRepository{}
	for _, row := range fixture.Chunks {
		if row.ID == "replay-0310-slide-18" {
			row.TenantID, row.KnowledgeBaseID, row.IsEnabled = 1, qa4Stage2Library, true
			repo.rows = append(repo.rows, row)
		}
	}
	require.Len(r.t, repo.rows, 1)
	r.engine.toolRegistry.RegisterTool(agenttools.NewReadDocumentTool(&qa4Stage2CDocuments{documents: []*types.Knowledge{doc}},
		&qa4Stage2CChunks{repo: repo}, r.engine.config.SearchTargets))
	agenttools.PrepareDocumentScope(r.engine.toolRegistry, r.question)
	result := r.execute("real-page-18", qa4Stage2Call{Tool: agenttools.ToolReadDocument, Arguments: map[string]any{"id": doc.ID, "page": 18}}, nil, nil)
	require.True(r.t, result.Result.Success, result.Result.Error)
	require.True(r.t, agenttools.IsCurrentDocumentRead(r.engine.toolRegistry, result.Result))
	r.engine.modelContext.ModelToolResultForTool(agenttools.ToolReadDocument, result.Result)
	require.NotEmpty(r.t, r.engine.modelContext.CitableChunkHandle("replay-0310-slide-18"))
}

func TestQA4Af1EndpointNormalAndBudgetDelivery(t *testing.T) {
	fixture := loadAf1SourceFixture(t)
	for _, enabled := range []string{"false", "true"} {
		for _, trace := range fixture.Endpoints {
			t.Run(enabled+"/"+trace.Run, func(t *testing.T) {
				t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", enabled)
				t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "false")
				r := newAf1SourceReplay(t, trace.Question, trace.Observed, trace.Window)
				r.engine.turnReview.Query = trace.Question
				original := trace.Observed
				var flag any
				hasEndpoint := trace.Run == "qa4-A-3" && enabled == "true"
				if hasEndpoint {
					flag = true
				} else if trace.Run == "qa4-A-3" {
					original.Response = trace.OtherProtocol
				}
				read := r.execute("original", original, nil, flag)
				require.True(t, read.Result.Success, read.Result.Error)
				if trace.Window.Tool != "" {
					window := r.execute("older-window", trace.Window, nil, nil)
					require.True(t, window.Result.Success, window.Result.Error)
				}
				r.readAf1Report()
				ctx := context.WithValue(r.ctx, types.LanguageContextKey, "zh-CN")
				budgetState := r.state
				review := r.engine.turnReview
				r.engine.commitTerminalResult(ctx, &budgetState, "session", "", "", "failed", false, terminalBudget)
				require.True(t, budgetState.IsComplete)
				if hasEndpoint {
					for _, value := range []string{"2023-09-21", "5160", "B3340"} {
						require.Contains(t, budgetState.FinalAnswer, value)
					}
				} else {
					require.NotContains(t, budgetState.FinalAnswer, "5160")
					require.NotContains(t, budgetState.FinalAnswer, "同字段最近有效记录为 2023-09-19")
				}
				r.engine.turnReview = review
				if trace.Run == "qa4-A-2" && enabled == "true" {
					model := r.engine.chatModel.(*qa4Stage2CModel)
					r.engine.config.StructuredResultReviewEnabled = true
					historical := strings.NewReplacer(
						"2b3e3e83-b309-4e66-9eee-79f437126111", "replay-0310-slide-18",
						"0d463b67-5640-4b94-84d1-8bbb426c3bcd", "replay-library",
					).Replace(trace.HistoricalFinal)
					gapClaim, _, _ := strings.Cut(historical, "\n\n")
					model.review = lightReviewEdits(
						structuredResultEdit{gapClaim, "对仓单和有效预报合计的查询没有返回所问日数值，仓单单项的所问日数值尚未核实。"},
						structuredResultEdit{"该字段最后一次有值的日期和原值如下：", "下面是原表窗口中的一条历史记录，该次读取未核实单项末值："},
					)
					decision := r.engine.checkCandidate(ctx, trace.Question, historical, "", &r.state, nil, false)
					require.Equal(t, candidateAccepted, decision.Disposition)
					require.Equal(t, "corrected", decision.StructuredResultReviewStatus)
					require.Equal(t, 1, model.reviewCalls)
					require.Contains(t, model.reviewInput[1].Content, `"series_id":"warehouse.AF"`)
					require.Contains(t, model.reviewInput[1].Content, `"endpoint_status":"unavailable"`)
					require.NotContains(t, model.reviewInput[1].Content, `"original_value":5467`)
					r.engine.commitTerminalResult(ctx, &r.state, "session", decision.Candidate, "", "answer", false)
					require.NotContains(t, r.state.FinalAnswer, "该字段最后一次有值")
					require.Contains(t, r.state.FinalAnswer, "该次读取未核实单项末值")
					require.Contains(t, r.state.FinalAnswer, "60.6万吨")
					return
				}
				draft := "棉花数据库.xlsx 中所问日期的仓单数量原值尚未核实。"
				if hasEndpoint {
					draft = "棉花数据库.xlsx 的郑棉仓单数量在 2024-03-10 无有效记录。其最后有效日为 2023-09-21，B3340 的原值是 5160，单位未确认。"
				}
				draft += "\n\n棉花周报20240310.pptx 第18页披露郑棉仓单合计60.6万吨，属独立报告口径。<kb doc=\"棉花周报20240310.pptx\" chunk_id=\"replay-0310-slide-18\" kb_id=\"replay-library\" />"
				decision := r.engine.checkCandidate(ctx, trace.Question, draft, "", &r.state, nil, false)
				require.Equal(t, candidateAccepted, decision.Disposition)
				r.engine.commitTerminalResult(ctx, &r.state, "session", decision.Candidate, "", "answer", false)
				require.Equal(t, draft, r.state.FinalAnswer)
				require.Contains(t, r.state.FinalAnswer, "60.6万吨")
				if hasEndpoint {
					require.Equal(t, 1, strings.Count(r.state.FinalAnswer, "5160"))
				}
			})
		}
	}
}

func TestQA4Af1EndpointCitationCheckedCache(t *testing.T) {
	trace := loadAf1SourceFixture(t).Endpoints[2]
	for _, editor := range []bool{false, true} {
		for _, omit := range []string{"", "2023-09-21", "5160", "B3340", "different-cell"} {
			name := "editor-off"
			if editor {
				name = "editor-keep"
			}
			if omit != "" {
				name += "/omit-" + omit
			}
			t.Run(name, func(t *testing.T) {
				t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", "true")
				t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "false")
				r := newAf1SourceReplay(t, trace.Question, trace.Observed)
				r.engine.config.EvidenceReferencesEnabled = true
				r.engine.config.StructuredResultReviewEnabled = editor
				result := r.execute("checked-sql", trace.Observed, nil, true)
				require.True(t, result.Result.Success, result.Result.Error)
				require.Contains(t, r.engine.currentStructuredCitationView(result), "/last_valid/rows/0/value")
				if omit != "" {
					r.readAf1Report()
				}
				var endpointID string
				for id, citation := range r.engine.turnReview.StructuredCitations {
					if pointer, _ := citation.Directory["json_pointer"].(string); strings.HasSuffix(pointer, "/last_valid/rows/0/value") {
						endpointID = id
					}
				}
				require.NotEmpty(t, endpointID)
				draft := "棉花数据库.xlsx 郑棉仓单最后有效日 2023-09-21，B3340 原值 5160。"
				if omit == "different-cell" {
					draft = strings.Replace(draft, "B3340", "B3338", 1)
				} else if omit != "" {
					draft = strings.Replace(draft, omit, "", 1)
				}
				draft += `<evidence evidence_id="` + endpointID + `" />`
				if omit != "" {
					draft += "\n\n棉花周报20240310.pptx 第18页披露郑棉仓单合计60.6万吨。<kb doc=\"棉花周报20240310.pptx\" chunk_id=\"replay-0310-slide-18\" kb_id=\"replay-library\" />"
				}
				decision := r.engine.checkCandidate(r.ctx, trace.Question, draft, "", &r.state, nil, false)
				require.Equal(t, candidateAccepted, decision.Disposition)
				model := r.engine.chatModel.(*qa4Stage2CModel)
				if editor {
					require.Equal(t, 1, model.reviewCalls)
					require.Equal(t, "kept", decision.StructuredResultReviewStatus)
				} else {
					require.Zero(t, model.reviewCalls)
				}
				got, cached := r.engine.currentCoverageDraft(r.ctx, &r.state)
				if omit == "" || omit == "B3340" {
					if omit == "" {
						require.True(t, cached)
						require.Equal(t, draft, got)
					}
					require.Equal(t, terminalNoFailure, r.engine.terminalCandidateEvidenceFailure(r.ctx, &r.state, draft))
					r.engine.commitTerminalResult(r.ctx, &r.state, "session", draft, "", "answer", false)
					require.Equal(t, draft, r.state.FinalAnswer)
					return
				}
				require.False(t, cached)
				require.NotNil(t, decision.CoverageFeedback)
				require.Contains(t, decision.CoverageFeedback.text(), "B3340")
				require.Equal(t, terminalEndpointEvidence, r.engine.terminalCandidateEvidenceFailure(r.ctx, &r.state, draft))
				r.engine.commitTerminalResult(r.ctx, &r.state, "session", draft, "", "answer", false)
				require.NotEqual(t, draft, r.state.FinalAnswer)
				for _, checked := range []string{"2023-09-21", "5160", "B3340", "棉花周报20240310.pptx", "60.6万吨"} {
					require.Contains(t, r.state.FinalAnswer, checked)
				}
				require.NotContains(t, r.state.FinalAnswer, "B3338")
			})
		}
	}
}
