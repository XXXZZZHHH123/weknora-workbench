package agent

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestQA4AnalysisEndpointUsesUniqueCompleteNativeCell(t *testing.T) {
	const receipt = `{
		"observation_coverage_scope":"loaded_analysis_inputs_before_user_sql",
		"actual_first_date":"2025-01-03","actual_last_date":"2025-01-08","valid_last_date":"2025-01-06","input_count":3,"valid_count":2,
		"series":{"dimensions":{"block":"closing","contract":"2025-04"}},
		"observation_coverage":{
			"scope_start_date":"2025-01-03","scope_end_date":"2025-01-08","scope_record_count":3,"scope_valid_count":2,
			"total_segment_count":2,"truncated":false,
			"segments":[
				{"start_date":"2025-01-03","end_date":"2025-01-06","first_cell":"F8","last_cell":"F9","record_count":2,"valid_count":2,"value_status":"valid","has_value":true,"duplicate_date_boundary":false,"undated_boundary":false},
				{"start_date":"2025-01-08","end_date":"2025-01-08","first_cell":"F10","last_cell":"F10","record_count":1,"valid_count":0,"value_status":"blank","has_value":false,"duplicate_date_boundary":false,"undated_boundary":false}
			],
			"value_changes":{"scope":{"record_count":3,"valid_value_count":2,"eligible_record_count":2,"eligible_end_date":"2025-01-06","complete_filtered_input":true},"last":{"date":"2025-01-06","value":0.0}}
		}
	}`
	field := coverageEvidence{Bound: true, EvidenceID: "read", Operation: "analyze_table", SourceID: "dataset", DatasetID: "dataset",
		Sheet: "Sheet", SeriesID: "field/a~b", Metric: "Metric", Column: "F", PeriodTo: "2025-02-28", LastValidDate: "2025-01-06"}
	for _, tt := range []struct {
		name   string
		mutate func(field *coverageEvidence, input, coverage, segment, last map[string]any)
	}{
		{name: "complete"},
		{name: "string numeric", mutate: func(_ *coverageEvidence, _, _, _, last map[string]any) { last["value"] = "0.0" }},
		{name: "nonfinite", mutate: func(_ *coverageEvidence, _, _, _, last map[string]any) { last["value"] = json.Number("1e999") }},
		{name: "missing provenance date", mutate: func(_ *coverageEvidence, input, _, _, _ map[string]any) { delete(input, "valid_last_date") }},
		{name: "different endpoint date", mutate: func(_ *coverageEvidence, _, _, _, last map[string]any) { last["date"] = "2025-01-08" }},
		{name: "duplicate date", mutate: func(_ *coverageEvidence, _, _, segment, _ map[string]any) { segment["duplicate_date_boundary"] = true }},
		{name: "truncated segments", mutate: func(_ *coverageEvidence, _, coverage, _, _ map[string]any) { coverage["truncated"] = true }},
		{name: "neighboring column", mutate: func(_ *coverageEvidence, _, _, segment, _ map[string]any) { segment["last_cell"] = "G9" }},
		{name: "incorrect end date", mutate: func(_ *coverageEvidence, _, _, segment, _ map[string]any) { segment["end_date"] = "2025-01-05" }},
		{name: "wrong workbook", mutate: func(_ *coverageEvidence, input, _, _, _ map[string]any) {
			coverageObject(input["series"])["source_dataset_id"] = "retained"
		}},
		{name: "outside native read", mutate: func(field *coverageEvidence, _, _, _, _ map[string]any) { field.PeriodFrom = "2025-01-09" }},
		{name: "overlapping other segment", mutate: func(_ *coverageEvidence, _, coverage, _, _ map[string]any) {
			coverageObject(coverage["segments"].([]any)[1])["start_date"] = "2025-01-06"
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			item := field
			input, err := decodeCitationJSON(receipt)
			require.NoError(t, err)
			coverage := coverageObject(input["observation_coverage"])
			if tt.mutate != nil {
				tt.mutate(&item, input, coverage, coverageObject(coverage["segments"].([]any)[0]), coverageObject(coverageObject(coverage["value_changes"])["last"]))
			}
			last, verified := coverageAnalysisLastValid(item, input)
			require.Equal(t, tt.mutate == nil, verified)
			if verified {
				require.True(t, coverageAnalysisEndpoint(last))
				require.Equal(t, "F9", last.Cell)
				require.Equal(t, "2025-01-06", last.Date)
				require.Equal(t, "2025-04", last.Subject)
				require.Equal(t, json.Number("0.0"), last.Raw)
				require.Empty(t, last.DateCell)
				require.Equal(t, "/data/inputs/field~1a~0b/observation_coverage/segments/0/last_cell", last.CellJSONPointer)
				require.Equal(t, "/data/inputs/field~1a~0b/observation_coverage/value_changes/last/date", last.DateJSONPointer)
				require.Equal(t, "/data/inputs/field~1a~0b/observation_coverage/value_changes/last/value", last.JSONPointer)
			}
		})
	}
}

