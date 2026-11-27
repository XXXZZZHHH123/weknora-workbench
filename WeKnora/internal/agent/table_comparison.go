package agent

import (
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
)

func tableComparison(target taskCoverageTarget) bool {
	if target.StructuredTaskGoal == nil || target.VersionTask != "compare_sources" {
		return false
	}
	if agenttools.DocumentTableEvidenceEnabled() {
		aggregation, _ := target.Delivery["aggregation"].(string)
		return plannedTableComparison(target) && (aggregation == "" || aggregation == "as_reported")
	}
	return comparisonTableScopeValid(target) && len(target.ComparisonSources) == 2
}

func comparisonTableScopeValid(target taskCoverageTarget) bool {
	aggregation, _ := target.Delivery["aggregation"].(string)
	_, _, grain := coverageTargetDateRange(target)
	return target.Table != "" && (aggregation == "" || aggregation == "as_reported") && slices.Contains([]string{"day", "month", "year"}, grain)
}

func (e *AgentEngine) coverageComparisonSourceMatches(goal int, ref string, selected coverageEvidence) bool {
	id := firstNonEmptyAgent(selected.KnowledgeID, selected.SourceID)
	if !e.coverageScopeAllows(selected.KnowledgeBaseID, id) {
		return false
	}
	if selected.SourceDomain == "knowledge" && !agenttools.DocumentScopeAllowsAssociation(e.toolRegistry, id) {
		return false
	}
	if taskCoverageSourceHandle(ref) && (e.modelContext == nil || e.modelContext.RegisterDocument(id) != ref) {
		return false
	}
	return !slices.ContainsFunc(e.evidenceCompletion.record.Bindings, func(prior coverageBinding) bool {
		if prior.GoalIndex != goal || prior.SourceRef == "" {
			return false
		}
		priorID, selectedID := prior.SourceID, selected.SourceID
		if agenttools.DocumentTableEvidenceEnabled() {
			priorID, selectedID = firstNonEmptyAgent(prior.PhysicalSourceID, priorID), id
		}
		sameSource := prior.KnowledgeBaseID == selected.KnowledgeBaseID && priorID == selectedID
		return (prior.SourceRef == ref) != sameSource
	})
}

// A read's image catalog locates the object. Only its actual source-bound OCR
// can provide a table cell. Keep that text on its own pointer and reference.
func coverageImageTableBodies(data map[string]any, bodies []coverageEvidence) []coverageEvidence {
	var observations []coverageEvidence
	coverageRows(data, types.DocumentImageTextEvidenceKey, func(row map[string]any, path string) {
		if row["kind"] != string(types.ChunkTypeImageOCR) || row["truncated"] == true ||
			(agenttools.DocumentTableEvidenceEnabled() || data["table_reads"] != nil) && row["text_truncated"] == true ||
			!reviewImageTextBoundToSource(data, row) {
			return
		}
		page := citationInt(row["page"])
		parent := slices.IndexFunc(bodies, func(item coverageEvidence) bool {
			return item.Kind == "body" && item.Bound && item.Page == page && item.ChunkID == row["chunk_id"]
		})
		if parent == -1 || page == 0 {
			return
		}
		item := bodies[parent]
		item.Object = sourceNavigationString(row, "stored_reference")
		item.JSONPointer = "/data" + path + "/text"
		body := sourceNavigationString(row, "text")
		item.Raw, item.ContentHash = body, candidateTextDigest(body)
		start, end := 0, utf8.RuneCountInString(body)
		item.SpanStart, item.SpanEnd = &start, &end
		observations = append(observations, item)
	})
	return observations
}

