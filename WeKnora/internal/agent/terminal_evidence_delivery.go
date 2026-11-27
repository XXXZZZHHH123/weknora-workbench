package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"slices"
	"strings"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
)

type terminalCoverageFact struct {
	coverageFact
	required bool
}

type terminalCoverageDelivery struct {
	facts        []terminalCoverageFact
	notes        []string
	missing      []string
	conclusions  []string
	hasDirectory bool
	subject      string
	record       coverageRecord
	readReceipts []coverageEvidence
	chinese      bool
}

type terminalEvidenceSnapshot struct {
	scope string
	task  string
	calls map[string]string
}

// A newly appended call does not invalidate an older checked answer. A
// changed or removed receipt, changed snapshot, or narrowed scope does.
func (snapshot terminalEvidenceSnapshot) current(next terminalEvidenceSnapshot) bool {
	if snapshot.scope != "" && snapshot.scope != next.scope || snapshot.task != "" && snapshot.task != next.task {
		return false
	}
	for id, fingerprint := range snapshot.calls {
		if next.calls[id] != fingerprint {
			return false
		}
	}
	return true
}

func (snapshot terminalEvidenceSnapshot) staleCalls(next terminalEvidenceSnapshot) map[string]bool {
	var stale map[string]bool
	for id, fingerprint := range snapshot.calls {
		if next.calls[id] != fingerprint || snapshot.task != "" && snapshot.task != next.task {
			if stale == nil {
				stale = make(map[string]bool)
			}
			stale[id] = true
		}
	}
	return stale
}

// Build from completed native executions, not the cached declaration or
// the model's transcript. A changed receipt or revoked source is discarded.
func (e *AgentEngine) terminalCoverageRecord(ctx context.Context, state *types.AgentState) (coverageRecord, []coverageAttempt) {
	turn := &e.evidenceCompletion
	record := coverageRecord{
		Revision:  e.candidateTaskDigest(e.turnReview.Query, e.turnReview.Conversation),
		AuthScope: e.unverifiedSourceScopeDigest(ctx),
	}
	if turn.revision == record.Revision && turn.scope == record.AuthScope {
		record.Bindings = turn.record.Bindings
	}
	if state == nil || e.toolRegistry == nil {
		return record, nil
	}
	record, attempts := e.projectCurrentTaskCoverage(state, e.turnReview.TerminalStartStep, record)
	// Compute the current workbook relationship on the intact projection.
	// Deleting first would lose the receipt needed to verify some endpoints.
	var current []coverageEvidence
	for _, item := range record.Evidence {
		if item.Bound && coverageCurrentEvidence(record, item) && e.coverageEvidenceAllowed(ctx, nil, item) {
			current = append(current, item)
		}
	}
	record.Evidence = current
	return record, attempts
}

func (e *AgentEngine) terminalEvidenceRevision(ctx context.Context, state *types.AgentState) terminalEvidenceSnapshot {
	record, attempts := e.terminalCoverageRecord(ctx, state)
	conversation := e.semanticReviewConversation(e.turnReview.Conversation)
	if e.turnReview.TaskConversationCaptured {
		conversation = e.turnReview.TaskConversation
	}
	snapshot := terminalEvidenceSnapshot{
		scope: e.sourceScopeDigest(ctx, false),
		task:  reviewDigest(documentScopeUserTexts(e.turnReview.Query, conversation)),
	}
	if len(attempts) == 0 {
		return snapshot
	}
	byCall := make(map[string][]string, len(attempts))
	for _, attempt := range attempts {
		byCall[attempt.ToolCallID] = append(byCall[attempt.ToolCallID], attempt.EvidenceID)
	}
	for _, item := range record.Evidence {
		byCall[item.ToolCallID] = append(byCall[item.ToolCallID], item.SnapshotHash, item.ContentHash, item.JSONPointer)
	}
	snapshot.calls = make(map[string]string, len(byCall))
	for id, receipt := range byCall {
		snapshot.calls[id] = reviewDigest(receipt)
	}
	return snapshot
}

func (e *AgentEngine) terminalCoverageFailure(ctx context.Context, state *types.AgentState, failures ...terminalFailure) string {
	failure := terminalEmpty
	if len(failures) > 0 {
		failure = failures[0]
	}
	delivery := e.terminalCoverageDelivery(ctx, state)
	return delivery.renderFallback(failure)
}

