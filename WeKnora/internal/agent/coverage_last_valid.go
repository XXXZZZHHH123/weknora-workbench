package agent

import (
	"context"
	"encoding/json"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/structuredresult"
	"github.com/Tencent/WeKnora/internal/types"
)

// The workbench may request the endpoint alongside an already selected read.
// Keep the tool's public default and respect an explicit option from the model.
func (e *AgentEngine) defaultCoverageLastValid(ctx context.Context, call types.LLMToolCall, descriptor agenttools.StructuredOperationDescriptor, args, selection map[string]any) bool {
	dataset := sourceNavigationString(selection, "dataset_id")
	if !descriptor.SupportsLastValid() || !e.workbenchEvidenceTask() || dataset == "" {
		return false
	}
	if _, explicit := selection["include_last_valid"]; explicit {
		return false
	}
	library := e.coverageStructuredInvocationLibrary(args)
	if library == "" || !e.coverageScopeAllows(library, "") || types.AuthorizeTenantAPIKeyKnowledgeBases(ctx, library) != nil {
		return false
	}
	var field string
	switch descriptor.Operation {
	case "query_series":
		operation := firstNonEmptyAgent(sourceNavigationString(selection, "operation"), "rows")
		field = sourceNavigationString(selection, "series_id")
		if operation != "rows" && operation != "latest" ||
			citationInt(selection["offset"]) != 0 || coverageEndpointFilterHash(selection) != "" {
			return false
		}
	case "analyze_table":
		ids, _ := selection["series_ids"].([]any)
		if len(ids) != 1 || sourceNavigationString(selection, "sql") == "" {
			return false
		}
		field, _ = ids[0].(string)
	default:
		return false
	}
	start, end := sourceNavigationString(selection, "start_date"), sourceNavigationString(selection, "end_date")
	targets := e.coverageCallTargets(call)
	if field == "" || !coverageAnalysisDay(start) || !coverageAnalysisDay(end) || len(targets) != 1 {
		return false
	}
	target := targets[0]
	from, to, _ := coverageTargetDateRange(target)
	if target.RequestBasis == "" || target.SourceMode != "only" && target.SourceMode != "named_first" ||
		!slices.Contains(target.OnVerifiedGap, "last_valid_same_field") || start != from || end != to {
		return false
	}
	record := e.currentCoverageScopeRecord(ctx)
	return slices.ContainsFunc(record.Bindings, func(binding coverageBinding) bool {
		if binding.GoalIndex != target.GoalIndex || binding.Role != "primary" ||
			binding.KnowledgeBaseID != library || binding.SourceID != dataset || binding.SeriesID != field ||
			binding.PeriodFrom != start || binding.PeriodTo != end {
			return false
		}
		return slices.ContainsFunc(record.Evidence, func(item coverageEvidence) bool {
			return item.Bound && item.SourceDomain == "structured" && item.KnowledgeBaseID == library &&
				item.DatasetID == dataset && item.SeriesID == field && item.EvidenceID == binding.EvidenceID &&
				item.JSONPointer == binding.JSONPointer && coverageCurrentEvidence(record, item)
		})
	})
}

type coverageLastValidFeedback struct {
	Source            string `json:"source"`
	Dataset           string `json:"dataset_id"`
	Sheet             string `json:"sheet"`
	Series            string `json:"series_id"`
	Field             string `json:"field"`
	RequestedFrom     string `json:"requested_from,omitempty"`
	RequestedTo       string `json:"requested_to,omitempty"`
	Period            string `json:"period,omitempty"`
	Endpoint          string `json:"endpoint_status"`
	Date              string `json:"observed_date,omitempty"`
	Raw               any    `json:"original_value,omitempty"`
	Cell              string `json:"source_cell,omitempty"`
	DateCell          string `json:"date_cell,omitempty"`
	Unit              string `json:"unit,omitempty"`
	SupplementPending bool   `json:"supplement_pending,omitempty"`
}

const coverageLastValidFeedbackInstructions = "The verified gaps and endpoint receipts below apply only to their named field, source and scope. Use the full user question and actual field metadata to check whether each queried field is the requested measure. Use the existing field directory to correct a field that includes other measures or uses a different definition. When the original task calls for an endpoint and permits earlier data, request include_last_valid=true on the same scoped read if the correct field lacks a verified endpoint. Do not substitute a directory date, row window, another field, or a supplement for the original value. For a verified endpoint report the source, date, original value and cell together. If the requested field or its endpoint remains unverified, describe that specific missing evidence."

