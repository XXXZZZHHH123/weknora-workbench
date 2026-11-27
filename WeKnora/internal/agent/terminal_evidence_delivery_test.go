package agent

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestTerminalEvidenceCompletionFallback(t *testing.T) {
	for _, test := range []struct {
		name       string
		alter      func(*AgentEngine, *types.AgentState)
		submission string
		want       []string
		omit       []string
	}{
		{name: "native facts without declarations", want: []string{
			"Elapsed time 在 2021-07-02 无有效记录", "Equipment.xlsx", "Operating hours", "2021-06-03", "47.25", "A15", "B15", "单位未确认",
		}, omit: []string{"2021-01-01", "Unrelated field", "其余要求"}},
		{name: "normal delivery does not attach an unlinked field", submission: "Readings in scope were reviewed.", want: []string{"Readings in scope were reviewed."}, omit: []string{"2021-01-01", "Unrelated field", "47.25"}},
		{name: "normal delivery preserves a checked field", submission: "Equipment.xlsx, Operating hours. Elapsed time had no valid records on 2021-07-02.\n\nThe requested day remains uncovered.",
			want: []string{"\n\nThe requested day remains uncovered."}, omit: []string{"2021-06-03", "47.25", "B15", "Unrelated field"}},
		{name: "legacy switch preserves the normal candidate", submission: "Equipment.xlsx, Operating hours. Elapsed time had no valid records on 2021-07-02.\n\nThe requested day remains uncovered.",
			alter: func(e *AgentEngine, _ *types.AgentState) { e.evidenceCompletion.enabled = false },
			want:  []string{"\n\nThe requested day remains uncovered."}, omit: []string{"2021-06-03", "47.25", "B15"}},
		{name: "scope narrowed", alter: func(e *AgentEngine, _ *types.AgentState) {
			e.config.SearchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "different-library"}}
		}, want: []string{"当前范围内没有可用于所问结果的已核实记录"}, omit: []string{"Equipment.xlsx", "47.25"}},
		{name: "new human task", alter: func(e *AgentEngine, state *types.AgentState) {
			e.reviewConversation = append(e.reviewConversation, chat.Message{Role: "user", Content: "Use a different file for this question."})
			e.resetDocumentTurnProgress(&loopGuards{}, e.turnReview.Query, state)
		}, want: []string{"没有取得可核实的来源回执"}, omit: []string{"Equipment.xlsx", "47.25"}},
		{name: "source snapshot expires", alter: func(_ *AgentEngine, state *types.AgentState) {
			catalogue := terminalLatestSeriesCall()
			catalogue.ID = "newer-read"
			catalogue.Result.Data["source"].(map[string]any)["sha256"] = strings.Repeat("b", 64)
			catalogue.Result.Data["rows"].([]any)[0].(map[string]any)["value"] = 48.5
			state.RoundSteps[0].ToolCalls = append(state.RoundSteps[0].ToolCalls, catalogue)
		}, want: []string{"Equipment.xlsx", "48.5"}, omit: []string{"47.25", "2021-07-02"}},
		{name: "only excludes unrelated workbook", alter: func(e *AgentEngine, state *types.AgentState) {
			e.taskPlan = &taskEvidencePlan{CoverageTargets: []taskCoverageTarget{{StructuredTaskGoal: &StructuredTaskGoal{SourceMode: "only"}}}}
			e.evidenceCompletion = newEvidenceCompletionTurn(0)
			e.refreshEvidenceCompletion(t.Context(), e.turnReview.Query, state, e.reviewConversation)
			e.evidenceCompletion.record.Bindings = []coverageBinding{{Role: "primary", SourceID: "other-register", KnowledgeBaseID: "allowed-library"}}
		}, want: []string{"当前范围内没有可用于所问结果的已核实记录"}, omit: []string{"Equipment.xlsx", "47.25"}},
		{name: "current directory only", alter: func(e *AgentEngine, state *types.AgentState) {
			state.RoundSteps[0].ToolCalls = state.RoundSteps[0].ToolCalls[:1]
		}, want: []string{"Equipment.xlsx", "Operating hours", "目录条目", "尚无所问范围的读数"}, omit: []string{"B15", "47.25", "没有有效观测值"}},
		{name: "verified gap ignores an incomplete earlier period", alter: func(_ *AgentEngine, state *types.AgentState) {
			call := state.RoundSteps[0].ToolCalls[1]
			call.Args["operation"], call.Args["start_date"], call.Args["end_date"] = "rows", "2021-06-01", "2021-06-30"
			filters := call.Result.Data["filters"].(map[string]any)
			filters["operation"], filters["start_date"], filters["end_date"] = "rows", "2021-06-01", "2021-06-30"
			call.Result.Data["rows_are"] = "observations"
			rows := call.Result.Data["rows"].([]any)
			for i := 16; i <= 20; i++ {
				cell := strconv.Itoa(i)
				rows = append(rows, map[string]any{"date": "2021-06-03", "date_cell": "A" + cell, "source_cell": "B" + cell, "value": i, "value_status": "valid"})
			}
			call.Result.Data["rows"] = rows
		}, want: []string{"无有效记录", "2021-07-02", "最后有效日为 2021-06-03", "尚缺该日原值"}, omit: []string{"2021-01-01", "Unrelated field", "47.25", "B19", "B20"}},
		{name: "only rejected source calls", alter: func(e *AgentEngine, state *types.AgentState) {
			state.RoundSteps[0].ToolCalls = state.RoundSteps[0].ToolCalls[1:2]
			state.RoundSteps[0].ToolCalls[0].Result = &types.ToolResult{PreflightRejected: true, Error: "ineligible optional selection", Data: map[string]any{"execution_started": false}}
		}, want: []string{"在执行前停止", "未读取来源内容"}, omit: []string{"ineligible optional selection", "47.25"}},
		{name: "legacy switch", alter: func(e *AgentEngine, _ *types.AgentState) {
			e.evidenceCompletion.enabled = false
		}, want: []string{"2021-07-02", "47.25", "B15"}, omit: []string{answerReviewUnavailableFallback}},
		{name: "exact format", alter: func(e *AgentEngine, _ *types.AgentState) {
			e.taskPlan = &taskEvidencePlan{DeliveryRequirements: []taskDeliveryRequirement{{Kind: "text", Format: "exact"}}}
		}, want: []string{"2021-07-02", "47.25", "B15"}, omit: []string{answerReviewUnavailableFallback}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "true")
			e := newTestEngine(t, &mockChat{})
			e.config.AnswerReviewMode, e.config.WorkbenchDefault = "standard", true
			e.toolRegistry = agenttools.NewToolRegistry()
			base := agenttools.NewStructuredDataTool()
			e.toolRegistry.RegisterTool(base)
			for _, tool := range agenttools.NewStructuredReadOperationTools(base, structuredCodecManifest()) {
				e.toolRegistry.RegisterTool(tool)
			}
			ctx := context.WithValue(t.Context(), types.LanguageContextKey, "zh-CN")
			e.turnReview.Query = "What was recorded for the requested day?"
			e.reviewConversationReady = true
			e.reviewConversation = []chat.Message{{Role: "user", Content: e.turnReview.Query}}
			directory := types.ToolCall{ID: "catalogue", Name: "structured_list_datasets", Args: map[string]any{},
				Result: &types.ToolResult{Success: true, Data: map[string]any{
					"_structured_execution": map[string]any{"canonical_operation": "list_datasets", "effect_class": "read", "scope_mode": "directory"},
					"source_context":        map[string]any{"knowledge_base_id": "allowed-library"},
					"datasets":              []any{map[string]any{"dataset_id": "register", "source_file": "Equipment.xlsx", "sheets": []any{"Operating hours"}}},
					"datasets_truncated":    false,
				}}}
			latest, gap := terminalLatestSeriesCall(), terminalLatestSeriesCall()
			gap.ID = "exact-gap"
			gap.Args["start_date"], gap.Args["end_date"], gap.Args["operation"] = "2021-07-02", "2021-07-02", "rows"
			filters := gap.Result.Data["filters"].(map[string]any)
			filters["start_date"], filters["end_date"], filters["operation"] = "2021-07-02", "2021-07-02", "rows"
			gap.Result.Data["status"], gap.Result.Data["rows"], gap.Result.Data["valid_count"] = "no_data", []any{}, 0
			wider, unrelated := terminalLatestSeriesCall(), terminalLatestSeriesCall()
			for _, call := range []types.ToolCall{wider, unrelated} {
				call.Result.Data["status"], call.Result.Data["rows"], call.Result.Data["valid_count"] = "no_data", []any{}, 0
				call.Args["start_date"], call.Args["end_date"], call.Args["operation"] = "2021-01-01", "2021-12-31", "rows"
				filters := call.Result.Data["filters"].(map[string]any)
				filters["start_date"], filters["end_date"], filters["operation"] = call.Args["start_date"], call.Args["end_date"], "rows"
			}
			wider.ID, unrelated.ID = "wide-gap", "other-gap"
			unrelated.Args["series_id"] = "unrelated"
			unrelated.Result.Data["filters"].(map[string]any)["series_id"] = "unrelated"
			unrelated.Result.Data["series"].(map[string]any)["series_id"] = "unrelated"
			unrelated.Result.Data["series"].(map[string]any)["metric"] = "Unrelated field"
			state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{directory, latest, gap, wider, unrelated}}}}
			e.refreshEvidenceCompletion(ctx, e.turnReview.Query, state, e.reviewConversation)
			require.NotEmpty(t, e.evidenceCompletion.record.Evidence)
			require.Empty(t, e.coverageHandoffFacts())
			if test.alter != nil {
				test.alter(e, state)
			}
			recorder := &terminalAnswerRecorder{}
			recorder.attach(e.eventBus)
			submission, outcome := test.submission, "answer"
			if submission == "" {
				submission, outcome = answerReviewUnavailableFallback, "failed"
			}
			e.commitTerminalResult(ctx, state, "terminal", submission, "", outcome, false)
			e.commitTerminalResult(ctx, state, "terminal", "Duplicate candidate", "", "answer", false)
			require.True(t, state.IsComplete)
			done, streamed := recorder.snapshot()
			require.Equal(t, 1, done)
			require.Equal(t, state.FinalAnswer, streamed)
			if test.submission != "" {
				require.Equal(t, test.submission, state.FinalAnswer)
			}
			for _, value := range test.want {
				require.Contains(t, state.FinalAnswer, value)
			}
			for _, value := range test.omit {
				require.NotContains(t, state.FinalAnswer, value)
			}
		})
	}
}