func TestQA4AnalysisEndpointProjectionRequiresCurrentWorkbookAndActiveSwitch(t *testing.T) {
	fixture, err := os.ReadFile("testdata/qa4/replay/r7-2-kb31.json")
	require.NoError(t, err)
	for _, tt := range []struct {
		name, enabled string
		change        func(types.ToolCall)
	}{
		{name: "enabled", enabled: "true"},
		{name: "rollback", enabled: "false"},
		{name: "unbound original workbook", enabled: "true", change: func(call types.ToolCall) {
			delete(coverageObject(call.Result.Data["source"]), "workbook_declared_last_saved_at")
		}},
		{name: "executed range mismatch", enabled: "true", change: func(call types.ToolCall) {
			call.Args["end_date"] = "2025-02-27"
		}},
		{name: "executed SQL mismatch", enabled: "true", change: func(call types.ToolCall) {
			call.Args["sql"] = "SELECT 1"
		}},
		{name: "failed execution", enabled: "true", change: func(call types.ToolCall) {
			call.Result.Data["status"] = "needs_clarification"
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", tt.enabled)
			var trace struct{ Steps []types.AgentStep }
			require.NoError(t, json.Unmarshal(fixture, &trace))
			engine := &AgentEngine{toolRegistry: qa4ReplayRegistry(trace.Steps...)}
			var call types.ToolCall
			for _, step := range trace.Steps {
				for _, candidate := range step.ToolCalls {
					if candidate.Name == "structured_analyze_table" {
						call = candidate
					}
				}
			}
			require.NotNil(t, call.Result)
			const to = "2025-02-28"
			call.Args["end_date"] = to
			data, err := json.Marshal(call.Result.Data)
			require.NoError(t, err)
			call.Result.Data, err = decodeCitationJSON(string(data))
			require.NoError(t, err)
			coverageObject(call.Result.Data["requested_range"])["end_date"] = to
			if tt.change != nil {
				tt.change(call)
			}
			require.NoError(t, rehashCoverageSQLFixture(call.Result.Data))
			state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
			record, _ := engine.projectCurrentTaskCoverage(state, 0, coverageRecord{})
			require.Equal(t, tt.name == "enabled", slices.ContainsFunc(record.Evidence, func(item coverageEvidence) bool {
				return coverageAnalysisEndpoint(item) && coverageCurrentEvidence(record, item)
			}))
		})
	}
}