func lastValidFeedbackInstructions(feedback []coverageLastValidFeedback) string {
	if slices.ContainsFunc(feedback, func(item coverageLastValidFeedback) bool { return item.SupplementPending }) {
		return coverageLastValidFeedbackInstructions + " For a gap marked supplement_pending, keep the primary gap and existing readings. The plan allows separately attributed discovery for this request. Consider the actual authorized catalog and navigation in the ongoing source decision. When they provide a relevant lead, use the ordinary source tools to read it within the existing budget, and attribute its disclosed value, period, unit and location separately. Reuse readings already obtained and respect every explicit source restriction."
	}
	return coverageLastValidFeedbackInstructions
}

func (e *AgentEngine) lastValidCoverageFeedback(ctx context.Context) []coverageLastValidFeedback {
	if !structuredresult.LastValidEnabled() || e.toolRegistry == nil {
		return nil
	}
	record := e.currentCoverageScopeRecord(ctx)
	supported := make(map[string]bool)
	var feedback []coverageLastValidFeedback
	for _, gap := range slices.Backward(record.Evidence) {
		if gap.Kind != "gap" || !gap.Bound || !gap.Complete || gap.SeriesID == "" ||
			!coverageCurrentEvidence(record, gap) || !e.coverageEvidenceAllowed(ctx, nil, gap) {
			continue
		}
		if _, known := supported[gap.Operation]; !known {
			operation, _, _ := e.toolRegistry.StructuredInvocation("structured_"+gap.Operation, json.RawMessage(`{}`))
			supported[gap.Operation] = operation.SupportsLastValid()
		}
		if !supported[gap.Operation] {
			continue
		}
		item := coverageLastValidFeedback{
			Source: gap.SourceTitle, Dataset: gap.DatasetID, Sheet: gap.Sheet, Series: gap.SeriesID, Field: gap.Metric,
			RequestedFrom: gap.PeriodFrom, RequestedTo: gap.PeriodTo, Period: gap.Period,
			Endpoint: "unavailable", SupplementPending: e.plannedSupplementPending(ctx, record, gap),
		}
		endpoint, present := coverageEndpointForGap(record, gap)
		if present && e.coverageEvidenceAllowed(ctx, nil, endpoint) {
			item.Endpoint = endpoint.LastValidStatus
			if coverageProtocolLastValid(endpoint) && sameCoverageLastValid(record, gap, endpoint) {
				item.Date, item.Raw, item.Cell, item.DateCell, item.Unit = endpoint.Date, endpoint.Raw, endpoint.Cell, endpoint.DateCell, endpoint.Unit
			}
		}
		if !slices.Contains(feedback, item) {
			feedback = append(feedback, item)
			if len(feedback) == 8 {
				break
			}
		}
	}
	return feedback
}

// Advertise a planned follow-up in the existing model or review input. The
// already executed native gap establishes coverage, not the choice of report.
func (e *AgentEngine) plannedSupplementPending(ctx context.Context, record coverageRecord, gap coverageEvidence) bool {
	if !relaxedTerminalEvidenceEnabled() || e.coverageOnlyDomain() != "" || !coverageExactRange(gap) {
		return false
	}
	search, read := e.toolRegistry.DocumentCoverageCapabilities()
	if !read || !search {
		return false
	}
	targets := e.evidenceCompletionTargets()
	if slices.ContainsFunc(targets, func(target taskCoverageTarget) bool { return target.SourceMode == "only" }) {
		return false
	}
	plannedDiscovery := e.taskPlan != nil && e.taskPlan.SourceDiscovery != nil &&
		slices.Contains(e.taskPlan.SourceDiscovery.SourceDomains, "knowledge")
	if !slices.ContainsFunc(targets, func(target taskCoverageTarget) bool {
		from, to, _ := coverageTargetDateRange(target)
		return target.SourceMode == "named_first" && target.RequestBasis != "" &&
			(target.VersionTask == "" || target.VersionTask == "single") &&
			(plannedDiscovery || slices.Contains(target.OnVerifiedGap, "attributed_supplement")) &&
			from != "" && to != "" && gap.PeriodFrom <= from && to <= gap.PeriodTo &&
			coverageEqual(target.Table, gap.Sheet) && e.coverageMatchesPrimaryReference(target, gap)
	}) {
		return false
	}
	return !slices.ContainsFunc(record.Evidence, func(item coverageEvidence) bool {
		return item.SourceDomain == "knowledge" && item.Kind == "body" && item.Bound &&
			comparisonPhysicalSource(item) != comparisonPhysicalSource(gap) && coverageCurrentEvidence(record, item) &&
			e.coverageEvidenceAllowed(ctx, nil, item)
	})
}

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
