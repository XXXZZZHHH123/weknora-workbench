package agent

import (
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
)

// Observations contain runtime provenance only. Bindings separately express a
// model's association of an actual field, page or span with a task goal.
type coverageEvidence struct {
	Kind, EvidenceID, ToolCallID, Operation, SourceDomain, SourceID, KnowledgeBaseID string
	KnowledgeID, DatasetID                                                           string
	Revision, AuthScope, SnapshotHash, ContentHash, SourceTitle                      string
	Sheet, SeriesID, Subject, Metric, Grain, DateMode, Unit, UnitBasis               string
	RowLabel, Column, Cell, ChunkID, Object, Period, PeriodFrom, PeriodTo            string
	Date, DateCell, CitationID, JSONPointer, Status, LastValidDate                   string
	LastObservedDate, LastValidRef, DateEvidenceID, DateJSONPointer                  string
	SourceDate, ReportedValue                                                        string
	ResultJSONPointer, CellJSONPointer, WorkbookSnapshotHash                         string
	Page                                                                             int
	SpanStart, SpanEnd, NextOffset                                                   *int
	Raw                                                                              any
	Complete, Bound, RangeComplete, LatestInScope, WorkbookBound                     bool
}

type coverageAttempt struct {
	ToolCallID, Operation, EvidenceID string
	Success                           bool
	Args                              map[string]any
	result                            *types.ToolResult
}

type coverageEvidenceBatch struct {
	Evidence []coverageEvidence
	Attempts []coverageAttempt
}

type coverageBinding struct {
	GoalIndex, Page                                                   int
	Role, Kind, SourceID, KnowledgeBaseID, SnapshotHash               string
	Sheet, SeriesID, Column, ChunkID, Object                          string
	EvidenceID, JSONPointer, CitationID, Subject, Metric, TargetField string
	Period, PeriodFrom, PeriodTo, Grain, GrainBasis, Date             string
	Unit, Span, RawClaim                                              string
	RequireUnit, Provisional                                          bool
}

type coverageRecord struct {
	Revision, AuthScope, StopReason string
	Evidence                        []coverageEvidence
	Bindings                        []coverageBinding
	Attempts                        []coverageRecoveryReceipt
	Snapshots                       map[string]string
}

type coverageRecoveryReceipt struct {
	GapID      string `json:"gap_id"`
	Action     string `json:"action"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	EvidenceID string `json:"evidence_id,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Succeeded  bool   `json:"succeeded"`
}

type coverageFact struct {
	Role     string           `json:"role"`
	Evidence coverageEvidence `json:"evidence"`
}

type coverageCompletion struct {
	GoalIndex       int                      `json:"goal_index"`
	State           string                   `json:"state"`
	Reason          string                   `json:"reason"`
	GapID           string                   `json:"gap_id"`
	NextRead        string                   `json:"next_read,omitempty"`
	SatisfiedRefs   []string                 `json:"satisfied_refs,omitempty"`
	OpenItems       []string                 `json:"open_items,omitempty"`
	PinnedFacts     []coverageFact           `json:"pinned_facts,omitempty"`
	AttemptReceipt  *coverageRecoveryReceipt `json:"attempt_receipt,omitempty"`
	BudgetRemaining int                      `json:"budget_remaining"`
}

const (
	coverageOpen      = "open"
	coverageSatisfied = "satisfied"
	coverageExhausted = "exhausted"
)

