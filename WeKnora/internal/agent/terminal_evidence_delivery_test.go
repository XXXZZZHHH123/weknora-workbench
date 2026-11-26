package agent

import (
	"context"
	"encoding/json"
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
			"Elapsed time 在已核查范围 2021-07-02", "Equipment.xlsx", "Operating hours", "2021-06-03", "47.25", "A15", "B15", "单位未确认",
		}, omit: []string{"2021-01-01", "Unrelated field", "其余要求"}},
		{name: "normal delivery does not attach an unlinked field", submission: "Readings in scope were reviewed.", want: []string{"Readings in scope were reviewed."}, omit: []string{"2021-01-01", "Unrelated field", "47.25"}},
		{name: "normal delivery supplements a checked field", submission: "Equipment.xlsx, Operating hours. Elapsed time had no valid records on 2021-07-02.\n\nThe requested day remains uncovered.",
			want: []string{"2021-06-03", "47.25", "B15", "\n\nThe requested day remains uncovered."}, omit: []string{"returned no valid observation", "Unrelated field"}},
		{name: "legacy switch preserves the normal candidate", submission: "Equipment.xlsx, Operating hours. Elapsed time had no valid records on 2021-07-02.\n\nThe requested day remains uncovered.",
			alter: func(e *AgentEngine, _ *types.AgentState) { e.evidenceCompletion.enabled = false },
			want:  []string{"\n\nThe requested day remains uncovered."}, omit: []string{"2021-06-03", "47.25", "B15"}},
		{name: "scope narrowed", alter: func(e *AgentEngine, _ *types.AgentState) {
			e.config.SearchTargets = types.SearchTargets{{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "different-library"}}
		}, want: []string{"仍待核实"}, omit: []string{"Equipment.xlsx", "47.25"}},
		{name: "new human task", alter: func(e *AgentEngine, _ *types.AgentState) {
			e.reviewConversation = append(e.reviewConversation, chat.Message{Role: "user", Content: "Use a different file for this question."})
		}, want: []string{"仍待核实"}, omit: []string{"Equipment.xlsx", "47.25"}},
		{name: "source snapshot expires", alter: func(_ *AgentEngine, state *types.AgentState) {
			catalogue := terminalLatestSeriesCall()
			catalogue.ID = "newer-read"
			catalogue.Result.Data["source"].(map[string]any)["sha256"] = strings.Repeat("b", 64)
			catalogue.Result.Data["rows"].([]any)[0].(map[string]any)["value"] = 48.5
			state.RoundSteps[0].ToolCalls = append(state.RoundSteps[0].ToolCalls, catalogue)
		}, want: []string{"Equipment.xlsx"}, omit: []string{"47.25", "48.5"}},
		{name: "only excludes unrelated workbook", alter: func(e *AgentEngine, state *types.AgentState) {
			e.taskPlan = &taskEvidencePlan{CoverageTargets: []taskCoverageTarget{{StructuredTaskGoal: &StructuredTaskGoal{SourceMode: "only"}}}}
			e.evidenceCompletion = newEvidenceCompletionTurn(0)
			e.refreshEvidenceCompletion(t.Context(), e.turnReview.Query, state, e.reviewConversation)
			e.evidenceCompletion.record.Bindings = []coverageBinding{{Role: "primary", SourceID: "other-register", KnowledgeBaseID: "allowed-library"}}
		}, want: []string{"仍待核实"}, omit: []string{"Equipment.xlsx", "47.25"}},
		{name: "current directory only", alter: func(e *AgentEngine, state *types.AgentState) {
			state.RoundSteps[0].ToolCalls = state.RoundSteps[0].ToolCalls[:1]
		}, want: []string{"Equipment.xlsx", "Operating hours", "本轮来源目录", "仍待核实"}, omit: []string{"B15", "47.25", "没有有效观测值"}},
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
		}, want: []string{"没有有效观测值", "2021-07-02", "最后有效日期是 2021-06-03", "尚未取得该日原值"}, omit: []string{"2021-01-01", "Unrelated field", "47.25", "B19", "B20"}},
		{name: "only rejected source calls", alter: func(e *AgentEngine, state *types.AgentState) {
			state.RoundSteps[0].ToolCalls = state.RoundSteps[0].ToolCalls[1:2]
			state.RoundSteps[0].ToolCalls[0].Result = &types.ToolResult{PreflightRejected: true, Error: "ineligible optional selection", Data: map[string]any{"execution_started": false}}
		}, want: []string{"读取开始前被拦下", "仍待核实"}, omit: []string{"ineligible optional selection", "47.25"}},
		{name: "legacy switch", alter: func(e *AgentEngine, _ *types.AgentState) {
			e.evidenceCompletion.enabled = false
		}, want: []string{"2021-07-02", "47.25", "B15"}, omit: []string{answerReviewUnavailableFallback}},
		{name: "exact format", alter: func(e *AgentEngine, _ *types.AgentState) {
			e.taskPlan = &taskEvidencePlan{DeliveryRequirements: []taskDeliveryRequirement{{Kind: "text", Format: "exact"}}}
		}, want: []string{answerReviewUnavailableFallback}, omit: []string{"47.25"}},
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
			for _, value := range test.want {
				require.Contains(t, state.FinalAnswer, value)
			}
			for _, value := range test.omit {
				require.NotContains(t, state.FinalAnswer, value)
			}
		})
	}
}

