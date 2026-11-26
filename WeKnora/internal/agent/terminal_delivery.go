package agent

import (
	"context"
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
		!e.turnReview.Committed && (e.holdCollaborationAnswer || e.terminalEvidenceCompletionPending()) {
		return ""
	}
	if existingID != "" {
		return existingID
	}
	id := generateEventID("answer")
	if e.eventBus != nil {
		_ = e.eventBus.Emit(ctx, event.Event{ID: id, Type: event.EventAgentFinalAnswer, SessionID: sessionID, Data: event.AgentFinalAnswerData{Content: answer, Done: false, Truncated: truncated}})
	}
	return id
}

func (e *AgentEngine) terminalEvidenceCompletionPending() bool {
	return e.evidenceCompletionEnabled() && e.terminalCoverageContextKnown()
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
)

var terminalFallbacks = [...]string{
	answerReviewUnavailableFallback, incompleteAnswerFallback, stalledAnswerFallback, sourceProgressStalledFallback,
	truncatedAnswerFallback, numericFailureFallback, finalNumericFailureFallback,
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

// Only checked candidates or explicit runtime failure messages reach this
// terminal outlet. IsComplete remains loop termination, not semantic success.
func (e *AgentEngine) commitTerminalResult(ctx context.Context, state *types.AgentState, sessionID, answer, existingID, outcome string, truncated bool) {
	if ctx.Err() != nil || e.turnReview.Committed {
		return
	}
	answer = stripCoverageAnswer(answer)
	markedNumbers := 0
	markedCitations := 0
	if delivery, ok := e.numericWarningDelivery(answer, state); ok {
		answer, outcome = delivery, "numeric_warning"
	} else if (outcome == "answer" || outcome == "partial") && e.needsSourceConsistencyCheck(state) {
		approved := e.turnReview.Last.Candidate
		submittedHash := candidateTextDigest(agenttools.TrimAnswerWhitespace(answer))
		approvedHash := candidateTextDigest(agenttools.TrimAnswerWhitespace(approved))
		coveredPartial := outcome == "partial" && e.turnReview.Last.Disposition == candidateEvidencePartial &&
			e.evidenceCompletionEnabled() && e.turnReview.Last.SourceScopeHash == e.unverifiedSourceScopeDigest(ctx) &&
			e.candidateDecisionCurrent(e.turnReview.Last, approved, state)
		if submittedHash == approvedHash && (e.candidateApprovalCurrent(approved, state) || coveredPartial) {
			// Store the checked text so citation bindings still have its exact bytes.
			answer = approved
		} else {
			common.PipelineWarn(ctx, "Agent", "terminal_approval_mismatch", map[string]interface{}{
				"submitted_candidate_hash": candidateTextDigest(answer),
				"checked_candidate_hash":   e.turnReview.Last.CandidateHash,
				"checked_disposition":      e.turnReview.Last.Disposition,
			})
			answer = answerReviewUnavailableFallback
			existingID = ""
			outcome = "failed"
			truncated = false
		}
	}
	coverageDraft := false
	if terminalCandidateFallback(answer) {
		if draft, ok := e.currentCoverageDraft(ctx, state); ok {
			answer, existingID, outcome, truncated = stripCoverageAnswer(draft), "", "evidence_partial", false
			coverageDraft = true
		}
	}
	if terminalCandidateFallback(answer) {
		if delivery, citations, numbers, ok := e.currentCitationUnverifiedDelivery(ctx, state); ok {
			answer, outcome, markedCitations, markedNumbers = delivery, "citation_unverified", citations, numbers
			if numbers > 0 {
				outcome = "citation_numeric_unverified"
			}
			existingID, truncated = "", false
		} else if delivery, ok := e.numericWarningDelivery(e.turnReview.UnverifiedCandidate, state); ok {
			answer, outcome = delivery, "numeric_warning"
			existingID, truncated = "", false
		} else if delivery, ok := e.currentUnverifiedDelivery(ctx, state); ok {
			answer = e.prepareUnverifiedCitationDelivery(ctx, state, delivery)
			existingID = ""
			outcome = "unverified"
			truncated = false
		} else if answer == numericFailureFallback || answer == finalNumericFailureFallback {
			if delivery, marked, ok := e.currentNumericUnverifiedDelivery(ctx, state); ok {
				answer, outcome, markedNumbers = delivery, "numeric_unverified", marked
				existingID, truncated = "", false
			}
		}
	}
	if !terminalCandidateFallback(answer) && e.evidenceCompletionEnabled() && (outcome == "answer" || outcome == "partial" || coverageDraft ||
		outcome == "stalled" && e.candidateApprovalCurrent(answer, state)) {
		base := answer
		answer = e.terminalCoverageDelivery(ctx, state, false).render(false, base)
		if answer != base {
			existingID = ""
		}
		if coverageDraft || outcome == "partial" {
			e.bindCoverageDraftDelivery(base, answer)
		} else if answer != base && e.candidateApprovalCurrent(base, state) {
			// The candidate kept its verified claims. Bind persistent inline
			// citations to the final bytes after the runtime added its facts.
			e.turnReview.Last.Candidate, e.turnReview.Last.CandidateHash = answer, candidateTextDigest(answer)
		}
	}
	if comparison, complete, handled := e.terminalTableComparison(ctx, state); handled {
		if comparison != answer {
			existingID = ""
		}
		answer, truncated = comparison, false
		markedCitations, markedNumbers = 0, 0
		outcome = "evidence_partial"
		if complete {
			outcome = "answer"
		}
		e.turnReview.TableComparisonDelivery = tableComparisonDeliveryReceipt{candidateTextDigest(answer), e.tableComparisonDeliveryRevision(ctx, state)}
	} else if e.evidenceCompletionEnabled() && e.evidenceCompletion.legacyGap != "" && e.evidenceCompletion.stopReason != "" &&
		len(e.evidenceCompletionTargets()) == 0 && !e.candidateApprovalCurrent(answer, state) {
		// A draft cannot settle an unresolved semantic requirement without a
		// current association. Preserve the verified primary-gap route, which
		// reports that gap and separately attributed source evidence.
		delivery := e.terminalCoverageDelivery(ctx, state, true)
		if !delivery.hasGap() {
			answer, existingID, outcome, truncated = delivery.render(true, ""), "", "evidence_partial", false
		}
	}
	answer = stripCoverageAnswer(answer)
	if terminalCandidateFallback(answer) {
		answer, existingID, outcome, truncated = e.terminalCoverageFailure(ctx, state), "", "evidence_incomplete", false
	}
	e.turnReview.Committed = true
	state.FinalAnswer = answer
	state.IsComplete = true
	id := e.publishAnswer(ctx, sessionID, answer, existingID, truncated)
	e.turnReview.TerminalStreamID, e.turnReview.TerminalTruncated = id, truncated
	if !e.turnReview.DeferTerminalClose {
		e.closeAnswerStream(ctx, sessionID, id, truncated)
	}
	common.PipelineInfo(ctx, "Agent", "terminal_commit", map[string]interface{}{"outcome": outcome, "candidate_hash": candidateTextDigest(answer), "repairs": e.turnReview.Repairs, "checks": e.turnReview.Checks, "marked_numeric_count": markedNumbers, "marked_citation_count": markedCitations})
}