// Each planned period has independent coverage. A selection can name an exact
// native location for a forthcoming read or point to previously read content.
func evaluateTaskCoverage(target taskCoverageTarget, input coverageRecord) []coverageCompletion {
	if target.StructuredTaskGoal == nil || target.SourceTime == "general_concept" ||
		(len(target.Required) == 1 && target.Required[0] == "optional") {
		return nil
	}
	from, to, grain := coverageTargetDateRange(target)
	type slot struct{ from, to, grain, period, field string }
	selections := map[slot][]coverageBinding{}
	order := []slot{}
	periods := map[[3]string]bool{}
	compare := target.VersionTask == "compare_versions" || target.VersionTask == "compare_sources"
	periodScope, _ := target.Period["scope"].(string)
	if from == "" && to == "" && grain == "day" {
		if _, err := time.Parse(time.DateOnly, periodScope); err == nil {
			from, to = periodScope, periodScope
		}
	}
	for _, binding := range input.Bindings {
		if binding.GoalIndex != target.GoalIndex || binding.Role != "primary" || binding.SourceID == "" ||
			!coverageEqual(from, binding.PeriodFrom) || !coverageEqual(to, binding.PeriodTo) || !coverageGrain(grain, binding.Grain) ||
			periodScope != "" && binding.PeriodFrom == "" && binding.PeriodTo == "" && binding.Period == "" {
			continue
		}
		if binding.Provisional && slices.ContainsFunc(input.Bindings, func(selected coverageBinding) bool {
			return coverageRefinesBinding(binding, selected)
		}) {
			continue
		}
		if binding.SnapshotHash == "" {
			// A field directory may omit the workbook hash. Keep the actual
			// parent snapshot pinned until a matching field read supplies it.
			for _, prior := range slices.Backward(input.Bindings) {
				if prior.Provisional && prior.SnapshotHash != "" && coverageRefinesBinding(prior, binding) {
					binding.SnapshotHash = prior.SnapshotHash
					break
				}
			}
		}
		field := binding.TargetField
		if !compare && (binding.SeriesID != "" || binding.Column != "") {
			field = reviewDigest([]string{binding.Sheet, binding.SeriesID, binding.Column})
		}
		key := slot{binding.PeriodFrom, binding.PeriodTo, binding.Grain, binding.Period, field}
		periods[[3]string{key.from, key.to, key.period}] = true
		if _, exists := selections[key]; !exists {
			order = append(order, key)
		}
		selections[key] = append(selections[key], binding)
	}
	if len(order) == 0 {
		order = append(order, slot{from: from, to: to, grain: grain})
	}
	result := make([]coverageCompletion, 0, len(order))
	for _, key := range order {
		result = append(result, resolveCoverageSlot(target, input, key.from, key.to, key.grain, key.period, key.field, selections[key]))
	}
	if count, ok := coveragePeriodCount(target.Period["count"]); ok && count > len(periods) {
		missing := resolveCoverageSlot(target, input, from, to, grain, "", "", nil)
		missing.OpenItems = []string{"requested_periods"}
		result = append(result, missing)
	}
	return result
}

func coverageRefinesBinding(prior, selected coverageBinding) bool {
	if prior.GoalIndex != selected.GoalIndex || prior.Role != selected.Role || prior.SourceID != selected.SourceID ||
		prior.KnowledgeBaseID != selected.KnowledgeBaseID || selected.SnapshotHash != "" && !coverageEqual(prior.SnapshotHash, selected.SnapshotHash) ||
		prior.PeriodFrom != selected.PeriodFrom || prior.PeriodTo != selected.PeriodTo || prior.Period != selected.Period || prior.Grain != selected.Grain ||
		!coverageEqual(prior.TargetField, selected.TargetField) || prior.Page != 0 && prior.Page != selected.Page {
		return false
	}
	from := [...]string{prior.Sheet, prior.SeriesID, prior.Column, prior.Object, prior.ChunkID}
	to := [...]string{selected.Sheet, selected.SeriesID, selected.Column, selected.Object, selected.ChunkID}
	moreSpecific := prior.Page != selected.Page
	for i, value := range from {
		if !coverageEqual(value, to[i]) {
			return false
		}
		moreSpecific = moreSpecific || value != to[i]
	}
	return moreSpecific
}

func coveragePeriod(period map[string]any) (from, to, grain string) {
	from, _ = period["from"].(string)
	to, _ = period["to"].(string)
	grain, _ = period["grain"].(string)
	return
}