func TestQA4AnalysisEndpointRetryUsesOnlyEarlierMatchingRequest(t *testing.T) {
	const question = "Please check the source's readings over February."
	const from, to = "2025-02-01", "2025-02-28"
	endpoint := coverageEvidence{Operation: "analyze_table", Kind: "last_valid", Bound: true, Complete: true, LatestInScope: true,
		SeriesID: "measure.a", EvidenceID: "current", DateEvidenceID: "current", DateJSONPointer: "/date", CellJSONPointer: "/segment/cell",
		KnowledgeBaseID: "library", DatasetID: "dataset", Date: "2025-01-24", PeriodTo: to}
	for _, tt := range []struct {
		name   string
		mutate func(attempts []coverageAttempt, endpoint *coverageEvidence) []coverageAttempt
	}{
		{name: "same fields despite transport order"},
		{name: "different source", mutate: func(attempts []coverageAttempt, _ *coverageEvidence) []coverageAttempt {
			attempts[0].Args["dataset_id"] = "other"
			return attempts
		}},
		{name: "different field", mutate: func(attempts []coverageAttempt, _ *coverageEvidence) []coverageAttempt {
			attempts[0].Args["series_ids"] = []any{"measure.a", "unselected"}
			return attempts
		}},
		{name: "range extends past native end", mutate: func(attempts []coverageAttempt, _ *coverageEvidence) []coverageAttempt {
			attempts[0].Args["end_date"] = "2025-03-01"
			return attempts
		}},
		{name: "other original question", mutate: func(attempts []coverageAttempt, _ *coverageEvidence) []coverageAttempt {
			attempts[1].Args["source_question"] = "Analyze averages."
			return attempts
		}},
		{name: "missing confirmed field binding", mutate: func(attempts []coverageAttempt, _ *coverageEvidence) []coverageAttempt {
			delete(attempts[1].Args, "source_binding")
			return attempts
		}},
		{name: "later clarification", mutate: func(attempts []coverageAttempt, _ *coverageEvidence) []coverageAttempt {
			return []coverageAttempt{attempts[1], attempts[0]}
		}},
		{name: "conflicting prior request", mutate: func(attempts []coverageAttempt, _ *coverageEvidence) []coverageAttempt {
			other := attempts[0]
			other.Args = map[string]any{"dataset_id": "dataset", "knowledge_base_id": "library", "series_ids": []any{"measure.a", "measure.b"},
				"start_date": "2025-02-02", "end_date": to}
			return []coverageAttempt{attempts[0], other, attempts[1]}
		}},
		{name: "tool did not execute", mutate: func(attempts []coverageAttempt, _ *coverageEvidence) []coverageAttempt {
			attempts[0].Success = false
			return attempts
		}},
		{name: "observation is within requested range", mutate: func(attempts []coverageAttempt, endpoint *coverageEvidence) []coverageAttempt {
			endpoint.Date = from
			return attempts
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			attempts := []coverageAttempt{
				{Operation: "analyze_table", Success: true, Args: map[string]any{
					"dataset_id": "dataset", "knowledge_base_id": "library", "series_ids": []any{"measure.b", "measure.a"},
					"start_date": from, "end_date": to}, result: &types.ToolResult{Data: map[string]any{
					"status": "needs_clarification", "kind": "structured_source_binding", "source_context": map[string]any{"knowledge_base_id": "library"}}}},
				{Operation: "analyze_table", Success: true, EvidenceID: "current", Args: map[string]any{
					"source_question": question, "series_ids": []any{"measure.a", "measure.b"},
					"source_binding": []any{map[string]any{"selected_ids": []any{"measure.a", "measure.b"}, "considered_ids": []any{"measure.a", "measure.b"}, "reason": "checked both real fields"}}}},
			}
			item := endpoint
			if tt.mutate != nil {
				attempts = tt.mutate(attempts, &item)
			}
			start, end := coverageAnalysisRetriedScope(attempts, item, question)
			gap, verified := coverageAnalysisGap(item, start, end, "month")
			require.Equal(t, tt.mutate == nil, verified)
			if verified {
				require.Equal(t, "gap", gap.Kind)
				require.Equal(t, from, gap.PeriodFrom)
				require.Equal(t, to, gap.PeriodTo)
				require.Equal(t, item.Date, gap.Raw)
			}
		})
	}
}