// Only the read tool's actual table objects become selectable evidence.
// The contributing chunks must independently pass native read provenance.
func coverageReadTableBodies(data map[string]any, bodies []coverageEvidence) []coverageEvidence {
	var tables []coverageEvidence
	coverageRows(data, "table_reads", func(row map[string]any, _ string) {
		page := citationInt(row["page"])
		grid := coverageOffset(row["grid_index"])
		title, object := sourceNavigationString(row, "title"), sourceNavigationString(row, "object_id")
		bodyChunk, titleChunk := sourceNavigationString(row, "body_chunk_id"), sourceNavigationString(row, "title_chunk_id")
		bodyPointer, titlePointer := sourceNavigationString(row, "body_pointer"), sourceNavigationString(row, "title_pointer")
		if page <= 0 || grid == nil || row["grid_complete"] != true || title == "" || object == "" || bodyChunk == "" || titleChunk == "" {
			return
		}
		find := func(chunk, pointer string, actualPage int) int {
			return slices.IndexFunc(bodies, func(item coverageEvidence) bool {
				return item.Kind == "body" && item.Operation == agenttools.ToolReadDocument && item.Bound && item.Complete &&
					item.ChunkID == chunk && item.JSONPointer == pointer && (actualPage == 0 || item.Page == actualPage)
			})
		}
		index, section := find(bodyChunk, bodyPointer, page), find(titleChunk, titlePointer, page)
		if index < 0 || section < 0 {
			return
		}
		body, heading := bodies[index], bodies[section]
		headingText, _ := heading.Raw.(string)
		if body.SourceID != heading.SourceID || body.KnowledgeBaseID != heading.KnowledgeBaseID || !strings.Contains(headingText, title) ||
			body.ToolCallID != heading.ToolCallID || (body.Object != "" && body.Object != sourceNavigationString(row, "figure_id")) {
			return
		}
		body.Sheet, body.Object, body.TableGrid, body.TableTitleChunkID = title, object, grid, titleChunk
		body.SourceDate = ""
		sourceDate := coverageDay(sourceNavigationString(row, "disclosure_date"))
		disclosureChunk, disclosurePointer := sourceNavigationString(row, "disclosure_chunk_id"), sourceNavigationString(row, "disclosure_pointer")
		if sourceDate != "" && disclosurePointer != "" {
			if dateIndex := find(disclosureChunk, disclosurePointer, 0); dateIndex >= 0 {
				disclosureText, _ := bodies[dateIndex].Raw.(string)
				label := firstNonEmptyAgent(sourceNavigationString(row, "disclosure_label"), sourceNavigationString(row, "disclosure_date"))
				if strings.Contains(disclosureText, label) {
					body.SourceDate, body.TableDisclosureChunkID = sourceDate, disclosureChunk
				}
			} else if strings.HasPrefix(disclosurePointer, "/data/document/metadata/") {
				value, _ := reviewPointerValue(map[string]any{"data": data}, disclosurePointer)
				if raw, ok := value.(string); ok {
					date := coverageDay(raw)
					if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
						date = parsed.Format(time.DateOnly)
					}
					if date == sourceDate {
						body.SourceDate = sourceDate
					}
				}
			}
		}
		if unit := sourceNavigationString(row, "unit"); unit != "" {
			pointer := sourceNavigationString(row, "unit_pointer")
			if pointer != "" && (pointer == bodyPointer && strings.Contains(body.Raw.(string), unit) ||
				pointer == titlePointer && strings.Contains(headingText, unit)) {
				body.Unit, body.UnitBasis = unit, body.EvidenceID+"#"+pointer
			}
		}
		tables = append(tables, body)
	})
	return tables
}

func evaluateTableComparison(target taskCoverageTarget, input coverageRecord) coverageCompletion {
	if agenttools.DocumentTableEvidenceEnabled() {
		return evaluateReadTableComparison(target, input)
	}
	from, to, grain := coverageTargetDateRange(target)
	result := coverageCompletion{GoalIndex: target.GoalIndex, State: coverageOpen, Reason: "comparison_source_unverified", NextRead: "read",
		GapID:           reviewDigest([]any{input.Revision, input.AuthScope, target.GoalIndex, target.Table, target.Metric, target.Period, target.ComparisonSources}),
		BudgetRemaining: 1}
	for _, side := range target.ComparisonSources {
		var matches []coverageEvidence
		for _, binding := range input.Bindings {
			if binding.GoalIndex != target.GoalIndex || binding.Role != "primary" || binding.SourceRef != side || binding.Provisional {
				continue
			}
			for _, item := range input.Evidence {
				if !tableComparisonBindingMatches(input, binding, item) {
					continue
				}
				fact, ok := tableComparisonFact(target, input, binding, item, from, to, grain)
				if ok && !slices.ContainsFunc(matches, func(prior coverageEvidence) bool {
					return prior.SourceID == fact.SourceID && prior.KnowledgeBaseID == fact.KnowledgeBaseID &&
						prior.SnapshotHash == fact.SnapshotHash && prior.Sheet == fact.Sheet && prior.Cell == fact.Cell &&
						prior.ChunkID == fact.ChunkID && prior.Object == fact.Object && prior.Date == fact.Date && prior.Metric == fact.Metric && prior.Raw == fact.Raw
				}) {
					matches = append(matches, fact)
				}
			}
		}
		if len(matches) == 1 {
			result.pin("scoped_value", matches[0])
			continue
		}
		result.OpenItems = append(result.OpenItems, side)
	}
	if len(result.PinnedFacts) == 2 {
		first, second := result.PinnedFacts[0].Evidence, result.PinnedFacts[1].Evidence
		if first.Metric == second.Metric && comparisonPhysicalSource(first) != comparisonPhysicalSource(second) {
			result.PinnedFacts = append(result.PinnedFacts, tableComparisonSupportingFacts(target, input, result.PinnedFacts, from, to, grain)...)
			return result.satisfied("matching_primary")
		}
		// Individually selected values do not establish a common comparison.
		result.PinnedFacts, result.SatisfiedRefs = nil, nil
		result.OpenItems = slices.Clone(target.ComparisonSources)
		result.Reason = "comparison_scope_conflict"
	}
	return result.withStop(input.StopReason)
}