func coveragePeriodCount(value any) (int, bool) {
	switch count := value.(type) {
	case int:
		return count, count > 0
	case float64:
		return int(count), count > 0 && float64(int(count)) == count
	default:
		return 0, false
	}
}

func resolveCoverageSlot(target taskCoverageTarget, input coverageRecord, from, to, grain, period, field string, primary []coverageBinding) coverageCompletion {
	key := []any{input.Revision, input.AuthScope, target.GoalIndex, target.Metric, target.Period, from, to, grain, period, field}
	result := coverageCompletion{GoalIndex: target.GoalIndex, State: coverageOpen, Reason: "primary_unverified", NextRead: "read",
		GapID:     reviewDigest(append(slices.Clone(key), "scoped_value")),
		OpenItems: []string{"scoped_value"}, BudgetRemaining: 1}
	var gaps []coverageEvidence
	supplied := map[[3]string]bool{}
	var missing, conflicting bool
	for _, binding := range primary {
		var hasValue, hasGap bool
		for _, evidence := range input.Evidence {
			fact, ok := matchCoverageEvidence(target, input, binding, evidence)
			if !ok && binding.Provisional && coverageSameSource(binding, evidence) && coverageLocation(binding, evidence) {
				for _, selected := range primary {
					if selected.Provisional {
						continue
					}
					fact, ok = matchCoverageEvidence(target, input, selected, evidence)
					if ok {
						break
					}
				}
			}
			if !ok {
				continue
			}
			switch fact.Kind {
			case "gap":
				gaps = append(gaps, fact)
				hasGap = true
			default:
				result.pin(coverageValueRole(fact.Kind), fact)
				if coverageCellCompletesPeriod(target, fact) {
					hasValue = true
					supplied[[3]string{fact.KnowledgeBaseID, fact.SourceID, fact.SnapshotHash}] = true
				}
			}
		}
		missing = missing || !hasValue && !hasGap
		conflicting = conflicting || hasValue && hasGap
	}
	compare := target.VersionTask == "compare_versions" || target.VersionTask == "compare_sources"
	if !missing && len(gaps) == 0 && len(supplied) > 0 && (!compare || len(supplied) > 1) {
		return result.satisfied("matching_primary")
	}
	for _, gap := range gaps {
		result.pin("coverage_boundary", gap)
		for _, observation := range input.Evidence {
			if sameCoverageLastValid(input, gap, observation) {
				date := observation
				date.Raw, date.Cell = observation.Date, observation.DateCell
				date.Unit, date.UnitBasis = "", ""
				if observation.DateEvidenceID != "" && observation.DateJSONPointer != "" {
					date.EvidenceID, date.JSONPointer = observation.DateEvidenceID, observation.DateJSONPointer
				}
				result.pin("last_valid_date", date)
				result.pin("last_valid_value", observation)
			}
		}
	}
	if conflicting {
		result.Reason = "conflicting_observations"
	}
	if missing || conflicting || compare || len(gaps) == 0 {
		return result.withPrimaryReceipt(input)
	}
	if target.SourceMode == "only" {
		return result.satisfied("only_verified_gap")
	}
	if target.SourceMode != "named_first" {
		return result.withPrimaryReceipt(input)
	}
	var fieldRole string
	if len(primary) > 0 {
		fieldRole = primary[0].TargetField
	}
	result.GapID = reviewDigest(append(key, "attributed_supplement"))
	return resolveCoverageSupplement(target, input, result, from, to, grain, period, fieldRole)
}

func coverageValueRole(kind string) string {
	switch kind {
	case "directory", "artifact":
		return kind
	default:
		return "scoped_value"
	}
}

