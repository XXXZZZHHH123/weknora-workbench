package agent

import (
	"context"
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
	id := generateEventID("answer")
	if e.eventBus != nil {
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

// Only checked candidates or explicit runtime failure messages reach this
// terminal outlet. IsComplete remains loop termination, not semantic success.
func (e *AgentEngine) commitTerminalResult(ctx context.Context, state *types.AgentState, sessionID, answer, existingID, outcome string, truncated bool) {
	if ctx.Err() != nil || e.turnReview.Committed {
		return
	}
	e.turnReview.Committed = true
	if outcome == "answer" && e.needsSourceConsistencyCheck(state) &&
		!e.candidateApprovalCurrent(answer, state) {
		answer = answerReviewUnavailableFallback
		existingID = ""
		outcome = "failed"
		truncated = false
	}
	// Review failure or exhausted revisions: deliver the latest numerically
	// grounded candidate with a notice rather than a bare failure message.
	if answer == answerReviewUnavailableFallback || answer == incompleteAnswerFallback || answer == stalledAnswerFallback ||
		answer == numericFailureFallback || answer == finalNumericFailureFallback {
		if delivery, ok := e.unverifiedDelivery(); ok {
			answer = delivery
			existingID = ""
			outcome = "unverified"
			truncated = false
		}
	}
	id := e.publishAnswer(ctx, sessionID, answer, existingID, truncated)
	e.closeAnswerStream(ctx, sessionID, id, truncated)
	state.FinalAnswer = answer
	state.IsComplete = true
	common.PipelineInfo(ctx, "Agent", "terminal_commit", map[string]interface{}{"outcome": outcome, "candidate_hash": candidateTextDigest(answer), "repairs": e.turnReview.Repairs, "checks": e.turnReview.Checks})
}
