package agent

import (
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/structuredresult"
)

// The input summary reports occurrence dates independently of the model's
// SQL. Keep an occurrence when the executed calculation names the same
// extremum and its value agrees with the full valid input population.
func coverageAnalysisExtrema(item coverageEvidence, input, data map[string]any, contract *structuredresult.Contract, path string) []coverageEvidence {
	if !coverageExactRange(item) || !contract.Capabilities.ColumnDependenciesVerified ||
		input["observation_coverage_scope"] != "loaded_analysis_inputs_before_user_sql" {
		return nil
	}
	coverage := coverageObject(input["observation_coverage"])
	changes := coverageObject(coverage["value_changes"])
	scope := coverageObject(changes["scope"])
	validCount := input["valid_count"]
	rows, _ := data["rows"].([]any)
	if len(rows) != 1 || citationInt(validCount) < 1 || scope["complete_filtered_input"] != true ||
		scope["valid_value_count"] != validCount || scope["eligible_record_count"] != validCount {
		return nil
	}
	row, _ := rows[0].([]any)
	var result []coverageEvidence
	for _, column := range contract.Columns {
		if column.Index < 0 || column.Index >= len(row) || column.Operation["kind"] != "function" ||
			!slices.Equal(column.Dependency.SourceColumns, []string{"value"}) {
			continue
		}
		op := sourceNavigationString(column.Operation, "name")
		if op != "min" && op != "max" {
			continue
		}
		arguments := sourceNavigationRows(column.Operation["arguments"])
		if len(arguments) != 1 || arguments[0]["kind"] != "column_reference" || !coverageContainsString(arguments[0]["names"], "value") {
			continue
		}
		observation := coverageObject(changes[op+"imum"])
		date := sourceNavigationString(observation, "date")
		if !coverageAnalysisDay(date) || date < item.PeriodFrom || date > item.PeriodTo ||
			!coverageSameOriginalValue(observation["value"], row[column.Index]) {
			continue
		}
		extremum := item
		extremum.Kind, extremum.Extremum, extremum.Date, extremum.Raw, extremum.Complete = "derived", op, date, observation["value"], true
		extremum.DateEvidenceID = item.EvidenceID
		extremum.DateJSONPointer = path + "/observation_coverage/value_changes/" + op + "imum/date"
		extremum.JSONPointer = "/data/rows/0/" + strconv.Itoa(column.Index)
		result = append(result, extremum)
	}
	return result
}