func (result *coverageCompletion) pin(role string, evidence coverageEvidence) {
	ref := coverageFactRef(evidence)
	if slices.ContainsFunc(result.PinnedFacts, func(fact coverageFact) bool {
		prior := fact.Evidence
		return fact.Role == role && coverageFactRef(prior) == ref && prior.SourceID == evidence.SourceID &&
			prior.KnowledgeBaseID == evidence.KnowledgeBaseID && prior.Period == evidence.Period &&
			prior.PeriodFrom == evidence.PeriodFrom && prior.PeriodTo == evidence.PeriodTo && prior.Date == evidence.Date
	}) {
		return
	}
	result.PinnedFacts = append(result.PinnedFacts, coverageFact{Role: role, Evidence: evidence})
	if !slices.Contains(result.SatisfiedRefs, ref) {
		result.SatisfiedRefs = append(result.SatisfiedRefs, ref)
	}
}

func coverageFactRef(evidence coverageEvidence) string {
	ref := evidence.EvidenceID + "#" + evidence.JSONPointer
	if evidence.SpanStart != nil && evidence.SpanEnd != nil {
		ref += "@" + strconv.Itoa(*evidence.SpanStart) + ":" + strconv.Itoa(*evidence.SpanEnd)
	}
	return ref
}

func (result coverageCompletion) satisfied(reason string) coverageCompletion {
	result.State, result.Reason, result.NextRead = coverageSatisfied, reason, ""
	result.OpenItems, result.BudgetRemaining = nil, 0
	return result
}

func (result coverageCompletion) withStop(reason string) coverageCompletion {
	if reason != "" {
		result.State, result.Reason, result.NextRead = coverageExhausted, reason, ""
		result.BudgetRemaining = 0
	}
	return result
}

func (result coverageCompletion) withPrimaryReceipt(input coverageRecord) coverageCompletion {
	if receipt := coverageReceipt(input, result.GapID, "read"); receipt != nil {
		result.AttemptReceipt = receipt
		return result.withStop(coverageReceiptReason(receipt))
	}
	return result.withStop(input.StopReason)
}

func coverageReceipt(input coverageRecord, gapID, action string) *coverageRecoveryReceipt {
	for i, receipt := range input.Attempts {
		if receipt.GapID == gapID && receipt.Action == action && (receipt.ToolCallID != "" || receipt.Reason != "") {
			return &input.Attempts[i]
		}
	}
	return nil
}

func coverageReceiptReason(receipt *coverageRecoveryReceipt) string {
	if receipt != nil && receipt.Reason != "" {
		return receipt.Reason
	}
	return "no_matching_evidence"
}

func resolveCoverageSupplement(target taskCoverageTarget, input coverageRecord, result coverageCompletion, from, to, grain, period, field string) coverageCompletion {
	result.OpenItems, result.Reason = []string{"attributed_supplement"}, "verified_primary_gap"
	result.BudgetRemaining = 2
	discover := coverageReceipt(input, result.GapID, "discover")
	read := coverageReceipt(input, result.GapID, "read")
	for _, receipt := range []*coverageRecoveryReceipt{discover, read} {
		if receipt != nil {
			result.BudgetRemaining--
			result.AttemptReceipt = receipt
		}
	}
	var matched, navigable bool
	for _, binding := range input.Bindings {
		if binding.GoalIndex != target.GoalIndex || binding.Role != "supplement" || binding.SourceID == "" ||
			binding.TargetField != field || !coverageEqual(from, binding.PeriodFrom) || !coverageEqual(to, binding.PeriodTo) ||
			!coverageEqual(period, binding.Period) || !coverageGrain(grain, binding.Grain) {
			continue
		}
		for _, evidence := range input.Evidence {
			if !coverageSameSource(binding, evidence) || !coverageCurrentEvidence(input, evidence) {
				continue
			}
			if (evidence.Kind == "navigation" || evidence.Kind == "directory") && coverageLocation(binding, evidence) &&
				(binding.EvidenceID == "" || binding.EvidenceID == evidence.EvidenceID) && (binding.Page != 0 || binding.ChunkID != "" || binding.SeriesID != "" || binding.Column != "") {
				navigable = true
			}
			fact, ok := matchCoverageEvidence(target, input, binding, evidence)
			if ok && (fact.Kind == "body" || fact.Kind == "cell" || fact.Kind == "derived") {
				matched = matched || coverageCellCompletesPeriod(target, fact)
				result.pin("supplement", fact)
			}
		}
	}
	if matched {
		return result.satisfied("matching_supplement")
	}
	if input.StopReason != "" {
		return result.withStop(input.StopReason)
	}
	navigable = navigable || discover != nil && slices.ContainsFunc(input.Evidence, func(observation coverageEvidence) bool {
		return coverageSupplementNavigation(input, discover.ToolCallID, observation)
	})
	if read != nil || discover != nil && !navigable {
		return result.withStop(coverageReceiptReason(result.AttemptReceipt))
	}
	if navigable {
		result.NextRead, result.BudgetRemaining = "read", 1
	} else {
		result.NextRead = "discover"
	}
	return result
}