func TestTerminalCoverageNaturalFieldAssertions(t *testing.T) {
	field := coverageEvidence{
		SourceID: "meter-readings", KnowledgeBaseID: "maintenance", SnapshotHash: "current",
		SourceTitle: "机台记录.xlsx", Sheet: "运行时长", Metric: "一号机累计时长", SeriesID: "meter.B",
	}
	gap, last := field, field
	gap.Kind, gap.Complete, gap.PeriodFrom, gap.PeriodTo = "gap", true, "2021-07-02", "2021-07-02"
	last.Kind, last.Raw, last.Date, last.DateCell, last.Cell = "last_valid", json.Number("47.25"), "2021-06-03", "A15", "B15"
	delivery := terminalCoverageDelivery{inspected: true, chinese: true, facts: []terminalCoverageFact{
		{coverageFact: coverageFact{"coverage_boundary", gap}, required: true},
		{coverageFact: coverageFact{"last_valid_value", last}, required: true},
	}}
	const closing = "\n\n所问期间无可比原值。<kb doc=\"检修说明.pdf\" chunk_id=\"checked-note\" kb_id=\"maintenance\" />"
	for _, test := range []struct {
		name, draft       string
		addGap, addLatest bool
	}{
		{"naturally complete",
			"**机台记录里查不到 2021年7月2日这一天的数据。** 该文件「运行时长」表最后一条有数值的记录为 2021年6月3日，「一号机累计时长」为 47.25（字段单位尚未确认），原单元格为运行时长!B15。2021年7月2日的单日查询返回零条有效记录。",
			false, false},
		{"gap only", "机台记录的「运行时长」表中，一号机累计时长在 2021 年 7 月 2 日没有有效记录。", false, true},
		{"latest only", "机台记录的「运行时长」表中，2021年6月3日的最后一条记录中，一号机累计时长为 47.25。",
			true, false},
		{"same date and value in different paragraphs", "机台记录的「运行时长」表中，一号机累计时长最后一条记录在 2021年6月3日。\n\n校准系数是 47.25。", true, true},
		{"other file in the same paragraph", "机台记录的「运行时长」表中有一号机累计时长。年度汇总.xlsx 在 2021年7月2日没有有效记录，最后一条记录为 2021年6月3日的 47.25。", true, true},
		{"other file with coinciding cells", "年度汇总.xlsx 的一号机累计时长最后一条记录在 2021年6月3日，值为 47.25，日期在 A15，原格在 B15。", true, true},
		{"other date in same assertion", "机台记录的「运行时长」表中，一号机累计时长最后一条记录在 2021年6月4日，为 47.25，前一天 2021年6月3日也有记录。", true, true},
		{"negated assertions", "机台记录的「运行时长」表中，不能断定一号机累计时长在 2021年7月2日没有有效记录。最后一条记录在 2021年6月3日，并非 47.25。", true, true},
		{"number appears only in file or coordinates", "机台记录的「运行时长」表中，一号机累计时长的最后有效日期是 2021年6月3日，原格为 B47，备注标识 ID47.25。", true, true},
		{"value has unsupported units", "机台记录的「运行时长」表中，最后一条记录在 2021年6月3日，一号机累计时长为 47.25 万小时。", true, true},
		{"same number from another metric", "机台记录的「运行时长」表中，一号机累计时长最后一条记录在 2021年6月3日，校准系数为 47.25。", true, true},
		{"table with source cell", "机台记录的「运行时长」表包含一号机累计时长。\n| 一号机累计时长 | 2021-06-03 | 47.25 | B15 |", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := test.draft + closing
			result := delivery.render(false, candidate)
			if test.addGap || test.addLatest {
				require.True(t, strings.HasSuffix(result, closing), result)
				for _, paragraph := range strings.Split(test.draft, "\n\n") {
					require.Contains(t, result, paragraph)
				}
				require.Equal(t, test.addGap, strings.Contains(result, "在 2021-07-02 没有有效记录"), result)
				require.Equal(t, test.addLatest, strings.Contains(result, "最后有值的日期为 2021-06-03，原值为 47.25"), result)
			} else {
				require.Equal(t, candidate, result)
			}
			require.Equal(t, result, delivery.render(false, result))
		})
	}
}