func evaluateReadTableComparison(target taskCoverageTarget, input coverageRecord) coverageCompletion {
	from, to, grain := coverageTargetDateRange(target)
	result := coverageCompletion{GoalIndex: target.GoalIndex, State: coverageOpen, Reason: "comparison_source_unverified", NextRead: "read",
		GapID:           reviewDigest([]any{input.Revision, input.AuthScope, target.GoalIndex, target.Table, target.Metric, target.Period}),
		BudgetRemaining: 1, UnresolvedSides: max(target.UnresolvedComparisonSources, 2-len(target.ComparisonSources))}
	if !comparisonTableScopeValid(target) || target.Metric == "" || coverageDay(from) == "" || coverageDay(to) == "" {
		result.OpenItems = slices.Clone(target.ComparisonSources)
		result.Reason, result.NextRead, result.BudgetRemaining = "comparison_target_incomplete", "", 0
		return result.withStop(input.StopReason)
	}
	for _, side := range target.ComparisonSources {
		var sideResult coverageCompletion
		for _, binding := range input.Bindings {
			if binding.GoalIndex != target.GoalIndex || binding.Role != "primary" || binding.SourceRef != side || binding.Provisional {
				continue
			}
			for _, item := range input.Evidence {
				if !tableComparisonBindingMatches(input, binding, item) {
					continue
				}
				if fact, ok := tableComparisonFact(target, input, binding, item, from, to, grain); ok {
					fact.ComparisonSourceRef = side
					sideResult.pin("scoped_value", fact)
				}
			}
		}
		if len(sideResult.PinnedFacts) == 1 {
			fact := sideResult.PinnedFacts[0].Evidence
			result.pin("scoped_value", fact)
			if fact.ComparisonGrainUnverified {
				result.OpenItems = append(result.OpenItems, side)
			}
		} else {
			result.OpenItems = append(result.OpenItems, side)
		}
	}
	if len(result.PinnedFacts) != 2 || result.UnresolvedSides != 0 {
		if result.UnresolvedSides != 0 {
			result.Reason = "comparison_source_unresolved"
		}
		return result.withStop(input.StopReason)
	}
	if len(result.OpenItems) != 0 {
		result.Reason = "comparison_grain_unverified"
		return result.withStop(input.StopReason)
	}
	first, second := result.PinnedFacts[0].Evidence, result.PinnedFacts[1].Evidence
	if first.Metric != second.Metric || first.Subject != second.Subject ||
		comparisonPhysicalSource(first) == comparisonPhysicalSource(second) {
		result.Reason, result.NextRead, result.BudgetRemaining = "comparison_scope_conflict", "", 0
		return result.withStop(input.StopReason)
	}
	if comparisonUnitStatus(first, second) == "incompatible" || target.RequestedUnit != "" &&
		(terminalUnitFromSource(first) && first.Unit != target.RequestedUnit || terminalUnitFromSource(second) && second.Unit != target.RequestedUnit) {
		result.Reason, result.NextRead, result.BudgetRemaining = "comparison_units_incompatible", "", 0
		return result.withStop(input.StopReason)
	}
	for _, fact := range tableComparisonSupportingFacts(target, input, result.PinnedFacts, from, to, grain) {
		result.pin(fact.Role, fact.Evidence)
	}
	return result.satisfied("matching_primary")
}

func tableComparisonBindingMatches(record coverageRecord, binding coverageBinding, item coverageEvidence) bool {
	if agenttools.DocumentTableEvidenceEnabled() && item.TableGrid != nil &&
		binding.TableObjectGrid != strconv.Itoa(*item.TableGrid) {
		return false
	}
	return item.Bound && coverageCurrentEvidence(record, item) && coverageSameSource(binding, item) &&
		coverageLocation(binding, item) && binding.EvidenceID == item.EvidenceID && binding.JSONPointer == item.JSONPointer &&
		(!agenttools.DocumentTableEvidenceEnabled() ||
			coverageEqual(binding.PhysicalSourceID, comparisonPhysicalSource(item)[1]))
}