func TestQA4AnalysisEndpointMissingValueSchedulesBoundedLatest(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "true")
	fixture, err := os.ReadFile("testdata/qa4/replay/r7-2-kb31.json")
	require.NoError(t, err)
	var trace struct{ Steps []types.AgentStep }
	require.NoError(t, json.Unmarshal(fixture, &trace))
	var call types.ToolCall
	for _, step := range trace.Steps {
		for _, candidate := range step.ToolCalls {
			if candidate.Name == "structured_analyze_table" {
				call = candidate
			}
		}
	}
	require.NotNil(t, call.Result)
	data, err := json.Marshal(call.Result.Data)
	require.NoError(t, err)
	call.Result.Data, err = decodeCitationJSON(string(data))
	require.NoError(t, err)
	const question, from, to = "Is there a recorded value in February?", "2025-02-01", "2025-02-28"
	call.Result.Data["requested_range"] = map[string]any{"start_date": from, "end_date": to}
	call.Result.Data["input_count"], call.Result.Data["rows"] = json.Number("0"), []any{}
	call.Result.Data["returned_row_count"] = json.Number("0")
	var id, lastDay string
	for key, candidate := range coverageObject(call.Result.Data["inputs"]) {
		id = key
		input := coverageObject(candidate)
		lastDay = sourceNavigationString(coverageObject(input["series"]), "last_valid_date")
		input["valid_count"], input["input_count"] = json.Number("0"), json.Number("0")
		delete(input, "observation_coverage")
	}
	require.NotEmpty(t, id)
	require.NotEmpty(t, lastDay)
	call.Args["start_date"], call.Args["end_date"], call.Args["source_question"] = from, to, question
	binding := []any{map[string]any{"considered_ids": []any{id, "other"}, "selected_ids": []any{id}, "reason": "model selected the measure over its sibling"}}
	call.Args["source_binding"] = binding
	require.NoError(t, rehashCoverageSQLFixture(call.Result.Data))
	engine := newTestEngine(t, &mockChat{})
	engine.config.WorkbenchDefault, engine.config.AnswerReviewMode = true, "standard"
	engine.toolRegistry = qa4ReplayRegistry(trace.Steps...)
	engine.turnReview.Query = question
	state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
	engine.refreshEvidenceCompletion(t.Context(), question, state, nil)
	key, name, args := engine.coverageLatestRead(state, true)
	require.NotEmpty(t, key)
	require.Equal(t, "structured_query_series", name)
	require.Equal(t, map[string]any{"dataset_id": call.Args["dataset_id"], "knowledge_base_id": call.Args["knowledge_base_id"],
		"series_id": id, "operation": "latest", "limit": 1, "start_date": lastDay, "end_date": lastDay,
		"source_binding": binding, "source_question": question}, args)
	for _, change := range []func(){
		func() { delete(call.Args, "source_binding") },
		func() { call.Args["source_question"] = "A different question." },
		func() { call.Args["series_ids"] = []any{id, "other"} },
		func() { engine.evidenceCompletion.latestAttempts = map[string]bool{key: true} },
	} {
		change()
		_, name, _ := engine.coverageLatestRead(state, true)
		require.Empty(t, name)
		call.Args["source_binding"], call.Args["source_question"], call.Args["series_ids"] = binding, question, []any{id}
	}
	engine.evidenceCompletion.latestAttempts = nil
	record := &engine.evidenceCompletion.record
	gap := record.Evidence[slices.IndexFunc(record.Evidence, coverageAnalysisEmptyInput)]
	last := gap
	last.Operation, last.Kind, last.Status = "query_series", "last_valid", "ok"
	last.EvidenceID, last.JSONPointer, last.SnapshotHash = "actual-cell-read", "/data/rows/0/value", gap.WorkbookSnapshotHash
	last.Date, last.DateCell, last.Cell, last.Raw = lastDay, "A8", gap.Column+"8", json.Number("72")
	record.Evidence = append(record.Evidence, last)
	record.Snapshots[coverageSnapshotKey(last)] = last.SnapshotHash
	require.True(t, sameCoverageLastValid(*record, gap, last))
	legacyGap := gap
	legacyGap.WorkbookSnapshotHash = ""
	require.False(t, sameCoverageLastValid(*record, legacyGap, last))
	require.True(t, coverageAnalysisFollowUpMatches(*record, legacyGap, last), "the rollback keeps the existing terminal-only join")
	_, name, _ = engine.coverageLatestRead(state, true)
	require.Empty(t, name, "a current result of the exact same field closes the one allowed follow-up")
}