func TestTerminalSQLFollowUpWithoutDraft(t *testing.T) {
	for _, language := range []string{"zh-CN", "en-US"} {
		t.Run(language, func(t *testing.T) {
			r := newQA4Stage2CReplay(t, "s2-3-kb16-bounded-latest", true, false)
			r.calls(r.trace.Calls)
			ctx := context.WithValue(r.ctx, types.LanguageContextKey, language)
			fallback := r.engine.terminalCoverageFailure(ctx, r.state, terminalBudget)
			for _, expected := range []string{"2023-09-22", "17110", "CF.CZC", "A4705", "B4705"} {
				require.Contains(t, fallback, expected)
			}
			require.Regexp(t, "无有效记录|no valid record", fallback)
			require.True(t, slices.ContainsFunc(strings.Split(fallback, "\n\n"), func(paragraph string) bool {
				return strings.Contains(paragraph, "17110") && strings.Contains(paragraph, "CF.CZC") &&
					strings.Contains(paragraph, "2023-09-22")
			}), fallback)
			require.Equal(t, 1, strings.Count(fallback, "17110"))
		})
	}
}

func terminalLatestSeriesCall() types.ToolCall {
	return types.ToolCall{ID: "latest-read", Name: "structured_query_series",
		Args: map[string]any{"dataset_id": "register", "series_id": "elapsed-time", "operation": "latest"},
		Result: &types.ToolResult{Success: true, Data: map[string]any{
			"_structured_execution": map[string]any{"canonical_operation": "query_series", "effect_class": "read", "scope_mode": "knowledge_base"},
			"status":                "ok", "source_context": map[string]any{"knowledge_base_id": "allowed-library"},
			"source":     map[string]any{"file": "Equipment.xlsx", "sheet": "Operating hours", "sha256": strings.Repeat("a", 64)},
			"dataset_id": "register", "truncated": false, "rows_are": "latest_valid_observation", "valid_last_date": "2021-06-03",
			"series":  map[string]any{"series_id": "elapsed-time", "metric": "Elapsed time", "column": "B", "unit_status": "needs_review", "last_valid_date": "2021-06-03"},
			"filters": map[string]any{"series_id": "elapsed-time", "operation": "latest", "value_filters": []any{}},
			"rows":    []any{map[string]any{"date": "2021-06-03", "date_cell": "A15", "source_cell": "B15", "value": 47.25, "value_status": "valid"}},
		}}}
}