// The model's actual series query identifies the native field. Match that
// exact field in the read table and use the plan's period and table identity.
// These are observed operands, even when the optional declaration did not
// resolve both handles. They cannot authorize a source or establish cadence.
func (e *AgentEngine) tableReceiptReadings(target taskCoverageTarget, record coverageRecord, draft string) ([]coverageFact, bool) {
	if !plannedTableComparison(target) || !comparisonTableScopeValid(target) || target.Metric == "" {
		return nil, false
	}
	from, to, grain := coverageTargetDateRange(target)
	if from == "" || to == "" {
		return nil, false
	}
	var activeDocs, selectedDocs []string
	if e.toolRegistry != nil {
		activeDocs = agenttools.CurrentDocumentScopeIDs(e.toolRegistry)
	}
	libraries := make(map[string]string)
	for _, item := range record.Evidence {
		if item.Operation == agenttools.ToolBindDocumentScope && item.Kind == "directory" && item.Bound && slices.Contains(activeDocs, item.SourceID) {
			if !slices.Contains(selectedDocs, item.SourceID) {
				selectedDocs = append(selectedDocs, item.SourceID)
			}
			libraries[item.SourceID] = item.KnowledgeBaseID
		}
	}
	var plannedDocs []string
	plannedLibraries := make(map[string]string)
	if e.modelContext != nil {
		for _, ref := range target.ComparisonSources {
			if taskCoverageSourceHandle(ref) {
				for _, doc := range e.selectedDocs {
					if doc != nil && doc.KnowledgeID != "" && e.modelContext.RegisterDocument(doc.KnowledgeID) == ref &&
						!slices.Contains(plannedDocs, doc.KnowledgeID) {
						plannedDocs = append(plannedDocs, doc.KnowledgeID)
						plannedLibraries[doc.KnowledgeID] = doc.KnowledgeBaseID
					}
				}
			}
		}
	}
	if len(plannedDocs) == 2 || len(plannedDocs) == 1 && !slices.Contains(selectedDocs, plannedDocs[0]) {
		selectedDocs, libraries = plannedDocs, plannedLibraries
	}
	sourceConflict := false
	for _, binding := range record.Bindings {
		id := binding.PhysicalSourceID
		if id != "" && binding.GoalIndex == target.GoalIndex && binding.Role == "primary" && binding.SourceRef != "" && !binding.Provisional {
			if library := libraries[id]; library != "" && binding.KnowledgeBaseID != library {
				sourceConflict = true
				continue
			}
			if !slices.Contains(selectedDocs, id) {
				selectedDocs = append(selectedDocs, id)
			}
			libraries[id] = binding.KnowledgeBaseID
		}
	}
	allows := func(item coverageEvidence) bool {
		library, id := item.KnowledgeBaseID, comparisonPhysicalSource(item)[1]
		if len(selectedDocs) >= 2 && !slices.Contains(selectedDocs, id) || libraries[id] != "" && libraries[id] != library {
			return false
		}
		for _, binding := range record.Bindings {
			if binding.GoalIndex != target.GoalIndex || binding.Role != "primary" || binding.Provisional ||
				!coverageSameSource(binding, item) {
				continue
			}
			if !coverageLocation(binding, item) || binding.Metric != "" && item.Metric != "" && binding.Metric != item.Metric {
				return false
			}
		}
		return true
	}
	// The same physical observation may be returned by repeated reads.
	add := func(facts []coverageFact, item coverageEvidence) []coverageFact {
		key := coverageEvidenceFactKey(item)
		for i := range facts {
			if coverageEvidenceFactKey(facts[i].Evidence) == key {
				facts[i].Evidence.Receipts = mergeCoverageFactReceipts(coverageFactReceipts(facts[i].Evidence), coverageFactReceipts(item))
				return facts
			}
		}
		return append(facts, coverageFact{Role: "scoped_value", Evidence: item})
	}
	var series []coverageFact
	for _, item := range record.Evidence {
		if item.Kind != "cell" || (item.Operation != "query_series" &&
			!(item.Operation == "analyze_table" && item.WorkbookBound && item.ResultJSONPointer != "")) || item.Sheet != target.Table ||
			!item.Bound || !item.RangeComplete || !coverageCurrentEvidence(record, item) || !allows(item) ||
			item.SeriesID == "" || item.Metric == "" || item.DateCell == "" || item.Cell == "" ||
			(item.Date != to && !comparisonNativeRowPeriod(item.Period, from, to, grain)) ||
			(item.PeriodFrom != from || item.PeriodTo != to) && (item.PeriodFrom != to || item.PeriodTo != to) {
			continue
		}
		if _, ok := coverageComparisonNumber(item); ok {
			series = add(series, item)
		}
	}
	if len(series) > 1 {
		series = slices.DeleteFunc(series, func(fact coverageFact) bool {
			return fact.Evidence.Metric != target.Metric
		})
	}
	var alternateField string
	if len(series) == 1 {
		item := series[0].Evidence
		for _, text := range strings.Split(draft, "\n\n") {
			paragraph := newCoverageDraftParagraph(text)
			if item.Metric != target.Metric && paragraph.hasLabel(item.SourceTitle) && paragraph.hasField(item) &&
				(paragraph.hasLabel(item.Cell) || coverageTextHasDate(text, item.Date)) && paragraph.displays(item, "scoped_value", false) {
				alternateField = item.Metric
				break
			}
		}
	}
	var report, alternate []coverageFact
	var exactFieldInRead bool
	for _, item := range record.Evidence {
		if item.Kind != "body" || item.TableGrid == nil || item.Sheet != target.Table || !item.Bound ||
			!coverageCurrentEvidence(record, item) || !allows(item) {
			continue
		}
		body, _ := item.Raw.(string)
		grids := agenttools.ParseDocumentTableGrids(body)
		if *item.TableGrid < 0 || *item.TableGrid >= len(grids) || !grids[*item.TableGrid].ParseComplete {
			continue
		}
		rows := grids[*item.TableGrid].Rows
		if len(rows) < 2 || len(rows[0]) < 2 {
			continue
		}
		exactFieldInRead = exactFieldInRead || slices.Contains(rows[0][1:], target.Metric)
		for _, row := range rows[1:] {
			if len(row) == 0 || !comparisonNativeRowPeriod(row[0], from, to, grain) {
				continue
			}
			if fact, ok := selectedReadTableCell(target, coverageBinding{Metric: target.Metric, Date: row[0]}, item, from, to, grain); ok && allows(fact) {
				report = add(report, fact)
			}
			if alternateField != "" {
				if fact, ok := selectedReadTableCell(target, coverageBinding{Metric: alternateField, Date: row[0]}, item, from, to, grain); ok && allows(fact) {
					alternate = add(alternate, fact)
				}
			}
		}
	}
	if !exactFieldInRead {
		report = alternate
	}
	var readings []coverageFact
	if len(report) == 1 {
		readings = report
	}
	aligned := false
	if len(series) == 1 {
		item := series[0].Evidence
		if len(readings) == 0 || readings[0].Evidence.Metric == item.Metric &&
			comparisonPhysicalSource(readings[0].Evidence) != comparisonPhysicalSource(item) {
			aligned = !sourceConflict && len(readings) == 1 && (len(selectedDocs) == 0 || len(selectedDocs) == 2) &&
				(item.Grain == "" || item.GrainFromLookup || item.Grain == grain)
			item.ComparisonGrainUnverified = aligned && item.Grain != grain && !comparisonNativeRowPeriod(item.Period, from, to, grain)
			readings = append(readings, coverageFact{Role: series[0].Role, Evidence: item})
		}
	}
	return readings, aligned
}

