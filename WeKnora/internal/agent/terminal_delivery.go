package agent

import (
	"context"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/common"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
)

func relaxedTerminalEvidenceEnabled() bool {
	enabled, err := strconv.ParseBool(strings.TrimSpace(os.Getenv("WEKNORA_AGENT_RELAXED_TERMINAL_EVIDENCE")))
	return types.VerifiedPublicationEnabled() && (err != nil || enabled)
}

func finalReadAnswerEnabled() bool {
	enabled, err := strconv.ParseBool(strings.TrimSpace(os.Getenv("WEKNORA_AGENT_FINAL_READ_ANSWER")))
	return relaxedTerminalEvidenceEnabled() && (err != nil || enabled)
}

func substantiveFinalAnswerEnabled() bool {
	enabled, err := strconv.ParseBool(strings.TrimSpace(os.Getenv("WEKNORA_AGENT_SUBSTANTIVE_FINAL_ANSWER")))
	return finalReadAnswerEnabled() && (err != nil || enabled)
}

func scopedReadRecoveryEnabled() bool {
	enabled, err := strconv.ParseBool(strings.TrimSpace(os.Getenv("WEKNORA_AGENT_SCOPED_READ_RECOVERY")))
	return finalReadAnswerEnabled() && (err != nil || enabled)
}

func gapKnowledgeRecallEnabled() bool {
	enabled, err := strconv.ParseBool(strings.TrimSpace(os.Getenv("WEKNORA_AGENT_GAP_KNOWLEDGE_RECALL")))
	return finalReadAnswerEnabled() && err == nil && enabled
}

func (e *AgentEngine) finalReadPublicationEligible(state *types.AgentState) bool {
	return finalReadAnswerEnabled() && state != nil && state.Suspended == nil &&
		e.evidenceCompletionEligible() && e.standardTextDelivery(state)
}

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
	if types.VerifiedPublicationEnabled() && types.HasAgentAnswerMarkup(answer) {
		return ""
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
	terminalNoFailure               terminalFailure = ""
	terminalEmpty                   terminalFailure = "empty"
	terminalReview                  terminalFailure = "review_failed"
	terminalNumeric                 terminalFailure = "numeric_failed"
	terminalIncomplete              terminalFailure = "incomplete"
	terminalStalled                 terminalFailure = "stalled"
	terminalTruncated               terminalFailure = "truncated"
	terminalBudget                  terminalFailure = "budget_exhausted"
	terminalInterrupted             terminalFailure = "interrupted"
	terminalTableEvidence           terminalFailure = "table_evidence_unverified"
	terminalEndpointEvidence        terminalFailure = "endpoint_evidence_unverified"
	terminalAnswerEvidence          terminalFailure = "answer_evidence_missing"
	terminalSourceStalled           terminalFailure = "source_stalled"
	terminalSourceChanged           terminalFailure = "source_changed"
	terminalCollaborationContention terminalFailure = "collaboration_contention"
	terminalFieldInput              terminalFailure = "field_confirmation_input"
	terminalFieldService            terminalFailure = "field_confirmation_service"
	terminalFieldBlocked            terminalFailure = "field_confirmation_preflight"
	terminalFieldResult             terminalFailure = "field_confirmation_result"
)

