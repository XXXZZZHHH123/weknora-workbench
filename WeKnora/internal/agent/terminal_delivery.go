package agent

import (
	"context"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/common"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
)

// publishAnswer reuses an existing stream, or emits the buffered candidate
// once. Closing remains separate so a queued human correction can continue.
func (e *AgentEngine) publishAnswer(ctx context.Context, sessionID, answer, existingID string, truncated bool) string {
	if ctx.Err() != nil {
		return ""
	}
	if existingID != "" {
		return existingID
	}
	// analyzeResponse may encounter a runtime placeholder before the terminal
	// outlet selects a current candidate. Do not stream that placeholder and
	// then append its replacement as a second answer.
	if !e.turnReview.Committed && terminalCandidateFallback(answer) {
		return ""
	}
	id := generateEventID("answer")
	if e.eventBus != nil && !e.holdCollaborationAnswer {
		_ = e.eventBus.Emit(ctx, event.Event{ID: id, Type: event.EventAgentFinalAnswer, SessionID: sessionID, Data: event.AgentFinalAnswerData{Content: answer, Done: false, Truncated: truncated}})
	}
	return id
}

// Numeric-failure fallbacks. A later candidate that fails numeric grounding
// never replaces an earlier one that passed it; terminal delivery prefers that
// earlier candidate with a notice over this message.
const (
	numericFailureFallback      = "已经取得原表依据，但完整回答的部分数字未通过来源核验，暂不提供未经核验的统计结论。请查看取数依据。"
	finalNumericFailureFallback = "已有取数依据，但完整回答的部分数字未通过来源核验。此次暂不提供未经核验的统计结论，请查看来源或重试。"
)

func terminalCandidateFallback(answer string) bool {
	return answer == answerReviewUnavailableFallback || answer == incompleteAnswerFallback || answer == stalledAnswerFallback ||
		answer == sourceProgressStalledFallback || answer == truncatedAnswerFallback || answer == numericFailureFallback || answer == finalNumericFailureFallback
}

// Only checked candidates or explicit runtime failure messages reach this
// terminal outlet. IsComplete remains loop termination, not semantic success.
func (e *AgentEngine) commitTerminalResult(ctx context.Context, state *types.AgentState, sessionID, answer, existingID, outcome string, truncated bool) {
	if ctx.Err() != nil || e.turnReview.Committed {
		return
	}
	e.turnReview.Committed = true
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
	if terminalCandidateFallback(answer) {
		if delivery := e.terminalEvidenceCompletionDelivery(ctx, state); delivery != "" {
			answer, existingID, outcome, truncated = delivery, "", "evidence_incomplete", false
		}
	}
	id := e.publishAnswer(ctx, sessionID, answer, existingID, truncated)
	e.closeAnswerStream(ctx, sessionID, id, truncated)
	state.FinalAnswer = answer
	state.IsComplete = true
	common.PipelineInfo(ctx, "Agent", "terminal_commit", map[string]interface{}{"outcome": outcome, "candidate_hash": candidateTextDigest(answer), "repairs": e.turnReview.Repairs, "checks": e.turnReview.Checks, "marked_numeric_count": markedNumbers, "marked_citation_count": markedCitations})
}