func TestTerminalEvidenceCompletionDocumentReadStatus(t *testing.T) {
	for _, example := range []struct {
		name, selectedSpan string
		wantQuote          bool
	}{
		{"scope only", "", false},
		{"selected original passage", "实际该页的图例：类型甲、类型乙。", true},
		{"unmatched passage", "课件未载的 OCR 文本类型甲", false},
	} {
		t.Run(example.name, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "true")
			document := &types.Knowledge{ID: "actual-document", TenantID: 7, KnowledgeBaseID: "granted-base", Title: "Quarterly inspection",
				FileName: "quarterly.pptx", FileType: "pptx", ParseStatus: types.ParseStatusCompleted}
			targets := types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: document.KnowledgeBaseID, TenantID: document.TenantID}}
			e := newTestEngine(t, &mockChat{})
			e.config.AnswerReviewMode, e.config.WorkbenchDefault, e.config.SearchTargets = "standard", true, targets
			e.toolRegistry = agenttools.NewToolRegistry()
			read := agenttools.NewReadDocumentTool(&plannedDocumentKnowledge{document: document}, &plannedDocumentChunks{repo: &plannedDocumentRepo{}}, targets)
			e.toolRegistry.RegisterTool(read)
			const query = "Which labels were reported on the selected page?"
			agenttools.PrepareDocumentScope(e.toolRegistry, query)
			ctx := context.WithValue(t.Context(), types.LanguageContextKey, "zh-CN")
			ctx = types.WithCaller(ctx, types.Caller{TenantID: 7, UserID: "verified", Role: types.TenantRoleViewer})
			args := `{"id":"actual-document","page":4,"source_scope":{"selection_reason":"Read the selected inspection page.","sources":[{"knowledge_id":"actual-document","expected_file_name":"quarterly.pptx","page":4}]}}`
			result, err := read.Execute(ctx, []byte(args))
			require.NoError(t, err)
			require.True(t, result.Success, result.Error)
			require.True(t, agenttools.IsCurrentDocumentRead(e.toolRegistry, result))
			state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{{
				ID: "scoped-page", Name: agenttools.ToolReadDocument, Args: map[string]any{"id": document.ID, "page": 4}, Result: result,
			}}}}}
			e.turnReview.Query = query
			e.reviewConversation, e.reviewConversationReady = []chat.Message{{Role: "user", Content: query}}, true
			if example.selectedSpan != "" {
				e.taskPlan = &taskEvidencePlan{CoverageTargets: []taskCoverageTarget{{RequestBasis: query, StructuredTaskGoal: &StructuredTaskGoal{SourceMode: "open"}}}}
				e.refreshEvidenceCompletion(ctx, query, state, e.reviewConversation)
				require.Len(t, e.evidenceCompletion.record.Evidence, 1)
				read := e.evidenceCompletion.record.Evidence[0]
				e.evidenceCompletion.record.Bindings = []coverageBinding{{GoalIndex: 0, Role: "primary", Kind: "body",
					SourceID: read.SourceID, KnowledgeBaseID: read.KnowledgeBaseID, SnapshotHash: read.SnapshotHash,
					ChunkID: read.ChunkID, Page: read.Page, EvidenceID: read.EvidenceID, JSONPointer: read.JSONPointer, Span: example.selectedSpan}}
			}
			e.commitTerminalResult(ctx, state, "terminal", answerReviewUnavailableFallback, "", "failed", false)
			if example.wantQuote {
				require.Contains(t, state.FinalAnswer, "Quarterly inspection")
				require.Contains(t, state.FinalAnswer, "第 4 页")
				require.Contains(t, state.FinalAnswer, "实际该页的图例：类型甲、类型乙。")
			} else {
				require.Contains(t, state.FinalAnswer, "Quarterly inspection第 4 页")
				require.Contains(t, state.FinalAnswer, "返回的正文片段")
				require.Contains(t, state.FinalAnswer, "尚缺与所问范围对应的读数")
				require.NotContains(t, state.FinalAnswer, "课件未载的 OCR 文本")
			}
		})
	}
}