type terminalPublicationReceipt struct {
	answerHash    string
	evidence      terminalEvidenceSnapshot
	staleCalls    map[string]bool
	failure       terminalFailure
	notice        string
	partial       bool
	partialReason terminalFailure
	fromEvidence  bool
	streamed      string
	submitted     string
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
	submitted := original
	answer = stripCoverageAnswer(answer)
	failure := terminalNoFailure
	if len(failures) != 0 {
		failure = failures[0]
	}
	if failure == terminalNoFailure && terminalCandidateFallback(answer) {
		failure = legacyTerminalFailure(answer)
	}

	last := e.turnReview.Last
	if failure == terminalNoFailure && e.evidenceCompletionEligible() && e.standardTextDelivery(state) &&
		last.Disposition == candidateNumericFailure && last.CandidateHash == candidateTextDigest(answer) {
		failure = terminalNumeric
	}
	finalRead := e.finalReadPublicationEligible(state)
	approvedBeforeRead, readMore := false, false
	if finalRead {
		prior := e.turnReview.FinalScopedReadApproval
		approvedBeforeCloseRead := prior != "" && last.Disposition == candidateAccepted && prior == scopedFinalReadApprovalKey(last)
		approvedBeforeRead = approvedBeforeCloseRead || e.candidateApprovalCurrent(last.Candidate, state)
		readMore = e.recoverFinalReadings(ctx, state, sessionID) || approvedBeforeCloseRead
		if ctx.Err() != nil {
			return
		}
	}
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
	if evidenceFailure != terminalNoFailure && failure != terminalSourceChanged && !terminalCandidateFallback(answer) &&
		(failure == terminalNoFailure || evidenceFailure != terminalAnswerEvidence) {
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
			if submittedHash == approvedHash && (approvedBeforeRead || e.candidateApprovalCurrent(last.Candidate, state) || coveredPartial) {
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
		} else if failure == terminalCollaborationContention {
			// Durable finalization recovered without an approved draft.
			// Render from current receipts below.
		} else if evidenceFailure != terminalNoFailure && evidenceFailure != terminalAnswerEvidence {
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
	fromEvidence := false
	if finalRead {
		current := terminalCurrentState(state, staleCalls)
		result := e.selectFinalReadPublication(ctx, current, answer, failure, outcome)
		if result.fromEvidence {
			answer, outcome, truncated = result.answer, "evidence_partial", false
			e.turnReview.TerminalReceipt.notice, e.turnReview.TerminalReceipt.partial = result.notice, result.partial
			fromEvidence = true
			state.EvidenceRefs, state.KnowledgeRefs = nil, nil
			e.turnReview.UnverifiedCitationDelivery = nil
			e.turnReview.CitationUnverifiedDelivery = nil
			e.turnReview.NumericUnverifiedDelivery = nil
			markedNumbers, markedCitations = 0, 0
		}
		if result.complete {
			failure, outcome = terminalNoFailure, "answer"
		} else if failure == terminalNoFailure && result.notice != "" {
			failure = terminalAnswerEvidence
		}
		if result.recordIncompleteEndpoint(answer, &e.turnReview.TerminalReceipt) {
			outcome = "evidence_partial"
		}
		// Directed reads can add receipts after a draft was approved. Its
		// original sources remain current, although the larger transcript
		// no longer matches the approval's full evidence hash.
		fromEvidence = fromEvidence || readMore && approvedBeforeRead && failure == terminalNoFailure && answer == last.Candidate
	} else if failure != terminalNoFailure {
		e.turnReview.UnverifiedCitationDelivery = nil
		e.turnReview.CitationUnverifiedDelivery = nil
		e.turnReview.NumericUnverifiedDelivery = nil
		markedNumbers, markedCitations = 0, 0
		result := e.terminalCoveragePublication(ctx, terminalCurrentState(state, staleCalls), failure)
		answer, truncated = result.answer, false
		outcome = "evidence_partial"
		if result.notice != "" {
			outcome = "evidence_incomplete"
		}
		e.turnReview.TerminalReceipt.notice = result.notice
		e.turnReview.TerminalReceipt.partial = result.partial
		fromEvidence = result.answer != ""
		if result.complete {
			failure, outcome = terminalNoFailure, "answer"
		}
		state.EvidenceRefs, state.KnowledgeRefs = nil, nil
	}
	if !finalRead && failure != terminalSourceChanged {
		withSupplement := e.retainReadSupplement(ctx, state, answer)
		fromEvidence = fromEvidence || withSupplement != answer
		answer = withSupplement
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
		staleCalls:    staleCalls,
		notice:        e.turnReview.TerminalReceipt.notice,
		partial:       e.turnReview.TerminalReceipt.partial,
		partialReason: e.turnReview.TerminalReceipt.partialReason,
		fromEvidence:  fromEvidence,
		streamed:      original,
		submitted:     submitted,
	}
	if existingID == "" {
		e.turnReview.TerminalReceipt.streamed = ""
	}
	checkedPublication := types.VerifiedPublicationEnabled() && types.HasAgentAnswerMarkup(answer)
	if !checkedPublication {
		deferPublication := e.turnReview.DeferTerminalClose && (e.turnReview.TerminalSensitive || e.terminalCoverageContextKnown() ||
			failure != terminalNoFailure || finalRead && fromEvidence)
		if !deferPublication {
			e.publishTerminalStream(ctx, state, sessionID)
		}
		if !e.turnReview.DeferTerminalClose {
			e.closeAnswerStream(ctx, sessionID, e.turnReview.TerminalStreamID, truncated)
		}
	}
	common.PipelineInfo(ctx, "Agent", "terminal_commit", map[string]interface{}{"outcome": outcome, "failure": failure, "candidate_hash": candidateTextDigest(answer), "repairs": e.turnReview.Repairs, "checks": e.turnReview.Checks, "marked_numeric_count": markedNumbers, "marked_citation_count": markedCitations})
}

func (e *AgentEngine) terminalCandidateEvidenceFailure(ctx context.Context, state *types.AgentState, answer string) terminalFailure {
	if !e.evidenceCompletionEligible() || state == nil || !e.standardTextDelivery(state) {
		return terminalNoFailure
	}
	tables := agenttools.DocumentTableEvidenceEnabled()
	review := e.currentCoverageDraftReview(ctx, state, answer)
	assessment := e.assessCoverageDraft(ctx, review, answer, state)
	if assessment.endpointMismatch {
		return terminalEndpointEvidence
	}
	if review.requiresReading() && !assessment.verifiedReading {
		return terminalAnswerEvidence
	}
	if !tables {
		return terminalNoFailure
	}
	if len(e.evidenceCompletion.versionClaims[candidateTextDigest(strings.TrimSpace(answer))]) > 0 &&
		assessment.versionBlocked {
		return terminalReview
	}
	if len(assessment.unattributedTable) > 0 {
		return terminalTableEvidence
	}
	if !review.blockedComparison() {
		return terminalNoFailure
	}
	for _, decision := range []candidateDecision{e.turnReview.Last, e.turnReview.CoverageDraft} {
		if decision.CandidateHash == candidateTextDigest(answer) &&
			decision.PublicationEvidence.current(e.terminalEvidenceRevision(ctx, state)) &&
			comparisonDraftChecked(decision, assessment) {
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
