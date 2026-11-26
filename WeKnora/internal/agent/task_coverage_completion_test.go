package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func coverageCompletionFixture(mode string) (taskCoverageTarget, coverageRecord, coverageEvidence) {
	target := taskCoverageTarget{GoalIndex: 2, StructuredTaskGoal: &StructuredTaskGoal{
		SourceMode: mode, Metric: "benchmark", Period: map[string]any{"scope": "2024-06-05", "grain": "day"},
		Required: []string{"scoped_value"},
	}}
	primary := coverageBinding{GoalIndex: 2, Role: "primary", SourceID: "book", KnowledgeBaseID: "kb",
		Sheet: "sheet", SeriesID: "series", Column: "B", Grain: "day", PeriodFrom: "2024-06-05", PeriodTo: "2024-06-05"}
	gap := coverageEvidence{Kind: "gap", SourceID: "book", KnowledgeBaseID: "kb", SnapshotHash: "snapshot",
		Revision: "task", AuthScope: "permissions", Sheet: "sheet", SeriesID: "series", Column: "B", Grain: "day",
		PeriodFrom: "2024-06-05", PeriodTo: "2024-06-05", EvidenceID: "gap", JSONPointer: "/data", Complete: true, Bound: true,
		LastValidDate: "2024-05-31"}
	return target, coverageRecord{Revision: "task", AuthScope: "permissions", Bindings: []coverageBinding{primary}}, gap
}

func TestTaskCoverageCompletionOnlyScopedGap(t *testing.T) {
	target, input, gap := coverageCompletionFixture("only")
	value := gap
	value.Kind, value.EvidenceID, value.JSONPointer = "last_valid", "previous", "/data/rows/0"
	value.Date, value.DateCell, value.Cell, value.Raw = "2024-05-31", "A10", "B10", float64(0)
	value.DateEvidenceID, value.DateJSONPointer = "calendar", "/data/rows/0/date"
	wrong := value
	wrong.Date, wrong.DateCell, wrong.Cell = "2024-06-04", "A14", "B14"
	input.Evidence = []coverageEvidence{gap, wrong, value, value}
	result := evaluateTaskCoverage(target, input)[0]
	require.Equal(t, coverageSatisfied, result.State)
	require.Empty(t, result.NextRead)
	require.Zero(t, result.BudgetRemaining)
	date := value
	date.Raw, date.Cell, date.EvidenceID, date.JSONPointer = value.Date, value.DateCell, value.DateEvidenceID, value.DateJSONPointer
	require.Equal(t, []coverageFact{{"coverage_boundary", gap}, {"last_valid_date", date}, {"last_valid_value", value}}, result.PinnedFacts)

	for name, invalidate := range map[string]func(*coverageRecord, *coverageEvidence){
		"wrong field":        func(_ *coverageRecord, e *coverageEvidence) { e.SeriesID = "other" },
		"same header":        func(_ *coverageRecord, e *coverageEvidence) { e.Sheet = "other" },
		"wider range":        func(_ *coverageRecord, e *coverageEvidence) { e.PeriodFrom = "2024-06-01" },
		"wrong grain":        func(_ *coverageRecord, e *coverageEvidence) { e.Grain = "week" },
		"incomplete result":  func(_ *coverageRecord, e *coverageEvidence) { e.Complete = false },
		"continuation":       func(_ *coverageRecord, e *coverageEvidence) { offset := 10; e.NextOffset = &offset },
		"failed execution":   func(_ *coverageRecord, e *coverageEvidence) { e.Kind = "error" },
		"unbound":            func(_ *coverageRecord, e *coverageEvidence) { e.Bound = false },
		"missing auth stamp": func(_ *coverageRecord, e *coverageEvidence) { e.AuthScope = "" },
		"different task":     func(_ *coverageRecord, e *coverageEvidence) { e.Revision = "prior" },
		"new snapshot":       func(r *coverageRecord, _ *coverageEvidence) { r.Snapshots = map[string]string{"kb\x00book": "new"} },
	} {
		t.Run(name, func(t *testing.T) {
			mutatedInput, mutatedGap := input, gap
			invalidate(&mutatedInput, &mutatedGap)
			mutatedInput.Evidence = []coverageEvidence{mutatedGap, value}
			result := evaluateTaskCoverage(target, mutatedInput)[0]
			require.Equal(t, coverageOpen, result.State)
			require.NotEqual(t, "discover", result.NextRead)
			require.Empty(t, result.PinnedFacts)
		})
	}
}

