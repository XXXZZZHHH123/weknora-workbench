package agent

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"strings"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

// This is a receipt for a marked presentation, never citation approval.
type citationUnverifiedDeliveryBinding struct {
	Decision                       candidateDecision
	FinalText, ReferencesHash      string
	MarkedCitations, MarkedNumbers int
}

func (e *AgentEngine) candidateCitationsInvalid(candidate string, state *types.AgentState, conversation []chat.Message) bool {
	if len(uniqueCandidateCitationIDs(candidate)) != len(e.currentCandidateKnowledgeReferences(candidate, state, conversation)) ||
		len(uniqueCandidateEvidenceIDs(candidate)) != len(e.currentCandidateEvidenceReferences(candidate, state)) ||
		(!e.config.CitationsEnabled() && len(uniqueCandidateCitationIDs(candidate))+len(uniqueCandidateEvidenceIDs(candidate)) > 0) {
		return true
	}
	views := currentCitationSourceViews(state)
	for _, match := range documentAnswerCitation.FindAllStringSubmatch(candidate, -1) {
		if !citationWarningTagScopeSafe(match[0], views[match[1]]) {
			return true
		}
	}
	return false
}

func (e *AgentEngine) citationIdentityFailure(d candidateDecision, state *types.AgentState, conversation []chat.Message) candidateDecision {
	d.ReasonCode = "citation_identity_invalid"
	feedback, counts, needsRead := e.candidateCitationRecovery(d.Candidate, state, conversation)
	d.CitationIssueCounts = counts
	e.answerReviewFeedback, e.answerReviewAction = feedback, reviewReviseAnswer
	d.Disposition = candidateRevise
	if needsRead {
		e.answerReviewAction, d.Disposition = reviewCollectEvidence, candidateNeedSource
	}
	return d
}

// A missing/ambiguous presentation can be labelled only when its ID really
// belongs to current successful observations in one document/library/version.
// Unknown IDs, cross-library/document observations and version conflicts are
// security/staleness failures, not candidates for a presentation warning.
func (e *AgentEngine) citationWarningIdentitySafe(id string, views []citationSourceView) bool {
	if e.modelContext == nil || e.modelContext.CitableChunkHandle(id) == "" || len(views) == 0 {
		return false
	}
	document, library := "", ""
	revision, revisionKnown := 0, false
	projectedRevisions := []int{}
	for _, view := range views {
		row := view.row
		if status, _ := view.data["status"].(string); status != "" && status != "ok" {
			return false
		}
		doc := citationString(row["knowledge_id"])
		kb := citationString(row["knowledge_base_id"])
		if alias := citationString(row["knowledge_base"]); alias != "" {
			if kb != "" && kb != alias {
				return false
			}
			kb = alias
		}
		if citationString(view.data["knowledge_id"]) == doc {
			if parent := citationString(view.data["knowledge_base_id"]); parent != "" {
				if kb != "" && kb != parent {
					return false
				}
				kb = parent
			}
		}
		if citationString(row["chunk_id"]) != id || doc == "" || kb == "" ||
			(document != "" && doc != document) || (library != "" && kb != library) {
			return false
		}
		document, library = doc, kb
		current, known := citationViewRevision(row["content_revision"])
		if row["content_revision"] != nil && !known {
			return false
		}
		if known {
			if revisionKnown && revision != current {
				return false
			}
			revision, revisionKnown = current, true
		}
		if projection := row["content_source_view"]; projection != nil {
			raw, err := json.Marshal(projection)
			var proof types.ChunkContentView
			if err != nil || json.Unmarshal(raw, &proof) != nil || proof.OriginRevision < 0 {
				return false
			}
			projectedRevisions = append(projectedRevisions, proof.OriginRevision)
		}
		if content, ok := row["content"].(string); !ok || strings.TrimSpace(content) == "" || row["content_rewritten"] == true {
			return false
		}
	}
	for _, originRevision := range projectedRevisions {
		if !revisionKnown || originRevision != revision {
			return false
		}
	}
	return revisionKnown && e.citationWarningScopeAllows(document, library)
}