func TestTerminalDisplayedValueRequiresWholeUnit(t *testing.T) {
	for _, tt := range []struct {
		unit, display string
		want          bool
	}{
		{"m", "Reading was 47.25 m (B15)", true},
		{"m", "Reading was 47.25 min (B15)", false},
		{"m", "Reading was 47.25 m.s (B15)", false},
		{"m", "Reading was 47.25m (B15)", true},
		{"m", "Reading was 47.25min (B15)", false},
		{"m", "Reading was 47.25m/s (B15)", false},
		{"元", "Reading was 47.25 元。", true},
		{"元", "Reading was 47.25 元/吨。", false},
		{"元", "Reading was 47.25 元 / 吨。", false},
		{"元", "Reading是47.25元。", true},
		{"", "Reading是47.25。", true},
		{"", "Reading was 47.25on", false},
	} {
		t.Run(tt.display, func(t *testing.T) {
			fact := terminalCoverageFact{coverageFact: coverageFact{Evidence: coverageEvidence{
				Metric: "Reading", Raw: json.Number("47.25"), Unit: tt.unit, UnitBasis: "explicit",
			}}}
			require.Equal(t, tt.want, terminalDisplayedValue(tt.display, fact, false))
		})
	}
}

func TestTerminalCoverageSameMetricSeriesStayDistinct(t *testing.T) {
	last := coverageEvidence{
		Kind: "last_valid", SourceID: "meter-readings", KnowledgeBaseID: "maintenance", SnapshotHash: "current",
		SourceTitle: "Meter log.xlsx", Sheet: "Sessions", Metric: "Elapsed time", SeriesID: "meter-early",
		Subject: "AX01.EX", Date: "2021-06-03", Raw: json.Number("47.25"), DateCell: "A15", Cell: "B15",
	}
	peer, third := last, last
	peer.SeriesID, peer.Subject, peer.Cell = "meter-late", "AX07.EX", "D15"
	third.SeriesID, third.Subject, third.Cell, third.Raw = "meter-final", "AX11.EX", "F15", json.Number("48.5")
	delivery := terminalCoverageDelivery{inspected: true, facts: []terminalCoverageFact{
		{coverageFact: coverageFact{"last_valid_value", last}, required: true},
		{coverageFact: coverageFact{"last_valid_value", peer}, required: true},
		{coverageFact: coverageFact{"last_valid_value", third}, required: true},
	}}
	const draft = "In Meter log.xlsx, sheet Sessions, the latest Elapsed time for AX01.EX was 47.25 on 2021-06-03. AX07 and AX11 both have their latest valid date on 2021-06-03."
	const closing = "\n\nThe requested month is outside the register's coverage."
	answer := delivery.render(false, draft+closing)
	require.True(t, strings.HasSuffix(answer, closing), answer)
	require.Contains(t, answer, draft)
	require.Equal(t, 2, strings.Count(answer, "Meter log.xlsx"), answer)
	require.NotContains(t, answer, "value cell B15")
	require.Contains(t, answer, "AX07.EX 47.25 (A15, D15)")
	require.Contains(t, answer, "AX11.EX 48.5 (A15, F15)")
	require.Equal(t, 1, strings.Count(answer, "units unconfirmed"), answer)
	require.Equal(t, answer, delivery.render(false, answer))

	delivery.naturalSQLFallback = true
	for i := range delivery.facts {
		last := &delivery.facts[i].Evidence
		last.Operation, last.Complete, last.LatestInScope = "analyze_table", true, true
		last.EvidenceID, last.DateEvidenceID = "analysis", "analysis"
		last.CellJSONPointer, last.DateJSONPointer = "/data/cell", "/data/date"
	}
	for _, last := range slices.Clone(delivery.facts) {
		gap, ok := coverageAnalysisGap(last.Evidence, "2021-07-01", "2021-07-31", "month")
		require.True(t, ok)
		delivery.facts = append(delivery.facts, terminalCoverageFact{coverageFact: coverageFact{"coverage_boundary", gap}, required: true})
	}
	fallback := delivery.render(true, "")
	require.Equal(t, 1, strings.Count(fallback, "has no valid records"), fallback)
	require.Equal(t, 1, strings.Count(fallback, "records the latest valid values"), fallback)
	require.Contains(t, fallback, "AX01.EX 47.25 (A15, B15)")
	require.Contains(t, fallback, "AX07.EX 47.25 (A15, D15)")
	require.Contains(t, fallback, "AX11.EX 48.5 (A15, F15)")
	require.Equal(t, fallback, delivery.render(false, fallback))
}

