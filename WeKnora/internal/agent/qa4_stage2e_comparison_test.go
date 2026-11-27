package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestQA4Stage2ETableComparisonHistory(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "false")
	t.Run("s2-1 actual same-table reads", func(t *testing.T) {
		r := newQA4Stage2CReplay(t, "s2-1-kb28", true, false)
		r.through(33)
		require.Contains(t, r.results[23].Result.Output, "298.93291576470585")
		require.Contains(t, r.results[33].Result.Output, "331.2")
		require.Contains(t, r.trace.HistoricalFinal, "两者给出的数并不相同")
		// This older SSE stores the final text and tool results, with
		// body/image evidence projected from the stored source. The typed
		// repair cases below exercise actual final publication separately.
		require.Contains(t, r.trace.HistoricalFinal, "298.93")
	})
	t.Run("s2-2 actual evidence and historical incomplete delivery", func(t *testing.T) {
		r := newQA4Stage2CReplay(t, "s2-2-kb28", true, false)
		r.through(30)
		require.Contains(t, r.results[26].Result.Output, "298.93291576470585")
		require.Contains(t, r.results[30].Result.Output, "331.2")
		require.NotContains(t, r.trace.HistoricalFinal, "331.2")
	})
	t.Run("s2-3 missing", func(t *testing.T) {
		t.Skip("The historical s2-3 run did not include KB-28. No substitute historical output.")
	})
	for _, mode := range []string{"default", "false", "recovery", "revoked", "canonical revoked", "only", "supporting", "wrong verdict", "no verdict"} {
		t.Run("s2-4 "+mode, func(t *testing.T) {
			repair := mode != "default" && mode != "false"
			var extensions []qa4Stage2Call
			if mode == "supporting" {
				// Independently queried in the original import after the
				// historical run. No component beyond G was read in s2-4.
				// The transport identity is restored from #21 after matching
				// the dataset ID and source SHA of the native offline result.
				raw, err := os.ReadFile("testdata/qa4/replay/comparison-components-native-sql.json")
				require.NoError(t, err)
				var read qa4Stage2Call
				require.NoError(t, json.Unmarshal(raw, &read))
				extensions = append(extensions, read)
			}
			r := newQA4Stage2CReplay(t, "s2-4-kb28", repair, false, extensions...)
			r.trace.Calls = append(r.trace.Calls, extensions...)
			if mode == "default" {
				require.NoError(t, os.Unsetenv("WEKNORA_AGENT_EVIDENCE_COMPLETION"))
			}
			require.Nil(t, r.engine.taskPlan, "historical plan failed validation")
			if mode == "only" {
				// A separate repair variant with a genuine explicit restriction.
				r.trace.Question = "只用这两份材料回答。" + r.trace.Question
				r.messages[len(r.messages)-1].Content = r.trace.Question
				r.engine.turnReview.Query = r.trace.Question
				r.engine.reviewConversation = conversationThroughLastHuman(r.messages)
				r.engine.turnReview.Conversation = r.engine.reviewConversation
				for _, doc := range r.trace.Documents {
					r.engine.selectedDocs = append(r.engine.selectedDocs, &SelectedDocumentInfo{KnowledgeID: doc.ID, KnowledgeBaseID: qa4Stage2Library, FileName: doc.FileName, Title: doc.Title})
				}
			}
			r.through(21)
			if repair {
				// The logged source-progress recovery selected the wrong page
				// first. Replay its existing bounded recovery separately.
				r.engine.beginEvidenceCompletionRecovery(r.ctx, r.state)
			}
			r.through(33)
			report := r.results[33].Result
			require.True(t, report.Success, report.Error)
			require.Contains(t, report.Output, "876")
			require.NotContains(t, report.Output, "331.2")
			draft := qa4Stage2EComparisonDraft(t, r, 21)
			if repair {
				qa4Stage2ECompareRecover(t, r, draft, mode, extensions)
			} else {
				r.run(chatAnswer(draft))
			}
			r.exhaust()
			answer := r.answer()
			require.Contains(t, answer, "298.93")
			require.Contains(t, answer, "G87")
			if repair {
				require.NotContains(t, documentNumericProse(answer), "876")
			} else {
				require.Nil(t, r.engine.taskPlan)
				require.Empty(t, r.engine.evidenceCompletionTargets())
				require.Empty(t, r.engine.currentCoverageDraftReview(r.ctx, r.state).comparisons)
				require.Contains(t, documentNumericProse(answer), "876", "the original model paired different tables; the same-table result was never read")
			}
			require.NotContains(t, answer, "<task_coverage>")
			if repair {
				require.Contains(t, answer, "331.2")
				require.Contains(t, answer, "第6页")
				require.Equal(t, 1, strings.Count(answer, "数字不同"))
				require.NotContains(t, answer, "这两个数字相同")
				require.Len(t, uniqueCandidateCitationIDs(answer), 1)
				wantEvidence := 1
				if mode == "supporting" {
					wantEvidence = 5
					for _, number := range []string{"217.2", "116.0", "28.7", "25.1", "231.9329", "**110.0**", "**10.0**", "**17.0**", "材料日期 2024-03-31"} {
						require.Contains(t, answer, number)
					}
					require.NotContains(t, answer, "**70.0**", "the model did not request the extra same-row field")
				}
				require.Len(t, uniqueCandidateEvidenceIDs(answer), wantEvidence)
			} else {
				require.NotContains(t, answer, "331.2")
			}
			var completed event.AgentCompleteData
			r.engine.eventBus.On(event.EventAgentComplete, func(_ context.Context, item event.Event) error {
				completed = item.Data.(event.AgentCompleteData)
				return nil
			})
			revoked := mode == "revoked" || mode == "canonical revoked"
			if revoked {
				r.engine.config.SearchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "unrelated-library", TenantID: 1}}
			}
			r.engine.emitCompletionEvent(r.ctx, r.state, "s2e", "turn", time.Now())
			if revoked {
				require.NotContains(t, completed.FinalAnswer, "331.2")
				require.NotContains(t, completed.FinalAnswer, "298.93")
				require.Empty(t, completed.KnowledgeRefs)
				require.Empty(t, completed.EvidenceRefs)
				require.True(t, r.finals[len(r.finals)-1].Replace)
				require.Equal(t, completed.FinalAnswer, r.finals[len(r.finals)-1].Content)
				return
			}
			require.Equal(t, answer, completed.FinalAnswer)
			if repair {
				require.Len(t, completed.KnowledgeRefs, 1)
				require.Len(t, completed.EvidenceRefs, len(uniqueCandidateEvidenceIDs(answer)))
			}
		})
	}
}