func TestTaskCoverageCompletionBoundedSupplement(t *testing.T) {
	target, input, gap := coverageCompletionFixture("named_first")
	nav := coverageEvidence{Kind: "navigation", EvidenceID: "search", JSONPointer: "/data/results/0",
		SourceID: "report", KnowledgeBaseID: "kb", Page: 5, Bound: true, AuthScope: input.AuthScope, Revision: input.Revision}
	input.Evidence = []coverageEvidence{nav}
	require.Equal(t, "primary_unverified", evaluateTaskCoverage(target, input)[0].Reason)
	input.Evidence = append(input.Evidence, gap)
	discovery := evaluateTaskCoverage(target, input)[0]
	require.Equal(t, "discover", discovery.NextRead)
	require.Equal(t, 2, discovery.BudgetRemaining)
	discovered := coverageRecoveryReceipt{GapID: discovery.GapID, Action: "discover", ToolCallID: "search-call", Succeeded: true}
	input.Attempts = []coverageRecoveryReceipt{discovered}
	unrelated := evaluateTaskCoverage(target, input)[0]
	require.Equal(t, coverageExhausted, unrelated.State)
	require.Equal(t, &discovered, unrelated.AttemptReceipt)
	require.Len(t, unrelated.PinnedFacts, 1)

	modelSelection := coverageBinding{GoalIndex: 2, Role: "supplement", Kind: "body", SourceID: "report",
		KnowledgeBaseID: "kb", Page: 5, Grain: "day", PeriodFrom: "2024-06-05", PeriodTo: "2024-06-05"}
	input.Bindings = append(input.Bindings, modelSelection)
	read := evaluateTaskCoverage(target, input)[0]
	require.Equal(t, discovery.GapID, read.GapID)
	require.Equal(t, coverageOpen, read.State)
	require.Equal(t, "read", read.NextRead)
	require.Equal(t, 1, read.BudgetRemaining)
	input.Attempts = append(input.Attempts, coverageRecoveryReceipt{GapID: read.GapID, Action: "read", ToolCallID: "read-call", Reason: "service_failed"})
	failed := evaluateTaskCoverage(target, input)[0]
	require.Equal(t, coverageExhausted, failed.State)
	require.Equal(t, "service_failed", failed.Reason)
	require.Zero(t, failed.BudgetRemaining)

	body := nav
	body.Kind, body.Operation, body.ChunkID, body.SnapshotHash, body.Complete = "body", "search_knowledge", "body-chunk", "material-hash", true
	body.Raw = "2024-06-05 benchmark: 15 tonnes"
	body.ContentHash = candidateTextDigest(body.Raw.(string))
	modelSelection.EvidenceID, modelSelection.Span, modelSelection.Metric = body.EvidenceID, body.Raw.(string), "benchmark"
	modelSelection.RawClaim, modelSelection.Date, modelSelection.Unit = "15", "2024-06-05", "tonnes"
	input.Bindings[1] = modelSelection
	input.Attempts = nil // The actual reusable body was already available in search.
	input.Evidence = append(input.Evidence, body, body)
	success := evaluateTaskCoverage(target, input)[0]
	require.Equal(t, coverageSatisfied, success.State)
	require.Equal(t, "matching_supplement", success.Reason)
	require.Len(t, success.PinnedFacts, 2)
	require.Equal(t, "supplement", success.PinnedFacts[1].Role)
	require.Equal(t, body.Raw, success.PinnedFacts[1].Evidence.Raw)

	for name, mutate := range map[string]func(*coverageEvidence){
		"wrong physical page": func(e *coverageEvidence) { e.Page = 4 },
		"wrong grain":         func(e *coverageEvidence) { e.Grain = "week" },
		"navigation only":     func(e *coverageEvidence) { e.Kind = "navigation" },
		"edited content":      func(e *coverageEvidence) { e.Raw = "unrelated " + e.Raw.(string) },
		"old authorisation":   func(e *coverageEvidence) { e.AuthScope = "revoked" },
	} {
		t.Run(name, func(t *testing.T) {
			mutatedInput, mutatedBody := input, body
			mutate(&mutatedBody)
			mutatedInput.Evidence = []coverageEvidence{gap, nav, mutatedBody}
			result := evaluateTaskCoverage(target, mutatedInput)[0]
			require.Equal(t, coverageOpen, result.State)
			require.Len(t, result.PinnedFacts, 1)
		})
	}
}