func tableComparisonSupportingFacts(target taskCoverageTarget, input coverageRecord, primary []coverageFact, from, to, grain string) []coverageFact {
	var supporting coverageCompletion
	strict := agenttools.DocumentTableEvidenceEnabled()
	for _, binding := range input.Bindings {
		if !strict && len(supporting.PinnedFacts) >= 8 {
			break
		}
		if binding.GoalIndex != target.GoalIndex || binding.Role != "supporting" || binding.Provisional || binding.Metric == "" {
			continue
		}
		goal := *target.StructuredTaskGoal
		goal.Metric = binding.Metric
		otherField := taskCoverageTarget{StructuredTaskGoal: &goal}
		for _, item := range input.Evidence {
			if !tableComparisonBindingMatches(input, binding, item) {
				continue
			}
			fact := item
			var verified bool
			if item.Kind == "cell" && item.RangeComplete && item.Metric == binding.Metric &&
				(!strict || item.Operation == "analyze_table" && item.WorkbookBound) {
				_, verified = coverageComparisonNumber(item)
				if strict {
					fact.PeriodFrom, fact.PeriodTo, fact.Grain = from, to, grain
					verified = verified && (binding.Unit == "" || terminalUnitFromSource(item) && item.Unit == binding.Unit)
				}
			} else {
				fact, verified = tableComparisonFact(otherField, input, binding, item, from, to, grain)
			}
			if !verified || !slices.ContainsFunc(primary, func(peer coverageFact) bool {
				return peer.Evidence.Metric != fact.Metric && sameTableComparisonRow(peer.Evidence, fact) &&
					(!strict || peer.Evidence.ComparisonSourceRef == binding.SourceRef && coverageEqual(grain, fact.Grain))
			}) {
				continue
			}
			if strict {
				fact.ComparisonSourceRef = binding.SourceRef
				if len(supporting.PinnedFacts) < 8 || slices.ContainsFunc(supporting.PinnedFacts, func(prior coverageFact) bool {
					return coverageEvidenceFactKey(prior.Evidence) == coverageEvidenceFactKey(fact)
				}) {
					supporting.pin("supporting", fact)
				}
				continue
			}
			if !slices.ContainsFunc(supporting.PinnedFacts, func(prior coverageFact) bool {
				return prior.Evidence.Metric == fact.Metric && prior.Evidence.SourceID == fact.SourceID &&
					prior.Evidence.KnowledgeBaseID == fact.KnowledgeBaseID && prior.Evidence.Sheet == fact.Sheet &&
					prior.Evidence.Date == fact.Date
			}) {
				supporting.PinnedFacts = append(supporting.PinnedFacts, coverageFact{Role: "supporting", Evidence: fact})
			}
		}
	}
	return supporting.PinnedFacts
}

func sameTableComparisonRow(first, second coverageEvidence) bool {
	if first.Kind != second.Kind || first.SourceID != second.SourceID || first.KnowledgeBaseID != second.KnowledgeBaseID ||
		first.KnowledgeID != "" && second.KnowledgeID != "" && first.KnowledgeID != second.KnowledgeID ||
		first.Sheet != second.Sheet || first.Date != second.Date {
		return false
	}
	if first.Kind == "body" {
		if agenttools.DocumentTableEvidenceEnabled() {
			return first.SnapshotHash == second.SnapshotHash && first.ChunkID == second.ChunkID &&
				first.Object == second.Object && first.Page == second.Page && first.RowLabel == second.RowLabel &&
				first.TableGrid != nil && second.TableGrid != nil && *first.TableGrid == *second.TableGrid
		}
		return first.SnapshotHash == second.SnapshotHash && first.EvidenceID == second.EvidenceID && first.JSONPointer == second.JSONPointer &&
			first.ChunkID == second.ChunkID && first.Object == second.Object && first.Page == second.Page
	}
	_, firstRow := coverageCellPosition(first.Cell)
	_, secondRow := coverageCellPosition(second.Cell)
	if firstRow == "" || firstRow != secondRow {
		return false
	}
	if first.SnapshotHash == second.SnapshotHash && first.DateCell != "" && first.DateCell == second.DateCell {
		return true
	}
	return second.Operation == "analyze_table" && second.WorkbookBound && second.WorkbookSnapshotHash == first.SnapshotHash &&
		second.RangeComplete && second.ResultJSONPointer != ""
}