func (e *AgentEngine) citationWarningScopeAllows(document, library string) bool {
	// Actual tool execution owns ACL checks. Where a runtime allow-list exists,
	// also intersect it here; a receipt cannot expand that scope.
	for _, target := range e.config.SearchTargets {
		if target == nil || target.KnowledgeBaseID != library {
			continue
		}
		if len(target.KnowledgeIDs) == 0 {
			return true
		}
		for _, id := range target.KnowledgeIDs {
			if id == document {
				return true
			}
		}
	}
	if len(e.config.SearchTargets) > 0 {
		return false
	}
	for _, kb := range e.config.KnowledgeBases {
		if kb == library {
			return true
		}
	}
	for _, id := range e.config.KnowledgeIDs {
		if id == document {
			return true
		}
	}
	return len(e.config.SearchTargets)+len(e.config.KnowledgeBases)+len(e.config.KnowledgeIDs) == 0
}

// Parse the fixed citation protocol, including aliases and duplicate
// attributes. A substring match must never hide a second identity assertion.
func citationWarningTagScopeSafe(tag string, views []citationSourceView) bool {
	decoder := xml.NewDecoder(strings.NewReader(tag))
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	start, ok := token.(xml.StartElement)
	if !ok || start.Name.Local != "kb" || start.Name.Space != "" {
		return false
	}
	seen := map[string]bool{}
	for _, attribute := range start.Attr {
		name := attribute.Name.Local
		if attribute.Name.Space != "" || seen[name] {
			return false
		}
		seen[name] = true
		if name == "doc" {
			continue
		}
		key := name
		switch name {
		case "kb_id", "knowledge_base_id":
			key = "knowledge_base_id"
		case "knowledge_id", "chunk_id":
		default:
			return false
		}
		for _, view := range views {
			actual := citationString(view.row[key])
			if actual == "" && key == "knowledge_base_id" {
				actual = citationString(view.row["knowledge_base"])
			}
			if actual == "" && citationString(view.data["knowledge_id"]) == citationString(view.row["knowledge_id"]) {
				actual = citationString(view.data[key])
			}
			if actual == "" || actual != attribute.Value {
				return false
			}
		}
	}
	return seen["chunk_id"]
}

