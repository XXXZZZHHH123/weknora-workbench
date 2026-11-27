package agent

import (
	"context"
	"encoding/json"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/structuredresult"
	"github.com/Tencent/WeKnora/internal/types"
)

// Only a requested endpoint in a validated native contract can replace the
// legacy inference. A malformed endpoint never supplies a value or a date.
func coverageLastValid(base coverageEvidence, args, source, series, input, scope map[string]any, path string, contract *structuredresult.Contract) coverageEvidence {
	coverage := coverageObject(input["coverage"])
	raw, supplied := coverage["last_valid"]
	if !structuredresult.LastValidEnabled() || !supplied {
		return coverageEvidence{}
	}
	item := coverageRejectLastValid(base, path)
	if args["include_last_valid"] != true || contract == nil || scope == nil || contract.SourceSnapshot.Kind != "excel_observations" ||
		contract.SourceSnapshot.DatasetID != item.DatasetID || item.ManifestHash == "" || item.SeriesID == "" || item.Sheet == "" ||
		contract.Execution.Kind != "series_query" && contract.Execution.Kind != "sql" || coverage["complete"] != true {
		return item
	}
	for _, key := range []string{"start_date", "end_date", "period"} {
		if sourceNavigationString(args, key) != sourceNavigationString(scope, key) {
			return item
		}
	}
	last := coverageObject(raw)
	dimensions := coverageObject(last["dimensions"])
	seriesDimensions := coverageObject(series["dimensions"])
	if last["series_id"] != item.SeriesID || last["sheet"] != item.Sheet || last["manifest_sha256"] != item.ManifestHash || dimensions == nil ||
		(len(dimensions) != 0 || len(seriesDimensions) != 0) && !reviewJSONValuesEqual(dimensions, seriesDimensions) {
		return item
	}
	workbook := coverageObject(coverage["source"])
	workbookHash, workbookManifest := sourceNavigationString(workbook, "sha256"), sourceNavigationString(workbook, "manifest_sha256")
	workbookDataset := sourceNavigationString(workbook, "dataset_id")
	if workbookDataset == "" || !validFigureDigest(workbookHash) || !validFigureDigest(workbookManifest) ||
		last["source_sha256"] != workbookHash || last["source_manifest_sha256"] != workbookManifest || last["source_dataset_id"] != workbookDataset ||
		workbookDataset == item.DatasetID && (workbookHash != contract.SourceSnapshot.SourceSHA256 || workbookManifest != item.ManifestHash) {
		return item
	}
	if item.Operation == "query_series" {
		if contract.Execution.Kind != "series_query" || source["sheet"] != item.Sheet ||
			contract.Execution.Operation != coverageObject(input["filters"])["operation"] ||
			workbookHash != item.SnapshotHash || workbookDataset != firstNonEmptyAgent(sourceNavigationString(source, "source_dataset_id"), item.DatasetID) {
			return item
		}
	} else if contract.Execution.Kind != "sql" || args["sql"] == nil || !reviewJSONValuesEqual(workbook, coverageObject(input["source"])) {
		return item
	}
	if !coverageLastValidBounds(item, coverage) {
		return item
	}
	status, direction, date := sourceNavigationString(last, "status"), sourceNavigationString(last, "direction"), sourceNavigationString(last, "date")
	rows, rowsOK := last["rows"].([]any)
	count, countOK := citationViewRevision(last["row_count"])
	conflict, conflictOK := last["conflict"].(bool)
	if !rowsOK || !countOK || !conflictOK || count < len(rows) {
		return item
	}
	ordered := item.DateMode != "none" && item.DateMode != "period"
	switch status {
	case "no_valid", "not_ordered":
		if date != "" || direction != "" || count != 0 || conflict || ordered != (status == "no_valid") ||
			status == "no_valid" && coverage["valid_last_date"] != nil {
			return item
		}
	case "observed", "conflict", "truncated":
		if !ordered || !coverageAnalysisDay(date) || coverage["valid_last_date"] != date ||
			count == 0 || (status == "truncated") != (count > len(rows)) || status != "truncated" && conflict != (status == "conflict") ||
			(status == "conflict" && count < 2) ||
			direction != coverageLastValidDirection(item.PeriodFrom, date) {
			return item
		}
	default:
		return item
	}
	item.WorkbookSnapshotHash, item.WorkbookManifestHash, item.WorkbookBound = workbookHash, workbookManifest, true
	item.Complete, item.LastValidStatus, item.LastValidDirection, item.LastValidDate = true, status, direction, date
	item.LastObservedDate = sourceNavigationString(coverage, "record_last_date")
	if item.Subject == "" {
		item.Subject = coverageAnalysisSubject(coverageObject(series["dimensions"]))
	}
	if status != "observed" {
		return item
	}
	unit, basis, unitOK := coverageEndpointUnit(item, series, coverageObject(last["unit"]))
	var observation coverageEvidence
	var value float64
	for index, rawRow := range rows {
		row := coverageObject(rawRow)
		if coverageMissingEndpointCell(item, row, date) {
			continue // A blank or invalid duplicate does not erase the day's values.
		}
		rowPath := path + "/last_valid/rows/" + strconv.Itoa(index)
		next, number, ok := coverageLastValidCell(item, row, rowPath, date)
		if !ok || observation.Cell != "" && number != value {
			item.LastValidStatus, item.LastValidDate = "unverified", ""
			return item
		}
		if observation.Cell == "" || next.Cell < observation.Cell {
			observation, value = next, number
		}
	}
	if observation.Cell == "" || !unitOK {
		item.LastValidStatus, item.LastValidDate = "unverified", ""
		return item
	}
	observation.Unit, observation.UnitBasis = unit, basis
	return observation
}

