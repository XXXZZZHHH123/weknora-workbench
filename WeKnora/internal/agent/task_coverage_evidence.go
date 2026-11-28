package agent

import (
	"context"
	"encoding/json"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/structuredresult"
	"github.com/Tencent/WeKnora/internal/types"
)

func coverageSnapshotKey(evidence coverageEvidence) string {
	part := evidence.ChunkID
	if evidence.SourceDomain == "structured" {
		part = evidence.Sheet + "\x00" + evidence.Object
		if evidence.Operation == "analyze_table" {
			part += "\x00published_dataset"
		}
	}
	return evidence.KnowledgeBaseID + "\x00" + evidence.SourceID + "\x00" + part
}

func coverageManifestKey(evidence coverageEvidence) string {
	return "manifest\x00" + evidence.KnowledgeBaseID + "\x00" + evidence.DatasetID
}

func coverageRememberSnapshot(snapshots map[string]string, item coverageEvidence) {
	if !item.Bound || item.SourceID == "" || item.SnapshotHash == "" {
		return
	}
	snapshots[coverageSnapshotKey(item)] = item.SnapshotHash
	if item.ManifestHash != "" {
		snapshots[coverageManifestKey(item)] = item.ManifestHash
	}
}

// Completed steps are the batch ledger for sequential, parallel and resumed
// execution. Provider tool text cannot enter this projection.
func (e *AgentEngine) taskCoverageBatch(step types.AgentStep, state *types.AgentState, seen map[string]bool, terminal ...bool) coverageEvidenceBatch {
	var batch coverageEvidenceBatch
	for _, call := range step.ToolCalls {
		if call.Result == nil || call.ID == "" {
			continue
		}
		name := call.ExecutionName()
		if name != call.Name {
			// A proxy target can share a native function name without being that executor.
			continue
		}
		registered, err := e.toolRegistry.GetTool(name)
		if err != nil {
			continue
		}
		args := call.ExecutionArgs()
		operation, domain := name, "knowledge"
		raw, _ := json.Marshal(args)
		descriptor, inner, native := e.toolRegistry.StructuredInvocation(name, raw)
		if native && descriptor.EffectClass == "read" {
			operation, domain, args = descriptor.Operation, "structured", inner
		} else {
			switch registered.(type) {
			case *agenttools.ListDocumentsTool, *agenttools.SearchKnowledgeTool, *agenttools.ReadDocumentTool:
			case *renderChartTool:
				if !chartDeliveryExecution(call) {
					continue
				}
				domain = "artifact"
			default:
				if name != agenttools.ToolBindDocumentScope || !agenttools.HasDocumentScopeBinder(e.toolRegistry) {
					continue
				}
			}
		}
		encoded, err := json.Marshal(sourceReviewResultPayload(call, nativeGeometryImageIndexes(call)))
		if err != nil {
			continue
		}
		id := reviewEvidenceID(chat.Message{Name: name, ToolCallID: call.ID, Content: string(encoded)})
		data := call.Result.Data
		success := call.Result.Success && !call.Result.PreflightRejected && data["execution_started"] != false
		var payload map[string]any
		key := id
		if native {
			// Round-tripped history stores this descriptor as JSON. Recover its
			// type through the registry and verify the executed protocol identity.
			var executed agenttools.StructuredOperationDescriptor
			raw, _ := json.Marshal(data["_structured_execution"])
			verified := json.Unmarshal(raw, &executed) == nil && executed.Operation == descriptor.Operation &&
				executed.EffectClass == descriptor.EffectClass && executed.ScopeMode == descriptor.ScopeMode &&
				(executed.ToolName == "" || executed.ToolName == descriptor.ToolName)
			success = success && verified
			if verified {
				payload, err = decodeCitationJSON(string(encoded))
				if err == nil {
					// Private idempotency key. Keep the original reader handle
					// for citations and exact pointers into either representation.
					if output, ok := payload["output"].(string); call.Result.Success && ok {
						// The reader suppresses byte-identical output. A restored
						// descriptor may have changed only its JSON key order.
						if object, err := decodeCitationJSON(output); err == nil && reviewJSONValuesEqual(object, payload["data"]) {
							delete(payload, "output")
						}
					}
					key = reviewDigest([]any{name, call.ID, args, executed, success, payload})
				}
			}
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		batch.Attempts = append(batch.Attempts, coverageAttempt{EvidenceID: id, ToolCallID: call.ID, Operation: operation, Success: success, Args: args, result: call.Result})
		if !success {
			continue
		}
		base := coverageEvidence{EvidenceID: id, ToolCallID: call.ID, Operation: operation, SourceDomain: domain,
			Status: sourceNavigationString(data, "status"), NextOffset: coverageOffset(data["next_offset"])}
		// Decode the same canonical payload used by its evidence ID. This keeps
		// object arrays, numeric lexemes and JSON pointer indexes stable on replay.
		if payload == nil {
			payload, err = decodeCitationJSON(string(encoded))
			if err != nil {
				continue
			}
		}
		data = coverageObject(payload["data"])
		switch domain {
		case "knowledge":
			batch.Evidence = append(batch.Evidence, e.coverageDocumentEvidence(call, base, data)...)
		case "structured":
			finalRead := len(terminal) != 0 && terminal[0]
			if finalRead && operation == "analyze_table" {
				finalRead = slices.ContainsFunc(e.evidenceCompletionTargets(), func(target taskCoverageTarget) bool {
					from, to, _ := coverageTargetDateRange(target)
					return target.SourceTime == "observed_period" && target.Delivery["aggregation"] == "min_max" &&
						from == sourceNavigationString(args, "start_date") && to == sourceNavigationString(args, "end_date")
				})
			}
			bases := coverageStructuredEvidence(base, args, data, operation == "analyze_table", finalRead)
			citations := e.coverageCallCitations(call, id)
			for _, item := range bases {
				if e.coverageScopeAllows(item.KnowledgeBaseID, "") {
					item.CitationID = citations[firstNonEmptyAgent(item.ResultJSONPointer, item.JSONPointer)]
					if coverageProtocolLastValid(item) && citations[item.JSONPointer] != "" {
						item.CitationID = citations[item.JSONPointer]
					}
					batch.Evidence = append(batch.Evidence, item)
				}
			}
		case "artifact":
			if data["status"] == "ok" && data["kind"] == "data_chart" {
				tool := registered.(*renderChartTool)
				tool.mu.Lock()
				for _, receipt := range tool.renderReceipts {
					if receipt.CallID == call.ID && receipt.DataHash == reviewDigest(call.Result.Data) && chartReceiptSourceCurrent(call, tool, state) {
						base.Kind, base.Bound, base.Complete = "artifact", true, true
						base.SourceID, base.JSONPointer, base.Raw = sourceNavigationString(data, "source_id"), "/data/artifact", data["artifact"]
						batch.Evidence = append(batch.Evidence, base)
						break
					}
				}
				tool.mu.Unlock()
			}
		}
	}
	return batch
}

// Reproject native executions at a delivery boundary, where the latest
// snapshot and actual current payload must agree. Incremental turn state may
// have observed the earlier version of a result.
func (e *AgentEngine) projectCurrentTaskCoverage(state *types.AgentState, startStep int, record coverageRecord, terminal ...bool) (coverageRecord, []coverageAttempt) {
	record.Snapshots = make(map[string]string)
	var attempts []coverageAttempt
	seen := make(map[string]bool)
	for _, step := range state.RoundSteps[min(startStep, len(state.RoundSteps)):] {
		batch := e.taskCoverageBatch(step, state, seen, terminal...)
		attempts = append(attempts, batch.Attempts...)
		for _, item := range batch.Evidence {
			item.Revision, item.AuthScope = record.Revision, record.AuthScope
			coverageRememberSnapshot(record.Snapshots, item)
			record.Evidence = append(record.Evidence, item)
		}
	}
	return record, attempts
}

func (e *AgentEngine) coverageCallCitations(call types.ToolCall, evidenceID string) map[string]string {
	if len(e.turnReview.StructuredCitations) == 0 || e.modelContext == nil {
		return nil
	}
	dataHash := reviewDigest(call.Result.Data)
	citations := map[string]string{}
	for _, id := range e.sortedCurrentStructuredCitationIDs() {
		entry := e.turnReview.StructuredCitations[id]
		if entry.CallID == call.ID && entry.DataHash == dataHash && entry.Directory["evidence_id"] == evidenceID &&
			e.modelContext.CitableEvidenceHandle(id) != "" {
			pointer := sourceNavigationString(entry.Directory, "json_pointer")
			if pointer != "" && citations[pointer] == "" {
				citations[pointer] = id
			}
		}
	}
	return citations
}

func coverageObject(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func coverageOffset(value any) *int {
	if number, ok := citationViewRevision(value); ok {
		return &number
	}
	return nil
}

func coverageRows(data map[string]any, key string, visit func(map[string]any, string)) {
	rows, _ := data[key].([]any)
	for index, item := range rows {
		if row, ok := item.(map[string]any); ok {
			visit(row, "/"+key+"/"+strconv.Itoa(index))
		}
	}
}

func (e *AgentEngine) coverageScopeAllows(kb, source string) bool {
	return e.config == nil || len(e.config.SearchTargets) == 0 ||
		agenttools.SearchTargetsIncludeSource(e.config.SearchTargets, kb, source)
}

func coverageStructuredEvidence(base coverageEvidence, args, data map[string]any, includeAnalysisEndpoints bool, terminal ...bool) []coverageEvidence {
	base, contract, ok := coverageStructuredResultIdentity(base, args, data)
	if !ok {
		return nil
	}
	switch base.Operation {
	case "list_datasets", "list_series", "list_pdf_tables", "list_charts":
		return coverageStructuredDirectory(base, data)
	case "query_series":
		return coverageSeriesEvidence(base, args, data, contract, terminal...)
	case "analyze_table":
		return coverageAnalysisEvidence(base, args, data, contract, includeAnalysisEndpoints, terminal...)
	case "inspect_source":
		return coverageInspectionEvidence(base, args, data, terminal...)
	case "read_pdf_table":
		return coveragePDFEvidence(base, args, data, contract)
	case "query_pdf_table":
		if base.Object == args["table_id"] {
			return coverageDerivedRows(base, data, contract)
		}
	}
	return nil
}

func coverageStructuredResultIdentity(base coverageEvidence, args, data map[string]any) (coverageEvidence, *structuredresult.Contract, bool) {
	scope, source := coverageObject(data["source_context"]), coverageObject(data["source"])
	base.KnowledgeBaseID = sourceNavigationString(scope, "knowledge_base_id")
	if base.KnowledgeBaseID == "" || source["knowledge_base_id"] != nil && source["knowledge_base_id"] != base.KnowledgeBaseID ||
		args["knowledge_base_id"] != nil && args["knowledge_base_id"] != base.KnowledgeBaseID {
		return base, nil, false
	}
	base.KnowledgeID = sourceNavigationString(source, "knowledge_id")
	base.DatasetID = sourceNavigationString(data, "dataset_id")
	base.SourceID = base.DatasetID
	base.SourceTitle = sourceNavigationString(source, "file", "file_name")
	base.SnapshotHash = sourceNavigationString(source, "sha256")
	base.Sheet = sourceNavigationString(source, "sheet")
	base.Object = sourceNavigationString(data, "table_id")
	if base.SourceID == "" {
		base.SourceID = base.KnowledgeID
		if base.SourceID == "" {
			base.SourceID = base.Object
		}
	}
	base.Bound = base.SourceID != "" && validFigureDigest(base.SnapshotHash)
	base.Complete = (data["truncated"] == false || data["output_truncated"] == false) &&
		data["truncated"] != true && data["output_truncated"] != true
	base.Complete = base.Complete && base.NextOffset == nil
	if dataset := sourceNavigationString(args, "dataset_id"); dataset != "" && dataset != data["dataset_id"] {
		return base, nil, false
	}
	contract, err := structuredresult.ValidatePayload(data)
	if err != nil {
		return base, nil, false
	}
	if contract != nil {
		snapshot := contract.SourceSnapshot
		if snapshot.SourceSHA256 != base.SnapshotHash ||
			snapshot.KnowledgeBaseID != "" && snapshot.KnowledgeBaseID != base.KnowledgeBaseID ||
			snapshot.KnowledgeID != "" && snapshot.KnowledgeID != base.KnowledgeID ||
			snapshot.DatasetID != "" && snapshot.DatasetID != data["dataset_id"] {
			return base, nil, false
		}
		if base.Object != "" {
			base.SnapshotHash = reviewDigest([]any{snapshot.SourceSHA256, snapshot.Versions})
		}
		if base.Operation == "query_series" || base.Operation == "analyze_table" {
			base.ManifestHash = snapshot.Versions["manifest_sha256"]
		}
	}
	return base, contract, true
}

func coverageStructuredDirectory(base coverageEvidence, data map[string]any) []coverageEvidence {
	var result []coverageEvidence
	key := map[string]string{"list_datasets": "datasets", "list_series": "series", "list_pdf_tables": "tables", "list_charts": "charts"}[base.Operation]
	if base.Operation == "list_datasets" {
		base.Complete = data["datasets_truncated"] == false && base.NextOffset == nil
	}
	base.Complete = base.Complete && citationInt(data["offset"]) == 0
	coverageRows(data, key, func(row map[string]any, path string) {
		item := coverageSeriesIdentity(base, row)
		item.Kind, item.JSONPointer, item.Raw = "directory", "/data"+path, row
		if item.Operation == "list_datasets" {
			item.DatasetID = sourceNavigationString(row, "dataset_id")
			item.SourceID = item.DatasetID
			item.SourceTitle = sourceNavigationString(row, "source_file", "title")
			item.SnapshotHash = sourceNavigationString(row, "sha256")
		}
		if item.Operation == "list_pdf_tables" || item.Operation == "list_charts" {
			item.Object = sourceNavigationString(row, "table_id", "chart_id")
		}
		if item.Operation == "list_pdf_tables" {
			source := coverageObject(row["source"])
			item.KnowledgeID = sourceNavigationString(source, "knowledge_id")
			item.SourceID = item.KnowledgeID
			if item.SourceID == "" {
				item.SourceID = item.Object
			}
			item.SourceTitle = sourceNavigationString(source, "file_name", "file")
		}
		item.Bound = item.SourceID != "" && item.KnowledgeBaseID != ""
		if item.Bound {
			result = append(result, item)
		}
	})
	return result
}

func coverageSeriesIdentity(base coverageEvidence, series map[string]any) coverageEvidence {
	if sheet := sourceNavigationString(series, "sheet"); sheet != "" {
		base.Sheet = sheet
	}
	base.SeriesID = sourceNavigationString(series, "series_id")
	base.Metric = sourceNavigationString(series, "metric")
	base.Subject = sourceNavigationString(series, "subject")
	if agenttools.DocumentTableEvidenceEnabled() {
		base.Dimensions = coverageObject(series["dimensions"])
	}
	base.DateMode = sourceNavigationString(series, "date_mode")
	base.LastValidDate, base.LastObservedDate = sourceNavigationString(series, "last_valid_date"), sourceNavigationString(series, "last_date")
	base.Grain = sourceNavigationString(series, "grain", "frequency")
	base.Column = sourceNavigationString(series, "column")
	if base.Operation == "list_series" {
		context := coverageObject(series["source_context"])
		if source := sourceNavigationString(context, "source_column"); source != "" {
			base.Column = source
		}
		if source := sourceNavigationString(context, "source_sha256"); validFigureDigest(source) {
			base.SnapshotHash = source
		}
	}
	if base.Column == "" && base.DateMode != "period" {
		// The native catalog retains a header chain from outer merged label to
		// the actual innermost field cell. The first merged label can be shared.
		if cells, ok := series["header_cells"].([]any); ok && len(cells) > 0 {
			if header, ok := cells[len(cells)-1].(string); ok {
				base.Column, _ = coverageCellPosition(header)
			}
		}
	}
	base.UnitBasis = sourceNavigationString(series, "unit_status")
	if base.UnitBasis != "needs_review" && base.UnitBasis != "mixed" {
		base.Unit = sourceNavigationString(series, "unit")
	}
	metadata := coverageObject(series["field_metadata"])
	if base.Unit != "" && metadata["unit_source"] != nil {
		base.UnitBasis = sourceNavigationString(metadata, "unit_source")
	}
	if base.Grain == "" {
		base.Grain = sourceNavigationString(metadata, "frequency")
	}
	base.Grain = coverageFrequency(base.Grain)
	return base
}

func coverageFrequency(frequency string) string {
	switch frequency {
	case "daily":
		return "day"
	case "weekly":
		return "week"
	case "monthly":
		return "month"
	case "quarterly":
		return "quarter"
	case "annual", "yearly":
		return "year"
	default:
		return frequency
	}
}

func coverageDateRange(item *coverageEvidence, scope map[string]any) {
	item.PeriodFrom = sourceNavigationString(scope, "start_date")
	item.PeriodTo = sourceNavigationString(scope, "end_date")
	item.Period = sourceNavigationString(scope, "period")
}

func coverageExactRange(item coverageEvidence) bool {
	if item.Period != "" {
		return item.PeriodFrom == "" && item.PeriodTo == ""
	}
	start, first := time.Parse(time.DateOnly, item.PeriodFrom)
	end, last := time.Parse(time.DateOnly, item.PeriodTo)
	return first == nil && last == nil && !start.After(end)
}

func coverageZero(value any) bool {
	return value == json.Number("0")
}

func coverageCell(base coverageEvidence, row map[string]any, path string) coverageEvidence {
	item := base
	item.Kind, item.JSONPointer, item.Raw = "cell", path+"/value", row["value"]
	if item.Raw == nil {
		return item
	}
	item.Date, item.DateCell = sourceNavigationString(row, "date"), sourceNavigationString(row, "date_cell")
	if item.Date != "" {
		item.DateEvidenceID, item.DateJSONPointer = base.EvidenceID, path+"/date"
	}
	item.Period = sourceNavigationString(row, "period")
	item.RowLabel, item.Cell = sourceNavigationString(row, "row_label"), sourceNavigationString(row, "source_cell", "cell")
	item.Column, _ = coverageCellPosition(item.Cell)
	if raw := row["raw_value"]; raw != nil && raw != "" && row["formula"] == nil {
		item.Raw, item.JSONPointer = raw, path+"/raw_value"
	}
	return item
}

func coverageCellPosition(address string) (string, string) {
	column := strings.TrimRight(address, "0123456789")
	row := strings.TrimPrefix(address, column)
	number, err := strconv.Atoi(row)
	if err != nil || number < 1 || column == "" || len(column) > 3 || strconv.Itoa(number) != row {
		return "", ""
	}
	for _, letter := range column {
		if letter < 'A' || letter > 'Z' {
			return "", ""
		}
	}
	return column, row
}

// The model selects the published field and both original cells. Their address
// relationship, last-valid date, source version and typed value are checked
// independently. Adjacent columns can contain equal values.
func coveragePinInspectedLastValid(field, date, value coverageEvidence) (coverageEvidence, bool) {
	dateColumn, dateRow := coverageCellPosition(date.Cell)
	valueColumn, valueRow := coverageCellPosition(value.Cell)
	fieldData := coverageObject(field.Raw)
	context := coverageObject(fieldData["source_context"])
	expectedDateColumn, _ := coverageCellPosition(sourceNavigationString(coverageObject(context["formula_source"]), "date_cell"))
	retainedDataset := sourceNavigationString(fieldData, "source_dataset_id")
	if field.Kind != "directory" || field.Operation != "list_series" || !field.Bound || field.SeriesID == "" ||
		field.DateMode == "period" || field.DateMode == "none" || field.Sheet == "" || field.LastValidDate == "" ||
		retainedDataset != "" && retainedDataset != value.DatasetID ||
		field.Column == "" || valueColumn != field.Column || dateColumn == "" || dateColumn == valueColumn || dateRow != valueRow ||
		expectedDateColumn != "" && expectedDateColumn != dateColumn ||
		date.Kind != "cell" || value.Kind != "cell" || date.Operation != "inspect_source" || value.Operation != "inspect_source" ||
		!date.Bound || !value.Bound || date.SnapshotHash == "" || date.SnapshotHash != value.SnapshotHash ||
		date.EvidenceID != value.EvidenceID || date.SourceID != value.SourceID || date.SourceID != field.SourceID ||
		date.Sheet != value.Sheet || date.Sheet != field.Sheet || date.KnowledgeBaseID != value.KnowledgeBaseID ||
		date.KnowledgeBaseID != field.KnowledgeBaseID || field.SnapshotHash != "" && field.SnapshotHash != value.SnapshotHash {
		return coverageEvidence{}, false
	}
	// The original workbook reader returns typed numbers and ISO dates.
	measure, measured := value.Raw.(json.Number)
	number, err := measure.Float64()
	actualDate, dated := date.Raw.(string)
	if !measured || err != nil || math.IsNaN(number) || math.IsInf(number, 0) || !dated || !coverageOriginalDate(actualDate, field.LastValidDate) {
		return coverageEvidence{}, false
	}
	value.Kind, value.Date, value.DateCell = "last_valid", field.LastValidDate, date.Cell
	value.DateEvidenceID, value.DateJSONPointer = date.EvidenceID, date.JSONPointer
	value.SeriesID, value.Metric, value.Subject = field.SeriesID, field.Metric, field.Subject
	value.Unit, value.UnitBasis, value.DateMode = field.Unit, field.UnitBasis, field.DateMode
	return value, true
}

func coverageOriginalDate(raw, want string) bool {
	for _, layout := range []string{time.DateOnly, "2006-01-02T15:04:05", time.RFC3339Nano} {
		if date, err := time.Parse(layout, raw); err == nil {
			return date.Format(time.DateOnly) == want
		}
	}
	return false
}

func coverageInspectedBoundary(field, last coverageEvidence, from, to, grain string) (coverageEvidence, bool) {
	start, startErr := time.Parse(time.DateOnly, from)
	end, endErr := time.Parse(time.DateOnly, to)
	previous, lastErr := time.Parse(time.DateOnly, field.LastValidDate)
	if last.Kind != "last_valid" && !coverageProtocolLastValid(last) || field.Kind != "directory" || field.Operation != "list_series" ||
		field.SeriesID != last.SeriesID || field.LastValidDate != last.Date ||
		startErr != nil || endErr != nil || lastErr != nil || start.After(end) || !start.After(previous) {
		return coverageEvidence{}, false
	}
	item := field
	item.Kind, item.Status, item.Complete, item.NextOffset = "gap", "outside_published_valid_observations", true, nil
	item.SourceTitle = firstNonEmptyAgent(field.SourceTitle, last.SourceTitle)
	item.Subject = firstNonEmptyAgent(field.Subject, last.Subject)
	if observed, err := time.Parse(time.DateOnly, field.LastObservedDate); err == nil && start.After(observed) {
		item.Status = "outside_published_observations"
	}
	item.SnapshotHash, item.LastValidRef = last.SnapshotHash, last.EvidenceID
	item.PeriodFrom, item.PeriodTo, item.Grain = from, to, grain
	item.JSONPointer += "/last_valid_date"
	item.Raw = field.LastValidDate
	return item, true
}

func coverageSeriesEvidence(base coverageEvidence, args, data map[string]any, contract *structuredresult.Contract, terminal ...bool) []coverageEvidence {
	series, filters := coverageObject(data["series"]), coverageObject(data["filters"])
	base = coverageSeriesIdentity(base, series)
	// The terminal renderer can use original rows from a legacy native read
	// with an exact source selector. Keep these point observations separate
	// from snapshot, aggregate and absence claims.
	direct := len(terminal) != 0 && terminal[0] && base.Status == "ok" && base.DatasetID != "" &&
		args["dataset_id"] == base.DatasetID && base.KnowledgeBaseID != "" && base.SourceTitle != "" &&
		(base.Bound || base.SnapshotHash == "")
	if !base.Bound && !direct || base.SeriesID == "" || base.SeriesID != args["series_id"] || base.SeriesID != filters["series_id"] ||
		(base.Status != "ok" && base.Status != "no_data") {
		return nil
	}
	for _, key := range []string{"start_date", "end_date", "period"} {
		if sourceNavigationString(args, key) != sourceNavigationString(filters, key) {
			return nil
		}
	}
	if op := sourceNavigationString(args, "operation"); op != "" && op != filters["operation"] {
		return nil
	}
	if contract != nil {
		for _, column := range contract.Columns {
			if column.Name == "value" {
				base.Unit, base.UnitBasis = column.Unit.Symbol, column.Unit.EvidenceLevel
				if column.Unit.Status == "mixed" {
					base.UnitBasis = "mixed"
				}
			}
		}
	}
	coverageDateRange(&base, filters)
	if base.DateMode == "" {
		base.DateMode = sourceNavigationString(coverageObject(data["source_semantics"]), "date_representation")
	}
	if base.Grain == "" && base.PeriodFrom != "" && base.PeriodFrom == base.PeriodTo {
		// The call is complete over one calendar date. This describes the
		// lookup, not the frequency or business meaning of the series.
		base.Grain, base.GrainFromLookup = "day", true
	}
	base.LastValidFilterHash = coverageEndpointFilterHash(filters)
	endpoint := coverageLastValid(base, args, coverageObject(data["source"]), series, data, coverageObject(data["requested_range"]), "/data/coverage", contract)
	if endpoint.LastValidStatus != "" && contract == nil && !direct {
		return nil
	}
	var result []coverageEvidence
	valueFilters, filtersKnown := filters["value_filters"].([]any)
	filtered := !filtersKnown || len(valueFilters) != 0 || filters["last_n"] != nil || filters["top_n"] != nil || citationInt(args["offset"]) != 0
	operation := sourceNavigationString(filters, "operation")
	extrema := sourceNavigationRows(data["extremum_rows"])
	verifiedExtrema := !filtered && contract != nil && contract.Execution.Kind == "series_query" &&
		contract.Execution.Operation == operation && (operation == "min" || operation == "max") &&
		data["rows_are"] == "extremum_observations" && len(extrema) > 0
	if endpoint.LastValidStatus != "" && (!filtersKnown || base.LastValidFilterHash != coverageEndpointFilterHash(args) ||
		!filtered && coverageZero(data["valid_count"]) && coverageProtocolLastValid(endpoint) && endpoint.LastValidDirection == "in_range") {
		endpoint = coverageRejectLastValid(base, "/data/coverage")
	}
	rowsOnly := direct && (!base.Bound || endpoint.LastValidStatus == "unverified" || contract == nil && endpoint.LastValidStatus != "")
	if rowsOnly {
		endpoint = coverageEvidence{}
	}
	_, completeCount := citationViewRevision(data["scope_record_count"])
	completeRange := base.Complete || endpoint.LastValidStatus != "unverified" && endpoint.LastValidStatus != "" &&
		completeCount && data["record_count"] == data["scope_record_count"]
	gap := base.Status == "no_data" && completeRange && !filtered && coverageExactRange(base) && coverageZero(data["valid_count"])
	if gap {
		item := base
		item.Kind, item.JSONPointer, item.Complete, item.NextOffset = "gap", "/data/valid_count", true, nil
		item.LastValidDate = sourceNavigationString(series, "last_valid_date")
		result = append(result, coverageGapWithEndpoint(item, endpoint))
	}
	validRows, returnedRows := 0, 0
	lastIndex, lastCount := -1, 0
	modernLatest := filters["operation"] == "latest" && endpoint.LastValidStatus != ""
	coverageRows(data, "rows", func(row map[string]any, path string) {
		returnedRows++
		if modernLatest {
			return // A scalar latest result is verified by the endpoint, including ties.
		}
		item := coverageCell(base, row, "/data"+path)
		if item.Cell == "" && !direct || item.Raw == nil || row["value_status"] != "valid" ||
			base.Period != "" && item.Period != base.Period ||
			base.PeriodFrom != "" && item.Date < base.PeriodFrom ||
			base.PeriodTo != "" && (item.Date == "" || item.Date > base.PeriodTo) {
			return
		}
		if direct {
			if item.Column != "" && base.Column != "" && item.Column != base.Column {
				return
			}
			if item.Period == "" && !coverageAnalysisDay(item.Date) {
				return
			}
			item.Column = firstNonEmptyAgent(item.Column, base.Column)
			item.Bound = true
		}
		if verifiedExtrema && coverageAnalysisDay(item.Date) && coverageEndpointCells(base, item.Cell, item.DateCell) &&
			coverageSameOriginalValue(item.Raw, data["aggregate"]) &&
			slices.ContainsFunc(extrema, func(candidate map[string]any) bool { return reviewJSONValuesEqual(row, candidate) }) {
			item.Extremum = operation
		}
		item.LatestInScope = !rowsOnly && !filtered && base.Complete && data["rows_are"] == "latest_valid_observation" &&
			item.Date != "" && item.Date == data["valid_last_date"] && endpoint.LastValidStatus == ""
		if item.Date != "" && item.Date == series["last_valid_date"] {
			lastIndex, lastCount = len(result), lastCount+1
		}
		validRows++
		result = append(result, item)
	})
	rows, _ := data["rows"].([]any)
	count := json.Number(strconv.Itoa(returnedRows))
	if !rowsOnly && base.Complete && !filtered && coverageExactRange(base) && filters["operation"] == "rows" && data["rows_are"] == "observations" &&
		len(rows) == returnedRows &&
		data["scope_record_count"] == count && data["record_count"] == count && data["valid_count"] == json.Number(strconv.Itoa(validRows)) &&
		(data["returned_row_count"] == nil || data["returned_row_count"] == count) {
		for i := range result {
			if result[i].Kind == "cell" {
				result[i].RangeComplete = true
			}
		}
	}
	if endpoint.LastValidStatus == "" && lastCount == 1 && (result[lastIndex].LatestInScope || result[lastIndex].RangeComplete) {
		last := result[lastIndex]
		last.Kind = "last_valid"
		result = append(result, last)
	} else if endpoint.LastValidStatus != "" {
		if i := slices.IndexFunc(result, func(item coverageEvidence) bool {
			return coverageProtocolLastValid(endpoint) && item.Kind == "cell" && coverageSameOriginalCell(item, endpoint)
		}); i >= 0 {
			if coverageSameOriginalValue(result[i].Raw, endpoint.Raw) {
				endpoint.Kind, endpoint.RangeComplete = "cell", result[i].RangeComplete
				endpoint.Extremum = result[i].Extremum
				endpoint.ResultJSONPointer = strings.TrimSuffix(result[i].JSONPointer, "/raw_value")
				if endpoint.ResultJSONPointer != result[i].JSONPointer {
					endpoint.ResultJSONPointer += "/value"
				}
				result[i] = endpoint
			} else {
				result = append(result, coverageRejectLastValid(base, "/data/coverage"))
			}
		} else {
			if modernLatest && coverageProtocolLastValid(endpoint) && endpoint.LatestInScope {
				endpoint.Kind = "cell"
			}
			result = append(result, endpoint)
		}
	}
	if !rowsOnly && data["aggregate"] != nil && base.Status == "ok" && contract != nil && !filtered {
		item := base
		item.Complete = true // The aggregate covers all matching records, even when row examples are partial.
		item.Kind, item.JSONPointer, item.Raw = "derived", "/data/aggregate", data["aggregate"]
		if filters["operation"] == "count" {
			item.Unit, item.UnitBasis = "", ""
		}
		result = append(result, item)
	}
	return result
}

func coverageAnalysisEvidence(base coverageEvidence, args, data map[string]any, contract *structuredresult.Contract, includeEndpoints bool, terminal ...bool) []coverageEvidence {
	if !base.Bound || base.Status != "ok" || data["input_complete"] != true || contract == nil ||
		contract.Execution.InputScope != "loaded_inputs_before_user_sql" {
		return nil
	}
	scope := coverageObject(data["requested_range"])
	for _, key := range []string{"start_date", "end_date"} {
		if sourceNavigationString(args, key) != sourceNavigationString(scope, key) {
			return nil
		}
	}
	coverageDateRange(&base, scope)
	// SQL verifies a published dataset view, including any retained sheets.
	// Its transport snapshot must not be joined as original workbook evidence.
	workbookHash := base.SnapshotHash
	base.SnapshotHash = reviewDigest([]any{workbookHash, base.ManifestHash})
	inputs := coverageObject(data["inputs"])
	// The service emits this verified workbook metadata only when every
	// loaded sheet belongs to the current workbook, including on republish.
	_, workbookErr := time.Parse(time.RFC3339Nano, sourceNavigationString(coverageObject(data["source"]), "workbook_declared_last_saved_at"))
	includeEndpoints = includeEndpoints && contract.Execution.Kind == "sql" && contract.SourceSnapshot.Kind == "excel_observations" &&
		contract.SourceSnapshot.DatasetID == base.DatasetID && base.DatasetID != "" && args["dataset_id"] == base.DatasetID &&
		contract.SourceSnapshot.KnowledgeBaseID == base.KnowledgeBaseID && contract.SourceSnapshot.KnowledgeID == base.KnowledgeID &&
		args["sql"] == data["sql"]
	var result []coverageEvidence
	var boundedReadings []coverageEvidence
	var inputSeries coverageEvidence
	directEndpoints := make(map[string]bool)
	ids, _ := args["series_ids"].([]any)
	seen := make(map[string]bool, len(ids))
	for _, candidate := range ids {
		id, _ := candidate.(string)
		input := coverageObject(inputs[id])
		series := coverageObject(input["series"])
		item := coverageSeriesIdentity(base, series)
		if id == "" || item.SeriesID != id || seen[id] {
			continue
		}
		seen[id] = true
		if item.Grain == "" && item.PeriodFrom != "" && item.PeriodFrom == item.PeriodTo {
			item.Grain = "day"
		}
		if includeEndpoints {
			item.WorkbookSnapshotHash, item.WorkbookBound = workbookHash, workbookErr == nil
		}
		path := "/data/inputs/" + coveragePointerKey(id)
		if relaxedTerminalEvidenceEnabled() && includeEndpoints && len(ids) == 1 {
			result = append(result, coverageAnalysisExtrema(item, input, data, contract, path)...)
		}
		if len(terminal) != 0 && terminal[0] && includeEndpoints && len(ids) == 1 {
			result = append(result, coverageAnalysisRowExtrema(item, input, data, contract)...)
		}
		endpoint := coverageLastValid(item, args, nil, series, input, scope, path+"/coverage", contract)
		if endpoint.LastValidStatus != "" && (args["sql"] != data["sql"] ||
			coverageZero(input["valid_count"]) && coverageProtocolLastValid(endpoint) && endpoint.LastValidDirection == "in_range") {
			endpoint = coverageRejectLastValid(item, path+"/coverage")
		}
		if endpoint.WorkbookManifestHash != "" && endpoint.LastValidStatus != "unverified" {
			item.WorkbookSnapshotHash, item.WorkbookManifestHash, item.WorkbookBound = endpoint.WorkbookSnapshotHash, endpoint.WorkbookManifestHash, true
		}
		inputSeries = item
		// Keep the exact model-selected field identity for matching separately
		// verified original-workbook observations. This receipt is not a result.
		selection := item
		selection.Kind, selection.Raw = "analysis_input", firstNonEmptyAgent(item.WorkbookSnapshotHash, workbookHash)
		selection.JSONPointer = path + "/series"
		result = append(result, selection)
		if endpoint.LastValidStatus == "" && includeEndpoints {
			if endpoint, ok := coverageAnalysisLastValid(item, input); ok {
				if coverageAnalysisEndpoint(endpoint) {
					endpoint.ResultJSONPointer = coverageDirectAnalysisResultPointer(endpoint, data, contract)
					if item.PeriodTo != "" {
						result = append(result, endpoint)
					} else if endpoint.WorkbookBound && endpoint.ResultJSONPointer != "" {
						// In an unbounded query, the independently verified input
						// endpoint identifies this returned cell. It does not prove
						// a requested window or the absence of other values.
						endpoint.Kind, endpoint.LatestInScope = "cell", false
						result = append(result, endpoint)
					}
				} else if item.PeriodTo != "" && len(ids) == 1 && coverageExplicitAnalysisField(args, id) {
					result = append(result, endpoint)
				}
			}
		}
		if coverageExactRange(item) && coveragePinSingleAnalysisInput(item, input) {
			coverage := coverageObject(input["observation_coverage"])
			changes := coverageObject(coverage["value_changes"])
			first := coverageObject(changes["first"])
			segment := sourceNavigationRows(coverage["segments"])[0]
			observed := item
			observed.Kind, observed.Complete, observed.RangeComplete = "cell", true, true
			observed.Date, observed.Cell, observed.Raw = sourceNavigationString(first, "date"), sourceNavigationString(segment, "first_cell"), first["value"]
			observed.JSONPointer = path + "/observation_coverage/value_changes/first/value"
			observed.ResultJSONPointer = coverageDirectAnalysisResultPointer(observed, data, contract)
			var dateCellOmitted bool
			observed.DateCell, dateCellOmitted = coverageDirectAnalysisDateCell(observed, data, contract)
			// A single-date read still proves that day's input. In a wider
			// query, require the complete SQL result to independently return
			// the same original value and both same-row cell addresses.
			if item.PeriodFrom != item.PeriodTo && observed.WorkbookBound && base.Complete {
				if observed.DateCell != "" {
					boundedReadings = append(boundedReadings, observed)
				} else if dateCellOmitted && relaxedTerminalEvidenceEnabled() {
					// The selected input and returned row still agree on the
					// date, value, field and workbook address. An explicitly
					// returned inconsistent date address never reaches here.
					result = append(result, observed)
				}
			}
			if item.PeriodFrom == item.PeriodTo {
				if coverageProtocolLastValid(endpoint) && coverageSameOriginalCell(endpoint, observed) {
					if coverageSameOriginalValue(observed.Raw, endpoint.Raw) {
						endpoint.Kind, endpoint.RangeComplete = "cell", true
					} else {
						endpoint = coverageRejectLastValid(item, path+"/coverage")
						result = append(result, observed)
					}
				} else {
					result = append(result, observed)
				}
			}
		}
		if endpoint.LastValidStatus != "" {
			if coverageProtocolLastValid(endpoint) && endpoint.LastValidDirection == "in_range" {
				endpoint.ResultJSONPointer = coverageDirectAnalysisResultPointer(endpoint, data, contract)
				if endpoint.ResultJSONPointer != "" {
					endpoint.Kind, directEndpoints[endpoint.ResultJSONPointer] = "cell", true
				}
			}
			result = append(result, endpoint)
		}
		// SQL output is user-defined. Only the transport-bounded, per-field
		// facts from all loaded inputs can prove a gap independently of SQL.
		if coverageExactRange(item) && coverageZero(input["valid_count"]) {
			item.Kind, item.Complete = "gap", true
			item.LastValidDate = sourceNavigationString(series, "last_valid_date")
			item.JSONPointer = path + "/valid_count"
			if includeEndpoints && item.Subject == "" {
				item.Subject = coverageAnalysisSubject(coverageObject(series["dimensions"]))
			}
			result = append(result, coverageGapWithEndpoint(item, endpoint))
		}
	}
	if len(boundedReadings) > 0 && len(boundedReadings) == len(ids) {
		first := boundedReadings[0]
		if !slices.ContainsFunc(boundedReadings, func(item coverageEvidence) bool {
			return item.Sheet != first.Sheet || item.Date != first.Date || item.DateCell != first.DateCell
		}) {
			result = append(result, boundedReadings...)
		}
	}
	if len(ids) == 1 && inputSeries.SeriesID != "" {
		base = inputSeries
	}
	// The loaded transport range does not certify SQL output membership.
	// Store dependencies and values without inheriting that period.
	base.PeriodFrom, base.PeriodTo = "", ""
	for _, item := range coverageDerivedRows(base, data, contract) {
		if !directEndpoints[item.JSONPointer] {
			result = append(result, item)
		}
	}
	return result
}

// A SQL result citation can point to the displayed value of a verified
// original input only when its direct columns also identify that original row.
func coverageDirectAnalysisResultPointer(item coverageEvidence, data map[string]any, contract *structuredresult.Contract) string {
	return coverageAnalysisResultPointer(item, data, contract, false)
}

func coverageAnalysisResultPointer(item coverageEvidence, data map[string]any, contract *structuredresult.Contract, singleInput bool) string {
	if !contract.Capabilities.ColumnDependenciesVerified {
		return ""
	}
	indices := map[string]int{"series_id": -1, "date": -1, "source_cell": -1, "value": -1}
	for _, column := range contract.Columns {
		if column.Operation["kind"] != "column_reference" || len(column.Dependency.SourceColumns) != 1 {
			continue
		}
		name := column.Dependency.SourceColumns[0]
		if index, needed := indices[name]; needed {
			if index != -1 {
				return ""
			}
			indices[name] = column.Index
		}
	}
	for column, index := range indices {
		if column == "series_id" && singleInput && index == -1 {
			continue
		}
		if index < 0 || index >= len(contract.Columns) {
			return ""
		}
	}
	var pointer string
	rows, _ := data["rows"].([]any)
	for rowIndex, raw := range rows {
		row, ok := raw.([]any)
		if !ok || len(row) != len(contract.Columns) || indices["series_id"] >= 0 && row[indices["series_id"]] != item.SeriesID || row[indices["date"]] != item.Date ||
			row[indices["source_cell"]] != item.Cell || !reviewJSONValuesEqual(row[indices["value"]], item.Raw) {
			continue
		}
		if pointer != "" {
			return ""
		}
		pointer = "/data/rows/" + strconv.Itoa(rowIndex) + "/" + strconv.Itoa(indices["value"])
	}
	return pointer
}

func coverageDirectAnalysisDateCell(item coverageEvidence, data map[string]any, contract *structuredresult.Contract) (string, bool) {
	if item.ResultJSONPointer == "" {
		return "", false
	}
	rowPointer := item.ResultJSONPointer[:strings.LastIndexByte(item.ResultJSONPointer, '/')]
	var pointer string
	for _, column := range contract.Columns {
		if column.Operation["kind"] == "column_reference" && slices.Equal(column.Dependency.SourceColumns, []string{"date_cell"}) {
			if pointer != "" {
				return "", false
			}
			pointer = rowPointer + "/" + strconv.Itoa(column.Index)
		}
	}
	if pointer == "" {
		return "", true
	}
	raw, _ := reviewPointerValue(map[string]any{"data": data}, pointer)
	dateCell, _ := raw.(string)
	dateColumn, dateRow := coverageCellPosition(dateCell)
	valueColumn, valueRow := coverageCellPosition(item.Cell)
	if dateColumn == "" || valueColumn == "" || dateColumn == valueColumn || dateRow != valueRow {
		return "", false
	}
	return dateCell, false
}

// The pre-SQL input receipt is independent of aliases, filters and literals in
// the model's SQL. A singleton observation has a unique date, value and cell.
func coveragePinSingleAnalysisInput(item coverageEvidence, input map[string]any) bool {
	if !coverageAnalysisDay(item.PeriodFrom) || !coverageAnalysisDay(item.PeriodTo) || input["input_count"] != json.Number("1") ||
		input["valid_count"] != json.Number("1") || input["observation_coverage_scope"] != "loaded_analysis_inputs_before_user_sql" {
		return false
	}
	coverage := coverageObject(input["observation_coverage"])
	segments := sourceNavigationRows(coverage["segments"])
	if coverage["truncated"] != false || len(segments) != 1 {
		return false
	}
	segment := segments[0]
	cell := sourceNavigationString(segment, "first_cell")
	column, _ := coverageCellPosition(cell)
	changes := coverageObject(coverage["value_changes"])
	first := coverageObject(changes["first"])
	date := sourceNavigationString(first, "date")
	_, numeric := first["value"].(json.Number)
	return column != "" && column == item.Column && numeric && coverageObject(changes["scope"])["complete_filtered_input"] == true &&
		coverageAnalysisDay(date) && date >= item.PeriodFrom && date <= item.PeriodTo && segment["start_date"] == date && segment["end_date"] == date &&
		(item.PeriodFrom == item.PeriodTo || input["actual_first_date"] == date && input["actual_last_date"] == date &&
			input["valid_first_date"] == date && input["valid_last_date"] == date && segment["value_status"] == "valid") &&
		segment["last_cell"] == cell && segment["record_count"] == json.Number("1") && segment["valid_count"] == json.Number("1")
}

func coverageDerivedRows(base coverageEvidence, data map[string]any, contract *structuredresult.Contract) []coverageEvidence {
	if !base.Bound || base.Status != "ok" || contract == nil || !contract.Capabilities.ColumnDependenciesVerified {
		return nil
	}
	var result []coverageEvidence
	rows, _ := data["rows"].([]any)
	for index, raw := range rows {
		row, ok := raw.([]any)
		if !ok || len(row) != len(contract.Columns) {
			continue
		}
		for _, column := range contract.Columns {
			if column.Index < 0 || column.Index >= len(row) || row[column.Index] == nil ||
				!column.Dependency.SourceRowDependency || column.Dependency.TechnicalMetadata {
				continue
			}
			item := base
			item.Kind, item.Column, item.Raw = "derived", column.Name, row[column.Index]
			item.Unit, item.UnitBasis = column.Unit.Symbol, column.Unit.EvidenceLevel
			item.JSONPointer = "/data/rows/" + strconv.Itoa(index) + "/" + strconv.Itoa(column.Index)
			result = append(result, item)
		}
	}
	return result
}

func coveragePointerKey(key string) string {
	return strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}

func coverageInspectionEvidence(base coverageEvidence, args, data map[string]any, terminal ...bool) []coverageEvidence {
	if base.Status != "ok" || data["hash_verified"] != true || base.SourceID == "" {
		return nil
	}
	base.SourceTitle = sourceNavigationString(data, "source_file")
	base.SnapshotHash = sourceNavigationString(data, "sha256")
	base.Bound = validFigureDigest(base.SnapshotHash)
	var result []coverageEvidence
	if scope, ok := coverageInspectionDate(base, args, data); ok {
		result = append(result, scope)
	}
	if len(terminal) != 0 && terminal[0] && base.Bound {
		// The source extent locates a bounded follow-up read of the original
		// date and value.
		sheet := sourceNavigationString(args, "sheet")
		extent := coverageObject(coverageObject(data["coverage"])[sheet])
		if sheet != "" && extent["source_extent_status"] == "complete" {
			start, end, _ := strings.Cut(sourceNavigationString(extent, "source_used_range"), ":")
			if _, row := coverageCellPosition(firstNonEmptyAgent(end, start)); row != "" {
				locator := base
				locator.Kind, locator.Sheet, locator.Raw = "inspection_extent", sheet, row
				locator.JSONPointer = "/data/coverage/" + coveragePointerKey(sheet) + "/source_used_range"
				result = append(result, locator)
			}
		}
	}
	coverageRows(data, "ranges", func(group map[string]any, path string) {
		var lastRow string
		if relaxedTerminalEvidenceEnabled() {
			extent := coverageObject(coverageObject(data["coverage"])[sourceNavigationString(group, "sheet")])
			if extent["source_extent_status"] == "complete" {
				start, end, _ := strings.Cut(sourceNavigationString(extent, "source_used_range"), ":")
				_, lastRow = coverageCellPosition(firstNonEmptyAgent(end, start))
			}
		}
		coverageRows(group, "cells", func(row map[string]any, cellPath string) {
			if row["value_status"] != "original" {
				return
			}
			item := coverageCell(base, row, "/data"+path+cellPath)
			item.Sheet = sourceNavigationString(row, "sheet")
			if agenttools.DocumentTableEvidenceEnabled() {
				item.OriginalNumberFormat = sourceNavigationString(row, "number_format")
			}
			if item.Sheet != "" && item.Cell != "" && item.Raw != nil {
				if _, cellRow := coverageCellPosition(item.Cell); lastRow != "" && cellRow == lastRow {
					raw, _ := item.Raw.(string)
					day, _, _ := strings.Cut(raw, "T")
					if coverageOriginalDate(raw, day) {
						item.LastObservedDate = day
					}
				}
				result = append(result, item)
			}
		})
	})
	coverageRows(data, "calculations", func(row map[string]any, path string) {
		if !base.Bound || row["value"] == nil || row["sha256"] != base.SnapshotHash || len(sourceNavigationRows(row["cells"])) == 0 {
			return
		}
		item := base
		item.Kind, item.Sheet = "derived", sourceNavigationString(row, "sheet")
		item.JSONPointer, item.Raw = "/data"+path+"/value", row["value"]
		result = append(result, item)
	})
	return result
}

func coverageInspectionDate(base coverageEvidence, args, data map[string]any) (coverageEvidence, bool) {
	base.Sheet = sourceNavigationString(args, "sheet")
	searches, _ := args["searches"].([]any)
	scan := coverageObject(data["scan"])
	if !base.Bound || base.Sheet == "" || len(searches) == 0 || scan["complete"] != true ||
		!coverageContainsString(scan["sheets_scanned"], base.Sheet) {
		return coverageEvidence{}, false
	}
	var date string
	for _, raw := range searches {
		literal, _ := raw.(string)
		value := coverageDay(literal)
		if value == "" || date != "" && date != value {
			return coverageEvidence{}, false
		}
		date = value
	}
	base.Kind, base.JSONPointer = "scope_lookup", "/data/scan"
	base.PeriodFrom, base.PeriodTo, base.Grain = date, date, "day"
	return base, true
}

func coverageTargetDateRange(target taskCoverageTarget) (string, string, string) {
	from, to, grain := coveragePeriod(target.Period)
	if from == "" && to == "" && grain == "day" {
		if scope := coverageDay(sourceNavigationString(target.Period, "scope")); scope != "" {
			from, to = scope, scope
		}
	}
	if grain == "month" || grain == "year" {
		layout := "2006-01"
		if grain == "year" {
			layout = "2006"
		}
		if first, err := time.Parse(layout, from); err == nil {
			from = first.Format(time.DateOnly)
		}
		if last, err := time.Parse(layout, to); err == nil {
			next := last.AddDate(0, 1, 0)
			if grain == "year" {
				next = last.AddDate(1, 0, 0)
			}
			to = next.AddDate(0, 0, -1).Format(time.DateOnly)
		}
	}
	return from, to, grain
}

// Join original cells to a selected field's published coverage boundary. SQL
// returning zero rows alone says nothing about the underlying field or period.
func nativeCoverageGapFacts(target taskCoverageTarget, record coverageRecord) []coverageEvidence {
	from, to, grain := coverageTargetDateRange(target)
	var result []coverageEvidence
	for _, field := range record.Evidence {
		if field.Operation != "list_series" || field.Kind != "directory" || !field.Bound || !coverageCurrentEvidence(record, field) ||
			field.SnapshotHash == "" || field.Metric == "" || field.SeriesID == "" {
			continue
		}
		selected := func(binding coverageBinding) bool {
			return binding.GoalIndex == target.GoalIndex && binding.Role == "primary" && (binding.SeriesID != "" || binding.Column != "")
		}
		bound := slices.ContainsFunc(record.Bindings, func(binding coverageBinding) bool {
			return selected(binding) && coverageSameSource(binding, field) && coverageLocation(binding, field)
		})
		if !bound && slices.ContainsFunc(record.Bindings, selected) {
			continue
		}
		if !bound && !slices.ContainsFunc(record.Evidence, func(input coverageEvidence) bool {
			return input.Kind == "analysis_input" && input.SourceID == field.SourceID && input.KnowledgeBaseID == field.KnowledgeBaseID &&
				input.Sheet == field.Sheet && input.SeriesID == field.SeriesID && input.Raw == field.SnapshotHash && coverageCurrentEvidence(record, input)
		}) {
			continue
		}
		start, end, frequency := from, to, grain
		if start == "" && end == "" {
			if frequency != "" && frequency != "day" {
				continue
			}
			var date string
			ambiguous := false
			for _, scope := range record.Evidence {
				if scope.Kind == "scope_lookup" && scope.SourceID == field.SourceID && scope.KnowledgeBaseID == field.KnowledgeBaseID &&
					scope.Sheet == field.Sheet && scope.SnapshotHash == field.SnapshotHash && coverageCurrentEvidence(record, scope) {
					ambiguous = ambiguous || date != "" && date != scope.PeriodFrom
					date = scope.PeriodFrom
				}
			}
			if ambiguous || date == "" {
				continue
			}
			start, end, frequency = date, date, "day"
		}
		context := coverageObject(coverageObject(field.Raw)["source_context"])
		if bound && context["source_sha256"] == field.SnapshotHash {
			for _, last := range record.Evidence {
				if last.Operation != "query_series" || last.DatasetID == "" || last.DatasetID != field.DatasetID ||
					!sameCoverageLastValid(record, field, last) {
					continue
				}
				if gap, ok := coverageInspectedBoundary(field, last, start, end, frequency); ok {
					gap.Subject = firstNonEmptyAgent(gap.Subject, coverageAnalysisSubject(coverageObject(coverageObject(field.Raw)["dimensions"])))
					result = append(result, gap)
				}
			}
		}
		for _, last := range inspectedFieldLastValid(record, field) {
			if gap, ok := coverageInspectedBoundary(field, last, start, end, frequency); ok {
				result = append(result, gap, last)
			}
		}
	}
	return result
}

// Pair a declared field's published last date with the numeric observation
// from that same physical row in a current, hash-verified original read.
// No requested period or field choice is inferred from the row window.
func inspectedFieldLastValid(record coverageRecord, field coverageEvidence) []coverageEvidence {
	if field.Kind != "directory" || field.Operation != "list_series" || field.SourceDomain != "structured" ||
		!field.Bound || field.SnapshotHash == "" || field.Metric == "" || !coverageCurrentEvidence(record, field) {
		return nil
	}
	dateColumn := coverageFieldDateColumn(field)
	if dateColumn == "" {
		return nil
	}
	var found []coverageEvidence
	for _, date := range record.Evidence {
		if !matchingInspectedFieldDate(record, field, date, dateColumn, field.LastValidDate) {
			continue
		}
		_, row := coverageCellPosition(date.Cell)
		for _, value := range record.Evidence {
			if value.Cell != field.Column+row || !coverageCurrentEvidence(record, value) {
				continue
			}
			last, ok := coveragePinInspectedLastValid(field, date, value)
			if !ok {
				continue
			}
			if !slices.ContainsFunc(found, func(prior coverageEvidence) bool {
				return coverageSameOriginalCell(prior, last) && reviewJSONValuesEqual(prior.Raw, last.Raw)
			}) {
				found = append(found, last)
			}
		}
	}
	if len(found) != 1 {
		return nil // Conflicting or distinct original rows do not identify one endpoint.
	}
	return found
}

func coverageFieldDateColumn(field coverageEvidence) string {
	fieldContext := coverageObject(coverageObject(field.Raw)["source_context"])
	column, _ := coverageCellPosition(sourceNavigationString(coverageObject(fieldContext["formula_source"]), "date_cell"))
	return column
}

func matchingInspectedFieldDate(record coverageRecord, field, date coverageEvidence, dateColumn, day string) bool {
	raw, dated := date.Raw.(string)
	column, _ := coverageCellPosition(date.Cell)
	return date.Kind == "cell" && date.Operation == "inspect_source" && date.Bound && dateColumn != "" && column == dateColumn &&
		date.KnowledgeBaseID == field.KnowledgeBaseID && date.SourceID == field.SourceID &&
		date.Sheet == field.Sheet && date.SnapshotHash == field.SnapshotHash && dated && coverageOriginalDate(raw, day) && coverageCurrentEvidence(record, date)
}

// The original date column can establish a checked window outside the source
// boundary before a choice among same-name numeric columns has been made.
func (e *AgentEngine) inspectedDateCoverageGap(ctx context.Context, target taskCoverageTarget, record coverageRecord) (coverageEvidence, bool) {
	if target.StructuredTaskGoal == nil || target.SourceTime != "observed_period" ||
		(target.SourceMode != "named_first" && target.SourceMode != "only") ||
		target.VersionTask == "compare_sources" || target.VersionTask == "compare_versions" {
		return coverageEvidence{}, false
	}
	from, to, grain := coverageTargetDateRange(target)
	if !coverageExactRange(coverageEvidence{PeriodFrom: from, PeriodTo: to}) {
		return coverageEvidence{}, false
	}
	var gap coverageEvidence
	competingDates := false
	for _, field := range record.Evidence {
		if field.Operation != "list_series" || field.Kind != "directory" || !field.Bound || !coverageCurrentEvidence(record, field) ||
			field.LastObservedDate == "" || field.LastObservedDate >= from || (target.Table != "" && target.Table != field.Sheet) {
			continue
		}
		if selected, matches := coveragePrimaryField(target, record, field); selected && !matches {
			continue
		}
		dateColumn := coverageFieldDateColumn(field)
		for _, date := range record.Evidence {
			if date.LastObservedDate != field.LastObservedDate || !matchingInspectedFieldDate(record, field, date, dateColumn, field.LastObservedDate) ||
				!e.coverageEvidenceAllowed(ctx, &target, date) || !e.coverageMatchesPrimaryReference(target, date) {
				continue
			}
			if gap.Bound {
				if gap.SourceID != date.SourceID || gap.KnowledgeBaseID != date.KnowledgeBaseID || gap.SnapshotHash != date.SnapshotHash ||
					gap.Sheet != date.Sheet {
					return coverageEvidence{}, false
				}
				if gap.Cell == date.Cell && gap.LastObservedDate == date.LastObservedDate {
					continue
				}
				competingDates = true
				if gap.Cell < date.Cell || gap.Cell == date.Cell && gap.LastObservedDate <= date.LastObservedDate {
					continue
				}
			}
			gap = date
			gap.Kind, gap.Status, gap.Complete = "gap", "outside_published_observations", true
			gap.PeriodFrom, gap.PeriodTo, gap.Grain, gap.DateCell = from, to, grain, date.Cell
		}
	}
	if gap.Bound && (competingDates || slices.ContainsFunc(record.Evidence, func(field coverageEvidence) bool {
		if field.Operation != "list_series" || field.Kind != "directory" || !field.Bound ||
			field.KnowledgeBaseID != gap.KnowledgeBaseID || field.SourceID != gap.SourceID || field.Sheet != gap.Sheet ||
			field.SnapshotHash != gap.SnapshotHash || field.LastObservedDate == "" || !coverageCurrentEvidence(record, field) {
			return false
		}
		column, _ := coverageCellPosition(gap.DateCell)
		fieldColumn := coverageFieldDateColumn(field)
		return field.LastObservedDate != gap.LastObservedDate || fieldColumn != "" && fieldColumn != column
	})) {
		gap.Complete, gap.Status = false, ""
	}
	return gap, gap.Bound
}

func inspectedPublishedLastValid(record coverageRecord) []coverageEvidence {
	var readings []coverageEvidence
	var fields []coverageEvidence
	for _, field := range record.Evidence {
		if field.Kind != "directory" || field.Operation != "list_series" {
			continue
		}
		if coverageObject(coverageObject(field.Raw)["source_context"])["source_sha256"] != field.SnapshotHash {
			continue
		}
		for _, observation := range inspectedFieldLastValid(record, field) {
			if !slices.ContainsFunc(record.Evidence, func(item coverageEvidence) bool {
				return item.LastValidDate == observation.Date && item.LastValidStatus != "" && item.LastValidStatus != "unverified" &&
					coverageCurrentEvidence(record, item) &&
					item.KnowledgeBaseID == observation.KnowledgeBaseID && item.DatasetID == observation.DatasetID &&
					item.Sheet == observation.Sheet && item.SeriesID == observation.SeriesID && coverageSameEndpointSnapshot(item, observation) &&
					(item.LastValidStatus == "conflict" || item.LastValidStatus == "truncated" ||
						coverageProtocolLastValid(item) && coverageSameOriginalCell(item, observation) && coverageSameOriginalValue(observation.Raw, item.Raw))
			}) {
				if !slices.ContainsFunc(readings, func(existing coverageEvidence) bool {
					return coverageSameOriginalCell(existing, observation) && reviewJSONValuesEqual(existing.Raw, observation.Raw)
				}) {
					readings = append(readings, observation)
					fields = append(fields, field)
				}
			}
		}
	}
	for i, field := range fields {
		if readings[i].Subject != "" || slices.ContainsFunc(record.Bindings, func(binding coverageBinding) bool {
			return binding.Role == "primary" && binding.SeriesID == field.SeriesID && coverageSameSource(binding, field)
		}) {
			continue
		}
		if slices.ContainsFunc(fields, func(other coverageEvidence) bool {
			return other.SeriesID != field.SeriesID && other.KnowledgeBaseID == field.KnowledgeBaseID && other.DatasetID == field.DatasetID &&
				other.SnapshotHash == field.SnapshotHash && other.Sheet == field.Sheet && other.Metric == field.Metric
		}) {
			dimensions := maps.Clone(coverageObject(coverageObject(field.Raw)["dimensions"]))
			delete(dimensions, "source_series_code")
			readings[i].Subject = coverageAnalysisSubject(dimensions)
		}
	}
	return readings
}

func coveragePDFEvidence(base coverageEvidence, args, data map[string]any, contract *structuredresult.Contract) []coverageEvidence {
	if !base.Bound || base.Status != "ok" || base.Object == "" || base.Object != args["table_id"] || contract == nil {
		return nil
	}
	var result []coverageEvidence
	coverageRows(data, "rows", func(row map[string]any, path string) {
		spans := coverageObject(row["source_cells"])
		coverageRows(data, "columns", func(column map[string]any, _ string) {
			name := sourceNavigationString(column, "name")
			value := coverageObject(row["raw_values"])[name]
			cells := sourceNavigationRows(spans[name])
			if value == nil || len(cells) != 1 || citationInt(cells[0]["page"]) == 0 || cells[0]["source_sha256"] != contract.SourceSnapshot.SourceSHA256 {
				return
			}
			item := base
			item.Kind, item.Column = "cell", name
			item.RowLabel = sourceNavigationString(row, "_row_id")
			item.Page = citationInt(cells[0]["page"])
			item.JSONPointer, item.Raw = "/data"+path+"/raw_values/"+coveragePointerKey(name), value
			item.Cell = "/data" + path + "/source_cells/" + coveragePointerKey(name) + "/0"
			for _, sourceColumn := range contract.Columns {
				if sourceColumn.Name == name {
					item.Unit, item.UnitBasis = sourceColumn.Unit.Symbol, sourceColumn.Unit.EvidenceLevel
					break
				}
			}
			result = append(result, item)
		})
	})
	return result
}
