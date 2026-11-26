package agent

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
)

func tableComparison(target taskCoverageTarget) bool {
	if target.StructuredTaskGoal == nil || target.VersionTask != "compare_sources" ||
		target.Table == "" || len(target.ComparisonSources) != 2 {
		return false
	}
	aggregation, _ := target.Delivery["aggregation"].(string)
	_, _, grain := coverageTargetDateRange(target)
	return (aggregation == "" || aggregation == "as_reported") && slices.Contains([]string{"day", "month", "year"}, grain)
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
		sameSource := prior.KnowledgeBaseID == selected.KnowledgeBaseID && prior.SourceID == selected.SourceID
		return (prior.SourceRef == ref) != sameSource
	})
}

// A read's image catalog locates the object. Only its actual source-bound OCR
// can provide a table cell. Keep that text on its own pointer and reference.
func coverageImageTableBodies(data map[string]any, bodies []coverageEvidence) []coverageEvidence {
	var observations []coverageEvidence
	coverageRows(data, types.DocumentImageTextEvidenceKey, func(row map[string]any, path string) {
		if row["kind"] != string(types.ChunkTypeImageOCR) || row["truncated"] == true ||
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

func evaluateTableComparison(target taskCoverageTarget, input coverageRecord) coverageCompletion {
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

func tableComparisonBindingMatches(record coverageRecord, binding coverageBinding, item coverageEvidence) bool {
	return item.Bound && coverageCurrentEvidence(record, item) && coverageSameSource(binding, item) &&
		coverageLocation(binding, item) && binding.EvidenceID == item.EvidenceID && binding.JSONPointer == item.JSONPointer
}

func tableComparisonSupportingFacts(target taskCoverageTarget, input coverageRecord, primary []coverageFact, from, to, grain string) []coverageFact {
	var supporting []coverageFact
	for _, binding := range input.Bindings {
		if len(supporting) >= 8 {
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
			if item.Kind == "cell" && item.RangeComplete && item.Metric == binding.Metric {
				_, verified = terminalComparisonNumber(item)
			} else {
				fact, verified = tableComparisonFact(otherField, input, binding, item, from, to, grain)
			}
			if !verified || !slices.ContainsFunc(primary, func(peer coverageFact) bool {
				return peer.Evidence.Metric != fact.Metric && sameTableComparisonRow(peer.Evidence, fact)
			}) || slices.ContainsFunc(supporting, func(prior coverageFact) bool {
				return prior.Evidence.Metric == fact.Metric && prior.Evidence.SourceID == fact.SourceID &&
					prior.Evidence.KnowledgeBaseID == fact.KnowledgeBaseID && prior.Evidence.Sheet == fact.Sheet &&
					prior.Evidence.Date == fact.Date
			}) {
				continue
			}
			supporting = append(supporting, coverageFact{Role: "supporting", Evidence: fact})
		}
	}
	return supporting
}

func sameTableComparisonRow(first, second coverageEvidence) bool {
	if first.Kind != second.Kind || first.SourceID != second.SourceID || first.KnowledgeBaseID != second.KnowledgeBaseID ||
		first.KnowledgeID != "" && second.KnowledgeID != "" && first.KnowledgeID != second.KnowledgeID ||
		first.Sheet != second.Sheet || first.Date != second.Date {
		return false
	}
	if first.Kind == "body" {
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
		nativePeriod := firstNonEmptyAgent(item.Period, item.Date)
		if item.Grain == "month" && item.Period == "" && item.Date >= from && item.Date <= to {
			nativePeriod = from[:7]
		}
		if item.Sheet != target.Table || item.Metric == "" || item.Cell == "" || item.DateCell == "" && item.RowLabel == "" ||
			!comparisonPeriodMatches(nativePeriod, from, to, grain) || item.Grain != grain && !exactMonthEnd {
			return item, false
		}
		if binding.TargetField != "" && binding.TargetField != item.Metric {
			return item, false
		}
	case "body":
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
	item.PeriodFrom, item.PeriodTo, item.Grain = from, to, grain
	// A more specific native field requires an explicit source association
	// to the verified header, never a different field with the same value.
	if item.Metric != target.Metric && (binding.TargetField != item.Metric || !strings.Contains(item.Metric, target.Metric)) {
		return item, false
	}
	_, numeric := terminalComparisonNumber(item)
	return item, numeric
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
		for _, layout := range []string{"2006-01", "2006年1月", "2006/1"} {
			if month, err := time.Parse(layout, label); err == nil {
				return month.Equal(start)
			}
		}
		return coverageDay(label) == to
	case "year":
		start, err := time.Parse(time.DateOnly, from)
		return err == nil && start.YearDay() == 1 && start.AddDate(1, 0, -1).Format(time.DateOnly) == to && label == start.Format("2006")
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