func TestTerminalSQLFollowUpWithoutDraft(t *testing.T) {
	for _, tt := range []struct {
		language, gap, endpoint string
	}{
		{"zh-CN", "没有有效记录", "最后有值的日期为 2023-09-22，原值为 17110"},
		{"en-US", "has no valid records", "on 2023-09-22 as 17110"},
	} {
		t.Run(tt.language, func(t *testing.T) {
			r := newQA4Stage2CReplay(t, "s2-3-kb16-bounded-latest", true, false)
			r.calls(r.trace.Calls)
			ctx := context.WithValue(r.ctx, types.LanguageContextKey, tt.language)
			fallback := r.engine.terminalCoverageFailure(ctx, r.state, answerReviewUnavailableFallback)
			for _, expected := range []string{tt.gap, tt.endpoint, "CF.CZC", "A4705", "B4705"} {
				require.Contains(t, fallback, expected)
			}
			require.True(t, slices.ContainsFunc(strings.Split(fallback, "\n\n"), func(paragraph string) bool {
				return strings.Contains(paragraph, tt.endpoint) && strings.Contains(paragraph, "CF.CZC")
			}), fallback)
			require.NotContains(t, fallback, "棉花数据库.xlsx /")

			delivery := r.engine.terminalCoverageDelivery(ctx, r.state, false)
			require.Equal(t, fallback, delivery.render(false, fallback))
			const draft = "已核查《棉花数据库.xlsx》的「郑棉期货」表及「收盘价」列\n\n这里保留结论。"
			final := delivery.render(false, draft)
			require.Contains(t, final, tt.endpoint)
			require.Equal(t, final, delivery.render(false, final))
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
				require.Contains(t, state.FinalAnswer, "第4页")
				require.Contains(t, state.FinalAnswer, "实际该页的图例：类型甲、类型乙。")
			} else {
				require.Contains(t, state.FinalAnswer, "本轮已读取 Quarterly inspection 第 4 页返回的正文")
				require.Contains(t, state.FinalAnswer, "仍待核实")
				require.NotContains(t, state.FinalAnswer, "类型甲")
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
		{"empty final", "", "budget", false, false, "true", "尚未完成所需的来源读取"},
		{"missing source", answerReviewUnavailableFallback, "", false, false, "true", "本轮没有完成"},
		{"bad tool", incompleteAnswerFallback, "", true, false, "true", "已查询 2021-07-02 的结构化记录"},
		{"blocked scope", sourceProgressStalledFallback, "", true, true, "true", "2021-07-02 的数据请求在读取开始前被拦下"},
		{"progress stalled", stalledAnswerFallback, "", true, false, "true", "未取得可用结果"},
		{"truncated answer", truncatedAnswerFallback, "budget", false, false, "true", "尚未完成所需的来源读取"},
		{"numeric fallback", numericFailureFallback, "", true, false, "true", "未取得可用结果"},
		{"numeric final fallback", finalNumericFailureFallback, "", true, false, "true", "未取得可用结果"},
		{"legacy tool decisions", sourceProgressStalledFallback, "", true, true, "false", "2021-07-02 的数据请求在读取开始前被拦下"},
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
			require.True(t, strings.HasPrefix(state.FinalAnswer, "Pressure 2021-07-02 仍待核实。"), state.FinalAnswer)
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
			"", "needs_review", "", "47.25 hours", "uncertain"},
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
		{"different values", "hours", "两处来源在所问范围给出的数值不一致。", 32.5},
		{"equal values", "hours", "两处来源在所问范围给出的数值相同。", 47.25},
		{"unknown unit", "", "两处来源列示的数字不同。两处单位能否直接比较仍待核实。", 32.5},
		{"unknown unit equal figures", "", "两处来源列示的数字相同。两处单位能否直接比较仍待核实。", 47.25},
		{"incompatible unit", "minutes", "两处来源列示的数字不同。来源使用的单位不同，以下保留各自原值。", 32.5},
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
	require.Contains(t, state.FinalAnswer, e.turnReview.Query)
	require.Contains(t, state.FinalAnswer, "Equipment.xlsx")
	require.Contains(t, state.FinalAnswer, "2021-06-03")
	require.NotContains(t, state.FinalAnswer, "47.25")
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
			require.Contains(t, state.FinalAnswer, "no verified material")
			require.NotContains(t, state.FinalAnswer, answerReviewUnavailableFallback)
			count, streamed := recorder.snapshot()
			require.Equal(t, 1, count)
			require.Equal(t, streamed, state.FinalAnswer)
		})
	}
}