func TestTerminalSourceFailuresPublishVerifiedScopeOnce(t *testing.T) {
	for _, test := range []struct {
		name, candidate, stop string
		hasAttempt, blocked   bool
		enabled               string
		want                  string
	}{
		{"empty final", "", "budget", false, false, "true", "没有取得可核实的来源回执"},
		{"missing source", answerReviewUnavailableFallback, "", false, false, "true", "核验阶段未完成"},
		{"bad tool", incompleteAnswerFallback, "", true, false, "true", "数据查询尝试"},
		{"blocked scope", sourceProgressStalledFallback, "", true, true, "true", "数据查询在执行前停止"},
		{"progress stalled", stalledAnswerFallback, "", true, false, "true", "未返回可核实回执"},
		{"truncated answer", truncatedAnswerFallback, "budget", false, false, "true", "没有取得可核实的来源回执"},
		{"numeric fallback", numericFailureFallback, "", true, false, "true", "未返回可核实回执"},
		{"numeric final fallback", finalNumericFailureFallback, "", true, false, "true", "未返回可核实回执"},
		{"legacy tool decisions", sourceProgressStalledFallback, "", true, true, "false", "数据查询在执行前停止"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", test.enabled)
			e := newTestEngine(t, &mockChat{})
			e.config.AnswerReviewMode, e.config.WorkbenchDefault = "standard", true
			e.toolRegistry = qa4ReplayRegistry()
			ctx := context.WithValue(t.Context(), types.LanguageContextKey, "zh-CN")
			e.turnReview.Query = "What pressure was recorded on 2021-07-02?"
			e.taskPlan = &taskEvidencePlan{CoverageTargets: []taskCoverageTarget{{
				RequestBasis: e.turnReview.Query, StructuredTaskGoal: &StructuredTaskGoal{SourceMode: "open",
					Metric: "Pressure", Period: map[string]any{"grain": "day", "from": "2021-07-02", "to": "2021-07-02"}},
			}}}
			state := &types.AgentState{}
			if test.hasAttempt {
				attempt := terminalLatestSeriesCall()
				attempt.Args["start_date"], attempt.Args["end_date"] = "2021-07-02", "2021-07-02"
				attempt.Result = &types.ToolResult{Error: "private service detail", PreflightRejected: test.blocked}
				state.RoundSteps = []types.AgentStep{{ToolCalls: []types.ToolCall{attempt}}}
			}
			if test.stop != "" {
				e.evidenceCompletionEnabled()
				e.stopEvidenceCompletion(ctx, e.turnReview.Query, state, nil, test.stop)
			}
			recorder := &terminalAnswerRecorder{}
			recorder.attach(e.eventBus)
			id := e.publishAnswer(ctx, "terminal", test.candidate, "", false)
			require.Empty(t, id)
			e.commitTerminalResult(ctx, state, "terminal", test.candidate, id, "failed", true)
			e.commitTerminalResult(ctx, state, "terminal", "second candidate", "", "answer", false)
			count, streamed := recorder.snapshot()
			require.Equal(t, 1, count)
			require.Equal(t, state.FinalAnswer, streamed)
			require.True(t, state.IsComplete)
			require.False(t, terminalCandidateFallback(state.FinalAnswer))
			require.Contains(t, state.FinalAnswer, "Pressure 2021-07-02")
			require.Contains(t, state.FinalAnswer, test.want)
			require.NotContains(t, state.FinalAnswer, "private service detail")
			require.NotContains(t, state.FinalAnswer, "没有有效观测值")
		})
	}
}