func qa4Stage2EComparisonDraft(t *testing.T, r *qa4Stage2CReplay, event int) string {
	t.Helper()
	draft := r.draft(len(r.trace.Drafts)-1, event)
	for _, chunk := range r.trace.Chunks {
		tag := fmt.Sprintf(`<kb doc="%s" chunk_id="%s" kb_id="%s" />`, r.trace.Documents[1].Title, chunk.ID, qa4Stage2Library)
		if strings.Contains(draft, tag) {
			handle := r.engine.modelContext.CitableChunkHandle(chunk.ID)
			require.NotEmpty(t, handle)
			draft = strings.ReplaceAll(draft, tag, fmt.Sprintf(`<ref id="%s"/>`, handle))
		}
	}
	return draft
}

func qa4Stage2ECompareRecover(t *testing.T, r *qa4Stage2CReplay, historicalDraft, mode string, extensions []qa4Stage2Call) {
	t.Helper()
	// These are new model selection and response fixtures. The historical
	// draft above used another table and actually lacked the requested cell.
	raw, err := os.ReadFile("testdata/qa4/replay/s2-2-kb28.json")
	require.NoError(t, err)
	var earlier qa4Stage2CTrace
	require.NoError(t, json.Unmarshal(raw, &earlier))
	for _, row := range earlier.Chunks {
		row.TenantID, row.KnowledgeBaseID, row.IsEnabled = 1, qa4Stage2Library, true
		r.repo.rows = append(r.repo.rows, row)
	}
	const table = "中国棉花平衡表"
	const metric = "工商业期末库存"
	report, book := "3月31日周报", "棉花数据库"
	if mode == "only" {
		book = r.engine.modelContext.RegisterDocument(r.trace.Documents[0].ID)
		report = r.engine.modelContext.RegisterDocument(r.trace.Documents[1].ID)
	}
	gap := r.engine.evidenceCompletion.legacyGap
	goal := map[string]any{"goal_index": 0, "request_basis": r.trace.Question, "source_mode": "open",
		"version_task": "compare_sources", "table": table, "comparison_sources": []string{report, book},
		"metric": metric, "source_time": "observed_period", "required": []string{"scoped_value"},
		"period": map[string]string{"grain": "month", "from": "2023-10", "to": "2023-10"}}
	if mode == "only" {
		goal["source_mode"], goal["restriction_basis"] = "only", "只用这两份材料回答。"
	}
	selectRef := func(match func(coverageEvidence) bool) string {
		for ref, obs := range r.engine.evidenceCompletion.refs {
			if match(obs) {
				return ref
			}
		}
		t.Fatal("matching current observation is absent")
		return ""
	}
	bookRef := selectRef(func(item coverageEvidence) bool {
		return item.Kind == "cell" && item.Sheet == table && item.Cell == "G87"
	})
	annualRef := selectRef(func(item coverageEvidence) bool { return item.Kind == "body" && item.Page == 28 })
	selection := func(side, ref string) map[string]any {
		return map[string]any{"goal_index": 0, "gap_id": gap, "role": "primary", "source_ref": side, "selection_ref": ref}
	}
	bookChoice := selection(book, bookRef)
	bookChoice["goal"] = goal
	bookChoice["grain"], bookChoice["period_from"], bookChoice["period_to"] = "month", "2023-10-01", "2023-10-31"
	announce, err := json.Marshal([]any{bookChoice, selection(report, annualRef)})
	require.NoError(t, err)

	r.run(chatAnswer(historicalDraft + coverageAnswerTag + string(announce) + coverageAnswerClose))
	require.False(t, r.state.IsComplete)
	if mode != "only" {
		var recalled *types.ToolCall
		for step := len(r.state.RoundSteps) - 1; step >= 0 && recalled == nil; step-- {
			for _, call := range r.state.RoundSteps[step].ToolCalls {
				if call.ExecutionName() == agenttools.ToolReadDocument && call.ExecutionArgs()["regex"] == true {
					recalled = &call
					break
				}
			}
		}
		require.NotNil(t, recalled, "one bounded native lookup used the selected report")
		require.True(t, recalled.Result.Success)
		require.Equal(t, "replay-comparison-report", recalled.ExecutionArgs()["id"])
		for _, term := range []string{table, metric, "2023-10-31"} {
			require.Contains(t, recalled.ExecutionArgs()["query"], term)
		}
		require.Contains(t, recalled.Result.Output, "# 第6页")
		require.NotContains(t, recalled.Result.Output, "# 第28页")
	} else {
		require.Len(t, r.engine.evidenceCompletionTargets(), 1, "the comparison scope uses both selected document handles")
		require.Equal(t, "only", r.engine.evidenceCompletionTargets()[0].SourceMode)
	}
	// The replay model chooses the page actually exposed by the lookup.
	readArgs := map[string]any{"id": "replay-comparison-report", "page": 6}
	if len(extensions) != 0 {
		readArgs["source_scope"] = map[string]any{
			"selection_reason": "Select the same named report and verify its material date from the original file name.",
			"sources": []any{map[string]any{"knowledge_id": "replay-comparison-report", "expected_file_name": "棉花周报20240331.pptx",
				"date_evidence": map[string]string{"date": "2024-03-31", "source_field": "file_name", "source_text": "20240331"}}},
		}
	}
	r.calls([]qa4Stage2Call{{Event: 101, Tool: agenttools.ToolReadDocument, Arguments: readArgs}})
	require.Contains(t, r.results[101].Result.Output, "331.2")
	if mode == "only" {
		r.calls([]qa4Stage2Call{{Event: 103, Tool: agenttools.ToolReadDocument, Arguments: map[string]any{"id": "same-library-third-source", "page": 6}}})
		require.True(t, r.results[103].Result.PreflightRejected)
		require.Contains(t, r.results[103].Result.Error, coverageScopeRejection)
		// The same verified restriction still applies with the legacy gap
		// controller in shadow, as does comparison of the two current sides.
		r.engine.evidenceCompletion.enabled = false
	}
	receipt := selectRef(func(item coverageEvidence) bool { return item.Kind == "body" && item.Page == 6 && item.Object != "" })
	asOf := selection(report, receipt)
	asOf["date"], asOf["metric"] = "2023-10", metric
	declarations := []any{asOf}
	if len(extensions) != 0 {
		r.calls(extensions)
		fields := []string{"工商业期初库存", "产量", "进口", "公开抛储"}
		for i, field := range fields {
			reportDetail := selection(report, receipt)
			reportDetail["role"], reportDetail["metric"], reportDetail["date"] = "supporting", field, "2023-10"
			cell := []string{"B87", "C87", "D87", "E87"}[i]
			bookDetail := selection(book, selectRef(func(item coverageEvidence) bool {
				return item.Kind == "cell" && item.Cell == cell && item.Operation == "analyze_table"
			}))
			bookDetail["role"] = "supporting"
			declarations = append(declarations, reportDetail, bookDetail)
		}
	}
	wire, err := json.Marshal(declarations)
	require.NoError(t, err)
	reportItem := r.engine.evidenceCompletion.refs[receipt]
	bookItem := r.engine.evidenceCompletion.refs[bookRef]
	known := func(item coverageEvidence) string {
		handle := r.engine.modelContext.CitableEvidenceHandle(item.CitationID)
		if item.Kind == "body" {
			handle = r.engine.modelContext.CitableChunkHandle(item.ChunkID)
		}
		require.NotEmpty(t, handle, item.Metric)
		return fmt.Sprintf(`<ref id="%s"/>`, handle)
	}
	const verdict = "两份材料所列数字不同。"
	prose := "《棉花周报20240331.pptx》第6页的中国棉花平衡表显示，2023-10 工商业期末库存为 **331.2 万吨**" + known(reportItem) + "。\n\n" +
		"棉花数据库的中国棉花平衡表同月工商业期末库存则是 **298.93 万吨**（G87）" + known(bookItem) + "。"
	if len(extensions) != 0 {
		prose += "\n\n周报同一行的工商业期初库存、产量、进口和公开抛储依次为 217.2、116.0、28.7 和 25.1 万吨" + known(reportItem) + "。材料日期 2024-03-31。"
		for _, component := range []struct{ metric, cell, value string }{
			{"工商业期初库存", "B87", "231.9329"},
			{"产量", "C87", "**110.0**"},
			{"进口", "D87", "**10.0**"},
			{"公开抛储", "E87", "**17.0**"},
		} {
			item := r.engine.evidenceCompletion.refs[selectRef(func(item coverageEvidence) bool {
				return item.Kind == "cell" && item.Cell == component.cell && item.Operation == "analyze_table"
			})]
			prose += "\n\n数据库同一行的" + component.metric + "为 " + component.value + " 万吨（" + component.cell + "）" + known(item) + "。"
		}
	}
	candidate := verdict + "\n\n" + prose
	if mode == "wrong verdict" || mode == "no verdict" {
		bad := "这两个数字相同。"
		if mode == "no verdict" {
			bad = "以下列出两份来源。"
		}
		candidate = bad + "\n\n" + prose
		wire, err := json.Marshal(map[string]any{"action": "correct", "edits": []map[string]string{{"old": bad, "new": verdict}}})
		require.NoError(t, err)
		r.model.review = chatAnswer(string(wire))
	}
	if mode == "canonical revoked" {
		initial := r.engine.acceptCoverageAnswer(r.ctx, r.trace.Question, r.state, r.messages, candidate+coverageAnswerTag+string(wire)+coverageAnswerClose, true)
		decoded := r.engine.modelContext.DecodeOutputText(initial)
		decision := r.engine.checkCandidate(r.ctx, r.trace.Question, decoded, "", r.state, r.messages, false)
		require.Equal(t, candidateAccepted, decision.Disposition)
		require.Equal(t, decoded, decision.Candidate)
		const streamID = "existing-comparison-stream"
		require.NoError(t, r.engine.eventBus.Emit(r.ctx, event.Event{ID: streamID, Type: event.EventAgentFinalAnswer, SessionID: "s2e",
			Data: event.AgentFinalAnswerData{Content: decoded}}))
		r.engine.commitTerminalResult(r.ctx, r.state, "s2e", decoded, streamID, "stalled", false)
		require.Equal(t, streamID, r.engine.turnReview.TerminalStreamID)
		require.Equal(t, decoded, r.state.FinalAnswer)
		return
	}
	reviews := r.model.reviewCalls

	r.run(chatAnswer(candidate + coverageAnswerTag + string(wire) + coverageAnswerClose))
	if mode == "wrong verdict" || mode == "no verdict" {
		require.Equal(t, reviews+1, r.model.reviewCalls, "use the existing structured-result editor")
		require.Contains(t, r.model.reviewInput[1].Content, "checked_same_table_comparisons")
		require.Contains(t, r.model.reviewInput[1].Content, `"reported_numbers":"different"`)
	}
	if r.state.IsComplete {
		require.Equal(t, r.engine.modelContext.DecodeOutputText(verdict+"\n\n"+prose), r.state.FinalAnswer)
	}

	require.True(t, r.engine.evidenceCompletion.result[0].State == coverageSatisfied)
	require.False(t, r.engine.coverageComparisonSourceMatches(0, book, coverageEvidence{
		KnowledgeBaseID: qa4Stage2Library, SourceID: "same-library-third-source", KnowledgeID: "same-library-third-source", SourceDomain: "structured",
	}), "authorization alone cannot swap the original source named for that side")
	require.False(t, r.engine.coverageComparisonSourceMatches(0, report, r.engine.evidenceCompletion.refs[bookRef]),
		"the same earlier authorized source cannot occupy the other side")

	target := r.engine.evidenceCompletionTargets()[0]
	record := r.engine.evidenceCompletion.record
	for name, change := range map[string]func(*coverageRecord){
		"another sheet": func(record *coverageRecord) {
			for i := range record.Evidence {
				if record.Evidence[i].Kind == "cell" {
					record.Evidence[i].Sheet = "another table"
				}
			}
		},
		"only navigation": func(record *coverageRecord) {
			for i := range record.Evidence {
				if record.Evidence[i].Object != "" {
					record.Evidence[i].Kind = "navigation"
				}
			}
		},
		"another month": func(record *coverageRecord) {
			for i := range record.Bindings {
				if record.Bindings[i].Object != "" {
					record.Bindings[i].Date = "2023-11"
				}
			}
		},
		"another metric on both sides": func(record *coverageRecord) {
			goal := *target.StructuredTaskGoal
			goal.Metric = "unrelated field"
			target.StructuredTaskGoal = &goal
		},
	} {
		t.Run(name, func(t *testing.T) {
			before := target
			defer func() { target = before }()
			copy := coverageRecord{Revision: record.Revision, AuthScope: record.AuthScope, Snapshots: record.Snapshots,
				Evidence: append([]coverageEvidence(nil), record.Evidence...), Bindings: append([]coverageBinding(nil), record.Bindings...)}
			change(&copy)
			require.NotEqual(t, coverageSatisfied, evaluateTableComparison(target, copy).State)
		})
	}
}