func coverageSupplementNavigation(record coverageRecord, callID string, observation coverageEvidence) bool {
	return callID != "" && observation.ToolCallID == callID && observation.SourceDomain == "knowledge" &&
		(observation.Kind == "navigation" || observation.Kind == "body") && observation.ChunkID != "" &&
		coverageCurrentEvidence(record, observation)
}

func coverageCellCompletesPeriod(target taskCoverageTarget, fact coverageEvidence) bool {
	// Samples remain valid individual facts. Only a complete native range
	// receipt or an explicitly requested latest value can close a longer span.
	return fact.Kind != "cell" || fact.PeriodFrom == "" || fact.PeriodTo == "" || fact.PeriodFrom == fact.PeriodTo ||
		fact.RangeComplete || target.SourceTime == "latest_observation" && fact.LatestInScope
}

func coverageEqual(expected, actual string) bool { return expected == "" || expected == actual }

func coverageGrain(expected, actual string) bool {
	return expected == "none" || coverageEqual(expected, actual)
}

func coverageCurrentEvidence(input coverageRecord, evidence coverageEvidence) bool {
	if evidence.SourceID == "" || evidence.EvidenceID == "" || evidence.JSONPointer == "" ||
		evidence.Revision != input.Revision || evidence.AuthScope != input.AuthScope {
		return false
	}
	sourceKey := evidence.KnowledgeBaseID + "\x00" + evidence.SourceID
	current := input.Snapshots[coverageSnapshotKey(evidence)]
	if current == "" {
		current = input.Snapshots[sourceKey]
	}
	if current == "" {
		current = input.Snapshots[evidence.SourceID]
	}
	return (current == "" || current == evidence.SnapshotHash) &&
		(!(coverageAnalysisEndpoint(evidence) || coverageAnalysisMissingCell(evidence)) || coverageCurrentAnalysisWorkbook(input, evidence))
}

func coverageSameSource(binding coverageBinding, evidence coverageEvidence) bool {
	return binding.SourceID == evidence.SourceID && coverageEqual(binding.KnowledgeBaseID, evidence.KnowledgeBaseID) &&
		coverageEqual(binding.SnapshotHash, evidence.SnapshotHash)
}

func coverageLocation(binding coverageBinding, evidence coverageEvidence) bool {
	return coverageEqual(binding.Sheet, evidence.Sheet) && coverageEqual(binding.SeriesID, evidence.SeriesID) &&
		coverageEqual(binding.Column, evidence.Column) && coverageEqual(binding.ChunkID, evidence.ChunkID) &&
		coverageEqual(binding.Object, evidence.Object) && (binding.Page == 0 || binding.Page == evidence.Page)
}

