package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"math/big"
	"regexp"
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
	requestedUnit     string
	required, adopted bool
	includeCitation   bool
}

type terminalCoverageDelivery struct {
	facts             []terminalCoverageFact
	context           []string
	goal, limitation  string
	comparisonLead    string
	versionChoice     terminalVersionChoice
	documentBodies    []coverageEvidence
	zeroCountCalls    map[string]string
	presentationUnit  string
	chinese           bool
	includeCitations  bool
	prioritizePrimary bool
	explicitOnly      bool
	inspected         bool
}

// Reproject native executions before delivery. A result changed after its
// initial registration or a source outside the current scope cannot be used.
func (e *AgentEngine) terminalCoverageDelivery(ctx context.Context, state *types.AgentState, failure bool) terminalCoverageDelivery {
	var delivery terminalCoverageDelivery
	if !failure && (state == nil || e.holdCollaborationAnswer || !e.evidenceCompletionEligible()) {
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
	if state != nil && e.toolRegistry != nil {
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
	plannedTargets := e.evidenceCompletionTargets()
	targets := slices.DeleteFunc(slices.Clone(plannedTargets), func(target taskCoverageTarget) bool {
		return target.SourceTime == "general_concept" || slices.Equal(target.Required, []string{"optional"})
	})
	hasSourceContext := len(attempts) > 0 || hadCoverage || len(targets) > 0 || e.terminalCoverageContextKnown()
	delivery.inspected = hasSourceContext || failure
	if !delivery.inspected {
		return delivery
	}
	delivery.chinese = strings.HasPrefix(types.LanguageFromContextOrDefault(ctx), "zh")
	delivery.includeCitations = e.config != nil && e.config.CitationsEnabled()
	delivery.prioritizePrimary = slices.ContainsFunc(targets, func(target taskCoverageTarget) bool { return target.SourceMode == "named_first" })
	delivery.explicitOnly = slices.ContainsFunc(targets, func(target taskCoverageTarget) bool { return target.SourceMode == "only" })

	for _, attempt := range attempts {
		if !attempt.Success || attempt.Operation != "analyze_table" {
			continue
		}
		call := types.ToolCall{ID: attempt.ToolCallID, Result: attempt.result}
		for _, id := range e.coverageCallCitations(call, attempt.EvidenceID) {
			for _, scalar := range coverageObject(e.turnReview.StructuredCitations[id].Directory["value"]) {
				if reviewDigest(scalar) == reviewDigest(0) {
					if delivery.zeroCountCalls == nil {
						delivery.zeroCountCalls = make(map[string]string)
					}
					delivery.zeroCountCalls[id] = attempt.EvidenceID
				}
			}
		}
	}

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

	add := func(role string, item coverageEvidence, unit string, required bool) *terminalCoverageFact {
		switch item.Kind {
		case "cell", "last_valid", "derived", "gap", "body":
		default:
			return nil
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
		if index := slices.IndexFunc(delivery.facts, func(prior terminalCoverageFact) bool {
			if role == "coverage_boundary" && prior.Role == role && terminalSameFieldGap(record, item, prior.Evidence) {
				return true
			}
			if coverageAnalysisEndpoint(item) && coverageAnalysisEndpoint(prior.Evidence) && prior.Role == role {
				checked := prior.Evidence
				return item.KnowledgeBaseID == checked.KnowledgeBaseID && item.SourceID == checked.SourceID &&
					item.SnapshotHash == checked.SnapshotHash && item.Sheet == checked.Sheet && item.SeriesID == checked.SeriesID &&
					item.Date == checked.Date && item.Cell == checked.Cell && item.Raw == checked.Raw
			}
			return coverageFactRef(prior.Evidence) == coverageFactRef(item) && prior.Evidence.Kind == item.Kind &&
				prior.Evidence.PeriodFrom == item.PeriodFrom && prior.Evidence.PeriodTo == item.PeriodTo && prior.Evidence.Period == item.Period
		}); index >= 0 {
			delivery.facts[index].required = delivery.facts[index].required || required
			return &delivery.facts[index]
		}
		delivery.facts = append(delivery.facts, terminalCoverageFact{
			coverageFact: coverageFact{role, item}, requestedUnit: unit, required: required,
		})
		return &delivery.facts[len(delivery.facts)-1]
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
				if fact.Role == "last_valid_date" {
					continue
				}
				var requiredAnalysis bool
				switch fact.Role {
				case "coverage_boundary":
					_, requiredAnalysis = e.plannedAnalysisGap(target, record, fact.Evidence)
				case "last_valid_value":
					var scoped bool
					fact.Evidence, requiredAnalysis, scoped = e.terminalPlannedLastValid(target, record, result.PinnedFacts, fact.Evidence)
					if !scoped {
						continue
					}
				}
				delivered := add(fact.Role, fact.Evidence, unit, requiredAnalysis || terminalRequestedPinnedFact(record, target, fact))
				if delivered != nil && requiredAnalysis && fact.Role == "last_valid_value" && fact.Evidence.Operation == "query_series" {
					delivered.Evidence.Subject = firstNonEmptyAgent(fact.Evidence.Subject, delivered.Evidence.Subject)
				}
			}
		}
		supplementExpected = supplementExpected || supplementOpen
	}
	question := e.coverageAnalysisQuestion()
	independentReads := failure && turn.legacyGap != "" && turn.stopReason != "" && len(targets) == 0
	if delivery.goal == "" {
		delivery.goal = terminalEvidenceText(taskPlanPrefix(question, 400))
	}
	if len(plannedTargets) == 0 {
		for _, item := range record.Evidence {
			if !(coverageAnalysisEndpoint(item) || coverageAnalysisMissingCell(item)) || !e.coverageScopeAllows(item.KnowledgeBaseID, item.KnowledgeID) {
				continue
			}
			from, to := coverageAnalysisRetriedScope(attempts, item, question)
			if gap, ok := coverageAnalysisGap(item, from, to, ""); ok {
				if coverageAnalysisMissingCell(item) {
					record.Evidence = append(record.Evidence, gap)
				} else {
					add("coverage_boundary", gap, "", true)
					add("last_valid_value", item, "", true)
				}
			}
		}
	}
	inPlannedAnalysisScope := func(analysis coverageEvidence) bool {
		return len(plannedTargets) == 0 || slices.ContainsFunc(targets, func(target taskCoverageTarget) bool {
			_, matches := e.plannedAnalysisGap(target, record, analysis)
			return matches
		})
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
			analysisGap := coverageAnalysisLatestBoundary(item)
			analysisInScope := !analysisGap || inPlannedAnalysisScope(item)
			required := analysisGap && analysisInScope && coverageCurrentAnalysisWorkbook(record, item) &&
				slices.ContainsFunc(attempts, func(attempt coverageAttempt) bool {
					return attempt.EvidenceID == item.EvidenceID && attempt.Success && e.coverageBoundAnalysisField(item, attempt.Args)
				})
			add("coverage_boundary", item, "", required)
			foundLast := false
			for _, value := range record.Evidence {
				if !analysisInScope || coverageAnalysisEndpoint(value) && !inPlannedAnalysisScope(value) {
					continue
				}
				if sameCoverageLastValid(record, item, value) || item.WorkbookSnapshotHash == "" && coverageAnalysisFollowUpMatches(record, item, value) {
					foundLast = true
					if latest := add("last_valid_value", value, unit, required); latest != nil && required && value.Operation == "query_series" {
						if latest.Evidence.Subject == "" {
							latest.Evidence.Subject = item.Subject
						}
					}
				}
			}
			if !foundLast && analysisInScope && item.LastValidDate != "" {
				boundary := item
				boundary.Kind, boundary.Date, boundary.Raw = "last_valid", item.LastValidDate, nil
				add("last_valid_date", boundary, "", false)
			}
		case item.Kind == "cell" && item.SeriesID != "" &&
			(item.Operation == "query_series" || item.Operation == "analyze_table") &&
			(item.LatestInScope || item.RangeComplete && (item.Period != "" || item.Date != "" && item.Date >= item.PeriodFrom && item.Date <= item.PeriodTo) ||
				item.Date != "" && item.Date == item.PeriodFrom && item.Date == item.PeriodTo):
			repeatsEndpoint := item.Operation == "analyze_table" && item.WorkbookSnapshotHash != "" && slices.ContainsFunc(delivery.facts, func(fact terminalCoverageFact) bool {
				last := fact.Evidence
				return fact.Role == "last_valid_value" && last.EvidenceID == item.EvidenceID && last.SeriesID == item.SeriesID &&
					last.Date == item.Date && last.Cell == item.Cell && last.Raw == item.Raw
			})
			if !repeatsEndpoint && (len(targets) != 0 || linkedReads[item.EvidenceID] || independentReads) &&
				(!item.LatestInScope || target.StructuredTaskGoal != nil && target.SourceTime == "latest_observation") {
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
	if independentReads && len(delivery.facts) != 0 && delivery.limitation == "" {
		delivery.limitation = delivery.text("以上是本轮实际取得的来源读数。所求结果仍有内容待核实。", "These are the verified source readings. The rest of the requested result remains unverified.")
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
	if turn.enabled && turn.legacyGap != "" && turn.stopReason != "" && len(targets) == 0 && len(delivery.facts) == 0 {
		var source, sheet, lastDate string
		var uniformDate bool
		for _, item := range record.Evidence {
			if item.Kind != "directory" || item.Operation != "list_series" || item.DatasetID == "" || item.Sheet == "" {
				continue
			}
			title := item.SourceTitle
			if title == "" {
				for _, dataset := range record.Evidence {
					if dataset.Operation == "list_datasets" && dataset.Kind == "directory" &&
						dataset.DatasetID == item.DatasetID && dataset.KnowledgeBaseID == item.KnowledgeBaseID &&
						dataset.SnapshotHash == item.SnapshotHash {
						title = dataset.SourceTitle
						break
					}
				}
			}
			if title == "" || source != "" && (source != title || sheet != item.Sheet) {
				source = ""
				break
			}
			if source == "" {
				lastDate, uniformDate = item.LastValidDate, coverageDay(item.LastValidDate) != ""
			} else {
				uniformDate = uniformDate && lastDate == item.LastValidDate
			}
			source, sheet = title, item.Sheet
		}
		if source != "" {
			delivery.context, delivery.goal = nil, ""
			label := delivery.checkedFactSource(coverageEvidence{SourceTitle: source, Sheet: sheet})
			delivery.limitation = fmt.Sprintf(delivery.text("本轮已定位%s的字段目录，尚未完成所问原值的核对。", "This turn located the field directory for %s. Verification of the requested value remains incomplete."), label)
			if uniformDate {
				delivery.limitation += fmt.Sprintf(delivery.text("本轮列出的字段最后有效日均为 %s。", "The fields shown in that directory share a last valid date of %s."), lastDate)
			}
			delivery.context, delivery.limitation = []string{delivery.limitation}, ""
			seen := make(map[string]bool)
			for _, report := range delivery.documentBodies {
				if seen[report.SourceID] {
					continue
				}
				seen[report.SourceID] = true
				delivery.addContext(terminalCoverageDocumentRead(report, delivery.chinese))
			}
		}
	}
	delivery.versionChoice = currentTerminalVersionChoice(record, delivery.facts)
	return delivery
}

func terminalSameFieldGap(record coverageRecord, item, prior coverageEvidence) bool {
	if item.Kind != "gap" || prior.Kind != "gap" || item.SeriesID == "" || item.DatasetID == "" ||
		item.KnowledgeBaseID != prior.KnowledgeBaseID || item.SourceID != prior.SourceID || item.DatasetID != prior.DatasetID ||
		item.Sheet != prior.Sheet || item.SeriesID != prior.SeriesID || item.Column != prior.Column ||
		item.PeriodFrom != prior.PeriodFrom || item.PeriodTo != prior.PeriodTo || item.Period != prior.Period ||
		item.LastValidDate != prior.LastValidDate {
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

func (e *AgentEngine) terminalPlannedLastValid(target taskCoverageTarget, record coverageRecord, pinned []coverageFact, item coverageEvidence) (checked coverageEvidence, required, inScope bool) {
	if coverageAnalysisEndpoint(item) {
		_, matches := e.plannedAnalysisGap(target, record, item)
		return item, matches, matches
	}
	var analysisBoundary, otherBoundary bool
	for _, boundary := range pinned {
		gap := boundary.Evidence
		if boundary.Role != "coverage_boundary" || !sameCoverageLastValid(record, gap, item) {
			continue
		}
		if !coverageAnalysisLatestBoundary(gap) {
			otherBoundary = true
			continue
		}
		analysisBoundary = true
		if _, matches := e.plannedAnalysisGap(target, record, gap); matches {
			item.Subject = firstNonEmptyAgent(gap.Subject, item.Subject)
			return item, true, true
		}
	}
	return item, false, !analysisBoundary || otherBoundary
}

func terminalRequestedPinnedFact(record coverageRecord, target taskCoverageTarget, fact coverageFact) bool {
	if fact.Role == "supplement" || fact.Role == "supplement_context" {
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

func (e *AgentEngine) terminalCoverageFailure(ctx context.Context, state *types.AgentState) string {
	delivery := e.terminalCoverageDelivery(ctx, state, true)
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
	d := terminalCoverageDelivery{chinese: chinese}
	switch attempt.Operation {
	case agenttools.ToolSearchKnowledge:
		query := terminalEvidenceText(taskPlanPrefix(sourceNavigationString(attempt.Args, "query"), 160))
		if blocked {
			return d.text("授权文档检索在执行前被拦下。", "The authorized document search stopped before it ran.")
		}
		if query != "" && attempt.Success {
			return fmt.Sprintf(d.text("已在授权文档中检索“%s”。", "The authorized documents were searched for “%s”."), query)
		}
		if !attempt.Success {
			return d.text("尝试检索授权文档，未取得可用结果。", "The authorized document search returned no usable result.")
		}
	case agenttools.ToolReadDocument:
		if blocked {
			return d.text("已选文档的读取请求在执行前被拦下。", "The selected document read stopped before it ran.")
		}
		if !attempt.Success {
			return d.text("尝试读取已选文档，未取得可核实的正文。", "The selected document read returned no verifiable passage.")
		}
	}
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
	paragraphs := strings.Split(strings.TrimSpace(candidate), "\n\n")
	var insertions []terminalCoverageInsertion
	sourced := false
	primaryLead := -1
	if !fallback && d.prioritizePrimary {
		paragraphs, primaryLead, sourced = d.primarySourceOpening(paragraphs)
	}
	var supplementalSource string
	for _, fact := range d.facts {
		if fact.Role != "supplement" && fact.Role != "supplement_context" {
			continue
		}
		if supplementalSource != "" && supplementalSource != fact.Evidence.SourceID {
			supplementalSource = ""
			break
		}
		supplementalSource = fact.Evidence.SourceID
	}
	// Lead with the requested range and verified absence, where established.
	for _, gapFirst := range []bool{true, false} {
		for _, fact := range d.facts {
			item := fact.Evidence
			if unitFactsAlreadyPresent || (item.Kind == "gap") != gapFirst {
				continue
			}
			if item.Kind == "gap" || item.Kind == "last_valid" {
				present, anchor, addSource := d.checkedFieldLocation(paragraphs, fact)
				if primaryLead >= 0 && fact.required && (fact.Role == "coverage_boundary" || fact.Role == "last_valid_value") &&
					(anchor < 0 || d.supplementParagraph(paragraphs[anchor])) {
					present, anchor, addSource = false, primaryLead, false
				}
				if addSource {
					paragraphs[anchor] = fmt.Sprintf(d.text("据%s，%s", "From %s. %s"),
						d.checkedFactSource(item), paragraphs[anchor])
					sourced = true
				}
				if present || !fallback && !fact.required && anchor < 0 && !d.mentionsCheckedField(candidate, fact) {
					continue
				}
				if item.Kind == "last_valid" && item.Raw == nil && slices.ContainsFunc(d.facts, func(peer terminalCoverageFact) bool {
					return peer.Evidence.Kind == "last_valid" && peer.Evidence.Raw != nil && peer.Evidence.Date == item.Date &&
						terminalSameImport(item, peer.Evidence) && peer.Evidence.Sheet == item.Sheet && peer.Evidence.SeriesID == item.SeriesID
				}) {
					continue
				}
				insertions = append(insertions, terminalCoverageInsertion{anchor, fact})
				usedRequestedUnit = usedRequestedUnit || item.Raw != nil && fact.requestedUnit != "" && terminalUnitUnspecified(item)
				continue
			}
			if primaryLead >= 0 && fact.required && fact.Role == "scoped_value" && item.SourceDomain == "structured" &&
				!slices.ContainsFunc(paragraphs[:primaryLead+1], func(paragraph string) bool { return terminalCoverageFactPresent(paragraph, fact) }) {
				insertions = append(insertions, terminalCoverageInsertion{primaryLead, fact})
				continue
			}
			if !fallback && (!fact.required && !d.mentionsCheckedField(candidate, fact) || terminalCoverageFactPresent(candidate, fact)) {
				continue
			}
			if !fallback && (fact.Role == "supplement" || fact.Role == "supplement_context") && d.supplementFactPresent(paragraphs, fact.Evidence) {
				continue
			}
			fact.adopted = supplementalSource != "" && supplementalSource == item.SourceID
			fact.includeCitation = d.includeCitations
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
		if candidate == "" && len(lines) == 0 && len(insertions) == 0 {
			if d.goal != "" {
				candidate = d.text(d.goal+" 仍待核实。", "The requested result for "+d.goal+" remains unverified.")
			} else if d.limitation == "" && len(d.context) == 0 {
				candidate = d.text("所求结果仍待核实。", "The requested result remains unverified.")
			}
		}
		if len(lines) == 0 && len(insertions) == 0 || d.comparisonLead != "" && !slices.ContainsFunc(d.facts, func(f terminalCoverageFact) bool { return f.Evidence.Kind == "body" }) {
			lines = append(lines, d.context...)
		}
	}
	if d.limitation != "" && (fallback || (len(lines) != 0 || len(insertions) != 0) && !d.citesCurrentDocument(candidate)) {
		lines = append(lines, d.limitation)
	}
	if choice := d.versionChoice.render(d.chinese); choice != "" && !strings.Contains(candidate, choice) {
		lines = append(lines, choice)
	}
	if len(insertions) != 0 {
		if fallback && candidate != "" {
			lines = append([]string{candidate}, lines...)
		}
		candidate = d.insertCheckedFacts(paragraphs, insertions, lines)
		lines = nil
	} else if sourced {
		candidate = strings.Join(paragraphs, "\n\n")
	}
	if len(lines) == 0 {
		return candidate
	}
	if candidate != "" {
		return strings.TrimSpace(candidate) + "\n\n" + strings.Join(lines, "\n\n")
	}
	return strings.Join(lines, "\n\n")
}

func (d terminalCoverageDelivery) supplementFactPresent(paragraphs []string, item coverageEvidence) bool {
	if item.SourceTitle == "" || item.ChunkID == "" || item.ReportedValue == "" || item.Date == "" || item.Metric == "" && item.Subject == "" {
		return false
	}
	return slices.ContainsFunc(paragraphs, func(paragraph string) bool {
		if !slices.Contains(uniqueCandidateCitationIDs(paragraph), item.ChunkID) &&
			(d.includeCitations || !terminalContainsLabel(paragraph, item.SourceTitle)) {
			return false
		}
		for _, clause := range terminalClaimBreak.Split(sourceCitationTag.ReplaceAllStringFunc(paragraph, numericMask), -1) {
			if coverageTextHasDate(clause, item.Date) &&
				(item.Subject == "" || terminalContainsLabel(clause, item.Subject)) &&
				(item.Metric == "" || terminalContainsLabel(clause, item.Metric)) &&
				(item.Unit == "" || strings.Contains(clause, item.Unit)) &&
				coverageClaimInBody(clause, 0, clause, item.ReportedValue, item.Unit) {
				return true
			}
		}
		return false
	})
}

func (d terminalCoverageDelivery) primarySourceOpening(paragraphs []string) ([]string, int, bool) {
	if len(d.documentBodies) == 0 || d.comparisonLead != "" {
		return paragraphs, -1, false
	}
	firstSupplement := slices.IndexFunc(paragraphs, d.supplementParagraph)
	if firstSupplement < 0 {
		return paragraphs, -1, false
	}
	if firstSupplement == 0 {
		// A paragraph may already give the primary finding before the report.
		// A report citation at its end does not make that opening a supplement.
		sentences := terminalSentenceBreak.Split(paragraphs[0], 2)
		if len(sentences) == 2 && !d.supplementParagraph(sentences[0]) {
			return paragraphs, -1, false
		}
	}
	var primary []int
	var requiredPrimary bool
	for _, fact := range d.facts {
		if !fact.required {
			continue
		}
		anchor := -1
		switch {
		case fact.Role == "coverage_boundary" || fact.Role == "last_valid_value":
			present, found, _ := d.checkedFieldLocation(paragraphs, fact)
			if present || fact.Role == "coverage_boundary" {
				anchor = found
			}
		case fact.Role == "scoped_value" && fact.Evidence.SourceDomain == "structured":
			anchor = slices.IndexFunc(paragraphs, func(paragraph string) bool {
				return !d.supplementParagraph(paragraph) && terminalCoverageFactPresent(paragraph, fact)
			})
		default:
			continue
		}
		requiredPrimary = true
		if anchor >= 0 && !d.supplementParagraph(paragraphs[anchor]) && !slices.Contains(primary, anchor) {
			primary = append(primary, anchor)
		}
	}
	if len(primary) == 0 {
		if requiredPrimary {
			return append([]string{""}, paragraphs...), 0, true
		}
		return paragraphs, -1, false
	}
	slices.Sort(primary)
	if primary[len(primary)-1] < firstSupplement {
		return paragraphs, primary[0], false
	}
	ordered := make([]string, 0, len(paragraphs))
	for _, index := range primary {
		ordered = append(ordered, paragraphs[index])
	}
	for i, paragraph := range paragraphs {
		if !slices.Contains(primary, i) {
			ordered = append(ordered, paragraph)
		}
	}
	return ordered, 0, true
}

func (d terminalCoverageDelivery) supplementParagraph(paragraph string) bool {
	if d.citesCurrentDocument(paragraph) {
		return true
	}
	return slices.ContainsFunc(d.documentBodies, func(item coverageEvidence) bool {
		title := item.SourceTitle
		if terminalContainsLabel(paragraph, title) {
			return true
		}
		if i := strings.LastIndexByte(title, '.'); i > 0 {
			return terminalContainsLabel(paragraph, title[:i])
		}
		return false
	})
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

var (
	terminalClaimBreak    = regexp.MustCompile(`[。！？!?；;\n]+|\.[ \t]+`)
	terminalSentenceBreak = regexp.MustCompile(`[。！？!?]+|\.[ \t]+`)
	terminalDateSpacing   = regexp.MustCompile(`[ \t]*([年月日])[ \t]*`)
	terminalGapDisplay    = regexp.MustCompile(`(?i)(?:返回\s*)?(?:零|0)\s*条(?:有效)?(?:记录|观测值)|没有有效(?:记录|观测值)|有效(?:记录|观测)数(?:都|均)?(?:是|为|[:：=])\s*0\b|\b(?:0|no|zero) valid (?:records?|observations?)\b`)
	terminalGroupedZero   = regexp.MustCompile(`(?i)有效(?:记录|观测)数(?:都|均)(?:是|为|[:：=])\s*0\b|\b(?:all|each) (?:have|has) (?:0|zero) valid (?:records?|observations?)\b`)
	terminalTableRow      = regexp.MustCompile(`(?m)^[ \t]*\|`)
	terminalRangeJoin     = regexp.MustCompile(`(?i)^\s*(?:至|到|[-—–~～]|to|through)\s*$`)
	terminalAssignment    = regexp.MustCompile("(?i)[:：=为是]|\\b(?:is|was)\\b")
	terminalUnasserted    = regexp.MustCompile(`(?i)并非|不是|不能|无法|是否|\b(?:not|cannot|whether)\b`)
	terminalWorkbookExt   = regexp.MustCompile(`(?i)\.(?:xlsx|xlsm|xlsb|xls|ods|csv|tsv)\b`)
	terminalDateLabel     = regexp.MustCompile(`(?i)最后有效日期|最后有值的日期|\b(?:last|latest) valid date\b`)
)

// Check the displayed operands of a verified fact. A paragraph may establish
// the source and field. The observed date and result must be in one assertion.
func (d terminalCoverageDelivery) checkedFieldLocation(paragraphs []string, fact terminalCoverageFact) (present bool, anchor int, addSource bool) {
	item := fact.Evidence
	rendered := strings.TrimRight(d.checkedFactSentence([]terminalCoverageFact{fact}), ".。")
	anchor = -1
	for i, paragraph := range paragraphs {
		if strings.Contains(paragraph, "```") {
			continue
		}
		visible := sourceCitationTag.ReplaceAllStringFunc(paragraph, numericMask)
		contextual := terminalFieldMention(visible, item) && !d.competingSource(paragraph, item, false)
		if contextual && anchor < 0 {
			anchor = i
		}
		if contextual && item.Kind == "gap" && item.Subject != "" {
			group := slices.DeleteFunc(slices.Clone(d.facts), func(other terminalCoverageFact) bool {
				return !terminalGroupable(item, other.Evidence)
			})
			if len(group) > 1 && strings.Contains(visible, d.checkedFactSentence(group)) {
				return true, i, false
			}
		}
		for _, clause := range terminalClaimBreak.Split(paragraph, -1) {
			ids := uniqueCandidateEvidenceIDs(clause)
			cited := item.CitationID != "" && slices.Contains(ids, item.CitationID)
			statement := sourceCitationTag.ReplaceAllStringFunc(clause, numericMask)
			if contextual && rendered != "" && strings.TrimRight(strings.TrimSpace(statement), ".。") == rendered {
				return true, i, false
			}
			local := terminalFieldMention(statement, item)
			coordinates := !contextual && !local && !cited && !terminalTableRow.MatchString(paragraph) &&
				d.checkedCoordinates(statement, paragraph, item)
			if !contextual && !local && !cited && !coordinates || d.competingSource(clause, item, cited) {
				continue
			}
			if anchor < 0 {
				anchor = i
			}
			groupCount := item.Kind == "gap" && contextual && item.EvidenceID != "" && terminalGroupedZero.MatchString(statement) && slices.ContainsFunc(ids, func(id string) bool {
				return d.zeroCountCalls[id] == item.EvidenceID
			})
			if terminalUnasserted.MatchString(statement) || !cited && !groupCount && !d.terminalFieldApplies(statement, visible, item) {
				continue
			}
			dates := terminalDateSpacing.ReplaceAllString(statement, "$1")
			if item.Kind == "gap" && terminalGapDisplay.MatchString(statement) && terminalGapPeriodPresent(dates, item) {
				return true, i, false
			}
			if item.Kind == "last_valid" && terminalExactDay(dates, item.Date) {
				anchor = i
				if item.Raw == nil && terminalDateLabel.MatchString(statement) && (cited || terminalContainsLabel(statement, item.Metric)) ||
					item.Raw != nil && terminalDisplayedValue(dates, fact, cited) {
					return true, i, coordinates
				}
			}
		}
	}
	return false, anchor, false
}

func (d terminalCoverageDelivery) checkedCoordinates(statement, paragraph string, item coverageEvidence) bool {
	if item.Kind != "last_valid" || item.Raw == nil || item.SourceTitle == "" || item.DateCell == "" || item.Cell == "" ||
		!terminalContainsLabel(statement, item.Metric) || !terminalContainsLabel(statement, item.DateCell) ||
		!terminalContainsLabel(statement, item.Cell) || d.competingSource(paragraph, item, false) {
		return false
	}
	return !slices.ContainsFunc(d.facts, func(fact terminalCoverageFact) bool {
		peer := fact.Evidence
		return !terminalSameImport(item, peer) && peer.Metric == item.Metric &&
			peer.Date == item.Date && peer.DateCell == item.DateCell && peer.Cell == item.Cell
	})
}

func terminalFieldMention(text string, item coverageEvidence) bool {
	return terminalSourceMention(text, item.SourceTitle) && terminalContainsLabel(text, item.Metric) &&
		(item.Sheet == "" || terminalContainsLabel(text, item.Sheet))
}

func terminalSourceMention(text, title string) bool {
	stem := terminalWorkbookExt.ReplaceAllString(title, "")
	return terminalContainsLabel(text, title) || stem != title && terminalContainsLabel(text, stem)
}

func terminalContainsLabel(text, label string) bool {
	if label == "" {
		return false
	}
	text = strings.ToLower(text)
	return maskExactSourceIdentity(text, strings.ToLower(label), true) != text
}

func terminalSameImport(item, peer coverageEvidence) bool {
	return item.SourceID == peer.SourceID && item.KnowledgeBaseID == peer.KnowledgeBaseID &&
		firstNonEmptyAgent(item.WorkbookSnapshotHash, item.SnapshotHash) == firstNonEmptyAgent(peer.WorkbookSnapshotHash, peer.SnapshotHash)
}

func (d terminalCoverageDelivery) competingSource(text string, item coverageEvidence, cited bool) bool {
	if len(uniqueCandidateCitationIDs(text)) > 0 {
		return true
	}
	visible := sourceCitationTag.ReplaceAllStringFunc(text, numericMask)
	withoutTitle := strings.ReplaceAll(strings.ToLower(visible), strings.ToLower(item.SourceTitle), "")
	return terminalWorkbookExt.MatchString(withoutTitle) || slices.ContainsFunc(d.documentBodies, func(body coverageEvidence) bool {
		return terminalContainsLabel(visible, body.SourceTitle)
	}) || slices.ContainsFunc(d.facts, func(fact terminalCoverageFact) bool {
		peer := fact.Evidence
		return !terminalSameImport(item, peer) && (!cited || !strings.EqualFold(item.SourceTitle, peer.SourceTitle)) &&
			terminalSourceMention(visible, peer.SourceTitle)
	})
}

func (d terminalCoverageDelivery) terminalFieldApplies(statement, paragraph string, item coverageEvidence) bool {
	if item.Subject != "" && !terminalContainsLabel(statement, item.Subject) && !terminalContainsLabel(statement, item.Cell) {
		return false
	}
	return !slices.ContainsFunc(d.facts, func(fact terminalCoverageFact) bool {
		peer := fact.Evidence
		if !terminalSameImport(item, peer) || peer.SeriesID == item.SeriesID {
			return false
		}
		if peer.Metric == item.Metric && peer.Sheet == item.Sheet {
			return item.Subject == "" && !terminalContainsLabel(statement, item.Cell) ||
				item.Kind == "gap" && terminalContainsLabel(statement, peer.Subject)
		}
		return peer.Sheet != item.Sheet && terminalContainsLabel(statement, peer.Sheet) ||
			peer.Metric != item.Metric && (terminalContainsLabel(statement, peer.Metric) ||
				!terminalContainsLabel(statement, item.Metric) && terminalContainsLabel(paragraph, peer.Metric))
	})
}

func terminalExactDay(text, day string) bool {
	found := false
	for _, span := range calendarDateLiteral.FindAllStringIndex(text, -1) {
		if date := coverageDay(text[span[0]:span[1]]); date != "" && standaloneDateToken(text, span[0], span[1]) {
			if date != day {
				return false
			}
			found = true
		}
	}
	return found
}

func terminalGapPeriodPresent(text string, item coverageEvidence) bool {
	if item.Period != "" {
		return terminalContainsLabel(text, item.Period)
	}
	from, to := item.PeriodFrom, item.PeriodTo
	if from == to {
		return terminalExactDay(text, from)
	}
	if strings.Contains(text, terminalCoveragePeriod(from, to, "")) {
		return true
	}
	if start := strings.Index(text, from); start >= 0 {
		between, _, found := strings.Cut(text[start+len(from):], to)
		if found && terminalRangeJoin.MatchString(between) {
			return true
		}
	}
	if date, err := time.Parse(time.DateOnly, from); err == nil && date.Day() == 1 && date.AddDate(0, 1, -1).Format(time.DateOnly) == to {
		return slices.ContainsFunc(calendarDateLiteral.FindAllStringIndex(text, -1), func(span []int) bool {
			literal := text[span[0]:span[1]]
			return standaloneDateToken(text, span[0], span[1]) && (literal == date.Format("2006年1月") || literal == date.Format("2006-01"))
		})
	}
	return false
}

func terminalDisplayedValue(statement string, fact terminalCoverageFact, cited bool) bool {
	item := fact.Evidence
	raw, ok := reviewScalarWitnessQuote(item.Raw)
	if !ok {
		return false
	}
	original, numeric := new(big.Rat).SetString(raw)
	unit := fact.requestedUnit
	if terminalUnitFromSource(item) {
		unit = item.Unit
	}
	match := func(right string) bool {
		right = strings.TrimLeft(right, " \t*_\x60")
		if !numeric {
			return terminalContainsLabel(right, raw) && strings.HasPrefix(right, raw)
		}
		span := proseNumber.FindStringIndex(right)
		if span == nil || span[0] != 0 || unit == "" && span[1] < len(right) && numericASCIIWord(right[span[1]]) {
			return false
		}
		value, valid := new(big.Rat).SetString(strings.ReplaceAll(strings.ReplaceAll(right[:span[1]], ",", ""), "−", "-"))
		suffix := strings.TrimSpace(right[span[1]:])
		if unit != "" {
			remainder, found := strings.CutPrefix(suffix, unit)
			if !found {
				return false
			}
			suffix = strings.TrimSpace(strings.TrimLeft(remainder, "*_`"))
			if strings.HasPrefix(suffix, ".") && len(suffix) > 1 {
				return false
			}
		}
		endsValue := suffix == "" || strings.ContainsRune(".,，。；;（()）|", []rune(suffix)[0]) || suffix == "on"
		return valid && original.Cmp(value) == 0 && endsValue
	}
	masked := cellOrListMarker.ReplaceAllStringFunc(maskValidatedCalendarDates(statement), numericMask)
	if item.Subject != "" && item.Cell != "" {
		// The compact display lists an exact field dimension immediately
		// before its observation, with the receipt's own cell in parentheses.
		if _, after, found := strings.Cut(statement, item.Subject+" "); found {
			if claim, _, closed := strings.Cut(after, ")"); closed && terminalContainsLabel(claim, item.Cell) && match(claim) {
				return true
			}
		}
	}
	if strings.HasPrefix(strings.TrimSpace(statement), "|") && (cited || terminalContainsLabel(statement, item.Cell)) {
		numbers, observed := 0, false
		for _, cell := range strings.Split(masked, "|") {
			cell = strings.Trim(strings.TrimSpace(cell), "*_`")
			if span := proseNumber.FindStringIndex(cell); span != nil && span[0] == 0 {
				numbers++
			}
			observed = observed || match(cell)
		}
		return numbers == 1 && observed
	}
	for _, assignment := range terminalAssignment.FindAllStringIndex(masked, -1) {
		left := strings.TrimRight(strings.TrimSpace(statement[:assignment[0]]), "」”\"*` ")
		hasLabel := item.Subject != "" && strings.HasSuffix(left, item.Subject) ||
			item.Subject == "" && item.Metric != "" && strings.HasSuffix(left, item.Metric)
		if !cited && !hasLabel && !terminalContainsLabel(statement, item.Cell) {
			continue
		}
		if match(masked[assignment[1]:]) {
			return true
		}
	}
	return false
}

type terminalCoverageInsertion struct {
	anchor int
	fact   terminalCoverageFact
}

func terminalGroupable(item, peer coverageEvidence) bool {
	return item.Kind == peer.Kind && item.Subject != "" && peer.Subject != "" &&
		terminalSameImport(item, peer) && item.Sheet == peer.Sheet && item.Metric == peer.Metric &&
		(item.Kind == "gap" && peer.PeriodFrom == item.PeriodFrom && peer.PeriodTo == item.PeriodTo && peer.Period == item.Period ||
			item.Kind == "last_valid" && peer.Raw != nil && item.Raw != nil && peer.Date == item.Date)
}

func (d terminalCoverageDelivery) insertCheckedFacts(paragraphs []string, insertions []terminalCoverageInsertion, lines []string) string {
	at := make([][]string, len(paragraphs))
	keepPrefix := len(paragraphs) == 1 && !slices.ContainsFunc(insertions, func(item terminalCoverageInsertion) bool {
		return item.anchor >= 0
	})
	for len(insertions) > 0 {
		first := insertions[0]
		item := first.fact.Evidence
		insertions = insertions[1:]
		group := []terminalCoverageFact{first.fact}
		if item.Subject != "" && (item.Kind == "gap" || item.Raw != nil) {
			insertions = slices.DeleteFunc(insertions, func(other terminalCoverageInsertion) bool {
				if other.anchor != first.anchor || !terminalGroupable(item, other.fact.Evidence) {
					return false
				}
				group = append(group, other.fact)
				return true
			})
		}
		index := first.anchor
		if index < 0 {
			index = len(paragraphs) - 1
		}
		if line := d.checkedFactSentence(group); line != "" && !slices.Contains(at[index], line) {
			at[index] = append(at[index], line)
		}
	}
	var output []string
	for i, paragraph := range paragraphs {
		if keepPrefix && paragraph != "" {
			output = append(output, paragraph)
			paragraph = ""
		}
		if i == len(paragraphs)-1 {
			output = append(output, at[i]...)
			output = append(output, lines...)
		} else if len(at[i]) != 0 {
			if paragraph == "" || terminalTableRow.MatchString(paragraph) {
				output = append(output, at[i]...)
			} else {
				separator := " "
				if trimmed := strings.TrimSpace(paragraph); strings.TrimRight(trimmed, ".!?。！？") == trimmed {
					separator = "\n"
				}
				paragraph += separator + strings.Join(at[i], " ")
			}
		}
		if paragraph != "" {
			output = append(output, paragraph)
		}
	}
	return strings.Join(output, "\n\n")
}

func (d terminalCoverageDelivery) checkedFactSentence(facts []terminalCoverageFact) string {
	fact := facts[0]
	item := fact.Evidence
	if fact.Role == "scoped_value" {
		return terminalCoverageFactText(fact, d.chinese)
	}
	if item.SourceTitle == "" || item.Metric == "" {
		return terminalCoverageFactText(fact, d.chinese)
	}
	source, field := d.checkedFactSource(item), terminalEvidenceText(item.Metric)
	if item.Subject != "" && len(facts) == 1 {
		field += "（" + terminalEvidenceText(item.Subject) + "）"
	}
	field = fmt.Sprintf(d.text("「%s」", "“%s”"), field)
	if item.Kind == "gap" && item.Complete && coverageExactRange(item) {
		period := terminalEvidenceText(terminalCoveragePeriod(item.PeriodFrom, item.PeriodTo, item.Period))
		if len(facts) > 1 {
			var subjects []string
			for _, gap := range facts {
				subjects = append(subjects, terminalEvidenceText(gap.Evidence.Subject))
			}
			field += d.text("（"+strings.Join(subjects, "、")+"）", " ("+strings.Join(subjects, ", ")+")")
		}
		gap := d.text("%s中，%s 在 %s 没有有效记录。", "%s has no valid records for %s in %s.")
		if d.chinese && item.Operation == "query_series" {
			gap = "%s中，%s 在 %s 没有有效观测值。"
		}
		return fmt.Sprintf(gap, source, field, period)
	}
	if item.Kind == "last_valid" && item.Raw == nil && coverageDay(item.Date) != "" {
		return fmt.Sprintf(d.text("%s中，%s 的最后有效日期是 %s。本轮尚未取得该日原值。", "%s records the latest valid date for %s as %s. This read verified the date."),
			source, field, terminalEvidenceText(item.Date))
	}
	if item.Date == "" || item.Raw == nil {
		return terminalCoverageFactText(fact, d.chinese)
	}
	var values []string
	sharedUnitUnknown := len(facts) > 1 && !slices.ContainsFunc(facts, func(fact terminalCoverageFact) bool {
		return terminalUnitFromSource(fact.Evidence) || fact.requestedUnit != ""
	})
	for _, latest := range facts {
		evidence := latest.Evidence
		raw, ok := reviewScalarWitnessQuote(evidence.Raw)
		if !ok {
			continue
		}
		var details []string
		if len(facts) == 1 && evidence.DateCell != "" {
			details = append(details, d.text("日期格 ", "date cell ")+terminalEvidenceText(evidence.DateCell))
		} else if evidence.DateCell != "" {
			details = append(details, terminalEvidenceText(evidence.DateCell))
		}
		if len(facts) == 1 && evidence.Cell != "" {
			details = append(details, d.text("取值格 ", "value cell ")+terminalEvidenceText(evidence.Cell))
		} else if evidence.Cell != "" {
			details = append(details, terminalEvidenceText(evidence.Cell))
		}
		value := terminalCoverageValueDetails(latest, d.chinese, terminalEvidenceText(raw), details, sharedUnitUnknown)
		if len(facts) > 1 {
			value = terminalEvidenceText(evidence.Subject) + " " + value
		}
		values = append(values, value)
	}
	if len(values) == 0 {
		return terminalCoverageFactText(fact, d.chinese)
	}
	tail := strings.Join(values, d.text("，", ", "))
	if sharedUnitUnknown {
		tail += d.text("，原表单位未确认", ", with units unconfirmed")
	}
	return fmt.Sprintf(d.text("%s中，%s 最后有值的日期为 %s，原值为 %s。", "%s records the latest valid values for %s on %s as %s."),
		source, field, terminalEvidenceText(item.Date), tail)
}

func (d terminalCoverageDelivery) checkedFactSource(item coverageEvidence) string {
	source := fmt.Sprintf(d.text("《%s》", "%s"), terminalEvidenceText(item.SourceTitle))
	if item.Sheet != "" {
		source += fmt.Sprintf(d.text("的「%s」表", ", sheet “%s”"), terminalEvidenceText(item.Sheet))
	}
	return source
}

func terminalCoverageFactPresent(candidate string, fact terminalCoverageFact) bool {
	item := fact.Evidence
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
		if fact.Role == "supplement" || fact.Role == "supplement_context" {
			var cited string
			if fact.includeCitation && item.CitationID != "" && item.CitationID == item.ChunkID && item.KnowledgeBaseID != "" {
				cited = fmt.Sprintf(`<kb doc="%s" chunk_id="%s" kb_id="%s" />`, html.EscapeString(item.SourceTitle),
					html.EscapeString(item.ChunkID), html.EscapeString(item.KnowledgeBaseID))
			}
			if item.ReportedValue != "" && item.Date != "" && firstNonEmptyAgent(item.Subject, item.Metric) != "" {
				measure := terminalEvidenceText(strings.TrimSpace(item.Subject + " " + item.Metric))
				value := terminalCoverageValueDetails(fact, chinese, terminalEvidenceText(item.ReportedValue), nil, false)
				raw = d.text(measure+"为 "+value, measure+" is "+value)
			}
			if fact.adopted {
				return fmt.Sprintf(d.text("本次补充采用 %s，按该材料的披露口径记录为“%s”%s。", "This answer uses %s as a supplement. It reports “%s” in its own reporting scope%s."), title, raw, cited)
			}
			return fmt.Sprintf(d.text("补充材料 %s 按其披露口径记载“%s”%s。", "Supplemental material %s reports “%s” in its own reporting scope%s."), title, raw, cited)
		}
		return fmt.Sprintf(d.text("%s 的原文为“%s”。", "%s reports “%s”."), title, raw)
	}
	raw = terminalCoverageValueDetails(fact, chinese, raw, details, false)
	if fact.Role == "scoped_value" && item.SourceDomain == "structured" && item.SourceTitle != "" && item.Metric != "" {
		return fmt.Sprintf(d.text("%s中，%s的原值为 %s。", "%s records %s as %s."), d.checkedFactSource(item), terminalEvidenceText(item.Metric), raw)
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
	return fmt.Sprintf(d.text("%s，%s %s。", "%s. %s %s."), label, descriptor, raw)
}

func terminalCoverageValueDetails(fact terminalCoverageFact, chinese bool, raw string, details []string, unitAlreadyStated bool) string {
	item := fact.Evidence
	d := terminalCoverageDelivery{chinese: chinese}
	switch {
	case terminalUnitFromSource(item):
		raw += " " + terminalEvidenceText(item.Unit)
		if fact.requestedUnit != "" && item.Unit != fact.requestedUnit {
			details = append(details, d.text("按来源单位保留原值", "original source unit"))
		}
	case fact.requestedUnit != "" && terminalUnitUnspecified(item):
		raw += " " + terminalEvidenceText(fact.requestedUnit)
	case !unitAlreadyStated && (item.Kind == "cell" || item.Kind == "last_valid"):
		details = append(details, d.text("单位未确认", "unit unconfirmed"))
	}
	if len(details) > 0 {
		raw += " (" + strings.Join(details, ", ") + ")"
	}
	return raw
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