func comparisonPhysicalSource(item coverageEvidence) [2]string {
	return [2]string{item.KnowledgeBaseID, firstNonEmptyAgent(item.KnowledgeID, item.SourceID)}
}

func tableComparisonFact(target taskCoverageTarget, input coverageRecord, binding coverageBinding, item coverageEvidence, from, to, grain string) (coverageEvidence, bool) {
	if coverageDay(from) == "" || coverageDay(to) == "" || grain == "" || item.SnapshotHash == "" {
		return item, false
	}
	switch item.Kind {
	case "cell":
		// A month-end source can carry an ISO day for the original monthly
		// row. The explicit model association is to the as-reported row,
		// and a genuinely daily series cannot stand in for a monthly value.
		exactMonthEnd := item.GrainFromLookup && grain == "month" && binding.Grain == grain &&
			binding.PeriodFrom == from && binding.PeriodTo == to && item.Operation == "query_series" &&
			item.PeriodFrom == item.Date && item.PeriodTo == item.Date && item.Date == to
		strict := agenttools.DocumentTableEvidenceEnabled()
		if strict && !exactMonthEnd && item.Grain == "" && grain == "month" &&
			binding.Grain == grain && binding.PeriodFrom == from && binding.PeriodTo == to && item.Operation == "query_series" &&
			item.RangeComplete && item.PeriodFrom == from && item.PeriodTo == to && item.Date == to {
			exactMonthEnd = true
		}
		nativePeriod := firstNonEmptyAgent(item.Period, item.Date)
		explicitNativePeriod := strict && item.DateMode == "period" && item.Period != "" &&
			comparisonNativeRowPeriod(item.Period, from, to, grain)
		if strict && grain == "month" && (item.Grain == "" || item.GrainFromLookup) && !explicitNativePeriod &&
			binding.Grain == grain && binding.PeriodFrom == from && binding.PeriodTo == to {
			if basis := workbookMonthLabelReceipt(input, item, from, to); basis != "" {
				item.Period, item.TablePeriodBasisRef = from[:7], basis
				nativePeriod, explicitNativePeriod = item.Period, true
			}
		}
		if item.Grain == "month" && item.Period == "" && item.Date >= from && item.Date <= to {
			nativePeriod = from[:7]
		}
		if item.Sheet != target.Table || item.Metric == "" || item.Cell == "" || item.DateCell == "" && item.RowLabel == "" ||
			!comparisonPeriodMatches(nativePeriod, from, to, grain) || item.Grain != grain && !exactMonthEnd && !explicitNativePeriod {
			return item, false
		}
		if binding.TargetField != "" && binding.TargetField != item.Metric {
			return item, false
		}
		// The original date and value are worth retaining. The model's choice
		// of that date as a candidate cannot turn a lookup into workbook grain.
		item.ComparisonGrainUnverified = strict && exactMonthEnd && !explicitNativePeriod && item.Grain != grain
	case "body":
		if agenttools.DocumentTableEvidenceEnabled() {
			fact, ok := selectedReadTableCell(target, binding, item, from, to, grain)
			if !ok {
				return item, false
			}
			item = fact
			break
		}
		if binding.Metric == "" || !comparisonPeriodMatches(binding.Date, from, to, grain) || item.Page == 0 || !item.Complete {
			return item, false
		}
		body, ok := item.Raw.(string)
		if !ok || item.ContentHash != candidateTextDigest(body) {
			return item, false
		}
		var section string
		for _, parent := range input.Evidence {
			if parent.Kind == "body" && parent.Bound && parent.Object == "" && parent.ToolCallID == item.ToolCallID &&
				parent.Page == item.Page && parent.ChunkID == item.ChunkID && parent.SnapshotHash == item.SnapshotHash &&
				coverageCurrentEvidence(input, parent) {
				text, _ := parent.Raw.(string)
				section = comparisonTableSection(text, target.Table, item.Object)
				break
			}
		}
		if section == "" {
			return item, false
		}
		if item.Object == "" {
			body = section
		}
		value, ok := comparisonTableIntersection(body, binding.Metric, binding.Date, item.Object != "")
		if !ok || binding.RawClaim != "" && binding.RawClaim != value {
			return item, false
		}
		item.Sheet, item.Metric, item.Date, item.RowLabel = target.Table, binding.Metric, binding.Date, binding.Date
		item.ReportedValue, item.Raw = value, json.Number(value)
	default:
		return item, false
	}
	if !item.ComparisonGrainUnverified {
		item.PeriodFrom, item.PeriodTo, item.Grain = from, to, grain
	}
	// A more specific native field requires an explicit source association
	// to the verified header, never a different field with the same value.
	if item.Metric != target.Metric && (binding.TargetField != item.Metric ||
		!agenttools.DocumentTableEvidenceEnabled() && !strings.Contains(item.Metric, target.Metric)) {
		return item, false
	}
	if agenttools.DocumentTableEvidenceEnabled() && binding.RequireUnit && !terminalUnitFromSource(item) {
		return item, false
	}
	_, numeric := coverageComparisonNumber(item)
	return item, numeric
}

