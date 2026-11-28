package agent

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// Af1 r2 KB-28 #19 and #29 retained the table locator and pixel-only
// page result. The event stream omitted the report body. The subsequent
// allowed read uses the independently preserved original OCR fixture.
func TestQA4Af2SourceHandoffAfterNaturalStop(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_FINAL_READ_ANSWER", "false")
	for _, mode := range []string{"repeated draft", "used draft repairs", "stalled directory", "revoked", "model budget", "iteration budget", "legacy", "document off", "planning off"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
			t.Setenv("WEKNORA_AGENT_STRUCTURED_TASK_PLAN", "true")
			r := newQA4Stage2CReplay(t, "af1-kb28", mode == "legacy", false)
			r.engine.config.StructuredResultReviewEnabled = false
			r.planComparison()
			target := &r.engine.taskPlan.CoverageTargets[0]
			target.Table = "中国棉花平衡表"
			target.ComparisonSources = []string{"3月31日周报", "棉花数据库"}
			// Retain the same useful portions of a degraded plan, without
			// substituting an otherwise absent per-read coverage declaration.
			target.PlanIssues = []string{"unverified_primary_ref"}
			r.engine.taskPlan.DeliveryRequirements = []taskDeliveryRequirement{{Kind: "text", Format: "prose"}}
			r.calls(r.trace.Calls[:1])
			if mode == "repeated draft" {
				// Af1 r2 #30 already had the workbook cell. This archived
				// Af1 r3 native request repeats that exact field and month
				// on the same source snapshot; it supplies the normal citation.
				r.calls(r.trace.Calls[5:6])
				require.Contains(t, r.results[32].Result.Output, "298.93291576470585")
			}
			raw, err := os.ReadFile("testdata/qa4/replay/af2-kb28-continuation.json")
			require.NoError(t, err)
			var frozen []qa4Stage2Call
			require.NoError(t, json.Unmarshal(raw, &frozen))
			for _, observed := range frozen {
				data, err := decodeCitationJSON(string(observed.Response))
				require.NoError(t, err)
				call := types.ToolCall{ID: observed.id(), Name: observed.Tool, Args: observed.Arguments,
					Result: &types.ToolResult{Success: true, Data: data, Output: string(observed.Response)}}
				r.state.RoundSteps = append(r.state.RoundSteps, types.AgentStep{Iteration: observed.Iteration, ToolCalls: []types.ToolCall{call}})
			}
			r.state.CurrentRound = 3
			r.engine.refreshEvidenceCompletion(r.ctx, r.trace.Question, r.state, r.messages)

			const incomplete = "我只看到来源位置，还没有可以比较的表内读数。"
			if mode == "used draft repairs" {
				r.engine.turnReview.Repairs = maxTurnCandidateRepairs
			}
			switch mode {
			case "revoked":
				r.ctx = types.WithTenantAPIKeyScope(r.ctx, types.TenantAPIKeyScope{KeyID: 1, KnowledgeBaseIDs: types.StringArray{"elsewhere"}})
				r.run(chatAnswer(incomplete))
				require.NotContains(t, r.guards.pendingRuntimeNotice, "unread_document_pages")
				require.Empty(t, r.results[17].ID)
				return
			case "model budget":
				r.ctx = context.WithValue(r.ctx, agentExecutionStartKey{}, time.Now().Add(-standardAnswerActiveBudget-time.Second))
				r.engine.chatModel = &mockChat{responses: []mockResponse{chatAnswer(incomplete), chatAnswer(incomplete), chatAnswer(incomplete)}}
				outcome, err := r.engine.runReActIteration(r.ctx, r.state, &r.messages, r.engine.buildToolsForLLM(), "s2c", "turn", r.trace.Question, &r.guards)
				require.NoError(t, err)
				require.Equal(t, iterOutcomeBreak, outcome)
				require.True(t, r.state.IsComplete)
				require.NotContains(t, r.guards.pendingRuntimeNotice, "unread_document_pages")
				return
			case "iteration budget":
				r.engine.config.MaxIterations = r.state.CurrentRound + 1
				r.run(chatAnswer(incomplete))
				require.NotContains(t, r.guards.pendingRuntimeNotice, "unread_document_pages")
				return
			case "document off", "planning off":
				if mode == "document off" {
					t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "false")
				} else {
					t.Setenv("WEKNORA_AGENT_STRUCTURED_TASK_PLAN", "false")
				}
				r.run(chatAnswer(incomplete))
				require.NotContains(t, r.guards.pendingRuntimeNotice, "unread_document_pages")
				return
			case "stalled directory":
				r.guards.toolProgress.stale = toolProgressStopRounds - 1
				// A repeated binding has the same physical source identity.
				// Its result reaches the normal after-tool stagnation branch.
				r.calls(r.trace.Calls[:1])
			default:
				r.run(chatAnswer(incomplete))
				require.False(t, r.state.IsComplete)
				require.Contains(t, r.guards.pendingRuntimeNotice, `"unread_document_pages":[{"document_id":"replay-comparison-report","page":6}]`)
				r.run(chatAnswer(incomplete + " <tool_call>"))
			}
			require.False(t, r.state.IsComplete)
			require.Contains(t, r.guards.pendingRuntimeNotice, "unread_document_pages")
			require.Empty(t, r.results[17].ID, "a reminder and literal tool-like text are not execution")
			r.calls(r.trace.Calls[1:2])
			require.True(t, r.results[17].Result.Success, r.results[17].Result.Error)
			require.NotEmpty(t, r.results[17].Result.Data["table_reads"])
			if mode == "repeated draft" {
				record := r.engine.currentCoverageScopeRecord(r.ctx)
				citation := af2ReplayCellCitation(t, record, "G87", "balance.primary.G")
				report := r.engine.modelContext.CitableChunkHandle("replay-comparison-page")
				require.NotEmpty(t, report)
				// Offline bounded edit of the historical Af1 r3 comparison.
				// The r2 draft never contained this completed two-source answer.
				draft := "两处原报数值不一致。\n\n" +
					`棉花周报20240331.pptx 第6页“中国棉花平衡表”的 2023-10 行，“工商业期末库存”为 331.2。<ref id="` + report + `"/>` + "\n\n" +
					`棉花数据库.xlsx 中国棉花平衡表在 2023-10-31（A87）记录的“工商业期末库存”为 298.93（G87）。<ref id="` +
					r.engine.modelContext.CitableEvidenceHandle(citation) + `"/>`
				r.run(chatAnswer(draft))
				require.NotContains(t, r.guards.pendingRuntimeNotice, "unread_document_pages")
				af2ReplayComplete(t, r.engine, r.ctx, r.state, "s2c")
				answer := r.answer()
				require.Contains(t, answer, "两处原报数值不一致")
				require.Contains(t, answer, "棉花周报20240331.pptx 第6页“中国棉花平衡表”的 2023-10 行，“工商业期末库存”为 331.2")
				require.Contains(t, answer, "棉花数据库.xlsx 中国棉花平衡表在 2023-10-31（A87）记录的“工商业期末库存”为 298.93（G87）")
				return
			}
			r.run(chatAnswer(incomplete))
			require.NotContains(t, r.guards.pendingRuntimeNotice, "unread_document_pages",
				"the checked full page consumes that physical reading action")
		})
	}
}