func TestTerminalRequestedUnitUsesOnlyVerifiedInput(t *testing.T) {
	const date = "2021-06-03"
	for _, test := range []struct {
		name, request, basis, unit, sourceUnit, status, answer, want, omit string
	}{
		{"human unit", "How many hours were recorded on 2021-06-03?", "How many hours were recorded on 2021-06-03?", "hours",
			"", "needs_review", "", "unit unconfirmed", "47.25 hours"},
		{"confirmed unit", "How many hours were recorded on 2021-06-03?", "How many hours were recorded on 2021-06-03?", "hours",
			"hours", "explicit", "", "47.25 hours", "unit supplied in the question"},
		{"different source unit", "How many hours were recorded on 2021-06-03?", "How many hours were recorded on 2021-06-03?", "hours",
			"minutes", "explicit", "", "47.25 minutes", "47.25 hours"},
		{"mixed source units", "How many hours were recorded on 2021-06-03?", "How many hours were recorded on 2021-06-03?", "hours",
			"", "mixed", "", "unit unconfirmed", "47.25 hours"},
		{"unsupported human quotation", "What was recorded on 2021-06-03?", "How many hours were recorded on 2021-06-03?", "hours",
			"", "needs_review", "", "unit unconfirmed", "47.25 hours"},
		{"no requested unit", "What was recorded on 2021-06-03?", "What was recorded on 2021-06-03?", "",
			"", "needs_review", "", "unit unconfirmed", "47.25 hours"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "true")
			e := newTestEngine(t, &mockChat{})
			e.config.AnswerReviewMode, e.config.WorkbenchDefault = "standard", true
			e.toolRegistry = qa4ReplayRegistry()
			e.turnReview.Query = test.request
			e.taskPlan = &taskEvidencePlan{CoverageTargets: []taskCoverageTarget{{
				RequestBasis: test.basis, StructuredTaskGoal: &StructuredTaskGoal{SourceMode: "open", VersionTask: "single",
					Metric: "Elapsed time", RequestedUnit: test.unit, Period: map[string]any{"grain": "day", "from": date, "to": date},
					Delivery: map[string]any{"kind": "text", "aggregation": "as_reported"}},
			}}}
			call := terminalLatestSeriesCall()
			call.Args["operation"], call.Args["start_date"], call.Args["end_date"] = "rows", date, date
			data := call.Result.Data
			filters := data["filters"].(map[string]any)
			filters["operation"], filters["start_date"], filters["end_date"] = "rows", date, date
			series := data["series"].(map[string]any)
			series["unit"], series["unit_status"] = test.sourceUnit, test.status
			data["rows_are"], data["record_count"], data["scope_record_count"], data["valid_count"] = "observations", 1, 1, 1
			state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
			ctx := context.WithValue(t.Context(), types.LanguageContextKey, "en-US")
			e.refreshEvidenceCompletion(ctx, e.turnReview.Query, state, nil)
			var outcome = "answer"
			if test.answer == "" {
				outcome = "failed"
			} else if e.needsSourceConsistencyCheck(state) {
				e.turnReview.Last = candidateDecision{Disposition: candidateAccepted, Candidate: test.answer,
					CandidateHash: candidateTextDigest(test.answer), TaskHash: e.candidateTaskDigest(e.turnReview.Query, nil),
					EvidenceHash: reviewDigest(e.sourceConsistencyMessages(state, nil)), StandardReview: true}
			}
			e.commitTerminalResult(ctx, state, "terminal", test.answer, "", outcome, false)
			require.Contains(t, state.FinalAnswer, "47.25")
			require.Contains(t, state.FinalAnswer, test.want, state.FinalAnswer)
			require.NotContains(t, state.FinalAnswer, test.omit)
			require.Contains(t, state.FinalAnswer, "B15")
			require.Contains(t, state.FinalAnswer, date)
		})
	}
}