func matchCoverageEvidence(target taskCoverageTarget, input coverageRecord, binding coverageBinding, evidence coverageEvidence) (coverageEvidence, bool) {
	if !evidence.Bound || !coverageCurrentEvidence(input, evidence) || !coverageSameSource(binding, evidence) || !coverageLocation(binding, evidence) ||
		!coverageEqual(binding.EvidenceID, evidence.EvidenceID) || !coverageEqual(binding.JSONPointer, evidence.JSONPointer) ||
		!coverageEqual(binding.CitationID, evidence.CitationID) {
		return evidence, false
	}
	kind, _ := target.Delivery["kind"].(string)
	if (kind == "chart" || kind == "line_chart" || kind == "bar_chart" || kind == "image" || kind == "file") && evidence.Kind != "artifact" {
		return evidence, false
	}
	if binding.Kind != "" && binding.Kind != evidence.Kind && evidence.Kind != "gap" {
		return evidence, false
	}
	switch evidence.Kind {
	case "directory":
		return evidence, evidence.Complete && target.SourceTime == "catalog_snapshot"
	case "artifact":
		return evidence, evidence.Complete && evidence.Raw != nil && binding.EvidenceID != ""
	case "body":
		return coverageBodyFact(binding, evidence, slices.Contains(target.Required, "scoped_value"))
	case "cell", "last_valid", "derived", "gap":
		if evidence.SnapshotHash == "" || binding.SeriesID == "" && (binding.Column == "" || binding.Sheet == "") &&
			(binding.Page == 0 || binding.Object == "" || binding.Column == "") ||
			!coverageGrain(binding.Grain, evidence.Grain) || !coverageEqual(binding.PeriodFrom, evidence.PeriodFrom) ||
			!coverageEqual(binding.PeriodTo, evidence.PeriodTo) || !coverageEqual(binding.Period, evidence.Period) || !coverageEqual(binding.Date, evidence.Date) ||
			!coverageEqual(binding.Metric, evidence.Metric) || !coverageEqual(binding.Subject, evidence.Subject) {
			return evidence, false
		}
		if evidence.Kind == "gap" {
			return evidence, evidence.Complete && evidence.NextOffset == nil && (binding.Period != "" || binding.PeriodFrom != "" && binding.PeriodTo != "")
		}
		if target.SourceTime == "latest_observation" && (evidence.Kind == "cell" || evidence.Kind == "last_valid") {
			unbounded := binding.Period == "" && binding.PeriodFrom == "" && binding.PeriodTo == ""
			if !evidence.LatestInScope && !(unbounded && evidence.Date != "" && evidence.Date == evidence.LastValidDate) {
				return evidence, false
			}
		} else if evidence.Kind == "last_valid" {
			return evidence, false
		}
		if evidence.Kind == "derived" && target.Delivery["aggregation"] == "as_reported" {
			return evidence, false
		}
		if evidence.Kind == "cell" && binding.Grain == "day" && binding.PeriodFrom == binding.PeriodTo && binding.PeriodFrom != "" && evidence.Date != binding.PeriodFrom {
			return evidence, false
		}
		if binding.RequireUnit && (evidence.Unit == "" || evidence.UnitBasis == "") || !coverageEqual(binding.Unit, evidence.Unit) || evidence.Raw == nil {
			return evidence, false
		}
		return evidence, (evidence.Kind == "cell" || evidence.Kind == "last_valid") && (evidence.Cell != "" || evidence.Page != 0 && evidence.Object != "" && evidence.RowLabel != "" && evidence.Column != "") ||
			evidence.Kind == "derived" && evidence.Complete && binding.Kind == "derived"
	default:
		return evidence, false
	}
}

