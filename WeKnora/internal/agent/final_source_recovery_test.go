package agent

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/structuredresult"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestFinalSourceRecoveryLibrarySelector(t *testing.T) {
	recovery := finalSourceRecovery{
		tools: []chat.Tool{{Function: chat.FunctionDef{Name: "structured_query_series"}}},
		workbooks: map[[2]string]finalSourceWorkbook{
			{"library", "dataset"}: {fields: map[string]bool{"field": true}},
		},
	}
	for _, tc := range []struct {
		name, defaultLibrary, args string
		allowed                    bool
	}{
		{"original explicit scope", "", `{"knowledge_base_id":"library","dataset_id":"dataset","series_id":"field"}`, true},
		{"original omitted scope", "", `{"dataset_id":"dataset","series_id":"field"}`, false},
		{"single authorized default", "library", `{"dataset_id":"dataset","series_id":"field"}`, true},
		{"explicit foreign scope", "library", `{"knowledge_base_id":"other","dataset_id":"dataset","series_id":"field"}`, false},
		{"malformed explicit scope", "library", `{"knowledge_base_id":null,"dataset_id":"dataset","series_id":"field"}`, false},
		{"unknown field", "library", `{"dataset_id":"dataset","series_id":"unseen"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recovery.defaultLibrary = tc.defaultLibrary
			calls := []types.LLMToolCall{{ID: "final-read", Function: types.FunctionCall{Name: "structured_query_series", Arguments: tc.args}}}
			require.Equal(t, tc.allowed, recovery.validCalls(calls, &types.AgentState{}))
		})
	}
}

func TestFinalSourceRecoveryKeepsNativeSQLDates(t *testing.T) {
	input, err := decodeCitationJSON(`{
		"valid_count":3,"observation_coverage_scope":"loaded_analysis_inputs_before_user_sql",
		"observation_coverage":{"value_changes":{
			"scope":{"complete_filtered_input":true,"valid_value_count":3,"eligible_record_count":3},
			"minimum":{"date":"2025-01-06","value":5},"maximum":{"date":"2025-01-10","value":50}
		}}}`)
	require.NoError(t, err)
	columns := make([]structuredresult.Column, 2)
	for i, op := range []string{"min", "max"} {
		columns[i] = structuredresult.Column{Index: i, Dependency: structuredresult.Dependency{SourceColumns: []string{"value"}},
			Operation: map[string]any{"kind": "function", "name": op, "arguments": []any{map[string]any{"kind": "column_reference", "names": []any{"value"}}}}}
	}
	contract := &structuredresult.Contract{Capabilities: structuredresult.Capabilities{ColumnDependenciesVerified: true}, Columns: columns}
	field := coverageEvidence{EvidenceID: "read", Operation: "analyze_table", SourceTitle: "original.xlsx", SeriesID: "field",
		PeriodFrom: "2025-01-01", PeriodTo: "2025-01-31"}
	row := []any{json.Number("5.0"), json.Number("50.0")}
	// The later SQL population can have the same minimum. Only the loaded
	// input summary proves the earlier occurrence recorded in this fixture.
	data := map[string]any{"rows": []any{row}, "sql": "SELECT MIN(value), MAX(value) FROM data WHERE date > DATE '2025-01-07'"}
	observations := coverageAnalysisExtrema(field, input, data, contract, "/data/inputs/field")
	require.Len(t, observations, 2)
	require.Equal(t, "2025-01-06", observations[0].Date)
	require.Equal(t, "2025-01-10", observations[1].Date)
	require.Empty(t, observations[0].Cell)
	finding := (terminalCoverageDelivery{chinese: true, relaxed: true}).receiptFinding(observations[0], false)
	require.Contains(t, finding, "已载入记录")
	require.Contains(t, finding, "2025-01-06")
	row[0] = json.Number("7")
	observations = coverageAnalysisExtrema(field, input, data, contract, "/data/inputs/field")
	require.Len(t, observations, 1)
	require.Equal(t, "max", observations[0].Extremum)
	changes := coverageObject(coverageObject(input["observation_coverage"])["value_changes"])
	coverageObject(changes["scope"])["eligible_record_count"] = json.Number("2")
	require.Empty(t, coverageAnalysisExtrema(field, input, data, contract, "/data/inputs/field"))
}

func TestFinalSourceRecoveryOriginalDateBoundary(t *testing.T) {
	t.Setenv("WEKNORA_AGENT_EVIDENCE_COMPLETION", "false")
	t.Setenv("WEKNORA_AGENT_DOCUMENT_TABLE_EVIDENCE", "true")
	t.Setenv("WEKNORA_AGENT_STRUCTURED_TASK_PLAN", "true")
	t.Setenv("WEKNORA_AGENT_FINAL_READ_ANSWER", "false")
	const library, dataset, sheet, day = "library", "dataset", "selected sheet", "2025-01-10"
	for _, tc := range []struct {
		name, relaxed  string
		change         func(base *coverageEvidence, data map[string]any, fields *[]coverageEvidence)
		seen, complete bool
	}{
		{name: "read matching end date", relaxed: "true", seen: true, complete: true},
		{name: "strict projection", relaxed: "false"},
		{name: "from another worksheet", relaxed: "true", change: func(_ *coverageEvidence, _ map[string]any, fields *[]coverageEvidence) {
			(*fields)[0].Sheet = "different sheet"
		}},
		{name: "date cell is earlier than original extent", relaxed: "true", change: func(_ *coverageEvidence, data map[string]any, _ *[]coverageEvidence) {
			coverageObject(coverageObject(data["coverage"])[sheet])["source_used_range"] = "A1:C11"
		}},
		{name: "different snapshot", relaxed: "true", change: func(_ *coverageEvidence, data map[string]any, _ *[]coverageEvidence) {
			data["sha256"] = strings.Repeat("b", 64)
		}},
		{name: "fields have different coverages", relaxed: "true", seen: true, change: func(_ *coverageEvidence, _ map[string]any, fields *[]coverageEvidence) {
			other := (*fields)[0]
			other.SeriesID, other.LastObservedDate = "other field", "2025-02-19"
			*fields = append(*fields, other)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("WEKNORA_AGENT_VERIFIED_PUBLICATION", "true")
			t.Setenv("WEKNORA_AGENT_RELAXED_TERMINAL_EVIDENCE", tc.relaxed)
			snapshot := strings.Repeat("a", 64)
			base := coverageEvidence{Kind: "directory", Operation: "list_series", SourceDomain: "structured", Bound: true,
				KnowledgeBaseID: library, DatasetID: dataset, SourceID: dataset, SourceTitle: "original.xlsx", Sheet: sheet,
				EvidenceID: "directory", JSONPointer: "/data/series/0", SnapshotHash: snapshot}
			field := base
			field.SeriesID, field.LastObservedDate = "field", day
			field.Raw = map[string]any{"source_context": map[string]any{"formula_source": map[string]any{"date_cell": "A2"}}}
			fields := []coverageEvidence{field}
			base.Operation, base.Status, base.EvidenceID = "inspect_source", "ok", "original"
			data := map[string]any{
				"hash_verified": true, "sha256": snapshot, "source_file": base.SourceTitle,
				"coverage": map[string]any{sheet: map[string]any{"source_extent_status": "complete", "source_used_range": "A1:C10"}},
				"ranges":   []any{map[string]any{"sheet": sheet, "cells": []any{map[string]any{"sheet": sheet, "cell": "A10", "value_status": "original", "value": day}}}},
			}
			if tc.change != nil {
				tc.change(&base, data, &fields)
			}
			record := coverageRecord{Evidence: append(fields, coverageInspectionEvidence(base, nil, data)...)}
			target := taskCoverageTarget{StructuredTaskGoal: &StructuredTaskGoal{
				SourceMode: "named_first", SourceTime: "observed_period", PrimaryRef: "original.xlsx", Table: sheet,
				Period: map[string]any{"from": "2025-02-01", "to": "2025-02-28", "grain": "month"},
			}}
			e := &AgentEngine{}
			for range 2 {
				gap, ok := e.inspectedDateCoverageGap(t.Context(), target, record)
				require.Equal(t, tc.seen, ok)
				if ok {
					require.Equal(t, tc.complete, gap.Complete)
					require.Equal(t, day, gap.LastObservedDate)
					require.Equal(t, "A10", gap.DateCell)
					require.Empty(t, gap.SeriesID)
					finding := (terminalCoverageDelivery{chinese: true}).gapFinding(gap, map[string]bool{})
					require.Contains(t, finding, day)
					require.Equal(t, tc.complete, strings.Contains(finding, "没有"))
				}
				slices.Reverse(record.Evidence)
			}
		})
	}
}