// Rank associated facts before directly verified native observations. A gap
// and its nearest value are a single finding. Unscoped exploratory rows are
// never enumerated as a substitute for the requested result.
func (e *AgentEngine) terminalCoverageDelivery(ctx context.Context, state *types.AgentState) terminalCoverageDelivery {
	d := terminalCoverageDelivery{chinese: strings.HasPrefix(types.LanguageFromContextOrDefault(ctx), "zh")}
	record, attempts := e.terminalCoverageRecord(ctx, state)
	d.subject = terminalEvidenceText(taskPlanPrefix(e.coverageAnalysisQuestion(), 160))
	targets := slices.DeleteFunc(slices.Clone(e.evidenceCompletionTargets()), func(target taskCoverageTarget) bool {
		return target.SourceTime == "general_concept" || slices.Equal(target.Required, []string{"optional"})
	})
	add := func(role string, item coverageEvidence, required bool) {
		for i, prior := range d.facts {
			p := prior.Evidence
			if terminalSameFieldGap(record, item, p) || item.Kind == p.Kind && item.SourceID == p.SourceID &&
				item.KnowledgeBaseID == p.KnowledgeBaseID && item.SnapshotHash == p.SnapshotHash && item.Sheet == p.Sheet &&
				item.SeriesID == p.SeriesID && item.Column == p.Column && item.Date == p.Date && item.Cell == p.Cell &&
				item.SourceDate == p.SourceDate && item.PeriodFrom == p.PeriodFrom && item.PeriodTo == p.PeriodTo &&
				item.Period == p.Period && item.ReportedValue == p.ReportedValue && reviewDigest(item.Raw) == reviewDigest(p.Raw) {
				if required && !prior.required {
					d.facts[i] = terminalCoverageFact{coverageFact{role, item}, true}
				}
				return
			}
		}
		d.facts = append(d.facts, terminalCoverageFact{coverageFact{role, item}, required})
	}

	turn := &e.evidenceCompletion
	if turn.revision == record.Revision && turn.scope == record.AuthScope {
		record.Evidence = append(record.Evidence, terminalInspectedChoiceFacts(record, turn.record.Evidence)...)
	}
	for _, target := range targets {
		scoped := e.coverageRecordForTarget(ctx, target, record)
		for _, result := range evaluateTaskCoverage(target, scoped) {
			if (target.VersionTask == "compare_sources" || target.VersionTask == "compare_versions") && result.State == coverageSatisfied {
				d.addComparison(result.PinnedFacts)
			}
			for _, missing := range result.OpenItems {
				d.addMissing(target, missing)
			}
			for _, fact := range result.PinnedFacts {
				if fact.Role == "last_valid_date" { // Rendered with its matching gap.
					continue
				}
				add(fact.Role, fact.Evidence, true)
			}
		}
	}
	if len(targets) == 0 {
		record.Evidence = append(record.Evidence, nativeCoverageGapFacts(taskCoverageTarget{StructuredTaskGoal: &StructuredTaskGoal{}}, record)...)
		// Historical analysis contracts can prove a missing tail only within
		// the actual retry bounds. They provide no assertion about other dates.
		for _, item := range record.Evidence {
			if !coverageAnalysisEndpoint(item) && !coverageAnalysisMissingCell(item) {
				continue
			}
			from, to := coverageAnalysisRetriedScope(attempts, item, e.coverageAnalysisQuestion())
			if gap, ok := coverageAnalysisGap(item, from, to, ""); ok {
				record.Evidence = append(record.Evidence, gap)
			}
		}
	}
	d.record = record
	exactMissingLookup := len(targets) == 0 && slices.ContainsFunc(record.Evidence, func(item coverageEvidence) bool {
		return item.Kind == "gap" && item.Complete && item.PeriodFrom != "" && item.PeriodFrom == item.PeriodTo
	})

	for _, item := range record.Evidence {
		target, relevant := terminalCoverageTarget(record, targets, item)
		if !relevant || len(targets) > 0 && !e.coverageEvidenceAllowed(ctx, &target, item) {
			continue
		}
		switch {
		case item.Kind == "gap" && item.Complete && coverageExactRange(item):
			if exactMissingLookup && item.PeriodFrom != item.PeriodTo {
				// With no chosen target, prefer the actual precise absence
				// receipt to exploratory range absences. Positive reads do
				// not inherit this gap's scope.
				continue
			}
			if coverageAnalysisLatestBoundary(item) && len(targets) > 0 {
				if _, matches := e.plannedAnalysisGap(target, record, item); !matches {
					continue
				}
			}
			add("coverage_boundary", item, false)
			for _, observed := range record.Evidence {
				if sameCoverageLastValid(record, item, observed) || coverageAnalysisFollowUpMatches(record, item, observed) {
					add("last_valid_value", observed, false)
				}
			}
		case item.Kind == "derived" && item.Complete && coverageExactRange(item):
			add("scoped_value", item, false)
		case item.Kind == "cell" && item.Date != "" && item.Metric != "" &&
			(item.Operation == "query_series" || item.Operation == "analyze_table") &&
			(item.Date == item.PeriodFrom && item.Date == item.PeriodTo || item.LatestInScope || item.RangeComplete ||
				item.Operation == "analyze_table" && item.WorkbookBound && item.Complete && item.ResultJSONPointer != "" && item.Cell != ""):
			add("scoped_value", item, false)
		case item.Kind == "body":
			add("source_passage", item, false)
		case item.Kind == "directory":
			if note := d.directoryNote(item, attempts); note != "" {
				d.hasDirectory = true
				d.notes = append(d.notes, note)
			}
		}
	}
	slices.Sort(d.notes)
	d.notes = slices.Compact(d.notes)
	var tryNotes []string
	for _, attempt := range attempts {
		d.readReceipts = append(d.readReceipts, e.terminalDocumentReadReceipt(ctx, attempt)...)
		if note := d.attemptNote(attempt); note != "" {
			tryNotes = append(tryNotes, note)
		}
	}
	slices.Sort(tryNotes)
	d.notes = append(d.notes, slices.Compact(tryNotes)...)
	return d
}
func terminalInspectedChoiceFacts(record coverageRecord, recorded []coverageEvidence) []coverageEvidence {
	observationsByLocation := make(map[[2]string]coverageEvidence)
	for _, item := range record.Evidence {
		if item.Kind == "directory" || item.Kind == "cell" && item.Operation == "inspect_source" {
			observationsByLocation[[2]string{item.EvidenceID, item.JSONPointer}] = item
		}
	}
	var result []coverageEvidence
	for _, gap := range recorded {
		if gap.Kind != "gap" || gap.Operation != "list_series" || gap.LastValidRef == "" {
			continue
		}
		field := observationsByLocation[[2]string{gap.EvidenceID, strings.TrimSuffix(gap.JSONPointer, "/last_valid_date")}]
		for _, original := range recorded {
			if original.Kind != "last_valid" || original.EvidenceID != gap.LastValidRef || original.SeriesID != gap.SeriesID {
				continue
			}
			date := observationsByLocation[[2]string{original.DateEvidenceID, original.DateJSONPointer}]
			value := observationsByLocation[[2]string{original.EvidenceID, original.JSONPointer}]
			if last, ok := coveragePinInspectedLastValid(field, date, value); ok {
				if boundary, ok := coverageInspectedBoundary(field, last, gap.PeriodFrom, gap.PeriodTo, gap.Grain); ok {
					result = append(result, boundary, last)
				}
			}
		}
	}
	return result
}

