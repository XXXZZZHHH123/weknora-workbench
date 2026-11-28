package agent

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
)

func (e *AgentEngine) finalPointSupplementReadAllowed(ctx context.Context, gap finalSupplementGap, item coverageEvidence) bool {
	selected, ok := e.turnReview.FinalPointSupplementReads[item.ToolCallID]
	return ok && selected.gap == gap && selected.documentID == item.SourceID &&
		item.SourceDomain == "knowledge" && item.Kind == "body" && item.Operation == agenttools.ToolReadDocument &&
		item.KnowledgeBaseID == gap.library && e.restrictedCoverageSourceAuthorized(ctx, item)
}

func (e *AgentEngine) scopedPointSupplementFacts(ctx context.Context, gap finalSupplementGap, record coverageRecord, attempts []coverageAttempt) []coverageEvidence {
	var facts []coverageEvidence
	for _, item := range record.Evidence {
		if item.SourceDomain != "knowledge" || item.Kind != "body" || item.Operation != agenttools.ToolReadDocument ||
			item.SourceTitle == "" || item.ChunkID == "" || item.KnowledgeBaseID != gap.library || item.SourceID == gap.primaryDocument ||
			!e.restrictedCoverageSourceAuthorized(ctx, item) {
			continue
		}
		body, _ := item.Raw.(string)
		passage := labeledSourcePassage(body, gap.metric)
		if passage == "" {
			continue
		}
		sourceDay := supplementSourceDay(item, attempts, gap.period)
		if sourceDay != gap.period && !coverageTextHasDate(passage, gap.period) {
			continue
		}
		item.Metric, item.SourceDate, item.Raw = gap.metric, sourceDay, passage
		facts = append(facts, item)
	}
	return facts
}

func supplementSourceDay(item coverageEvidence, attempts []coverageAttempt, day string) string {
	if item.SourceDate != "" {
		return item.SourceDate
	}
	for _, attempt := range attempts {
		if attempt.ToolCallID == item.ToolCallID && attempt.Success && attempt.result != nil {
			if agenttools.DocumentDateEvidenceFromDirectory(reviewAsMap(attempt.result.Data["document"]), day) != nil {
				return day
			}
			break
		}
	}
	return ""
}

func (e *AgentEngine) finalPointSupplementFacts(ctx context.Context, target taskCoverageTarget, record coverageRecord, attempts []coverageAttempt) []coverageEvidence {
	if target.SourceMode != "named_first" && target.SourceMode != "open" || target.SourceTime != "observed_period" ||
		target.VersionTask == "compare_sources" || target.VersionTask == "compare_versions" || target.Metric == "" {
		return nil
	}
	from, to, grain := coverageTargetDateRange(target)
	if grain != "day" || from != to || !coverageAnalysisDay(from) {
		return nil
	}
	var gaps []coverageEvidence
	for _, item := range record.Evidence {
		if item.SourceDomain == "structured" && item.Kind == "gap" && item.Complete && item.PeriodFrom == from && item.PeriodTo == to &&
			(target.Table == "" || item.Sheet == target.Table) {
			gaps = append(gaps, item)
		}
	}
	if len(gaps) == 0 {
		return nil
	}
	var facts []coverageEvidence
	for _, item := range record.Evidence {
		if item.Kind != "body" || item.Operation != agenttools.ToolReadDocument || item.SourceTitle == "" || item.Page <= 0 ||
			!slices.ContainsFunc(gaps, func(gap coverageEvidence) bool {
				return item.KnowledgeBaseID == gap.KnowledgeBaseID && comparisonPhysicalSource(item) != comparisonPhysicalSource(gap)
			}) || !e.coverageEvidenceAllowed(ctx, &target, item) {
			continue
		}
		sourceDay := supplementSourceDay(item, attempts, from)
		if sourceDay != from {
			continue
		}
		body, _ := item.Raw.(string)
		if passage := labeledSourcePassage(body, target.Metric); passage != "" {
			item.Metric, item.SourceDate, item.Raw = target.Metric, sourceDay, passage
			facts = append(facts, item)
		}
	}
	return facts
}

func labeledSourcePassage(body, metric string) string {
	for _, line := range strings.Split(documentSourceProse(body), "\n") {
		label, rest, found := strings.Cut(strings.Replace(line, "：", ":", 1), ":")
		if !found || strings.Trim(strings.TrimSpace(label), "-#* ") != metric {
			continue
		}
		if proseNumber.MatchString(maskValidatedCalendarDates(rest)) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// A new draft can omit a separately attributed disclosure that the model
// already selected from a current document read. Keep that disclosure after
// the primary result whenever the current task permits supplemental sources.
func (e *AgentEngine) retainReadSupplement(ctx context.Context, state *types.AgentState, answer string) string {
	if !relaxedTerminalEvidenceEnabled() || state == nil || strings.TrimSpace(answer) == "" ||
		!e.evidenceCompletionEligible() || !e.standardTextDelivery(state) || e.coverageOnlyDomain() != "" ||
		slices.ContainsFunc(e.evidenceCompletionTargets(), func(target taskCoverageTarget) bool {
			return target.SourceMode == "only"
		}) {
		return answer
	}
	primary := make(map[[2]string]bool)
	record, _ := e.terminalCoverageRecord(ctx, state)
	for _, item := range record.Evidence {
		if item.SourceDomain == "structured" && item.Kind == "gap" && item.Complete && coverageExactRange(item) {
			primary[comparisonPhysicalSource(item)] = true
		}
	}
	if len(primary) == 0 || !slices.ContainsFunc(record.Evidence, func(item coverageEvidence) bool {
		return item.Kind == "body" && item.SourceDomain == "knowledge" && !primary[comparisonPhysicalSource(item)]
	}) {
		return answer
	}

	delivery := terminalCoverageDelivery{record: record, chinese: strings.HasPrefix(types.LanguageFromContextOrDefault(ctx), "zh")}
	retained, selected := e.retainReadingParagraphs(ctx, state, delivery, answer, e.turnReview.Last.Candidate, primary)
	if selected {
		return retained
	}
	for _, step := range slices.Backward(state.RoundSteps[min(e.turnReview.TerminalStartStep, len(state.RoundSteps)):]) {
		if len(step.ToolCalls) == 0 && step.Thought != "" {
			if retained, selected := e.retainReadingParagraphs(ctx, state, delivery, answer, step.Thought, primary); selected {
				return retained
			}
		}
	}
	return answer
}

func supplementNamesSource(paragraph coverageDraftParagraph, item coverageEvidence) bool {
	return paragraph.hasLabel(item.SourceTitle) || paragraph.hasLabel(supplementSourceName(item))
}

func supplementSourceName(item coverageEvidence) string {
	return strings.TrimSuffix(item.SourceTitle, filepath.Ext(item.SourceTitle))
}

func supplementReadingPresent(answer string, prior coverageDraftParagraph, readings []coverageEvidence) bool {
	for _, block := range strings.Split(answer, "\n\n") {
		current := newCoverageDraftParagraph(block)
		for _, item := range readings {
			if !supplementNamesSource(current, item) && !slices.Contains(current.chunks, item.ChunkID) ||
				!coverageDraftReportsCitedQuantity(current, item) {
				continue
			}
			item.Raw = prior.text
			if coverageDraftReportsCitedQuantity(current, item) {
				return true
			}
		}
	}
	return false
}