func TestQA4AnalysisEndpointHonorsTypedTargetInReadAndDelivery(t *testing.T) {
	for _, tc := range []struct {
		name         string
		adjust       func(*StructuredTaskGoal)
		differentRow bool
	}{
		{name: "requested field"},
		{name: "unrelated metric from the same workbook", adjust: func(goal *StructuredTaskGoal) { goal.Metric = "开盘价" }},
		{name: "different primary workbook", adjust: func(goal *StructuredTaskGoal) { goal.PrimaryRef = "其他来源.xlsx" }},
		{name: "no last valid result requested", adjust: func(goal *StructuredTaskGoal) { goal.OnVerifiedGap = []string{"report_gap"} }},
		{name: "optional target", adjust: func(goal *StructuredTaskGoal) { goal.Required = []string{"optional"} }},
		{name: "same metric but different bound field", differentRow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newQA4Stage2CReplay(t, "s2-3-kb16-bounded-latest", true, false)
			analysis, latest := r.trace.Calls[0], r.trace.Calls[1]
			goal := StructuredTaskGoal{
				SourceMode: "named_first", PrimaryRef: "棉花数据库.xlsx", Metric: "收盘价", SourceTime: "observed_period",
				Period:   map[string]any{"from": analysis.Arguments["start_date"], "to": analysis.Arguments["end_date"], "grain": "month"},
				Required: []string{"scoped_value"}, OnVerifiedGap: []string{"report_gap", "last_valid_same_field"}, Delivery: map[string]any{"kind": "text"},
			}
			if tc.adjust != nil {
				tc.adjust(&goal)
			}
			r.setPlan(goal)
			r.calls([]qa4Stage2Call{analysis})
			engine := r.engine
			if tc.differentRow {
				engine.evidenceCompletion.record.Bindings = []coverageBinding{{GoalIndex: 0, Role: "primary", SeriesID: "different-field",
					SourceID: sourceNavigationString(analysis.Arguments, "dataset_id")}}
			}
			want := tc.adjust == nil && !tc.differentRow
			_, name, _ := engine.coverageSameFieldLatestRead(r.state)
			require.Equal(t, want, name == "structured_query_series")
			// An ordinary authorized read can still return the field, even if a different
			// task target cannot use it as its automatic or required last observation.
			r.calls([]qa4Stage2Call{latest})
			delivery := engine.terminalCoverageDelivery(r.ctx, r.state, false)
			require.Equal(t, want, slices.ContainsFunc(delivery.facts, func(fact terminalCoverageFact) bool {
				return fact.required && fact.Role == "coverage_boundary" && coverageAnalysisEmptyInput(fact.Evidence)
			}))
			require.Equal(t, want, slices.ContainsFunc(delivery.facts, func(fact terminalCoverageFact) bool {
				return fact.Role == "last_valid_value" && fact.Evidence.Cell == "B4705" && fact.required
			}))
			if !want {
				require.NotContains(t, delivery.render(true, ""), "17110")
			}
		})
	}
}