func TestTaskCoverageCompletionMultipleFieldsAndPeriods(t *testing.T) {
	target, input, value := coverageCompletionFixture("only")
	value.Kind, value.Cell, value.Date, value.Raw = "cell", "B10", "2024-06-05", 30
	second := input.Bindings[0]
	second.SeriesID, second.Column, second.RequireUnit = "other-series", "C", true
	input.Bindings = append(input.Bindings, second)
	input.Evidence = []coverageEvidence{value}
	result := evaluateTaskCoverage(target, input)
	require.Len(t, result, 2)
	require.Equal(t, coverageSatisfied, result[0].State)
	require.Equal(t, coverageOpen, result[1].State)
	other := value
	other.SeriesID, other.Column, other.Cell, other.JSONPointer = "other-series", "C", "C10", "/data/cells/1"
	input.Evidence = append(input.Evidence, other)
	require.Equal(t, coverageOpen, evaluateTaskCoverage(target, input)[1].State)
	other.Unit, other.UnitBasis = "tonnes", "C1"
	input.Evidence[1] = other
	require.Equal(t, coverageSatisfied, evaluateTaskCoverage(target, input)[1].State)

	target.Period = map[string]any{"count": 2, "grain": "day"}
	result = evaluateTaskCoverage(target, input)
	require.Equal(t, coverageOpen, result[2].State)
	require.Equal(t, []string{"requested_periods"}, result[2].OpenItems)

	target.Period = map[string]any{"scope": "2024-06-06", "grain": "day"}
	result = evaluateTaskCoverage(target, input)
	require.Equal(t, coverageOpen, result[0].State)
	require.Empty(t, result[0].PinnedFacts)
}

func TestTaskCoverageCompletionNavigationAndBodyAreDifferentReceipts(t *testing.T) {
	target, input, source := coverageCompletionFixture("only")
	nav := coverageEvidence{Kind: "navigation", SourceID: "report", KnowledgeBaseID: "kb", Page: 4,
		Revision: input.Revision, AuthScope: input.AuthScope, EvidenceID: "navigation", JSONPointer: "/data/results/0"}
	binding := coverageBinding{GoalIndex: target.GoalIndex, Role: "primary", Kind: "navigation", Provisional: true,
		SourceID: nav.SourceID, KnowledgeBaseID: nav.KnowledgeBaseID, Page: 4,
		PeriodFrom: source.PeriodFrom, PeriodTo: source.PeriodTo, Grain: "day"}
	input.Bindings, input.Evidence = []coverageBinding{binding}, []coverageEvidence{nav}
	before := evaluateTaskCoverage(target, input)[0]
	require.Equal(t, coverageOpen, before.State)
	require.Equal(t, "read", before.NextRead)
	require.Equal(t, 1, before.BudgetRemaining)
	input.Attempts = []coverageRecoveryReceipt{{GapID: before.GapID, Action: "read", ToolCallID: "duplicate-navigation", Succeeded: true}}
	exhausted := evaluateTaskCoverage(target, input)[0]
	require.Equal(t, coverageExhausted, exhausted.State)
	require.Zero(t, exhausted.BudgetRemaining)
	body := nav
	body.Kind, body.EvidenceID, body.ChunkID, body.SnapshotHash = "body", "page", "chunk", "file-hash"
	body.Complete, body.Bound = true, true
	body.Raw = "标题。2024年6月5日，指标原值为8。"
	body.ContentHash = candidateTextDigest(body.Raw.(string))
	declared := binding
	declared.Kind, declared.Provisional, declared.EvidenceID = "body", false, body.EvidenceID
	declared.Span, declared.Date, declared.RawClaim = "2024年6月5日，指标原值为8。", "2024年6月5日", "8"
	input.Bindings = append(input.Bindings, declared)
	input.Evidence = append(input.Evidence, body)
	result := evaluateTaskCoverage(target, input)[0]
	require.Equal(t, before.GapID, result.GapID)
	require.Equal(t, coverageSatisfied, result.State)
	require.Len(t, result.PinnedFacts, 1)
	require.Equal(t, 3, *result.PinnedFacts[0].Evidence.SpanStart)
	body.Page = 6
	input.Evidence[1] = body
	require.Equal(t, coverageExhausted, evaluateTaskCoverage(target, input)[0].State)
}

