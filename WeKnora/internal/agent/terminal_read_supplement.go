package agent

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

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