func terminalCoverageTarget(record coverageRecord, targets []taskCoverageTarget, item coverageEvidence) (taskCoverageTarget, bool) {
	if len(targets) == 0 {
		return taskCoverageTarget{}, true
	}
	for _, target := range targets {
		if kind := sourceNavigationString(target.Delivery, "kind"); kind != "" && kind != "text" {
			continue
		}
		from, to, _ := coverageTargetDateRange(target)
		if coverageExactRange(coverageEvidence{PeriodFrom: from, PeriodTo: to}) {
			switch item.Kind {
			case "gap", "derived":
				if item.PeriodFrom != from || item.PeriodTo != to {
					continue
				}
			case "cell":
				if item.Date != "" && (item.Date < from || item.Date > to) ||
					item.PeriodFrom != "" && item.PeriodFrom < from || item.PeriodTo != "" && item.PeriodTo > to {
					continue
				}
			}
		}
		hasSelection, matches := false, false
		for _, binding := range record.Bindings {
			if binding.GoalIndex != target.GoalIndex || binding.Role != "primary" && binding.Role != "supplement" ||
				binding.Provisional && slices.ContainsFunc(record.Bindings, func(other coverageBinding) bool { return coverageRefinesBinding(binding, other) }) {
				continue
			}
			hasSelection = true
			// The published SQL input has its own manifest snapshot. Trust that
			// result only if the selected version (when set) is the same.
			if coverageSameSource(binding, item) && coverageLocation(binding, item) {
				matches = true
				break
			}
		}
		if matches || !hasSelection && (len(targets) == 1 || item.Kind == "directory" || item.Kind == "navigation") {
			return target, true
		}
	}
	return taskCoverageTarget{}, false
}

