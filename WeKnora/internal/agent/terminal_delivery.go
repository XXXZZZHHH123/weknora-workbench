package agent

import (
	"context"
	"maps"
	"slices"
	"strings"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/common"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
)

// publishAnswer reuses an existing stream, or emits the buffered candidate
// once. Closing remains separate so a queued human correction can continue.
func (e *AgentEngine) publishAnswer(ctx context.Context, sessionID, answer, existingID string, truncated bool) string {
	answer = stripCoverageAnswer(answer)
	if ctx.Err() != nil || terminalCandidateFallback(answer) ||
		!e.turnReview.Committed && (e.holdCollaborationAnswer || e.terminalEvidenceCompletionPending() || e.turnReview.TerminalSensitive) {
		return ""
	}
	if existingID != "" {
		return existingID
	}
	id := generateEventID("answer")
	e.turnReview.TerminalStreamID, e.turnReview.TerminalReceipt.streamed = id, answer
	if e.eventBus != nil {
		_ = e.eventBus.Emit(ctx, event.Event{ID: id, Type: event.EventAgentFinalAnswer, SessionID: sessionID, Data: event.AgentFinalAnswerData{Content: answer, Done: false, Truncated: truncated}})
	}
	return id
}

func (e *AgentEngine) terminalEvidenceCompletionPending() bool {
	return e.terminalCoverageContextKnown()
}

func (e *AgentEngine) terminalCoverageContextKnown() bool {
	if !e.evidenceCompletionEligible() || e.holdCollaborationAnswer {
		return false
	}
	if e.hasEvidenceCompletionContext() {
		return true
	}
	if e.taskPlan != nil && e.taskPlan.SourceDiscovery != nil && e.taskPlan.SourceDiscovery.Mode != "none" &&
		slices.ContainsFunc(e.taskPlan.SourceDiscovery.SourceDomains, func(domain string) bool { return domain == "knowledge" || domain == "structured" }) {
		return true
	}
	return slices.ContainsFunc(e.evidenceCompletionTargets(), func(target taskCoverageTarget) bool {
		return target.SourceTime != "general_concept" && !slices.Equal(target.Required, []string{"optional"})
	})
}

// Numeric-failure fallbacks. A later candidate that fails numeric grounding
// never replaces an earlier one that passed it; terminal delivery prefers that
// earlier candidate with a notice over this message.
const (
	numericFailureFallback      = "已经取得原表依据，但完整回答的部分数字未通过来源核验，暂不提供未经核验的统计结论。请查看取数依据。"
	finalNumericFailureFallback = "已有取数依据，但完整回答的部分数字未通过来源核验。此次暂不提供未经核验的统计结论，请查看来源或重试。"
	interruptedAnswerFallback   = "任务执行期间服务中断，当前步骤没有可安全恢复的检查点，已停止本轮及未完成的子任务，避免重复执行。请重新发起任务。"
	timedOutAnswerFallback      = "任务已超过执行时限，已停止本轮及未完成的子任务，避免重复执行。已保存的执行记录和子任务结果仍可查看。"
)

var terminalFallbacks = [...]string{
	answerReviewUnavailableFallback, incompleteAnswerFallback, stalledAnswerFallback, sourceProgressStalledFallback,
	truncatedAnswerFallback, numericFailureFallback, finalNumericFailureFallback,
	interruptedAnswerFallback, timedOutAnswerFallback,
}

func terminalCandidateFallback(answer string) bool {
	answer = strings.TrimSpace(answer)
	return answer == "" || slices.Contains(terminalFallbacks[:], answer)
}

// A streamed prefix can still become a runtime placeholder. Wait until the
// content distinguishes it from every placeholder before sending the prefix.
func terminalFallbackPrefix(prefix string) bool {
	prefix = strings.TrimSpace(prefix)
	return slices.ContainsFunc(terminalFallbacks[:], func(fallback string) bool { return strings.HasPrefix(fallback, prefix) })
}

type terminalFailure string