func TestQA4Af2UnboundClosingFieldReturnsToOrdinaryRead(t *testing.T) {
	for _, enabled := range []string{"true", "false"} {
		t.Run(enabled, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "false")
			t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
			t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", enabled)
			fixture := loadAf2LastValidCalls(t)
			raw, err := os.ReadFile("testdata/qa4/replay/af2-kb16-endpoint.json")
			require.NoError(t, err)
			var observed qa4Stage2Call
			require.NoError(t, json.Unmarshal(raw, &observed))
			data, err := decodeCitationJSON(string(observed.Response))
			require.NoError(t, err)
			require.NoError(t, rehashCoverageSQLFixture(data))
			observed.Response, err = json.Marshal(data)
			require.NoError(t, err)
			observed.Arguments["source_binding"] = nil // Advertise the existing native schema.
			question := fixture.ClosingUnbound.Arguments["source_question"].(string)
			r := newAf1SourceReplay(t, question, fixture.ClosingCatalog, fixture.ClosingUnbound, observed)
			r.engine.config.StructuredResultReviewEnabled = false
			r.engine.config.EvidenceReferencesEnabled = true
			r.engine.config.MaxIterations = 20
			r.engine.turnReview.Query = question
			r.ctx = withAgentExecutionStart(r.ctx)
			messages := []chat.Message{{Role: "system", Content: "Read authorized sources."}, {Role: "user", Content: question}}
			r.engine.reviewConversation, r.engine.reviewConversationReady = messages, true
			r.engine.taskPlan = &taskEvidencePlan{
				Goals: []string{"Read the selected closing price"}, Evidence: []string{"Original bounded observation"},
				DeliveryRequirements: []taskDeliveryRequirement{{Kind: "text", Format: "prose"}},
				CoverageTargets: []taskCoverageTarget{{GoalIndex: 0, RequestBasis: question, PlanIssues: []string{"unverified_primary_ref"},
					StructuredTaskGoal: &StructuredTaskGoal{
						SourceMode: "named_first", SourceTime: "observed_period", Metric: "收盘价",
						Period:        map[string]any{"from": "2024-03-01", "to": "2024-03-31", "grain": "month"},
						OnVerifiedGap: []string{"last_valid_same_field"},
					}}},
			}
			catalog := r.execute(fixture.ClosingCatalog.id(), fixture.ClosingCatalog, nil, nil)
			require.True(t, catalog.Result.Success, catalog.Result.Error)
			var sent any
			if enabled == "true" {
				sent = true
			}
			unbound := r.execute(fixture.ClosingUnbound.id(), fixture.ClosingUnbound, nil, sent)
			require.Equal(t, "needs_clarification", unbound.Result.Data["status"])
			r.state.CurrentRound = 2
			guards := &loopGuards{}
			visible := &terminalAnswerRecorder{}
			visible.attach(r.engine.eventBus)
			model := &mockChat{responses: []mockResponse{chatAnswer("尚未核对所问期间的收盘价。")}}
			r.engine.chatModel = model
			run := func() iterOutcome {
				t.Helper()
				outcome, err := r.engine.runReActIteration(r.ctx, &r.state, &messages, r.engine.buildToolsForLLM(), "session", "turn", question, guards)
				require.NoError(t, err)
				r.state.CurrentRound++
				return outcome
			}
			run()
			require.Equal(t, 2, r.transport, "a model stop cannot create a source binding or a value")
			if enabled == "false" {
				require.NotContains(t, guards.pendingRuntimeNotice, "bind_series_endpoint")
				return
			}
			require.False(t, r.state.IsComplete)
			require.Contains(t, guards.pendingRuntimeNotice, "bind_series_endpoint")
			require.Contains(t, guards.pendingRuntimeNotice, "2024-03-01")

			// The ordinary offline model selects one actual field using the
			// complete group witnessed by r1. Its chosen range and singleton
			// service return repeat Af1 r2 #14/#15.
			binding := e2eClosingBinding(t, unbound.Result.Data, observed.Arguments["series_id"].(string))
			observed.Arguments["source_binding"] = binding
			args, err := json.Marshal(observed.Arguments)
			require.NoError(t, err)
			r.response, r.wantFlag = observed, true
			model.responses = append(model.responses, chatToolCall("bound-query", observed.Tool, string(args)))
			require.Equal(t, iterOutcomeNext, run())
			require.Len(t, model.calls, 2, "only the original source loop uses the offline model")
			require.Equal(t, 3, r.transport)
			require.Contains(t, model.opts[1].RuntimeNotice, "candidate_groups")
			calls := r.state.RoundSteps[len(r.state.RoundSteps)-1].ToolCalls
			require.Len(t, calls, 1)
			require.True(t, calls[0].Result.Success, calls[0].Result.Error)
			require.Equal(t, binding, calls[0].ExecutionArgs()["source_binding"])
			endpoints := r.engine.lastValidCoverageFeedback(r.ctx)
			require.Len(t, endpoints, 1)
			require.Equal(t, "2023-09-22", endpoints[0].Date)
			require.Equal(t, "B4705", endpoints[0].Cell)
			require.True(t, coverageSameOriginalValue(endpoints[0].Raw, json.Number("17110")))
			citation := af2ReplayCellCitation(t, r.engine.currentCoverageScopeRecord(r.ctx), "B4705", "futures.close.CF.CZC")
			// The final model stub adapts the successful Af1 r2 answer.
			// The r1 response stopped at the directory and never had this value.
			draft := "2024年3月取不到：在《棉花数据库.xlsx》的“郑棉期货”工作表中，“收盘价”列（合约代码 CF.CZC，B列）在该月没有已导入的有效记录。\n\n" +
				"可核实的最新一个有效值：2023-09-22 收盘价 17110（工作表“郑棉期货”，B4705单元格，对应日期在A4705）。原表未标明单位。" +
				`<ref id="` + r.engine.modelContext.CitableEvidenceHandle(citation) + `"/>`
			finalModel := &qa4Stage2CModel{t: t, reply: chatAnswer(draft)}
			r.engine.chatModel = finalModel
			outcome := run()
			if outcome == iterOutcomeNext {
				require.True(t, guards.structuredAnswerReviewed, "the existing draft review may request one checked rewrite")
				finalModel.reply = chatAnswer(draft)
				outcome = run()
			}
			require.Equal(t, iterOutcomeBreak, outcome)
			af2ReplayComplete(t, r.engine, r.ctx, &r.state, "session")
			done, answer := visible.snapshot()
			require.Equal(t, 1, done)
			require.Equal(t, r.state.FinalAnswer, answer)
			require.Contains(t, answer, "2024年3月取不到")
			require.Contains(t, answer, "2023-09-22 收盘价 17110（工作表“郑棉期货”，B4705单元格，对应日期在A4705）")
		})
	}
}