func TestQA4AnalysisEndpointClippedRangeRequiresBoundFieldAndRequest(t *testing.T) {
	for _, tc := range []struct {
		name                                 string
		plan, retry, wrongField, wrongMetric bool
		withoutBinding, unknownWorkbook      bool
		publish                              bool
		want                                 bool
	}{
		{name: "verified plan", plan: true, want: true},
		{name: "verified plan publishes original cell after a cropped summary", plan: true, publish: true, want: true},
		{name: "earlier native request", retry: true, want: true},
		{name: "no exact requested range"},
		{name: "retry of another field", retry: true, wrongField: true},
		{name: "plan requests a different metric", plan: true, wrongMetric: true},
		{name: "missing explicit field binding", retry: true, withoutBinding: true},
		{name: "unverified original workbook", plan: true, unknownWorkbook: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var followUps []qa4Stage2Call
			if tc.publish {
				raw, err := os.ReadFile("testdata/qa4/replay/s2-3-kb16-bounded-latest.json")
				require.NoError(t, err)
				var fixture qa4Stage2CTrace
				require.NoError(t, json.Unmarshal(raw, &fixture))
				followUps = fixture.Calls[1:2]
			}
			r := newQA4Stage2CReplay(t, "s2-3-kb16", true, false, followUps...)
			var attempt, analysis qa4Stage2Call
			for _, call := range r.trace.Calls {
				switch call.Event {
				case 25:
					attempt = call
				case 29:
					analysis = call
				}
			}
			data, err := decodeCitationJSON(string(analysis.Response))
			require.NoError(t, err)
			field := "futures.close.CF03M.CZC"
			if tc.publish {
				field = "futures.close.CF.CZC"
			}
			input := coverageObject(coverageObject(data["inputs"])[field])
			if tc.publish {
				// Keep the native date/value summary and simulate a missing tail
				// of the cell segments. The follow-up is the actual same-workbook B-cell receipt.
				coverage := coverageObject(input["observation_coverage"])
				segment := sourceNavigationRows(coverage["segments"])[0]
				segment["end_date"], segment["last_cell"] = segment["start_date"], segment["first_cell"]
				segment["record_count"], segment["valid_count"] = json.Number("1"), json.Number("1")
				coverage["truncated"], coverage["total_segment_count"], coverage["max_segments"] = true, json.Number("2"), json.Number("1")
			}
			require.Equal(t, true, coverageObject(input["observation_coverage"])["truncated"])
			data["input_count"], data["inputs"] = input["input_count"], map[string]any{field: input}
			var rows []any
			for _, raw := range data["rows"].([]any) {
				if raw.([]any)[0] == field {
					rows = append(rows, raw)
				}
			}
			require.Len(t, rows, 1)
			data["rows"], data["returned_row_count"] = rows, json.Number("1")
			if tc.unknownWorkbook {
				delete(coverageObject(data["source"]), "workbook_declared_last_saved_at")
			}
			require.NoError(t, rehashCoverageSQLFixture(data))
			analysis.Response, err = json.Marshal(data)
			require.NoError(t, err)
			analysis.Arguments["series_ids"] = []any{field}
			if tc.withoutBinding {
				delete(analysis.Arguments, "source_binding")
			} else {
				sourceNavigationRows(analysis.Arguments["source_binding"])[0]["selected_ids"] = []any{field}
			}
			attempt.Arguments["series_ids"] = []any{field}
			if tc.wrongField {
				attempt.Arguments["series_ids"] = []any{"futures.close.CF.CZC"}
			}
			if tc.plan {
				goal := StructuredTaskGoal{
					SourceMode: "named_first", PrimaryRef: "棉花数据库.xlsx", Metric: "收盘价", SourceTime: "observed_period",
					Period:        map[string]any{"from": attempt.Arguments["start_date"], "to": attempt.Arguments["end_date"], "grain": "month"},
					OnVerifiedGap: []string{"last_valid_same_field"}, Delivery: map[string]any{"kind": "text"},
				}
				if tc.wrongMetric {
					goal.Metric = "开盘价"
				}
				r.setPlan(goal)
			}
			r.trace.Calls = []qa4Stage2Call{analysis}
			if tc.retry {
				r.trace.Calls = []qa4Stage2Call{attempt, analysis}
			}
			r.calls(r.trace.Calls)
			key, name, args := r.engine.coverageSameFieldLatestRead(r.state)
			if !tc.want {
				require.Empty(t, name)
				return
			}
			require.NotEmpty(t, key)
			require.Equal(t, "structured_query_series", name)
			require.Equal(t, field, args["series_id"])
			require.Equal(t, "2023-09-22", args["start_date"])
			require.Equal(t, args["start_date"], args["end_date"])
			require.Equal(t, analysis.Arguments["source_binding"], args["source_binding"])
			for _, item := range r.engine.evidenceCompletion.record.Evidence {
				if item.SeriesID == field {
					require.False(t, coverageAnalysisEndpoint(item), "the clipped result has no verified original cell")
				}
			}
			r.engine.evidenceCompletion.latestAttempts = map[string]bool{key: true}
			_, name, _ = r.engine.coverageSameFieldLatestRead(r.state)
			require.Empty(t, name)
			if tc.publish {
				latest := followUps[0]
				latest.Arguments["source_binding"] = analysis.Arguments["source_binding"]
				r.trace.Calls = append(r.trace.Calls, latest)
				r.calls([]qa4Stage2Call{latest})
				final := r.engine.terminalCoverageFailure(r.ctx, r.state, answerReviewUnavailableFallback)
				for _, expected := range []string{"2024-03-01", "2024-03-31", "没有有效记录", "CF.CZC", "2023-09-22", "17110", "B4705"} {
					require.Contains(t, final, expected)
				}
				require.NotContains(t, final, "棉花数据库.xlsx /")
			}
		})
	}
}

