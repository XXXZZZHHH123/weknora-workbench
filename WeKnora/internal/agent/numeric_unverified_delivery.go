package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// A terminal warning is not approval. Keep a private binding for the exact
// marked presentation and its navigation references, independently of the
// semantic-review fallback and complete-chart receipts.
type numericUnverifiedDeliveryBinding struct {
	FinalText, ReferencesHash string
	Marked                    int
}

func (e *AgentEngine) numericUnverifiedCitationsCurrent(candidate string, state *types.AgentState) bool {
	if e == nil || e.config == nil || state == nil {
		return false
	}
	ids, evidenceIDs := uniqueCandidateCitationIDs(candidate), uniqueCandidateEvidenceIDs(candidate)
	if !e.config.CitationsEnabled() && len(ids)+len(evidenceIDs) > 0 {
		return false
	}
	refs := e.currentCandidateKnowledgeReferences(candidate, state, e.turnReview.Conversation)
	evidenceRefs := e.currentCandidateEvidenceReferences(candidate, state)
	if len(ids) != len(refs) || len(evidenceIDs) != len(evidenceRefs) {
		return false
	}
	chunks, evidence := map[string]bool{}, map[string]bool{}
	for _, ref := range refs {
		chunks[ref.ID] = true
	}
	for _, ref := range evidenceRefs {
		evidence[ref.ID] = true
	}
	// Unknown or malformed tags cannot disappear silently on this path.
	return filterFallbackCitationTags(candidate, evidence, chunks) == candidate
}

func (e *AgentEngine) rememberNumericUnverifiedCandidate(ctx context.Context, decision candidateDecision, state *types.AgentState) {
	if ctx.Err() != nil || decision.Disposition != candidateNumericFailure || decision.ReasonCode != "numeric_tokens_unmatched" ||
		decision.Err != nil || len(decision.Unsupported) == 0 || strings.TrimSpace(decision.Candidate) == "" {
		return
	}
	base := decision.Candidate
	if decoded, ok := decodeReviewedAnswerEnvelope(base); ok {
		base = decoded
		// Transfer escapes and JSON attributes are not the visible answer.
		// Preserve the original checked hash while validating the exact decoded
		// body independently; an empty diagnostic does not qualify here.
		evidence := e.answerNumericEvidence(base, state.RoundSteps, e.turnReview.Conversation)
		decision.Unsupported = ungroundedStructuredNumbers(base, evidence, state.RoundSteps)
	}
	if len(decision.Unsupported) == 0 || !e.candidateEvidenceBudgetFits(base, state) ||
		!e.numericUnverifiedCitationsCurrent(base, state) || !e.numericUnverifiedImagesCurrent(base, state) {
		return
	}
	e.turnReview.NumericUnverifiedDecision = decision
}

func (e *AgentEngine) numericUnverifiedPresentation(ctx context.Context, state *types.AgentState) (string, int, types.EvidenceReferences, []*types.SearchResult, bool) {
	d := e.turnReview.NumericUnverifiedDecision
	if ctx.Err() != nil || d.Disposition != candidateNumericFailure || d.ReasonCode != "numeric_tokens_unmatched" || d.Err != nil ||
		d.SourceScopeHash == "" || d.SourceScopeHash != e.unverifiedSourceScopeDigest(ctx) ||
		!e.candidateDecisionCurrent(d, d.Candidate, state) {
		return "", 0, nil, nil, false
	}
	base := d.Candidate
	if decoded, ok := decodeReviewedAnswerEnvelope(base); ok {
		base = decoded
	}
	if !e.numericUnverifiedCitationsCurrent(base, state) || !e.numericUnverifiedImagesCurrent(base, state) || !e.candidateEvidenceBudgetFits(base, state) {
		return "", 0, nil, nil, false
	}
	// Re-run the unchanged matcher on the decoded presentation. JSON escape
	// offsets never become offsets in visible prose, and a changed diagnostic
	// cannot inherit the earlier candidate's terminal warning.
	evidence := e.answerNumericEvidence(base, state.RoundSteps, e.turnReview.Conversation)
	if reviewDigest(ungroundedStructuredNumbers(base, evidence, state.RoundSteps)) != reviewDigest(d.Unsupported) {
		return "", 0, nil, nil, false
	}
	occurrences := ungroundedStructuredNumberOccurrences(base, evidence, state.RoundSteps)
	marked, count, ok := annotateUnverifiedNumbers(base, occurrences)
	if !ok || count == 0 {
		return "", 0, nil, nil, false
	}
	delivery := marked + "\n\n> 提示：标注〔未核验〕的数字未能与本轮取数结果逐项核对，请以引用的原表、原文或数据为准。"
	if visible := visibleNumericWarningTokens(d.Unsupported); len(visible) > 0 {
		delivery += "未核验数字：“" + strings.Join(visible, "、") + "”。"
	}
	refs, legacy := e.unverifiedNavigationReferences(delivery, state)
	if len(refs) != len(uniqueCandidateEvidenceIDs(delivery)) || len(legacy) != len(uniqueCandidateCitationIDs(delivery)) ||
		!numericUnverifiedReferenceBudgetFits(refs, legacy) {
		return "", 0, nil, nil, false
	}
	return delivery, count, refs, legacy, true
}

func numericUnverifiedReferenceBudgetFits(refs types.EvidenceReferences, legacy []*types.SearchResult) bool {
	if len(refs)+len(legacy) > types.MaxMessageEvidenceReferences {
		return false
	}
	total := 0
	for _, ref := range refs {
		encoded, err := json.Marshal(ref.StorageEnvelope())
		if err != nil || ref.ValidateStored() != nil {
			return false
		}
		total += len(encoded)
	}
	for _, ref := range legacy {
		encoded, err := json.Marshal(ref)
		if err != nil {
			return false
		}
		total += len(encoded)
	}
	return total <= types.MaxMessageEvidenceBytes
}

func (e *AgentEngine) currentNumericUnverifiedDelivery(ctx context.Context, state *types.AgentState) (string, int, bool) {
	delivery, count, refs, legacy, ok := e.numericUnverifiedPresentation(ctx, state)
	if !ok {
		return "", 0, false
	}
	e.turnReview.NumericUnverifiedDelivery = &numericUnverifiedDeliveryBinding{
		FinalText: delivery, ReferencesHash: unverifiedReferenceDigest(refs, legacy), Marked: count,
	}
	return delivery, count, true
}

func (e *AgentEngine) publishNumericUnverifiedReferences(ctx context.Context, state *types.AgentState) bool {
	binding := e.turnReview.NumericUnverifiedDelivery
	delivery, count, refs, legacy, ok := e.numericUnverifiedPresentation(ctx, state)
	if !ok || state.FinalAnswer != binding.FinalText || delivery != binding.FinalText || count != binding.Marked ||
		unverifiedReferenceDigest(refs, legacy) != binding.ReferencesHash {
		// Completion/persistence must not publish a stale candidate or reference.
		state.FinalAnswer, refs, legacy = numericFailureFallback, nil, nil
	}
	state.EvidenceRefs, state.KnowledgeRefs = refs, legacy
	return true
}