func coverageRejectLastValid(item coverageEvidence, path string) coverageEvidence {
	item.Kind, item.LastValidStatus, item.LastValidDate = "last_valid_receipt", "unverified", ""
	item.JSONPointer = path + "/last_valid/status"
	return item
}

func coverageLastValidDirection(start, date string) string {
	if start != "" && date < start {
		return "before_range"
	}
	return "in_range"
}

func coverageLastValidBounds(item coverageEvidence, coverage map[string]any) bool {
	bound := sourceNavigationString(coverage, "as_of_date")
	want, wantBound := "snapshot", ""
	switch {
	case item.DateMode == "period" && item.Period != "":
		want = "requested_period"
	case item.DateMode == "period" || item.DateMode == "none":
		want = "undated"
	case item.PeriodTo != "":
		want, wantBound = "through_requested_end", item.PeriodTo
	}
	if coverage["scope"] != want || bound != wantBound || sourceNavigationString(coverage, "period") != item.Period ||
		item.PeriodFrom != "" && !coverageAnalysisDay(item.PeriodFrom) ||
		item.PeriodTo != "" && (!coverageAnalysisDay(item.PeriodTo) || item.PeriodFrom > item.PeriodTo) {
		return false
	}
	var dates [4]string
	for i, key := range []string{"record_first_date", "record_last_date", "valid_first_date", "valid_last_date"} {
		if coverage[key] == nil {
			continue
		}
		dates[i] = sourceNavigationString(coverage, key)
		if !coverageAnalysisDay(dates[i]) || bound != "" && dates[i] > bound {
			return false
		}
	}
	return (dates[0] == "") == (dates[1] == "") && (dates[2] == "") == (dates[3] == "") && dates[0] <= dates[1] &&
		dates[2] <= dates[3] && (dates[2] == "" || dates[0] != "" && dates[0] <= dates[2] && dates[3] <= dates[1])
}