func e2eClosingBinding(t *testing.T, data map[string]any, selected string) []any {
	t.Helper()
	groups := reviewAsSlice(data["fields"])
	require.Len(t, groups, 1)
	var ids []any
	for _, candidate := range reviewAsSlice(groups[0]) {
		ids = append(ids, reviewAsMap(candidate)["series_id"])
	}
	require.Len(t, ids, 7)
	require.True(t, slices.Contains(ids, any(selected)))
	return []any{map[string]any{"considered_ids": ids, "selected_ids": []any{selected},
		"reason": "选合约维度 CF.CZC 的本列，其他候选分别为期限合约。"}}
}

func TestQA4Af2WarehouseSupplementUsesExistingSourceRound(t *testing.T) {
	for _, mode := range []string{"named_first", "only"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
			t.Setenv("WEKNORA_STRUCTURED_LAST_VALID", "true")
			frozen := loadAf2LastValidCalls(t).MultiSQL[1]
			r := newQA4Stage2CReplay(t, "s2-2-kb31", false, true, frozen)
			r.engine.config.StructuredResultReviewEnabled = false
			goal := StructuredTaskGoal{
				SourceMode: "named_first", SourceTime: "observed_period", PrimaryRef: "棉花数据库",
				Metric: "仓单数量:一号棉花", Required: []string{"scoped_value"},
				Period:   map[string]any{"from": "2024-03-10", "to": "2024-03-10", "grain": "day"},
				Delivery: map[string]any{"kind": "text"}, OnVerifiedGap: []string{"last_valid_same_field", "attributed_supplement"},
			}
			r.setPlan(goal)
			r.engine.taskPlan.DeliveryRequirements = []taskDeliveryRequirement{{Kind: "text", Format: "prose"}}
			// Replay the genuine completed native batch. The original r2
			// stopped with this as its only read of the requested field.
			data, err := decodeCitationJSON(string(frozen.Response))
			require.NoError(t, err)
			rawArgs, err := json.Marshal(frozen.Arguments)
			require.NoError(t, err)
			descriptor, _, native := r.engine.toolRegistry.StructuredInvocation(frozen.Tool, rawArgs)
			require.True(t, native)
			data["_structured_execution"] = descriptor
			frozenCall := types.ToolCall{ID: frozen.id(), Name: frozen.Tool, Args: frozen.Arguments,
				Result: &types.ToolResult{Success: true, Data: data, Output: string(frozen.Response)}}
			r.state.RoundSteps = []types.AgentStep{{ToolCalls: []types.ToolCall{frozenCall}}}
			r.engine.currentStructuredCitationView(frozenCall)
			r.state.CurrentRound = 1
			require.Contains(t, string(frozen.Response), "5160")
			if mode == "only" {
				target := r.engine.taskPlan.CoverageTargets[0]
				target.SourceMode, target.RestrictionBasis = "only", target.PrimaryRef
				target.OnVerifiedGap = []string{"last_valid_same_field"}
				r.run(chatToolCall("outside-primary", "read_document", `{"id":"replay-supplemental-report","page":18}`))
				tool := r.state.RoundSteps[len(r.state.RoundSteps)-1].ToolCalls[0]
				require.True(t, tool.Result.PreflightRejected)
				require.Equal(t, coverageScopeRejection, tool.Result.Error)
				r.run(chatAnswer(""))
				require.NotContains(t, r.guards.pendingRuntimeNotice, "attributed_supplement")
				return
			}
			r.run(chatAnswer(""))
			require.False(t, r.state.IsComplete)
			require.Contains(t, r.guards.pendingRuntimeNotice, "discover_attributed_supplement")
			require.Contains(t, r.guards.pendingRuntimeNotice, "2024-03-10")
			// This offline model reply selects the separately authorized report.
			// The actual, independent report body comes from the archival fixture.
			r.run(chatToolCall("bind-supplement", "bind_document_scope",
				`{"selection_reason":"Choose the report covering the requested date as a separately attributed supplement.","sources":[{"knowledge_id":"replay-supplemental-report","expected_title":"棉花周报20240310.pptx","date_evidence":{"date":"2024-03-10","source_field":"file_name","source_text":"20240310"}}]}`))
			r.run(chatToolCall("read-supplement", "read_document", `{"id":"replay-supplemental-report","page":18}`))
			tool := r.state.RoundSteps[len(r.state.RoundSteps)-1].ToolCalls[0]
			require.True(t, tool.Result.Success, tool.Result.Error)
			require.Contains(t, tool.Result.Output, "60.6万吨")
			record := r.engine.currentCoverageScopeRecord(r.ctx)
			citation := af2ReplayCellCitation(t, record, "B3340", "warehouse.B")
			var selection string
			for ref, item := range r.engine.evidenceCompletion.refs {
				if content, ok := item.Raw.(string); ok && item.SourceID == "replay-supplemental-report" &&
					item.ChunkID == "replay-supplemental-page" && item.Kind == "body" && strings.Contains(content, "郑棉仓单：合计60.6万吨") {
					selection = ref
					break
				}
			}
			require.NotEmpty(t, selection)
			report := r.engine.modelContext.CitableChunkHandle("replay-supplemental-page")
			require.NotEmpty(t, report)
			// Offline edit of the successful A r3 wording. A model still has
			// to associate the report span with the supplemental role. The
			// recorded Af1 r2 candidate never read this report.
			draft := "棉花数据库.xlsx 的“郑棉仓单”表中，“仓单数量:一号棉花”在 2024-03-10 没有有效记录。\n\n" +
				"“仓单数量:一号棉花”最后有效日为 2023-09-21，B3340 原值 5160，日期格 A3340，原表单位未确认。" +
				`<ref id="` + r.engine.modelContext.CitableEvidenceHandle(citation) + `"/>` + "\n\n" +
				`独立来源《棉花周报20240310.pptx》第18页披露郑棉仓单合计60.6万吨。<ref id="` + report + `"/>`
			draft = r.withDeclaration(draft, map[string]any{"goal_index": 0, "selection_ref": selection, "role": "supplement",
				"page": 18, "metric": "郑棉仓单", "unit": "万吨", "value_text": "60.6",
				"span": "郑棉仓单：合计60.6万吨"})
			r.run(chatAnswer(draft))
			if !r.state.IsComplete {
				require.True(t, r.guards.structuredAnswerReviewed, "the existing draft review may request one checked rewrite")
				require.Empty(t, r.guards.pendingRuntimeNotice, "neither of the successfully read sources needs another read")
				r.run(chatAnswer(draft))
			}
			af2ReplayComplete(t, r.engine, r.ctx, r.state, "s2c")
			answer := r.answer()
			require.Contains(t, answer, "2024-03-10 没有有效记录")
			require.Contains(t, answer, "2023-09-21，B3340 原值 5160")
			require.Contains(t, answer, "独立来源《棉花周报20240310.pptx》第18页披露郑棉仓单合计60.6万吨")
			require.NotContains(t, answer, "20.784")
		})
	}
}

func af2ReplayCellCitation(t *testing.T, record coverageRecord, cell, series string) string {
	t.Helper()
	for _, item := range record.Evidence {
		if item.Cell == cell && item.SeriesID == series && item.CitationID != "" {
			return item.CitationID
		}
	}
	t.Fatalf("no native citation for %s/%s", series, cell)
	return ""
}

func af2ReplayComplete(t *testing.T, engine *AgentEngine, ctx context.Context, state *types.AgentState, session string) {
	t.Helper()
	require.True(t, state.IsComplete)
	require.NotEmpty(t, state.FinalAnswer)
	var completion event.AgentCompleteData
	engine.eventBus.On(event.EventAgentComplete, func(_ context.Context, emitted event.Event) error {
		completion = emitted.Data.(event.AgentCompleteData)
		return nil
	})
	engine.emitCompletionEvent(ctx, state, session, "turn", time.Now())
	require.Nil(t, completion.Incomplete)
	require.Equal(t, state.FinalAnswer, completion.FinalAnswer)
}