func TestTerminalNoDraftSourceComparison(t *testing.T) {
	const day = "2021-06-03"
	for _, test := range []struct {
		name, secondUnit, wantOpening string
		value                         float64
	}{
		{"different values", "hours", "两处来源在核查范围内的数值不同。", 32.5},
		{"equal values", "hours", "两处来源在核查范围内的数值相同。", 47.25},
		{"unknown unit", "", "两处来源的单位无法直接比较。", 32.5},
		{"unknown unit equal figures", "", "两处来源的单位无法直接比较。", 47.25},
		{"incompatible unit", "minutes", "两处来源的单位无法直接比较。", 32.5},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "true")
			e := newTestEngine(t, &mockChat{})
			e.config.AnswerReviewMode, e.config.WorkbenchDefault = "standard", true
			e.toolRegistry = qa4ReplayRegistry()
			ctx := context.WithValue(t.Context(), types.LanguageContextKey, "zh-CN")
			e.turnReview.Query = "Did both reports give the same elapsed time on 2021-06-03?"
			e.taskPlan = &taskEvidencePlan{CoverageTargets: []taskCoverageTarget{{
				RequestBasis: e.turnReview.Query, StructuredTaskGoal: &StructuredTaskGoal{SourceMode: "open", VersionTask: "compare_sources",
					Metric: "Elapsed time", Period: map[string]any{"grain": "day", "from": day, "to": day}, Required: []string{"scoped_value"}},
			}}}
			exact := func(id, title, unit string, value float64) types.ToolCall {
				call := terminalLatestSeriesCall()
				call.ID, call.Args["dataset_id"] = id, id
				call.Args["operation"], call.Args["start_date"], call.Args["end_date"] = "rows", day, day
				data := call.Result.Data
				data["dataset_id"] = id
				data["source"].(map[string]any)["file"] = title
				filters := data["filters"].(map[string]any)
				filters["operation"], filters["start_date"], filters["end_date"] = "rows", day, day
				data["series"].(map[string]any)["unit"] = unit
				if unit != "" {
					data["series"].(map[string]any)["unit_status"] = "explicit"
				}
				data["rows"].([]any)[0].(map[string]any)["value"] = value
				data["rows_are"], data["record_count"], data["scope_record_count"], data["valid_count"] = "observations", 1, 1, 1
				return call
			}
			state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{
				exact("register", "Equipment.xlsx", "hours", 47.25),
				exact("comparison", "Daily log.xlsx", test.secondUnit, test.value),
				exact("unrelated", "Other source.xlsx", "hours", 999.25),
			}}}}
			e.refreshEvidenceCompletion(ctx, e.turnReview.Query, state, nil)
			for _, item := range e.evidenceCompletion.record.Evidence {
				if item.Kind != "cell" || item.SourceID == "unrelated" {
					continue
				}
				e.evidenceCompletion.record.Bindings = append(e.evidenceCompletion.record.Bindings, coverageBinding{
					GoalIndex: 0, Role: "primary", Kind: "cell", SourceID: item.SourceID, KnowledgeBaseID: item.KnowledgeBaseID,
					SnapshotHash: item.SnapshotHash, Sheet: item.Sheet, SeriesID: item.SeriesID, Column: item.Column,
					PeriodFrom: day, PeriodTo: day, Grain: "day", EvidenceID: item.EvidenceID, JSONPointer: item.JSONPointer,
				})
			}
			e.commitTerminalResult(ctx, state, "terminal", answerReviewUnavailableFallback, "", "failed", false)
			require.True(t, strings.HasPrefix(state.FinalAnswer, test.wantOpening), state.FinalAnswer)
			for _, want := range []string{"Equipment.xlsx", "Daily log.xlsx", "47.25", strconv.FormatFloat(test.value, 'f', -1, 64), day, "B15"} {
				require.Contains(t, state.FinalAnswer, want)
			}
			require.NotContains(t, state.FinalAnswer, "999.25")
			require.NotContains(t, state.FinalAnswer, "Other source.xlsx")
		})
	}
}