func coverageLastValidCell(item coverageEvidence, row map[string]any, path, date string) (coverageEvidence, float64, bool) {
	value, ok := coverageFiniteNumber(row["value"])
	cell, dateCell := sourceNavigationString(row, "source_cell"), sourceNavigationString(row, "date_cell")
	cache := sourceNavigationString(row, "formula_cache_status")
	if !ok || row["date"] != date || row["value_status"] != "valid" || cache != "not_formula" && cache != "cached_numeric" ||
		!coverageEndpointCells(item, cell, dateCell) {
		return coverageEvidence{}, 0, false
	}
	if raw := row["raw_value"]; raw != nil {
		original, ok := coverageOriginalNumericValue(raw)
		if !ok || original != value {
			return coverageEvidence{}, 0, false
		}
	}
	item.Kind, item.Date, item.Cell, item.DateCell, item.Raw = "last_valid", date, cell, dateCell, row["value"]
	item.JSONPointer, item.CellJSONPointer = path+"/value", path+"/source_cell"
	item.DateEvidenceID, item.DateJSONPointer = item.EvidenceID, path+"/date"
	item.LatestInScope, item.FormulaCacheStatus = item.LastValidDirection == "in_range", cache
	return item, value, true
}

func coverageOriginalNumericValue(raw any) (float64, bool) {
	if literal, ok := raw.(string); ok {
		raw = json.Number(literal)
	}
	return coverageFiniteNumber(raw)
}

func coverageSameOriginalValue(original, endpoint any) bool {
	left, leftOK := coverageOriginalNumericValue(original)
	right, rightOK := coverageFiniteNumber(endpoint)
	return leftOK && rightOK && left == right
}

func coverageMissingEndpointCell(item coverageEvidence, row map[string]any, date string) bool {
	if row["value"] != nil || row["date"] != date || !coverageEndpointCells(item, sourceNavigationString(row, "source_cell"), sourceNavigationString(row, "date_cell")) {
		return false
	}
	cache := row["formula_cache_status"]
	switch row["value_status"] {
	case "blank":
		return cache == "not_formula"
	case "formula_no_cache":
		return cache == "missing"
	case "excel_error":
		return cache == "not_formula" || cache == "cached_error"
	case "unexpected_text", "invalid_value", "text_dimension", "annotation":
		return cache == "not_formula" || cache == "cached_non_numeric"
	default:
		return false
	}
}

func coverageEndpointCells(item coverageEvidence, cell, dateCell string) bool {
	col, cellRow := coverageExcelCell(cell)
	dateCol, dateRow := coverageExcelCell(dateCell)
	return col != "" && col == item.Column && dateCol != "" && dateCell != cell &&
		(item.DateMode == "shared_cell" || dateCol != col && dateRow == cellRow)
}

func coverageFiniteNumber(value any) (float64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	parsed, err := number.Float64()
	return parsed, err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
}

func coverageExcelCell(address string) (string, string) {
	column, row := coverageCellPosition(address)
	number, _ := strconv.Atoi(row)
	if number > 1048576 || len(column) == 3 && column > "XFD" {
		return "", ""
	}
	return column, row
}

func coverageEndpointUnit(item coverageEvidence, series, unit map[string]any) (string, string, bool) {
	evidence, ok := unit["evidence"].([]any)
	if !ok || unit["binding_scope"] != "result_column" {
		return "", "", false
	}
	if unit["status"] == "unknown" {
		return "", "", unit["evidence_level"] == "none" && len(evidence) == 0
	}
	symbol := sourceNavigationString(unit, "symbol")
	if unit["status"] != "declared" || unit["evidence_level"] != "imported_series_metadata" || symbol == "" || symbol != series["unit"] || len(evidence) != 1 {
		return "", "", false
	}
	witness := coverageObject(evidence[0])
	witnessHeaders, _ := witness["header_cells"].([]any)
	seriesHeaders, _ := series["header_cells"].([]any)
	return symbol, "imported_series_metadata", witness["series_id"] == item.SeriesID && witness["sheet"] == item.Sheet &&
		witness["source_sha256"] == item.WorkbookSnapshotHash && witness["declared_unit"] == symbol &&
		witness["binding_scope"] == "imported_series_declaration" && witness["original_annotation_verified"] == false &&
		(len(witnessHeaders) == 0 && len(seriesHeaders) == 0 || reviewJSONValuesEqual(witnessHeaders, seriesHeaders))
}