var terminalEvidenceEscaper = strings.NewReplacer("\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "~", "\\~")

func terminalEvidenceText(value string) string {
	return terminalEvidenceEscaper.Replace(html.EscapeString(strings.Join(strings.Fields(value), " ")))
}

func terminalCoverageLabel(item coverageEvidence) string {
	var fields []string
	for _, value := range []string{item.SourceTitle, item.Sheet, item.Subject, item.Metric} {
		if value != "" && !slices.Contains(fields, terminalEvidenceText(value)) {
			fields = append(fields, terminalEvidenceText(value))
		}
	}
	return strings.Join(fields, " / ")
}

func terminalCoveragePeriod(from, to, period string) string {
	if period != "" {
		return period
	}
	if from == to || to == "" {
		return from
	}
	return from + " – " + to
}
func terminalUnitFromSource(item coverageEvidence) bool {
	return item.Unit != "" && !slices.Contains([]string{"", "none", "unknown", "needs_review", "mixed"}, item.UnitBasis)
}

func terminalSameFieldGap(record coverageRecord, item, prior coverageEvidence) bool {
	if item.Kind != "gap" || prior.Kind != "gap" || item.SeriesID == "" || item.KnowledgeBaseID != prior.KnowledgeBaseID ||
		item.SourceID != prior.SourceID || item.Sheet != prior.Sheet || item.SeriesID != prior.SeriesID ||
		item.Column != prior.Column || item.PeriodFrom != prior.PeriodFrom || item.PeriodTo != prior.PeriodTo ||
		item.Period != prior.Period || item.LastValidDate != prior.LastValidDate {
		return false
	}
	workbook := func(gap coverageEvidence) string {
		if gap.WorkbookSnapshotHash != "" && coverageCurrentAnalysisWorkbook(record, gap) {
			return gap.WorkbookSnapshotHash
		}
		return gap.SnapshotHash
	}
	return workbook(item) != "" && workbook(item) == workbook(prior)
}

func (d terminalCoverageDelivery) text(zh, en string) string {
	if d.chinese {
		return zh
	}
	return en
}

func (d terminalCoverageDelivery) renderFallback(failure terminalFailure) string {
	if failure == terminalSourceChanged {
		// The earlier task description may itself name a source no longer in scope.
		d.subject = ""
	}
	rank := func(fact terminalCoverageFact) int {
		switch {
		case fact.Role == "coverage_boundary" && fact.required:
			return 0
		case fact.required && fact.Role != "last_valid_value" && fact.Evidence.Kind != "body":
			return 1
		case fact.Role == "coverage_boundary":
			return 2
		case fact.required:
			return 3
		case fact.Evidence.Kind == "derived":
			return 4
		case fact.Evidence.Kind == "cell":
			return 5
		default:
			return 6
		}
	}
	slices.SortFunc(d.facts, func(a, b terminalCoverageFact) int {
		if order := rank(a) - rank(b); order != 0 {
			return order
		}
		if order := strings.Compare(b.Evidence.Date, a.Evidence.Date); order != 0 {
			return order
		}
		return strings.Compare(terminalCoverageFactKey(a.Evidence), terminalCoverageFactKey(b.Evidence))
	})
	var lines, groupOrder []string
	groups := make(map[string][]terminalCoverageFact)
	used := map[string]bool{}
	fields := map[string]string{}
	for _, fact := range d.facts {
		item := fact.Evidence
		if used[terminalCoverageFactKey(item)] {
			continue
		}
		if item.Kind == "gap" {
			if sentence := d.gapFinding(item, used); !slices.Contains(lines, sentence) {
				lines = append(lines, sentence)
			}
			continue
		}
		if fact.Role == "last_valid_value" && slices.ContainsFunc(d.facts, func(other terminalCoverageFact) bool {
			return other.Evidence.Kind == "gap" && d.nearestValue(other.Evidence, item)
		}) {
			continue
		}
		if item.Kind == "body" && !fact.required && (len(lines) != 0 || len(groups) != 0 || item.ReportedValue == "" && d.hasDirectory) {
			continue
		}
		if item.Kind == "cell" || item.Kind == "derived" {
			field := strings.Join([]string{item.KnowledgeBaseID, item.SourceID, item.SnapshotHash, item.Sheet,
				item.SeriesID, item.Column, item.Metric, item.PeriodFrom, item.PeriodTo, item.Period}, "\x00")
			date, seen := fields[field]
			if seen && date != item.Date && !fact.required {
				continue
			}
			fields[field] = item.Date
			key := strings.Join([]string{item.KnowledgeBaseID, item.SourceID, item.SnapshotHash, item.Sheet,
				item.Date, item.PeriodFrom, item.PeriodTo, item.Period, fmt.Sprint(item.LatestInScope)}, "\x00")
			if _, exists := groups[key]; !exists {
				groupOrder = append(groupOrder, key)
			}
			groups[key] = append(groups[key], fact)
			continue
		}
		if sentence := d.receiptFinding(item); sentence != "" && !slices.Contains(lines, sentence) {
			lines = append(lines, sentence)
			used[terminalCoverageFactKey(item)] = true
		}
	}
	for _, key := range groupOrder {
		group := slices.DeleteFunc(groups[key], func(fact terminalCoverageFact) bool { return used[terminalCoverageFactKey(fact.Evidence)] })
		if len(group) == 0 {
			continue
		}
		sentence := ""
		if len(group) == 1 {
			sentence = d.receiptFinding(group[0].Evidence)
		} else {
			sentence = d.groupedValues(group)
		}
		if sentence != "" && !slices.Contains(lines, sentence) {
			lines = append(lines, sentence)
		}
	}
	if len(lines) != 0 {
		lines = append(lines, d.unmatchedReadNotes()...)
		for _, missing := range d.missing {
			if !slices.Contains(lines, missing) {
				lines = append(lines, missing)
			}
		}
		return strings.Join(append(d.conclusions, lines...), "\n\n")
	}
	lead := d.failureNote(failure)
	if len(d.notes) == 0 {
		d.notes = append(d.notes, d.text("没有取得可核实的来源回执。", "No verifiable source receipt was obtained."))
	}
	for _, note := range d.notes {
		if !slices.Contains(lines, note) {
			lines = append(lines, note)
		}
		if len(lines) == 2 {
			break
		}
	}
	return lead + " " + strings.Join(append(lines, d.missing...), " ")
}

func (d terminalCoverageDelivery) unmatchedReadNotes() []string {
	numeric := slices.ContainsFunc(d.facts, func(fact terminalCoverageFact) bool {
		item := fact.Evidence
		return (item.Kind == "cell" || item.Kind == "derived" || item.Kind == "last_valid") && d.observedValue(item) != ""
	})
	if !numeric {
		return nil
	}
	type readPages struct {
		title string
		pages []int
	}
	reads := make(map[[2]string]readPages)
	for _, item := range d.readReceipts {
		if slices.ContainsFunc(d.facts, func(fact terminalCoverageFact) bool {
			other := fact.Evidence
			if other.KnowledgeBaseID != item.KnowledgeBaseID {
				return false
			}
			if other.SourceID == item.SourceID && other.ReportedValue != "" {
				return true
			}
			return other.SourceDomain == "structured" && (other.SourceID == item.SourceID || other.KnowledgeID == item.SourceID)
		}) {
			continue
		}
		key := [2]string{item.KnowledgeBaseID, item.SourceID}
		read := reads[key]
		read.title = terminalEvidenceText(item.SourceTitle)
		if item.Page > 0 && !slices.Contains(read.pages, item.Page) {
			read.pages = append(read.pages, item.Page)
		}
		reads[key] = read
	}
	var notes []string
	for _, read := range reads {
		slices.Sort(read.pages)
		if len(read.pages) == 0 {
			notes = append(notes, fmt.Sprintf(d.text("本轮读取了 %s 返回的片段，尚未在其中核到可对应上述读数的值。", "The read returned passages from %s. A matching reading from that source is still unverified."), read.title))
			continue
		}
		var pages []string
		for _, page := range read.pages {
			pages = append(pages, fmt.Sprint(page))
		}
		notes = append(notes, fmt.Sprintf(d.text("本轮读取了 %s 第 %s 页返回的片段，尚未核到与上述读数对应的值。", "The read returned passages from %s at page(s) %s. A matching reading for that scope is still unverified."), read.title, strings.Join(pages, d.text("、", ", "))))
	}
	slices.Sort(notes)
	return slices.Compact(notes)
}

// A native read can establish the inspected scope without returning a
// complete body or the requested table cell. It never establishes a value.
func (e *AgentEngine) terminalDocumentReadReceipt(ctx context.Context, attempt coverageAttempt) []coverageEvidence {
	if attempt.Operation != agenttools.ToolReadDocument || !attempt.Success || !agenttools.IsCurrentDocumentRead(e.toolRegistry, attempt.result) {
		return nil
	}
	data := attempt.result.Data
	item := coverageEvidence{SourceDomain: "knowledge", Operation: attempt.Operation,
		SourceID: sourceNavigationString(data, "knowledge_id"), KnowledgeBaseID: sourceNavigationString(data, "knowledge_base_id"),
		SourceTitle: sourceNavigationString(data, "knowledge_title")}
	fetched, _ := citationViewRevision(data["fetched_chunks"])
	if item.SourceTitle == "" || item.KnowledgeBaseID == "" || fetched == 0 || !e.coverageEvidenceAllowed(ctx, nil, item) {
		return nil
	}
	if data["offset_space"] == "physical_page_chunks" {
		item.Page, _ = citationViewRevision(data["page"])
	}
	var read []coverageEvidence
	for _, page := range sourceNavigationRows(data["page_results"]) {
		chunks, _ := citationViewRevision(page["page_chunks"])
		if page["success"] == true && page["offset_space"] == "physical_page_chunks" && chunks > 0 {
			observed := item
			observed.Page, _ = citationViewRevision(page["page"])
			read = append(read, observed)
		}
	}
	if len(read) == 0 {
		read = append(read, item)
	}
	return read
}

func (d terminalCoverageDelivery) groupedValues(group []terminalCoverageFact) string {
	first := group[0].Evidence
	period := terminalEvidenceText(firstNonEmptyAgent(first.Date, terminalCoveragePeriod(first.PeriodFrom, first.PeriodTo, first.Period)))
	if period == "" || first.SourceTitle == "" {
		return ""
	}
	label := terminalCoverageLabel(coverageEvidence{SourceTitle: first.SourceTitle, Sheet: first.Sheet})
	fieldLabel := func(item coverageEvidence) string {
		return terminalCoverageLabel(coverageEvidence{Subject: item.Subject, Metric: item.Metric})
	}
	counts := make(map[string]int)
	fieldDates := make(map[[5]string]int)
	for _, fact := range group {
		item := fact.Evidence
		counts[fieldLabel(item)]++
		fieldDates[[5]string{item.SeriesID, item.Column, item.Subject, item.Metric, item.Date}]++
	}
	var values []string
	unconfirmed := false
	multipleSameDay := false
	for _, fact := range group {
		item := fact.Evidence
		field := fieldLabel(item)
		multiple := item.Date != "" && fieldDates[[5]string{item.SeriesID, item.Column, item.Subject, item.Metric, item.Date}] > 1
		multipleSameDay = multipleSameDay || multiple
		if counts[field] > 1 && !fact.required && !multiple && item.Column != "" {
			field += " (" + d.text("列 ", "column ") + terminalEvidenceText(item.Column) + ")"
		}
		value := terminalReceiptValue(item)
		if fact.required || multiple {
			value = d.observedValue(item)
		} else if item.Kind != "derived" && !terminalUnitFromSource(item) {
			unconfirmed = true
		}
		if field != "" && value != "" {
			observation := field + " " + value
			if !slices.Contains(values, observation) {
				values = append(values, observation)
			}
		}
	}
	if len(values) == 0 {
		return ""
	}
	var units string
	if unconfirmed {
		units = d.text("未标单位的读数，其来源单位尚未确认。", " Source units for readings without a shown unit remain unconfirmed.")
	}
	if multipleSameDay {
		units += d.text("同一字段在该日有多条记录，尚未核定唯一值。", " The source has multiple records for the same field and date. A single value has not been established.")
	}
	if first.LatestInScope {
		return fmt.Sprintf(d.text("%s 最近核实的有效记录为 %s 的 %s。", "The latest verified readings for %s on %s are %s."), label, period, strings.Join(values, d.text("、", ", "))) + units
	}
	return fmt.Sprintf(d.text("%s 在 %s 的核实读数为 %s。", "%s reports the following readings for %s. %s."), label, period, strings.Join(values, d.text("、", ", "))) + units
}

func (d *terminalCoverageDelivery) addComparison(facts []coverageFact) {
	if len(facts) != 2 || facts[0].Role != "scoped_value" || facts[1].Role != "scoped_value" {
		return
	}
	a, b := facts[0].Evidence, facts[1].Evidence
	left, leftOK := coverageComparisonNumber(a)
	right, rightOK := coverageComparisonNumber(b)
	if !leftOK || !rightOK {
		return
	}
	comparable := terminalUnitFromSource(a) && terminalUnitFromSource(b) && a.Unit == b.Unit
	var note string
	if comparable {
		note = d.text("两处来源在核查范围内的数值不同。", "The two sources report different values for the checked scope.")
		if left.Cmp(right) == 0 {
			note = d.text("两处来源在核查范围内的数值相同。", "The two sources report the same value for the checked scope.")
		}
	} else {
		note = d.text("两处来源的单位无法直接比较。", "The sources' units cannot be compared directly.")
	}
	if !slices.Contains(d.conclusions, note) {
		d.conclusions = append(d.conclusions, note)
	}
}

func (d *terminalCoverageDelivery) addMissing(target taskCoverageTarget, role string) {
	from, to, _ := coverageTargetDateRange(target)
	field := terminalEvidenceText(firstNonEmptyAgent(target.Metric, target.Table))
	scope := terminalEvidenceText(terminalCoveragePeriod(from, to, sourceNavigationString(target.Period, "scope")))
	subject := strings.TrimSpace(field + " " + scope)
	if subject == "" {
		return
	}
	var note string
	switch role {
	case "scoped_value", "requested_periods":
		note = fmt.Sprintf(d.text("尚缺 %s 对应的读数。", "A verified reading for %s is still needed."), subject)
	case "attributed_supplement":
		note = fmt.Sprintf(d.text("尚缺能对应 %s 的独立来源读数。", "A reading for %s from an independent source is still needed."), subject)
	}
	if note != "" && !slices.Contains(d.missing, note) {
		d.missing = append(d.missing, note)
	}
}

func (d terminalCoverageDelivery) failureNote(failure terminalFailure) string {
	var stage string
	switch failure {
	case terminalFieldInput:
		return d.text("字段确认提交未通过格式或必填项校验，未发送确认请求。", "The field confirmation did not pass the format or required-field checks. No confirmation request was sent.")
	case terminalFieldService:
		return d.text("未能找到与本次提交对应的可用字段确认服务，未发送确认请求。", "No available field confirmation service matched the submission. No confirmation request was sent.")
	case terminalFieldBlocked:
		return d.text("字段确认请求在执行前被拦下，字段选择尚未提交成功。", "The field confirmation stopped before execution. The field selection is still unconfirmed.")
	case terminalFieldResult:
		return d.text("字段确认请求未返回成功确认，尚未取得已确认的字段结果。", "The field confirmation returned no successful confirmation or verified field result.")
	case terminalSourceChanged:
		stage = d.text("来源授权或快照在完成前发生变化。", "Source access or the source snapshot changed before completion.")
	case terminalReview, terminalNumeric:
		stage = d.text("答复在核验阶段未完成。", "Verification of the draft did not complete.")
	case terminalBudget, terminalTruncated:
		stage = d.text("生成完整答复前达到了运行上限。", "The run reached its limit before completing the answer.")
	case terminalSourceStalled:
		stage = d.text("重复的来源请求已停止。", "Repeated source requests stopped.")
	case terminalStalled:
		stage = d.text("本轮在完成答复前已停止。", "The run stopped before completing the answer.")
	case terminalIncomplete:
		stage = d.text("已生成的草稿未覆盖所问内容。", "The generated draft did not address the full request.")
	default:
		stage = d.text("本轮未生成完整答复。", "This turn produced no complete draft.")
	}
	if d.subject == "" {
		return stage
	}
	return fmt.Sprintf(d.text("关于“%s”，%s", "For “%s”, %s"), d.subject, stage)
}

func terminalCoverageFactKey(item coverageEvidence) string {
	return strings.Join([]string{item.KnowledgeBaseID, item.SourceID, item.SnapshotHash, item.Sheet,
		item.SeriesID, item.Column, item.PeriodFrom, item.PeriodTo, item.Period, item.Date, item.Cell,
		item.Metric, item.ChunkID, item.JSONPointer, item.EvidenceID}, "\x00")
}

func (d terminalCoverageDelivery) nearestValue(gap, value coverageEvidence) bool {
	return sameCoverageLastValid(d.record, gap, value) || coverageAnalysisFollowUpMatches(d.record, gap, value)
}

func (d terminalCoverageDelivery) gapFinding(gap coverageEvidence, used map[string]bool) string {
	label := terminalCoverageLabel(gap)
	period := terminalCoveragePeriod(gap.PeriodFrom, gap.PeriodTo, gap.Period)
	line := fmt.Sprintf(d.text("%s 在 %s 无有效记录。", "%s has no valid record in %s."), label, terminalEvidenceText(period))
	used[terminalCoverageFactKey(gap)] = true
	for _, fact := range d.facts {
		item := fact.Evidence
		if value := d.observedValue(item); value != "" && d.nearestValue(gap, item) {
			used[terminalCoverageFactKey(item)] = true
			for _, other := range d.facts {
				cell := other.Evidence
				if cell.SourceID == item.SourceID && cell.SeriesID == item.SeriesID && cell.SnapshotHash == item.SnapshotHash &&
					cell.Date == item.Date && cell.Cell == item.Cell {
					used[terminalCoverageFactKey(cell)] = true
				}
			}
			if d.chinese {
				return line + fmt.Sprintf("同字段最近有效记录为 %s 的 %s。", item.Date, value)
			}
			return line + fmt.Sprintf(" The nearest verified value for the same field is %s on %s.", value, item.Date)
		}
	}
	if date := gap.LastValidDate; coverageDay(date) != "" {
		line += fmt.Sprintf(d.text("来源标明最后有效日为 %s，尚缺该日原值。", " The source identifies %s as the last valid date. The value for that date is still needed."), date)
	}
	return line
}

var terminalDocumentComment = regexp.MustCompile(`(?s)<!--.*?-->`)

func (d terminalCoverageDelivery) receiptFinding(item coverageEvidence) string {
	label := terminalCoverageLabel(item)
	if label == "" {
		return ""
	}
	period := terminalEvidenceText(firstNonEmptyAgent(item.Date, terminalCoveragePeriod(item.PeriodFrom, item.PeriodTo, item.Period)))
	if item.Kind == "body" {
		location := label
		if item.Page > 0 {
			location += fmt.Sprintf(d.text("第 %d 页", " page %d"), item.Page)
		}
		if item.SourceDate != "" && item.SourceDate != item.Date {
			location += fmt.Sprintf(d.text("（材料日期 %s）", " (source date %s)"), terminalEvidenceText(item.SourceDate))
		}
		if item.ReportedValue != "" && period != "" {
			if d.chinese {
				return fmt.Sprintf("%s披露的 %s 数值为 %s。", location, period, d.observedValue(item))
			}
			return fmt.Sprintf("%s reports %s for %s.", location, d.observedValue(item), period)
		}
		if text, ok := item.Raw.(string); ok && strings.TrimSpace(text) != "" {
			excerpt := terminalEvidenceText(taskPlanPrefix(strings.TrimSpace(terminalDocumentComment.ReplaceAllString(text, "")), 200))
			if excerpt == "" {
				return ""
			}
			return fmt.Sprintf(d.text("%s返回的正文片段为“%s”。尚缺与所问范围对应的读数。", "%s returned this passage: “%s”. A reading matched to the requested scope is still needed."), location, excerpt)
		}
		return ""
	}
	value := d.observedValue(item)
	if value == "" || period == "" {
		return ""
	}
	if item.LatestInScope || item.Kind == "last_valid" {
		if d.chinese {
			return fmt.Sprintf("%s 最近核实的有效记录为 %s 的 %s。", label, period, value)
		}
		return fmt.Sprintf("The latest verified value for %s is %s on %s.", label, value, period)
	}
	if d.chinese {
		return fmt.Sprintf("%s 在 %s 的核实结果为 %s。", label, period, value)
	}
	return fmt.Sprintf("%s reports %s for %s.", label, value, period)
}

func terminalReceiptValue(item coverageEvidence) string {
	value := terminalEvidenceText(item.ReportedValue)
	switch raw := item.Raw.(type) {
	case json.Number:
		value = raw.String()
	case string:
		if value == "" && item.Kind != "body" {
			value = terminalEvidenceText(taskPlanPrefix(raw, 100))
		}
	}
	if value == "" {
		return ""
	}
	if terminalUnitFromSource(item) {
		value += " " + terminalEvidenceText(item.Unit)
	}
	return value
}

func (d terminalCoverageDelivery) observedValue(item coverageEvidence) string {
	value := terminalReceiptValue(item)
	if value == "" {
		return ""
	}
	var location []string
	if item.DateCell != "" {
		location = append(location, d.text("日期格 ", "date cell ")+terminalEvidenceText(item.DateCell))
	}
	if item.Cell != "" {
		location = append(location, d.text("取值格 ", "value cell ")+terminalEvidenceText(item.Cell))
	}
	if !terminalUnitFromSource(item) && item.Kind != "derived" && item.Kind != "body" {
		location = append(location, d.text("单位未确认", "unit unconfirmed"))
	}
	if len(location) != 0 {
		value += " (" + strings.Join(location, ", ") + ")"
	}
	return value
}

func (d terminalCoverageDelivery) directoryNote(item coverageEvidence, attempts []coverageAttempt) string {
	label := terminalCoverageLabel(item)
	if label == "" {
		return ""
	}
	date := firstNonEmptyAgent(item.LastValidDate, item.LastObservedDate)
	for _, attempt := range attempts {
		if item.ToolCallID != attempt.ToolCallID || item.DatasetID == "" || item.Operation != "list_datasets" {
			continue
		}
		var fieldNotes []string
		for _, field := range sourceNavigationRows(attempt.result.Data["literal_fields"]) {
			if field["dataset_id"] != item.DatasetID {
				continue
			}
			observed := item
			observed.Sheet, observed.Metric = sourceNavigationString(field, "sheet"), sourceNavigationString(field, "metric")
			last := sourceNavigationString(field, "last_date")
			if observed.Metric != "" && coverageDay(last) != "" {
				fieldNotes = append(fieldNotes, terminalCoverageLabel(observed)+" "+last)
			}
		}
		if len(fieldNotes) != 0 {
			slices.Sort(fieldNotes)
			return fmt.Sprintf(d.text("来源目录列出字段和截至日期 %s，尚未取得对应原值。", "The source directory lists the field and last date %s. The corresponding value is still needed."), strings.Join(slices.Compact(fieldNotes), d.text("、", ", ")))
		}
	}
	if item.Sheet == "" {
		if sheets, ok := coverageObject(item.Raw)["sheets"].([]any); ok && len(sheets) == 1 {
			if sheet, ok := sheets[0].(string); ok {
				label += " / " + terminalEvidenceText(sheet)
			}
		}
	}
	if coverageDay(date) != "" {
		return fmt.Sprintf(d.text("%s 的字段目录记录最后有效日为 %s，尚未核对该日原值。", "The field directory for %s lists %s as the last valid date. The value for that date remains unchecked."), label, date)
	}
	return fmt.Sprintf(d.text("已定位 %s 的目录条目，尚无所问范围的读数。", "The directory lists %s. A reading for the requested scope is still needed."), label)
}

func (d terminalCoverageDelivery) attemptNote(attempt coverageAttempt) string {
	var operation string
	switch attempt.Operation {
	case agenttools.ToolSearchKnowledge:
		operation = d.text("文档检索", "Document search")
	case agenttools.ToolReadDocument:
		operation = d.text("文档实读", "Document reading")
	case agenttools.ToolListDocuments, "list_datasets", "list_series":
		operation = d.text("来源目录查询", "Source listing")
	case "query_series", "analyze_table", "inspect_source", "query_pdf_table":
		operation = d.text("数据查询", "Data query")
	default:
		return ""
	}
	if attempt.result.PreflightRejected || attempt.result.Data["execution_started"] == false {
		return fmt.Sprintf(d.text("%s在执行前停止，未读取来源内容。", "%s stopped before execution. No source content was read."), operation)
	}
	if !attempt.Success {
		return fmt.Sprintf(d.text("%s尝试未返回可核实回执。", "%s was attempted without a verifiable result."), operation)
	}
	for _, item := range d.record.Evidence {
		if item.ToolCallID != attempt.ToolCallID || item.Kind != "cell" && item.Kind != "derived" {
			continue
		}
		label := terminalCoverageLabel(item)
		scope := terminalEvidenceText(terminalCoveragePeriod(item.PeriodFrom, item.PeriodTo, item.Period))
		if label != "" && scope != "" {
			return fmt.Sprintf(d.text("已读取 %s 的 %s 范围，尚未核实它与所问结果的对应关系。", "The read covered %s for %s. Its relationship to the requested result remains unverified."), label, scope)
		}
	}
	if count := attempt.result.Data["count"]; count != nil && coverageZero(count) {
		return fmt.Sprintf(d.text("%s已执行，返回 0 条结果。", "%s completed with 0 results."), operation)
	}
	return fmt.Sprintf(d.text("%s已执行，当前范围内没有可用于所问结果的已核实记录。", "%s ran. No verified record for the requested result is available within the current scope."), operation)
}