const (
	terminalNoFailure        terminalFailure = ""
	terminalEmpty            terminalFailure = "empty"
	terminalReview           terminalFailure = "review_failed"
	terminalNumeric          terminalFailure = "numeric_failed"
	terminalIncomplete       terminalFailure = "incomplete"
	terminalStalled          terminalFailure = "stalled"
	terminalTruncated        terminalFailure = "truncated"
	terminalBudget           terminalFailure = "budget_exhausted"
	terminalInterrupted      terminalFailure = "interrupted"
	terminalTableEvidence    terminalFailure = "table_evidence_unverified"
	terminalEndpointEvidence terminalFailure = "endpoint_evidence_unverified"
	terminalSourceStalled    terminalFailure = "source_stalled"
	terminalSourceChanged    terminalFailure = "source_changed"
	terminalFieldInput       terminalFailure = "field_confirmation_input"
	terminalFieldService     terminalFailure = "field_confirmation_service"
	terminalFieldBlocked     terminalFailure = "field_confirmation_preflight"
	terminalFieldResult      terminalFailure = "field_confirmation_result"
)

type terminalPublicationReceipt struct {
	answerHash string
	evidence   terminalEvidenceSnapshot
	staleCalls map[string]bool
	failure    terminalFailure
	streamed   string
}

// This is the only translation of legacy candidate strings into failure reasons.
func legacyTerminalFailure(answer string) terminalFailure {
	switch strings.TrimSpace(answer) {
	case answerReviewUnavailableFallback:
		return terminalReview
	case numericFailureFallback, finalNumericFailureFallback:
		return terminalNumeric
	case incompleteAnswerFallback:
		return terminalIncomplete
	case stalledAnswerFallback:
		return terminalStalled
	case sourceProgressStalledFallback:
		return terminalSourceStalled
	case truncatedAnswerFallback:
		return terminalTruncated
	case interruptedAnswerFallback:
		return terminalInterrupted
	case timedOutAnswerFallback:
		return terminalBudget
	default:
		return terminalEmpty
	}
}

