package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
)

const terminalCoverageContextLimit = 5

type terminalCoverageFact struct {
	coverageFact
	requestedUnit string
	required      bool
}

type terminalCoverageDelivery struct {
	facts            []terminalCoverageFact
	context          []string
	goal, limitation string
	comparisonLead   string
	documentBodies   []coverageEvidence
	presentationUnit string
	chinese          bool
	explicitOnly     bool
	inspected        bool
}

// Reproject native executions before delivery. A result changed after its
// initial registration or a source outside the current scope cannot be used.
func (e *AgentEngine) terminalCoverageDelivery(ctx context.Context, state *types.AgentState, failure bool) terminalCoverageDelivery {
	var delivery terminalCoverageDelivery
	if state == nil || e.holdCollaborationAnswer {
		return delivery
	}
	if failure {
		if !e.workbenchEvidenceTask() || e.collaboration != nil || e.needsDeliveryReview(state) || hasWorkspaceExecution(state) {
			return delivery
		}
	} else if !e.evidenceCompletionEligible() {
		return delivery
	}
	hadCoverage := e.hasEvidenceCompletionContext()
	e.refreshEvidenceCompletion(ctx, e.turnReview.Query, state, e.turnReview.Conversation)
	turn := &e.evidenceCompletion
	record := coverageRecord{Revision: turn.revision, AuthScope: turn.scope,
		Bindings: turn.record.Bindings, Attempts: turn.record.Attempts, StopReason: turn.stopReason}
	if !e.evidenceCompletionEligible() {
		record.Revision = e.candidateTaskDigest(e.turnReview.Query, e.turnReview.Conversation)
		record.AuthScope = e.unverifiedSourceScopeDigest(ctx)
	}
	var attempts []coverageAttempt
	if e.toolRegistry != nil {
		record, attempts = e.projectCurrentTaskCoverage(state, turn.startStep, record)
	}
	catalogs := make(map[string]map[string]any)
	linkedReads := make(map[string]bool)
	for _, attempt := range attempts {
		if question := sourceNavigationString(attempt.Args, "source_question"); question != "" && question == e.turnReview.Query {
			linkedReads[attempt.EvidenceID] = true
		}
		if attempt.Success && attempt.Operation == "list_datasets" {
			catalogs[attempt.EvidenceID] = attempt.result.Data
		}
	}
	targets := slices.DeleteFunc(slices.Clone(e.evidenceCompletionTargets()), func(target taskCoverageTarget) bool {
		return target.SourceTime == "general_concept" || slices.Equal(target.Required, []string{"optional"})
	})
	hasSourceContext := len(attempts) > 0 || hadCoverage || len(targets) > 0 || e.terminalCoverageContextKnown()
	delivery.inspected = hasSourceContext || failure
	if !delivery.inspected {
		return delivery
	}
	delivery.chinese = strings.HasPrefix(types.LanguageFromContextOrDefault(ctx), "zh")
	delivery.explicitOnly = slices.ContainsFunc(targets, func(target taskCoverageTarget) bool { return target.SourceMode == "only" })

	record.Evidence = slices.DeleteFunc(record.Evidence, func(item coverageEvidence) bool {
		return !item.Bound || !coverageCurrentEvidence(record, item) || !e.terminalCoverageScopeAllowed(targets, item)
	})
	// Dimensioned source relationships are reconstructed against the fresh
	// record. A declared binding or previously pinned fact is never proof.
	record.Evidence = append(record.Evidence, terminalInspectedChoiceFacts(record, turn.record.Evidence)...)
	var supplementExpected bool
	for _, target := range targets {
		record.Evidence = append(record.Evidence, nativeCoverageGapFacts(target, record)...)
	}
	if len(targets) == 0 {
		record.Evidence = append(record.Evidence, nativeCoverageGapFacts(taskCoverageTarget{StructuredTaskGoal: &StructuredTaskGoal{}}, record)...)
	}
	exactLookup := slices.ContainsFunc(record.Evidence, func(item coverageEvidence) bool {
		return item.Kind == "gap" && item.Complete && item.PeriodFrom != "" && item.PeriodFrom == item.PeriodTo
	})

	add := func(role string, item coverageEvidence, unit string, required bool) {
		switch item.Kind {
		case "cell", "last_valid", "derived", "gap", "body":
		default:
			return
		}
		if unit != "" && terminalUnitUnspecified(item) && item.SeriesID != "" {
			for _, peer := range record.Evidence {
				if peer.SourceID != item.SourceID || peer.KnowledgeBaseID != item.KnowledgeBaseID || peer.SeriesID != item.SeriesID ||
					peer.Sheet != item.Sheet || peer.SnapshotHash != item.SnapshotHash || terminalUnitUnspecified(peer) {
					continue
				}
				if !terminalUnitFromSource(peer) || item.Unit != "" && item.Unit != peer.Unit {
					item.Unit, item.UnitBasis = "", "mixed"
					break
				}
				item.Unit, item.UnitBasis = peer.Unit, peer.UnitBasis
			}
		}
		if slices.ContainsFunc(delivery.facts, func(prior terminalCoverageFact) bool {
			return coverageFactRef(prior.Evidence) == coverageFactRef(item) && prior.Evidence.Kind == item.Kind &&
				prior.Evidence.PeriodFrom == item.PeriodFrom && prior.Evidence.PeriodTo == item.PeriodTo && prior.Evidence.Period == item.Period
		}) {
			return
		}
		delivery.facts = append(delivery.facts, terminalCoverageFact{coverageFact{role, item}, unit, required})
	}
	for _, target := range targets {
		if delivery.goal == "" && target.Metric != "" {
			from, to, _ := coveragePeriod(target.Period)
			delivery.goal = terminalEvidenceText(strings.TrimSpace(target.Metric + " " + terminalCoveragePeriod(from, to, sourceNavigationString(target.Period, "scope"))))
		}
		unit := e.terminalRequestedUnit(target)
		comparing := target.VersionTask == "compare_sources" || target.VersionTask == "compare_versions"
		if comparing && delivery.comparisonLead == "" {
			delivery.comparisonLead = delivery.text("所求的来源比较仍待核实。", "The requested source comparison remains unverified.")
		}
		var supplementOpen bool
		results := evaluateTaskCoverage(target, e.coverageRecordForTarget(target, record))
		for _, result := range results {
			supplementOpen = supplementOpen || slices.Contains(result.OpenItems, "attributed_supplement")
			if comparing && len(targets) == 1 && len(results) == 1 && result.State == coverageSatisfied && result.Reason == "matching_primary" {
				if conclusion := terminalComparisonConclusion(result.PinnedFacts, delivery.chinese); conclusion != "" {
					delivery.comparisonLead = conclusion
				}
			}
			for _, fact := range result.PinnedFacts {
				if fact.Role != "last_valid_date" {
					add(fact.Role, fact.Evidence, unit, terminalRequestedPinnedFact(record, target, fact))
				}
			}
		}
		supplementExpected = supplementExpected || supplementOpen
		if supplementOpen && target.SourceMode == "named_first" {
			// An actual report can provide closest related context without
			// filling the original field or proving its observation period.
			for _, binding := range record.Bindings {
				if binding.GoalIndex != target.GoalIndex || binding.Role != "supplement" || binding.Kind != "body" {
					continue
				}
				for _, item := range record.Evidence {
					if item.Kind != "body" || !coverageSameSource(binding, item) || !coverageLocation(binding, item) ||
						binding.EvidenceID != item.EvidenceID || binding.JSONPointer != item.JSONPointer {
						continue
					}
					if fact, ok := coverageBodyFact(binding, item, true); ok && (fact.SourceDate != "" || fact.Date != "" || fact.Period != "") {
						add("supplement_context", fact, "", true)
					}
				}
			}
		}
	}
	if delivery.goal == "" {
		question := e.turnReview.Query
		if e.config.TaskPlanningHumanQuestion != nil {
			question = *e.config.TaskPlanningHumanQuestion
		}
		delivery.goal = terminalEvidenceText(taskPlanPrefix(question, 400))
	}
	for _, item := range record.Evidence {
		target, relevant := terminalCoverageTarget(record, targets, item)
		if !relevant {
			continue
		}
		// With no declared scope, an exact missing-day lookup anchors the
		// fallback. Broad exploratory reads are context, even if successful.
		if len(targets) == 0 && exactLookup && item.PeriodFrom != item.PeriodTo {
			continue
		}
		unit := e.terminalRequestedUnit(target)
		switch {
		case item.Kind == "gap" && item.Complete && coverageExactRange(item):
			add("coverage_boundary", item, "", false)
			foundLast := false
			for _, value := range record.Evidence {
				if terminalCoverageLastValidMatches(record, item, value) {
					foundLast = true
					add("last_valid_value", value, unit, false)
				}
			}
			if !foundLast && item.LastValidDate != "" {
				boundary := item
				boundary.Kind, boundary.Date, boundary.Raw = "last_valid", item.LastValidDate, nil
				add("last_valid_date", boundary, "", false)
			}
		case item.Kind == "cell" && item.SeriesID != "" &&
			(item.Operation == "query_series" || item.Operation == "analyze_table") &&
			(item.LatestInScope || item.RangeComplete && (item.Period != "" || item.Date != "" && item.Date >= item.PeriodFrom && item.Date <= item.PeriodTo) ||
				item.Date != "" && item.Date == item.PeriodFrom && item.Date == item.PeriodTo):
			if (len(targets) != 0 || linkedReads[item.EvidenceID]) && (!item.LatestInScope || target.StructuredTaskGoal != nil && target.SourceTime == "latest_observation") {
				add("scoped_value", item, unit, false)
			}
		case item.Kind == "derived" && item.Operation == "query_series" && item.SeriesID != "" && item.Complete && coverageExactRange(item) &&
			(len(targets) != 0 || linkedReads[item.EvidenceID]):
			add("scoped_value", item, unit, false)
		}
	}
	if len(targets) == 1 && len(delivery.facts) != 0 && targets[0].Delivery["aggregation"] == "as_reported" &&
		(targets[0].VersionTask == "" || targets[0].VersionTask == "single") && !slices.ContainsFunc(delivery.facts, func(f terminalCoverageFact) bool {
		return f.Evidence.Kind != "cell" || !f.Evidence.RangeComplete || !terminalUnitUnspecified(f.Evidence) || f.requestedUnit == ""
	}) {
		delivery.presentationUnit = e.terminalRequestedUnit(targets[0])
	}
	for _, item := range record.Evidence {
		if item.Kind == "body" {
			delivery.documentBodies = append(delivery.documentBodies, item)
		}
		if (len(delivery.facts) != 0 && delivery.comparisonLead == "") || len(delivery.context) >= terminalCoverageContextLimit {
			continue
		}
		if _, relevant := terminalCoverageTarget(record, targets, item); !relevant {
			continue
		}
		switch item.Kind {
		case "body":
			delivery.addContext(terminalCoverageDocumentRead(item, delivery.chinese))
		case "directory", "navigation":
			if len(delivery.facts) == 0 {
				delivery.addContext(terminalCoverageDirectory(item, catalogs[item.EvidenceID], delivery.chinese))
			}
		}
	}
	// Use the actual attempts, including zero hits, even when no source was
	// returned. A directory lookup or an incomplete read never proves absence.
	var attemptedSupplement, recoveredSearch, recoveredRead, failed, blocked bool
	var recoverySearchCount int
	for _, attempt := range attempts {
		data := attempt.result.Data
		stopped := attempt.result.PreflightRejected || data["execution_started"] == false
		if len(delivery.facts) == 0 {
			delivery.addContext(terminalCoverageAttemptContext(record, attempt, stopped, delivery.chinese))
		}
		switch attempt.Operation {
		case agenttools.ToolSearchKnowledge, agenttools.ToolListDocuments:
			attemptedSupplement = true
			if attempt.Success {
				recoveredSearch = true
				recoverySearchCount += max(citationInt(data["count"]), len(sourceNavigationRows(data["results"])), len(sourceNavigationRows(data["documents"])))
			}
		case agenttools.ToolReadDocument:
			attemptedSupplement = true
			recoveredRead = recoveredRead || attempt.Success
		}
		failed = failed || !stopped && !attempt.Success
		blocked = blocked || stopped
	}
	if delivery.hasGap() && !delivery.explicitOnly && !delivery.hasSupplement() && (supplementExpected || attemptedSupplement) {
		delivery.limitation = terminalSupplementStatus(delivery.chinese, recoveredSearch, recoveredRead, recoverySearchCount)
	}
	if len(delivery.facts) == 0 && len(delivery.context) == 0 {
		switch {
		case recoveredSearch:
			delivery.limitation = terminalSupplementStatus(delivery.chinese, true, recoveredRead, recoverySearchCount)
		case failed:
			delivery.limitation = delivery.text("来源查询未返回可用于所求结果的记录。", "The source query returned no records usable for the requested result.")
		case blocked:
			delivery.limitation = delivery.text("本轮的来源请求在读取开始前被拦下。", "The source requests stopped before a read began.")
		case len(attempts) != 0:
			delivery.limitation = delivery.text("已执行本轮来源查询，返回结果中缺少与所求对应的可核实记录。", "The source reads returned no verifiable record aligned with the requested result.")
		case record.StopReason != "":
			delivery.limitation = delivery.text("本轮取证已结束，尚未完成所需的来源读取。", "Source collection ended before the required source read could complete.")
		case !hasSourceContext:
			delivery.limitation = delivery.text("本轮没有取得可用于回答的已核实内容。", "This turn produced no verified material that resolves the request.")
		default:
			delivery.limitation = delivery.text("本轮没有完成可用于核对该结果的来源读取。", "No source read for checking that result completed this turn.")
		}
	}
	return delivery
}

