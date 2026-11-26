package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/common"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
)

type candidateDisposition string

const (
	candidateAccepted        candidateDisposition = "accepted"
	candidateRevise          candidateDisposition = "revise"
	candidateNeedSource      candidateDisposition = "need_source"
	candidateEvidencePartial candidateDisposition = "evidence_partial"
	candidateNumericFailure  candidateDisposition = "numeric_failure"
	candidateNumericWarning  candidateDisposition = "numeric_warning"
	candidateProtocolFailure candidateDisposition = "protocol_failure"
)

// A decision belongs to the actual candidate, human task and immutable tool
// observations sent to the checker. It is never a reusable boolean approval.
// Hashes are assigned by the runtime, not supplied by a model.
type candidateDecision struct {
	ReasonCode                   string
	CitationIssueCounts          map[string]int
	RepeatedUnsupported          []string
	Disposition                  candidateDisposition
	Candidate                    string
	CandidateHash                string
	TaskHash                     string
	EvidenceHash                 string
	SourceScopeHash              string // Runtime scope at the check; never supplied by a model.
	StandardReview               bool   // Approval cannot survive a change to the runtime review policy.
	StructuredResultReview       bool
	StructuredResultReviewStatus string // Empty/skipped/unavailable never means semantically checked.
	// Set only after the numeric guards and immediately before source review.
	SourceReviewAttempted bool
	Unsupported           []string
	NumericFeedback       map[string]any
	Err                   error
}

type turnReviewState struct {
	StructuredCitations      map[string]structuredCitationObservation
	StructuredCitationCalls  map[string][]string
	StructuredCitationNonce  string
	AdditionsCache           map[string]string
	Repairs                  int
	Checks                   int
	Last                     candidateDecision
	ReadEvidence             *reviewEvidenceHandoff
	ClosedStreams            map[string]bool
	Committed                bool
	TerminalStreamID         string
	TerminalTruncated        bool
	DeferTerminalClose       bool
	Query                    string
	Conversation             []chat.Message
	TaskConversation         []chat.Message
	TaskConversationCaptured bool
	// Latest numerically grounded semantic fallback, or a source-reviewed
	// numeric-warning delivery. The latter retains the exact checked envelope
	// separately from its deterministic decoded presentation.
	UnverifiedCandidate        string
	UnverifiedCheckedCandidate string
	UnverifiedUserNotice       string
	UnverifiedDecision         candidateDecision
	NumericWarningAllowed      bool
	UnverifiedCitationDelivery *unverifiedCitationDeliveryBinding
	NumericUnverifiedDecision  candidateDecision
	NumericUnverifiedDelivery  *numericUnverifiedDeliveryBinding
	CitationUnverifiedDelivery *citationUnverifiedDeliveryBinding
	StructuredResultReview     *structuredResultReviewTurn
	DiscoveryReview            *discoveryDeliveryReviewTurn
	FinalCoverageReadAttempted bool
	CoverageDraft              candidateDecision
	CoverageDraftFacts         []string
	CoverageDraftDeliveryHash  string
	TableComparisonDelivery    tableComparisonDeliveryReceipt
}

// Preserve the existing two numeric and two semantic repair allowances while
// preventing exceptional synthesis from opening another independent budget.
const maxTurnCandidateRepairs = 4

// Only explicit runtime configuration changes the quality policy. A model's
// reasoning depth or task plan cannot silently promote standard back to strict.
// Empty and unknown legacy values keep the existing strict behavior.
func (e *AgentEngine) standardAnswerReview() bool {
	return e != nil && e.config != nil && e.config.AnswerReviewMode == "standard"
}

func (e *AgentEngine) standardTextDelivery(state *types.AgentState) bool {
	return e.standardAnswerReview() && !e.needsDeliveryReview(state) && !hasWorkspaceExecution(state)
}

func (e *AgentEngine) spendCandidateRepair() bool {
	if e.turnReview.Repairs >= maxTurnCandidateRepairs {
		return false
	}
	e.turnReview.Repairs++
	return true
}

func candidateTextDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func reviewDigest(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// checkCandidate is shared by ordinary completion, stalled-turn recovery and
// exceptional synthesis. Numeric provenance precedes semantic review everywhere.
// forceReview retains the existing exceptional-path completeness policy.
func (e *AgentEngine) checkCandidate(ctx context.Context, query, candidate, reasoning string, state *types.AgentState, conversation []chat.Message, forceReview bool) (decision candidateDecision) {
	candidate = stripCoverageAnswer(candidate)
	e.turnReview.ReadEvidence = nil
	e.turnReview.Query = query
	e.turnReview.Conversation = append([]chat.Message(nil), conversation...)
	// Source exposure retains the real runtime transcript independently of
	// the human task snapshot. The latter must survive executeLoop cleanup so
	// completion/persistence never hashes later tool messages as human intent.
	e.turnReview.TaskConversation = e.semanticReviewConversation(conversation)
	e.turnReview.TaskConversationCaptured = true
	e.answerReviewFeedback = ""
	e.answerReviewUserNotice = ""
	e.answerReviewAction = ""
	decision = candidateDecision{Disposition: candidateAccepted,
		Candidate:              candidate,
		CandidateHash:          candidateTextDigest(candidate),
		TaskHash:               e.candidateTaskDigest(query, conversation),
		EvidenceHash:           reviewDigest(e.sourceConsistencyMessages(state, conversation)),
		SourceScopeHash:        e.unverifiedSourceScopeDigest(ctx),
		StandardReview:         e.standardAnswerReview(),
		StructuredResultReview: e.structuredResultReviewEnabled(),
	}
	defer func() {
		e.turnReview.Checks++
		e.turnReview.Last = decision
		e.rememberNumericUnverifiedCandidate(ctx, decision, state)
		if decision.ReasonCode == "evidence_completion_gap" || decision.ReasonCode == "evidence_completion_exhausted" ||
			decision.ReasonCode == "discovery_coverage_recovery" || decision.Disposition == candidateNumericFailure {
			e.rememberCoverageDraft(ctx, decision, reasoning, state)
		}
		if e.needsSourceConsistencyCheck(state) && !suspectAnswerFragment(candidate, reasoning) {
			// Only candidates judged by the source critic qualify; completeness
			// checks of fragments or non-sourced replies never become deliveries.
			e.rememberUnverifiedCandidate(candidate, decision)
		}
		common.PipelineInfo(ctx, "Agent", "candidate_check", map[string]interface{}{
			"disposition": decision.Disposition, "candidate_hash": decision.CandidateHash,
			"task_revision": decision.TaskHash, "evidence_revision": decision.EvidenceHash,
			"checks": e.turnReview.Checks, "repairs": e.turnReview.Repairs,
			"reason_code": decision.ReasonCode, "source_review_attempted": decision.SourceReviewAttempted,
			"citation_issue_counts": decision.CitationIssueCounts, "unsupported_count": len(decision.Unsupported),
			"repeated_unsupported_count": len(decision.RepeatedUnsupported),
			"feedback_hash":              candidateTextDigest(e.answerReviewFeedback),
		})
	}()
	if ctx.Err() != nil {
		decision.Disposition = candidateProtocolFailure
		decision.Err = ctx.Err()
		decision.ReasonCode = "context_cancelled"
		return
	}
	// A runtime view reference is a compact presentation request, never a
	// completed answer or source approval. Expand its private immutable cells
	// before the unchanged numeric, scope, citation and full-task checks.
	expanded, views, expansionErr := e.expandDocumentTableCandidate(candidate, state)
	if expansionErr != nil {
		decision.Candidate, decision.CandidateHash = "", candidateTextDigest("")
		decision.Disposition, decision.Err = candidateProtocolFailure, expansionErr
		decision.ReasonCode = "document_view_binding_invalid"
		e.answerReviewFeedback = "表格视图引用未通过本轮真实执行、来源版本或范围校验。请根据当前完整要求重新读取并选择实际表格，不要重抄或猜测视图内容。"
		e.answerReviewAction = reviewCollectEvidence
		return
	}
	if views > 0 {
		candidate = expanded
		decision.Candidate, decision.CandidateHash = candidate, candidateTextDigest(candidate)
		common.PipelineInfo(ctx, "Agent", "document_table_view_expanded", map[string]interface{}{
			"views": views, "candidate_hash": decision.CandidateHash,
		})
	}
	if !e.candidateEvidenceBudgetFits(candidate, state) {
		decision.ReasonCode = "evidence_storage_budget"
		e.answerReviewFeedback = "所选证据超出单条消息存储预算。使用同一次真实返回结果的聚合引用，保留必要原格和计算依据；不能丢弃引用后把未保存证据宣布为已完成。"
		e.answerReviewAction = reviewReviseAnswer
		decision.Disposition = candidateRevise
		return
	}
	if strings.TrimSpace(candidate) == "" {
		decision.ReasonCode = "empty_candidate"
		decision.Disposition = candidateRevise
		return
	}
	// Only the runtime's private receipt for this exact task, result and saved
	// Markdown can reuse the independent pre-render semantic source check.
	// Public success/accepted fields and a merely existing image cannot do so.
	if narrowed, ok := e.narrowVerifiedChartCandidate(query, candidate, state, conversation); ok {
		candidate = narrowed
		decision.Candidate, decision.CandidateHash = candidate, candidateTextDigest(candidate)
		common.PipelineInfo(ctx, "Agent", "chart_delivery_normalized", map[string]interface{}{
			"candidate_hash": decision.CandidateHash, "reason": "verified_complete_chart_only",
		})
	}
	if e.isVerifiedChartDelivery(query, candidate, state, conversation) {
		common.PipelineInfo(ctx, "Agent", "source_review_skipped", map[string]interface{}{"reason": "verified_chart_delivery"})
		return
	}
	candidate = e.selectCandidateAdditions(ctx, query, candidate, state, conversation)
	decision.Candidate, decision.CandidateHash = candidate, candidateTextDigest(candidate)
	if e.standardAnswerReview() && e.candidateCitationsInvalid(candidate, state, conversation) {
		decision = e.citationIdentityFailure(decision, state, conversation)
		return
	}
	if needsStructuredAnswerReviewForQuery(query, state.RoundSteps, conversation, candidate) {
		evidence := e.answerNumericEvidence(candidate, state.RoundSteps, conversation)
		// The exceptional chart delivery inserts the exact coverage caption
		// from a current receipt. Its range can name months with no result
		// rows. Keep it in the semantic review and check every model number.
		numericText := candidate
		if chartAnswer, ok := ctx.Value(boundedChartAnswerKey{}).(*boundedChartAnswer); ok {
			numericText = strings.TrimPrefix(candidate, chartAnswer.markdown+"\n\n")
		}
		decision.Unsupported = ungroundedStructuredNumbers(numericText, evidence, state.RoundSteps)
		decision.NumericFeedback = buildNumericValidationFeedback(candidate, decision.Unsupported)
		decision.RepeatedUnsupported = repeatedUnsupportedTokens(decision.Unsupported, e.turnReview.Last.Unsupported)
		if len(decision.Unsupported) > 0 && (e.standardAnswerReview() || !e.turnReview.NumericWarningAllowed && e.turnReview.Repairs < maxTurnCandidateRepairs) {
			decision.Disposition = candidateNumericFailure
			decision.ReasonCode = "numeric_tokens_unmatched"
			return
		}
		ctx = withNumericValidationFeedback(ctx, decision.NumericFeedback)
	}
	if e.standardTextDelivery(state) {
		decision = e.checkDiscoveryOnlyDelivery(ctx, query, decision, state, conversation)
		if decision.Disposition != candidateAccepted {
			return decision
		}
		// Actual tools have already enforced permissions and source identities;
		// immutable view, citation and numeric checks above remain mandatory.
		// The explicit structured-result policy adds at most one bounded conflict
		// check; ordinary answers still bypass the independent source critic.
		// Action/file delivery retains its existing execution/completeness check.
		decision = e.checkStructuredResultDelivery(ctx, query, decision, state, conversation)
		if decision.StructuredResultReviewStatus == "" {
			common.PipelineInfo(ctx, "Agent", "source_review_skipped", map[string]interface{}{"reason": "standard_text_delivery"})
		}
		return
	}
	// After the bounded numeric repairs, the matcher remains diagnostic. The
	// source model judges the complete task and actual evidence, including a
	// visible caution, rather than rejecting all supported results for an extra
	// number. This exception never changes ordinary direct-lookup policy.
	warning := len(decision.Unsupported) > 0
	if warning && !e.needsSourceConsistencyCheck(state) {
		decision.Disposition = candidateNumericFailure
		decision.ReasonCode = "numeric_tokens_unmatched"
		return
	}
	if warning {
		ctx = context.WithValue(ctx, numericWarningNoticeKey{}, numericWarningNotice(decision.Unsupported))
	}
	// Reasoning depth selects model effort, never a weaker quality contract.
	// A real citation proves identity only; directly transcribing a document
	// still requires checking requested coverage, scope and meaning. The private
	// full-task chart receipt above already supplies that independent review.
	decision.SourceReviewAttempted = true
	var repair bool
	if forceReview {
		repair, decision.Err = e.evaluateAnswerCompleteness(ctx, query, candidate, state, conversation)
	} else {
		repair, decision.Err = e.answerNeedsRepair(ctx, query, candidate, reasoning, state, conversation)
	}
	if decision.Err != nil {
		decision.Disposition = candidateProtocolFailure
		decision.ReasonCode = "source_review_protocol_failure"
		if e.needsSourceConsistencyCheck(state) && e.candidateCitationsInvalid(candidate, state, conversation) {
			// An unavailable critic cannot convert an invalid identity into the
			// older general semantic fallback either.
			decision.ReasonCode = "citation_identity_invalid"
		}
	} else if repair {
		decision.ReasonCode = "source_review_revision"
		decision.Disposition = candidateRevise
		if e.answerReviewAction == reviewCollectEvidence {
			decision.Disposition = candidateNeedSource
		}
		// Preserve strict review's semantic/coverage check and its existing
		// budget, then make an identity rejection actionable at the same outlet.
		if e.needsSourceConsistencyCheck(state) && e.candidateCitationsInvalid(candidate, state, conversation) {
			decision = e.citationIdentityFailure(decision, state, conversation)
		}
	} else if warning && e.needsSourceConsistencyCheck(state) {
		decision.Disposition = candidateNumericWarning
	}
	return
}

// hasExecutedPDFAnalysis recognizes only current canonical results from the
// actual authorized PDF SQL operation. It does not inspect the question,
// candidate, SQL text, displayed Output or the planner's proposed tools.
func hasExecutedPDFAnalysis(steps []types.AgentStep) bool {
	for _, step := range steps {
		for _, call := range step.ToolCalls {
			if call.Result == nil || !call.Result.Success {
				continue
			}
			data := call.Result.Data
			if data["kind"] != "structured_analysis" || data["status"] != "ok" || data["source_kind"] != "pdf_table" {
				continue
			}
			switch call.ExecutionName() {
			case "structured_query_pdf_table":
				return true
			case "structured_data":
				if call.ExecutionArgs()["operation"] == "query_pdf_table" {
					return true
				}
			}
		}
	}
	return false
}

// Recheck version identity at the delivery boundary. No model call is reused
// when the human task or actual observations changed after its approval.
func (e *AgentEngine) candidateApprovalCurrent(candidate string, state *types.AgentState) bool {
	d := e.turnReview.Last
	return d.Disposition == candidateAccepted && e.candidateDecisionCurrent(d, candidate, state)
}

func (e *AgentEngine) candidateDecisionCurrent(d candidateDecision, candidate string, state *types.AgentState) bool {
	taskConversation := e.turnReview.Conversation
	if e.turnReview.TaskConversationCaptured {
		taskConversation = e.turnReview.TaskConversation
	}
	return d.CandidateHash == candidateTextDigest(candidate) &&
		d.StandardReview == e.standardAnswerReview() &&
		d.StructuredResultReview == e.structuredResultReviewEnabled() &&
		d.TaskHash == e.candidateTaskDigest(e.turnReview.Query, taskConversation) &&
		d.EvidenceHash == reviewDigest(e.sourceConsistencyMessages(state, e.turnReview.Conversation))
}

// rememberUnverifiedCandidate keeps the newest candidate that reached semantic
// review and was not approved, or one approved only for numeric-warning
// delivery. Rejected numeric candidates and empty candidates are never kept.
func (e *AgentEngine) rememberUnverifiedCandidate(candidate string, decision candidateDecision) {
	if decision.ReasonCode == "citation_identity_invalid" || !decision.SourceReviewAttempted || strings.TrimSpace(candidate) == "" || (len(decision.Unsupported) > 0 && decision.Disposition != candidateNumericWarning) {
		return
	}
	switch decision.Disposition {
	case candidateRevise, candidateNeedSource, candidateProtocolFailure, candidateNumericWarning:
	default:
		return
	}
	e.turnReview.UnverifiedCheckedCandidate = candidate
	if decoded, ok := decodeReviewedAnswerEnvelope(candidate); ok && strings.TrimSpace(decoded) != "" {
		candidate = decoded
	}
	e.turnReview.UnverifiedCandidate = candidate
	e.turnReview.UnverifiedUserNotice = strings.TrimSpace(e.answerReviewUserNotice)
	e.turnReview.UnverifiedDecision = decision
	if decision.Disposition == candidateNumericWarning {
		e.turnReview.UnverifiedUserNotice = numericWarningNotice(decision.Unsupported)
	}
}

type numericWarningNoticeKey struct{}

func visibleNumericWarningTokens(tokens []string) []string {
	visible := []string{}
	for _, token := range tokens {
		if len(visible) == 4 {
			break
		}
		if len(token) <= 24 && !strings.ContainsAny(token, "\r\n<>") {
			visible = append(visible, token)
		}
	}
	return visible
}

func numericWarningNotice(tokens []string) string {
	visible := visibleNumericWarningTokens(tokens)
	if len(visible) == 0 {
		return "部分数字尚未完全核实，请结合引用的原始资料使用。"
	}
	return "其中“" + strings.Join(visible, "、") + "”尚未完成逐项核实，请结合引用的原始资料使用。"
}

func (e *AgentEngine) numericWarningDelivery(candidate string, state *types.AgentState) (string, bool) {
	d := e.turnReview.UnverifiedDecision
	base := e.turnReview.UnverifiedCandidate
	if d.Disposition != candidateNumericWarning || !e.candidateDecisionCurrent(d, e.turnReview.UnverifiedCheckedCandidate, state) {
		return "", false
	}
	delivery := base + "\n\n> 提示：" + e.turnReview.UnverifiedUserNotice
	if candidate != base && candidate != e.turnReview.UnverifiedCheckedCandidate && candidate != delivery {
		return "", false
	}
	return delivery, true
}

// User steering preserves actual evidence and the shared repair allowance,
// but none of the previous task's draft/approval caches may answer the new one.
func (e *AgentEngine) invalidateCandidateTask() {
	e.turnReview.Last = candidateDecision{}
	e.turnReview.TaskConversation = nil
	e.turnReview.TaskConversationCaptured = false
	e.turnReview.ReadEvidence = nil
	e.turnReview.UnverifiedCandidate = ""
	e.turnReview.UnverifiedCheckedCandidate = ""
	e.turnReview.UnverifiedUserNotice = ""
	e.turnReview.UnverifiedDecision = candidateDecision{}
	e.turnReview.UnverifiedCitationDelivery = nil
	e.turnReview.NumericUnverifiedDecision = candidateDecision{}
	e.turnReview.NumericUnverifiedDelivery = nil
	e.turnReview.CitationUnverifiedDelivery = nil
	e.turnReview.CoverageDraft = candidateDecision{}
	e.turnReview.CoverageDraftFacts = nil
	e.turnReview.CoverageDraftDeliveryHash = ""
	e.turnReview.TableComparisonDelivery = tableComparisonDeliveryReceipt{}
}

func (e *AgentEngine) rememberCoverageDraft(ctx context.Context, decision candidateDecision, reasoning string, state *types.AgentState) {
	if !e.evidenceCompletionEnabled() || !e.evidenceCompletionEligible() || decision.Err != nil ||
		suspectAnswerFragment(decision.Candidate, reasoning) {
		return
	}
	anchors, unmatched, comparedSources := e.coverageDraftGrounding(ctx, decision.Candidate, state)
	if len(decision.Unsupported) > 0 && (!comparedSources || slices.ContainsFunc(decision.Unsupported, func(token string) bool {
		return !slices.ContainsFunc(unmatched, func(item numericOccurrence) bool { return token == item.Token })
	})) {
		return
	}
	if comparedSources && len(unmatched) > 0 {
		// A comparison draft can add a calculation that happened to match a
		// number elsewhere. Retain the independently supported comparison and
		// its citations when the affected details are in separate paragraphs.
		qualified := coverageDraftSupportedParagraphs(decision.Candidate, unmatched)
		qualifiedAnchors, remaining, compared := e.coverageDraftGrounding(ctx, qualified, state)
		if qualified != "" && len(remaining) == 0 && compared &&
			len(ungroundedStructuredNumbers(qualified, e.answerNumericEvidence(qualified, state.RoundSteps, e.turnReview.Conversation), state.RoundSteps)) == 0 {
			decision.Candidate, decision.CandidateHash = qualified, candidateTextDigest(qualified)
			anchors, unmatched = qualifiedAnchors, nil
		}
	}
	if len(anchors) == 0 || len(unmatched) != 0 {
		return
	}
	prior := e.turnReview.CoverageDraft
	if prior.Candidate != "" && prior.TaskHash == decision.TaskHash && prior.SourceScopeHash == decision.SourceScopeHash &&
		len(anchors) <= len(e.turnReview.CoverageDraftFacts) {
		// A no-read rewrite can shorten a candidate without substantiating more
		// of the task. Preserve the original facts across that recovery round.
		if _, current := e.currentCoverageDraft(ctx, state); current {
			return
		}
	}
	e.turnReview.CoverageDraft, e.turnReview.CoverageDraftFacts = decision, anchors
}

func (e *AgentEngine) currentCoverageDraft(ctx context.Context, state *types.AgentState) (string, bool) {
	draft := e.turnReview.CoverageDraft
	if draft.Candidate == "" || !e.evidenceCompletionEnabled() || !e.evidenceCompletionEligible() ||
		draft.TaskHash != e.candidateTaskDigest(e.turnReview.Query, e.turnReview.TaskConversation) ||
		draft.SourceScopeHash != e.unverifiedSourceScopeDigest(ctx) ||
		draft.CandidateHash != candidateTextDigest(draft.Candidate) ||
		draft.StructuredResultReview != e.structuredResultReviewEnabled() {
		return "", false
	}
	anchors := e.coverageDraftAnchors(ctx, draft.Candidate, state)
	if len(anchors) == 0 || slices.ContainsFunc(e.turnReview.CoverageDraftFacts, func(ref string) bool { return !slices.Contains(anchors, ref) }) {
		return "", false
	}
	return draft.Candidate, true
}

func (e *AgentEngine) bindCoverageDraftDelivery(base, final string) {
	if base == e.turnReview.CoverageDraft.Candidate && final != "" {
		e.turnReview.CoverageDraftDeliveryHash = candidateTextDigest(final)
	}
}

func (e *AgentEngine) coverageDraftDeliveryCurrent(ctx context.Context, state *types.AgentState) bool {
	if state == nil || e.turnReview.CoverageDraftDeliveryHash == "" || e.turnReview.CoverageDraftDeliveryHash != candidateTextDigest(state.FinalAnswer) {
		return false
	}
	base, ok := e.currentCoverageDraft(ctx, state)
	if !ok || e.candidateCitationsInvalid(state.FinalAnswer, state, e.turnReview.Conversation) {
		return false
	}
	return strings.HasPrefix(state.FinalAnswer, base) || e.terminalCoverageDelivery(ctx, state, false).render(false, base) == state.FinalAnswer
}

// A completion association is optional. For a retained partial answer require
// current native evidence for the draft, through its real citations or named
// field and observation. The user's question and model tool transcript are
// deliberately excluded from numeric grounding at this outlet.
func (e *AgentEngine) coverageDraftAnchors(ctx context.Context, candidate string, state *types.AgentState) []string {
	anchors, unsupported, _ := e.coverageDraftGrounding(ctx, candidate, state)
	if len(unsupported) > 0 {
		return nil
	}
	return anchors
}

func (e *AgentEngine) coverageDraftGrounding(ctx context.Context, candidate string, state *types.AgentState) ([]string, []numericOccurrence, bool) {
	if state == nil || !e.standardTextDelivery(state) || !e.candidateEvidenceBudgetFits(candidate, state) ||
		strings.TrimSpace(candidate) == "" || terminalCandidateFallback(candidate) ||
		e.candidateCitationsInvalid(candidate, state, e.turnReview.Conversation) {
		return nil, nil, false
	}
	if e.structuredResultReviewEnabled() && hasExecutedPDFAnalysis(state.RoundSteps) {
		return nil, nil, false // Preserve the configured PDF result conflict check.
	}
	turn := &e.evidenceCompletion
	steps := state.RoundSteps[min(turn.startStep, len(state.RoundSteps)):]
	record, _ := e.projectCurrentTaskCoverage(state, turn.startStep, coverageRecord{
		Revision: turn.revision, AuthScope: e.unverifiedSourceScopeDigest(ctx), Bindings: turn.record.Bindings,
	})
	targets := e.evidenceCompletionTargets()
	compare := slices.ContainsFunc(targets, func(target taskCoverageTarget) bool {
		return target.VersionTask == "compare_sources" || target.VersionTask == "compare_versions"
	})
	for _, target := range targets {
		record.Evidence = append(record.Evidence, nativeCoverageGapFacts(target, record)...)
	}
	citedChunks := uniqueCandidateCitationIDs(candidate)
	citedResults := uniqueCandidateEvidenceIDs(candidate)
	candidateText := strings.ToLower(candidate)
	lines := coverageDraftLines(candidate)
	safeCalls := map[string]bool{}
	sourceIDs := map[[2]string]bool{}
	var anchors []string
	var values []float64
	for _, item := range record.Evidence {
		if !item.Bound || !coverageCurrentEvidence(record, item) || !e.terminalCoverageScopeAllowed(targets, item) ||
			!e.coverageScopeAllows(item.KnowledgeBaseID, item.KnowledgeID) && item.SourceDomain == "structured" {
			continue
		}
		associated := item.CitationID != "" && slices.Contains(citedResults, item.CitationID)
		metricMentioned := item.Metric != "" && strings.Contains(candidateText, strings.ToLower(item.Metric))
		switch item.Kind {
		case "body":
			associated = item.ChunkID != "" && slices.Contains(citedChunks, item.ChunkID)
		case "gap":
			associated = associated || metricMentioned && (coverageTextHasDate(candidate, item.LastValidDate) ||
				coverageTextHasDate(candidate, item.PeriodFrom)) ||
				item.SourceTitle != "" && strings.Contains(candidateText, strings.ToLower(item.SourceTitle)) &&
					slices.ContainsFunc(targets, func(target taskCoverageTarget) bool {
						return e.coverageMatchesPrimaryReference(target, item) && slices.ContainsFunc(record.Bindings, func(binding coverageBinding) bool {
							return binding.GoalIndex == target.GoalIndex && binding.Role == "primary" && coverageSameSource(binding, item) &&
								binding.SeriesID == item.SeriesID
						})
					})
		case "cell", "last_valid", "derived":
			cited := associated
			associated = false
			value, numeric := coverageDraftNumber(item.Raw)
			if !numeric || !coverageDraftValueInScope(targets, item, candidate) {
				continue
			}
			if cited || metricMentioned && item.SeriesID != "" && (item.Operation == "query_series" || item.Kind == "last_valid") {
				metric := strings.ToLower(item.Metric)
				associated = slices.ContainsFunc(lines, func(line coverageDraftLine) bool {
					return (cited && slices.Contains(line.citations, item.CitationID) ||
						metricMentioned && strings.Contains(line.text, metric)) &&
						slices.ContainsFunc(line.numbers, func(number coverageDraftNumeric) bool {
							return math.Abs(number.value-value) <= number.tolerance
						})
				})
			}
			if associated {
				values = append(values, value)
			}
		default:
			continue
		}
		if associated {
			if item.Operation == "query_series" || item.Kind == "body" || item.Kind == "derived" && item.CitationID != "" {
				safeCalls[item.ToolCallID] = true
			}
			anchor := reviewDigest([]string{coverageFactRef(item), item.SnapshotHash, item.SourceID, item.SeriesID})
			if !slices.Contains(anchors, anchor) {
				anchors = append(anchors, anchor)
			}
			if item.Kind != "gap" {
				source := item.KnowledgeID
				if source == "" {
					source = item.SourceID
				}
				sourceIDs[[2]string{item.KnowledgeBaseID, source}] = true
			}
		}
	}
	if len(anchors) == 0 {
		return nil, nil, false
	}
	var selected []types.AgentStep
	for _, step := range steps {
		var calls []types.ToolCall
		for _, call := range step.ToolCalls {
			if safeCalls[call.ID] || len(citedChunks) > 0 &&
				call.ExecutionName() == agenttools.ToolReadDocument &&
				agenttools.IsCurrentDocumentRead(e.toolRegistry, call.Result) &&
				len(citedImageObservations(call, coverageCitedChunks(citedChunks))) > 0 {
				calls = append(calls, call)
			}
		}
		selected = append(selected, types.AgentStep{ToolCalls: calls})
	}
	evidence := e.answerNumericEvidence(candidate, selected)
	evidence = append(evidence, chat.Message{Role: "tool", NumericObservations: &chat.NumericObservations{Values: values}})
	unmatched := ungroundedStructuredNumberOccurrencesWithEmptyEvidence(candidate, evidence, true, selected)
	return anchors, unmatched, compare && len(sourceIDs) > 1 && len(values) > 0
}

func coverageDraftValueInScope(targets []taskCoverageTarget, item coverageEvidence, candidate string) bool {
	if len(targets) == 0 {
		return true
	}
	return slices.ContainsFunc(targets, func(target taskCoverageTarget) bool {
		if target.SourceTime == "latest_observation" {
			return item.LatestInScope || item.Kind == "last_valid" && coverageTextHasDate(candidate, item.Date)
		}
		from, to, _ := coverageTargetDateRange(target)
		if coverageDay(from) == "" || coverageDay(to) == "" {
			return true
		}
		if item.Kind == "last_valid" && coverageTextHasDate(candidate, item.Date) {
			return true // A separately dated boundary value keeps its own time.
		}
		switch item.Kind {
		case "derived":
			return item.PeriodFrom == from && item.PeriodTo == to
		default:
			return item.Date >= from && item.Date <= to
		}
	})
}

func coverageDraftSupportedParagraphs(candidate string, unmatched []numericOccurrence) string {
	var paragraphs []string
	for start := 0; start < len(candidate); {
		paragraph, after, _ := strings.Cut(candidate[start:], "\n\n")
		end := start + len(paragraph)
		omitted := slices.ContainsFunc(unmatched, func(number numericOccurrence) bool {
			return number.Start >= start && number.Start < end
		})
		if omitted && start == 0 {
			return ""
		}
		if !omitted && strings.TrimSpace(paragraph) != "" {
			paragraphs = append(paragraphs, paragraph)
		}
		start = len(candidate) - len(after)
	}
	return strings.Join(paragraphs, "\n\n")
}

func coverageCitedChunks(ids []string) map[string]bool {
	cited := make(map[string]bool, len(ids))
	for _, id := range ids {
		cited[id] = true
	}
	return cited
}

func coverageDraftNumber(raw any) (float64, bool) {
	number, ok := raw.(json.Number)
	if !ok {
		if text, ok := raw.(string); ok {
			number = json.Number(text)
		}
	}
	value, err := number.Float64()
	return value, err == nil
}

type coverageDraftNumeric struct {
	value, tolerance float64
}

type coverageDraftLine struct {
	text      string
	citations []string
	numbers   []coverageDraftNumeric
}

func coverageDraftLines(candidate string) []coverageDraftLine {
	var lines []coverageDraftLine
	for _, body := range strings.Split(candidate, "\n") {
		line := coverageDraftLine{text: strings.ToLower(body), citations: uniqueCandidateEvidenceIDs(body)}
		for _, token := range proseNumber.FindAllString(documentNumericProse(body), -1) {
			token = strings.ReplaceAll(strings.ReplaceAll(token, ",", ""), "−", "-")
			number, err := strconv.ParseFloat(token, 64)
			if err != nil {
				continue
			}
			_, fraction, _ := strings.Cut(token, ".")
			line.numbers = append(line.numbers, coverageDraftNumeric{number, 0.500001 * math.Pow10(-len(fraction))})
		}
		if len(line.numbers) != 0 {
			lines = append(lines, line)
		}
	}
	return lines
}

// A warning permits delivery after review failure, never reuse of another
// task, source version, scope or candidate. This is distinct from approval.
func (e *AgentEngine) unverifiedCandidateCurrent(ctx context.Context, state *types.AgentState) bool {
	if e == nil || e.config == nil || state == nil {
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
	return strings.TrimSpace(base) != "" && strings.TrimSpace(base) == strings.TrimSpace(e.turnReview.UnverifiedCandidate)
}

func (e *AgentEngine) currentUnverifiedDelivery(ctx context.Context, state *types.AgentState) (string, bool) {
	if !e.unverifiedCandidateCurrent(ctx, state) {
		return "", false
	}
	return e.unverifiedDelivery()
}

// unverifiedDelivery is the delivery policy chosen by the user (2026-10-20):
// when semantic review fails or keeps rejecting, deliver the latest
// numerically grounded candidate with an explicit notice instead of nothing.
// Reviewer repair advice is written for the author and may itself be wrong, so
// it is never shown; only the reviewer's separate user-facing caution is.
func (e *AgentEngine) unverifiedDelivery() (string, bool) {
	if e.turnReview.UnverifiedDecision.Disposition == candidateNumericWarning {
		return "", false
	}
	candidate := strings.TrimSpace(e.turnReview.UnverifiedCandidate)
	if candidate == "" {
		return "", false
	}
	notice := "\n\n> 提示：本回答未能完成自动核验，部分表述可能与原始资料有出入，请以引用的原文、原图或数据为准。"
	if caution := strings.TrimSpace(e.turnReview.UnverifiedUserNotice); caution != "" {
		notice = "\n\n> 提示：本回答未能完成自动核验。" + caution
	}
	return candidate + notice, true
}

func (e *AgentEngine) taskReasoningDepth() string {
	if e == nil || e.taskPlan == nil {
		return ""
	}
	return e.taskPlan.ReasoningDepth
}