func TestTerminalWarningsKeepCurrentVerifiedFacts(t *testing.T) {
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
			require.Contains(t, state.FinalAnswer, "Equipment.xlsx")
			require.Contains(t, state.FinalAnswer, "47.25")
			require.Contains(t, state.FinalAnswer, "B15")
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

func TestTerminalUnitEditPreservesAttributedDraft(t *testing.T) {
	const original = "The hours spent on calibration support the ordering in the report.\n\n" +
		"Elapsed time: 47.25 <evidence evidence_id=\"current-value\" />\n\n" +
		"The worksheet does not label the value in hours."
	facts := []terminalCoverageFact{{coverageFact: coverageFact{Evidence: coverageEvidence{
		Metric: "Elapsed time", Raw: json.Number("47.25"), CitationID: "current-value",
	}}}}
	text, complete, updated := terminalAddDeclaredUnit(original, "hours", facts)
	require.True(t, complete)
	require.True(t, updated)
	require.Contains(t, text, "The hours spent on calibration support the ordering in the report.")
	require.Contains(t, text, "47.25 hours <evidence evidence_id=\"current-value\" />")
	require.NotContains(t, text, "The worksheet does not label")
	wrong := strings.Replace(original, "current-value", "different-source-same-value", 1)
	text, complete, updated = terminalAddDeclaredUnit(wrong, "hours", facts)
	require.False(t, complete)
	require.False(t, updated)
	require.Equal(t, wrong, text)
}