func TestTerminalUnboundLookupDescribesExecutedScope(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "true")
	e := newTestEngine(t, &mockChat{})
	e.config.AnswerReviewMode, e.config.WorkbenchDefault = "standard", true
	e.toolRegistry = qa4ReplayRegistry()
	e.turnReview.Query = "What pressure did the calibration certificate record?"
	call := terminalLatestSeriesCall()
	call.Args["operation"], call.Args["start_date"], call.Args["end_date"] = "rows", "2021-06-03", "2021-06-03"
	filters := call.Result.Data["filters"].(map[string]any)
	filters["operation"], filters["start_date"], filters["end_date"] = "rows", "2021-06-03", "2021-06-03"
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
	ctx := context.WithValue(t.Context(), types.LanguageContextKey, "en-US")
	e.commitTerminalResult(ctx, state, "terminal", "", "", "failed", false)
	require.Contains(t, state.FinalAnswer, "Equipment.xlsx")
	require.Contains(t, state.FinalAnswer, "2021-06-03")
	require.Contains(t, state.FinalAnswer, "47.25")
}

func TestTerminalWithoutPlanOrReadCarriesHumanRequest(t *testing.T) {
	for _, flag := range []string{"true", "false"} {
		t.Run(flag, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", flag)
			e := newTestEngine(t, &mockChat{})
			e.config.AnswerReviewMode, e.config.WorkbenchDefault = "standard", true
			e.toolRegistry = qa4ReplayRegistry()
			e.turnReview.Query = "Check the tolerance on the signed calibration certificate."
			state := &types.AgentState{}
			recorder := &terminalAnswerRecorder{}
			recorder.attach(e.eventBus)
			ctx := context.WithValue(t.Context(), types.LanguageContextKey, "en-US")
			e.commitTerminalResult(ctx, state, "terminal", answerReviewUnavailableFallback, "", "failed", false)
			require.Contains(t, state.FinalAnswer, e.turnReview.Query)
			require.Contains(t, state.FinalAnswer, "No verifiable source receipt was obtained")
			require.NotContains(t, state.FinalAnswer, answerReviewUnavailableFallback)
			count, streamed := recorder.snapshot()
			require.Equal(t, 1, count)
			require.Equal(t, streamed, state.FinalAnswer)
		})
	}
}