func coverageGapWithEndpoint(gap, endpoint coverageEvidence) coverageEvidence {
	if endpoint.LastValidStatus == "" {
		return gap
	}
	gap.LastValidStatus, gap.LastValidDirection, gap.LastValidDate = endpoint.LastValidStatus, endpoint.LastValidDirection, endpoint.LastValidDate
	gap.Subject = firstNonEmptyAgent(gap.Subject, endpoint.Subject)
	if endpoint.WorkbookBound {
		gap.WorkbookSnapshotHash, gap.WorkbookManifestHash, gap.WorkbookBound = endpoint.WorkbookSnapshotHash, endpoint.WorkbookManifestHash, true
		gap.LastObservedDate = endpoint.LastObservedDate
	}
	if endpoint.LastValidStatus == "observed" && endpoint.LastValidDirection == "before_range" {
		gap.LastValidRef = endpoint.EvidenceID
	}
	return gap
}

func coverageProtocolLastValid(item coverageEvidence) bool {
	return (item.Kind == "last_valid" || item.Kind == "cell") && item.LastValidStatus == "observed"
}

func coverageEndpointForGap(record coverageRecord, gap coverageEvidence) (coverageEvidence, bool) {
	var chosen coverageEvidence
	for _, item := range record.Evidence {
		if item.LastValidStatus == "" || item.Kind != "last_valid_receipt" && !coverageProtocolLastValid(item) ||
			!coverageCurrentEvidence(record, item) || gap.KnowledgeBaseID != item.KnowledgeBaseID || gap.DatasetID != item.DatasetID ||
			gap.SeriesID != item.SeriesID || gap.Sheet != item.Sheet || gap.Column != item.Column ||
			gap.PeriodFrom != item.PeriodFrom || gap.PeriodTo != item.PeriodTo || gap.Period != item.Period || gap.LastValidFilterHash != item.LastValidFilterHash ||
			!coverageSameEndpointSnapshot(gap, item) ||
			gap.ManifestHash != "" && gap.ManifestHash != item.ManifestHash {
			continue
		}
		if chosen.EvidenceID == "" || coverageEndpointStatusOrder(item.LastValidStatus) < coverageEndpointStatusOrder(chosen.LastValidStatus) ||
			item.LastValidStatus == chosen.LastValidStatus && (item.EvidenceID == gap.EvidenceID ||
				chosen.EvidenceID != gap.EvidenceID && strings.Compare(item.EvidenceID, chosen.EvidenceID) < 0) {
			chosen = item
		}
	}
	return chosen, chosen.EvidenceID != ""
}

func coverageEndpointFilterHash(filters map[string]any) string {
	values, _ := filters["value_filters"].([]any)
	if len(values) == 0 {
		if filters["last_n"] == nil && filters["top_n"] == nil {
			return ""
		}
		values = nil // An omitted value filter and an empty filter list agree.
	}
	return reviewDigest([]any{values, filters["last_n"], filters["top_n"]})
}

func coverageEndpointStatusOrder(status string) int {
	switch status {
	case "conflict":
		return 0
	case "no_valid":
		return 1
	case "truncated":
		return 2
	case "not_ordered":
		return 3
	case "observed":
		return 4
	default:
		return 5
	}
}