// Only checked candidates or explicit runtime failures reach this outlet.
func (e *AgentEngine) commitTerminalResult(ctx context.Context, state *types.AgentState, sessionID, answer, existingID, outcome string, truncated bool, failures ...terminalFailure) {
	if ctx.Err() != nil || e.turnReview.Committed {
		return
	}
	original := answer
	answer = stripCoverageAnswer(answer)
	failure := terminalNoFailure
	if len(failures) != 0 {
		failure = failures[0]
	}
	if failure == terminalNoFailure && terminalCandidateFallback(answer) {
		failure = legacyTerminalFailure(answer)
	}

	last := e.turnReview.Last
	evidenceRevision := e.terminalEvidenceRevision(ctx, state)
	scopeChanged := last.PublicationEvidence.scope == "" && last.SourceScopeHash != "" &&
		last.SourceScopeHash != e.unverifiedSourceScopeDigest(ctx) || !last.PublicationEvidence.current(evidenceRevision) ||
		!e.turnReview.TerminalInput.current(evidenceRevision)
	staleCalls := last.PublicationEvidence.staleCalls(evidenceRevision)
	if staleInput := e.turnReview.TerminalInput.staleCalls(evidenceRevision); len(staleInput) != 0 {
		if staleCalls == nil {
			staleCalls = staleInput
		} else {
			maps.Copy(staleCalls, staleInput)
		}
	}
	if scopeChanged {
		common.PipelineWarn(ctx, "Agent", "terminal_source_revision_changed", map[string]any{
			"checked_scope_changed": last.PublicationEvidence.scope != "" && last.PublicationEvidence.scope != evidenceRevision.scope,
			"checked_task_changed":  last.PublicationEvidence.task != "" && last.PublicationEvidence.task != evidenceRevision.task,
			"input_scope_changed":   e.turnReview.TerminalInput.scope != "" && e.turnReview.TerminalInput.scope != evidenceRevision.scope,
			"stale_receipts":        len(staleCalls),
		})
		failure = terminalSourceChanged
	}
	evidenceFailure := e.terminalCandidateEvidenceFailure(ctx, state, answer)
	if evidenceFailure != terminalNoFailure && failure != terminalSourceChanged && !terminalCandidateFallback(answer) {
		failure = evidenceFailure
	}
	markedNumbers, markedCitations := 0, 0
	if failure == terminalNoFailure {
		if delivery, ok := e.numericWarningDelivery(answer, state); ok {
			answer, outcome = delivery, "numeric_warning"
		} else if (outcome == "answer" || outcome == "partial") && e.needsSourceConsistencyCheck(state) {
			submittedHash := candidateTextDigest(agenttools.TrimAnswerWhitespace(answer))
			approvedHash := candidateTextDigest(agenttools.TrimAnswerWhitespace(last.Candidate))
			coveredPartial := outcome == "partial" && last.Disposition == candidateEvidencePartial &&
				e.candidateDecisionCurrent(last, last.Candidate, state)
			if submittedHash == approvedHash && (e.candidateApprovalCurrent(last.Candidate, state) || coveredPartial) {
				answer = last.Candidate
				if coveredPartial {
					e.bindCoverageDraftDelivery(last.Candidate, answer)
				}
			} else {
				common.PipelineWarn(ctx, "Agent", "terminal_approval_mismatch", map[string]interface{}{
					"submitted_candidate_hash": candidateTextDigest(answer),
					"checked_candidate_hash":   last.CandidateHash,
					"checked_disposition":      last.Disposition,
				})
				failure = terminalReview
			}
		}
	}
	if failure != terminalNoFailure && failure != terminalSourceChanged {
		if draft, ok := e.currentCoverageDraft(ctx, state); ok {
			answer, outcome, truncated = stripCoverageAnswer(draft), "evidence_partial", false
			e.bindCoverageDraftDelivery(draft, answer)
			failure = terminalNoFailure
		} else if evidenceFailure != terminalNoFailure {
			failure = evidenceFailure
		} else if e.turnReview.Last.ReasonCode == "table_evidence_attribution_incomplete" {
			failure = terminalTableEvidence
		} else if e.turnReview.Last.ReasonCode == incompleteVersionEvidence {
			failure = terminalReview
		} else if delivery, citations, numbers, ok := e.currentCitationUnverifiedDelivery(ctx, state); ok {
			answer, outcome, markedCitations, markedNumbers = delivery, "citation_unverified", citations, numbers
			if numbers > 0 {
				outcome = "citation_numeric_unverified"
			}
			failure, truncated = terminalNoFailure, false
		} else if delivery, ok := e.numericWarningDelivery(e.turnReview.UnverifiedCandidate, state); ok {
			answer, outcome, failure, truncated = delivery, "numeric_warning", terminalNoFailure, false
		} else if delivery, ok := e.currentUnverifiedDelivery(ctx, state); ok {
			answer, outcome, failure, truncated = e.prepareUnverifiedCitationDelivery(ctx, state, delivery), "unverified", terminalNoFailure, false
		} else if failure == terminalNumeric {
			if delivery, marked, ok := e.currentNumericUnverifiedDelivery(ctx, state); ok {
				answer, outcome, markedNumbers = delivery, "numeric_unverified", marked
				failure, truncated = terminalNoFailure, false
			}
		}
	}

	answer = stripCoverageAnswer(answer)
	if failure == terminalNoFailure {
		failure = e.terminalCandidateEvidenceFailure(ctx, state, answer)
	}
	if failure == terminalNoFailure && terminalCandidateFallback(answer) {
		failure = legacyTerminalFailure(answer)
	}
	if failure != terminalNoFailure {
		answer, outcome, truncated = e.terminalCoverageFailure(ctx, terminalCurrentState(state, staleCalls), failure), "evidence_incomplete", false
		state.EvidenceRefs, state.KnowledgeRefs = nil, nil
	}

	if existingID != "" && e.turnReview.TerminalStreamID == existingID {
		original = e.turnReview.TerminalReceipt.streamed
	}
	e.turnReview.Committed = true
	state.FinalAnswer = answer
	state.IsComplete = true
	e.turnReview.TerminalStreamID, e.turnReview.TerminalTruncated = existingID, truncated
	e.turnReview.TerminalReceipt = terminalPublicationReceipt{
		answerHash: candidateTextDigest(answer), evidence: evidenceRevision, failure: failure,
		staleCalls: staleCalls,
		streamed:   original,
	}
	if existingID == "" {
		e.turnReview.TerminalReceipt.streamed = ""
	}
	deferPublication := e.turnReview.DeferTerminalClose && (e.turnReview.TerminalSensitive || e.terminalCoverageContextKnown() || failure != terminalNoFailure)
	if !deferPublication {
		e.publishTerminalStream(ctx, state, sessionID)
	}
	if !e.turnReview.DeferTerminalClose {
		e.closeAnswerStream(ctx, sessionID, e.turnReview.TerminalStreamID, truncated)
	}
	common.PipelineInfo(ctx, "Agent", "terminal_commit", map[string]interface{}{"outcome": outcome, "failure": failure, "candidate_hash": candidateTextDigest(answer), "repairs": e.turnReview.Repairs, "checks": e.turnReview.Checks, "marked_numeric_count": markedNumbers, "marked_citation_count": markedCitations})
}