func TestTableComparisonRequiresTheSelectedSection(t *testing.T) {
	const page = "# Requested table\n\n| Month | Result |\n| --- | --- |\n| 2025-01 | 25 |\n\n![requested](resource://requested)\n\n# Other table\n\n| Month | Result |\n| --- | --- |\n| 2025-02 | 55 |\n\n![other](resource://other)"
	section := comparisonTableSection(page, "Requested table", "")
	_, ok := comparisonTableIntersection(section, "Result", "2025-02", false)
	require.False(t, ok, "the adjacent table cannot supply the requested table's row")
	value, ok := comparisonTableIntersection(section, "Result", "2025-01", false)
	require.True(t, ok)
	require.Equal(t, "25", value)
	require.Empty(t, comparisonTableSection(page, "Requested table", "resource://other"))
	require.NotEmpty(t, comparisonTableSection(page, "Requested table", "resource://requested"))
}

func TestTableComparisonAsReportedMonthUsesTheOriginalRow(t *testing.T) {
	const start, end = "2025-02-01", "2025-02-28"
	target := taskCoverageTarget{StructuredTaskGoal: &StructuredTaskGoal{
		Table: "Operations", Metric: "End level", ComparisonSources: []string{"North ledger", "South ledger"},
		VersionTask: "compare_sources", Period: map[string]any{"grain": "month", "from": start, "to": end},
	}}
	entry := coverageEvidence{Kind: "cell", Operation: "query_series", SnapshotHash: "snap", Sheet: target.Table, Metric: target.Metric,
		Grain: "day", PeriodFrom: end, PeriodTo: end, Date: end, DateCell: "A15", Cell: "B15", Raw: json.Number("25")}
	binding := coverageBinding{Grain: "month", PeriodFrom: start, PeriodTo: end}
	_, ok := tableComparisonFact(target, coverageRecord{}, binding, entry, start, end, "month")
	require.False(t, ok, "a declared daily row cannot become a monthly value")
	entry.GrainFromLookup = true
	fact, ok := tableComparisonFact(target, coverageRecord{}, binding, entry, start, end, "month")
	require.True(t, ok)
	require.Equal(t, end, fact.Date, "as-reported month-end label retains its original date")
	_, ok = tableComparisonFact(target, coverageRecord{}, coverageBinding{}, entry, start, end, "month")
	require.False(t, ok, "the month-end mapping needs an explicit association")
	entry.Grain, entry.Date, entry.GrainFromLookup = "month", start, false
	fact, ok = tableComparisonFact(target, coverageRecord{}, coverageBinding{}, entry, start, end, "month")
	require.True(t, ok)
	require.Equal(t, start, fact.Date, "an actual monthly field can use a month-start label")
	target.Delivery = map[string]any{"aggregation": "mean"}
	require.False(t, tableComparison(target), "aggregation still follows the ordinary verified calculation path")
}