func coverageSameEndpointSnapshot(left, right coverageEvidence) bool {
	if left.WorkbookSnapshotHash != "" && right.WorkbookSnapshotHash != "" && left.WorkbookSnapshotHash != right.WorkbookSnapshotHash {
		return false
	}
	if left.SnapshotHash == right.SnapshotHash {
		return true
	}
	leftWorkbook := firstNonEmptyAgent(left.WorkbookSnapshotHash, left.SnapshotHash)
	rightWorkbook := firstNonEmptyAgent(right.WorkbookSnapshotHash, right.SnapshotHash)
	return leftWorkbook != "" && leftWorkbook == rightWorkbook &&
		(left.WorkbookManifestHash == "" || right.WorkbookManifestHash == "" || left.WorkbookManifestHash == right.WorkbookManifestHash)
}

func coverageSameOriginalCell(left, right coverageEvidence) bool {
	return left.KnowledgeBaseID == right.KnowledgeBaseID && left.SourceID == right.SourceID && left.DatasetID == right.DatasetID &&
		left.SeriesID != "" && left.SeriesID == right.SeriesID && left.Sheet == right.Sheet && left.Date != "" && left.Date == right.Date &&
		left.Cell != "" && left.Cell == right.Cell && coverageSameEndpointSnapshot(left, right)
}

func coverageHasLastValid(data map[string]any) bool {
	if _, exists := coverageObject(data["coverage"])["last_valid"]; exists {
		return true
	}
	for _, value := range coverageObject(data["inputs"]) {
		if _, exists := coverageObject(coverageObject(value)["coverage"])["last_valid"]; exists {
			return true
		}
	}
	return false
}

func coverageRequestedLastValid(call types.ToolCall) bool {
	operation, args := structuredCitationOperation(call), call.ExecutionArgs()
	if operation != "query_series" && operation != "analyze_table" {
		return false
	}
	if call.ExecutionName() == "structured_data" {
		args = coverageObject(args["arguments"])
	}
	return args["include_last_valid"] == true
}

// Keep the original interval rows and their pre-SQL input receipts. Both A2
// endpoints and the separate last-record group can contain out-of-range rows.
func lastValidIntervalData(data map[string]any) map[string]any {
	projection := maps.Clone(data)
	if _, exists := coverageObject(data["coverage"])["last_valid"]; exists {
		delete(projection, "coverage")
	}
	if inputs := coverageObject(data["inputs"]); inputs != nil {
		projectedInputs := maps.Clone(inputs)
		for id, raw := range inputs {
			input := coverageObject(raw)
			if _, exists := coverageObject(input["coverage"])["last_valid"]; exists {
				projectedInput := maps.Clone(input)
				delete(projectedInput, "coverage")
				projectedInputs[id] = projectedInput
			}
		}
		projection["inputs"] = projectedInputs
	}
	return projection
}

func lastValidIntervalReviewCall(call types.ToolCall) types.ToolCall {
	result := *call.Result
	result.Data = lastValidIntervalData(result.Data)
	// The service contract authenticates the full original receipt. The view
	// has its own immutable review handle and carries no replacement contract.
	delete(result.Data, "result_contract")
	encoded, _ := json.Marshal(result.Data)
	result.Output = string(encoded)
	call.Result = &result
	return call
}