func coverageBodyFact(binding coverageBinding, evidence coverageEvidence, requireValue bool) (coverageEvidence, bool) {
	body, ok := evidence.Raw.(string)
	claim := strings.TrimSpace(binding.RawClaim)
	if !ok || !evidence.Complete || evidence.ChunkID == "" || evidence.SnapshotHash == "" || evidence.ContentHash != candidateTextDigest(body) || binding.EvidenceID == "" ||
		binding.Span == "" || binding.RequireUnit && binding.Unit == "" || (requireValue || binding.RequireUnit) && claim == "" ||
		binding.Subject != "" && !coverageEqual(evidence.Subject, binding.Subject) || binding.Metric != "" && !coverageEqual(evidence.Metric, binding.Metric) ||
		evidence.Grain != "" && !coverageGrain(binding.Grain, evidence.Grain) ||
		evidence.Grain == "" && binding.Grain != "" && binding.Grain != "none" && binding.Grain != "day" && binding.GrainBasis == "" {
		return evidence, false
	}
	start := strings.Index(body, binding.Span)
	if start < 0 || strings.Contains(body[start+1:], binding.Span) ||
		claim != "" && !coverageClaimInBody(body, start, binding.Span, claim, binding.Unit) || !strings.Contains(binding.Span, binding.Date) ||
		!strings.Contains(binding.Span, binding.Unit) || !strings.Contains(binding.Span, binding.Subject) ||
		!strings.Contains(binding.Span, binding.Metric) || !strings.Contains(binding.Span, binding.GrainBasis) {
		return evidence, false
	}
	if (binding.PeriodFrom != "" || binding.PeriodTo != "") && binding.Date == "" && evidence.Period == "" ||
		binding.Period != "" && binding.Period != evidence.Period && !strings.Contains(binding.Span, binding.Period) {
		return evidence, false
	}
	if !coverageBodyRange(binding, evidence) {
		return evidence, false
	}
	start = utf8.RuneCountInString(body[:start])
	if evidence.SpanStart != nil {
		start += *evidence.SpanStart
	}
	end := start + utf8.RuneCountInString(binding.Span)
	evidence.SpanStart, evidence.SpanEnd, evidence.Raw = &start, &end, binding.Span
	evidence.ReportedValue = claim
	evidence.Grain, evidence.PeriodFrom, evidence.PeriodTo = binding.Grain, binding.PeriodFrom, binding.PeriodTo
	if binding.Subject != "" {
		evidence.Subject = binding.Subject
	}
	if binding.Metric != "" {
		evidence.Metric = binding.Metric
	}
	if binding.Period != "" {
		evidence.Period = binding.Period
	}
	if binding.Date != "" {
		evidence.Date = binding.Date
	}
	if binding.Unit != "" {
		evidence.Unit, evidence.UnitBasis = binding.Unit, evidence.EvidenceID+"#"+evidence.JSONPointer
	}
	return evidence, true
}

// A declared literal may not start or end inside an original number or date.
// Compare with the complete body because the selected span itself can be clipped.
func coverageClaimInBody(body string, start int, span, claim, unit string) bool {
	boundaries := proseNumber.FindAllStringIndex(body, -1)
	for _, date := range calendarDateLiteral.FindAllStringIndex(body, -1) {
		if standaloneDateToken(body, date[0], date[1]) && validCalendarLiteral(body[date[0]:date[1]]) {
			boundaries = append(boundaries, date)
		}
	}
	for relative := strings.Index(span, claim); relative >= 0; {
		from, to := start+relative, start+relative+len(claim)
		cutsLiteral := slices.ContainsFunc(boundaries, func(token []int) bool {
			return token[0] < from && from < token[1] || token[0] < to && to < token[1]
		})
		if !cutsLiteral && coverageClaimWordBoundaries(body, from, to, unit) {
			return true
		}
		next := strings.Index(span[relative+1:], claim)
		if next < 0 {
			break
		}
		relative += next + 1
	}
	return false
}

func coverageClaimWordBoundaries(body string, start, end int, unit string) bool {
	previous, _ := utf8.DecodeLastRuneInString(body[:start])
	first, _ := utf8.DecodeRuneInString(body[start:])
	last, _ := utf8.DecodeLastRuneInString(body[:end])
	next, _ := utf8.DecodeRuneInString(body[end:])
	word := func(r rune) bool { return unicode.IsDigit(r) || r < utf8.RuneSelf && numericASCIIWord(byte(r)) }
	if word(first) && word(previous) || unicode.IsDigit(first) && previous == '.' {
		return false
	}
	return !(word(last) && word(next)) || unicode.IsDigit(last) && !unicode.IsDigit(next) && unit != "" && strings.HasPrefix(body[end:], unit)
}

