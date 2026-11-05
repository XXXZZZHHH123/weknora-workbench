package agent

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/Tencent/WeKnora/internal/common"
	"github.com/Tencent/WeKnora/internal/types"
)

// This private binding permits navigation to actual observations alongside a
// failed-review warning. It is deliberately separate from candidateDecision,
// reviewer receipts and complete-chart delivery. No saved/public field creates it.
type unverifiedCitationDeliveryBinding struct {
	CheckedHash, BaseHash, WarningHash, FinalHash string
	TaskHash, EvidenceHash, ScopeHash             string
	ReferencesHash                                string
	FinalText                                     string
	Eligible                                      bool
}

var fallbackCitationTag = regexp.MustCompile(`(?is)<(?:evidence|kb)\b[^>]*(?:>|$)|</(?:evidence|kb)\s*>`)

func (e *AgentEngine) unverifiedSourceScopeDigest(ctx context.Context) string {
	if e == nil || e.config == nil {
		return ""
	}
	principal, hasPrincipal := types.PrincipalFromContext(ctx)
	key, hasKey := types.TenantAPIKeyScopeFromContext(ctx)
	return reviewDigest(map[string]any{
		"caller": types.CallerFromContext(ctx), "principal": principal, "has_principal": hasPrincipal,
		"key_scope": key, "has_key_scope": hasKey,
		"knowledge_bases": e.config.KnowledgeBases, "knowledge_ids": e.config.KnowledgeIDs,
		"search_targets":  e.config.SearchTargets,
		"question_origin": e.config.QuestionOrigin, "page_context": e.config.PageContext,
		"shared_agent_read_only": e.config.SharedAgentReadOnly, "citations": e.config.CitationsEnabled(),
		"evidence_client": e.config.EvidenceReferencesEnabled,
	})
}

// Keep only complete canonical tags in the selected base. Any malformed,
// unknown, stale or over-budget tag disappears; the answer and honest warning
// remain. Warning text itself never chooses an additional source.
func filterFallbackCitationTags(text string, evidence, chunks map[string]bool) string {
	return fallbackCitationTag.ReplaceAllStringFunc(text, func(tag string) string {
		if match := answerEvidenceCitation.FindStringSubmatch(tag); len(match) == 2 && match[0] == tag && evidence[match[1]] {
			return tag
		}
		if match := documentAnswerCitation.FindStringSubmatch(tag); len(match) == 2 && match[0] == tag && chunks[match[1]] {
			return tag
		}
		return ""
	})
}

func (e *AgentEngine) unverifiedBaseCurrent(ctx context.Context, state *types.AgentState) bool {
	if e == nil || e.config == nil || state == nil || !e.config.CitationsEnabled() {
		return false
	}
	d := e.turnReview.UnverifiedDecision
	if d.Disposition != candidateRevise && d.Disposition != candidateNeedSource && d.Disposition != candidateProtocolFailure {
		return false
	}
	if !d.SourceReviewAttempted || len(d.Unsupported) != 0 || d.SourceScopeHash == "" || d.SourceScopeHash != e.unverifiedSourceScopeDigest(ctx) ||
		!e.candidateDecisionCurrent(d, e.turnReview.UnverifiedCheckedCandidate, state) {
		return false
	}
	base := e.turnReview.UnverifiedCheckedCandidate
	if decoded, ok := decodeReviewedAnswerEnvelope(base); ok && strings.TrimSpace(decoded) != "" {
		base = decoded
	}
	return strings.TrimSpace(base) == strings.TrimSpace(e.turnReview.UnverifiedCandidate)
}

func unverifiedInlineBindings(candidate, id string) json.RawMessage {
	encoded, _ := json.Marshal(map[string]any{
		"protocol": "unverified_navigation_v1", "candidate_sha256": candidateTextDigest(candidate),
		"navigation_only": true, "review_status": "unverified",
		"inline": json.RawMessage(structuredInlineBindings(candidate, id)),
		"notice": "Current execution observations for user checking. The answer has not completed semantic review; an inline position is not proof of its adjacent claim.",
	})
	return encoded
}

func (e *AgentEngine) unverifiedNavigationReferences(candidate string, state *types.AgentState) (types.EvidenceReferences, []*types.SearchResult) {
	refs := types.EvidenceReferences{}
	if e.config != nil && e.config.EvidenceReferencesEnabled {
		for _, ref := range e.currentCandidateEvidenceReferences(candidate, state) {
			ref.ReviewStatus = "unverified"
			ref.ClaimBindings = unverifiedInlineBindings(candidate, ref.ID)
			refs = append(refs, ref)
		}
	}
	// These are the same actual-read/content/version checks as the approved
	// legacy path, never a saved reference or same-page/source-name match.
	chunks := e.currentCandidateKnowledgeReferences(candidate, state, e.turnReview.Conversation)
	return refs, chunks
}

func unverifiedReferenceDigest(refs types.EvidenceReferences, chunks []*types.SearchResult) string {
	envelopes := make([]types.StoredEvidenceEnvelope, 0, len(refs))
	for _, ref := range refs {
		envelopes = append(envelopes, ref.StorageEnvelope())
	}
	return reviewDigest([]any{envelopes, chunks})
}