func terminalRequestedPinnedFact(record coverageRecord, target taskCoverageTarget, fact coverageFact) bool {
	if fact.Role == "supplement" {
		return true
	}
	item := fact.Evidence
	return slices.ContainsFunc(record.Bindings, func(binding coverageBinding) bool {
		if binding.GoalIndex != target.GoalIndex || binding.Role != "primary" ||
			!coverageSameSource(binding, item) || !coverageLocation(binding, item) {
			return false
		}
		if item.SourceDomain == "structured" {
			return binding.SeriesID != "" || binding.Sheet != "" && binding.Column != ""
		}
		return binding.EvidenceID == item.EvidenceID && binding.JSONPointer == item.JSONPointer
	})
}

func (e *AgentEngine) terminalCoverageFailure(ctx context.Context, state *types.AgentState, legacy string) string {
	delivery := e.terminalCoverageDelivery(ctx, state, true)
	if !delivery.inspected {
		return legacy
	}
	return delivery.render(true, "")
}

func (e *AgentEngine) terminalCoverageWarning(ctx context.Context, state *types.AgentState, warning string) string {
	if !e.evidenceCompletionEnabled() {
		return warning
	}
	delivery := e.terminalCoverageDelivery(ctx, state, false)
	delivery.presentationUnit = ""
	return delivery.render(false, warning)
}