func coverageBodyRange(binding coverageBinding, evidence coverageEvidence) bool {
	from, to := binding.PeriodFrom, binding.PeriodTo
	if from == "" || to == "" {
		return true
	}
	if from == to {
		return binding.Grain != "day" || evidence.Period == from || coverageDay(binding.Date) == from
	}
	if evidence.PeriodFrom == from && evidence.PeriodTo == to {
		return true
	}
	// A reported aggregate can have a single value for a documented range.
	// Two dates alone never establish complete coverage of daily observations.
	dates := calendarDateLiteral.FindAllString(binding.Date, -1)
	return binding.Grain != "day" && binding.Grain != "none" && binding.Grain != "" && len(dates) == 2 &&
		coverageDay(dates[0]) == from && coverageDay(dates[1]) == to
}

func coverageDay(literal string) string {
	for _, layout := range []string{time.DateOnly, "2006年1月2日", "2006/1/2", "2006.1.2"} {
		if date, err := time.Parse(layout, literal); err == nil {
			return date.Format(time.DateOnly)
		}
	}
	return ""
}

func sameCoverageLastValid(input coverageRecord, gap, value coverageEvidence) bool {
	analysisEndpoint := coverageAnalysisEndpoint(value)
	if value.Kind != "last_valid" || !value.Bound || !coverageCurrentEvidence(input, value) || value.Raw == nil || value.Date == "" ||
		!analysisEndpoint && value.DateCell == "" || value.Cell == "" {
		return false
	}
	if analysisEndpoint && ((gap.Operation != "analyze_table" && gap.Operation != "query_series") ||
		gap.LastValidRef != "" && gap.LastValidRef != value.EvidenceID || value.Date >= gap.PeriodFrom) {
		return false
	}
	if value.SourceID != gap.SourceID || value.KnowledgeBaseID != gap.KnowledgeBaseID ||
		value.Sheet != gap.Sheet || gap.SeriesID == "" || value.SeriesID != gap.SeriesID || !coverageEqual(gap.Column, value.Column) {
		return false
	}
	if value.SnapshotHash != gap.SnapshotHash {
		if analysisEndpoint && gap.Operation == "query_series" {
			return gap.Column == value.Column && gap.SnapshotHash == value.WorkbookSnapshotHash && gap.LastValidDate == value.Date
		}
		return gap.WorkbookSnapshotHash != "" && coverageCurrentAnalysisWorkbook(input, gap) &&
			coverageAnalysisFollowUpMatches(input, gap, value)
	}
	if gap.LastValidDate != "" {
		return gap.LastValidDate == value.Date
	}
	return value.EvidenceID == gap.EvidenceID || gap.LastValidRef != "" && value.EvidenceID == gap.LastValidRef
}

func coverageAnalysisFollowUpMatches(record coverageRecord, gap, value coverageEvidence) bool {
	if gap.Operation != "analyze_table" || value.Operation != "query_series" || value.Kind != "last_valid" ||
		value.DateCell == "" || value.Cell == "" || value.Raw == nil || gap.LastValidDate == "" || value.Date != gap.LastValidDate ||
		gap.SourceID != value.SourceID || gap.KnowledgeBaseID != value.KnowledgeBaseID || gap.Sheet != value.Sheet ||
		gap.SeriesID != value.SeriesID || gap.Column != value.Column {
		return false
	}
	// The analysis and original observation have separate snapshot identities.
	return slices.ContainsFunc(record.Evidence, func(item coverageEvidence) bool {
		return item.Kind == "analysis_input" && item.EvidenceID == gap.EvidenceID && item.SeriesID == gap.SeriesID &&
			item.SnapshotHash == gap.SnapshotHash && item.Raw == value.SnapshotHash
	})
}
