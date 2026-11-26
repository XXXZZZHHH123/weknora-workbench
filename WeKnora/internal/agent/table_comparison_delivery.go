package agent

import (
	"context"
	"fmt"
	"html"
	"slices"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

type tableComparisonDeliveryReceipt struct {
	answerHash, revision string
}

func (e *AgentEngine) tableComparisonDeliveryRevision(ctx context.Context, state *types.AgentState) string {
	scope := e.unverifiedSourceScopeDigest(ctx)
	task := e.candidateTaskDigest(e.turnReview.Query, e.turnReview.Conversation)
	targets := e.evidenceCompletionTargets()
	record := e.currentTableComparisonRecord(state, targets, task, scope)
	var results []coverageCompletion
	for _, target := range targets {
		if tableComparison(target) {
			results = append(results, evaluateTableComparison(target, record))
		}
	}
	return reviewDigest([]any{task, scope, targets, results})
}

func (e *AgentEngine) currentTableComparisonRecord(state *types.AgentState, targets []taskCoverageTarget, task, scope string) coverageRecord {
	turn := &e.evidenceCompletion
	record, _ := e.projectCurrentTaskCoverage(state, turn.startStep, coverageRecord{Revision: task, AuthScope: scope, Bindings: turn.record.Bindings})
	record.Evidence = slices.DeleteFunc(record.Evidence, func(item coverageEvidence) bool {
		return !e.terminalCoverageScopeAllowed(targets, item) ||
			!e.coverageScopeAllows(item.KnowledgeBaseID, firstNonEmptyAgent(item.KnowledgeID, item.SourceID))
	})
	return record
}

func (e *AgentEngine) tableComparisonDeliveryCurrent(ctx context.Context, state *types.AgentState) bool {
	receipt := e.turnReview.TableComparisonDelivery
	return receipt.answerHash != "" && receipt.answerHash == candidateTextDigest(state.FinalAnswer) &&
		receipt.revision == e.tableComparisonDeliveryRevision(ctx, state)
}

// Re-read the actual per-turn results at the terminal boundary. A candidate
// that cites a different table or period cannot stand in for either side.
func (e *AgentEngine) terminalTableComparison(ctx context.Context, state *types.AgentState) (answer string, complete, handled bool) {
	if !e.evidenceCompletionEnabled() || !e.evidenceCompletionEligible() || state == nil || e.holdCollaborationAnswer ||
		!e.standardTextDelivery(state) {
		return "", false, false
	}
	var targets []taskCoverageTarget
	for _, target := range e.evidenceCompletionTargets() {
		if target.SourceTime == "general_concept" || len(target.Required) == 1 && target.Required[0] == "optional" {
			continue
		}
		if !tableComparison(target) {
			return "", false, false
		}
		targets = append(targets, target)
	}
	if len(targets) == 0 {
		return "", false, false
	}
	record := e.currentTableComparisonRecord(state, targets, e.evidenceCompletion.revision, e.unverifiedSourceScopeDigest(ctx))
	d := terminalCoverageDelivery{chinese: strings.HasPrefix(types.LanguageFromContextOrDefault(ctx), "zh")}
	complete = true
	var paragraphs []string
	for _, target := range targets {
		result := evaluateTableComparison(target, record)
		main := slices.DeleteFunc(slices.Clone(result.PinnedFacts), func(fact coverageFact) bool { return fact.Role != "scoped_value" })
		conclusion := terminalComparisonConclusion(main, d.chinese)
		if result.State != coverageSatisfied || conclusion == "" {
			complete = false
			missing := strings.Join(result.OpenItems, d.text("、", ", "))
			conclusion = fmt.Sprintf(d.text("《%s》所问期间的比较尚未核实，仍缺 %s 的同表同期读数。", "The requested comparison for %s is unverified. The matching table value is still needed from %s."), terminalEvidenceText(target.Table), terminalEvidenceText(missing))
		}
		paragraphs = append(paragraphs, conclusion)
		for _, fact := range main {
			text := e.terminalTableComparisonFact(fact.Evidence, d)
			var details []string
			for _, support := range result.PinnedFacts {
				item := support.Evidence
				if support.Role != "supporting" || !sameTableComparisonRow(fact.Evidence, item) {
					continue
				}
				label := terminalEvidenceText(item.Metric) + " **" + fmt.Sprint(item.Raw) + "**"
				if terminalUnitFromSource(item) {
					label += " " + terminalEvidenceText(item.Unit)
				}
				if item.Cell != "" {
					label += " (" + terminalEvidenceText(item.Cell) + ")"
				}
				if item.CitationID != fact.Evidence.CitationID {
					label += e.terminalComparisonCitation(item)
				}
				details = append(details, label)
			}
			if len(details) != 0 {
				text += d.text(" 同一行还列出 ", " The same row also reports ") + strings.Join(details, d.text("、", ", ")) + d.text("。", ".")
			}
			paragraphs = append(paragraphs, text)
		}
	}
	return strings.Join(paragraphs, "\n\n"), complete, true
}

func (e *AgentEngine) terminalTableComparisonFact(item coverageEvidence, d terminalCoverageDelivery) string {
	location := fmt.Sprintf(d.text("第%d页", "page %d"), item.Page)
	if item.Cell != "" {
		location = fmt.Sprintf(d.text("单元格 %s", "cell %s"), terminalEvidenceText(item.Cell))
	}
	if item.SourceDate != "" && item.SourceDate != item.Date {
		location += fmt.Sprintf(d.text("，材料日期 %s", ", source date %s"), terminalEvidenceText(item.SourceDate))
	}
	value := fmt.Sprint(item.Raw)
	if terminalUnitFromSource(item) {
		value += " " + terminalEvidenceText(item.Unit)
	}
	source := terminalEvidenceText(item.SourceTitle)
	text := fmt.Sprintf("%s 的《%s》（%s）在 %s 列示的%s为 **%s**。",
		source, terminalEvidenceText(item.Sheet), location, terminalEvidenceText(item.Date), terminalEvidenceText(item.Metric), value)
	if !d.chinese {
		text = fmt.Sprintf("%s, %s (%s), reports **%s** for %s in %s.", source, terminalEvidenceText(item.Sheet), location, value, terminalEvidenceText(item.Metric), terminalEvidenceText(item.Date))
	}
	text += e.terminalComparisonCitation(item)
	if !terminalUnitFromSource(item) {
		text += " " + d.text("来源单位未确认。", "The source unit remains unconfirmed.")
	}
	return text
}

func (e *AgentEngine) terminalComparisonCitation(item coverageEvidence) string {
	if !e.config.CitationsEnabled() || item.CitationID == "" {
		return ""
	}
	if item.Kind == "body" {
		return fmt.Sprintf(`<kb doc="%s" chunk_id="%s" kb_id="%s" />`, html.EscapeString(item.SourceTitle), html.EscapeString(item.CitationID), html.EscapeString(item.KnowledgeBaseID))
	}
	return fmt.Sprintf(`<evidence evidence_id="%s" />`, html.EscapeString(item.CitationID))
}
