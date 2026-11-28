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
	"github.com/Tencent/WeKnora/internal/common"
	"github.com/Tencent/WeKnora/internal/types"
)

type terminalCoverageFact struct {
	coverageFact
	required bool
}

type terminalCoverageDelivery struct {
	facts            []terminalCoverageFact
	notes            []string
	missing          []string
	conclusions      []string
	hasDirectory     bool
	subject          string
	record           coverageRecord
	readReceipts     []coverageEvidence
	comparisonValues map[string]string
	comparisonGap    string
	comparisonUnit   string
	requested        []taskCoverageTarget
	chinese          bool
	relaxed          bool
	finalRead        bool
	substantive      bool
	lastValidPending bool
	attemptCount     int
}

type terminalCoveragePublication struct {
	answer   string
	notice   string
	partial  bool
	complete bool
}

type terminalReadDraft struct {
	text        string
	substantive bool
}

type terminalCoverageOptions struct {
	finalRead            bool
	useDirectoryIdentity bool
}

type finalReadPublication struct {
	terminalCoveragePublication
	fromEvidence     bool
	lastValidPending bool
}

func (p finalReadPublication) recordIncompleteEndpoint(answer string, receipt *terminalPublicationReceipt) bool {
	if !p.lastValidPending || terminalCandidateFallback(answer) {
		return false
	}
	receipt.notice, receipt.partial, receipt.partialReason = answer, true, terminalEndpointEvidence
	return true
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
func (e *AgentEngine) terminalCoverageRecord(ctx context.Context, state *types.AgentState, finalRead ...bool) (coverageRecord, []coverageAttempt) {
	return e.projectTerminalCoverage(ctx, state, terminalCoverageOptions{finalRead: len(finalRead) != 0 && finalRead[0]})
}

func (e *AgentEngine) projectTerminalCoverage(ctx context.Context, state *types.AgentState, options terminalCoverageOptions) (coverageRecord, []coverageAttempt) {
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
	record, attempts := e.projectCurrentTaskCoverage(state, e.turnReview.TerminalStartStep, record, options.finalRead)
	// Compute the current workbook relationship on the intact projection.
	// Deleting first would lose the receipt needed to verify some endpoints.
	var current []coverageEvidence
	for _, item := range record.Evidence {
		if options.useDirectoryIdentity && item.Kind == "directory" && item.Operation == "list_series" && item.SnapshotHash == "" {
			item.SnapshotHash, item.SourceTitle = terminalDirectoryWorkbook(record, item)
		}
		if item.Bound && coverageCurrentEvidence(record, item) && e.coverageEvidenceAllowed(ctx, nil, item) {
			current = append(current, item)
		}
	}
	if scopedReadRecoveryEnabled() && len(e.turnReview.FinalPointSupplementReads) != 0 && options.finalRead {
		scoped := record
		scoped.Evidence = current
		if gap, ok := e.finalSupplementPointGap(ctx, scoped); ok {
			for _, item := range record.Evidence {
				if item.Bound && coverageCurrentEvidence(record, item) && !e.coverageEvidenceAllowed(ctx, nil, item) &&
					e.finalPointSupplementReadAllowed(ctx, gap, item) {
					current = append(current, item)
				}
			}
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
	result := e.terminalCoveragePublication(ctx, state, failure)
	return firstNonEmptyAgent(result.answer, result.notice)
}

func (e *AgentEngine) terminalCoveragePublication(ctx context.Context, state *types.AgentState, failure terminalFailure) terminalCoveragePublication {
	delivery := e.terminalCoverageDelivery(ctx, state)

	steps := []types.AgentStep(nil)
	if state != nil {
		steps = state.RoundSteps[min(e.turnReview.TerminalStartStep, len(state.RoundSteps)):]
	}
	if failure == terminalTableEvidence && !delivery.relaxed {
		delivery.facts = slices.DeleteFunc(delivery.facts, func(fact terminalCoverageFact) bool {
			return !fact.required && fact.Evidence.Kind == "body"
		})
		reason := "current_selected_roles"
		if !slices.ContainsFunc(delivery.facts, func(fact terminalCoverageFact) bool { return fact.required }) {
			reason = delivery.retainCitedDraftReadings(e.turnReview.Last.Candidate)
		}
		common.PipelineInfo(ctx, "Agent", "terminal_table_evidence", map[string]any{"reason": reason, "verified_facts": len(delivery.facts)})
	}
	if failure == terminalEndpointEvidence {
		delivery.facts = slices.DeleteFunc(delivery.facts, func(fact terminalCoverageFact) bool {
			return fact.Role == "source_passage" && !fact.required
		})
		delivery.retainSelectedInspectedEndpoints(steps)
		delivery.pairInspectedScope()
	}
	if len(e.evidenceCompletionTargets()) == 0 {
		delivery.retainSelectedUnplannedReadings(steps, e.turnReview.Last.Candidate)
	}
	result := delivery.renderPublication(failure)
	if delivery.relaxed && failure != terminalSourceChanged && failure != terminalInterrupted && result.answer != "" &&
		len(delivery.requested) == 1 && len(delivery.conclusions) > 0 && len(delivery.missing) == 0 {
		result.complete = true
	}
	if result.answer == "" && types.VerifiedPublicationEnabled() && e.evidenceCompletionEligible() &&
		e.standardTextDelivery(state) && !delivery.relaxed && failure != terminalInterrupted && failure != terminalSourceChanged {
		if answer := delivery.verifiedSourceStatus(); answer != "" {
			result = terminalCoveragePublication{answer: answer, notice: answer, partial: true}
		}
	}
	if result.answer != "" && failure != terminalTableEvidence && failure != terminalSourceChanged && state != nil {
		result.answer = e.retainCheckedDocumentParagraphs(ctx, state, delivery, result.answer)
	}
	if result.notice != "" && !e.evidenceCompletionEligible() {
		// Keep the negotiated response protocol for other agent workflows.
		result = terminalCoveragePublication{answer: result.notice}
	}
	return result
}

func (e *AgentEngine) selectFinalReadPublication(ctx context.Context, state *types.AgentState, answer string, failure terminalFailure, outcome string) finalReadPublication {
	draft := answer
	if failure == terminalSourceChanged {
		draft = ""
	} else if terminalCandidateFallback(draft) {
		draft = e.turnReview.Last.Candidate
	}
	delivery := e.terminalCoverageDelivery(ctx, state, terminalReadDraft{
		text: draft, substantive: substantiveFinalAnswerEnabled() && failure != terminalSourceChanged,
	})
	lastValidPending := delivery.lastValidPending && failure != terminalSourceChanged && failure != terminalInterrupted
	steps := state.RoundSteps[min(e.turnReview.TerminalStartStep, len(state.RoundSteps)):]
	if failure != terminalSourceChanged {
		if len(delivery.requested) == 0 {
			delivery.retainSelectedUnplannedReadings(steps, firstNonEmptyAgent(draft, e.turnReview.Last.Candidate))
		}
		delivery.retainSelectedInspectedEndpoints(steps)
	}
	delivery.pairInspectedScope()
	usableDraft := failure == terminalNoFailure && !terminalCandidateFallback(types.StripAgentAnswerMarkup(answer))
	partialDraft := outcome == "evidence_partial" || outcome == "partial"
	checked := e.turnReview.Last
	if hash := candidateTextDigest(answer); checked.CandidateHash != hash && e.turnReview.CoverageDraftDeliveryHash == hash {
		checked = e.turnReview.CoverageDraft
	}
	keepDraft := usableDraft
	if keepDraft && len(delivery.conclusions) > 0 {
		// A pair of displayed numbers doesn't establish that the draft drew
		// the checked relation. Reuse an existing semantic comparison review
		// or a previously rendered relation before leaving the prose intact.
		reviewed := checked.CandidateHash == candidateTextDigest(answer) &&
			(checked.SourceReviewAttempted && checked.Disposition == candidateAccepted ||
				checked.StructuredComparisonReview && (checked.StructuredResultReviewStatus == "kept" || checked.StructuredResultReviewStatus == "corrected"))
		keepDraft = reviewed || !slices.ContainsFunc(delivery.conclusions, func(conclusion string) bool {
			return !strings.Contains(answer, conclusion)
		})
	}
	retainsReading := false
	appendToDraft := false
	if keepDraft {
		var paragraphs []coverageDraftParagraph
		for _, block := range strings.Split(strings.ReplaceAll(answer, "\r\n", "\n"), "\n\n") {
			paragraphs = append(paragraphs, newCoverageDraftParagraph(block))
		}
		missing := slices.DeleteFunc(slices.Clone(delivery.facts), func(fact terminalCoverageFact) bool {
			if fact.Evidence.Kind == "gap" || fact.Evidence.Kind == "last_valid_receipt" ||
				fact.Evidence.Kind == "body" && fact.Evidence.ReportedValue == "" && fact.Role != "attributed_supplement" {
				return true
			}
			shown := draftDisplaysFinalReading(paragraphs, delivery.record, fact)
			retainsReading = retainsReading || shown
			return shown
		})
		if retainsReading && !partialDraft {
			// A tool may return a wider row for context. A useful draft already
			// anchored to the actual readings keeps its selected fields.
			missing = slices.DeleteFunc(missing, func(fact terminalCoverageFact) bool {
				item := fact.Evidence
				return !fact.required && !slices.ContainsFunc(delivery.requested, func(target taskCoverageTarget) bool {
					return item.Metric != "" && target.Metric == item.Metric
				}) && !slices.ContainsFunc(paragraphs, func(paragraph coverageDraftParagraph) bool {
					return paragraph.hasField(item) || paragraph.hasLabel(item.Cell)
				})
			})
		}
		// A known missing fact can supersede a draft's account of what was
		// read, even when the draft still contains correct values. Render
		// the current facts together rather than carry over that account.
		missingRequired := slices.ContainsFunc(missing, func(fact terminalCoverageFact) bool { return fact.required })
		appendToDraft = retainsReading && !partialDraft && !missingRequired
		if appendToDraft || len(missing) == 0 && (retainsReading || !partialDraft) {
			delivery.facts = missing
		}
		if len(delivery.facts) == 0 {
			delivery.conclusions, delivery.comparisonUnit = nil, ""
		}
	}
	result := delivery.renderPublication(failure)
	if result.answer != "" {
		if appendToDraft {
			if draft := delivery.withoutGeneratedGap(answer); draft != "" {
				result.answer = draft + "\n\n" + result.answer
			}
		}
		if failure != terminalSourceChanged {
			result.answer = e.retainReadSupplement(ctx, state, result.answer)
		}
		result.complete = len(delivery.conclusions) > 0 && len(delivery.missing) == 0 && !lastValidPending
		return finalReadPublication{result, true, lastValidPending}
	}
	keepDraft = keepDraft && (retainsReading || !partialDraft || e.candidateApprovalCurrent(answer, state))
	planned := coverageDraftReview{targets: delivery.requested}
	if keepDraft && !retainsReading && planned.requiresReading() {
		review := e.currentCoverageDraftReview(ctx, state, answer)
		keepDraft = e.assessCoverageDraft(ctx, review, answer, state).verifiedReading
	}
	if keepDraft {
		if draft := delivery.withoutGeneratedGap(answer); !terminalCandidateFallback(draft) {
			if retained := e.retainReadSupplement(ctx, state, draft); retained != answer {
				return finalReadPublication{terminalCoveragePublication{answer: retained}, true, lastValidPending}
			}
			return finalReadPublication{lastValidPending: lastValidPending}
		}
	}
	notice := delivery.finalReadStatus(failure)
	if substantiveFinalAnswerEnabled() && failure == terminalSourceChanged {
		notice = delivery.changedSourceFindings()
	} else if delivery.substantive {
		notice = delivery.unreadFindings()
	}
	return finalReadPublication{terminalCoveragePublication{answer: notice, notice: notice, partial: true}, true, false}
}

// Historical terminal deliveries can carry an open item as a standalone
// paragraph. Match the whole status generated for this task, leaving source
// conclusions and other prose untouched.
func (d terminalCoverageDelivery) withoutGeneratedGap(answer string) string {
	if len(d.missing) == 0 && d.comparisonGap == "" {
		return answer
	}
	separator := "\n\n"
	if strings.Contains(answer, "\r\n\r\n") {
		separator = "\r\n\r\n"
	}
	paragraphs := strings.Split(answer, separator)
	paragraphs = slices.DeleteFunc(paragraphs, func(paragraph string) bool {
		text := strings.TrimSpace(paragraph)
		return text != "" && (text == d.comparisonGap || slices.Contains(d.missing, text))
	})
	return strings.Join(paragraphs, separator)
}

func draftDisplaysFinalReading(paragraphs []coverageDraftParagraph, record coverageRecord, fact terminalCoverageFact) bool {
	item := fact.Evidence
	if item.Kind == "last_valid" && coverageDraftDisplaysEndpoint(paragraphs, item) ||
		displaysUniqueSourceReading(paragraphs, record.Evidence, item) {
		return true
	}
	return slices.ContainsFunc(paragraphs, func(paragraph coverageDraftParagraph) bool {
		hasDate := func(text string) bool {
			return coverageTextHasDate(text, item.Date) || slices.ContainsFunc(spacedCalendarDate.FindAllStringIndex(text, -1), func(span []int) bool {
				return standaloneDateToken(text, span[0], span[1]) && coverageDay(strings.Join(strings.Fields(text[span[0]:span[1]]), "")) == item.Date
			})
		}
		if item.Extremum != "" && item.Date != "" && !hasDate(paragraph.text) {
			return false
		}
		if paragraph.displays(item, fact.Role, false) {
			return true
		}
		if item.Cell != "" && item.Date != "" && paragraph.hasLabel(item.SourceTitle) {
			if amount, ok := coverageComparisonNumber(item); ok {
				value, _ := amount.Float64()
				for _, assertion := range coverageDraftAssertionBoundary.Split(paragraph.text, -1) {
					if hasDate(assertion) &&
						slices.Contains(cellOrListMarker.FindAllString(strings.ToUpper(assertion), -1), item.Cell) &&
						coverageDraftAmountOccurrences(assertion, value) > 0 {
						return true
					}
				}
			}
		}
		if item.Kind == "body" && item.ReportedValue != "" &&
			(supplementNamesSource(paragraph, item) || slices.Contains(paragraph.chunks, item.ChunkID)) {
			if amount, ok := documentTableAmount(item.ReportedValue); ok &&
				coverageDraftAmountOccurrences(maskCitedPageLocator(paragraph.text, item.Page), amount) > 0 {
				return true
			}
		}
		if fact.Role == "attributed_supplement" && (supplementNamesSource(paragraph, item) || slices.Contains(paragraph.chunks, item.ChunkID)) {
			return coverageDraftReportsCitedQuantity(paragraph, item)
		}
		return draftAttributesComparisonReading(paragraph.text, item)
	})
}

func (d terminalCoverageDelivery) finalReadStatus(failure terminalFailure) string {
	if failure == terminalSourceChanged {
		return d.text("来源授权或快照在完成前发生变化。", "Source access or the source snapshot changed before completion.")
	}
	if len(d.notes) != 0 {
		notes := strings.Join(d.notes, " ")
		var sources []string
		for _, read := range d.readReceipts {
			location := terminalEvidenceText(read.SourceTitle)
			if read.Page > 0 {
				location += fmt.Sprintf(d.text("第 %d 页", " page %d"), read.Page)
			}
			sources = append(sources, location)
		}
		slices.Sort(sources)
		sources = slices.Compact(sources)
		if len(sources) != 0 {
			notes += fmt.Sprintf(d.text(" 已读取 %s。", " Read material: %s."), strings.Join(sources, d.text("、", ", ")))
		}
		if d.subject != "" {
			notes = fmt.Sprintf(d.text("为核查“%s”，%s", "To check “%s”, this turn ran the following source steps. %s"), d.subject, notes)
		}
		return notes + d.text(" 这些结果未给出可对应所问范围的读数。", " These results supplied no reading for the requested scope.")
	}
	statement := d.text("本轮没有完成来源读取。", "This turn did not complete a source read.")
	if d.subject != "" {
		statement = fmt.Sprintf(d.text("本轮没有完成对“%s”的来源读取。", "This turn did not complete a source read for “%s”."), d.subject)
	}
	switch failure {
	case terminalInterrupted:
		statement += d.text("执行中途已中断。", " Execution was interrupted.")
	case terminalBudget, terminalTruncated:
		statement += d.text("执行已达到运行上限。", " Execution reached its limit.")
	}
	return statement
}

func (e *AgentEngine) retainCheckedDocumentParagraphs(ctx context.Context, state *types.AgentState, delivery terminalCoverageDelivery, answer string) string {
	answer, _ = e.retainReadingParagraphs(ctx, state, delivery, answer, e.turnReview.Last.Candidate, nil)
	return answer
}

func (e *AgentEngine) retainReadingParagraphs(ctx context.Context, state *types.AgentState, delivery terminalCoverageDelivery, answer, candidate string, primary map[[2]string]bool) (string, bool) {
	if candidate == "" || !e.evidenceCompletionEligible() || !e.standardTextDelivery(state) {
		return answer, false
	}
	supplement := primary != nil
	selected := false
	blocks := strings.Split(candidate, "\n\n")
	for i, text := range blocks {
		markdownTable := strings.HasPrefix(strings.TrimSpace(text), "|")
		if i+1 < len(blocks) && markdownTable {
			locator := blocks[i+1]
			if strings.TrimSpace(documentAnswerCitation.ReplaceAllString(locator, "")) == "" {
				text += "\n\n" + locator
			}
		}
		paragraph := newCoverageDraftParagraph(text)
		if len(paragraph.results) != 0 || !supplement && (len(paragraph.chunks) == 0 || e.candidateCitationsInvalid(text, state, e.turnReview.Conversation, ctx)) {
			continue
		}
		calls := make(map[string]bool)
		var locations []string
		var readings []coverageEvidence
		for _, item := range delivery.record.Evidence {
			if item.Kind == "body" && !primary[comparisonPhysicalSource(item)] &&
				(slices.Contains(paragraph.chunks, item.ChunkID) || supplement && supplementNamesSource(paragraph, item)) &&
				coverageDraftReportsCitedQuantity(paragraph, item) {
				calls[item.ToolCallID] = true
				readings = append(readings, item)
				location := terminalEvidenceText(item.SourceTitle)
				page := item.Page
				if page == 0 {
					for _, receipt := range delivery.readReceipts {
						if receipt.SourceID == item.SourceID && receipt.KnowledgeBaseID == item.KnowledgeBaseID && receipt.Page > 0 {
							if page > 0 && receipt.Page != page {
								page = 0
								break
							}
							page = receipt.Page
						}
					}
				}
				if location != "" && page > 0 {
					location += fmt.Sprintf(delivery.text("第 %d 页", " page %d"), page)
				}
				if location != "" && !slices.Contains(locations, location) {
					locations = append(locations, location)
				}
			}
		}
		if len(calls) == 0 {
			continue
		}
		var verified []types.AgentStep
		for _, step := range state.RoundSteps {
			var selected []types.ToolCall
			for _, call := range step.ToolCalls {
				if calls[call.ID] {
					selected = append(selected, call)
				}
			}
			verified = append(verified, types.AgentStep{ToolCalls: selected})
		}
		numericText := text
		if supplement {
			for _, item := range readings {
				if name := supplementSourceName(item); name != "" {
					numericText = strings.ReplaceAll(numericText, name, numericMask(name))
				}
			}
		}
		if len(ungroundedStructuredNumberOccurrencesWithEmptyEvidence(numericText, e.answerNumericEvidence(text, verified), true, verified)) == 0 {
			selected = true
			alreadyCited := slices.ContainsFunc(paragraph.chunks, func(id string) bool { return slices.Contains(uniqueCandidateCitationIDs(answer), id) })
			if !supplement && alreadyCited || supplement && supplementReadingPresent(answer, paragraph, readings) {
				continue
			}
			if supplement {
				// The current read supplies the citation even if the older draft's
				// reference was removed or no longer has a publishable handle.
				text = strings.TrimSpace(sourceCitationTag.ReplaceAllString(stripCoverageAnswer(agenttools.StripThinkBlocks(text)), ""))
			}
			if len(locations) == 1 && (markdownTable || supplement && !slices.ContainsFunc(readings, func(item coverageEvidence) bool {
				return supplementNamesSource(paragraph, item)
			})) {
				text = fmt.Sprintf(delivery.text("补充材料：%s。", "Supplementary source: %s."), locations[0]) + "\n\n" + text
			}
			answer += "\n\n" + text
		}
	}
	return answer, selected
}

func (d *terminalCoverageDelivery) retainSelectedUnplannedReadings(steps []types.AgentStep, candidate string) {
	exploratory := func(fact terminalCoverageFact) bool {
		return fact.Role == "scoped_value" && fact.Evidence.Kind == "cell" && !fact.required
	}
	var readings []terminalCoverageFact
	for _, fact := range d.facts {
		if exploratory(fact) {
			readings = append(readings, fact)
		}
	}
	if len(readings) < 2 {
		return
	}
	first := readings[0].Evidence
	if first.SeriesID != "" && !slices.ContainsFunc(readings, func(fact terminalCoverageFact) bool {
		item := fact.Evidence
		return item.ToolCallID != first.ToolCallID || item.SnapshotHash != first.SnapshotHash || item.SourceID != first.SourceID ||
			item.Sheet != first.Sheet || item.SeriesID != first.SeriesID || item.Date != first.Date
	}) {
		return // Preserve competing original rows from one selected field and date.
	}
	if first.Operation == "analyze_table" && first.DateCell != "" && !slices.ContainsFunc(readings, func(fact terminalCoverageFact) bool {
		item := fact.Evidence
		return item.ToolCallID != first.ToolCallID || item.DateCell != first.DateCell || item.Date != first.Date ||
			!item.WorkbookBound || item.ResultJSONPointer == ""
	}) {
		return // The original executed selection is one verified SQL result row.
	}
	if d.relaxed && len(d.readReceipts) == 0 && coverageExactRange(first) &&
		!slices.ContainsFunc(readings, func(fact terminalCoverageFact) bool {
			item := fact.Evidence
			if item.Metric == "" || item.SeriesID == "" || !item.Complete ||
				comparisonPhysicalSource(item) != comparisonPhysicalSource(first) || item.Sheet != first.Sheet ||
				!coverageSameEndpointSnapshot(item, first) || item.PeriodFrom != first.PeriodFrom || item.PeriodTo != first.PeriodTo {
				return true
			}
			if first.Extremum != "" {
				return item.SeriesID != first.SeriesID || item.Extremum == ""
			}
			_, firstRow := coverageCellPosition(first.Cell)
			_, row := coverageCellPosition(item.Cell)
			return item.Extremum != "" || !item.RangeComplete || firstRow == "" || row != firstRow || item.Date != first.Date
		}) {
		// These are the original values returned by the model's field reads
		// at one source row, or its extrema of one field in the same scope.
		return
	}
	attributed := func(draft string, item coverageEvidence) bool {
		if draftAttributesComparisonReading(draft, item) {
			return true
		}
		if !d.relaxed || draft == "" {
			return false
		}
		blocks := strings.Split(strings.ReplaceAll(draft, "\r\n", "\n"), "\n\n")
		paragraphs := make([]coverageDraftParagraph, 0, len(blocks))
		for _, block := range blocks {
			paragraphs = append(paragraphs, newCoverageDraftParagraph(block))
		}
		return displaysUniqueSourceReading(paragraphs, d.record.Evidence, item)
	}
	d.facts = slices.DeleteFunc(d.facts, func(fact terminalCoverageFact) bool {
		if !exploratory(fact) || attributed(candidate, fact.Evidence) {
			return false
		}
		return !slices.ContainsFunc(steps, func(step types.AgentStep) bool {
			return len(step.ToolCalls) == 0 && attributed(step.Thought, fact.Evidence)
		})
	})
}

func (d *terminalCoverageDelivery) retainSelectedInspectedEndpoints(steps []types.AgentStep) {
	selected := make(map[string]bool)
	for _, step := range steps {
		if len(step.ToolCalls) != 0 || step.Thought == "" {
			continue
		}
		var paragraphs []coverageDraftParagraph
		for _, block := range strings.Split(strings.ReplaceAll(step.Thought, "\r\n", "\n"), "\n\n") {
			paragraphs = append(paragraphs, newCoverageDraftParagraph(block))
		}
		for _, fact := range d.facts {
			if item := fact.Evidence; coverageInspectedEndpoint(item) && (coverageDraftDisplaysEndpoint(paragraphs, item) ||
				displaysUniqueSourceReading(paragraphs, d.record.Evidence, item)) {
				selected[terminalCoverageFactKey(item)] = true
			}
		}
	}
	if len(selected) != 0 || len(d.record.Bindings) == 0 {
		d.facts = slices.DeleteFunc(d.facts, func(fact terminalCoverageFact) bool {
			return !fact.required && coverageInspectedEndpoint(fact.Evidence) && !selected[terminalCoverageFactKey(fact.Evidence)]
		})
	}
}

func (d *terminalCoverageDelivery) pairInspectedScope() {
	for i, fact := range d.facts {
		last := fact.Evidence
		if !coverageInspectedEndpoint(last) {
			continue
		}
		var from string
		for _, lookup := range d.record.Evidence {
			if lookup.Kind != "scope_lookup" || lookup.SourceID != last.SourceID || lookup.KnowledgeBaseID != last.KnowledgeBaseID ||
				lookup.SnapshotHash != last.SnapshotHash || lookup.Sheet != last.Sheet || !coverageCurrentEvidence(d.record, lookup) {
				continue
			}
			if from != "" && from != lookup.PeriodFrom {
				from = ""
				break
			}
			from = lookup.PeriodFrom
		}
		if from == "" {
			continue
		}
		for _, field := range d.record.Evidence {
			if field.LastObservedDate == "" || field.LastObservedDate >= from || field.SourceID != last.SourceID ||
				field.KnowledgeBaseID != last.KnowledgeBaseID || field.Sheet != last.Sheet || field.SnapshotHash != last.SnapshotHash ||
				!coverageCurrentEvidence(d.record, field) {
				continue
			}
			if gap, ok := coverageInspectedBoundary(field, last, from, from, "day"); ok && sameCoverageLastValid(d.record, gap, last) {
				d.facts[i].Role = "last_valid_value"
				if !slices.ContainsFunc(d.facts, func(other terminalCoverageFact) bool { return terminalSameFieldGap(d.record, gap, other.Evidence) }) {
					d.facts = append(d.facts, terminalCoverageFact{coverageFact{"coverage_boundary", gap}, fact.required})
				}
				break
			}
		}
	}
}

// If the draft's comparison could not pass, a separately cited original
// workbook field can still identify the relevant independent reading.
// A whole-result citation alone cannot select a field in a multi-column result.
func (d *terminalCoverageDelivery) retainCitedDraftReadings(candidate string) string {
	if terminalCandidateFallback(candidate) {
		return "no_model_draft"
	}
	var paragraphs []coverageDraftParagraph
	for _, block := range strings.Split(strings.ReplaceAll(candidate, "\r\n", "\n"), "\n\n") {
		paragraphs = append(paragraphs, newCoverageDraftParagraph(block))
	}
	var selected []terminalCoverageFact
	for _, fact := range d.facts {
		item := fact.Evidence
		if item.SourceDomain != "structured" || item.CitationID == "" || item.Cell == "" ||
			!slices.ContainsFunc(paragraphs, func(paragraph coverageDraftParagraph) bool {
				return (paragraph.hasField(item) || paragraph.hasLabel(item.Cell)) && paragraph.displays(item, fact.Role, true)
			}) {
			continue
		}
		selected = append(selected, fact)
	}
	if len(selected) == 0 {
		return "no_current_cited_reading"
	}
	d.facts = selected
	return "current_cited_reading"
}

// Rank associated facts before directly verified native observations. A gap
// and its nearest value are a single finding. Unscoped exploratory rows are
// never enumerated as a substitute for the requested result.
func (e *AgentEngine) terminalCoverageDelivery(ctx context.Context, state *types.AgentState, finalDraft ...terminalReadDraft) terminalCoverageDelivery {
	d := terminalCoverageDelivery{
		chinese:   strings.HasPrefix(types.LanguageFromContextOrDefault(ctx), "zh"),
		relaxed:   relaxedTerminalEvidenceEnabled() && e.evidenceCompletionEligible() && e.standardTextDelivery(state),
		finalRead: len(finalDraft) != 0,
	}
	d.substantive = d.finalRead && finalDraft[0].substantive
	record, attempts := e.projectTerminalCoverage(ctx, state, terminalCoverageOptions{finalRead: d.finalRead, useDirectoryIdentity: d.substantive})
	d.attemptCount = len(attempts)
	if d.finalRead {
		field, _ := e.finalLastValidField(ctx, record)
		d.lastValidPending = field.EvidenceID != ""
	}
	d.subject = terminalEvidenceText(taskPlanPrefix(e.coverageAnalysisQuestion(), 160))
	targets := slices.DeleteFunc(slices.Clone(e.evidenceCompletionTargets()), func(target taskCoverageTarget) bool {
		return target.SourceTime == "general_concept" || slices.Equal(target.Required, []string{"optional"})
	})
	d.requested = targets
	add := func(role string, item coverageEvidence, required bool) {
		for i, prior := range d.facts {
			p := prior.Evidence
			if agenttools.DocumentTableEvidenceEnabled() && terminalCoverageFactKey(item) == terminalCoverageFactKey(p) {
				receipts := mergeCoverageFactReceipts(coverageFactReceipts(p), coverageFactReceipts(item))
				if required && !prior.required || item.LastValidStatus != "" && p.LastValidStatus == "" {
					d.facts[i] = terminalCoverageFact{coverageFact{role, item}, required || prior.required}
				}
				if len(receipts) > 1 {
					d.facts[i].Evidence.Receipts = receipts
				}
				return
			}
			sameEndpoint := (coverageProtocolLastValid(item) || coverageProtocolLastValid(p)) && prior.Role == role && item.Extremum == p.Extremum &&
				coverageSameOriginalCell(item, p) && reviewJSONValuesEqual(item.Raw, p.Raw)
			if sameEndpoint || terminalSameFieldGap(record, item, p) || !agenttools.DocumentTableEvidenceEnabled() && item.Kind == p.Kind && item.SourceID == p.SourceID &&
				item.KnowledgeBaseID == p.KnowledgeBaseID && item.SnapshotHash == p.SnapshotHash && item.Sheet == p.Sheet &&
				item.SeriesID == p.SeriesID && item.Column == p.Column && item.Date == p.Date && item.Cell == p.Cell && item.Extremum == p.Extremum &&
				item.SourceDate == p.SourceDate && item.PeriodFrom == p.PeriodFrom && item.PeriodTo == p.PeriodTo &&
				item.Period == p.Period && item.ReportedValue == p.ReportedValue && reviewDigest(item.Raw) == reviewDigest(p.Raw) {
				if required && !prior.required || item.LastValidStatus != "" && p.LastValidStatus == "" {
					d.facts[i] = terminalCoverageFact{coverageFact{role, item}, required || prior.required}
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
	d.record = record
	var point finalSupplementGap
	if d.finalRead && scopedReadRecoveryEnabled() {
		point, _ = e.finalSupplementPointGap(ctx, record)
	}
	for _, target := range targets {
		scoped := e.coverageRecordForTarget(ctx, target, record)
		if d.finalRead {
			for _, fact := range e.finalStructuredEndpointFacts(ctx, state, target, scoped) {
				add(fact.Role, fact.Evidence, true)
			}
			if point != (finalSupplementGap{}) && point.goal == target.GoalIndex {
				_, fields, _ := e.finalSupplementPointFields(ctx, target, scoped)
				for _, field := range fields {
					add("coverage_boundary", field, false)
					for _, item := range scoped.Evidence {
						if sameCoverageLastValid(scoped, field, item) {
							add("last_valid_value", item, false)
						}
					}
				}
				for _, fact := range e.scopedPointSupplementFacts(ctx, point, record, attempts) {
					add("attributed_supplement", fact, true)
				}
			} else {
				for _, fact := range e.finalPointSupplementFacts(ctx, target, scoped, attempts) {
					add("attributed_supplement", fact, true)
				}
			}
		}
		draft := e.turnReview.Last.Candidate
		if d.finalRead {
			draft = finalDraft[0].text
		}
		readings, reportedPair := e.tableReceiptReadingsForDelivery(target, scoped, draft, d.relaxed)
		if d.finalRead && !reportedPair && tableComparison(target) {
			visual := e.finalVisualTableReadings(ctx, target, scoped, attempts)
			if d.substantive {
				visual.inspected = inspectedComparisonReading(target, scoped, attempts, draft)
			}
			if len(visual.focusedFields) > 0 || visual.inspected.Bound || scopedReadRecoveryEnabled() && !slices.ContainsFunc(readings, func(fact coverageFact) bool {
				return fact.Evidence.SourceDomain == "structured"
			}) {
				if pair, aligned := e.tableReceiptReadingsWithDraftAttribution(target, scoped, draft, true, &visual); len(pair) > 0 {
					readings, reportedPair = pair, aligned
				}
			}
		}
		if plannedTableComparison(target) && !reportedPair && (d.relaxed || len(readings) < 2) && state != nil {
			for _, step := range slices.Backward(state.RoundSteps[min(e.turnReview.TerminalStartStep, len(state.RoundSteps)):]) {
				if len(step.ToolCalls) == 0 && step.Thought != "" {
					if prior, aligned := e.tableReceiptReadingsForDelivery(target, scoped, step.Thought, d.relaxed); aligned {
						readings, reportedPair = prior, aligned
						break
					}
				}
			}
		}
		for _, result := range evaluateTaskCoverage(target, scoped) {
			if reportedPair && result.State != coverageSatisfied {
				continue
			}
			if (target.VersionTask == "compare_sources" || target.VersionTask == "compare_versions") && result.State == coverageSatisfied {
				d.addComparison(result.PinnedFacts, agenttools.DocumentTableEvidenceEnabled() && tableComparison(target))
				if tableComparison(target) {
					readings, reportedPair = nil, false
				}
			}
			for _, missing := range result.OpenItems {
				d.addMissing(target, missing)
			}
			if agenttools.DocumentTableEvidenceEnabled() && tableComparison(target) && result.State != coverageSatisfied {
				if d.relaxed && len(readings) > 0 && !slices.ContainsFunc(readings[1:], func(fact coverageFact) bool {
					return comparisonPhysicalSource(fact.Evidence) != comparisonPhysicalSource(readings[0].Evidence)
				}) {
					result.OpenItems, result.UnresolvedSides = nil, 1
				}
				if d.substantive && len(readings) == 2 && comparisonPhysicalSource(readings[0].Evidence) != comparisonPhysicalSource(readings[1].Evidence) {
					result.PinnedFacts, result.OpenItems, result.UnresolvedSides = readings, nil, 0
				}
				d.addTableComparisonGap(target, result)
			}
			for _, fact := range result.PinnedFacts {
				if fact.Role == "last_valid_date" { // Rendered with its matching gap.
					continue
				}
				add(fact.Role, fact.Evidence, true)
			}
		}
		if d.relaxed {
			if gap, ok := e.inspectedDateCoverageGap(ctx, target, scoped); ok &&
				!slices.ContainsFunc(d.facts, func(fact terminalCoverageFact) bool {
					item := fact.Evidence
					return item.Kind == "gap" && item.SourceID == gap.SourceID && item.Sheet == gap.Sheet &&
						item.PeriodFrom == gap.PeriodFrom && item.PeriodTo == gap.PeriodTo
				}) {
				add("coverage_boundary", gap, true)
			}
		}
		if reportedPair {
			for i := 0; i+1 < len(readings); i += 2 {
				start := len(d.conclusions)
				d.addComparison(readings[i:i+2], true)
				if len(readings) > 2 && len(d.conclusions) > start {
					d.conclusions[start] = terminalEvidenceText(readings[i].Evidence.Metric) + d.text("：", ": ") + d.conclusions[start]
				}
			}
		}
		for _, fact := range readings {
			add(fact.Role, fact.Evidence, true)
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
	record.Evidence = append(record.Evidence, inspectedPublishedLastValid(record)...)
	d.record = record
	exactMissingLookup := len(targets) == 0 && slices.ContainsFunc(record.Evidence, func(item coverageEvidence) bool {
		return item.Kind == "gap" && item.Complete && item.PeriodFrom != "" && item.PeriodFrom == item.PeriodTo
	})
	extrema := make(map[string]bool)
	for _, item := range record.Evidence {
		if item.Extremum != "" {
			extrema[item.EvidenceID] = true
		}
	}

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
					// A draft may have passed with other readings before this endpoint arrived.
					recovered := d.finalRead && scopedReadRecoveryEnabled() && strings.HasPrefix(observed.ToolCallID, finalLastValidReadPrefix)
					add("last_valid_value", observed, recovered)
				}
			}
		case item.Extremum != "":
			add("scoped_value", item, false)
		case item.Kind == "derived" && item.Complete && coverageExactRange(item) && !extrema[item.EvidenceID]:
			add("scoped_value", item, false)
		case coverageProtocolLastValid(item) && item.Kind == "last_valid" && item.LatestInScope && item.PeriodTo != "":
			add("scoped_value", item, false)
		case item.Kind == "last_valid" && item.Operation == "inspect_source" && item.PeriodFrom == "" && item.PeriodTo == "":
			// The original cell has been paired with the catalog's published
			// date. Record its field and date without inferring a requested gap.
			add("scoped_value", item, false)
		case item.Kind == "last_valid_receipt":
			if !slices.ContainsFunc(record.Evidence, func(gap coverageEvidence) bool {
				if gap.Kind != "gap" {
					return false
				}
				receipt, supplied := coverageEndpointForGap(record, gap)
				return supplied && receipt.ToolCallID == item.ToolCallID && receipt.SeriesID == item.SeriesID
			}) {
				add("last_valid_status", item, false)
			}
		case item.Kind == "cell" && item.Date != "" && item.Metric != "" &&
			(item.Operation == "query_series" || item.Operation == "analyze_table") &&
			(item.Date == item.PeriodFrom && item.Date == item.PeriodTo || item.LatestInScope || item.RangeComplete ||
				item.Operation == "analyze_table" && item.WorkbookBound && item.Complete && item.ResultJSONPointer != "" && item.Cell != ""):
			add("scoped_value", item, false)
		case item.Kind == "body":
			add("source_passage", item, false)
		case item.Kind == "directory":
			d.hasDirectory = true
		}
	}
	slices.Sort(d.notes)
	d.notes = slices.Compact(d.notes)
	var tryNotes []string
	for _, attempt := range attempts {
		if d.relaxed {
			d.readReceipts = append(d.readReceipts, e.terminalDocumentReadReceipt(ctx, attempt)...)
		}
		if note := d.attemptNote(attempt); note != "" {
			tryNotes = append(tryNotes, note)
		}
	}
	if d.substantive && d.comparisonGap != "" && len(d.conclusions) == 0 {
		d.nameUnresolvedComparisonSide()
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
		if agenttools.DocumentTableEvidenceEnabled() && tableComparison(target) {
			// Comparison operands are admitted only by the checked result.
			continue
		}
		if target.Table != "" && item.Sheet != "" && item.Sheet != target.Table {
			continue
		}
		from, to, _ := coverageTargetDateRange(target)
		orderedExtremum := finalReadAnswerEnabled() && item.Operation == "analyze_table" && item.Extremum != "" && item.Cell != ""
		alignedExtremum := orderedExtremum && sourceNavigationString(target.Delivery, "aggregation") == "min_max" &&
			item.PeriodFrom == from && item.PeriodTo == to
		if orderedExtremum && !alignedExtremum {
			continue
		}
		if coverageExactRange(coverageEvidence{PeriodFrom: from, PeriodTo: to}) {
			switch item.Kind {
			case "gap", "derived", "last_valid_receipt":
				if item.PeriodFrom != from || item.PeriodTo != to {
					continue
				}
			case "cell":
				if item.Date != "" && (item.Date < from || item.Date > to) ||
					item.PeriodFrom != "" && item.PeriodFrom < from || item.PeriodTo != "" && item.PeriodTo > to {
					continue
				}
			case "last_valid":
				if item.Operation == "inspect_source" && target.SourceMode == "only" &&
					(item.Date < from || item.Date > to) && !slices.Contains(target.OnVerifiedGap, "last_valid_same_field") {
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
		if matches || !hasSelection && (len(targets) == 1 || alignedExtremum) {
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
		item.Period != prior.Period || item.LastValidFilterHash != prior.LastValidFilterHash {
		return false
	}
	if item.LastValidStatus != "" || prior.LastValidStatus != "" {
		return coverageSameEndpointSnapshot(item, prior) && (item.ManifestHash == "" || prior.ManifestHash == "" || item.ManifestHash == prior.ManifestHash)
	}
	if item.LastValidDate != prior.LastValidDate {
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
	result := d.renderPublication(failure)
	return firstNonEmptyAgent(result.answer, result.notice)
}

func (d terminalCoverageDelivery) renderPublication(failure terminalFailure) terminalCoveragePublication {
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
	var lines, status, groupOrder []string
	groups := make(map[string][]terminalCoverageFact)
	used := map[string]bool{}
	fields := map[string]string{}
	for _, fact := range d.facts {
		item := fact.Evidence
		if used[terminalCoverageFactKey(item)] {
			continue
		}
		if item.Kind == "gap" {
			if sentence := d.gapFinding(item, used); agenttools.DocumentTableEvidenceEnabled() || !slices.Contains(lines, sentence) {
				lines = append(lines, sentence)
			}
			continue
		}
		if fact.Role == "last_valid_value" && slices.ContainsFunc(d.facts, func(other terminalCoverageFact) bool {
			return other.Evidence.Kind == "gap" && d.nearestValue(other.Evidence, item)
		}) {
			continue
		}
		if item.Kind == "body" && !fact.required && (len(lines) != 0 || len(groups) != 0 || item.ReportedValue == "" && (d.hasDirectory || d.finalRead)) {
			continue
		}
		if item.Extremum != "" {
			if sentence := d.receiptFinding(item, false); sentence != "" && !slices.Contains(lines, sentence) {
				lines = append(lines, sentence)
			}
			used[terminalCoverageFactKey(item)] = true
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
		if sentence := d.receiptFinding(item, fact.required && item.Kind == "body"); sentence != "" && (agenttools.DocumentTableEvidenceEnabled() || !slices.Contains(lines, sentence)) {
			if item.Kind == "last_valid_receipt" || item.Kind == "body" && item.ReportedValue == "" && !fact.required {
				status = append(status, sentence)
			} else {
				lines = append(lines, d.appendComparisonCitation(sentence, item))
			}
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
			sentence = d.receiptFinding(group[0].Evidence, false)
			if sentence != "" {
				sentence = d.appendComparisonCitation(sentence, group[0].Evidence)
			}
		} else {
			sentence = d.groupedValues(group)
		}
		if sentence != "" && (agenttools.DocumentTableEvidenceEnabled() || !slices.Contains(lines, sentence)) {
			lines = append(lines, sentence)
		}
	}
	if len(lines) != 0 {
		missingFindings := d.missing
		if d.relaxed {
			missingFindings = nil
			if d.comparisonGap != "" && (!d.finalRead || d.substantive) {
				missingFindings = append(missingFindings, d.comparisonGap)
			}
			if d.comparisonUnit != "" {
				lines = append(lines, d.comparisonUnit)
			}
		}
		for _, missing := range missingFindings {
			if !slices.Contains(lines, missing) {
				lines = append(lines, missing)
			}
		}
		answer := strings.Join(append(d.conclusions, lines...), "\n\n")
		if d.substantive && d.comparisonGap != "" && len(d.conclusions) == 0 {
			return terminalCoveragePublication{answer: answer, notice: answer, partial: true}
		}
		return terminalCoveragePublication{answer: answer}
	}
	d.notes = append(status, d.notes...)
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
	return terminalCoveragePublication{notice: lead + " " + strings.Join(append(lines, d.missing...), " ")}
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
	sameOriginalRow := first.Operation == "analyze_table" && first.DateCell != "" && !slices.ContainsFunc(group, func(fact terminalCoverageFact) bool {
		item := fact.Evidence
		return !item.WorkbookBound || item.ResultJSONPointer == "" || item.DateCell != first.DateCell || item.Cell == ""
	})
	if sameOriginalRow {
		period += " (" + d.text("日期格 ", "date cell ") + terminalEvidenceText(first.DateCell) + ")"
	}
	label := terminalCoverageLabel(coverageEvidence{SourceTitle: d.fallbackSourceTitle(first), Sheet: first.Sheet})
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
		if agenttools.DocumentTableEvidenceEnabled() && counts[fieldLabel(item)] > 1 && len(item.Dimensions) != 0 {
			var dimensions []string
			for key, value := range item.Dimensions {
				if label, ok := reviewScalarWitnessQuote(value); ok && len(label) <= 128 && len(key) <= 128 {
					dimensions = append(dimensions, terminalEvidenceText(key)+" "+terminalEvidenceText(label))
				}
			}
			slices.Sort(dimensions)
			if len(dimensions) != 0 {
				field += " (" + strings.Join(dimensions, ", ") + ")"
			}
		}
		value := terminalReceiptValue(item)
		if fact.required || multiple || agenttools.DocumentTableEvidenceEnabled() && counts[fieldLabel(item)] > 1 {
			value = d.observedValue(item)
		} else {
			if value != "" && (sameOriginalRow || d.finalRead && item.Cell != "") {
				value += " (" + d.text("取值格 ", "value cell ") + terminalEvidenceText(item.Cell) + ")"
			}
			unconfirmed = unconfirmed || item.Kind != "derived" && !terminalUnitFromSource(item)
		}
		if field != "" && value != "" {
			observation := field + " " + value
			if agenttools.DocumentTableEvidenceEnabled() || !slices.Contains(values, observation) {
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

func (d *terminalCoverageDelivery) addComparison(facts []coverageFact, checkedTable bool) {
	if checkedTable {
		facts = slices.DeleteFunc(slices.Clone(facts), func(fact coverageFact) bool { return fact.Role != "scoped_value" })
	}
	if len(facts) != 2 || facts[0].Role != "scoped_value" || facts[1].Role != "scoped_value" {
		return
	}
	a, b := facts[0].Evidence, facts[1].Evidence
	left, leftOK := coverageComparisonNumber(a)
	right, rightOK := coverageComparisonNumber(b)
	if !leftOK || !rightOK {
		return
	}
	if checkedTable && types.VerifiedPublicationEnabled() {
		d.addVerifiedComparison(a, b, left, right)
		return
	}
	comparable := terminalUnitFromSource(a) && terminalUnitFromSource(b) && a.Unit == b.Unit
	var note string
	if comparable && !a.ComparisonGrainUnverified && !b.ComparisonGrainUnverified {
		note = d.text("两处来源在核查范围内的数值不同。", "The two sources report different values for the checked scope.")
		if left.Cmp(right) == 0 {
			note = d.text("两处来源在核查范围内的数值相同。", "The two sources report the same value for the checked scope.")
		}
	} else {
		note = d.text("两处来源的单位无法直接比较。", "The sources' units cannot be compared directly.")
		if checkedTable {
			note = d.text("两处来源的原报数值不同。", "The two sources print different numbers.")
			if left.Cmp(right) == 0 {
				note = d.text("两处来源的原报数值相同。", "The two sources print the same numbers.")
			}
			switch comparisonUnitStatus(a, b) {
			case "unknown":
				note += d.text("单位尚未核实为可直接比较。", " Direct unit comparability remains unverified.")
			case "incompatible":
				note += d.text("原单位不同，无法直接比较量值。", " The source units differ, so the quantities cannot be compared directly.")
			}
		}
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

func (d *terminalCoverageDelivery) addTableComparisonGap(target taskCoverageTarget, result coverageCompletion) {
	from, to, _ := coverageTargetDateRange(target)
	field := terminalEvidenceText(strings.TrimSpace(target.Table + " " + target.Metric))
	label := ""
	if !d.substantive {
		label = sourceNavigationString(target.Period, "scope")
	} else {
		from, to = coverageDay(from), coverageDay(to)
	}
	period := terminalEvidenceText(terminalCoveragePeriod(from, to, label))
	scope := strings.TrimSpace(field + " " + period)
	if len(result.OpenItems) == 0 && result.UnresolvedSides == 0 && len(result.PinnedFacts) == 2 {
		if d.substantive {
			d.comparisonGap = fmt.Sprintf(d.text("两份来源的 %s 读数尚未对齐表、字段或单位，无法判断两处是否一致。", "The checked sources do not yet establish an aligned table, field and unit for %s, so it is unclear whether they agree."), scope)
		} else {
			d.comparisonGap = fmt.Sprintf(d.text("两份实读的 %s 尚未对齐表、字段或单位。", "The checked sources do not yet establish an aligned table, field and unit for %s."), scope)
		}
		d.missing = append(d.missing, d.comparisonGap)
		return
	}
	var names []string
	for _, side := range result.OpenItems {
		if !taskCoverageSourceHandle(side) && side != "" {
			names = append(names, terminalEvidenceText(side))
		}
	}
	if len(names) == 0 {
		if d.substantive {
			if result.UnresolvedSides == 1 {
				d.comparisonGap = fmt.Sprintf(d.text("本轮未从另一来源读到可对应 %s 的数，无法判断两处是否一致。", "This turn did not find a matching reading for %s in the other source, so it is unclear whether the two agree."), scope)
			} else {
				d.comparisonGap = fmt.Sprintf(d.text("本轮未读到可对照的 %s 读数，无法判断两处是否一致。", "This turn did not find corresponding readings for %s that establish whether the sources agree."), scope)
			}
		} else {
			d.comparisonGap = fmt.Sprintf(d.text("另一来源的 %s 尚缺同表、同期读数。", "A matching reading for %s is still needed from the other source."), scope)
		}
		d.missing = append(d.missing, d.comparisonGap)
		return
	}
	sources := strings.Join(names, d.text("、", ", "))
	if d.chinese {
		d.comparisonGap = fmt.Sprintf("尚未核到 %s 的 %s 同表、同期读数。", sources, scope)
	} else {
		d.comparisonGap = fmt.Sprintf("A matching reading for %s from %s is still unverified.", scope, sources)
	}
	if d.substantive {
		d.comparisonGap += d.text("因此无法判断两处是否一致。", " It is unclear whether the sources agree.")
	}
	d.missing = append(d.missing, d.comparisonGap)
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
	case terminalTableEvidence:
		stage = d.text("已返回的表格片段尚未核到所需的原行和列。", "The returned table passages still need matching original rows and columns.")
	case terminalBudget, terminalTruncated:
		stage = d.text("生成完整答复前达到了运行上限。", "The run reached its limit before completing the answer.")
	case terminalInterrupted:
		stage = d.text("答复完成前执行已中断。", "Execution was interrupted before the answer was complete.")
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
	if agenttools.DocumentTableEvidenceEnabled() {
		key := coverageEvidenceFactKey(item)
		if item.Extremum != "" {
			return key + "\x00" + item.Extremum + "\x00" + item.PeriodFrom + "\x00" + item.PeriodTo
		}
		return key
	}
	return strings.Join([]string{item.KnowledgeBaseID, item.SourceID, item.SnapshotHash, item.Sheet,
		item.SeriesID, item.Column, item.PeriodFrom, item.PeriodTo, item.Period, item.Date, item.Cell,
		item.Metric, item.ChunkID, item.JSONPointer, item.EvidenceID}, "\x00")
}

func (d terminalCoverageDelivery) nearestValue(gap, value coverageEvidence) bool {
	if value.Kind == "cell" && value.Operation == "query_series" && value.LatestInScope && value.Date < gap.PeriodFrom {
		value.Kind = "last_valid" // A direct latest lookup has the same original-cell proof.
	}
	return sameCoverageLastValid(d.record, gap, value) || coverageAnalysisFollowUpMatches(d.record, gap, value)
}

func (d terminalCoverageDelivery) gapFinding(gap coverageEvidence, used map[string]bool) string {
	gap.SourceTitle = d.fallbackSourceTitle(gap)
	label := terminalCoverageLabel(gap)
	period := terminalCoveragePeriod(gap.PeriodFrom, gap.PeriodTo, gap.Period)
	if gap.Operation == "inspect_source" && gap.SeriesID == "" && gap.LastObservedDate != "" {
		used[terminalCoverageFactKey(gap)] = true
		if !gap.Complete {
			return fmt.Sprintf(d.text("%s 的日期格 %s 记载了 %s。", "In %s, date cell %s records %s."),
				label, terminalEvidenceText(gap.DateCell), terminalEvidenceText(gap.LastObservedDate))
		}
		return fmt.Sprintf(d.text("%s 的日期列没有 %s 的记录。原表日期列止于 %s（%s）。",
			"The date column of %s has no records for %s. It ends on %s (%s)."),
			label, terminalEvidenceText(period), terminalEvidenceText(gap.LastObservedDate), terminalEvidenceText(gap.DateCell))
	}
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
	if endpoint, supplied := coverageEndpointForGap(d.record, gap); supplied {
		if note := d.lastValidStatusNote(endpoint); note != "" {
			line += d.text("", " ") + note
		}
	}
	return line
}

func (d terminalCoverageDelivery) lastValidStatusNote(item coverageEvidence) string {
	switch item.LastValidStatus {
	case "no_valid":
		return d.text("该字段在已核实范围内没有有效值。", "The field has no valid value in the verified scope.")
	case "conflict":
		return d.text("同字段有互相冲突的值，末值未能唯一确定。", "The field has conflicting values, leaving its latest value undetermined.")
	case "truncated":
		return d.text("末日原行超出回包上限，尚未取得可核实的唯一末值。", "The latest date has more original rows than the response can return. A unique latest value remains unverified.")
	case "not_ordered":
		return d.text("该字段没有可用于判断末值的日期顺序。", "The field has no date order for identifying a latest value.")
	case "unverified":
		return d.text("尚未取得可核实的同字段最近有效值。", "A verified latest value for the field is still needed.")
	default:
		return ""
	}
}

var terminalDocumentComment = regexp.MustCompile(`(?s)<!--.*?-->`)

func (d terminalCoverageDelivery) receiptFinding(item coverageEvidence, citedPassage bool) string {
	item.SourceTitle = d.fallbackSourceTitle(item)
	label := terminalCoverageLabel(item)
	if label == "" {
		return ""
	}
	if item.Kind == "last_valid_receipt" {
		if note := d.lastValidStatusNote(item); note != "" {
			return label + " · " + note
		}
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
			if citedPassage {
				return fmt.Sprintf(d.text("%s记载：“%s”。", "%s records: “%s”."), location, excerpt)
			}
			return fmt.Sprintf(d.text("%s返回的正文片段为“%s”。尚缺与所问范围对应的读数。", "%s returned this passage: “%s”. A reading matched to the requested scope is still needed."), location, excerpt)
		}
		return ""
	}
	value := d.observedValue(item)
	if value == "" || period == "" {
		return ""
	}
	if item.Extremum != "" {
		kind := d.text("最高值", "maximum")
		if item.Extremum == "min" {
			kind = d.text("最低值", "minimum")
		}
		if item.ObservationFrom != "" && item.ObservationTo != "" {
			interval := terminalEvidenceText(terminalCoveragePeriod(item.ObservationFrom, item.ObservationTo, ""))
			return fmt.Sprintf(d.text("%s 在 %s 的有效记录中，%s为 %s，出现在 %s。",
				"For %s, in the records from %s, the %s was %s, observed on %s."), label, interval, kind, value, period)
		}
		interval := terminalEvidenceText(terminalCoveragePeriod(item.PeriodFrom, item.PeriodTo, item.Period))
		if interval != "" {
			label += " (" + interval + ")"
		}
		if d.relaxed && item.Operation == "analyze_table" {
			return fmt.Sprintf(d.text("%s 已载入记录中的%s为 %s，日期为 %s。",
				"The %s's %s was %s in the loaded records, dated %s."), label, kind, value, period)
		}
		return fmt.Sprintf(d.text("%s 的%s为 %s，出现在 %s。", "The %s reports a %s of %s on %s."), label, kind, value, period)
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

func (d terminalCoverageDelivery) fallbackSourceTitle(item coverageEvidence) string {
	title := item.SourceTitle
	if !agenttools.DocumentTableEvidenceEnabled() || title == "" || item.SourceID == "" {
		return title
	}
	type sourceKey struct {
		physical [2]string
		version  string
	}
	key := func(evidence coverageEvidence) sourceKey {
		source := sourceKey{physical: comparisonPhysicalSource(evidence)}
		if evidence.SourceDomain == "structured" {
			source.version = firstNonEmptyAgent(evidence.WorkbookSnapshotHash, evidence.SnapshotHash)
		}
		return source
	}
	var sources []sourceKey
	for _, fact := range d.facts {
		if fact.Evidence.SourceTitle == title && fact.Evidence.SourceID != "" {
			id := key(fact.Evidence)
			if !slices.Contains(sources, id) {
				sources = append(sources, id)
			}
		}
	}
	if index := slices.Index(sources, key(item)); len(sources) > 1 && index >= 0 {
		return title + fmt.Sprintf(d.text("（实读来源 %d）", " (checked source %d)"), index+1)
	}
	return title
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
	if compared := d.comparisonValues[terminalCoverageFactKey(item)]; compared != "" {
		value = compared
		if terminalUnitFromSource(item) {
			value += " " + terminalEvidenceText(item.Unit)
		}
	}
	if value == "" {
		return ""
	}
	var location []string
	if item.DateCell != "" {
		location = append(location, d.text("日期格 ", "date cell ")+terminalEvidenceText(item.DateCell))
	}
	if item.Cell != "" && item.Kind != "body" {
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
	if count := attempt.result.Data["count"]; count != nil && coverageZero(count) {
		return fmt.Sprintf(d.text("%s已执行，返回 0 条结果。", "%s completed with 0 results."), operation)
	}
	if d.finalRead {
		return fmt.Sprintf(d.text("%s已执行。", "%s completed."), operation)
	}
	return fmt.Sprintf(d.text("%s已执行，当前范围内没有可用于所问结果的已核实记录。", "%s ran. No verified record for the requested result is available within the current scope."), operation)
}