func TestTaskCoverageCompletionDirectoryNeedsCatalogGoal(t *testing.T) {
	target, input, evidence := coverageCompletionFixture("only")
	evidence.Kind = "directory"
	input.Bindings[0].Kind = "directory"
	input.Evidence = []coverageEvidence{evidence}
	require.Equal(t, coverageOpen, evaluateTaskCoverage(target, input)[0].State)
	target.SourceTime = "catalog_snapshot"
	require.Equal(t, coverageSatisfied, evaluateTaskCoverage(target, input)[0].State)
}

func TestTaskCoverageCompletionLatestRequiresUpperBoundary(t *testing.T) {
	target, input, value := coverageCompletionFixture("only")
	target.SourceTime = "latest_observation"
	target.Period = map[string]any{"grain": "day"}
	input.Bindings[0].PeriodFrom, input.Bindings[0].PeriodTo = "", ""
	value.Kind, value.Cell, value.Date, value.Raw = "cell", "B10", "2024-06-05", 30
	value.PeriodFrom, value.PeriodTo = "", ""
	value.LastValidDate, value.LastObservedDate = "2024-06-05", "2024-06-12"
	input.Evidence = []coverageEvidence{value}
	require.Equal(t, coverageSatisfied, evaluateTaskCoverage(target, input)[0].State)
	value.Date = "2024-06-04"
	input.Evidence[0] = value
	require.Equal(t, coverageOpen, evaluateTaskCoverage(target, input)[0].State)
	value.LatestInScope, value.PeriodFrom, value.PeriodTo = true, "2024-06-01", "2024-06-04"
	input.Bindings[0].PeriodFrom, input.Bindings[0].PeriodTo = value.PeriodFrom, value.PeriodTo
	input.Evidence[0] = value
	require.Equal(t, coverageSatisfied, evaluateTaskCoverage(target, input)[0].State)
}

func TestTaskCoverageCompletionRefinesProvisionalDirectory(t *testing.T) {
	target, input, value := coverageCompletionFixture("only")
	value.Kind, value.Cell, value.Date, value.Raw = "cell", "B10", "2024-06-05", 30
	field := input.Bindings[0]
	field.Provisional = true
	directory := field
	directory.Sheet, directory.SeriesID, directory.Column, directory.SnapshotHash = "", "", "", value.SnapshotHash
	input.Bindings, input.Evidence = []coverageBinding{directory, field}, []coverageEvidence{value}
	result := evaluateTaskCoverage(target, input)
	require.Len(t, result, 1)
	require.Equal(t, coverageSatisfied, result[0].State)
	require.Equal(t, value, result[0].PinnedFacts[0].Evidence)

	for name, mutate := range map[string]func(*coverageBinding){
		"other source":   func(b *coverageBinding) { b.SourceID = "other" },
		"other goal":     func(b *coverageBinding) { b.GoalIndex++ },
		"other period":   func(b *coverageBinding) { b.PeriodTo = "2024-06-07" },
		"other snapshot": func(b *coverageBinding) { b.SnapshotHash = "old" },
	} {
		t.Run(name, func(t *testing.T) {
			other := field
			mutate(&other)
			input.Bindings[1] = other
			require.Equal(t, coverageOpen, evaluateTaskCoverage(target, input)[0].State)
		})
	}
	directory.Page, field.Page = 7, 8
	require.False(t, coverageRefinesBinding(directory, field))
}

func TestTaskCoverageCompletionBodyRequiresWholeClaim(t *testing.T) {
	for _, test := range []struct {
		name, body, span, claim, unit string
		valid                         bool
	}{
		{"complete", "原值为50吨", "原值为50吨", "50", "吨", true},
		{"missing value", "原值为50吨", "原值为50吨", "", "", false},
		{"partial integer", "原值为50吨", "原值为50吨", "5", "吨", false},
		{"clipped span", "原值为50吨", "原值为5", "5", "", false},
		{"partial fraction", "Rate: 50.6%", "Rate: 50.6%", "50", "", false},
		{"unsigned fragment", "Change: −50", "Change: −50", "50", "", false},
		{"group fragment", "Total: 1,500.3", "Total: 1,500.3", "500.3", "", false},
		{"date fragment", "Measured on 2024-06-05", "Measured on 2024-06-05", "2024", "", false},
		{"written value", "Duration: thirteen hours", "Duration: thirteen hours", "thirteen", "hours", true},
		{"partial word", "The increase was thirteenfold", "The increase was thirteenfold", "thirteen", "", false},
		{"adjacent declared unit", "Distance: 50km", "Distance: 50km", "50", "km", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence := coverageEvidence{Raw: test.body, ContentHash: candidateTextDigest(test.body), ChunkID: "body", SnapshotHash: "hash", Complete: true}
			binding := coverageBinding{EvidenceID: "read", Span: test.span, RawClaim: test.claim, Unit: test.unit}
			_, ok := coverageBodyFact(binding, evidence, true)
			require.Equal(t, test.valid, ok)
		})
	}
	const context = "The station is operated by the city."
	_, ok := coverageBodyFact(coverageBinding{EvidenceID: "read", Span: context}, coverageEvidence{
		Raw: context, ContentHash: candidateTextDigest(context), ChunkID: "body", SnapshotHash: "hash", Complete: true,
	}, false)
	require.True(t, ok)
}