func (e *AgentEngine) lastValidIntervalReviewAllowed(ctx context.Context, record coverageRecord, attempt coverageAttempt) bool {
	if ctx == nil || !attempt.Success || attempt.Operation != "query_series" && attempt.Operation != "analyze_table" ||
		!coverageHasLastValid(attempt.result.Data) {
		return false
	}
	data := attempt.result.Data
	base := coverageEvidence{Operation: attempt.Operation, SourceDomain: "structured"}
	item, contract, valid := coverageStructuredResultIdentity(base, attempt.Args, data)
	if !valid || !item.Bound || contract == nil || contract.SourceSnapshot.Kind != "excel_observations" {
		return false
	}
	scope := coverageObject(data["requested_range"])
	if scope == nil {
		return false
	}
	for _, key := range []string{"start_date", "end_date", "period"} {
		if sourceNavigationString(attempt.Args, key) != sourceNavigationString(scope, key) {
			return false
		}
	}
	if attempt.Operation == "analyze_table" {
		if contract.Execution.Kind != "sql" || data["status"] != "ok" || data["sql"] != attempt.Args["sql"] {
			return false
		}
		ids, _ := attempt.Args["series_ids"].([]any)
		inputs := coverageObject(data["inputs"])
		if len(ids) == 0 {
			return false
		}
		selectedIDs := make(map[string]bool, len(ids))
		for _, selected := range ids {
			id, _ := selected.(string)
			if id == "" || coverageObject(coverageObject(inputs[id])["series"])["series_id"] != id {
				return false
			}
			selectedIDs[id] = true
		}
		if len(selectedIDs) != len(inputs) {
			return false
		}
		item.SnapshotHash = reviewDigest([]any{item.SnapshotHash, item.ManifestHash})
	} else {
		series := coverageObject(data["series"])
		filters := coverageObject(data["filters"])
		id := sourceNavigationString(series, "series_id")
		if contract.Execution.Kind != "series_query" || data["status"] != "ok" && data["status"] != "no_data" ||
			id == "" || id != attempt.Args["series_id"] || id != filters["series_id"] || item.Sheet != series["sheet"] ||
			contract.Execution.Operation != filters["operation"] ||
			firstNonEmptyAgent(sourceNavigationString(attempt.Args, "operation"), "rows") != filters["operation"] ||
			coverageEndpointFilterHash(attempt.Args) != coverageEndpointFilterHash(filters) {
			return false
		}
		for _, key := range []string{"start_date", "end_date", "period"} {
			if sourceNavigationString(filters, key) != sourceNavigationString(scope, key) {
				return false
			}
		}
		item = coverageSeriesIdentity(item, series)
	}
	if manifest := record.Snapshots[coverageManifestKey(item)]; manifest != "" && manifest != item.ManifestHash {
		return false
	}
	if snapshot := record.Snapshots[coverageSnapshotKey(item)]; snapshot != "" && snapshot != item.SnapshotHash {
		return false
	}
	return e.coverageEvidenceAllowed(ctx, nil, item)
}

// The review projection and its fallback reader share the same filtered
// executions. Stored results and the historical transcript remain intact.
func (e *AgentEngine) lastValidSourceReviewState(ctx context.Context, state *types.AgentState, startStep int) *types.AgentState {
	if state == nil {
		return nil
	}
	modern := func(call types.ToolCall) bool {
		return call.Result != nil && coverageHasLastValid(call.Result.Data)
	}
	if !slices.ContainsFunc(state.RoundSteps, func(step types.AgentStep) bool {
		return slices.ContainsFunc(step.ToolCalls, modern)
	}) {
		return state
	}
	allowed := map[string]bool{}
	enabled := structuredresult.LastValidEnabled()
	if ctx != nil && e.toolRegistry != nil {
		record, attempts := e.projectCurrentTaskCoverage(state, startStep, coverageRecord{})
		if enabled {
			for _, item := range record.Evidence {
				if item.LastValidStatus != "" && item.LastValidStatus != "unverified" && item.Bound &&
					coverageCurrentEvidence(record, item) && e.coverageEvidenceAllowed(ctx, nil, item) {
					allowed[item.EvidenceID] = true
				}
			}
		} else {
			for _, attempt := range attempts {
				if e.lastValidIntervalReviewAllowed(ctx, record, attempt) {
					allowed[attempt.EvidenceID] = true
				}
			}
		}
	}
	view := *state
	view.RoundSteps = slices.Clone(state.RoundSteps)
	for index, step := range view.RoundSteps {
		calls := make([]types.ToolCall, 0, len(step.ToolCalls))
		for _, call := range step.ToolCalls {
			if modern(call) {
				if !allowed[structuredCitationReaderID(call)] {
					continue
				}
				if !enabled {
					call = lastValidIntervalReviewCall(call)
				}
			}
			calls = append(calls, call)
		}
		view.RoundSteps[index].ToolCalls = calls
	}
	return &view
}