// Excel stores dates with day precision even when the original date cell is
// formatted as a month label. Use that formatting only from the verified
// original date cell associated with this selected workbook value.
func workbookMonthLabelReceipt(record coverageRecord, value coverageEvidence, from, to string) string {
	dateCol, dateRow := coverageCellPosition(value.DateCell)
	col, row := coverageCellPosition(value.Cell)
	if dateCol == "" || col == "" || dateCol == col || dateRow != row || value.Date < from || value.Date > to ||
		value.SnapshotHash == "" || value.SourceDomain != "structured" {
		return ""
	}
	var basis string
	for _, item := range record.Evidence {
		if item.Kind != "cell" || item.Operation != "inspect_source" || !item.Bound || !coverageCurrentEvidence(record, item) ||
			item.SourceID != value.SourceID || item.KnowledgeBaseID != value.KnowledgeBaseID || item.SnapshotHash != value.SnapshotHash ||
			item.Sheet != value.Sheet || item.Cell != value.DateCell {
			continue
		}
		raw, ok := item.Raw.(string)
		if !ok || coverageDay(strings.TrimSuffix(raw, "T00:00:00")) != value.Date {
			continue
		}
		if !workbookMonthNumberFormat(item.OriginalNumberFormat) {
			return ""
		}
		pointer := strings.TrimSuffix(strings.TrimSuffix(item.JSONPointer, "/raw_value"), "/value")
		basis = item.EvidenceID + "#" + pointer + "/number_format"
	}
	return basis
}

var workbookMonthFormat = regexp.MustCompile(`^(?:yyyy[-/ .]m{1,2}|m{1,2}[-/ .]yyyy|yyyy年m{1,2}月)$`)

func workbookMonthNumberFormat(format string) bool {
	if format == "" || len(format) > 128 {
		return false
	}
	positive, _, _ := strings.Cut(format, ";")
	positive = strings.ToLower(strings.TrimSpace(positive))
	positive = strings.NewReplacer("\\-", "-", "\\/", "/", "\\.", ".", `"-"`, "-", `"/"`, "/", `"."`, ".",
		`"年"`, "年", `"月"`, "月").Replace(positive)
	return workbookMonthFormat.MatchString(positive)
}

func selectedReadTableCell(target taskCoverageTarget, binding coverageBinding, item coverageEvidence, from, to, grain string) (coverageEvidence, bool) {
	if binding.Metric == "" || item.Page == 0 || item.TableGrid == nil || item.TableTitleChunkID == "" ||
		item.Object == "" || item.Operation != agenttools.ToolReadDocument || !item.Complete || item.Sheet != target.Table ||
		!comparisonNativeRowPeriod(binding.Date, from, to, grain) {
		return item, false
	}
	body, ok := item.Raw.(string)
	if !ok || item.ContentHash != candidateTextDigest(body) {
		return item, false
	}
	grids := agenttools.ParseDocumentTableGrids(body)
	if *item.TableGrid < 0 || *item.TableGrid >= len(grids) {
		return item, false
	}
	grid := grids[*item.TableGrid]
	if !grid.ParseComplete {
		return item, false
	}
	rows := grid.Rows
	if len(rows) < 2 {
		return item, false
	}
	header := rows[0]
	column := slices.Index(header, binding.Metric)
	if column < 1 || slices.Contains(header[column+1:], binding.Metric) {
		return item, false
	}
	rowIndex, value := -1, ""
	for index, row := range rows[1:] {
		if len(row) == 0 || row[0] != binding.Date {
			continue
		}
		if len(row) != len(header) || rowIndex >= 0 || !documentTableNumber.MatchString(row[column]) {
			return item, false
		}
		rowIndex, value = index+1, row[column]
	}
	if rowIndex < 0 || binding.RawClaim != "" && binding.RawClaim != value ||
		binding.Unit != "" && (!terminalUnitFromSource(item) || item.Unit != binding.Unit) {
		return item, false
	}
	item.Metric, item.Column, item.Date, item.RowLabel, item.Period = binding.Metric, header[column], binding.Date, binding.Date, binding.Date
	item.Cell, item.ReportedValue, item.Raw = strconv.Itoa(rowIndex)+":"+strconv.Itoa(column), value, value
	return item, true
}

func comparisonNativeRowPeriod(label, from, to, grain string) bool {
	if grain == "month" && coverageDay(label) != "" {
		return false // A daily label does not establish a monthly observation.
	}
	return comparisonPeriodMatches(label, from, to, grain)
}