func TestQA4AnalysisEndpointCurrentWorkbookFieldProof(t *testing.T) {
	endpoint := coverageEvidence{Kind: "last_valid", Operation: "analyze_table", SourceDomain: "structured", Complete: true, LatestInScope: true,
		EvidenceID: "input", DateEvidenceID: "input", DateJSONPointer: "/date", CellJSONPointer: "/segment/cell", JSONPointer: "/value",
		SourceID: "dataset", DatasetID: "dataset", KnowledgeBaseID: "library", Sheet: "sheet", SeriesID: "field", Column: "B", Date: "2025-01-08",
		SnapshotHash: "published", WorkbookSnapshotHash: "original"}
	field := coverageEvidence{Kind: "directory", Operation: "list_series", SourceDomain: "structured", Bound: true, EvidenceID: "field", JSONPointer: "/series",
		SourceID: "dataset", KnowledgeBaseID: "library", Sheet: "sheet", SeriesID: "field", Column: "B", SnapshotHash: "original", LastValidDate: "2025-01-08",
		Raw: map[string]any{"source_context": map[string]any{"source_sha256": "original"}}}
	record := coverageRecord{Evidence: []coverageEvidence{field}, Snapshots: map[string]string{coverageSnapshotKey(field): "original"}}
	require.True(t, coverageCurrentEvidence(record, endpoint))
	for _, change := range []func(*coverageEvidence){
		func(item *coverageEvidence) { item.SeriesID = "other" },
		func(item *coverageEvidence) { item.Column = "C" },
		func(item *coverageEvidence) { item.Date = "2025-01-11" },
		func(item *coverageEvidence) { item.WorkbookSnapshotHash = "retained" },
	} {
		wrong := endpoint
		change(&wrong)
		require.False(t, coverageCurrentEvidence(record, wrong))
	}
	coverageObject(field.Raw)["source_dataset_id"] = "retained"
	require.False(t, coverageCurrentEvidence(record, endpoint))
	record.Snapshots[coverageSnapshotKey(field)] = "newer"
	endpoint.WorkbookBound = true
	require.False(t, coverageCurrentEvidence(record, endpoint))
}