func (e *AgentEngine) prepareUnverifiedCitationDelivery(ctx context.Context, state *types.AgentState, delivery string) string {
	e.turnReview.UnverifiedCitationDelivery = nil
	base := strings.TrimSpace(e.turnReview.UnverifiedCandidate)
	// Only the existing server formatter may supply this particular warning.
	expected, ok := e.unverifiedDelivery()
	if !ok || delivery != expected || !strings.HasPrefix(delivery, base) {
		return filterFallbackCitationTags(delivery, nil, nil)
	}
	warning := filterFallbackCitationTags(strings.TrimPrefix(delivery, base), nil, nil)
	evidence, chunks := map[string]bool{}, map[string]bool{}
	eligible := e.unverifiedBaseCurrent(ctx, state)
	if eligible {
		refs, legacy := e.unverifiedNavigationReferences(base, state)
		for _, ref := range refs {
			evidence[ref.ID] = true
		}
		for _, ref := range legacy {
			chunks[ref.ID] = true
		}
	}
	clean := filterFallbackCitationTags(base, evidence, chunks)
	omitted := clean != base
	missingNotice := "\n\n> 部分引用无法提供核查入口，请以实际可打开的来源为准。"
	var refs types.EvidenceReferences
	var legacy []*types.SearchResult
	final := ""
	// Removing a tag changes positions/hashes, so rebuild bindings and measure
	// exact storage envelopes until the set fits. Never truncate a manifest or
	// retain a tag whose corresponding record cannot be stored.
	for {
		final = clean + warning
		if omitted {
			final += missingNotice
		}
		refs, legacy = e.unverifiedNavigationReferences(final, state)
		rejected := false
		total, count := 0, 0
		for _, ref := range refs {
			encoded, err := json.Marshal(ref.StorageEnvelope())
			if ref.ValidateStored() != nil || err != nil || count >= types.MaxMessageEvidenceReferences || total+len(encoded) > types.MaxMessageEvidenceBytes {
				delete(evidence, ref.ID)
				rejected = true
				continue
			}
			count++
			total += len(encoded)
		}
		for _, ref := range legacy {
			encoded, err := json.Marshal(ref)
			if err != nil || count >= types.MaxMessageEvidenceReferences || total+len(encoded) > types.MaxMessageEvidenceBytes {
				delete(chunks, ref.ID)
				rejected = true
				continue
			}
			count++
			total += len(encoded)
		}
		if !rejected {
			break
		}
		clean = filterFallbackCitationTags(base, evidence, chunks)
		omitted = true
	}
	d := e.turnReview.UnverifiedDecision
	e.turnReview.UnverifiedCitationDelivery = &unverifiedCitationDeliveryBinding{
		CheckedHash: d.CandidateHash, BaseHash: candidateTextDigest(base), WarningHash: candidateTextDigest(warning), FinalHash: candidateTextDigest(final),
		TaskHash: d.TaskHash, EvidenceHash: d.EvidenceHash, ScopeHash: e.unverifiedSourceScopeDigest(ctx),
		ReferencesHash: unverifiedReferenceDigest(refs, legacy), FinalText: final, Eligible: eligible,
	}
	return final
}

func (e *AgentEngine) currentUnverifiedCitationReferences(ctx context.Context, state *types.AgentState) (types.EvidenceReferences, []*types.SearchResult, bool) {
	binding := e.turnReview.UnverifiedCitationDelivery
	if binding == nil || state == nil || candidateTextDigest(state.FinalAnswer) != binding.FinalHash {
		return nil, nil, false
	}
	if !binding.Eligible {
		return nil, nil, len(uniqueCandidateEvidenceIDs(state.FinalAnswer))+len(uniqueCandidateCitationIDs(state.FinalAnswer)) == 0
	}
	d := e.turnReview.UnverifiedDecision
	delivery, ok := e.unverifiedDelivery()
	base := strings.TrimSpace(e.turnReview.UnverifiedCandidate)
	warning := filterFallbackCitationTags(strings.TrimPrefix(delivery, base), nil, nil)
	if !ok || !e.unverifiedBaseCurrent(ctx, state) || binding.CheckedHash != d.CandidateHash || binding.BaseHash != candidateTextDigest(base) ||
		binding.WarningHash != candidateTextDigest(warning) || binding.TaskHash != d.TaskHash || binding.EvidenceHash != d.EvidenceHash || binding.ScopeHash != e.unverifiedSourceScopeDigest(ctx) {
		return nil, nil, false
	}
	refs, legacy := e.unverifiedNavigationReferences(state.FinalAnswer, state)
	if len(refs) != len(uniqueCandidateEvidenceIDs(state.FinalAnswer)) || len(legacy) != len(uniqueCandidateCitationIDs(state.FinalAnswer)) || unverifiedReferenceDigest(refs, legacy) != binding.ReferencesHash {
		return nil, nil, false
	}
	return refs, legacy, true
}

func (e *AgentEngine) publishUnverifiedCitationReferences(ctx context.Context, state *types.AgentState) bool {
	if e.turnReview.UnverifiedCitationDelivery == nil {
		return false
	}
	refs, chunks, valid := e.currentUnverifiedCitationReferences(ctx, state)
	if !valid {
		// A changed task, result, scope or final candidate cannot inherit the
		// navigation binding. Retain the warning; remove every unusable tag.
		state.FinalAnswer = filterFallbackCitationTags(e.turnReview.UnverifiedCitationDelivery.FinalText, nil, nil)
		refs, chunks = nil, nil
	}
	state.EvidenceRefs, state.KnowledgeRefs = refs, chunks
	common.PipelineInfo(ctx, "Agent", "unverified_citation_delivery", map[string]any{
		"review_status": "unverified", "binding_current": valid, "candidate_hash": candidateTextDigest(state.FinalAnswer),
		"published_evidence_count": len(refs), "published_reference_count": len(chunks),
	})
	return true
}