// The native input summary gives a value and date before the model's SQL runs.
// Its matching status segment supplies the original value cell.
func coverageAnalysisLastValid(item coverageEvidence, input map[string]any) (coverageEvidence, bool) {
	series := coverageObject(input["series"])
	if item.Sheet == "" || item.Metric == "" || item.Column == "" || item.DateMode == "period" || item.DateMode == "none" ||
		input["observation_coverage_scope"] != "loaded_analysis_inputs_before_user_sql" ||
		sourceNavigationString(series, "source_dataset_id") != "" && series["source_dataset_id"] != item.DatasetID {
		return coverageEvidence{}, false
	}
	date := item.LastValidDate
	if !coverageAnalysisDay(date) || input["valid_last_date"] != date ||
		item.PeriodFrom != "" && (!coverageAnalysisDay(item.PeriodFrom) || date < item.PeriodFrom) ||
		item.PeriodTo != "" && (!coverageAnalysisDay(item.PeriodTo) || date > item.PeriodTo) {
		return coverageEvidence{}, false
	}
	coverage := coverageObject(input["observation_coverage"])
	changes := coverageObject(coverage["value_changes"])
	scope := coverageObject(changes["scope"])
	last := coverageObject(changes["last"])
	value, numeric := last["value"].(json.Number)
	parsed, err := value.Float64()
	recordCount, _ := input["input_count"].(json.Number)
	validCount, _ := input["valid_count"].(json.Number)
	records, valid, eligible := citationInt(recordCount), citationInt(validCount), citationInt(scope["eligible_record_count"])
	if !numeric || err != nil || math.IsInf(parsed, 0) || math.IsNaN(parsed) || last["date"] != date ||
		scope["complete_filtered_input"] != true || scope["eligible_end_date"] != date || eligible < 1 || eligible > valid || valid > records ||
		coverage["scope_record_count"] != recordCount || scope["record_count"] != recordCount ||
		coverage["scope_valid_count"] != validCount || scope["valid_value_count"] != validCount {
		return coverageEvidence{}, false
	}
	actualFirst, actualLast := sourceNavigationString(input, "actual_first_date"), sourceNavigationString(input, "actual_last_date")
	if !coverageAnalysisDay(actualFirst) || !coverageAnalysisDay(actualLast) || actualFirst > date || date > actualLast ||
		item.PeriodFrom != "" && actualFirst < item.PeriodFrom || item.PeriodTo != "" && actualLast > item.PeriodTo ||
		coverage["scope_start_date"] != actualFirst || coverage["scope_end_date"] != actualLast {
		return coverageEvidence{}, false
	}
	segments, _ := coverage["segments"].([]any)
	if len(segments) == 0 || len(segments) > 64 {
		return coverageEvidence{}, false
	}
	path := "/data/inputs/" + coveragePointerKey(item.SeriesID) + "/observation_coverage"
	if item.Subject == "" {
		item.Subject = coverageAnalysisSubject(coverageObject(series["dimensions"]))
	}
	if coverage["truncated"] == true && citationInt(coverage["total_segment_count"]) > len(segments) {
		// The complete input still proves the date even though the address
		// segment was omitted. Keep the number out of the evidence role.
		item.Kind, item.Date, item.Complete = "analysis_last_date", date, true
		item.JSONPointer = path + "/value_changes/last/date"
		return item, true
	}
	if coverage["truncated"] != false || coverage["total_segment_count"] != json.Number(strconv.Itoa(len(segments))) {
		return coverageEvidence{}, false
	}
	var cell, cellPointer string
	for index, raw := range segments {
		segment := coverageObject(raw)
		if segment["undated_boundary"] == true && segment["start_date"] == nil && segment["end_date"] == nil {
			continue
		}
		start, end := sourceNavigationString(segment, "start_date"), sourceNavigationString(segment, "end_date")
		if !coverageAnalysisDay(start) || !coverageAnalysisDay(end) || start > end || segment["has_value"] == true && end > date {
			return coverageEvidence{}, false
		}
		if date < start || date > end {
			continue
		}
		address := sourceNavigationString(segment, "last_cell")
		column, row := coverageCellPosition(address)
		segmentCount, _ := segment["valid_count"].(json.Number)
		if cell != "" || end != date || segment["duplicate_date_boundary"] != false || segment["undated_boundary"] != false ||
			segment["value_status"] != "valid" || segment["has_value"] != true || citationInt(segmentCount) < 1 ||
			segmentCount != segment["record_count"] || column != item.Column || citationInt(json.Number(row)) > 1048576 {
			return coverageEvidence{}, false
		}
		cell, cellPointer = address, path+"/segments/"+strconv.Itoa(index)+"/last_cell"
	}
	if cell == "" {
		return coverageEvidence{}, false
	}
	item.Kind, item.Complete, item.LatestInScope = "last_valid", true, true
	item.Date, item.Cell, item.Raw = date, cell, value
	item.JSONPointer, item.CellJSONPointer = path+"/value_changes/last/value", cellPointer
	item.DateEvidenceID, item.DateJSONPointer = item.EvidenceID, path+"/value_changes/last/date"
	return item, true
}

func coverageAnalysisDay(date string) bool {
	_, err := time.Parse(time.DateOnly, date)
	return err == nil
}