func terminalCoverageAttemptContext(record coverageRecord, attempt coverageAttempt, blocked, chinese bool) string {
	if attempt.Operation != "query_series" && attempt.Operation != "analyze_table" && attempt.Operation != "inspect_source" {
		return ""
	}
	var source string
	dataset := sourceNavigationString(attempt.Args, "dataset_id")
	library := sourceNavigationString(attempt.Args, "knowledge_base_id")
	sheet := sourceNavigationString(attempt.Args, "sheet")
	series := sourceNavigationString(attempt.Args, "series_id")
	for _, item := range record.Evidence {
		if dataset == "" || item.DatasetID != dataset || !coverageEqual(library, item.KnowledgeBaseID) ||
			!coverageEqual(sheet, item.Sheet) || !coverageEqual(series, item.SeriesID) {
			continue
		}
		if label := terminalCoverageLabel(item); label != "" {
			source = label
			break
		}
	}
	from, to := sourceNavigationString(attempt.Args, "start_date"), sourceNavigationString(attempt.Args, "end_date")
	var scope string
	if coverageExactRange(coverageEvidence{PeriodFrom: from, PeriodTo: to}) {
		scope = terminalCoveragePeriod(from, to, "")
	}
	if source != "" {
		scope = strings.TrimSpace(source + " " + scope)
	}
	if scope == "" {
		return ""
	}
	d := terminalCoverageDelivery{chinese: chinese}
	if blocked {
		return fmt.Sprintf(d.text("%s 的数据请求在读取开始前被拦下。", "The data request for %s stopped before a read began."), scope)
	}
	if !attempt.Success {
		return fmt.Sprintf(d.text("已查询 %s 的结构化记录，未取得可用结果。", "The query for structured records in %s returned no usable result."), scope)
	}
	return fmt.Sprintf(d.text("已查阅 %s 的结构化记录，所求结果仍待核实。", "Structured records were checked for %s. The requested result remains unverified."), scope)
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

func terminalComparisonConclusion(facts []coverageFact, chinese bool) string {
	if len(facts) != 2 || facts[0].Role != "scoped_value" || facts[1].Role != "scoped_value" {
		return ""
	}
	first, second := facts[0].Evidence, facts[1].Evidence
	if first.Metric == "" || first.Metric != second.Metric {
		return ""
	}
	firstValue, firstOK := terminalComparisonNumber(first)
	secondValue, secondOK := terminalComparisonNumber(second)
	if !firstOK || !secondOK {
		return ""
	}
	d := terminalCoverageDelivery{chinese: chinese}
	sameNumber := firstValue.Cmp(secondValue) == 0
	if !terminalUnitFromSource(first) || !terminalUnitFromSource(second) || first.Unit != second.Unit {
		opening := d.text("两处来源列示的数字不同。", "The figures printed by the two sources differ.")
		if sameNumber {
			opening = d.text("两处来源列示的数字相同。", "The two sources print the same number.")
		}
		if terminalUnitFromSource(first) && terminalUnitFromSource(second) {
			return opening + d.text("来源使用的单位不同，以下保留各自原值。", " The source units differ, and the original figures are shown in their own units.")
		}
		return opening + d.text("两处单位能否直接比较仍待核实。", " Whether the units are directly comparable remains unverified.")
	}
	if sameNumber {
		return d.text("两处来源在所问范围给出的数值相同。", "Both sources report the same value in the requested scope.")
	}
	return d.text("两处来源在所问范围给出的数值不一致。", "The two sources report different values in the requested scope.")
}

func terminalComparisonNumber(item coverageEvidence) (*big.Rat, bool) {
	value := item.ReportedValue
	if item.Kind != "body" {
		switch raw := item.Raw.(type) {
		case json.Number:
			value = raw.String()
		case string:
			value = raw
		}
	}
	if token := proseNumber.FindString(value); token == "" || token != value {
		return nil, false
	}
	return new(big.Rat).SetString(value)
}

func terminalCoverageLastValidMatches(record coverageRecord, gap, value coverageEvidence) bool {
	if sameCoverageLastValid(record, gap, value) {
		return true
	}
	if gap.Operation != "analyze_table" || value.Operation != "query_series" || value.Kind != "last_valid" ||
		value.DateCell == "" || value.Cell == "" || value.Raw == nil || gap.LastValidDate == "" || value.Date != gap.LastValidDate ||
		gap.SourceID != value.SourceID || gap.KnowledgeBaseID != value.KnowledgeBaseID || gap.Sheet != value.Sheet ||
		gap.SeriesID != value.SeriesID || gap.Column != value.Column {
		return false
	}
	// The published input and original observation have separate snapshot
	// identities. The native result also records the actual workbook hash.
	return slices.ContainsFunc(record.Evidence, func(input coverageEvidence) bool {
		return input.Kind == "analysis_input" && input.EvidenceID == gap.EvidenceID && input.SeriesID == gap.SeriesID &&
			input.SnapshotHash == gap.SnapshotHash && input.Raw == value.SnapshotHash
	})
}

func (e *AgentEngine) terminalRequestedUnit(target taskCoverageTarget) string {
	if target.StructuredTaskGoal == nil || target.RequestedUnit == "" || !strings.Contains(target.RequestBasis, target.RequestedUnit) {
		return ""
	}
	if strings.Contains(e.turnReview.Query, target.RequestBasis) {
		return target.RequestedUnit
	}
	for _, text := range documentScopeUserTexts(e.turnReview.Query, e.turnReview.Conversation) {
		if strings.Contains(text, target.RequestBasis) {
			return target.RequestedUnit
		}
	}
	return ""
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

func (d terminalCoverageDelivery) text(zh, en string) string {
	if d.chinese {
		return zh
	}
	return en
}

func (d *terminalCoverageDelivery) addContext(text string) {
	if text != "" && len(d.context) < terminalCoverageContextLimit && !slices.Contains(d.context, text) {
		d.context = append(d.context, text)
	}
}

func (d terminalCoverageDelivery) hasGap() bool {
	return slices.ContainsFunc(d.facts, func(f terminalCoverageFact) bool { return f.Evidence.Kind == "gap" })
}

func (d terminalCoverageDelivery) hasSupplement() bool {
	return slices.ContainsFunc(d.facts, func(f terminalCoverageFact) bool { return f.Role == "supplement" || f.Role == "supplement_context" })
}

func (d terminalCoverageDelivery) render(fallback bool, candidate string) string {
	if !d.inspected {
		return candidate
	}
	var lines []string
	var unitFactsAlreadyPresent, usedRequestedUnit bool
	if !fallback && d.presentationUnit != "" {
		candidate, unitFactsAlreadyPresent, usedRequestedUnit = terminalAddDeclaredUnit(candidate, d.presentationUnit, d.facts)
	}
	// Lead with the requested range and verified absence, where established.
	for _, gapFirst := range []bool{true, false} {
		for _, fact := range d.facts {
			if unitFactsAlreadyPresent || (fact.Evidence.Kind == "gap") != gapFirst || !fallback && !fact.required && !d.mentionsCheckedField(candidate, fact) ||
				!fallback && terminalCoverageFactPresent(candidate, fact) {
				continue
			}
			if line := terminalCoverageFactText(fact, d.chinese); line != "" && !slices.Contains(lines, line) {
				lines = append(lines, line)
				usedRequestedUnit = usedRequestedUnit || (fact.Evidence.Kind == "cell" || fact.Evidence.Kind == "last_valid" || fact.Evidence.Kind == "derived") &&
					fact.requestedUnit != "" && terminalUnitUnspecified(fact.Evidence)
			}
		}
	}
	if usedRequestedUnit {
		lines = append(lines, d.text("原表未标单位的数值按问题所给单位列示，原值未换算。", "Values without a source unit are shown in the unit supplied in the question. No conversion was applied."))
	}
	if fallback {
		candidate = d.comparisonLead
		if candidate == "" && len(d.facts) > 1 {
			candidate = d.directValueOpening()
		}
		if candidate == "" && len(lines) == 0 {
			candidate = d.text("所求结果仍待核实。", "The requested result remains unverified.")
			if d.goal != "" {
				candidate = d.text(d.goal+" 仍待核实。", "The requested result for "+d.goal+" remains unverified.")
			}
		}
		if len(lines) == 0 || d.comparisonLead != "" && !slices.ContainsFunc(d.facts, func(f terminalCoverageFact) bool { return f.Evidence.Kind == "body" }) {
			lines = append(lines, d.context...)
		}
	}
	if d.limitation != "" && (fallback || len(lines) != 0 && !d.citesCurrentDocument(candidate)) {
		lines = append(lines, d.limitation)
	}
	if len(lines) == 0 {
		return candidate
	}
	if candidate != "" {
		return strings.TrimSpace(candidate) + "\n\n" + strings.Join(lines, "\n\n")
	}
	return strings.Join(lines, "\n\n")
}

func (d terminalCoverageDelivery) directValueOpening() string {
	date := d.facts[0].Evidence.Date
	if coverageDay(date) == "" {
		return ""
	}
	var values []string
	for _, fact := range d.facts {
		item := fact.Evidence
		if fact.Role != "scoped_value" || item.Kind != "cell" || item.Date != date || item.Metric == "" || item.SourceID != d.facts[0].Evidence.SourceID {
			return ""
		}
		raw, ok := item.Raw.(json.Number)
		if !ok {
			return ""
		}
		value := terminalEvidenceText(item.Metric) + " " + raw.String()
		if terminalUnitFromSource(item) {
			value += " " + terminalEvidenceText(item.Unit)
		} else if fact.requestedUnit != "" && terminalUnitUnspecified(item) {
			value += " " + terminalEvidenceText(fact.requestedUnit)
		}
		values = append(values, value)
	}
	return d.text(date+" 的原值为 "+strings.Join(values, "、")+"。", "The recorded values for "+date+" are "+strings.Join(values, ", ")+".")
}

func (d terminalCoverageDelivery) mentionsCheckedField(candidate string, fact terminalCoverageFact) bool {
	item := fact.Evidence
	if fact.Role == "scoped_value" || item.Metric == "" || !strings.Contains(candidate, item.Metric) {
		return false
	}
	return item.SourceTitle != "" && strings.Contains(candidate, item.SourceTitle) ||
		item.Sheet != "" && strings.Contains(candidate, item.Sheet) ||
		item.Cell != "" && strings.Contains(candidate, item.Cell) ||
		slices.ContainsFunc(d.facts, func(related terminalCoverageFact) bool {
			value := related.Evidence
			return related.Role == "last_valid_value" && value.SourceID == item.SourceID && value.Sheet == item.Sheet &&
				value.SeriesID == item.SeriesID && value.Cell != "" && strings.Contains(candidate, value.Cell)
		})
}

func (d terminalCoverageDelivery) citesCurrentDocument(candidate string) bool {
	ids := uniqueCandidateCitationIDs(candidate)
	return slices.ContainsFunc(d.documentBodies, func(body coverageEvidence) bool {
		return slices.Contains(ids, body.ChunkID) && body.ChunkID != ""
	})
}

func terminalSupplementStatus(chinese, searched, read bool, hits int) string {
	switch {
	case read:
		if chinese {
			return "已读取授权范围内的补充材料，尚未核实与所问指标和时期对应的数值。"
		}
		return "Supplemental material was read within the authorized scope. A value for the requested measure and period is still unverified."
	case searched && hits == 0:
		if chinese {
			return "本轮已检索授权范围内的补充材料，未返回候选正文。"
		}
		return "The search for supplemental material within the authorized scope returned no candidate passages."
	case searched:
		if chinese {
			return "本轮检索到授权范围内的补充线索，相关时期的正文和数值仍待核实。"
		}
		return "The supplemental search returned leads within the authorized scope. The relevant passage and value remain unverified."
	default:
		if chinese {
			return "本轮尚未完成所问时期的补充材料读取。"
		}
		return "A supplemental read for the requested period did not complete this turn."
	}
}

func terminalCoverageDocumentRead(item coverageEvidence, chinese bool) string {
	if item.SourceTitle == "" {
		return ""
	}
	title := terminalEvidenceText(item.SourceTitle)
	if item.Page > 0 {
		if chinese {
			return fmt.Sprintf("本轮已读取 %s 第 %d 页返回的正文。", title, item.Page)
		}
		return fmt.Sprintf("This turn read the returned text from page %d of %s.", item.Page, title)
	}
	if chinese {
		return "本轮已读取 " + title + " 返回的正文片段。"
	}
	return "This turn read a returned text passage from " + title + "."
}

func (e *AgentEngine) terminalCoverageScopeAllowed(targets []taskCoverageTarget, item coverageEvidence) bool {
	source := item.SourceID
	if item.SourceDomain == "structured" {
		source = "" // The native structured executor authorizes whole libraries.
	}
	if !e.coverageScopeAllows(item.KnowledgeBaseID, source) {
		return false
	}
	if domain := e.coverageOnlyDomain(); domain != "" && domain != item.SourceDomain {
		return false
	}
	if !slices.ContainsFunc(targets, func(target taskCoverageTarget) bool { return target.SourceMode == "only" }) {
		return true
	}
	for _, target := range targets {
		docs, explicit := e.coverageOnlySelectedDocuments(target)
		if explicit && !coverageSelectedDocumentMatches(docs, item) {
			continue
		}
		if explicit || slices.ContainsFunc(e.evidenceCompletion.record.Bindings, func(binding coverageBinding) bool {
			return binding.GoalIndex == target.GoalIndex && (target.SourceMode != "only" || binding.Role == "primary") &&
				binding.SourceID == item.SourceID && binding.KnowledgeBaseID == item.KnowledgeBaseID
		}) {
			return true
		}
	}
	return false
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

func terminalCoverageDirectory(item coverageEvidence, catalog map[string]any, chinese bool) string {
	label := terminalCoverageLabel(item)
	if label == "" {
		return ""
	}
	directoryLine := func(label, date string) string {
		if date != "" {
			if chinese {
				return label + "，目录所列记录截至 " + terminalEvidenceText(date) + "。"
			}
			return label + " is listed with records through " + terminalEvidenceText(date) + "."
		}
		if chinese {
			return label + " 出现在本轮来源目录中。"
		}
		return label + " appeared in this turn's source listings."
	}
	var scope []string
	for _, field := range sourceNavigationRows(catalog["literal_fields"]) {
		if len(scope) >= terminalCoverageContextLimit {
			break
		}
		sheet, metric := sourceNavigationString(field, "sheet"), sourceNavigationString(field, "metric")
		if field["dataset_id"] != item.DatasetID || sheet == "" || metric == "" {
			continue
		}
		candidate := item
		candidate.Sheet, candidate.Metric = sheet, metric
		if line := directoryLine(terminalCoverageLabel(candidate), sourceNavigationString(field, "last_date")); !slices.Contains(scope, line) {
			scope = append(scope, line)
		}
	}
	if len(scope) > 0 {
		return strings.Join(scope, " ")
	}
	if entries, ok := coverageObject(item.Raw)["sheets"].([]any); ok && len(entries) == 1 {
		if sheet, ok := entries[0].(string); ok && sheet != "" {
			label += " / " + terminalEvidenceText(sheet)
		}
	}
	return directoryLine(label, item.LastObservedDate)
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

func terminalCoverageFactPresent(candidate string, fact terminalCoverageFact) bool {
	item := fact.Evidence
	if item.Kind == "gap" {
		return strings.Contains(candidate, terminalCoverageFactText(fact, true)) ||
			strings.Contains(candidate, terminalCoverageFactText(fact, false))
	}
	if item.SourceTitle == "" || !strings.Contains(candidate, item.SourceTitle) ||
		item.Cell != "" && !strings.Contains(candidate, item.Cell) || item.DateCell != "" && !strings.Contains(candidate, item.DateCell) {
		return false
	}
	if item.SourceDate != "" && !coverageTextHasDate(candidate, item.SourceDate) {
		return false
	}
	if item.Page > 0 && !strings.Contains(strings.ReplaceAll(candidate, " ", ""), fmt.Sprintf("第%d页", item.Page)) &&
		!strings.Contains(strings.ToLower(candidate), fmt.Sprintf("page %d", item.Page)) {
		return false
	}
	if item.Date != "" && !slices.ContainsFunc(calendarDateLiteral.FindAllString(candidate, -1), func(literal string) bool {
		return coverageDay(literal) == coverageDay(item.Date) && coverageDay(item.Date) != ""
	}) && !strings.Contains(candidate, item.Date) {
		return false
	}
	if numeric, ok := item.Raw.(json.Number); ok {
		return slices.Contains(proseNumber.FindAllString(candidate, -1), numeric.String())
	}
	value, _ := item.Raw.(string)
	return value == "" || strings.Contains(candidate, value)
}

func terminalUnitUnspecified(item coverageEvidence) bool {
	return item.Unit == "" && slices.Contains([]string{"", "none", "unknown", "needs_review"}, item.UnitBasis)
}

func terminalUnitFromSource(item coverageEvidence) bool {
	return item.Unit != "" && !slices.Contains([]string{"", "none", "unknown", "needs_review", "mixed"}, item.UnitBasis)
}

func terminalCoverageFactText(fact terminalCoverageFact, chinese bool) string {
	item := fact.Evidence
	label := terminalCoverageLabel(item)
	if label == "" {
		return ""
	}
	d := terminalCoverageDelivery{chinese: chinese}
	period := terminalCoveragePeriod(item.PeriodFrom, item.PeriodTo, item.Period)
	if item.Kind == "gap" && item.Complete && coverageExactRange(item) {
		return fmt.Sprintf(d.text("%s 在已核查范围 %s 内没有有效观测值。", "%s returned no valid observation in the checked scope %s."), label, terminalEvidenceText(period))
	}
	if item.Kind == "last_valid" && item.Raw == nil {
		if _, err := time.Parse(time.DateOnly, item.Date); err != nil {
			return ""
		}
		return fmt.Sprintf(d.text("%s 的最后有效日期是 %s。本轮所读结果只确认了日期，尚未取得该日原值。", "The last valid date for %s is %s. The source result confirmed the date. The value for that date remains unverified."), label, item.Date)
	}
	var raw string
	switch value := item.Raw.(type) {
	case string:
		raw = terminalEvidenceText(value)
	case json.Number:
		raw = value.String()
	}
	if raw == "" {
		return ""
	}
	var details []string
	if date := firstNonEmptyAgent(item.Date, period); date != "" {
		details = append(details, terminalEvidenceText(date))
	}
	if item.DateCell != "" {
		details = append(details, d.text("日期格 ", "date cell ")+terminalEvidenceText(item.DateCell))
	}
	if item.Cell != "" {
		details = append(details, d.text("取值格 ", "value cell ")+terminalEvidenceText(item.Cell))
	}
	if item.Page > 0 {
		details = append(details, fmt.Sprintf(d.text("第%d页", "page %d"), item.Page))
	}
	if item.Kind == "body" {
		if item.SpanStart == nil || item.SpanEnd == nil {
			return ""
		}
		if item.SourceDate != "" && item.SourceDate != coverageDay(item.Date) {
			details = append(details, d.text("材料日期 ", "source date ")+terminalEvidenceText(item.SourceDate))
		}
		title := label
		if len(details) > 0 {
			title += " (" + strings.Join(details, ", ") + ")"
		}
		if fact.Role == "supplement" {
			return fmt.Sprintf(d.text("补充资料 %s 的原文为“%s”。", "The supplemental source %s reports “%s”."), title, raw)
		}
		if fact.Role == "supplement_context" {
			return fmt.Sprintf(d.text("补充资料 %s 按其披露口径载明“%s”。", "The supplemental source %s reports “%s” in its own reporting scope."), title, raw)
		}
		return fmt.Sprintf(d.text("%s 的原文为“%s”。", "%s reports “%s”."), title, raw)
	}
	switch {
	case terminalUnitFromSource(item):
		raw += " " + terminalEvidenceText(item.Unit)
		if fact.requestedUnit != "" && item.Unit != fact.requestedUnit {
			details = append(details, d.text("按来源单位保留原值", "original source unit"))
		}
	case fact.requestedUnit != "" && terminalUnitUnspecified(item):
		raw += " " + terminalEvidenceText(fact.requestedUnit)
	case item.Kind == "cell" || item.Kind == "last_valid":
		details = append(details, d.text("单位未确认", "unit unconfirmed"))
	}
	descriptor := d.text("原值", "Recorded value")
	if item.LatestInScope || item.Kind == "last_valid" {
		descriptor = d.text("已核查范围内的最近有效值", "Latest valid observation in the checked scope")
	} else if item.Kind == "derived" {
		descriptor = d.text("已核对的查询结果", "Verified query result")
	}
	if fact.Role == "supplement" {
		descriptor = d.text("补充来源披露值", "Value reported by the supplemental source")
	}
	if len(details) > 0 {
		raw += " (" + strings.Join(details, ", ") + ")"
	}
	return fmt.Sprintf(d.text("%s，%s %s。", "%s. %s %s."), label, descriptor, raw)
}

func terminalAddDeclaredUnit(candidate, unit string, facts []terminalCoverageFact) (string, bool, bool) {
	// The model chose the task unit and the cited fields. Format only a
	// displayed number that matches a selected field's actual observation.
	paragraphs := strings.Split(candidate, "\n\n")
	matched := make([]bool, len(facts))
	updated := false
	for i, paragraph := range paragraphs {
		lines := strings.Split(paragraph, "\n")
		for j, line := range lines {
			ids := uniqueCandidateEvidenceIDs(line)
			if len(ids) == 0 {
				continue
			}
			visible := sourceCitationTag.ReplaceAllStringFunc(line, numericMask)
			spans := proseNumber.FindAllStringIndex(visible, -1)
			if len(spans) != 1 {
				continue
			}
			span := spans[0]
			for k, fact := range facts {
				item := fact.Evidence
				if matched[k] || item.CitationID == "" || !slices.Contains(ids, item.CitationID) || item.Metric == "" || !strings.Contains(visible[:span[0]], item.Metric) ||
					!terminalNumberRepresentsValue(visible[span[0]:span[1]], item.Raw) {
					continue
				}
				suffix := strings.TrimLeft(visible[span[1]:], " \t*_`")
				hasUnit := strings.HasPrefix(suffix, unit)
				if !hasUnit && strings.Trim(suffix, " \t*_`|.。；;，,") != "" {
					continue
				}
				matched[k] = true
				if !hasUnit {
					lines[j] = line[:span[1]] + " " + terminalEvidenceText(unit) + line[span[1]:]
					updated = true
				}
				break
			}
		}
		paragraphs[i] = strings.Join(lines, "\n")
	}
	if slices.Contains(matched, false) {
		return candidate, false, false
	}
	if !updated {
		return candidate, true, false
	}
	// Only the detached closing unit qualification can be replaced. Retain
	// qualitative remarks, numeric context and citations elsewhere verbatim.
	last := len(paragraphs) - 1
	for last >= 0 && strings.TrimSpace(paragraphs[last]) == "" {
		last--
	}
	if last >= 0 {
		closing := paragraphs[last]
		if strings.Contains(closing, unit) && !proseNumber.MatchString(closing) &&
			len(uniqueCandidateEvidenceIDs(closing))+len(uniqueCandidateCitationIDs(closing)) == 0 {
			paragraphs = slices.Delete(paragraphs, last, last+1)
		}
	}
	return strings.Join(paragraphs, "\n\n"), true, true
}

func terminalNumberRepresentsValue(display string, raw any) bool {
	var original string
	switch value := raw.(type) {
	case json.Number:
		original = value.String()
	case string:
		original = value
	}
	value, err := strconv.ParseFloat(original, 64)
	if err != nil {
		return false
	}
	display = strings.ReplaceAll(strings.ReplaceAll(display, ",", ""), "−", "-")
	precision := 0
	if _, fraction, hasDot := strings.Cut(display, "."); hasDot {
		precision = len(fraction)
	}
	return strings.TrimPrefix(display, "+") == strconv.FormatFloat(value, 'f', precision, 64)
}