func (e *AgentEngine) terminalCandidateEvidenceFailure(ctx context.Context, state *types.AgentState, answer string) terminalFailure {
	if !e.evidenceCompletionEligible() || state == nil || !e.standardTextDelivery(state) {
		return terminalNoFailure
	}
	tables := agenttools.DocumentTableEvidenceEnabled()
	evidenceIDs := uniqueCandidateEvidenceIDs(answer)
	if !tables && len(evidenceIDs) == 0 {
		return terminalNoFailure
	}
	review := e.currentCoverageDraftReview(ctx, state, answer)
	if len(evidenceIDs) != 0 && slices.ContainsFunc(review.record.Evidence, func(item coverageEvidence) bool {
		return coverageProtocolLastValid(item) && item.LastValidDirection == "before_range" && slices.Contains(evidenceIDs, item.CitationID)
	}) && e.assessCoverageDraft(ctx, review, answer, state).endpointMismatch {
		return terminalEndpointEvidence
	}
	if !tables {
		return terminalNoFailure
	}
	if len(e.evidenceCompletion.versionClaims[candidateTextDigest(strings.TrimSpace(answer))]) > 0 &&
		e.assessCoverageDraft(ctx, review, answer, state).versionBlocked {
		return terminalReview
	}
	if len(unattributedMultiSourceTableValues(review, answer)) > 0 {
		return terminalTableEvidence
	}
	if !review.blockedComparison() {
		return terminalNoFailure
	}
	for _, decision := range []candidateDecision{e.turnReview.Last, e.turnReview.CoverageDraft} {
		if decision.CandidateHash == candidateTextDigest(answer) &&
			decision.PublicationEvidence.current(e.terminalEvidenceRevision(ctx, state)) &&
			comparisonDraftChecked(decision, e.assessCoverageDraft(ctx, review, answer, state)) {
			return terminalNoFailure
		}
	}
	return terminalTableEvidence
}

func (e *AgentEngine) publishTerminalStream(ctx context.Context, state *types.AgentState, sessionID string) {
	receipt := &e.turnReview.TerminalReceipt
	id, truncated := e.turnReview.TerminalStreamID, e.turnReview.TerminalTruncated
	if id == "" {
		e.turnReview.TerminalStreamID = e.publishAnswer(ctx, sessionID, state.FinalAnswer, "", truncated)
	} else if receipt.streamed != state.FinalAnswer && e.eventBus != nil {
		_ = e.eventBus.Emit(ctx, event.Event{ID: id, Type: event.EventAgentFinalAnswer, SessionID: sessionID,
			Data: event.AgentFinalAnswerData{Content: state.FinalAnswer, Replace: true, Truncated: truncated}})
	}
	receipt.streamed = state.FinalAnswer
}