func coverageAnalysisSubject(dimensions map[string]any) string {
	var keys, labels []string
	for key := range dimensions {
		if key != "block" { // The importer groups columns by block within a sheet.
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	for _, key := range keys {
		if label := sourceNavigationString(dimensions, key); label != "" && !slices.Contains(labels, label) {
			labels = append(labels, label)
		}
	}
	return strings.Join(labels, " / ")
}

func coverageExplicitAnalysisField(args map[string]any, id string) bool {
	for _, binding := range sourceNavigationRows(args["source_binding"]) {
		selected, _ := binding["selected_ids"].([]any)
		considered, _ := binding["considered_ids"].([]any)
		if sourceNavigationString(binding, "reason") != "" && slices.Contains(selected, any(id)) && slices.Contains(considered, any(id)) {
			return true // The successful native executor already validated the binding against the catalog.
		}
	}
	return false
}

func coverageAnalysisGap(endpoint coverageEvidence, from, to, grain string) (coverageEvidence, bool) {
	if !(coverageAnalysisEndpoint(endpoint) || coverageAnalysisMissingCell(endpoint)) || !coverageAnalysisDay(from) || !coverageAnalysisDay(to) || from > to ||
		endpoint.Date >= from || endpoint.PeriodTo != "" && endpoint.PeriodTo < to {
		return coverageEvidence{}, false
	}
	gap := endpoint
	gap.Kind, gap.Status = "gap", "outside_published_valid_observations"
	gap.Date, gap.Cell, gap.CellJSONPointer, gap.DateEvidenceID, gap.DateJSONPointer, gap.ResultJSONPointer = "", "", "", "", "", ""
	gap.LatestInScope, gap.LastValidRef, gap.Raw = false, endpoint.EvidenceID, endpoint.Date
	if coverageAnalysisMissingCell(endpoint) {
		gap.LastValidRef = "" // A different current receipt may already prove this field's same original cell.
	}
	gap.JSONPointer, gap.CitationID = "/data/inputs/"+coveragePointerKey(endpoint.SeriesID)+"/valid_last_date", ""
	gap.PeriodFrom, gap.PeriodTo, gap.Grain = from, to, grain
	return gap, true
}

func coverageAnalysisEndpoint(item coverageEvidence) bool {
	return item.Operation == "analyze_table" && item.Kind == "last_valid" && item.Complete && item.LatestInScope &&
		item.CellJSONPointer != "" && item.DateEvidenceID == item.EvidenceID && item.DateJSONPointer != ""
}

func coverageAnalysisMissingCell(item coverageEvidence) bool {
	return item.Operation == "analyze_table" && item.Kind == "analysis_last_date"
}

func coverageAnalysisEmptyInput(item coverageEvidence) bool {
	return coverageAnalysisLatestBoundary(item) && item.JSONPointer == "/data/inputs/"+coveragePointerKey(item.SeriesID)+"/valid_count"
}

func coverageAnalysisLatestBoundary(item coverageEvidence) bool {
	return item.Operation == "analyze_table" && item.Kind == "gap" && item.Bound && item.Complete &&
		item.Sheet != "" && item.Column != "" && item.DateMode != "period" && item.DateMode != "none" &&
		item.WorkbookSnapshotHash != "" && coverageExactRange(item) && coverageAnalysisDay(item.LastValidDate) && item.LastValidDate < item.PeriodFrom &&
		(item.Status == "outside_published_valid_observations" || item.JSONPointer == "/data/inputs/"+coveragePointerKey(item.SeriesID)+"/valid_count")
}

func coverageCurrentAnalysisWorkbook(record coverageRecord, item coverageEvidence) bool {
	original := item
	original.Operation = "list_series"
	if current := record.Snapshots[coverageSnapshotKey(original)]; current != "" && current != item.WorkbookSnapshotHash {
		return false
	}
	if item.WorkbookBound {
		return true
	}
	return slices.ContainsFunc(record.Evidence, func(field coverageEvidence) bool {
		if field.Kind != "directory" || field.Operation != "list_series" || !field.Bound || !coverageCurrentEvidence(record, field) ||
			field.SourceID != item.SourceID || field.KnowledgeBaseID != item.KnowledgeBaseID || field.Sheet != item.Sheet ||
			field.SeriesID != item.SeriesID || field.Column != item.Column || field.SnapshotHash != item.WorkbookSnapshotHash ||
			field.LastValidDate != firstNonEmptyAgent(item.Date, item.LastValidDate) {
			return false
		}
		metadata := coverageObject(field.Raw)
		return coverageObject(metadata["source_context"])["source_sha256"] == item.WorkbookSnapshotHash &&
			(sourceNavigationString(metadata, "source_dataset_id") == "" || metadata["source_dataset_id"] == item.DatasetID)
	})
}

func (e *AgentEngine) plannedAnalysisGap(target taskCoverageTarget, record coverageRecord, item coverageEvidence) (coverageEvidence, bool) {
	if target.StructuredTaskGoal == nil || target.SourceMode != "named_first" && target.SourceMode != "only" ||
		target.VersionTask == "compare_sources" || target.VersionTask == "compare_versions" ||
		slices.Equal(target.Required, []string{"optional"}) ||
		!slices.Contains(target.OnVerifiedGap, "last_valid_same_field") || target.PrimaryRef == "" || target.Metric == "" ||
		target.SourceTime != "observed_period" || target.Delivery["kind"] != "text" {
		return coverageEvidence{}, false
	}
	if selected, matches := coveragePrimaryField(target, record, item); !matches && (selected || item.Metric != target.Metric) {
		return coverageEvidence{}, false
	}
	from, to, grain := coverageTargetDateRange(target)
	gap, ok := coverageAnalysisGap(item, from, to, grain)
	if !ok && coverageAnalysisLatestBoundary(item) && item.PeriodFrom == from && item.PeriodTo == to && coverageCurrentAnalysisWorkbook(record, item) {
		gap, ok = item, true
		gap.Grain = grain
	}
	if !ok || !item.Bound || !coverageCurrentEvidence(record, item) ||
		!e.coverageScopeAllows(item.KnowledgeBaseID, item.KnowledgeID) || !e.coverageMatchesPrimaryReference(target, item) {
		return coverageEvidence{}, false
	}
	selected := func(binding coverageBinding) bool {
		return binding.GoalIndex == target.GoalIndex && binding.Role == "primary"
	}
	if slices.ContainsFunc(record.Bindings, selected) && !slices.ContainsFunc(record.Bindings, func(binding coverageBinding) bool {
		return selected(binding) && binding.SourceID == item.SourceID && coverageEqual(binding.KnowledgeBaseID, item.KnowledgeBaseID) &&
			(binding.SnapshotHash == item.WorkbookSnapshotHash && binding.Provisional || coverageEqual(binding.SnapshotHash, item.SnapshotHash)) &&
			coverageLocation(binding, item)
	}) {
		return coverageEvidence{}, false
	}
	return gap, true
}

func (e *AgentEngine) plannedAnalysisGaps(target taskCoverageTarget, record coverageRecord) []coverageEvidence {
	var gaps []coverageEvidence
	for _, item := range record.Evidence {
		if gap, ok := e.plannedAnalysisGap(target, record, item); ok {
			gaps = append(gaps, gap)
		}
	}
	return gaps
}

// An earlier native analysis preserves the executed request's dates.
// Only a later complete analysis of the same selected fields supplies facts.
func coverageAnalysisRetriedScope(attempts []coverageAttempt, endpoint coverageEvidence, question string) (string, string) {
	for index, current := range attempts {
		if current.EvidenceID != endpoint.EvidenceID || !current.Success ||
			question == "" || current.Args["source_question"] != question || !coverageExplicitAnalysisField(current.Args, endpoint.SeriesID) {
			continue
		}
		var from string
		for _, prior := range attempts[:index] {
			if !prior.Success || prior.Operation != "analyze_table" || prior.Args["dataset_id"] != endpoint.DatasetID ||
				prior.Args["knowledge_base_id"] != endpoint.KnowledgeBaseID ||
				coverageObject(prior.result.Data["source_context"])["knowledge_base_id"] != endpoint.KnowledgeBaseID ||
				prior.Args["end_date"] != endpoint.PeriodTo || !coverageSameAnalysisFields(prior.Args, current.Args) {
				continue
			}
			if asked := sourceNavigationString(prior.Args, "source_question"); asked != "" && asked != question {
				continue
			}
			start := sourceNavigationString(prior.Args, "start_date")
			if !coverageAnalysisDay(start) || from != "" && start != from {
				return "", ""
			}
			from = start
		}
		return from, endpoint.PeriodTo
	}
	return "", ""
}

func coverageSameAnalysisFields(first, last map[string]any) bool {
	a, _ := first["series_ids"].([]any)
	b, _ := last["series_ids"].([]any)
	if len(a) == 0 || len(a) != len(b) {
		return false
	}
	seen := make(map[string]bool, len(a))
	for _, raw := range a {
		id, ok := raw.(string)
		if !ok || id == "" || seen[id] || !slices.Contains(b, any(id)) {
			return false
		}
		seen[id] = true
	}
	return true
}