func TestQA4AnalysisEndpointGroupReadsOneCompleteBoundSelection(t *testing.T) {
	const question, from, to, lastDay = "What are the readings this month?", "2025-02-01", "2025-02-28", "2025-01-31"
	engine := newTestEngine(t, &mockChat{})
	engine.toolRegistry = qa4ReplayRegistry()
	engine.turnReview.Query = question
	for _, tt := range []struct {
		name   string
		mutate func(args map[string]any, record *coverageRecord)
	}{
		{name: "entire native group"},
		{name: "different field snapshot", mutate: func(_ map[string]any, record *coverageRecord) { record.Evidence[1].SnapshotHash = "other" }},
		{name: "different endpoint day", mutate: func(_ map[string]any, record *coverageRecord) { record.Evidence[1].LastValidDate = "2025-01-30" }},
		{name: "different period", mutate: func(_ map[string]any, record *coverageRecord) { record.Evidence[1].PeriodTo = "2025-03-01" }},
		{name: "binding selected only one field", mutate: func(args map[string]any, _ *coverageRecord) {
			sourceNavigationRows(args["source_binding"])[0]["selected_ids"] = []any{"first"}
		}},
		{name: "one field is outside the selected group", mutate: func(args map[string]any, _ *coverageRecord) {
			args["series_ids"] = []any{"first", "other"}
		}},
		{name: "latest values already obtained", mutate: func(_ map[string]any, record *coverageRecord) {
			for _, gap := range record.Evidence {
				value := gap
				value.Kind, value.LatestInScope = "last_valid", true
				value.EvidenceID, value.DateEvidenceID = "read", "read"
				value.JSONPointer, value.DateJSONPointer, value.CellJSONPointer = "/value", "/date", "/cell"
				value.Date, value.Cell, value.Raw = lastDay, gap.Column+"8", json.Number("15")
				record.Evidence = append(record.Evidence, value)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			engine.evidenceCompletion.latestAttempts = nil
			ids := []any{"second", "first"}
			args := map[string]any{"knowledge_base_id": "library", "dataset_id": "dataset", "series_ids": ids,
				"source_question": question, "start_date": from, "end_date": to, "sql": "SELECT value FROM data",
				"source_binding": []any{map[string]any{"considered_ids": ids, "selected_ids": ids, "reason": "both selected fields"}}}
			call := types.ToolCall{ID: "group", Name: "structured_analyze_table", Args: args,
				Result: &types.ToolResult{Success: true, Data: map[string]any{"status": "ok"}}}
			state := &types.AgentState{RoundSteps: []types.AgentStep{{ToolCalls: []types.ToolCall{call}}}}
			first := coverageEvidence{Operation: "analyze_table", SourceDomain: "structured", Kind: "gap", ToolCallID: "group", EvidenceID: "group-analysis",
				KnowledgeBaseID: "library", DatasetID: "dataset", SourceID: "dataset", SnapshotHash: "published", WorkbookSnapshotHash: "original",
				WorkbookBound: true, Bound: true, Complete: true, SeriesID: "first", Column: "B", Sheet: "values", Metric: "measure",
				PeriodFrom: from, PeriodTo: to, LastValidDate: lastDay, JSONPointer: "/data/inputs/first/valid_count"}
			second := first
			second.SeriesID, second.Column, second.JSONPointer = "second", "C", "/data/inputs/second/valid_count"
			record := coverageRecord{Evidence: []coverageEvidence{first, second}}
			if tt.mutate != nil {
				tt.mutate(args, &record)
			}
			engine.evidenceCompletion.record = record
			key, name, read := engine.coverageLatestRead(state, true)
			if tt.mutate != nil {
				require.Empty(t, name)
				return
			}
			require.Equal(t, "structured_analyze_table", name)
			require.NotEmpty(t, key)
			require.Equal(t, args["source_binding"], read["source_binding"])
			require.Equal(t, ids, read["series_ids"])
			require.Equal(t, lastDay, read["start_date"])
			require.Equal(t, lastDay, read["end_date"])
			require.Equal(t, 2, read["limit"])
			require.Contains(t, read["sql"], "series_id, date, value, source_cell, date_cell")
			engine.evidenceCompletion.latestAttempts = map[string]bool{key: true}
			_, name, _ = engine.coverageLatestRead(state, true)
			require.Empty(t, name)
		})
	}
}