func TestTerminalWarningsDoNotAppendUnrelatedFacts(t *testing.T) {
	for _, citation := range []bool{false, true} {
		t.Run(strconv.FormatBool(citation), func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "true")
			e := newTestEngine(t, &mockChat{}, withCitationsEnabled(true))
			state := &types.AgentState{}
			if citation {
				e, state = citationWarningFixture(t, &mockChat{})
			} else {
				// This original transport fixture exercises the existing numeric
				// gate; the separately scoped field below supplies a verified fact.
				state, _, _ = numericRecoveryFixture()
			}
			e.config.AnswerReviewMode, e.config.WorkbenchDefault = "standard", true
			e.toolRegistry = agenttools.NewToolRegistry()
			base := agenttools.NewStructuredDataTool()
			e.toolRegistry.RegisterTool(base)
			for _, tool := range agenttools.NewStructuredReadOperationTools(base, structuredCodecManifest()) {
				e.toolRegistry.RegisterTool(tool)
			}
			ctx := context.WithValue(t.Context(), types.LanguageContextKey, "en-US")
			const question, date = "Read the time in the register on 2021-06-03.", "2021-06-03"
			e.taskPlan = &taskEvidencePlan{CoverageTargets: []taskCoverageTarget{{RequestBasis: question,
				StructuredTaskGoal: &StructuredTaskGoal{SourceMode: "open", Metric: "Elapsed time",
					Period: map[string]any{"from": date, "to": date, "grain": "day"}, Required: []string{"scoped_value"}}}}}
			call := terminalLatestSeriesCall()
			call.Args["operation"], call.Args["start_date"], call.Args["end_date"] = "rows", date, date
			data := call.Result.Data
			filters := data["filters"].(map[string]any)
			filters["operation"], filters["start_date"], filters["end_date"] = "rows", date, date
			data["rows_are"], data["record_count"], data["scope_record_count"], data["valid_count"] = "observations", 1, 1, 1
			state.RoundSteps = append(state.RoundSteps, types.AgentStep{ToolCalls: []types.ToolCall{call}})
			conversation := []chat.Message{{Role: "user", Content: question}}
			e.refreshEvidenceCompletion(ctx, question, state, conversation)
			for _, item := range e.evidenceCompletion.record.Evidence {
				if item.Kind == "cell" {
					e.evidenceCompletion.record.Bindings = []coverageBinding{{GoalIndex: 0, Role: "primary", Kind: "cell",
						SourceID: item.SourceID, KnowledgeBaseID: item.KnowledgeBaseID, SnapshotHash: item.SnapshotHash,
						Sheet: item.Sheet, SeriesID: item.SeriesID, Column: item.Column, EvidenceID: item.EvidenceID,
						JSONPointer: item.JSONPointer, PeriodFrom: date, PeriodTo: date, Grain: "day"}}
				}
			}
			text := "The estimated total was 999.25."
			if citation {
				text = citationWarningDraft
			}
			decision := e.checkCandidate(ctx, question, text, "", state, conversation, false)
			failure := numericFailureFallback
			if citation {
				require.Equal(t, "citation_identity_invalid", decision.ReasonCode)
				failure = answerReviewUnavailableFallback
			} else {
				require.Equal(t, candidateNumericFailure, decision.Disposition)
			}
			recorder := &terminalAnswerRecorder{}
			recorder.attach(e.eventBus)
			e.commitTerminalResult(ctx, state, "terminal", failure, "", "failed", false)
			require.NotContains(t, state.FinalAnswer, "Equipment.xlsx")
			require.NotContains(t, state.FinalAnswer, "47.25")
			require.NotContains(t, state.FinalAnswer, "B15")
			if citation {
				require.Contains(t, state.FinalAnswer, "〔引用待核对〕")
				require.NotContains(t, state.FinalAnswer, "view-shared")
			} else {
				require.Contains(t, state.FinalAnswer, "〔数字待核对〕")
			}
			emitted := state.FinalAnswer
			require.True(t, e.publishUnverifiedCitationReferences(ctx, state))
			require.Equal(t, emitted, state.FinalAnswer)
			count, streamed := recorder.snapshot()
			require.Equal(t, 1, count)
			require.Equal(t, state.FinalAnswer, streamed)
		})
	}
}

func TestTerminalLegacyWorkbenchTextFallbackUsesNativeProof(t *testing.T) {
	for _, flag := range []string{"true", "false"} {
		for _, mode := range []string{"strict", "legacy"} {
			t.Run(flag+"/"+mode, func(t *testing.T) {
				t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", flag)
				e := newTestEngine(t, &mockChat{})
				e.config.AnswerReviewMode, e.config.WorkbenchDefault = mode, true
				e.toolRegistry = qa4ReplayRegistry()
				e.turnReview.Query = "What elapsed time was recorded on 2021-06-03?"
				const day = "2021-06-03"
				e.taskPlan = &taskEvidencePlan{CoverageTargets: []taskCoverageTarget{{RequestBasis: e.turnReview.Query,
					StructuredTaskGoal: &StructuredTaskGoal{SourceMode: "open", Metric: "Elapsed time",
						Period: map[string]any{"from": day, "to": day, "grain": "day"}}}}}
				observed := terminalLatestSeriesCall()
				observed.Args["operation"], observed.Args["start_date"], observed.Args["end_date"] = "rows", day, day
				observed.Args["source_question"] = e.turnReview.Query
				filters := observed.Result.Data["filters"].(map[string]any)
				filters["operation"], filters["start_date"], filters["end_date"] = "rows", day, day
				observed.Result.Data["rows_are"] = "observations"
				state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{observed}}}}
				recorder := &terminalAnswerRecorder{}
				recorder.attach(e.eventBus)
				ctx := context.WithValue(t.Context(), types.LanguageContextKey, "en-US")
				e.commitTerminalResult(ctx, state, "terminal", answerReviewUnavailableFallback, "", "failed", false)
				require.Contains(t, state.FinalAnswer, "47.25")
				require.Contains(t, state.FinalAnswer, "B15")
				require.Contains(t, state.FinalAnswer, day)
				require.Contains(t, state.FinalAnswer, "Equipment.xlsx")
				require.False(t, e.candidateApprovalCurrent(state.FinalAnswer, state), "the strict reviewer never approves the fallback")
				count, streamed := recorder.snapshot()
				require.Equal(t, 1, count)
				require.Equal(t, state.FinalAnswer, streamed)
			})
		}
	}
}