func (e *AgentEngine) citationUnverifiedPresentation(ctx context.Context, state *types.AgentState, d candidateDecision) (string, int, int, types.EvidenceReferences, []*types.SearchResult, bool) {
	if ctx.Err() != nil || state == nil || d.Err != nil || d.ReasonCode != "citation_identity_invalid" ||
		!e.config.CitationsEnabled() || d.SourceScopeHash == "" || d.SourceScopeHash != e.unverifiedSourceScopeDigest(ctx) ||
		!e.candidateDecisionCurrent(d, d.Candidate, state) {
		return "", 0, 0, nil, nil, false
	}
	base := d.Candidate
	if decoded, ok := decodeReviewedAnswerEnvelope(base); ok {
		base = decoded
	}
	if !e.numericUnverifiedImagesCurrent(base, state) || !e.candidateEvidenceBudgetFits(base, state) {
		return "", 0, 0, nil, nil, false
	}
	valid := map[string]bool{}
	for _, ref := range e.currentCandidateKnowledgeReferences(base, state, e.turnReview.Conversation) {
		valid[ref.ID] = true
	}
	// Structured evidence IDs have an execution binding. An invalid binding is
	// never downgraded; it may be a forged ID or expired result.
	if len(uniqueCandidateEvidenceIDs(base)) != len(e.currentCandidateEvidenceReferences(base, state)) {
		return "", 0, 0, nil, nil, false
	}
	views := currentCitationSourceViews(state)
	selected := map[string]bool{}
	for _, id := range uniqueCandidateCitationIDs(base) {
		if !e.citationWarningIdentitySafe(id, views[id]) {
			return "", 0, 0, nil, nil, false
		}
		selected[id] = true
	}
	for _, step := range state.RoundSteps {
		for _, call := range step.ToolCalls {
			if e.toolRegistry == nil || (call.ExecutionName() != "read_document" && call.ExecutionName() != "search_knowledge") || call.Result == nil || !call.Result.Success {
				continue
			}
			for _, chunk := range fetchedDocumentChunks(call) {
				current := agenttools.IsCurrentDocumentRead(e.toolRegistry, call.Result)
				if call.ExecutionName() == "search_knowledge" {
					current = agenttools.IsCurrentDocumentSearchChunk(e.toolRegistry, call.Result, chunk.KnowledgeID)
				}
				if selected[chunk.ID] && !current {
					return "", 0, 0, nil, nil, false
				}
			}
		}
	}
	invalidChunks := map[string]bool{}
	malformed := false
	fallbackCitationTag.ReplaceAllStringFunc(base, func(tag string) string {
		if match := documentAnswerCitation.FindStringSubmatch(tag); len(match) == 2 && match[0] == tag {
			if !citationWarningTagScopeSafe(tag, views[match[1]]) {
				malformed = true
				return tag
			}
			if valid[match[1]] {
				return tag
			}
			invalidChunks[match[1]] = true
			return tag
		}
		if match := answerEvidenceCitation.FindStringSubmatch(tag); len(match) == 2 && match[0] == tag {
			return tag
		}
		malformed = true
		return tag
	})
	if malformed || len(invalidChunks) == 0 {
		return "", 0, 0, nil, nil, false
	}
	marked, markedCitations, ok := annotateUnverifiedCitations(base, invalidChunks)
	if !ok || markedCitations == 0 {
		return "", 0, 0, nil, nil, false
	}
	// Identity repair precedes numeric review in standard mode. Run the same
	// numeric matcher before delivery so this route cannot bypass it.
	evidence := e.answerNumericEvidence(base, state.RoundSteps, e.turnReview.Conversation)
	occurrences := ungroundedStructuredNumberOccurrences(marked, evidence, state.RoundSteps)
	markedNumbers := 0
	if len(occurrences) > 0 {
		var ok bool
		marked, markedNumbers, ok = annotateUnverifiedNumbers(marked, occurrences)
		if !ok {
			return "", 0, 0, nil, nil, false
		}
	}
	notice := "本次读取的资料未能确认带标记的引用，因此没有可打开的来源链接。请找到对应原文后再引用。"
	if markedNumbers > 0 {
		notice += "带标记的数字也待核对，写入报告前请对照原表或原文。"
	}
	delivery := marked + "\n\n> 提示：" + notice
	refs, legacy := e.unverifiedNavigationReferences(delivery, state)
	if len(refs) != len(uniqueCandidateEvidenceIDs(delivery)) || len(legacy) != len(uniqueCandidateCitationIDs(delivery)) || !numericUnverifiedReferenceBudgetFits(refs, legacy) {
		return "", 0, 0, nil, nil, false
	}
	return delivery, markedCitations, markedNumbers, refs, legacy, true
}

func (e *AgentEngine) currentCitationUnverifiedDelivery(ctx context.Context, state *types.AgentState) (string, int, int, bool) {
	d := e.turnReview.Last
	text, citations, numbers, refs, legacy, ok := e.citationUnverifiedPresentation(ctx, state, d)
	if ok {
		e.turnReview.CitationUnverifiedDelivery = &citationUnverifiedDeliveryBinding{Decision: d, FinalText: text,
			ReferencesHash: unverifiedReferenceDigest(refs, legacy), MarkedCitations: citations, MarkedNumbers: numbers}
	}
	return text, citations, numbers, ok
}

func (e *AgentEngine) publishCitationUnverifiedReferences(ctx context.Context, state *types.AgentState) bool {
	b := e.turnReview.CitationUnverifiedDelivery
	text, citations, numbers, refs, legacy, ok := e.citationUnverifiedPresentation(ctx, state, b.Decision)
	if !ok || state.FinalAnswer != b.FinalText || text != b.FinalText || citations != b.MarkedCitations || numbers != b.MarkedNumbers || unverifiedReferenceDigest(refs, legacy) != b.ReferencesHash {
		state.FinalAnswer, refs, legacy = answerReviewUnavailableFallback, nil, nil
	}
	state.EvidenceRefs, state.KnowledgeRefs = refs, legacy
	return true
}