func comparisonPeriodMatches(label, from, to, grain string) bool {
	switch grain {
	case "day":
		return from == to && coverageDay(label) == from
	case "month":
		start, err := time.Parse(time.DateOnly, from)
		if err != nil || start.Day() != 1 || start.AddDate(0, 1, -1).Format(time.DateOnly) != to {
			return false
		}
		label = strings.TrimSpace(label)
		for _, layout := range []string{"2006-01", "2006-1", "2006年1月", "2006/1", "2006.1"} {
			if month, err := time.Parse(layout, label); err == nil {
				return month.Equal(start)
			}
		}
		return coverageDay(label) == to
	case "year":
		start, err := time.Parse(time.DateOnly, from)
		return err == nil && start.YearDay() == 1 && start.AddDate(1, 0, -1).Format(time.DateOnly) == to &&
			strings.TrimSuffix(strings.TrimSpace(label), "年") == start.Format("2006")
	default:
		return false
	}
}

// The table or figure must lie within its own title section. A page-level
// mention cannot attribute a sibling section's table to this title.
func comparisonTableSection(body, title, object string) string {
	lines := strings.Split(body, "\n")
	start, end := -1, len(lines)
	for i, line := range lines {
		line = strings.TrimSpace(line)
		label := strings.Trim(strings.TrimLeft(line, "# "), "*_ ")
		if label == title {
			if start >= 0 {
				return ""
			}
			start = i
			continue
		}
		if start >= 0 && end == len(lines) && markdownTableSectionHeading(line) {
			end = i
		}
	}
	if start < 0 {
		return ""
	}
	section := strings.Join(lines[start:end], "\n")
	if object != "" {
		// Match a figure destination, not a naked identifier in prose.
		anchor := "](" + object + ")"
		if strings.Count(section, anchor) != 1 || strings.Count(body, anchor) != 1 {
			return ""
		}
	}
	return section
}

func markdownTableSectionHeading(line string) bool {
	heading := strings.TrimLeft(line, "#")
	return len(line) != len(heading) && strings.HasPrefix(heading, " ")
}

func comparisonTableIntersection(body, metric, rowLabel string, imageOCR bool) (string, bool) {
	tables := parseDocumentSourceTables(body)
	if len(tables) > 1 {
		return "", false
	}
	if imageOCR && strings.Contains(body, "\t") {
		lines := strings.Split(strings.TrimSpace(body), "\n")
		rows := make([][]string, 0, len(lines))
		for _, line := range lines {
			fields := strings.Split(strings.TrimRight(line, "\r"), "\t")
			for i := range fields {
				fields[i] = strings.TrimSpace(fields[i])
			}
			rows = append(rows, fields)
		}
		tables = append(tables, documentSourceTable{Rows: rows, ParseComplete: true})
	}
	match := ""
	for _, table := range tables {
		if !table.ParseComplete || len(table.Rows) < 2 {
			continue
		}
		header := table.Rows[0]
		column := slices.Index(header, metric)
		if column <= 0 || slices.Contains(header[column+1:], metric) {
			continue
		}
		for _, row := range table.Rows[1:] {
			if len(row) != len(header) || row[0] != rowLabel {
				continue
			}
			value := row[column]
			if match != "" || !documentTableNumber.MatchString(value) || !json.Valid([]byte(value)) {
				return "", false
			}
			match = value
		}
	}
	return match, match != ""
}

// Use the selected report and the actual peer field as recall evidence. A
// title match or a local field/date match returns native navigation only.
// A later model round still chooses the page and the table intersection.
func (e *AgentEngine) coverageDirectedComparisonRead() (string, map[string]any) {
	turn := &e.evidenceCompletion
	_, read := e.toolRegistry.DocumentCoverageCapabilities()
	if !read {
		return "", nil
	}
	for _, target := range e.evidenceCompletionTargets() {
		if !tableComparison(target) {
			continue
		}
		result := evaluateTableComparison(target, turn.record)
		if result.State != coverageOpen || len(result.PinnedFacts) != 1 {
			continue
		}
		peer := result.PinnedFacts[0].Evidence
		if peer.SourceDomain != "structured" || peer.Date == "" || peer.Metric == "" {
			continue
		}
		for _, binding := range turn.record.Bindings {
			if binding.GoalIndex != target.GoalIndex || !slices.Contains(result.OpenItems, binding.SourceRef) || binding.SourceID == "" {
				continue
			}
			index := slices.IndexFunc(turn.record.Evidence, func(item coverageEvidence) bool {
				return item.SourceDomain == "knowledge" && item.Bound && item.Operation == agenttools.ToolReadDocument &&
					coverageSameSource(binding, item) && coverageCurrentEvidence(turn.record, item)
			})
			if index < 0 {
				continue
			}
			title, metric, date := regexp.QuoteMeta(peer.Sheet), regexp.QuoteMeta(peer.Metric), regexp.QuoteMeta(peer.Date)
			query := title + "|(?s:" + metric + ".{0,160}" + date + "|" + date + ".{0,160}" + metric + ")"
			if len(query) > 512 || turn.directedSearches[query] {
				continue
			}
			return agenttools.ToolReadDocument, map[string]any{"id": turn.record.Evidence[index].SourceID, "query": query, "regex": true}
		}
	}
	return "", nil
}