func TestEvidenceCompletionDoesNotAddTypedProtocolToConceptTurns(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "true")
	e := newTestEngine(t, &mockChat{})
	e.config.AnswerReviewMode, e.config.WorkbenchDefault = "standard", true
	e.toolRegistry = qa4ReplayRegistry()
	e.taskPlan = &taskEvidencePlan{CoverageTargets: []taskCoverageTarget{{StructuredTaskGoal: &StructuredTaskGoal{SourceTime: "general_concept"}}}}
	ctx := t.Context()
	state := &types.AgentState{}
	e.refreshEvidenceCompletion(ctx, "How does a comparison work?", state, nil)
	tools := e.buildToolsForLLM()
	require.NotEmpty(t, tools)
	require.Empty(t, e.evidenceCompletionNotice())
	require.Equal(t, tools, e.evidenceCompletionTools(tools))
}

func TestTerminalExhaustedSemanticRecoveryKeepsIndependentRows(t *testing.T) {
	for _, enabled := range []string{"true", "false"} {
		t.Run(enabled, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", enabled)
			model := &mockChat{}
			e := newTestEngine(t, model)
			e.config.AnswerReviewMode, e.config.WorkbenchDefault = "standard", true
			e.toolRegistry = qa4ReplayRegistry()
			e.turnReview.Query = "Explain the findings in these records."
			const date = "2021-06-03"
			call := terminalLatestSeriesCall()
			call.Args["operation"], call.Args["start_date"], call.Args["end_date"] = "rows", date, date
			data := call.Result.Data
			filters := data["filters"].(map[string]any)
			filters["operation"], filters["start_date"], filters["end_date"] = "rows", date, date
			data["rows"] = append(data["rows"].([]any), map[string]any{
				"date": date, "date_cell": "A16", "source_cell": "B16", "value": 48.5, "value_status": "valid",
			})
			data["rows_are"], data["record_count"], data["scope_record_count"], data["valid_count"] = "observations", 2, 2, 2
			state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
			ctx := context.WithValue(t.Context(), types.LanguageContextKey, "en-US")
			e.refreshEvidenceCompletion(ctx, e.turnReview.Query, state, nil)
			e.beginEvidenceCompletionRecovery(ctx, state)
			e.stopEvidenceCompletion(ctx, e.turnReview.Query, state, nil, "recovery_budget_exhausted")
			e.commitTerminalResult(ctx, state, "terminal", "Unsupported relationship", "", "failed", false, terminalBudget)
			require.Equal(t, terminalBudget, e.turnReview.TerminalReceipt.failure)
			require.True(t, state.IsComplete)
			require.Contains(t, state.FinalAnswer, date)
			require.Regexp(t, `47\.25[^.;]*B15`, state.FinalAnswer)
			require.Regexp(t, `48\.5[^.;]*B16`, state.FinalAnswer)
			require.Contains(t, state.FinalAnswer, "multiple records for the same field and date")
			require.Contains(t, state.FinalAnswer, "A single value has not been established.")
			require.NotContains(t, state.FinalAnswer, "Unsupported relationship")
			require.NotContains(t, state.FinalAnswer, "comparison")
			require.Zero(t, model.callCount)
		})
	}
}