func TestTaskCoverageCompletionBodyRangeRequiresActualPeriod(t *testing.T) {
	const span = "2024-06-05 to 2024-06-11, weekly total: 128."
	evidence := coverageEvidence{Raw: span, ContentHash: candidateTextDigest(span), ChunkID: "body", SnapshotHash: "hash", Complete: true}
	binding := coverageBinding{EvidenceID: "read", Span: span, RawClaim: "128", GrainBasis: "weekly", Grain: "week",
		PeriodFrom: "2024-06-05", PeriodTo: "2024-06-11", Date: "2024-06-05"}
	_, ok := coverageBodyFact(binding, evidence, true)
	require.False(t, ok)
	binding.Date = "2024-06-05 to 2024-06-11"
	_, ok = coverageBodyFact(binding, evidence, true)
	require.True(t, ok)
	binding.Grain = "day"
	_, ok = coverageBodyFact(binding, evidence, true)
	require.False(t, ok)
}

func TestTaskCoverageCompletionDailyRangeRetainsLoneValue(t *testing.T) {
	target, input, value := coverageCompletionFixture("only")
	target.Period = map[string]any{"from": "2024-06-05", "to": "2024-06-08", "grain": "day"}
	input.Bindings[0].PeriodTo = "2024-06-08"
	value.Kind, value.Cell, value.Date, value.Raw, value.PeriodTo = "cell", "B10", "2024-06-05", 30, "2024-06-08"
	input.Evidence = []coverageEvidence{value, value}
	result := evaluateTaskCoverage(target, input)[0]
	require.Equal(t, coverageOpen, result.State)
	require.Equal(t, []coverageFact{{"scoped_value", value}}, result.PinnedFacts)
	value.RangeComplete = true
	input.Evidence = []coverageEvidence{value}
	require.Equal(t, coverageSatisfied, evaluateTaskCoverage(target, input)[0].State)
}

func TestTaskCoverageCompletionNativeRangeUsesActualCounts(t *testing.T) {
	const data = `{"rows_are":"observations","scope_record_count":2,"record_count":2,"valid_count":1,"returned_row_count":2,
	  "series":{"series_id":"series","frequency":"daily"},
	  "filters":{"series_id":"series","operation":"rows","start_date":"2024-06-05","end_date":"2024-06-08","value_filters":[]},
	  "rows":[{"source_cell":"B10","date":"2024-06-05","value_status":"valid","value":30},
	          {"source_cell":"B11","date":"2024-06-06","value_status":"blank","value":null}]}`
	base := coverageEvidence{Status: "ok", SourceID: "book", SnapshotHash: "hash", Sheet: "sheet", Bound: true, Complete: true}
	args := map[string]any{"series_id": "series", "start_date": "2024-06-05", "end_date": "2024-06-08"}
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "complete with one valid cell"},
		{name: "count mismatch", mutate: func(d map[string]any) { d["scope_record_count"] = json.Number("3") }},
		{name: "valid count mismatch", mutate: func(d map[string]any) { d["valid_count"] = json.Number("2") }},
		{name: "returned count mismatch", mutate: func(d map[string]any) { d["returned_row_count"] = json.Number("1") }},
		{name: "ranked sample", mutate: func(d map[string]any) { coverageObject(d["filters"])["operation"] = "top" }},
		{name: "filtered", mutate: func(d map[string]any) {
			coverageObject(d["filters"])["value_filters"] = []any{map[string]any{"op": "gt", "value": json.Number("0")}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decoded, err := decodeCitationJSON(data)
			require.NoError(t, err)
			if tc.mutate != nil {
				tc.mutate(decoded)
			}
			values := coverageSeriesEvidence(base, args, decoded, nil)
			require.Len(t, values, 1)
			require.Equal(t, tc.mutate == nil, values[0].RangeComplete)
		})
	}
}
